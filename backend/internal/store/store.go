// Package store is the only place that knows SQL. Queries are hand-written
// against pgx rather than generated: there are few enough of them that a code
// generator would cost more in tooling setup than it saves, and they are all
// isolated here if that changes.
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

// ErrNotFound is returned instead of pgx.ErrNoRows so callers don't need to
// import pgx to tell "nothing there" from "something broke".
var ErrNotFound = errors.New("not found")

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, which is what lets
// WithTx hand back a Store bound to a transaction without duplicating methods.
type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ db querier }

func New(pool *pgxpool.Pool) *Store { return &Store{db: pool} }

// WithTx runs fn against a transactional Store, committing if it returns nil.
// Nested calls are a no-op so a caller already inside a transaction composes
// cleanly.
func (s *Store) WithTx(ctx context.Context, fn func(*Store) error) error {
	pool, ok := s.db.(*pgxpool.Pool)
	if !ok {
		return fn(s)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(&Store{db: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── source accounts ──────────────────────────────────────────────────────

type SourceAccount struct {
	ID                uuid.UUID `json:"id"`
	Source            string    `json:"source"`
	Mode              string    `json:"mode"`
	Label             string    `json:"label"`
	ExternalAccountID string    `json:"external_account_id,omitempty"`
	CredentialsRef    string    `json:"credentials_ref,omitempty"`
}

const sourceAccountCols = `id, source, mode, label,
	coalesce(external_account_id, ''), coalesce(credentials_ref, '')`

// scannable covers both pgx.Row and pgx.Rows so one scan helper serves every
// query that selects sourceAccountCols.
type scannable interface{ Scan(dest ...any) error }

func scanSourceAccount(s scannable) (SourceAccount, error) {
	var a SourceAccount
	err := s.Scan(&a.ID, &a.Source, &a.Mode, &a.Label, &a.ExternalAccountID, &a.CredentialsRef)
	return a, err
}

// EnsureSourceAccount is idempotent on (source, label), so start-up can call it
// unconditionally.
func (s *Store) EnsureSourceAccount(ctx context.Context, source, mode, label string) (SourceAccount, error) {
	return scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (source, mode, label)
		values ($1, $2, $3)
		on conflict (source, label) do update set mode = excluded.mode
		returning `+sourceAccountCols,
		source, mode, label))
}

// CreatePullAccount connects a polling source. Upserting on (source, label)
// makes reconnecting the same account idempotent rather than an error.
func (s *Store) CreatePullAccount(ctx context.Context, source, label, externalAccountID, credentialsRef string) (SourceAccount, error) {
	return scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (source, mode, label, external_account_id, credentials_ref)
		values ($1, 'pull', $2, $3, $4)
		on conflict (source, label) do update set
			mode                = 'pull',
			external_account_id = excluded.external_account_id,
			credentials_ref     = excluded.credentials_ref
		returning `+sourceAccountCols,
		source, label, externalAccountID, credentialsRef))
}

func (s *Store) SourceAccountByID(ctx context.Context, id uuid.UUID) (SourceAccount, error) {
	a, err := scanSourceAccount(s.db.QueryRow(ctx,
		`select `+sourceAccountCols+` from source_accounts where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceAccount{}, ErrNotFound
	}
	return a, err
}

// ListPullAccounts returns the accounts a sync run can actually poll.
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

// SourceAccountRow decorates an account with its sync state, which is what the
// UI needs to show whether a source is actually working.
type SourceAccountRow struct {
	SourceAccount
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

func (s *Store) ListSourceAccounts(ctx context.Context) ([]SourceAccountRow, error) {
	rows, err := s.db.Query(ctx, `
		select sa.id, sa.source, sa.mode, sa.label,
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
		if err := rows.Scan(&r.ID, &r.Source, &r.Mode, &r.Label,
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
		// Never synced is not an error, just an empty state.
		return SyncState{}, nil
	}
	return st, err
}

// SaveSyncState persists progress. LastSyncedAt is coalesced rather than
// overwritten so a failed run records its error without erasing the timestamp
// of the last run that actually worked.
func (s *Store) SaveSyncState(ctx context.Context, id uuid.UUID, st SyncState) error {
	_, err := s.db.Exec(ctx, `
		insert into sync_state (source_account_id, cursor, last_synced_at, last_error)
		values ($1, nullif($2, ''), $3, nullif($4, ''))
		on conflict (source_account_id) do update set
			cursor         = excluded.cursor,
			last_synced_at = coalesce(excluded.last_synced_at, sync_state.last_synced_at),
			last_error     = excluded.last_error`,
		id, st.Cursor, st.LastSyncedAt, st.LastError)
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreatePushEndpoint mints a webhook endpoint and returns its token in
// plaintext exactly once — only the hash is stored, so a lost token means
// minting a new endpoint rather than recovering the old one.
func (s *Store) CreatePushEndpoint(ctx context.Context, source, label string) (SourceAccount, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return SourceAccount{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)

	a, err := scanSourceAccount(s.db.QueryRow(ctx, `
		insert into source_accounts (source, mode, label, ingest_token_hash)
		values ($1, 'push', $2, $3)
		returning `+sourceAccountCols,
		source, label, hashToken(token)))
	if err != nil {
		return SourceAccount{}, "", err
	}
	return a, token, nil
}

func (s *Store) SourceAccountByToken(ctx context.Context, token string) (SourceAccount, error) {
	a, err := scanSourceAccount(s.db.QueryRow(ctx,
		`select `+sourceAccountCols+` from source_accounts where ingest_token_hash = $1`,
		hashToken(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceAccount{}, ErrNotFound
	}
	return a, err
}

// ── ingest log ───────────────────────────────────────────────────────────

// LogIngest records every inbound request, accepted or not. Failures are the
// whole point: raw_records only holds what succeeded.
func (s *Store) LogIngest(ctx context.Context, accountID *uuid.UUID, body []byte, status, errMsg string) error {
	const maxLoggedBody = 64 << 10

	var stored any
	if len(body) > 0 && len(body) <= maxLoggedBody && json.Valid(body) {
		stored = string(body)
	}

	var acct any
	if accountID != nil {
		acct = *accountID
	}

	_, err := s.db.Exec(ctx, `
		insert into ingest_log (source_account_id, body, status, error)
		values ($1, $2, $3, nullif($4, ''))`,
		acct, stored, status, errMsg)
	return err
}

// ── capture layer ────────────────────────────────────────────────────────

// InsertRawRecord is a no-op when this exact payload version already exists,
// so re-fetching unchanged records costs nothing while a genuine upstream edit
// is kept alongside the original.
func (s *Store) InsertRawRecord(ctx context.Context, r core.RawRecord) error {
	_, err := s.db.Exec(ctx, `
		insert into raw_records (source_account_id, external_id, payload, content_hash)
		values ($1, $2, $3, $4)
		on conflict (source_account_id, external_id, content_hash) do nothing`,
		r.SourceAccountID, r.ExternalID, string(r.Payload), r.ContentHash())
	return err
}

// UpsertEvent writes an event and reports whether it was newly created. The
// xmax trick distinguishes insert from update: on a freshly inserted row xmax
// is 0, on one updated by ON CONFLICT it is the current transaction ID.
func (s *Store) UpsertEvent(ctx context.Context, e core.Event) (created bool, err error) {
	err = s.db.QueryRow(ctx, `
		insert into events (id, source_account_id, external_id, kind, subject_key,
		                    title, url, occurred_at, payload)
		values ($1, $2, $3, $4, nullif($5, ''), $6, nullif($7, ''), $8, $9)
		on conflict (source_account_id, external_id, kind) do update set
			subject_key = excluded.subject_key,
			title       = excluded.title,
			url         = excluded.url,
			occurred_at = excluded.occurred_at,
			payload     = excluded.payload,
			ingested_at = now()
		returning (xmax = 0)`,
		e.ID, e.SourceAccountID, e.ExternalID, e.Kind, e.SubjectKey,
		e.Title, e.URL, e.OccurredAt, string(e.Payload),
	).Scan(&created)
	return created, err
}

// ── reads ────────────────────────────────────────────────────────────────

// EventRow is an event decorated with where it came from, for the timeline.
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
		from events e
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
