package core

import (
	"testing"

	"github.com/google/uuid"
)

// The whole curation layer hangs off these IDs being reproducible.
func TestEventIDIsDeterministic(t *testing.T) {
	acct := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	first := EventID(acct, "pr:acme/api#1", "pr_merged")
	second := EventID(acct, "pr:acme/api#1", "pr_merged")
	if first != second {
		t.Errorf("same natural key produced different IDs: %s vs %s", first, second)
	}
}

func TestEventIDSeparatesItsComponents(t *testing.T) {
	a := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	b := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	cases := []struct{ name string; x, y uuid.UUID }{
		{"account", EventID(a, "x", "k"), EventID(b, "x", "k")},
		{"external id", EventID(a, "x", "k"), EventID(a, "y", "k")},
		{"kind", EventID(a, "x", "k"), EventID(a, "x", "j")},
		// Without separators, ("ab","c") and ("a","bc") would concatenate alike.
		{"boundary", EventID(a, "ab", "c"), EventID(a, "a", "bc")},
	}
	for _, c := range cases {
		if c.x == c.y {
			t.Errorf("%s: differing inputs produced the same ID", c.name)
		}
	}
}
