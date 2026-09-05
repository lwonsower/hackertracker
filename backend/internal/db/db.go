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

const DefaultURL = "postgres://hackertracker:hackertracker@localhost:5432/hackertracker?sslmode=disable"

// Connect opens a pool and waits for the database to answer. Compose starts
// Postgres and the app together, so a few seconds of retrying on boot is the
// difference between "works" and "crashloops until you restart it".
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pool.Ping(ctx); err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("database unreachable at %s: %w", redact(url), err)
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
