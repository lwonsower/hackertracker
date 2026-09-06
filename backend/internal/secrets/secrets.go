// Package secrets resolves the pointers stored in source_accounts.credentials_ref
// into actual credentials.
//
// The database never holds a secret. Encrypting tokens in Postgres was
// considered and rejected: the encryption key would live in the environment
// anyway, so it would only defend against an attacker who can read the local
// database but not the local environment — close to nobody.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envNamePattern is the shape of a POSIX environment variable name.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// credentialPrefixes are the shapes of secrets people paste by mistake into a
// field that wants the *name* of a variable.
//
// Worth catching precisely rather than relying on the lookup failing: the name
// is echoed back in the "unset or empty" error, so a paste here would leak the
// credential into the browser and any log that captures responses.
var credentialPrefixes = []string{
	"github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", // GitHub
	"glpat-",              // GitLab
	"xox",                 // Slack
	"sk-", "sk_", "rk_",   // OpenAI, Stripe
	"AKIA", "ASIA",        // AWS
	"Bearer ",
}

// maxNameLength is generous for a variable name and far short of any real
// token, which is what makes length a usable signal on its own.
const maxNameLength = 64

func looksLikeCredential(name string) bool {
	for _, prefix := range credentialPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return len(name) > maxNameLength
}

// ErrPastedCredential is returned when the reference appears to be the secret
// itself. Deliberately says nothing about the value it saw.
var ErrPastedCredential = errors.New(
	"that looks like a token, not the name of an environment variable. " +
		"Put the token in .env.local (GITHUB_TOKEN=…), restart the server, " +
		"and enter the variable's name here instead. " +
		"If you pasted a real token, revoke it — it may now be in your browser history")

// Resolve turns a reference like "env:GITHUB_TOKEN" into the secret it names.
func Resolve(ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("no credentials configured for this source")
	}

	scheme, name, ok := strings.Cut(ref, ":")
	if !ok || name == "" {
		if looksLikeCredential(ref) {
			return "", ErrPastedCredential
		}
		return "", fmt.Errorf("credentials_ref %q is malformed; expected something like env:GITHUB_TOKEN", ref)
	}

	// Checked before anything else touches the value, and before it can be
	// echoed back in an error message.
	if looksLikeCredential(name) {
		return "", ErrPastedCredential
	}

	switch scheme {
	case "env":
		if !envNamePattern.MatchString(name) {
			return "", errors.New("that is not a valid environment variable name: use letters, digits and underscores, e.g. GITHUB_TOKEN")
		}
		value := os.Getenv(name)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is unset or empty", name)
		}
		return value, nil
	default:
		return "", fmt.Errorf("unsupported credentials scheme %q (only env: is supported)", scheme)
	}
}
