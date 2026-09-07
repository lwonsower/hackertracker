package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Everything in this file is UNSCOPED: it runs as the connecting role against
// tables that carry no row-level security.
//
// That is deliberate, not an oversight. These are the queries that *establish*
// a scope — resolving a session cookie to a user, or an ingest token to a
// source account — so they cannot themselves run inside one without a
// chicken-and-egg problem. The tradeoff is that they are the one place where a
// mistake is not caught by the database, so keep this file small and obvious.

// ── ingest tokens ────────────────────────────────────────────────────────

// SourceAccountByIngestToken resolves a webhook token to its source account,
// which is what tells the caller whose scope to open.
func (d *DB) SourceAccountByIngestToken(ctx context.Context, token string) (SourceAccount, error) {
	a, err := scanSourceAccount(d.pool.QueryRow(ctx,
		`select `+sourceAccountCols+` from source_accounts where ingest_token_hash = $1`,
		hashToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceAccount{}, ErrNotFound
	}
	return a, err
}

// ── users and sessions ───────────────────────────────────────────────────

type User struct {
	ID        uuid.UUID `json:"id"`
	AccountID uuid.UUID `json:"account_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name,omitempty"`
	AvatarURL string    `json:"avatar_url,omitempty"`
}

// Identity is what a provider tells us about whoever just signed in.
type Identity struct {
	Provider  string
	Subject   string
	Email     string
	Name      string
	AvatarURL string
}

// UpsertUser resolves a provider identity to a user, creating one if needed.
//
// Three cases, in order:
//
//  1. The identity is already linked — return its user. The ordinary path.
//  2. No identity, but the email matches an existing user — link this provider
//     to that user. This is what stops signing in with a second provider from
//     silently creating a duplicate account holding none of your history.
//  3. Nobody matches — create an account and a user.
//
// In case 3, adoptOrphanAccount lets a self-hosted instance claim an account
// that has data but no users yet. That is how the rows migrated from the
// single-user era find an owner instead of being stranded. It must stay off for
// hosted deployments, where it would hand a stranger someone else's data.
func (d *DB) UpsertUser(ctx context.Context, id Identity, adoptOrphanAccount bool) (User, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var u User
	err = tx.QueryRow(ctx, `
		select u.id, u.account_id, u.email, coalesce(u.name, ''), coalesce(u.avatar_url, '')
		from user_identities i
		join users u on u.id = i.user_id
		where i.provider = $1 and i.subject = $2`,
		id.Provider, id.Subject,
	).Scan(&u.ID, &u.AccountID, &u.Email, &u.Name, &u.AvatarURL)

	switch {
	case err == nil:
		// Known identity. Refresh the profile, which may have changed upstream.
		if _, err := tx.Exec(ctx, `
			update users set name = coalesce(nullif($2, ''), name),
			                 avatar_url = coalesce(nullif($3, ''), avatar_url),
			                 last_seen_at = now()
			where id = $1`, u.ID, id.Name, id.AvatarURL); err != nil {
			return User{}, err
		}

	case errors.Is(err, pgx.ErrNoRows):
		if id.Email == "" {
			return User{}, fmt.Errorf("%s did not provide an email address", id.Provider)
		}

		err = tx.QueryRow(ctx, `
			select id, account_id, email, coalesce(name, ''), coalesce(avatar_url, '')
			from users where lower(email) = lower($1)`, id.Email,
		).Scan(&u.ID, &u.AccountID, &u.Email, &u.Name, &u.AvatarURL)

		if errors.Is(err, pgx.ErrNoRows) {
			accountID, err := resolveAccount(ctx, tx, id, adoptOrphanAccount)
			if err != nil {
				return User{}, err
			}
			if err := tx.QueryRow(ctx, `
				insert into users (account_id, email, name, avatar_url, last_seen_at)
				values ($1, $2, nullif($3, ''), nullif($4, ''), now())
				returning id, account_id, email, coalesce(name, ''), coalesce(avatar_url, '')`,
				accountID, id.Email, id.Name, id.AvatarURL,
			).Scan(&u.ID, &u.AccountID, &u.Email, &u.Name, &u.AvatarURL); err != nil {
				return User{}, err
			}
		} else if err != nil {
			return User{}, err
		}

		if _, err := tx.Exec(ctx, `
			insert into user_identities (user_id, provider, subject, email)
			values ($1, $2, $3, nullif($4, ''))
			on conflict (provider, subject) do nothing`,
			u.ID, id.Provider, id.Subject, id.Email); err != nil {
			return User{}, err
		}

	default:
		return User{}, err
	}

	return u, tx.Commit(ctx)
}

func resolveAccount(ctx context.Context, tx pgx.Tx, id Identity, adoptOrphan bool) (uuid.UUID, error) {
	if adoptOrphan {
		var orphan uuid.UUID
		err := tx.QueryRow(ctx, `
			select a.id from accounts a
			where not exists (select 1 from users u where u.account_id = a.id)
			order by a.created_at
			limit 1`).Scan(&orphan)
		if err == nil {
			return orphan, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, err
		}
	}

	name := id.Name
	if name == "" {
		name = id.Email
	}
	var created uuid.UUID
	err := tx.QueryRow(ctx, `insert into accounts (name) values ($1) returning id`, name).Scan(&created)
	return created, err
}

// ── sessions ─────────────────────────────────────────────────────────────

// CreateSession returns the bearer token exactly once; only its hash is stored,
// so a database leak does not hand over live sessions.
func (d *DB) CreateSession(ctx context.Context, userID uuid.UUID, ttl time.Duration, userAgent string) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	expires := time.Now().UTC().Add(ttl)

	if _, err := d.pool.Exec(ctx, `
		insert into sessions (user_id, token_hash, expires_at, user_agent, last_used_at)
		values ($1, $2, $3, nullif($4, ''), now())`,
		userID, hashToken(token), expires, userAgent); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// UserBySessionToken resolves a cookie to its user, or ErrNotFound if the
// session is unknown or expired.
func (d *DB) UserBySessionToken(ctx context.Context, token string) (User, error) {
	var u User
	err := d.pool.QueryRow(ctx, `
		update sessions set last_used_at = now()
		where token_hash = $1 and expires_at > now()
		returning user_id`, hashToken(token),
	).Scan(&u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}

	err = d.pool.QueryRow(ctx, `
		select id, account_id, email, coalesce(name, ''), coalesce(avatar_url, '')
		from users where id = $1`, u.ID,
	).Scan(&u.ID, &u.AccountID, &u.Email, &u.Name, &u.AvatarURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (d *DB) DeleteSession(ctx context.Context, token string) error {
	_, err := d.pool.Exec(ctx, `delete from sessions where token_hash = $1`, hashToken(token))
	return err
}

// ── OAuth handshake state ────────────────────────────────────────────────

type AuthState struct {
	State      string
	Nonce      string
	Verifier   string
	RedirectTo string
}

func (d *DB) SaveAuthState(ctx context.Context, s AuthState, ttl time.Duration) error {
	_, err := d.pool.Exec(ctx, `
		insert into auth_states (state, nonce, verifier, redirect_to, expires_at)
		values ($1, $2, $3, nullif($4, ''), $5)`,
		s.State, s.Nonce, s.Verifier, s.RedirectTo, time.Now().UTC().Add(ttl))
	return err
}

// ConsumeAuthState deletes and returns the state in one statement, so a
// replayed callback finds nothing.
func (d *DB) ConsumeAuthState(ctx context.Context, state string) (AuthState, error) {
	var s AuthState
	err := d.pool.QueryRow(ctx, `
		delete from auth_states
		where state = $1 and expires_at > now()
		returning state, nonce, verifier, coalesce(redirect_to, '')`, state,
	).Scan(&s.State, &s.Nonce, &s.Verifier, &s.RedirectTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthState{}, ErrNotFound
	}
	return s, err
}

// PurgeExpired clears finished sessions and abandoned handshakes.
func (d *DB) PurgeExpired(ctx context.Context) error {
	if _, err := d.pool.Exec(ctx, `delete from sessions where expires_at < now()`); err != nil {
		return err
	}
	_, err := d.pool.Exec(ctx, `delete from auth_states where expires_at < now()`)
	return err
}
