package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lwonsower/hackertracker/backend/internal/auth"
	"github.com/lwonsower/hackertracker/backend/internal/connectors/gcal"
	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/ingest"
	"github.com/lwonsower/hackertracker/backend/internal/store"
)

const (
	calendarSource  = "google_calendar"
	calendarLabel   = "Google Calendar"
	calendarPurpose = "calendar"
	handshakeTTL    = 10 * time.Minute
	// How far back a first review looks. A week, because this is a habit, not
	// a backfill: history you never reviewed is not evidence you remember.
	firstReviewWindow = 7 * 24 * time.Hour
	maxPromoteBatch   = 200
	maxNoteLength     = 2000
)

// CalendarConfig is nil when no Google client is configured, which makes the
// whole feature report itself as unavailable rather than half-work.
type CalendarConfig struct {
	gcal.Config
	RedirectURL string
}

func (a *API) calendarRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/calendar":            a.handleCalendarStatus,
		"GET /api/calendar/connect":    a.handleCalendarConnect,
		"GET /api/calendar/callback":   a.handleCalendarCallback,
		"GET /api/calendar/proposals":  a.handleCalendarProposals,
		"POST /api/calendar/proposals": a.handlePromoteProposals,
		"DELETE /api/calendar":         a.handleCalendarDisconnect,
	}
}

func (a *API) calendarReady() bool {
	return a.calendar != nil && a.calendar.ClientID != "" && a.calendar.RedirectURL != ""
}

// calendarSourceFor loads the connected calendar, or reports that there is none.
func (a *API) calendarSourceFor(w http.ResponseWriter, r *http.Request) (store.SourceAccount, bool) {
	var acct store.SourceAccount
	ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		acct, err = st.SourceAccountBySource(r.Context(), calendarSource)
		return err
	})
	if !ok {
		return acct, false
	}
	// A disconnected calendar still has a row, because its recorded meetings
	// hang off it. It just cannot be read from any more.
	if acct.CredentialsRef == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this calendar is disconnected; reconnect it to review meetings again", "reconnect": true,
		})
		return acct, false
	}
	return acct, true
}

func (a *API) handleCalendarStatus(w http.ResponseWriter, r *http.Request) {
	var acct store.SourceAccount
	var state store.SyncState
	var connected, known bool

	if ok := a.scope(w, r, func(st *store.Store) error {
		found, err := st.SourceAccountBySource(r.Context(), calendarSource)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		acct = found
		connected = found.CredentialsRef != ""
		known = true
		state, err = st.GetSyncState(r.Context(), found.ID)
		return err
	}); !ok {
		return
	}

	body := map[string]any{
		"available": a.calendarReady(),
		"connected": connected,
		// Known but not connected: the calendar was disconnected and its
		// recorded meetings are still here.
		"disconnected": known && !connected,
	}
	if known {
		body["account"] = acct.ExternalAccountID
		body["last_reviewed_at"] = state.LastSyncedAt
	}
	writeJSON(w, http.StatusOK, body)
}

// handleCalendarConnect starts a second, separate Google handshake. Sign-in
// asks only for identity; consenting to read a calendar is a different
// decision and is asked for at the moment it is needed.
func (a *API) handleCalendarConnect(w http.ResponseWriter, r *http.Request) {
	if !a.calendarReady() {
		writeError(w, http.StatusServiceUnavailable, "google calendar is not configured on this server")
		return
	}
	user, ok := auth.UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}

	state, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the handshake")
		return
	}
	verifier, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the handshake")
		return
	}

	// The account is carried in the state row, so the callback binds to the
	// person who started it rather than to whoever's cookie arrives.
	if err := a.db.SaveAuthState(r.Context(), store.AuthState{
		State:      state,
		Nonce:      user.AccountID.String(),
		Verifier:   verifier,
		Purpose:    calendarPurpose,
		RedirectTo: "/",
	}, handshakeTTL); err != nil {
		log.Printf("save calendar state: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start the handshake")
		return
	}

	challenge := sha256.Sum256([]byte(verifier))
	http.Redirect(w, r, a.calendar.AuthCodeURL(
		a.calendar.RedirectURL, state,
		base64.RawURLEncoding.EncodeToString(challenge[:]),
	), http.StatusFound)
}

func (a *API) handleCalendarCallback(w http.ResponseWriter, r *http.Request) {
	if !a.calendarReady() {
		writeError(w, http.StatusServiceUnavailable, "google calendar is not configured on this server")
		return
	}
	ctx := r.Context()
	query := r.URL.Query()

	if failure := query.Get("error"); failure != "" {
		http.Redirect(w, r, "/?calendar=denied", http.StatusFound)
		return
	}

	// Consuming deletes the row, and the purpose keeps a sign-in code from
	// being redeemed here.
	state, err := a.db.ConsumeAuthState(ctx, query.Get("state"), calendarPurpose)
	if err != nil {
		http.Redirect(w, r, "/?calendar=expired", http.StatusFound)
		return
	}

	user, ok := auth.UserFrom(ctx)
	if !ok || user.AccountID.String() != state.Nonce {
		http.Redirect(w, r, "/?calendar=mismatch", http.StatusFound)
		return
	}

	refreshToken, accessToken, err := a.calendar.Exchange(ctx, query.Get("code"), a.calendar.RedirectURL, state.Verifier)
	if err != nil {
		log.Printf("calendar exchange: %v", err)
		http.Redirect(w, r, "/?calendar=failed", http.StatusFound)
		return
	}

	// Prove the grant works before storing anything, the same way a GitHub
	// token is verified before it becomes a row.
	if _, err := a.calendar.List(ctx, accessToken, "primary", time.Now().Add(-time.Hour), time.Now()); err != nil {
		log.Printf("calendar verification read: %v", err)
		http.Redirect(w, r, "/?calendar=failed", http.StatusFound)
		return
	}

	if err := a.db.Scope(ctx, user.AccountID, func(st *store.Store) error {
		previous, err := st.CredentialRefForSource(ctx, calendarSource, calendarLabel)
		if err != nil {
			return err
		}
		keyID, ciphertext, err := a.secrets.Seal(user.AccountID, refreshToken)
		if err != nil {
			return err
		}
		id, err := st.CreateCredential(ctx, calendarLabel, keyID, ciphertext)
		if err != nil {
			return err
		}
		if _, err := st.CreateConnectedAccount(ctx, calendarSource, "review", calendarLabel,
			user.Email, "secret:"+id.String()); err != nil {
			return err
		}
		// Reconnecting replaces the grant rather than orphaning it.
		if old, found := strings.CutPrefix(previous, "secret:"); found {
			if oldID, err := uuid.Parse(old); err == nil {
				return st.DeleteCredential(ctx, oldID)
			}
		}
		return nil
	}); err != nil {
		log.Printf("store calendar credential: %v", err)
		http.Redirect(w, r, "/?calendar=failed", http.StatusFound)
		return
	}

	http.Redirect(w, r, "/?calendar=connected", http.StatusFound)
}

// handleCalendarDisconnect revokes our access and keeps the evidence.
//
// Deleting the source would cascade to its events, and those are meetings the
// person deliberately kept, with the notes they wrote on them. The likeliest
// moment to disconnect is when you are leaving the job — which is exactly when
// losing that history would be worst.
func (a *API) handleCalendarDisconnect(w http.ResponseWriter, r *http.Request) {
	if ok := a.scope(w, r, func(st *store.Store) error {
		acct, err := st.SourceAccountBySource(r.Context(), calendarSource)
		if err != nil {
			return err
		}
		if err := st.ClearCredentialRef(r.Context(), acct.ID); err != nil {
			return err
		}
		if id, found := strings.CutPrefix(acct.CredentialsRef, "secret:"); found {
			if credID, err := uuid.Parse(id); err == nil {
				return st.DeleteCredential(r.Context(), credID)
			}
		}
		return nil
	}); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── proposals ────────────────────────────────────────────────────────────

type proposal struct {
	gcal.Event
	AlreadyAdded bool `json:"already_added,omitempty"`
}

// handleCalendarProposals reads a window of the calendar and returns it.
//
// Nothing here is written. This is the whole design: a calendar holds
// interviews and medical appointments, and the capture layer keeps raw
// payloads forever, so unpicked meetings must never reach it.
func (a *API) handleCalendarProposals(w http.ResponseWriter, r *http.Request) {
	acct, ok := a.calendarSourceFor(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	now := time.Now().UTC()

	var syncState store.SyncState
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		syncState, err = st.GetSyncState(r.Context(), acct.ID)
		return err
	}); !ok {
		return
	}

	// The review watermark is the window: what you already looked at does not
	// come back, and no record of what you dismissed has to be kept.
	defaultFrom := now.Add(-firstReviewWindow)
	if syncState.LastSyncedAt != nil && syncState.LastSyncedAt.After(defaultFrom) {
		defaultFrom = syncState.LastSyncedAt.UTC()
	}
	from, err := parseTimeParam(q.Get("from"), defaultFrom)
	if err != nil {
		writeError(w, http.StatusBadRequest, "from: "+err.Error())
		return
	}
	to, err := parseTimeParam(q.Get("to"), now)
	if err != nil {
		writeError(w, http.StatusBadRequest, "to: "+err.Error())
		return
	}

	events, err := a.readCalendar(r, acct, from, to)
	if err != nil {
		a.writeCalendarError(w, err)
		return
	}

	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	var existing map[string]bool
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		existing, err = st.ExistingExternalIDs(r.Context(), acct.ID, ids)
		return err
	}); !ok {
		return
	}

	// Excluded events are returned, not dropped, so "show what was hidden" is
	// a toggle rather than another round trip to Google.
	proposals := make([]proposal, 0, len(events))
	suggested, hidden := 0, 0
	for _, e := range events {
		p := proposal{Event: e, AlreadyAdded: existing[e.ID]}
		switch {
		case p.AlreadyAdded:
		case p.Excluded != "":
			hidden++
		default:
			suggested++
		}
		proposals = append(proposals, p)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"from": from, "to": to,
		"suggested": suggested, "hidden": hidden,
		"proposals": proposals,
	})
}

type promoteRequest struct {
	// ReviewedThrough advances the watermark, so meetings you looked at and
	// chose not to keep do not come back — without storing anything about them.
	ReviewedThrough *time.Time `json:"reviewed_through"`
	Picks           []struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	} `json:"picks"`
}

// handlePromoteProposals turns the picked meetings into events. It re-reads
// the window rather than trusting the browser's copy: the client sends ids and
// notes, never the facts, so a tampered payload cannot invent a meeting.
func (a *API) handlePromoteProposals(w http.ResponseWriter, r *http.Request) {
	acct, ok := a.calendarSourceFor(w, r)
	if !ok {
		return
	}

	var in promoteRequest
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Picks) > maxPromoteBatch {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d meetings at a time", maxPromoteBatch))
		return
	}

	notes := make(map[string]string, len(in.Picks))
	for _, p := range in.Picks {
		note := strings.TrimSpace(p.Note)
		if len(note) > maxNoteLength {
			writeError(w, http.StatusBadRequest, "a note is longer than 2000 characters")
			return
		}
		notes[p.ID] = note
	}

	var res ingest.Result
	if len(notes) > 0 {
		// A generous window: the browser may be showing an older review.
		from := time.Now().UTC().Add(-firstReviewWindow * 8)
		if in.ReviewedThrough != nil {
			from = in.ReviewedThrough.UTC().Add(-firstReviewWindow * 8)
		}
		events, err := a.readCalendar(r, acct, from, time.Now().UTC().Add(24*time.Hour))
		if err != nil {
			a.writeCalendarError(w, err)
			return
		}

		raws := make([]core.RawRecord, 0, len(notes))
		for _, e := range events {
			note, picked := notes[e.ID]
			if !picked {
				continue
			}
			payload, err := json.Marshal(gcal.Promoted{Event: e, Note: note})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "could not encode a meeting")
				return
			}
			raws = append(raws, core.RawRecord{ExternalID: e.ID, Payload: payload})
		}
		if len(raws) != len(notes) {
			writeError(w, http.StatusConflict,
				"some of those meetings are no longer on the calendar; reload the review")
			return
		}

		if ok := a.scope(w, r, func(st *store.Store) error {
			var err error
			res, err = a.pipeline.Ingest(r.Context(), st, acct, raws)
			return err
		}); !ok {
			return
		}
	}

	// The watermark moves whether or not anything was picked: reviewing and
	// keeping nothing is still reviewing.
	through := time.Now().UTC()
	if in.ReviewedThrough != nil {
		through = in.ReviewedThrough.UTC()
	}
	if ok := a.scope(w, r, func(st *store.Store) error {
		return st.SaveSyncState(r.Context(), acct.ID, store.SyncState{LastSyncedAt: &through})
	}); !ok {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"added": res.Created, "updated": res.Updated, "reviewed_through": through,
	})
}

// readCalendar resolves the stored refresh token inside the account scope, so
// row-level security covers reading the credential too, then reads the window.
func (a *API) readCalendar(r *http.Request, acct store.SourceAccount, from, to time.Time) ([]gcal.Event, error) {
	if !a.calendarReady() {
		return nil, errors.New("google calendar is not configured on this server")
	}
	ctx := r.Context()

	var refreshToken string
	if err := a.db.Scope(ctx, acct.AccountID, func(st *store.Store) error {
		var err error
		refreshToken, err = a.secrets.Resolve(ctx, st, acct.AccountID, acct.CredentialsRef)
		return err
	}); err != nil {
		return nil, err
	}

	accessToken, err := a.calendar.AccessToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return a.calendar.List(ctx, accessToken, "primary", from, to)
}

func (a *API) writeCalendarError(w http.ResponseWriter, err error) {
	if errors.Is(err, gcal.ErrReconnect) {
		// Deliberately not 401: the browser reads that as "your session ended"
		// and would tell you to sign in again when the real fix is to
		// reauthorise the calendar.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": gcal.ErrReconnect.Error(), "reconnect": true,
		})
		return
	}
	log.Printf("calendar read: %v", err)
	writeError(w, http.StatusBadGateway, "could not read your calendar")
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
