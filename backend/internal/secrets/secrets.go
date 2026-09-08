// Package secrets resolves the pointers stored in
// source_accounts.credentials_ref into actual credentials.
//
// Two schemes:
//
//	env:NAME     an environment variable. Self-host only — with several
//	             accounts a shared variable would let one read another's data.
//	secret:UUID  a row in the credentials table, encrypted at rest.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// credentialPrefixes are the shapes of secrets people paste by mistake into a
// field that wants the name of a variable.
var credentialPrefixes = []string{
	"github_pat_", "ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"glpat-",
	"xox",
	"sk-", "sk_", "rk_",
	"AKIA", "ASIA",
	"Bearer ",
}

const maxNameLength = 64

func looksLikeCredential(name string) bool {
	for _, prefix := range credentialPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return len(name) > maxNameLength
}

var ErrPastedCredential = errors.New(
	"that looks like a token, not the name of an environment variable. " +
		"Untick the environment-variable option and paste the token itself; " +
		"it is encrypted before it is stored. " +
		"If you pasted a real token here, revoke it — it may now be in your browser history")

// Store reads encrypted credentials. Implemented by an account-scoped
// store.Store, so row-level security applies to credential lookup too.
type Store interface {
	Credential(ctx context.Context, id uuid.UUID) (keyID string, ciphertext []byte, err error)
}

// Resolver turns credential references into credentials.
type Resolver struct {
	AllowEnv bool

	keys     map[string][]byte
	activeID string
}

// NewResolver parses a key specification: "id:base64key[,id:base64key…]".
// The first key encrypts; the rest only decrypt, so a key can be rotated
// without rewriting every row at once.
func NewResolver(allowEnv bool, spec string) (Resolver, error) {
	r := Resolver{AllowEnv: allowEnv, keys: map[string][]byte{}}
	if strings.TrimSpace(spec) == "" {
		return r, nil
	}

	for i, entry := range strings.Split(spec, ",") {
		id, encoded, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok || id == "" {
			return Resolver{}, fmt.Errorf("credentials key %d is malformed; expected id:base64key", i+1)
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return Resolver{}, fmt.Errorf("credentials key %q is not valid base64: %w", id, err)
		}
		if len(key) != 32 {
			return Resolver{}, fmt.Errorf("credentials key %q is %d bytes; AES-256 needs 32", id, len(key))
		}
		r.keys[id] = key
		if i == 0 {
			r.activeID = id
		}
	}
	return r, nil
}

// CanEncrypt reports whether a key is configured.
func (r Resolver) CanEncrypt() bool { return r.activeID != "" }

// Seal encrypts a credential for one account. The account ID is authenticated
// additional data, so a ciphertext copied into another account's row fails to
// decrypt rather than leaking.
func (r Resolver) Seal(accountID uuid.UUID, plaintext string) (keyID string, ciphertext []byte, err error) {
	if !r.CanEncrypt() {
		return "", nil, errors.New("no credentials encryption key is configured (set CREDENTIALS_KEY)")
	}
	gcm, err := newGCM(r.keys[r.activeID])
	if err != nil {
		return "", nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, err
	}
	return r.activeID, gcm.Seal(nonce, nonce, []byte(plaintext), accountID[:]), nil
}

func (r Resolver) open(accountID uuid.UUID, keyID string, ciphertext []byte) (string, error) {
	key, ok := r.keys[keyID]
	if !ok {
		return "", fmt.Errorf("credential was encrypted with key %q, which is not configured", keyID)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("stored credential is truncated")
	}

	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, accountID[:])
	if err != nil {
		return "", errors.New("stored credential could not be decrypted")
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Resolve turns a reference into the secret it names. store may be nil when
// only env: references are expected.
func (r Resolver) Resolve(ctx context.Context, store Store, accountID uuid.UUID, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("no credentials configured for this source")
	}

	scheme, name, ok := strings.Cut(ref, ":")
	if !ok || name == "" {
		if looksLikeCredential(ref) {
			return "", ErrPastedCredential
		}
		return "", fmt.Errorf("credentials_ref %q is malformed", ref)
	}

	switch scheme {
	case "secret":
		if store == nil {
			return "", errors.New("cannot read stored credentials here")
		}
		id, err := uuid.Parse(name)
		if err != nil {
			return "", errors.New("credentials_ref is not a valid credential id")
		}
		keyID, ciphertext, err := store.Credential(ctx, id)
		if err != nil {
			return "", err
		}
		return r.open(accountID, keyID, ciphertext)

	case "env":
		if !r.AllowEnv {
			return "", errors.New(
				"environment-variable credentials are disabled on this deployment, because a " +
					"shared variable would let one account read another's data. Store the token " +
					"on the source instead")
		}
		if looksLikeCredential(name) {
			return "", ErrPastedCredential
		}
		if !envNamePattern.MatchString(name) {
			return "", errors.New("that is not a valid environment variable name: use letters, digits and underscores, e.g. GITHUB_TOKEN")
		}
		value := os.Getenv(name)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is unset or empty", name)
		}
		return value, nil

	default:
		return "", fmt.Errorf("unsupported credentials scheme %q", scheme)
	}
}
