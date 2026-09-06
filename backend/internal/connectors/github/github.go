// Package github pulls merged pull requests and code reviews from GitHub.
//
// Everything here is shaped by three documented limits on the search API:
// 30 requests/minute authenticated, 100 results per page, and — the one that
// actually drives the design — a hard cap of 1,000 results per query. The cap
// is why a backfill is issued as one query per calendar month rather than one
// query for the whole range.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lwonsower/hackertracker/backend/internal/core"
)

const (
	DefaultBaseURL = "https://api.github.com"

	perPage            = 100
	maxResultsPerQuery = 1000 // GitHub's hard cap, not a pagination limit
	maxPages           = maxResultsPerQuery / perPage

	// 30 requests/minute means one every two seconds. Pacing by construction is
	// simpler than reading rate-limit headers and far simpler than backing off
	// after a 403.
	defaultSpacing = 2 * time.Second

	// Bounded work per Sync call, so a long backfill makes steady progress and
	// can resume rather than restarting. The runner calls Sync in a loop.
	defaultWindowsPerSync = 6
)

// streamMerged and streamReviewed are the two searches run per month window.
// They are kept as separate external ID prefixes so the same pull request can
// appear in both without colliding in raw_records.
const (
	streamMerged   = "merged"
	streamReviewed = "reviewed"
)

// Normalizer converts GitHub search items into events.
//
// Deliberately stateless and separate from Connector: normalising needs no
// credentials, so raw_records can be re-normalised later (for example to
// replace approximate review timestamps with real ones) without a token and
// without re-fetching anything.
type Normalizer struct{}

// Connector adds acquisition on top of Normalizer.
type Connector struct {
	Normalizer

	client         *http.Client
	token          string
	login          string
	baseURL        string
	spacing        time.Duration
	windowsPerSync int
	now            func() time.Time

	lastRequest time.Time
	stats       map[string]int
}

type Config struct {
	Login   string
	Token   string
	BaseURL string         // defaults to api.github.com; set for Enterprise Server
	Spacing *time.Duration // nil means the default pacing; tests set zero
	Client  *http.Client
	Now     func() time.Time

	// WindowsPerSync bounds the month windows covered by a single Sync call.
	// Zero means the default.
	WindowsPerSync int
}

func New(cfg Config) (*Connector, error) {
	if cfg.Login == "" {
		return nil, fmt.Errorf("github: login is required")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("github: token is required")
	}

	spacing := defaultSpacing
	if cfg.Spacing != nil {
		spacing = *cfg.Spacing
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}

	windows := cfg.WindowsPerSync
	if windows <= 0 {
		windows = defaultWindowsPerSync
	}

	return &Connector{
		client:         client,
		token:          cfg.Token,
		login:          cfg.Login,
		baseURL:        base,
		spacing:        spacing,
		windowsPerSync: windows,
		now:            now,
		stats:          map[string]int{},
	}, nil
}

// SyncStats reports what the last Sync examined, not just what it produced.
// A sync that searched twelve windows and found nothing is a very different
// situation from one that never issued a query, and the caller must be able to
// tell them apart.
func (c *Connector) SyncStats() map[string]int {
	out := make(map[string]int, len(c.stats))
	for k, v := range c.stats {
		out[k] = v
	}
	return out
}

// task is one search: one calendar month, one stream.
type task struct {
	start  time.Time
	stream string
}

// cursor marks the next task to run, so an interrupted backfill resumes.
type cursor struct {
	From   string `json:"from"` // YYYY-MM-DD, start of the month window
	Stream string `json:"stream"`
}

// Sync fetches a bounded number of month windows and returns a cursor when
// more remain.
//
// On error it returns no cursor at all, so the runner leaves the previously
// stored one in place and the next attempt simply repeats the failed window.
// Repeating work is free here: the unique index on
// (source_account_id, external_id, kind) makes re-ingesting a no-op.
func (c *Connector) Sync(ctx context.Context, since time.Time, cur string) ([]core.RawRecord, string, error) {
	// Stats accumulate across calls rather than resetting here. A backfill is
	// several Sync calls driven by the cursor, and the caller reports on the
	// run as a whole — resetting would make a 26-query backfill report as
	// however many queries the final round happened to issue.
	until := c.now().UTC()

	tasks := buildTasks(since.UTC(), until)
	if len(tasks) == 0 {
		return nil, "", nil
	}

	start := 0
	if cur != "" {
		var parsed cursor
		if err := json.Unmarshal([]byte(cur), &parsed); err != nil {
			// A cursor we can't read is not worth failing a sync over — the
			// window it pointed at gets redone, which costs nothing.
			start = 0
		} else {
			start = indexOf(tasks, parsed)
		}
	}

	var records []core.RawRecord
	for i := start; i < len(tasks); i++ {
		if i-start >= c.windowsPerSync {
			next, err := json.Marshal(cursor{
				From:   tasks[i].start.Format("2006-01-02"),
				Stream: tasks[i].stream,
			})
			if err != nil {
				return records, "", err
			}
			return records, string(next), nil
		}

		found, err := c.runTask(ctx, tasks[i], until)
		if err != nil {
			return nil, "", err
		}
		records = append(records, found...)
		c.stats["windows_searched"]++
	}

	return records, "", nil
}

// buildTasks produces one task per (calendar month x stream), oldest first.
// Starting at the first of the month means the earliest window reaches back
// before `since`. That overlap is intentional and free.
func buildTasks(since, until time.Time) []task {
	if since.After(until) {
		return nil
	}

	var tasks []task
	month := time.Date(since.Year(), since.Month(), 1, 0, 0, 0, 0, time.UTC)
	for !month.After(until) {
		tasks = append(tasks,
			task{start: month, stream: streamMerged},
			task{start: month, stream: streamReviewed},
		)
		month = month.AddDate(0, 1, 0)
	}
	return tasks
}

func indexOf(tasks []task, c cursor) int {
	for i, t := range tasks {
		if t.stream == c.Stream && t.start.Format("2006-01-02") == c.From {
			return i
		}
	}
	return 0
}

func (c *Connector) runTask(ctx context.Context, t task, until time.Time) ([]core.RawRecord, error) {
	last := t.start.AddDate(0, 1, 0).AddDate(0, 0, -1)
	if last.After(until) {
		last = until
	}
	q := c.query(t.stream, t.start, last)

	var out []core.RawRecord
	for page := 1; page <= maxPages; page++ {
		items, err := c.search(ctx, q, page)
		if err != nil {
			return nil, err
		}

		for _, item := range items {
			rec, ok := rawRecord(t.stream, item)
			if !ok {
				continue
			}
			out = append(out, rec)
		}
		c.stats["records_fetched"] += len(items)

		if len(items) < perPage {
			return out, nil
		}
		if page == maxPages {
			// More than 1,000 results in a single month means silent data loss.
			// Surface it rather than pretending the window is complete.
			c.stats["windows_truncated"]++
		}
	}
	return out, nil
}

func (c *Connector) query(stream string, from, to time.Time) string {
	window := from.Format("2006-01-02") + ".." + to.Format("2006-01-02")
	switch stream {
	case streamMerged:
		return fmt.Sprintf("is:pr author:%s is:merged merged:%s", c.login, window)
	default:
		// Exclude your own pull requests: commenting on your own PR is not a
		// code review, and it would double-count work already captured above.
		return fmt.Sprintf("is:pr reviewed-by:%s -author:%s updated:%s", c.login, c.login, window)
	}
}

type searchResponse struct {
	TotalCount int               `json:"total_count"`
	Items      []json.RawMessage `json:"items"`
}

func (c *Connector) search(ctx context.Context, q string, page int) ([]json.RawMessage, error) {
	if err := c.pace(ctx); err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("q", q)
	params.Set("per_page", fmt.Sprint(perPage))
	params.Set("page", fmt.Sprint(page))
	// Opt in to advanced search: the classic path now emits deprecation
	// warnings, and every qualifier used here is supported by both.
	params.Set("advanced_search", "true")

	endpoint := c.baseURL + "/search/issues?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	c.stats["queries_issued"]++

	if resp.StatusCode != http.StatusOK {
		return nil, describeFailure(resp)
	}

	var parsed searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("github search: could not decode response: %w", err)
	}
	return parsed.Items, nil
}

// pace spaces requests to stay inside 30/minute without tracking headers.
func (c *Connector) pace(ctx context.Context) error {
	if !c.lastRequest.IsZero() && c.spacing > 0 {
		if wait := c.spacing - time.Since(c.lastRequest); wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	c.lastRequest = time.Now()
	return nil
}

// describeFailure turns GitHub's status codes into something actionable,
// because the failure that matters most here is the quiet one.
func describeFailure(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("github rejected the token (401): check the value of the environment variable named in credentials_ref")
	case http.StatusForbidden:
		if retry := resp.Header.Get("Retry-After"); retry != "" {
			return fmt.Errorf("github rate limit hit (403): retry after %s seconds", retry)
		}
		return fmt.Errorf("github refused the request (403): the token may lack the scopes needed, or an org may require SSO authorisation for it")
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("github rejected the search query (422): this is a bug in the connector's query construction")
	default:
		return fmt.Errorf("github search returned %s", resp.Status)
	}
}
