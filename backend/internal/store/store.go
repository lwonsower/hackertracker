// Package store is the only place that knows SQL.
//
// Content queries are reachable only through DB.Scope, which opens a
// transaction, drops to a role that cannot bypass row-level security, and sets
// the account the policies filter on. A Store therefore cannot exist without an
// account, and a query that forgets to mention one still sees nothing.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lwonsower/hackertracker/backend/internal/core"
)

// AppRole is the privilege-shedding role every scoped transaction assumes.
// It has no LOGIN and exists only so policies actually apply — see the comment
// in migration 00002.
const AppRole = "hackertracker_app"

var ErrNotFound = errors.New("not found")

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// DB owns the pool. It deliberately exposes no content queries of its own.
type DB struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }

// Scope runs fn against a Store bound to one account.
//
// Order matters: the role is dropped before anything else, because as a
// superuser the policies are inert no matter what app.account_id says. Both
// settings are transaction-local, so they unwind on commit or rollback and
// cannot leak to the next borrower of this connection.
func (d *DB) Scope(ctx context.Context, accountID uuid.UUID, fn func(*Store) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "set local role "+AppRole); err != nil {
		return fmt.Errorf("could not drop to %s: %w", AppRole, err)
	}
	if _, err := tx.Exec(ctx, "select set_config('app.account_id', $1, true)", accountID.String()); err != nil {
		return fmt.Errorf("could not set the account scope: %w", err)
	}

	if err := fn(&Store{db: tx, accountID: accountID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Store is bound to exactly one account for the life of one transaction.
type Store struct {
	db        querier
	accountID uuid.UUID
}

func (s *Store) AccountID() uuid.UUID { return s.accountID }

// WithTx is a no-op passthrough: a scoped Store is already inside one. It
// exists so callers written before scoping still read naturally.
func (s *Store) WithTx(_ context.Context, fn func(*Store) error) error { return fn(s) }

// ── source accounts ──────────────────────────────────────────────────────

type SourceAccount struct {
	ID                uuid.UUID `json:"id"`
	AccountID         uuid.UUID `json:"-"`
	Source            string    `json:"source"`
	Mode              string    `json:"mode"`
	Label             string    `json:"label"`
	ExternalAccountID string    `json:"external_account_id,omitempty"`
	CredentialsRef    string    `json:"credentials_ref,omitempty"`
}

const sourceAccountCols = `id, account_id, source, mode, label,
	coalesce(external_account_id, ''), coalesce(credentials_ref, '')`

type scannable interface{ Scan(dest ...any) error }

func scanSourceAccount(s scannable) (SourceAccount, error) {
	var a SourceAccount
	err := s.Scan(&a.ID, &a.AccountID, &a.Source, &a.Mode, &a.Label,
		&a.ExternalAccountID, &a.CredentialsRef)
	return a, err
}

func (s *Store) EnsureSourceAccount(ctx context.Context, source, mode, label string) (SourceAccount, error) {
	return scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (account_id, source, mode, label)
		values ($1, $2, $3, $4)
		on conflict (account_id, source, label) do update set mode = excluded.mode
		returning `+sourceAccountCols,
		s.accountID, source, mode, label))
}

func (s *Store) CreatePullAccount(ctx context.Context, source, label, externalAccountID, credentialsRef string) (SourceAccount, error) {
	return scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (account_id, source, mode, label, external_account_id, credentials_ref)
		values ($1, $2, 'pull', $3, $4, $5)
		on conflict (account_id, source, label) do update set
			mode                = 'pull',
			external_account_id = excluded.external_account_id,
			credentials_ref     = excluded.credentials_ref
		returning `+sourceAccountCols,
		s.accountID, source, label, externalAccountID, credentialsRef))
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePushEndpoint mints a webhook endpoint, returning its token in plaintext
// exactly once — only the hash is stored.
func (s *Store) CreatePushEndpoint(ctx context.Context, source, label string) (SourceAccount, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return SourceAccount{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	a, err := scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (account_id, source, mode, label, ingest_token_hash)
		values ($1, $2, 'push', $3, $4)
		returning `+sourceAccountCols,
		s.accountID, source, label, hashToken(token)))
	if err != nil {
		return SourceAccount{}, "", err
	}
	return a, token, nil
}

func (s *Store) SourceAccountByID(ctx context.Context, id uuid.UUID) (SourceAccount, error) {
	a, err := scanSourceAccount(s.db.QueryRow(ctx,
		`select `+sourceAccountCols+` from source_accounts where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceAccount{}, ErrNotFound
	}
	return a, err
}

func (s *Store) ListPullAccounts(ctx context.Context) ([]SourceAccount, error) {
	rows, err := s.db.Query(ctx,
		`select `+sourceAccountCols+` from source_accounts where mode = 'pull' order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []SourceAccount{}
	for rows.Next() {
		a, err := scanSourceAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

type SourceAccountRow struct {
	SourceAccount
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

func (s *Store) ListSourceAccounts(ctx context.Context) ([]SourceAccountRow, error) {
	rows, err := s.db.Query(ctx, `
		select sa.id, sa.account_id, sa.source, sa.mode, sa.label,
		       coalesce(sa.external_account_id, ''), coalesce(sa.credentials_ref, ''),
		       ss.last_synced_at, coalesce(ss.last_error, '')
		from source_accounts sa
		left join sync_state ss on ss.source_account_id = sa.id
		order by sa.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []SourceAccountRow{}
	for rows.Next() {
		var r SourceAccountRow
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Source, &r.Mode, &r.Label,
			&r.ExternalAccountID, &r.CredentialsRef, &r.LastSyncedAt, &r.LastError); err != nil {
			return nil, err
		}
		accounts = append(accounts, r)
	}
	return accounts, rows.Err()
}

// ── sync state ───────────────────────────────────────────────────────────

type SyncState struct {
	Cursor       string
	LastSyncedAt *time.Time
	LastError    string
}

func (s *Store) GetSyncState(ctx context.Context, id uuid.UUID) (SyncState, error) {
	var st SyncState
	err := s.db.QueryRow(ctx, `
		select coalesce(cursor, ''), last_synced_at, coalesce(last_error, '')
		from sync_state where source_account_id = $1`, id,
	).Scan(&st.Cursor, &st.LastSyncedAt, &st.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncState{}, nil
	}
	return st, err
}

// SaveSyncState coalesces LastSyncedAt so a failed run records its error
// without erasing the timestamp of the last run that worked.
func (s *Store) SaveSyncState(ctx context.Context, id uuid.UUID, st SyncState) error {
	_, err := s.db.Exec(ctx, `
		insert into sync_state (account_id, source_account_id, cursor, last_synced_at, last_error)
		values ($1, $2, nullif($3, ''), $4, nullif($5, ''))
		on conflict (source_account_id) do update set
			cursor         = excluded.cursor,
			last_synced_at = coalesce(excluded.last_synced_at, sync_state.last_synced_at),
			last_error     = excluded.last_error`,
		s.accountID, id, st.Cursor, st.LastSyncedAt, st.LastError)
	return err
}

// ── ingest log ───────────────────────────────────────────────────────────

func (s *Store) LogIngest(ctx context.Context, accountID *uuid.UUID, body []byte, status, errMsg string) error {
	const maxLoggedBody = 64 << 10

	var stored any
	if len(body) > 0 && len(body) <= maxLoggedBody && json.Valid(body) {
		stored = string(body)
	}

	var src any
	if accountID != nil {
		src = *accountID
	}

	_, err := s.db.Exec(ctx, `
		insert into ingest_log (account_id, source_account_id, body, status, error)
		values ($1, $2, $3, $4, nullif($5, ''))`,
		s.accountID, src, stored, status, errMsg)
	return err
}

// ── capture layer ────────────────────────────────────────────────────────

func (s *Store) InsertRawRecord(ctx context.Context, r core.RawRecord) error {
	_, err := s.db.Exec(ctx, `
		insert into raw_records (account_id, source_account_id, external_id, payload, content_hash)
		values ($1, $2, $3, $4, $5)
		on conflict (source_account_id, external_id, content_hash) do nothing`,
		s.accountID, r.SourceAccountID, r.ExternalID, string(r.Payload), r.ContentHash())
	return err
}

// UpsertEvent writes an event and reports whether it was newly created. On a
// freshly inserted row xmax is 0; on one updated by ON CONFLICT it is the
// current transaction ID.
func (s *Store) UpsertEvent(ctx context.Context, e core.Event) (created bool, err error) {
	err = s.db.QueryRow(ctx, `
		insert into events (id, account_id, source_account_id, external_id, kind,
		                    subject_key, title, url, occurred_at, payload)
		values ($1, $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''), $9, $10)
		on conflict (source_account_id, external_id, kind) do update set
			subject_key = excluded.subject_key,
			title       = excluded.title,
			url         = excluded.url,
			occurred_at = excluded.occurred_at,
			payload     = excluded.payload,
			ingested_at = now()
		returning (xmax = 0)`,
		e.ID, s.accountID, e.SourceAccountID, e.ExternalID, e.Kind, e.SubjectKey,
		e.Title, e.URL, e.OccurredAt, string(e.Payload),
	).Scan(&created)
	return created, err
}

// ── reads ────────────────────────────────────────────────────────────────

type EventRow struct {
	core.Event
	Source      string `json:"source"`
	SourceLabel string `json:"source_label"`
}

type EventFilter struct {
	From  time.Time
	To    time.Time
	Limit int
}

func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]EventRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}

	rows, err := s.db.Query(ctx, `
		select e.id, e.external_id, e.kind, coalesce(e.subject_key, ''), e.title,
		       coalesce(e.url, ''), e.occurred_at, e.payload, sa.source, sa.label
		from live_events e
		join source_accounts sa on sa.id = e.source_account_id
		where e.occurred_at >= $1 and e.occurred_at < $2
		order by e.occurred_at desc, e.title
		limit $3`,
		f.From, f.To, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	out := []EventRow{}
	for rows.Next() {
		var r EventRow
		var payload []byte
		if err := rows.Scan(&r.ID, &r.ExternalID, &r.Kind, &r.SubjectKey, &r.Title,
			&r.URL, &r.OccurredAt, &payload, &r.Source, &r.SourceLabel); err != nil {
			return nil, err
		}
		r.Payload = json.RawMessage(payload)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── credentials ──────────────────────────────────────────────────────────

type CredentialRow struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// Credential returns the stored ciphertext and marks it used. Plaintext never
// passes through this package.
func (s *Store) Credential(ctx context.Context, id uuid.UUID) (keyID string, ciphertext []byte, err error) {
	err = s.db.QueryRow(ctx, `
		update credentials set last_used_at = now()
		where id = $1
		returning key_id, ciphertext`, id,
	).Scan(&keyID, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return keyID, ciphertext, err
}

func (s *Store) CreateCredential(ctx context.Context, label, keyID string, ciphertext []byte) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `
		insert into credentials (account_id, label, key_id, ciphertext)
		values ($1, $2, $3, $4)
		returning id`,
		s.accountID, label, keyID, ciphertext).Scan(&id)
	return id, err
}

func (s *Store) ListCredentials(ctx context.Context) ([]CredentialRow, error) {
	rows, err := s.db.Query(ctx, `
		select id, label, created_at, last_used_at
		from credentials order by created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CredentialRow{}
	for rows.Next() {
		var c CredentialRow
		if err := rows.Scan(&c.ID, &c.Label, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCredential(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `delete from credentials where id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SourcesUsingCredential names the sources referencing a credential, so a
// delete that would break them can be refused with something readable.
func (s *Store) SourcesUsingCredential(ctx context.Context, id uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`select label from source_accounts where credentials_ref = $1`, "secret:"+id.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	labels := []string{}
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// CredentialRefForSource returns the existing reference for a source, so
// reconnecting can replace the old secret instead of orphaning it.
func (s *Store) CredentialRefForSource(ctx context.Context, source, label string) (string, error) {
	var ref string
	err := s.db.QueryRow(ctx, `
		select coalesce(credentials_ref, '') from source_accounts
		where source = $1 and label = $2`, source, label).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ref, err
}
