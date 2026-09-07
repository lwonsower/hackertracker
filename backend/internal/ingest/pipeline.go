// Package ingest is the single write path into the events table.
//
// Pull connectors, webhook deliveries, file imports and the manual-entry form
// all funnel through Pipeline.Ingest. They differ only in how their RawRecords
// are produced; everything from raw_records onward is identical.
package ingest

import (
	"context"
	"fmt"

	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/store"
)

// Pipeline routes raw records to the right normaliser and persists the result.
type Pipeline struct {
	normalizers map[string]core.Normalizer
	fallback    core.Normalizer
}

func New() *Pipeline {
	return &Pipeline{
		normalizers: map[string]core.Normalizer{},
		// Anything without a registered normaliser is expected to speak the
		// strict envelope — which is every push, manual and import source.
		fallback: core.EnvelopeNormalizer{},
	}
}

// Register attaches a source-specific normaliser, e.g. one that understands
// GitHub's PR JSON. This is the only thing a new pull connector has to add on
// the write side.
func (p *Pipeline) Register(source string, n core.Normalizer) {
	p.normalizers[source] = n
}

func (p *Pipeline) normalizerFor(source string) core.Normalizer {
	if n, ok := p.normalizers[source]; ok {
		return n
	}
	return p.fallback
}

// Result reports what a batch did. Updated counts redeliveries and genuine
// upstream edits alike, which is the behaviour that makes retries safe.
type Result struct {
	Received int `json:"received"`
	Created  int `json:"created"`
	Updated  int `json:"updated"`
}

// Ingest stores raw records and upserts the events they normalise to.
//
// The Store is passed in rather than held, because a Store only exists inside
// an account scope — which is what guarantees these writes land in the right
// account and cannot land in anyone else's.
//
// A batch is all-or-nothing: with a strict envelope a bad record is a caller
// error, and half-applying a payload someone will retry is worse than
// rejecting the whole thing.
func (p *Pipeline) Ingest(ctx context.Context, scoped *store.Store, acct store.SourceAccount, raws []core.RawRecord) (Result, error) {
	res := Result{Received: len(raws)}
	normalizer := p.normalizerFor(acct.Source)

	err := scoped.WithTx(ctx, func(st *store.Store) error {
		for _, raw := range raws {
			raw.SourceAccountID = acct.ID

			if err := st.InsertRawRecord(ctx, raw); err != nil {
				return fmt.Errorf("store raw record %q: %w", raw.ExternalID, err)
			}

			events, err := normalizer.Normalize(raw)
			if err != nil {
				return err // Typically a core.ValidationError; the API layer 400s it.
			}

			for _, e := range events {
				created, err := st.UpsertEvent(ctx, e)
				if err != nil {
					return fmt.Errorf("upsert event %q: %w", e.ExternalID, err)
				}
				if created {
					res.Created++
				} else {
					res.Updated++
				}
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}
