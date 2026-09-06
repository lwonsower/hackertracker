// Package syncer drives pull connectors: resolve credentials, ask the Fetcher
// for records, hand them to the ingest pipeline, record what happened.
//
// There is no scheduler here on purpose. Syncs run on demand and once at
// startup. The server only exists while it is running, so a ticker would sync
// only while you happen to be developing — and because backfill takes a deep
// `since`, a sync that has not run in three weeks simply catches up.
package syncer

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/ingest"
	"github.com/lwonsower/hackertracker/backend/internal/secrets"
	"github.com/lwonsower/hackertracker/backend/internal/store"
)

// BuildFunc constructs a Fetcher for one configured account. Registering one
// of these is all a new pull connector has to do on this side.
type BuildFunc func(acct store.SourceAccount, token string) (core.Fetcher, error)

// statsReporter is optional. A Fetcher that implements it can say what it
// examined, which is what makes an empty sync distinguishable from a broken one.
type statsReporter interface {
	SyncStats() map[string]int
}

type Runner struct {
	store    *store.Store
	pipeline *ingest.Pipeline
	builders map[string]BuildFunc

	// overlap re-covers ground on every sync. Search indexes are eventually
	// consistent and clocks drift, so an exact `since` boundary loses records
	// silently. Overlapping costs nothing: re-ingesting is a no-op.
	overlap time.Duration

	// BackfillYears is how far the very first sync reaches.
	//
	// Ten rather than one: this tool exists to reconstruct a career's evidence,
	// and someone whose last merged pull request was two years ago still needs
	// it. A one-year default silently returned nothing for exactly that case.
	// Year-sized search windows keep the cost of the wider default low.
	BackfillYears int

	// maxRounds bounds one Sync call. Fetchers return a cursor and expect to be
	// called repeatedly; this stops a bug in one from looping forever.
	maxRounds int
}

func New(st *store.Store, p *ingest.Pipeline) *Runner {
	return &Runner{
		store:     st,
		pipeline:  p,
		builders:  map[string]BuildFunc{},
		overlap:       24 * time.Hour,
		BackfillYears: 10,
		maxRounds:     128,
	}
}

func (r *Runner) Register(source string, build BuildFunc) {
	r.builders[source] = build
}

// Report describes a sync in terms of what it looked at, not only what it
// produced. "Found nothing" and "never issued a query" must not look alike.
type Report struct {
	SourceAccountID uuid.UUID      `json:"source_account_id"`
	Label           string         `json:"label"`
	Since           time.Time      `json:"since"`
	Rounds          int            `json:"rounds"`
	Complete        bool           `json:"complete"`
	Examined        map[string]int `json:"examined,omitempty"`
	Created         int            `json:"created"`
	Updated         int            `json:"updated"`
	Notes           []string       `json:"notes,omitempty"`
}

// Options tunes a single run.
type Options struct {
	// Since overrides where a backfill starts. Set when the user asks for a
	// specific range; nil means "since the last success, or the default
	// backfill if there has never been one".
	Since *time.Time
}

func (r *Runner) Sync(ctx context.Context, acct store.SourceAccount, opts Options) (Report, error) {
	report := Report{SourceAccountID: acct.ID, Label: acct.Label}

	build, ok := r.builders[acct.Source]
	if !ok {
		return report, fmt.Errorf("no connector registered for source %q", acct.Source)
	}

	token, err := secrets.Resolve(acct.CredentialsRef)
	if err != nil {
		return report, r.fail(ctx, acct.ID, err)
	}

	state, err := r.store.GetSyncState(ctx, acct.ID)
	if err != nil {
		return report, err
	}

	startedAt := time.Now().UTC()
	since := startedAt.AddDate(-r.BackfillYears, 0, 0)
	if state.LastSyncedAt != nil {
		since = state.LastSyncedAt.Add(-r.overlap)
	}
	// An explicit request wins over both, which is how you re-reach history
	// that a previous successful sync has already moved the watermark past.
	if opts.Since != nil {
		since = opts.Since.UTC()
	}
	report.Since = since

	fetcher, err := build(acct, token)
	if err != nil {
		return report, r.fail(ctx, acct.ID, err)
	}

	cursor := state.Cursor
	for round := 0; round < r.maxRounds; round++ {
		records, next, err := fetcher.Sync(ctx, since, cursor)
		if err != nil {
			return report, r.fail(ctx, acct.ID, err)
		}
		report.Rounds++

		if len(records) > 0 {
			res, err := r.pipeline.Ingest(ctx, acct, records)
			if err != nil {
				return report, r.fail(ctx, acct.ID, err)
			}
			report.Created += res.Created
			report.Updated += res.Updated
		}

		if next == "" {
			report.Complete = true
			break
		}

		// Persist the cursor between rounds so an interrupted backfill resumes
		// where it stopped instead of starting over.
		cursor = next
		if err := r.store.SaveSyncState(ctx, acct.ID, store.SyncState{Cursor: next}); err != nil {
			return report, err
		}
	}

	if sr, ok := fetcher.(statsReporter); ok {
		report.Examined = sr.SyncStats()
	}

	final := store.SyncState{}
	if report.Complete {
		final.LastSyncedAt = &startedAt
	} else {
		final.Cursor = cursor
	}
	if err := r.store.SaveSyncState(ctx, acct.ID, final); err != nil {
		return report, err
	}

	report.Notes = notes(report)
	return report, nil
}

// SyncAll runs every configured pull account, collecting failures rather than
// stopping: one broken source should not prevent the others from updating.
func (r *Runner) SyncAll(ctx context.Context) ([]Report, error) {
	accounts, err := r.store.ListPullAccounts(ctx)
	if err != nil {
		return nil, err
	}

	reports := make([]Report, 0, len(accounts))
	for _, acct := range accounts {
		report, err := r.Sync(ctx, acct, Options{})
		if err != nil {
			report.Notes = append(report.Notes, err.Error())
			log.Printf("sync %s: %v", acct.Label, err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// fail records the error against the account so it is visible in the UI rather
// than only in the logs, and returns it unchanged.
func (r *Runner) fail(ctx context.Context, id uuid.UUID, cause error) error {
	if err := r.store.SaveSyncState(ctx, id, store.SyncState{LastError: cause.Error()}); err != nil {
		log.Printf("could not record sync failure: %v", err)
	}
	return cause
}

func notes(r Report) []string {
	var out []string

	if r.Created+r.Updated == 0 {
		// The quiet failure this whole design guards against: a token that can
		// see nothing produces a successful-looking sync with no events.
		//
		// Naming the window first, because "nothing in this range" is the more
		// common cause and the cheaper one to check. An earlier version led
		// with scopes and SSO, which sent someone hunting a permissions problem
		// when their most recent merged pull request was simply older than the
		// range being searched.
		out = append(out, fmt.Sprintf(
			"Matched nothing across %d queries, searching back to %s. "+
				"If your work is older than that, sync again with an earlier start date. "+
				"Otherwise check the token can see the repositories you have in mind — "+
				"private repos need access granted, and organisations with SSO must "+
				"authorise the token explicitly.",
			r.Examined["queries_issued"], r.Since.Format("2 January 2006")))
	}
	if !r.Complete {
		out = append(out, "Stopped before finishing the backfill. Run sync again to continue where it left off.")
	}
	if n := r.Examined["windows_truncated"]; n > 0 {
		out = append(out, fmt.Sprintf(
			"%d month(s) hit GitHub's 1,000-result cap, so some records in those months were not returned.", n))
	}
	if n := r.Examined["windows_split"]; n > 0 {
		out = append(out, fmt.Sprintf(
			"%d year(s) held more than one query can return and were re-fetched month by month.", n))
	}
	return out
}
