// Package httpapi exposes the ingest pipeline over HTTP.
//
// There is one generic endpoint (POST /api/ingest/{token}) rather than a
// bespoke route per integration. Anything that can make an HTTP request can
// feed the system: Zapier, n8n, a GitHub Action, a cron script, curl. Building
// a first-party connector is then an optimisation for tools worth zero-touch
// capture, not a prerequisite for using one.
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
	"os"
	"time"

	"github.com/google/uuid"

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
	store    *store.Store
	pipeline *ingest.Pipeline
	runner   *syncer.Runner
	manual   store.SourceAccount
}

func New(st *store.Store, p *ingest.Pipeline, run *syncer.Runner, manual store.SourceAccount) *API {
	return &API{store: st, pipeline: p, runner: run, manual: manual}
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/ingest/{token}", a.handleIngest)
	mux.HandleFunc("POST /api/events", a.handleCreateEvent)
	mux.HandleFunc("GET /api/events", a.handleListEvents)
	mux.HandleFunc("GET /api/source-accounts", a.handleListSourceAccounts)
	mux.HandleFunc("POST /api/source-accounts", a.handleCreateSourceAccount)
	mux.HandleFunc("POST /api/source-accounts/{id}/sync", a.handleSync)
}

// ── generic ingest ───────────────────────────────────────────────────────

func (a *API) handleIngest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	acct, err := a.store.SourceAccountByToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		// Logged with a nil account: a stream of these is how you find out a
		// stale endpoint is still being posted to.
		a.logIngest(ctx, nil, body, "rejected", "unknown ingest token")
		writeError(w, http.StatusNotFound, "unknown ingest endpoint")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not resolve ingest endpoint")
		return
	}

	items, err := splitBatch(body)
	if err != nil {
		a.logIngest(ctx, &acct.ID, body, "rejected", err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	raws := make([]core.RawRecord, 0, len(items))
	for i, item := range items {
		// The handler extracts external_id itself even though the normaliser
		// will parse the payload again. That is deliberate: whoever *produces*
		// a raw record is responsible for identifying it, which holds for a
		// pull connector reading an API's id field just as much as here.
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

	res, err := a.pipeline.Ingest(ctx, acct, raws)
	if err != nil {
		var invalid core.ValidationError
		if errors.As(err, &invalid) {
			a.rejectBatch(ctx, w, acct, body, invalid.Error())
			return
		}
		log.Printf("ingest failed for %s: %v", acct.Label, err)
		a.logIngest(ctx, &acct.ID, body, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "could not store events")
		return
	}

	a.logIngest(ctx, &acct.ID, body, "accepted", "")
	writeJSON(w, http.StatusOK, res)
}

func (a *API) rejectBatch(ctx context.Context, w http.ResponseWriter, acct store.SourceAccount, body []byte, msg string) {
	a.logIngest(ctx, &acct.ID, body, "rejected", msg)
	writeError(w, http.StatusBadRequest, msg)
}

func (a *API) logIngest(ctx context.Context, id *uuid.UUID, body []byte, status, msg string) {
	if err := a.store.LogIngest(ctx, id, body, status, msg); err != nil {
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

// manualEntry is what the capture form posts. It is deliberately thinner than
// the envelope: the browser shouldn't have to invent an external_id or know
// what a source account is.
type manualEntry struct {
	Title      string     `json:"title"`
	Kind       string     `json:"kind"`
	OccurredAt *time.Time `json:"occurred_at"`
	URL        string     `json:"url"`
	Note       string     `json:"note"`
}

func (a *API) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	// feeds the same pipeline as every webhook delivery. The generated ID is
	// stable from creation, which is all the deterministic-UUID scheme needs.
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

	res, err := a.pipeline.Ingest(ctx, a.manual, []core.RawRecord{{
		ExternalID: env.ExternalID,
		Payload:    raw,
	}})
	if err != nil {
		var invalid core.ValidationError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, invalid.Error())
			return
		}
		log.Printf("manual entry failed: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save event")
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

	events, err := a.store.ListEvents(r.Context(), store.EventFilter{From: from, To: to, Limit: limit})
	if err != nil {
		log.Printf("list events: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *API) handleListSourceAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := a.store.ListSourceAccounts(r.Context())
	if err != nil {
		log.Printf("list source accounts: %v", err)
		writeError(w, http.StatusInternalServerError, "could not load source accounts")
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

	acct, token, err := a.store.CreatePushEndpoint(r.Context(), in.Source, in.Label)
	if err != nil {
		log.Printf("create push endpoint: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create endpoint")
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

// createPullAccount connects a polling source, verifying the credentials
// before storing anything.
//
// Verifying up front matters more than it looks: the characteristic GitHub
// failure is a token that authenticates fine but can see nothing, which
// produces syncs that succeed and return zero events. Failing at connect time
// with a real login echoed back is the difference between "it works" and "it
// appears to work".
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

	token, err := secrets.Resolve(credentialsRef)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	login, err := github.WhoAmI(r.Context(), token, os.Getenv("GITHUB_API_BASE_URL"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	acct, err := a.store.CreatePullAccount(r.Context(), source, label, login, credentialsRef)
	if err != nil {
		log.Printf("create pull account: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save the source account")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"source_account": acct,
		"login":          login,
	})
}

func (a *API) handleSync(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid source account id")
		return
	}

	acct, err := a.store.SourceAccountByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such source account")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load the source account")
		return
	}
	if acct.Mode != "pull" {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("%q is a %s source; only pull sources can be synced", acct.Label, acct.Mode))
		return
	}

	// An optional start date, for reaching back past the point a previous
	// successful sync already advanced the watermark to.
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
	// That is safe: progress is checkpointed to the cursor between rounds, so
	// the next run resumes rather than restarting.
	report, err := a.runner.Sync(r.Context(), acct, opts)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":  err.Error(),
			"report": report,
		})
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
