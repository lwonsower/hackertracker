package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Envelope is the strict wire format the generic ingest endpoint accepts.
//
// Strict on purpose: callers send our shape, and transforming whatever their
// tool emits into it is their job (a Zapier step, a few lines of script). The
// alternative — per-endpoint mapping expressions over arbitrary JSON — means
// building a low-code ETL product and debugging other people's JQ. That can
// come later, built against real payloads collected in ingest_log, rather than
// designed blind now.
type Envelope struct {
	ExternalID string          `json:"external_id"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	OccurredAt time.Time       `json:"occurred_at"`
	URL        string          `json:"url,omitempty"`
	SubjectKey string          `json:"subject_key,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// ValidationError is a payload problem the caller must fix, as opposed to
// something that went wrong on our side. The HTTP layer turns these into 400s.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Normalize trims and validates the envelope in place, returning the cleaned
// copy. Kept separate from the interface method so manual entry can reuse it.
func (e Envelope) Normalize() (Envelope, error) {
	e.ExternalID = strings.TrimSpace(e.ExternalID)
	e.Kind = strings.TrimSpace(e.Kind)
	e.Title = strings.TrimSpace(e.Title)
	e.URL = strings.TrimSpace(e.URL)
	e.SubjectKey = strings.TrimSpace(e.SubjectKey)

	// external_id carries the entire idempotency story. Webhook delivery is
	// at-least-once, so without a stable caller-supplied ID every retry becomes
	// a duplicate accomplishment. Refusing the record is friendlier than
	// silently triple-logging it.
	if e.ExternalID == "" {
		return e, invalid("external_id is required: it must be stable across redeliveries of the same record")
	}
	if e.Kind == "" {
		return e, invalid("kind is required (e.g. pr_merged, doc_published, note)")
	}
	if e.Title == "" {
		return e, invalid("title is required")
	}
	if e.OccurredAt.IsZero() {
		return e, invalid("occurred_at is required and must be an RFC 3339 timestamp")
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage(`{}`)
	}
	return e, nil
}

// Event converts a validated envelope into an event for the given source.
func (e Envelope) Event(sourceAccountID uuid.UUID) Event {
	return Event{
		ID:              EventID(sourceAccountID, e.ExternalID, e.Kind),
		SourceAccountID: sourceAccountID,
		ExternalID:      e.ExternalID,
		Kind:            e.Kind,
		SubjectKey:      e.SubjectKey,
		Title:           e.Title,
		URL:             e.URL,
		OccurredAt:      e.OccurredAt.UTC(),
		Payload:         e.Payload,
	}
}

// EnvelopeNormalizer is the default normaliser, used by every push and manual
// source. Pull connectors register their own instead.
type EnvelopeNormalizer struct{}

func (EnvelopeNormalizer) Normalize(r RawRecord) ([]Event, error) {
	var env Envelope
	if err := json.Unmarshal(r.Payload, &env); err != nil {
		return nil, invalid("payload is not a valid ingest envelope: %v", err)
	}
	env, err := env.Normalize()
	if err != nil {
		return nil, err
	}
	return []Event{env.Event(r.SourceAccountID)}, nil
}
