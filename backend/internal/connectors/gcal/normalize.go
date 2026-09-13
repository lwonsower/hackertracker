package gcal

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lwonsower/hackertracker/backend/internal/core"
)

// Kind is what a promoted meeting becomes on the timeline.
const Kind = "meeting"

// Promoted is what gets written when a person picks a meeting: the calendar's
// facts plus the line they wrote about what came of it.
type Promoted struct {
	Event
	Note string `json:"note,omitempty"`
}

// Normalizer turns a promoted meeting into an event. Like every normaliser it
// needs no credentials, so a raw record can be re-normalised later without
// touching Google.
type Normalizer struct{}

func (Normalizer) Normalize(r core.RawRecord) ([]core.Event, error) {
	var p Promoted
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return nil, fmt.Errorf("not a promoted calendar event: %w", err)
	}
	if p.ID == "" {
		return nil, core.ValidationError{Msg: "calendar event has no id"}
	}
	if p.Start.IsZero() {
		return nil, core.ValidationError{Msg: "calendar event has no start time"}
	}

	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}

	return []core.Event{{
		ID:              core.EventID(r.SourceAccountID, p.ID, Kind),
		SourceAccountID: r.SourceAccountID,
		ExternalID:      p.ID,
		Kind:            Kind,
		Title:           p.Title,
		URL:             p.URL,
		OccurredAt:      p.Start.UTC(),
		Payload:         json.RawMessage(payload),
	}}, nil
}

// Minutes is the meeting's length, which is evidence in itself: a three-hour
// design review and a fifteen-minute sync are not the same fact.
func (p Promoted) Minutes() int {
	if p.End.IsZero() || !p.End.After(p.Start) {
		return 0
	}
	return int(p.End.Sub(p.Start) / time.Minute)
}
