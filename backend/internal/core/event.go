// Package core holds the domain types every ingest path shares.
//
// The important idea here is that there is only one write path. A pulled
// GitHub PR, a webhook delivery, a CSV row and something typed into the form
// all become a RawRecord, get normalised into Events, and land in the same
// table. Sources differ only in how their raw records are produced.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// eventNamespace seeds the deterministic event UUIDs. It must never change:
// every event ID in every database is derived from it, so altering it would
// orphan every annotation, tag and project membership ever written.
var eventNamespace = uuid.MustParse("9a5b0f8c-4c1e-4f0a-8b3d-6f2e1c7a9d40")

// EventID derives a stable UUIDv5 from an event's natural key.
//
// Note what this does and does not buy, because it is easy to overclaim:
//
//   - It does NOT provide idempotency on its own. Safe redelivery comes from
//     the unique index on (source_account_id, external_id, kind) plus ON
//     CONFLICT, which would work just as well with random IDs.
//   - It DOES let a producer compute an event's ID without touching the
//     database, so batches can be cross-referenced before they are written.
//   - It DOES make a rebuild reproducible: re-normalising raw_records into a
//     fresh database yields byte-identical IDs, so a curation-layer export
//     keyed by event_id can be re-attached, and the same event has the same ID
//     on a laptop and on a future hosted instance.
//
// What it emphatically does not make safe is deleting rows. annotations,
// event_tags and project_events are ON DELETE CASCADE, so `truncate events` or
// `delete from events` destroys the irreplaceable half of the database.
// Re-normalisation is an UPSERT PASS over raw_records — never a rebuild.
func EventID(sourceAccountID uuid.UUID, externalID, kind string) uuid.UUID {
	// NUL separators so ("ab", "c") and ("a", "bc") cannot collide.
	key := sourceAccountID.String() + "\x00" + externalID + "\x00" + kind
	return uuid.NewSHA1(eventNamespace, []byte(key))
}

// RawRecord is a payload exactly as it arrived, before anyone has interpreted
// it. Kept forever: you can re-normalise later, but you cannot re-fetch after
// the token dies — which is precisely the situation this tool exists for.
type RawRecord struct {
	SourceAccountID uuid.UUID
	ExternalID      string
	Payload         json.RawMessage
}

// ContentHash identifies this exact version of the payload, so re-fetching an
// unchanged record is a no-op while a genuine edit upstream is kept as a new
// row alongside the old one.
func (r RawRecord) ContentHash() string {
	sum := sha256.Sum256(r.Payload)
	return hex.EncodeToString(sum[:])
}

// Event is the normalised spine. Deliberately narrow: anything source-specific
// belongs in Payload, not in a new column.
type Event struct {
	ID              uuid.UUID       `json:"id"`
	SourceAccountID uuid.UUID       `json:"-"`
	ExternalID      string          `json:"external_id"`
	Kind            string          `json:"kind"`
	SubjectKey      string          `json:"subject_key,omitempty"`
	Title           string          `json:"title"`
	URL             string          `json:"url,omitempty"`
	OccurredAt      time.Time       `json:"occurred_at"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// Normalizer turns raw payloads into events. Every source has one.
type Normalizer interface {
	Normalize(RawRecord) ([]Event, error)
}

// Fetcher pulls raw records from a remote API. Only pull sources implement it —
// push sources receive their raw records over HTTP instead, which is the whole
// reason this is a separate interface from Normalizer rather than one big
// Connector. Everything downstream of raw_records is identical either way.
type Fetcher interface {
	Sync(ctx context.Context, since time.Time, cursor string) (records []RawRecord, next string, err error)
}
