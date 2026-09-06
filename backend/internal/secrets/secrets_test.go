package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveEnv(t *testing.T) {
	t.Setenv("HT_TEST_TOKEN", "s3cret")

	got, err := Resolve("env:HT_TEST_TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("got %q", got)
	}
}

func TestResolveRejectsBadRefs(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"no scheme":         "GITHUB_TOKEN",
		"unknown scheme":    "vault:secret/github",
		"unset env var":     "env:HT_DEFINITELY_UNSET",
		"scheme with no id": "env:",
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Resolve(ref); err == nil {
				t.Errorf("expected an error for %q", ref)
			}
		})
	}
}

// Pasting a token where a variable name belongs must be caught before the
// value can be echoed back in an error message.
func TestResolveRejectsPastedCredentials(t *testing.T) {
	pasted := []string{
		"env:github_pat_11AEAWVAA0FVnj9aVqkWqxXQMevouUBqHFWwre498Jcv6OMSbBNzoujdgy6Dpw6",
		"env:ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"env:glpat-abcdefghijklmnopqrst",
		"env:xoxb-1234-5678-abcdefghijklmnop",
		"github_pat_11AEAWVAA0FVnj9aVqkWqxXQMevouUBqHFWwre498Jcv6OMSbBNzoujdgy6Dpw6",
	}
	for _, ref := range pasted {
		err := mustFail(t, ref)
		if !errors.Is(err, ErrPastedCredential) {
			t.Errorf("%.20s…: expected ErrPastedCredential, got %v", ref, err)
		}
		// The whole point is not leaking the value onward.
		if strings.Contains(err.Error(), "github_pat_") || strings.Contains(err.Error(), "ghp_") ||
			strings.Contains(err.Error(), "glpat-") || strings.Contains(err.Error(), "xoxb-") {
			t.Errorf("error echoed the pasted credential: %v", err)
		}
	}
}

func TestResolveRejectsInvalidNames(t *testing.T) {
	for _, ref := range []string{"env:has space", "env:1STARTS_WITH_DIGIT", "env:has-dash"} {
		if err := mustFail(t, ref); errors.Is(err, ErrPastedCredential) {
			t.Errorf("%q should be a name-shape error, not a pasted-credential one", ref)
		}
	}
}

// A normal typo should still name the variable, since that is what makes the
// error useful.
func TestResolveEchoesOrdinaryNames(t *testing.T) {
	err := mustFail(t, "env:GITHUB_TOKN")
	if !strings.Contains(err.Error(), "GITHUB_TOKN") {
		t.Errorf("expected the variable name in the error, got %v", err)
	}
}

func mustFail(t *testing.T, ref string) error {
	t.Helper()
	_, err := Resolve(ref)
	if err == nil {
		t.Fatalf("expected %q to be rejected", ref)
	}
	return err
}
