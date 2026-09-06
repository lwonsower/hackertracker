package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// item builds one GitHub search result. Only the fields the connector reads
// are populated; raw_records would keep whatever else GitHub sent.
func item(number int, merged bool) string {
	pr := `"pull_request":{}`
	if merged {
		pr = `"pull_request":{"merged_at":"2026-01-22T10:30:00Z"}`
	}
	return fmt.Sprintf(`{
		"title":"Migrate auth service",
		"html_url":"https://github.com/acme/api/pull/%d",
		"number":%d,
		"repository_url":"https://api.github.com/repos/acme/api",
		"created_at":"2026-01-20T09:00:00Z",
		"updated_at":"2026-01-22T11:00:00Z",
		"closed_at":"2026-01-22T10:30:00Z",
		"labels":[{"name":"backend"}],
		%s
	}`, number, number, pr)
}

type stub struct {
	server  *httptest.Server
	queries []string
}

// newStub answers every search with `count` items on the first page.
func newStub(t *testing.T, count int) *stub {
	t.Helper()
	s := &stub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		s.queries = append(s.queries, q.Get("q"))

		n := count
		if q.Get("page") != "1" {
			n = 0
		}
		items := make([]string, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, item(1000+i, true))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, n, strings.Join(items, ","))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func newConnector(t *testing.T, base string, windows int) *Connector {
	t.Helper()
	zero := time.Duration(0)
	c, err := New(Config{
		Login:          "testuser",
		Token:          "t0ken",
		BaseURL:        base,
		Spacing:        &zero, // no pacing in tests
		WindowsPerSync: windows,
		Now:            func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// Jan 15 to Mar 10 spans three calendar months, and each month is searched
// twice (merged and reviewed), so six windows.
func TestSyncCoversOneQueryPerMonthPerStream(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 10)

	since := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	records, cursor, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if cursor != "" {
		t.Errorf("expected the run to complete, got cursor %q", cursor)
	}
	if len(s.queries) != 6 {
		t.Errorf("expected 6 queries (3 months x 2 streams), got %d: %v", len(s.queries), s.queries)
	}
	if len(records) != 6 {
		t.Errorf("expected 6 records, got %d", len(records))
	}

	// Windows must be month-bounded, which is what keeps each query under
	// GitHub's 1,000-result cap.
	if !strings.Contains(s.queries[0], "merged:2026-01-01..2026-01-31") {
		t.Errorf("first query is not month-bounded: %q", s.queries[0])
	}
	if !strings.Contains(s.queries[0], "author:testuser is:merged") {
		t.Errorf("merged stream query is wrong: %q", s.queries[0])
	}
	// Your own PRs are excluded from the review stream so they aren't counted twice.
	if !strings.Contains(s.queries[1], "reviewed-by:testuser -author:testuser") {
		t.Errorf("reviewed stream query is wrong: %q", s.queries[1])
	}
	if stats := c.SyncStats(); stats["windows_searched"] != 6 {
		t.Errorf("expected 6 windows searched, got %d", stats["windows_searched"])
	}
}

// A bounded Sync must hand back a cursor and resume from it, so a long
// backfill makes progress instead of restarting.
func TestSyncResumesFromCursor(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 2)

	since := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	_, cursor, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if cursor == "" {
		t.Fatal("expected a cursor after a bounded run")
	}
	if len(s.queries) != 2 {
		t.Fatalf("expected 2 queries in the first round, got %d", len(s.queries))
	}

	var parsed struct {
		From   string `json:"from"`
		Stream string `json:"stream"`
	}
	if err := json.Unmarshal([]byte(cursor), &parsed); err != nil {
		t.Fatalf("cursor is not valid JSON: %v", err)
	}
	if parsed.From != "2026-02-01" || parsed.Stream != streamMerged {
		t.Errorf("cursor should point at February merged, got %+v", parsed)
	}

	if _, _, err := c.Sync(context.Background(), since, cursor); err != nil {
		t.Fatalf("resumed Sync: %v", err)
	}
	// Resuming must not redo January.
	for _, q := range s.queries[2:] {
		if strings.Contains(q, "2026-01-01") {
			t.Errorf("resumed run repeated January: %q", q)
		}
	}
}

func TestSyncPaginates(t *testing.T) {
	s := newStub(t, perPage) // a full page forces a second request
	c := newConnector(t, s.server.URL, 1)

	since := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	records, _, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(s.queries) != 2 {
		t.Errorf("a full first page should trigger a second request, got %d", len(s.queries))
	}
	if len(records) != perPage {
		t.Errorf("expected %d records, got %d", perPage, len(records))
	}
}

func TestSyncReportsAuthFailureClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newConnector(t, srv.URL, 1)
	_, _, err := c.Sync(context.Background(), time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "")
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if !strings.Contains(err.Error(), "credentials_ref") {
		t.Errorf("401 should point at the credential, got: %v", err)
	}
}

func TestNormalizeMergedPullRequest(t *testing.T) {
	acct := uuid.New()
	rec, ok := rawRecord(streamMerged, json.RawMessage(item(1234, true)))
	if !ok {
		t.Fatal("rawRecord rejected a valid item")
	}
	if rec.ExternalID != "pr:acme/api#1234" {
		t.Errorf("external id = %q", rec.ExternalID)
	}
	rec.SourceAccountID = acct

	events, err := Normalizer{}.Normalize(rec)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	e := events[0]
	if e.Kind != "pr_merged" {
		t.Errorf("kind = %q", e.Kind)
	}
	if e.SubjectKey != "github:acme/api#1234" {
		t.Errorf("subject_key = %q", e.SubjectKey)
	}
	// merged_at is preferred over closed_at and updated_at.
	if want := time.Date(2026, 1, 22, 10, 30, 0, 0, time.UTC); !e.OccurredAt.Equal(want) {
		t.Errorf("occurred_at = %s, want %s", e.OccurredAt, want)
	}
}

// merged_at is not guaranteed in search results, so closed_at has to carry it.
func TestNormalizeFallsBackToClosedAt(t *testing.T) {
	rec, _ := rawRecord(streamMerged, json.RawMessage(item(7, false)))
	events, err := Normalizer{}.Normalize(rec)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if want := time.Date(2026, 1, 22, 10, 30, 0, 0, time.UTC); !events[0].OccurredAt.Equal(want) {
		t.Errorf("occurred_at = %s, want closed_at %s", events[0].OccurredAt, want)
	}
}

// Review timestamps are approximations, and must say so — that marker is what
// makes a later upgrade to real review times findable.
func TestNormalizeReviewMarksApproximateTimestamp(t *testing.T) {
	rec, ok := rawRecord(streamReviewed, json.RawMessage(item(1234, true)))
	if !ok {
		t.Fatal("rawRecord rejected a valid item")
	}
	if rec.ExternalID != "review:acme/api#1234" {
		t.Fatalf("external id = %q; must not collide with the pr: stream", rec.ExternalID)
	}

	events, err := Normalizer{}.Normalize(rec)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	e := events[0]
	if e.Kind != "review_submitted" {
		t.Errorf("kind = %q", e.Kind)
	}
	if !strings.HasPrefix(e.Title, "Reviewed: ") {
		t.Errorf("title = %q", e.Title)
	}
	if want := time.Date(2026, 1, 22, 11, 0, 0, 0, time.UTC); !e.OccurredAt.Equal(want) {
		t.Errorf("occurred_at = %s, want updated_at %s", e.OccurredAt, want)
	}

	var payload map[string]any
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["timestamp_precision"] != "approximate" {
		t.Errorf("review events must be marked approximate, got %v", payload["timestamp_precision"])
	}
}

// The same pull request appearing in both streams must produce two distinct
// events rather than colliding.
func TestStreamsDoNotCollide(t *testing.T) {
	acct := uuid.New()

	merged, _ := rawRecord(streamMerged, json.RawMessage(item(1234, true)))
	merged.SourceAccountID = acct
	reviewed, _ := rawRecord(streamReviewed, json.RawMessage(item(1234, true)))
	reviewed.SourceAccountID = acct

	a, err := Normalizer{}.Normalize(merged)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Normalizer{}.Normalize(reviewed)
	if err != nil {
		t.Fatal(err)
	}
	if a[0].ID == b[0].ID {
		t.Error("merged and reviewed events for the same PR share an ID")
	}
	if a[0].SubjectKey != b[0].SubjectKey {
		t.Error("both should still point at the same underlying pull request")
	}
}

func TestBuildTasksIsMonthAligned(t *testing.T) {
	since := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)

	tasks := buildTasks(since, until)
	if len(tasks) != 6 {
		t.Fatalf("expected 6 tasks, got %d", len(tasks))
	}
	// Reaching back to the first of the month deliberately overlaps `since`;
	// re-ingesting is a no-op, and the overlap is what stops silent gaps.
	if got := tasks[0].start; got.Day() != 1 || got.Month() != time.January {
		t.Errorf("first window should start 1 January, got %s", got)
	}
}

// A backfill is several cursor-driven Sync calls. Stats describe the whole run,
// so they must accumulate rather than reset each call — otherwise a 26-query
// backfill reports however many queries the last round issued.
func TestSyncStatsAccumulateAcrossRounds(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 2)

	since := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	cursor := ""
	for round := 0; round < 10; round++ {
		_, next, err := c.Sync(context.Background(), since, cursor)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	stats := c.SyncStats()
	if stats["queries_issued"] != 6 {
		t.Errorf("queries_issued = %d across all rounds, want 6", stats["queries_issued"])
	}
	if stats["windows_searched"] != 6 {
		t.Errorf("windows_searched = %d across all rounds, want 6", stats["windows_searched"])
	}
}
