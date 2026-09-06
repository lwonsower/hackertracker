// Command hackertracker is the backend for a personal work-tracking tool.
//
// It serves the frontend from an embedded filesystem and exposes one generic
// ingest API. Every way of getting work into the system — the capture form, a
// webhook, a future pull connector, a file import — writes through the same
// pipeline in internal/ingest.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lwonsower/hackertracker/backend/internal/connectors/github"
	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/dotenv"
	"github.com/lwonsower/hackertracker/backend/internal/db"
	"github.com/lwonsower/hackertracker/backend/internal/httpapi"
	"github.com/lwonsower/hackertracker/backend/internal/ingest"
	"github.com/lwonsower/hackertracker/backend/internal/store"
	"github.com/lwonsower/hackertracker/backend/internal/syncer"
)

// web holds Vite's production output, which is why the directory name matches
// the configured outDir. `go build` after a Vite build yields one binary.
//
//go:embed web
var webFS embed.FS

const (
	defaultPort     = "8080"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("hackertracker: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Before anything reads the environment. .env.local is listed first so it
	// wins over .env, and neither overrides a variable that is already set.
	for _, path := range dotenv.Load(".env.local", ".env") {
		log.Printf("loaded configuration from %s", path)
	}

	port := envOr("PORT", defaultPort)
	databaseURL := envOr("DATABASE_URL", db.DefaultURL)

	// Connect first. Connect is the step that waits for Postgres to accept
	// connections, so migrating ahead of it would blow past that wait and fail
	// instantly whenever the database is merely still starting up — which,
	// under `npm run dev`, is most of the time.
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Then migrate, before serving a single request: a query failing against a
	// stale schema is far harder to diagnose than failing here.
	if err := db.Migrate(ctx, databaseURL); err != nil {
		return err
	}

	st := store.New(pool)
	pipeline := ingest.New(st)

	// GitHub payloads are not envelopes, so they get their own normaliser.
	// Registering it separately from the connector means raw_records can be
	// re-normalised later without credentials.
	pipeline.Register("github", github.Normalizer{})

	runner := syncer.New(st, pipeline)
	if years, err := strconv.Atoi(os.Getenv("SYNC_BACKFILL_YEARS")); err == nil && years > 0 {
		runner.BackfillYears = years
	}
	runner.Register("github", func(acct store.SourceAccount, token string) (core.Fetcher, error) {
		return github.New(github.Config{
			Login:   acct.ExternalAccountID,
			Token:   token,
			BaseURL: os.Getenv("GITHUB_API_BASE_URL"), // empty means api.github.com
		})
	})

	// Manual entry is just another source account, so hand-typed events carry
	// the same provenance as anything captured automatically.
	manual, err := st.EnsureSourceAccount(ctx, "manual", "manual", "Manual entry")
	if err != nil {
		return err
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz(pool))
	httpapi.New(st, pipeline, runner, manual).Routes(mux)
	mux.Handle("GET /", spaHandler(static))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()

		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	// Sync once at startup, off the critical path so a slow or unreachable
	// upstream never delays serving.
	go func() {
		reports, err := runner.SyncAll(ctx)
		if err != nil {
			log.Printf("startup sync: %v", err)
			return
		}
		for _, rep := range reports {
			log.Printf("startup sync %s: %d new, %d updated, complete=%t",
				rep.Label, rep.Created, rep.Updated, rep.Complete)
			for _, note := range rep.Notes {
				log.Printf("  note: %s", note)
			}
		}
	}()

	log.Printf("listening on http://localhost:%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-shutdownDone
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// spaHandler serves the embedded frontend, falling back to index.html for any
// path that isn't a real file. That fallback is what makes client-side routing
// work on a hard refresh: hitting /timeline directly must return the app shell
// so react-router can take over, rather than a 404.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API routes are deliberately excluded from the fallback. A typo'd
		// endpoint should 404 honestly instead of returning HTML that the
		// client then fails to parse as JSON.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if _, err := fs.Stat(static, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			files.ServeHTTP(w, r)
			return
		}

		files.ServeHTTP(w, r)
	})
}

// handleHealthz reports unhealthy when the database is unreachable, so the
// Compose healthcheck fails for the reason that actually matters.
func handleHealthz(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		status, code := "ok", http.StatusOK
		if err := pool.Ping(ctx); err != nil {
			status, code = "database unreachable", http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	}
}
