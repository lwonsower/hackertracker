package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
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
	server *httptest.Server

	mu      sync.Mutex
	queries []string
}

func (s *stub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

// newStubWith lets a test decide what each query matches, which is how window
// density can be simulated.
func newStubWith(t *testing.T, respond func(q string, page int) (total int, items []string)) *stub {
	t.Helper()
	s := &stub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)

		s.mu.Lock()
		s.queries = append(s.queries, q)
		s.mu.Unlock()

		total, items := respond(q, page)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total_count":%d,"items":[%s]}`, total, strings.Join(items, ","))
	}))
	t.Cleanup(s.server.Close)
	return s
}

// newStub answers every search with `count` items on the first page.
func newStub(t *testing.T, count int) *stub {
	return newStubWith(t, func(_ string, page int) (int, []string) {
		if page != 1 {
			return count, nil
		}
		items := make([]string, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, item(1000+i, true))
		}
		return count, items
	})
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

var rangePattern = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\.\.(\d{4}-\d{2}-\d{2})`)

// spansMoreThanAMonth tells a year-sized query from a month-sized one.
func spansMoreThanAMonth(t *testing.T, q string) bool {
	t.Helper()
	m := rangePattern.FindStringSubmatch(q)
	if m == nil {
		t.Fatalf("query has no date range: %q", q)
	}
	from, err := time.Parse(dateFormat, m[1])
	if err != nil {
		t.Fatal(err)
	}
	to, err := time.Parse(dateFormat, m[2])
	if err != nil {
		t.Fatal(err)
	}
	return to.Sub(from) > 40*24*time.Hour
}

// 2024-06 to 2026-03 spans three calendar years, each searched twice.
func TestSyncUsesYearWindows(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 10)

	since := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	records, next, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if next != "" {
		t.Errorf("expected the run to complete, got cursor %q", next)
	}

	queries := s.seen()
	if len(queries) != 6 {
		t.Errorf("expected 6 queries (3 years x 2 streams), got %d: %v", len(queries), queries)
	}
	if len(records) != 6 {
		t.Errorf("expected 6 records, got %d", len(records))
	}
	if !strings.Contains(queries[0], "merged:2024-01-01..2024-12-31") {
		t.Errorf("first window should be a whole year: %q", queries[0])
	}
	if !strings.Contains(queries[0], "author:testuser is:merged") {
		t.Errorf("merged stream query is wrong: %q", queries[0])
	}
	// Streams run one after the other, so the merged stream finishes first.
	if strings.Contains(queries[1], "reviewed-by") {
		t.Errorf("expected the merged stream to complete before reviewed: %q", queries[1])
	}
	// Your own PRs are excluded from the review stream so they aren't counted twice.
	if !strings.Contains(queries[3], "reviewed-by:testuser -author:testuser") {
		t.Errorf("reviewed stream query is wrong: %q", queries[3])
	}
}

// The whole point of year windows: a decade of history should not cost a
// query per month when the data is sparse.
func TestTenYearBackfillIsCheapWhenSparse(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 100)

	since := time.Date(2016, 3, 10, 0, 0, 0, 0, time.UTC)
	if _, _, err := c.Sync(context.Background(), since, ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// 2016..2026 inclusive is 11 years, two streams.
	if got := len(s.seen()); got != 22 {
		t.Errorf("expected 22 queries for an 11-year sparse backfill, got %d", got)
	}
}

// A year holding more than one query can return must be re-fetched by month,
// or records are silently lost.
func TestDenseYearSplitsIntoMonths(t *testing.T) {
	s := newStubWith(t, func(q string, page int) (int, []string) {
		if spansMoreThanAMonth(t, q) {
			// Too dense to express in one query.
			return 5000, []string{item(1, true)}
		}
		if page != 1 {
			return 1, nil
		}
		return 1, []string{item(1, true)}
	})
	c := newConnector(t, s.server.URL, 10)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	records, _, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	stats := c.SyncStats()
	if stats["windows_split"] != 2 {
		t.Errorf("both streams' year windows should have split, got %d", stats["windows_split"])
	}
	// "now" is 10 March, so the year window covers January to March: one probe
	// plus three month queries, per stream.
	if got := len(s.seen()); got != 8 {
		t.Errorf("expected 8 queries (2 probes + 6 months), got %d", got)
	}
	if len(records) != 6 {
		t.Errorf("expected 6 records from the month windows, got %d", len(records))
	}
	if stats["windows_truncated"] != 0 {
		t.Errorf("months were not over the cap; nothing should be truncated")
	}
}

// A single month over the cap cannot be subdivided further, so it must be
// reported rather than passed off as complete.
func TestMonthOverCapIsReportedAsTruncated(t *testing.T) {
	s := newStubWith(t, func(_ string, page int) (int, []string) {
		if page != 1 {
			return 5000, nil
		}
		return 5000, []string{item(1, true)}
	})
	c := newConnector(t, s.server.URL, 10)

	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := c.Sync(context.Background(), since, ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n := c.SyncStats()["windows_truncated"]; n == 0 {
		t.Error("expected truncation to be reported when a month exceeds the cap")
	}
}

// A bounded Sync must hand back a cursor and resume from it, so a long
// backfill makes progress instead of restarting.
func TestSyncResumesFromCursor(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 2)

	since := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	_, next, err := c.Sync(context.Background(), since, "")
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if next == "" {
		t.Fatal("expected a cursor after a bounded run")
	}
	if got := len(s.seen()); got != 2 {
		t.Fatalf("expected 2 queries in the first round, got %d", got)
	}

	var parsed cursor
	if err := json.Unmarshal([]byte(next), &parsed); err != nil {
		t.Fatalf("cursor is not valid JSON: %v", err)
	}
	if parsed.From != "2026-01-01" || parsed.Stream != streamMerged {
		t.Errorf("cursor should point at 2026 merged, got %+v", parsed)
	}

	if _, _, err := c.Sync(context.Background(), since, next); err != nil {
		t.Fatalf("resumed Sync: %v", err)
	}
	// The reviewed stream covers 2024 again legitimately — it is a separate
	// stream. What must not repeat is the merged window already done.
	for _, q := range s.seen()[2:] {
		if strings.Contains(q, "is:merged") && strings.Contains(q, "merged:2024-01-01") {
			t.Errorf("resumed run repeated the merged 2024 window: %q", q)
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
	if got := len(s.seen()); got != 2 {
		t.Errorf("a full first page should trigger a second request, got %d", got)
	}
	if len(records) != perPage {
		t.Errorf("expected %d records, got %d", perPage, len(records))
	}
}

// A backfill is several cursor-driven Sync calls. Stats describe the whole run,
// so they must accumulate rather than reset each call.
func TestSyncStatsAccumulateAcrossRounds(t *testing.T) {
	s := newStub(t, 1)
	c := newConnector(t, s.server.URL, 2)

	since := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	resume := ""
	for round := 0; round < 10; round++ {
		_, next, err := c.Sync(context.Background(), since, resume)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if next == "" {
			break
		}
		resume = next
	}

	stats := c.SyncStats()
	if stats["queries_issued"] != 6 {
		t.Errorf("queries_issued = %d across all rounds, want 6", stats["queries_issued"])
	}
	if stats["windows_searched"] != 6 {
		t.Errorf("windows_searched = %d across all rounds, want 6", stats["windows_searched"])
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
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("401 should be reported clearly, got: %v", err)
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

func TestBuildTasksIsYearAlignedPerStream(t *testing.T) {
	since := time.Date(2024, 6, 20, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)

	tasks := buildTasks(since, until)
	if len(tasks) != 6 {
		t.Fatalf("expected 6 tasks (3 years x 2 streams), got %d", len(tasks))
	}
	// Reaching back to 1 January deliberately overlaps `since`; re-ingesting is
	// a no-op, and the overlap is what stops silent gaps.
	if got := tasks[0].start; got.Day() != 1 || got.Month() != time.January || got.Year() != 2024 {
		t.Errorf("first window should start 1 January 2024, got %s", got)
	}
	// The final window must stop at `until`, not run to 31 December.
	if got := tasks[2].end; !got.Equal(until) {
		t.Errorf("last merged window should end at until (%s), got %s", until, got)
	}
	for _, task := range tasks[:3] {
		if task.stream != streamMerged {
			t.Errorf("expected the merged stream first, got %q", task.stream)
		}
	}
}
