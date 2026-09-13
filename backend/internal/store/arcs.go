package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lwonsower/hackertracker/backend/internal/core"
)

const dateLayout = "2006-01-02"

// Date is a calendar date with no time and no zone, so a target date does not
// shift a day when the browser applies its own offset.
type Date struct{ time.Time }

func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Format(dateLayout))
}

func dateOf(t *time.Time) *Date {
	if t == nil {
		return nil
	}
	return &Date{*t}
}

type Arc struct {
	ID uuid.UUID `json:"id"`
	// ParentID is the arc this one supports. A promotion case holds
	// "cross-team influence", which holds the work itself.
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	Summary   string     `json:"summary,omitempty"`
	StartedAt *Date      `json:"started_at,omitempty"`
	TargetAt  *Date      `json:"target_at,omitempty"`
	EndedAt   *Date      `json:"ended_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ArcRow carries the counts the list view shows, so listing arcs does not
// mean fetching every arc's events to say how many there are.
type ArcRow struct {
	Arc
	EventCount  int        `json:"event_count"`
	EntryCount  int        `json:"entry_count"`
	ChildCount  int        `json:"child_count"`
	LastEventAt *time.Time `json:"last_event_at,omitempty"`
}

// Empty reports an arc that supports nothing yet — no events, no sub-arcs.
// This is the gap the goals tier existed to show: a named thing you said
// mattered, with nothing behind it.
func (r ArcRow) Empty() bool { return r.EventCount == 0 && r.ChildCount == 0 }

type ArcEntry struct {
	ID         uuid.UUID `json:"id"`
	ArcID      uuid.UUID `json:"arc_id"`
	Kind       string    `json:"kind,omitempty"`
	Body       string    `json:"body"`
	OccurredAt time.Time `json:"occurred_at"`
	CreatedAt  time.Time `json:"created_at"`
}

// ArcInput is the writable half of an arc. Dates arrive as YYYY-MM-DD, which
// is exactly what <input type="date"> produces; empty means unset.
type ArcInput struct {
	// ParentID is "" for a top-level arc. A whole-record replace means an
	// emptied value detaches it, which a merge could not express.
	ParentID  string `json:"parent_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	StartedAt string `json:"started_at"`
	TargetAt  string `json:"target_at"`
	EndedAt   string `json:"ended_at"`
}

var arcStatuses = map[string]bool{"open": true, "done": true, "dropped": true}

// entryKinds mirrors the check constraint on arc_entries. Empty is allowed:
// kinds are optional labels, not a choice you must make before writing.
var entryKinds = map[string]bool{
	"hypothesis": true, "update": true, "risk": true, "outcome": true, "retro": true,
}

func (in *ArcInput) clean() error {
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Status = strings.TrimSpace(in.Status)

	if in.Title == "" {
		return core.ValidationError{Msg: "an arc needs a title"}
	}
	if len(in.Title) > 200 {
		return core.ValidationError{Msg: "title is longer than 200 characters"}
	}
	if in.Status == "" {
		in.Status = "open"
	}
	if !arcStatuses[in.Status] {
		return core.ValidationError{Msg: fmt.Sprintf("status %q must be open, done or dropped", in.Status)}
	}
	in.ParentID = strings.TrimSpace(in.ParentID)
	if in.ParentID != "" {
		if _, err := uuid.Parse(in.ParentID); err != nil {
			return core.ValidationError{Msg: "parent_id is not a valid arc id"}
		}
	}
	for label, value := range map[string]*string{
		"started_at": &in.StartedAt, "target_at": &in.TargetAt, "ended_at": &in.EndedAt,
	} {
		*value = strings.TrimSpace(*value)
		if *value == "" {
			continue
		}
		if _, err := time.Parse(dateLayout, *value); err != nil {
			return core.ValidationError{Msg: label + " must be a date like 2026-03-14"}
		}
	}
	return nil
}

const arcCols = `id, parent_id, title, status, coalesce(summary, ''), started_at, target_at, ended_at, created_at`

func scanArc(s scannable) (Arc, error) {
	var a Arc
	var started, target, ended *time.Time
	err := s.Scan(&a.ID, &a.ParentID, &a.Title, &a.Status, &a.Summary, &started, &target, &ended, &a.CreatedAt)
	a.StartedAt, a.TargetAt, a.EndedAt = dateOf(started), dateOf(target), dateOf(ended)
	return a, err
}

func (s *Store) CreateArc(ctx context.Context, in ArcInput) (Arc, error) {
	if err := in.clean(); err != nil {
		return Arc{}, err
	}
	// Loading the parent first makes "that arc is not yours" a 404 rather than
	// a foreign-key violation surfacing as a 500.
	if in.ParentID != "" {
		if _, err := s.ArcByID(ctx, uuid.MustParse(in.ParentID)); err != nil {
			return Arc{}, err
		}
	}
	return scanArc(s.db.QueryRow(ctx, `
		insert into arcs (account_id, parent_id, title, status, summary, started_at, target_at, ended_at)
		values ($1, nullif($2, '')::uuid, $3, $4, nullif($5, ''), nullif($6, '')::date, nullif($7, '')::date, nullif($8, '')::date)
		returning `+arcCols,
		s.accountID, in.ParentID, in.Title, in.Status, in.Summary, in.StartedAt, in.TargetAt, in.EndedAt))
}

// wouldCycle walks up from a proposed parent looking for the arc being moved.
// The database can only refuse an arc parented to itself; a longer loop —
// A under B under A — has to be caught here, and an unchecked one would make
// every recursive read hang.
func (s *Store) wouldCycle(ctx context.Context, arcID, parentID uuid.UUID) (bool, error) {
	const maxDepth = 64
	at := parentID
	for i := 0; i < maxDepth; i++ {
		if at == arcID {
			return true, nil
		}
		var next *uuid.UUID
		err := s.db.QueryRow(ctx, `select parent_id from arcs where id = $1`, at).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, err
		}
		if next == nil {
			return false, nil
		}
		at = *next
	}
	// Deeper than anything real; treat it as a loop rather than spin.
	return true, nil
}

// UpdateArc replaces the writable fields wholesale, so clearing a date is
// expressible — a merge could only ever set them.
func (s *Store) UpdateArc(ctx context.Context, id uuid.UUID, in ArcInput) (Arc, error) {
	if err := in.clean(); err != nil {
		return Arc{}, err
	}
	if in.ParentID != "" {
		parent := uuid.MustParse(in.ParentID)
		if parent == id {
			return Arc{}, core.ValidationError{Msg: "an arc cannot be its own parent"}
		}
		looped, err := s.wouldCycle(ctx, id, parent)
		if err != nil {
			return Arc{}, err
		}
		if looped {
			return Arc{}, core.ValidationError{Msg: "that would put the arc inside itself"}
		}
	}
	a, err := scanArc(s.db.QueryRow(ctx, `
		update arcs set parent_id = nullif($2, '')::uuid,
		                title = $3, status = $4, summary = nullif($5, ''),
		                started_at = nullif($6, '')::date,
		                target_at  = nullif($7, '')::date,
		                ended_at   = nullif($8, '')::date
		where id = $1
		returning `+arcCols,
		id, in.ParentID, in.Title, in.Status, in.Summary, in.StartedAt, in.TargetAt, in.EndedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		return Arc{}, ErrNotFound
	}
	return a, err
}

func (s *Store) ArcByID(ctx context.Context, id uuid.UUID) (Arc, error) {
	a, err := scanArc(s.db.QueryRow(ctx, `select `+arcCols+` from arcs where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Arc{}, ErrNotFound
	}
	return a, err
}

// ListArcs orders open arcs first: the ones you are still living in are the
// ones you file against.
func (s *Store) ListArcs(ctx context.Context) ([]ArcRow, error) {
	rows, err := s.db.Query(ctx, `
		select `+arcCols+`,
		       (select count(*) from arc_events ae where ae.arc_id = a.id),
		       (select count(*) from arc_entries en where en.arc_id = a.id),
		       (select count(*) from arcs c where c.parent_id = a.id),
		       (select max(e.occurred_at) from arc_events ae
		          join live_events e on e.id = ae.event_id
		         where ae.arc_id = a.id)
		from arcs a
		order by (status = 'open') desc, coalesce(target_at, started_at, created_at::date) desc, title`)
	if err != nil {
		return nil, fmt.Errorf("list arcs: %w", err)
	}
	defer rows.Close()

	out := []ArcRow{}
	for rows.Next() {
		var r ArcRow
		var started, target, ended *time.Time
		if err := rows.Scan(&r.ID, &r.ParentID, &r.Title, &r.Status, &r.Summary, &started, &target, &ended,
			&r.CreatedAt, &r.EventCount, &r.EntryCount, &r.ChildCount, &r.LastEventAt); err != nil {
			return nil, err
		}
		r.StartedAt, r.TargetAt, r.EndedAt = dateOf(started), dateOf(target), dateOf(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Children returns the arcs directly under this one, with the same counts the
// list view uses so an empty sub-arc is visible as a gap from its parent.
func (s *Store) Children(ctx context.Context, parentID uuid.UUID) ([]ArcRow, error) {
	rows, err := s.db.Query(ctx, `
		select `+arcCols+`,
		       (select count(*) from arc_events ae where ae.arc_id = a.id),
		       (select count(*) from arc_entries en where en.arc_id = a.id),
		       (select count(*) from arcs c where c.parent_id = a.id),
		       (select max(e.occurred_at) from arc_events ae
		          join live_events e on e.id = ae.event_id
		         where ae.arc_id = a.id)
		from arcs a
		where a.parent_id = $1
		order by (status = 'open') desc, title`, parentID)
	if err != nil {
		return nil, fmt.Errorf("list child arcs: %w", err)
	}
	defer rows.Close()

	out := []ArcRow{}
	for rows.Next() {
		var r ArcRow
		var started, target, ended *time.Time
		if err := rows.Scan(&r.ID, &r.ParentID, &r.Title, &r.Status, &r.Summary, &started, &target, &ended,
			&r.CreatedAt, &r.EventCount, &r.EntryCount, &r.ChildCount, &r.LastEventAt); err != nil {
			return nil, err
		}
		r.StartedAt, r.TargetAt, r.EndedAt = dateOf(started), dateOf(target), dateOf(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ancestors walks from an arc up to its root, nearest parent first, so the
// detail page can show where it sits without a second round trip per level.
func (s *Store) Ancestors(ctx context.Context, id uuid.UUID) ([]Arc, error) {
	out := []Arc{}
	at, err := s.ArcByID(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 64 && at.ParentID != nil; i++ {
		parent, err := s.ArcByID(ctx, *at.ParentID)
		if errors.Is(err, ErrNotFound) {
			// Row-level security hides another account's arc, and a parent
			// that is not visible is simply where the chain stops.
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, parent)
		at = parent
	}
	return out, nil
}

// ── entries ──────────────────────────────────────────────────────────────

func (s *Store) AddArcEntry(ctx context.Context, arcID uuid.UUID, kind, body string, occurredAt *time.Time) (ArcEntry, error) {
	kind, body = strings.TrimSpace(kind), strings.TrimSpace(body)
	if body == "" {
		return ArcEntry{}, core.ValidationError{Msg: "an entry needs a body"}
	}
	if kind != "" && !entryKinds[kind] {
		return ArcEntry{}, core.ValidationError{Msg: fmt.Sprintf("kind %q is not one of hypothesis, update, risk, outcome, retro", kind)}
	}
	// The arc is loaded first so a bad id is a 404 rather than a foreign-key
	// violation surfacing as a 500.
	if _, err := s.ArcByID(ctx, arcID); err != nil {
		return ArcEntry{}, err
	}

	when := time.Now().UTC()
	if occurredAt != nil {
		when = occurredAt.UTC()
	}

	var e ArcEntry
	var storedKind *string
	err := s.db.QueryRow(ctx, `
		insert into arc_entries (account_id, arc_id, kind, body, occurred_at)
		values ($1, $2, nullif($3, ''), $4, $5)
		returning id, arc_id, kind, body, occurred_at, created_at`,
		s.accountID, arcID, kind, body, when,
	).Scan(&e.ID, &e.ArcID, &storedKind, &e.Body, &e.OccurredAt, &e.CreatedAt)
	if storedKind != nil {
		e.Kind = *storedKind
	}
	return e, err
}

// ListArcEntries returns the narrative oldest first: the sequence is the
// evidence, and a prediction only reads as one when it precedes its outcome.
func (s *Store) ListArcEntries(ctx context.Context, arcID uuid.UUID) ([]ArcEntry, error) {
	rows, err := s.db.Query(ctx, `
		select id, arc_id, coalesce(kind, ''), body, occurred_at, created_at
		from arc_entries where arc_id = $1
		order by occurred_at, created_at`, arcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ArcEntry{}
	for rows.Next() {
		var e ArcEntry
		if err := rows.Scan(&e.ID, &e.ArcID, &e.Kind, &e.Body, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── membership ───────────────────────────────────────────────────────────

// AttachEvents files many events at once. Filing is the interaction the whole
// tool lives or dies on, so it is a set operation, never one row per request.
// Ids belonging to another account do not exist here, so they are skipped
// rather than refused: the insert selects from live_events, which row-level
// security has already filtered.
func (s *Store) AttachEvents(ctx context.Context, arcID uuid.UUID, eventIDs []uuid.UUID) (int, error) {
	if _, err := s.ArcByID(ctx, arcID); err != nil {
		return 0, err
	}
	if len(eventIDs) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, `
		insert into arc_events (account_id, arc_id, event_id)
		select $1, $2, e.id from live_events e where e.id = any($3)
		on conflict do nothing`,
		s.accountID, arcID, eventIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) DetachEvent(ctx context.Context, arcID, eventID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`delete from arc_events where arc_id = $1 and event_id = $2`, arcID, eventID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── evidence and candidates ──────────────────────────────────────────────

// ArcEventRow is an event plus the other arcs it is filed under, so filing the
// same event twice is a visible choice rather than an accident.
type ArcEventRow struct {
	EventRow
	OtherArcs []string `json:"other_arcs,omitempty"`
}

// CandidateFilter drives the search behind "Add evidence".
type CandidateFilter struct {
	Query string
	From  time.Time
	To    time.Time
	Limit int
}

// likeEscape neutralises the wildcards in a search box, so typing % does not
// silently match everything.
func likeEscape(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

const arcEventCols = `e.id, e.external_id, e.kind, coalesce(e.subject_key, ''), e.title,
	coalesce(e.url, ''), e.occurred_at, e.payload, sa.source, sa.label`

func scanArcEvents(rows pgx.Rows) ([]ArcEventRow, error) {
	defer rows.Close()

	out := []ArcEventRow{}
	for rows.Next() {
		var r ArcEventRow
		var payload []byte
		if err := rows.Scan(&r.ID, &r.ExternalID, &r.Kind, &r.SubjectKey, &r.Title,
			&r.URL, &r.OccurredAt, &payload, &r.Source, &r.SourceLabel, &r.OtherArcs); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		out = append(out, r)
	}
	return out, rows.Err()
}

// otherArcs lists the arcs an event is filed under, excluding the one being
// looked at.
const otherArcs = `(select coalesce(array_agg(a2.title order by a2.title), '{}')
	from arc_events ae2 join arcs a2 on a2.id = ae2.arc_id
	where ae2.event_id = e.id and ae2.arc_id <> $1)`

func (s *Store) ListArcEvents(ctx context.Context, arcID uuid.UUID) ([]ArcEventRow, error) {
	rows, err := s.db.Query(ctx, `
		select `+arcEventCols+`, `+otherArcs+`
		from arc_events ae
		join live_events e on e.id = ae.event_id
		join source_accounts sa on sa.id = e.source_account_id
		where ae.arc_id = $1
		order by e.occurred_at desc, e.title`, arcID)
	if err != nil {
		return nil, fmt.Errorf("list arc events: %w", err)
	}
	return scanArcEvents(rows)
}

// ListCandidateEvents finds events not yet filed under this arc. Events
// already filed elsewhere are still offered — one PR can be evidence for two
// lines of work — but they arrive labelled with where else they live.
func (s *Store) ListCandidateEvents(ctx context.Context, arcID uuid.UUID, f CandidateFilter) ([]ArcEventRow, error) {
	// Loaded so an arc that is not yours is a 404, the same answer every other
	// arc route gives, rather than an empty list that looks like a real search.
	if _, err := s.ArcByID(ctx, arcID); err != nil {
		return nil, err
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	query := strings.TrimSpace(f.Query)

	rows, err := s.db.Query(ctx, `
		select `+arcEventCols+`, `+otherArcs+`
		from live_events e
		join source_accounts sa on sa.id = e.source_account_id
		where not exists (select 1 from arc_events ae
		                   where ae.arc_id = $1 and ae.event_id = e.id)
		  and e.occurred_at >= $2 and e.occurred_at < $3
		  and ($4 = '' or e.title ilike $5 or e.kind ilike $5
		       or coalesce(e.subject_key, '') ilike $5)
		order by e.occurred_at desc, e.title
		limit $6`,
		arcID, f.From, f.To, query, "%"+likeEscape(query)+"%", f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list candidate events: %w", err)
	}
	return scanArcEvents(rows)
}
