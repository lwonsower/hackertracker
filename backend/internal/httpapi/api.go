// Package httpapi exposes the ingest pipeline over HTTP.
//
// Two authentication styles live here, deliberately:
//
//   - Everything a person does is behind a session cookie, and every handler
//     opens an account scope before touching data.
//   - POST /api/ingest/{token} is authenticated by the token itself, because
//     the caller is a script or a Zapier step, not a browser. The token
//     resolves to a source account, which names the scope to open.
package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lwonsower/hackertracker/backend/internal/auth"
	"github.com/lwonsower/hackertracker/backend/internal/connectors/github"
	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/ingest"
	"github.com/lwonsower/hackertracker/backend/internal/secrets"
	"github.com/lwonsower/hackertracker/backend/internal/store"
	"github.com/lwonsower/hackertracker/backend/internal/syncer"
)

const (
	maxBodyBytes  = 1 << 20 // 1 MiB
	maxBatchItems = 500
	defaultWindow = 90 * 24 * time.Hour
)

type API struct {
	db            *store.DB
	pipeline      *ingest.Pipeline
	runner        *syncer.Runner
	secrets       secrets.Resolver
	githubBaseURL string
}

func New(db *store.DB, p *ingest.Pipeline, run *syncer.Runner, resolver secrets.Resolver, githubBaseURL string) *API {
	return &API{db: db, pipeline: p, runner: run, secrets: resolver, githubBaseURL: githubBaseURL}
}

// Routes registers handlers. `protect` wraps everything that acts on behalf of
// a signed-in person; the ingest endpoint is left out because it carries its
// own credential.
func (a *API) Routes(mux *http.ServeMux, protect func(http.Handler) http.Handler) {
	mux.HandleFunc("POST /api/ingest/{token}", a.handleIngest)

	guarded := map[string]http.HandlerFunc{
		"GET /api/me":                       a.handleMe,
		"POST /api/events":                  a.handleCreateEvent,
		"GET /api/events":                   a.handleListEvents,
		"GET /api/source-accounts":          a.handleListSourceAccounts,
		"POST /api/source-accounts":         a.handleCreateSourceAccount,
		"POST /api/source-accounts/{id}/sync": a.handleSync,
	}
	for pattern, handler := range guarded {
		mux.Handle(pattern, protect(handler))
	}
}

// scope runs fn inside the signed-in user's account scope. Handlers cannot
// reach data any other way, which is what makes "forgot to filter by account"
// unrepresentable rather than merely discouraged.
func (a *API) scope(w http.ResponseWriter, r *http.Request, fn func(*store.Store) error) bool {
	user, ok := auth.UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return false
	}
	if err := a.db.Scope(r.Context(), user.AccountID, fn); err != nil {
		var invalid core.ValidationError
		switch {
		case errors.As(err, &invalid):
			writeError(w, http.StatusBadRequest, invalid.Error())
			return false
		case errors.Is(err, store.ErrNotFound):
			// Inside a scope, "belongs to someone else" and "does not exist"
			// are the same answer, which is the point.
			writeError(w, http.StatusNotFound, "not found")
			return false
		}
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		writeError(w, http.StatusInternalServerError, "something went wrong")
		return false
	}
	return true
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

// ── generic ingest ───────────────────────────────────────────────────────

func (a *API) handleIngest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	// Unscoped by necessity: this lookup is what establishes the scope.
	acct, err := a.db.SourceAccountByIngestToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		// Nothing is logged for an unknown token: there is no account to
		// attribute it to, and writing it anywhere would mean storing an
		// unauthenticated stranger's payload.
		writeError(w, http.StatusNotFound, "unknown ingest endpoint")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve ingest endpoint")
		return
	}

	items, err := splitBatch(body)
	if err != nil {
		a.logIngest(ctx, acct, body, "rejected", err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	raws := make([]core.RawRecord, 0, len(items))
	for i, item := range items {
		// external_id is extracted here even though the normaliser parses the
		// payload again. Whoever *produces* a raw record identifies it — as
		// true for a pull connector reading an API's id field as it is here.
		var env core.Envelope
		if err := json.Unmarshal(item, &env); err != nil {
			a.rejectBatch(ctx, w, acct, body, fmt.Sprintf("item %d: not a valid ingest envelope: %v", i, err))
			return
		}
		env, err := env.Normalize()
		if err != nil {
			a.rejectBatch(ctx, w, acct, body, fmt.Sprintf("item %d: %s", i, err))
			return
		}
		raws = append(raws, core.RawRecord{ExternalID: env.ExternalID, Payload: item})
	}

	var res ingest.Result
	err = a.db.Scope(ctx, acct.AccountID, func(st *store.Store) error {
		var err error
		res, err = a.pipeline.Ingest(ctx, st, acct, raws)
		return err
	})
	if err != nil {
		var invalid core.ValidationError
		if errors.As(err, &invalid) {
			a.rejectBatch(ctx, w, acct, body, invalid.Error())
			return
		}
		log.Printf("ingest failed for %s: %v", acct.Label, err)
		a.logIngest(ctx, acct, body, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "could not store events")
		return
	}

	a.logIngest(ctx, acct, body, "accepted", "")
	writeJSON(w, http.StatusOK, res)
}

func (a *API) rejectBatch(ctx context.Context, w http.ResponseWriter, acct store.SourceAccount, body []byte, msg string) {
	a.logIngest(ctx, acct, body, "rejected", msg)
	writeError(w, http.StatusBadRequest, msg)
}

func (a *API) logIngest(ctx context.Context, acct store.SourceAccount, body []byte, status, msg string) {
	err := a.db.Scope(ctx, acct.AccountID, func(st *store.Store) error {
		return st.LogIngest(ctx, &acct.ID, body, status, msg)
	})
	if err != nil {
		log.Printf("could not write ingest log: %v", err)
	}
}

// splitBatch accepts either a single envelope or an array of them, so a caller
// can post one event now and backfill a thousand later without changing shape.
func splitBatch(body []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty request body")
	}
	if trimmed[0] != '[' {
		return []json.RawMessage{trimmed}, nil
	}

	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, fmt.Errorf("body is not valid JSON: %v", err)
	}
	if len(items) == 0 {
		return nil, errors.New("batch contains no items")
	}
	if len(items) > maxBatchItems {
		return nil, fmt.Errorf("batch of %d exceeds the %d item limit", len(items), maxBatchItems)
	}
	return items, nil
}

// ── manual entry ─────────────────────────────────────────────────────────

type manualEntry struct {
	Title      string     `json:"title"`
	Kind       string     `json:"kind"`
	OccurredAt *time.Time `json:"occurred_at"`
	URL        string     `json:"url"`
	Note       string     `json:"note"`
}

func (a *API) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var in manualEntry
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body as JSON")
		return
	}

	if in.Kind == "" {
		in.Kind = "note"
	}
	occurred := time.Now().UTC()
	if in.OccurredAt != nil {
		occurred = in.OccurredAt.UTC()
	}

	// The note is the *content* of a hand-written event, not commentary on
	// something captured elsewhere, so it belongs in the payload rather than in
	// the annotations table.
	payload := json.RawMessage(`{}`)
	if in.Note != "" {
		encoded, err := json.Marshal(map[string]string{"note": in.Note})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not encode note")
			return
		}
		payload = encoded
	}

	// Manual entry is not a special write path — it builds an envelope and
	// feeds the same pipeline as every webhook delivery.
	env := core.Envelope{
		ExternalID: uuid.NewString(),
		Kind:       in.Kind,
		Title:      in.Title,
		OccurredAt: occurred,
		URL:        in.URL,
		Payload:    payload,
	}
	if _, err := env.Normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := json.Marshal(env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not encode event")
		return
	}

	var res ingest.Result
	ok := a.scope(w, r, func(st *store.Store) error {
		// Created on demand rather than at boot: with many accounts there is no
		// single moment at which every manual source could be provisioned.
		manual, err := st.EnsureSourceAccount(r.Context(), "manual", "manual", "Manual entry")
		if err != nil {
			return err
		}
		res, err = a.pipeline.Ingest(r.Context(), st, manual, []core.RawRecord{{
			ExternalID: env.ExternalID,
			Payload:    raw,
		}})
		return err
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// ── reads ────────────────────────────────────────────────────────────────

func (a *API) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().UTC()

	from, err := parseTimeParam(q.Get("from"), now.Add(-defaultWindow))
	if err != nil {
		writeError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	to, err := parseTimeParam(q.Get("to"), now.Add(24*time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}

	limit := 0
	if raw := q.Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil {
			writeError(w, http.StatusBadRequest, "limit must be a number")
			return
		}
	}

	var events []store.EventRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		events, err = st.ListEvents(r.Context(), store.EventFilter{From: from, To: to, Limit: limit})
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *API) handleListSourceAccounts(w http.ResponseWriter, r *http.Request) {
	var accounts []store.SourceAccountRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		accounts, err = st.ListSourceAccounts(r.Context())
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source_accounts": accounts})
}

func (a *API) handleCreateSourceAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label          string `json:"label"`
		Source         string `json:"source"`
		Mode           string `json:"mode"`
		CredentialsRef string `json:"credentials_ref"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body as JSON")
		return
	}
	if in.Label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}
	if in.Source == "" {
		in.Source = "webhook"
	}
	if in.Mode == "pull" {
		a.createPullAccount(w, r, in.Source, in.Label, in.CredentialsRef)
		return
	}

	var acct store.SourceAccount
	var token string
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		acct, token, err = st.CreatePushEndpoint(r.Context(), in.Source, in.Label)
		return err
	}); !ok {
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// The token is returned exactly once — only its hash is stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"source_account": acct,
		"token":          token,
		"ingest_url":     fmt.Sprintf("%s://%s/api/ingest/%s", scheme, r.Host, token),
		"note":           "Store this token now; it is not recoverable.",
	})
}

// createPullAccount connects a polling source, verifying the credentials before
// storing anything.
//
// Verifying up front matters more than it looks: the characteristic GitHub
// failure is a token that authenticates fine but can see nothing, which
// produces syncs that succeed and return zero events.
func (a *API) createPullAccount(w http.ResponseWriter, r *http.Request, source, label, credentialsRef string) {
	if source != "github" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("no pull connector for source %q", source))
		return
	}
	if credentialsRef == "" {
		writeError(w, http.StatusBadRequest,
			"credentials_ref is required, e.g. env:GITHUB_TOKEN (a pointer to an environment variable, never the token itself)")
		return
	}

	token, err := a.secrets.Resolve(credentialsRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	login, err := github.WhoAmI(r.Context(), token, a.githubBaseURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	var acct store.SourceAccount
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		acct, err = st.CreatePullAccount(r.Context(), source, label, login, credentialsRef)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"source_account": acct, "login": login})
}

func (a *API) handleSync(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid source account id")
		return
	}

	// Loading through the scope is what stops one account syncing another's
	// source: an id belonging to someone else simply is not found.
	var acct store.SourceAccount
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		acct, err = st.SourceAccountByID(r.Context(), id)
		return err
	}); !ok {
		return
	}
	if acct.Mode != "pull" {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("%q is a %s source; only pull sources can be synced", acct.Label, acct.Mode))
		return
	}

	var opts syncer.Options
	var body struct {
		Since string `json:"since"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&body); err == nil && body.Since != "" {
		since, err := parseTimeParam(body.Since, time.Time{})
		if err != nil {
			writeError(w, http.StatusBadRequest, "since: "+err.Error())
			return
		}
		opts.Since = &since
	}

	// The request context governs the sync, so navigating away cancels it.
	// That is safe: progress is checkpointed to the cursor between rounds.
	report, err := a.runner.Sync(r.Context(), acct, opts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "report": report})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// ── helpers ──────────────────────────────────────────────────────────────

// parseTimeParam accepts a full RFC 3339 timestamp or a bare date, because
// review windows are naturally expressed as "2026-01-01".
func parseTimeParam(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("expected YYYY-MM-DD or an RFC 3339 timestamp")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("could not write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
