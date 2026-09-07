// Package db owns the connection pool and the schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose
	"github.com/pressly/goose/v3"
)

// Migrations ship inside the binary so a deploy is still one artefact.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

const DefaultURL = "postgres://hackertracker:hackertracker@localhost:5442/hackertracker?sslmode=disable"

// connectTimeout is how long to wait for Postgres to start accepting
// connections. Compose brings the database up alongside the app, so a boot
// that gives up immediately is the difference between "works" and "crashloops
// until you restart it by hand".
const connectTimeout = 30 * time.Second

// Connect opens a pool and waits for the database to answer. Compose starts
// Postgres and the app together, so a few seconds of retrying on boot is the
// difference between "works" and "crashloops until you restart it".
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	deadline := time.Now().Add(connectTimeout)
	for {
		if err = pool.Ping(ctx); err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf(
				"could not reach Postgres at %s after %s.\n"+
					"  Start it with:  npm run db:up\n"+
					"  Check it with:  docker compose ps\n"+
					"  If a role or database is reported missing, something else is\n"+
					"  listening on that port (often a local Postgres install) --\n"+
					"  change POSTGRES_PORT in .env, or point DATABASE_URL elsewhere.\n"+
					"  last error: %w",
				redact(url), connectTimeout, err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Migrate brings the schema up to date. goose wants a database/sql handle, so
// this opens a short-lived one over the same pgx driver rather than holding a
// second pool for the life of the process.
func Migrate(ctx context.Context, url string) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open migration handle: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// redact strips credentials so a connection failure can be logged safely.
func redact(url string) string {
	at := -1
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return url
	}
	scheme := 0
	for i := 0; i+2 < len(url); i++ {
		if url[i] == ':' && url[i+1] == '/' && url[i+2] == '/' {
			scheme = i + 3
			break
		}
	}
	return url[:scheme] + "***" + url[at:]
}

// VerifyIsolation asserts that row-level security will actually be enforced.
//
// This exists because the failure it catches is silent. Policies can be
// enabled, forced, and completely inert: superusers and BYPASSRLS roles ignore
// them, and the role Compose creates from POSTGRES_USER is a superuser. A
// misconfiguration here would not error, it would simply serve every account's
// data to everyone. Refusing to boot is the only safe response.
func VerifyIsolation(ctx context.Context, pool *pgxpool.Pool, appRole string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "set local role "+appRole); err != nil {
		return fmt.Errorf("could not assume %s (has migration 00002 run?): %w", appRole, err)
	}

	var current string
	var superuser, bypass bool
	if err := tx.QueryRow(ctx, `
		select current_user,
		       coalesce((select rolsuper from pg_roles where rolname = current_user), false),
		       coalesce((select rolbypassrls from pg_roles where rolname = current_user), false)`,
	).Scan(&current, &superuser, &bypass); err != nil {
		return err
	}
	if current != appRole {
		return fmt.Errorf("expected to be running as %s, got %s", appRole, current)
	}
	if superuser || bypass {
		return fmt.Errorf("%s can bypass row-level security (superuser=%t bypassrls=%t); "+
			"account isolation would not be enforced", appRole, superuser, bypass)
	}

	// Without app.account_id set, every content table must be empty.
	var visible int
	if err := tx.QueryRow(ctx, `select count(*) from events`).Scan(&visible); err != nil {
		return fmt.Errorf("could not probe events: %w", err)
	}
	if visible != 0 {
		return fmt.Errorf("row-level security is not filtering: %d events visible with no account set", visible)
	}
	return nil
}
