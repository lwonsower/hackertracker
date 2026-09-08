package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/lwonsower/hackertracker/backend/internal/store"
)

// maxAttachBatch bounds one filing action. Filing is deliberately a set
// operation, but not an unbounded one.
const maxAttachBatch = 500

func (a *API) arcRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /api/arcs":                          a.handleListArcs,
		"POST /api/arcs":                         a.handleCreateArc,
		"GET /api/arcs/{id}":                     a.handleGetArc,
		"PUT /api/arcs/{id}":                     a.handleUpdateArc,
		"POST /api/arcs/{id}/entries":            a.handleCreateArcEntry,
		"GET /api/arcs/{id}/candidates":          a.handleArcCandidates,
		"POST /api/arcs/{id}/events":             a.handleAttachEvents,
		"DELETE /api/arcs/{id}/events/{eventID}": a.handleDetachEvent,
	}
}

// arcID reads the path parameter, answering 400 for something that is not a
// UUID at all and leaving "not yours" to the scope, which calls it 404.
func arcID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid arc id")
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body as JSON")
		return false
	}
	return true
}

func (a *API) handleListArcs(w http.ResponseWriter, r *http.Request) {
	var arcs []store.ArcRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		arcs, err = st.ListArcs(r.Context())
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"arcs": arcs})
}

func (a *API) handleCreateArc(w http.ResponseWriter, r *http.Request) {
	var in store.ArcInput
	if !decodeBody(w, r, &in) {
		return
	}

	var arc store.Arc
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		arc, err = st.CreateArc(r.Context(), in)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"arc": arc})
}

// handleGetArc returns the arc with its narrative and its filed evidence in
// one response: the detail page needs all three, and three round-trips would
// only give it three chances to disagree with itself.
func (a *API) handleGetArc(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}

	var arc store.Arc
	var entries []store.ArcEntry
	var events []store.ArcEventRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		if arc, err = st.ArcByID(r.Context(), id); err != nil {
			return err
		}
		if entries, err = st.ListArcEntries(r.Context(), id); err != nil {
			return err
		}
		events, err = st.ListArcEvents(r.Context(), id)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"arc": arc, "entries": entries, "events": events})
}

func (a *API) handleUpdateArc(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}
	var in store.ArcInput
	if !decodeBody(w, r, &in) {
		return
	}

	var arc store.Arc
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		arc, err = st.UpdateArc(r.Context(), id, in)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"arc": arc})
}

func (a *API) handleCreateArcEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind       string     `json:"kind"`
		Body       string     `json:"body"`
		OccurredAt *time.Time `json:"occurred_at"`
	}
	if !decodeBody(w, r, &in) {
		return
	}

	var entry store.ArcEntry
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		entry, err = st.AddArcEntry(r.Context(), id, in.Kind, in.Body, in.OccurredAt)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"entry": entry})
}

// handleArcCandidates powers "Add evidence": events not yet filed under this
// arc, narrowed by a search box and a date range.
func (a *API) handleArcCandidates(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}

	q := r.URL.Query()
	now := time.Now().UTC()
	from, err := parseTimeParam(q.Get("from"), now.AddDate(-10, 0, 0))
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

	var events []store.ArcEventRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		events, err = st.ListCandidateEvents(r.Context(), id, store.CandidateFilter{
			Query: q.Get("q"), From: from, To: to, Limit: limit,
		})
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *API) handleAttachEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}
	var in struct {
		EventIDs []uuid.UUID `json:"event_ids"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.EventIDs) > maxAttachBatch {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("at most %d events at a time", maxAttachBatch))
		return
	}

	var attached int
	var events []store.ArcEventRow
	if ok := a.scope(w, r, func(st *store.Store) error {
		var err error
		if attached, err = st.AttachEvents(r.Context(), id, in.EventIDs); err != nil {
			return err
		}
		events, err = st.ListArcEvents(r.Context(), id)
		return err
	}); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attached": attached, "events": events})
}

func (a *API) handleDetachEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := arcID(w, r)
	if !ok {
		return
	}
	eventID, err := uuid.Parse(r.PathValue("eventID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "not a valid event id")
		return
	}

	// Detaching unfiles the event; it does not delete it. The event stays in
	// the capture layer, which sync owns.
	if ok := a.scope(w, r, func(st *store.Store) error {
		return st.DetachEvent(r.Context(), id, eventID)
	}); !ok {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
