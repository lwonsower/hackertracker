// Command hackertracker is the backend for a personal work-tracking tool.
//
// It serves the frontend from an embedded filesystem and exposes one generic
// ingest API. Every way of getting work into the system — the capture form, a
// webhook, a pull connector, a file import — writes through the same pipeline
// in internal/ingest, and every write happens inside an account scope.
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

	"github.com/lwonsower/hackertracker/backend/internal/auth"
	"github.com/lwonsower/hackertracker/backend/internal/connectors/github"
	"github.com/lwonsower/hackertracker/backend/internal/core"
	"github.com/lwonsower/hackertracker/backend/internal/db"
	"github.com/lwonsower/hackertracker/backend/internal/dotenv"
	"github.com/lwonsower/hackertracker/backend/internal/httpapi"
	"github.com/lwonsower/hackertracker/backend/internal/ingest"
	"github.com/lwonsower/hackertracker/backend/internal/secrets"
	"github.com/lwonsower/hackertracker/backend/internal/store"
	"github.com/lwonsower/hackertracker/backend/internal/syncer"
)

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

	for _, path := range dotenv.Load(".env.local", ".env") {
		log.Printf("loaded configuration from %s", path)
	}

	port := envOr("PORT", defaultPort)
	databaseURL := envOr("DATABASE_URL", db.DefaultURL)

	// Self-host enables orphan-account adoption, env: credentials and the
	// developer sign-in bypass, and stops marking cookies Secure.
	selfHosted := envOr("DEPLOYMENT_MODE", "hosted") == "self-host"
	if selfHosted {
		log.Printf("SELF-HOST MODE: orphan-account adoption, env: credentials and the " +
			"developer sign-in bypass are enabled, and cookies are not marked Secure. " +
			"Set DEPLOYMENT_MODE=hosted for anything reachable by more than one person.")
	}

	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, databaseURL); err != nil {
		return err
	}

	// Refuse to serve if account isolation is not actually enforced. The
	// failure this catches is silent: policies can be enabled and completely
	// inert, and the symptom would be serving everyone's data to everyone.
	if err := db.VerifyIsolation(ctx, pool, store.AppRole); err != nil {
		return err
	}

	database := store.New(pool)

	// Purge expired sessions and OAuth handshakes hourly.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := database.PurgeExpired(ctx); err != nil {
				log.Printf("could not purge expired sessions: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	resolver, err := secrets.NewResolver(selfHosted, os.Getenv("CREDENTIALS_KEY"))
	if err != nil {
		return err
	}
	if !resolver.CanEncrypt() {
		log.Print("CREDENTIALS_KEY is not set, so per-account tokens cannot be stored. " +
			"Generate one with: echo \"v1:$(openssl rand -base64 32)\"")
	}

	pipeline := ingest.New()
	// GitHub payloads are not envelopes, so they get their own normaliser.
	// Registered separately from the connector so raw_records can be
	// re-normalised later without credentials.
	pipeline.Register("github", github.Normalizer{})

	githubBaseURL := os.Getenv("GITHUB_API_BASE_URL") // empty means api.github.com

	runner := syncer.New(database, pipeline, resolver)
	if years, err := strconv.Atoi(os.Getenv("SYNC_BACKFILL_YEARS")); err == nil && years > 0 {
		runner.BackfillYears = years
	}
	runner.Register("github", func(acct store.SourceAccount, token string) (core.Fetcher, error) {
		return github.New(github.Config{
			Login:   acct.ExternalAccountID,
			Token:   token,
			BaseURL: githubBaseURL,
		})
	})

	authService := auth.New(database, auth.Config{
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("OAUTH_REDIRECT_URL"),
		SecureCookie: envOr("COOKIE_SECURE", boolString(!selfHosted)) == "true",
		// Only a self-hosted instance may adopt orphaned data or use the
		// developer bypass; both would be account takeover when hosted.
		AdoptOrphanAccount: selfHosted,
		DevSignInEmail:     os.Getenv("DEV_SIGN_IN_EMAIL"),
	})
	if selfHosted && os.Getenv("DEV_SIGN_IN_EMAIL") != "" {
		log.Printf("WARNING: DEV_SIGN_IN_EMAIL is set — anyone who can reach this server "+
			"can sign in as %s without any credential", os.Getenv("DEV_SIGN_IN_EMAIL"))
	}
	if !authService.Configured() {
		log.Printf("Google sign-in is not configured (set GOOGLE_CLIENT_ID, " +
			"GOOGLE_CLIENT_SECRET and OAUTH_REDIRECT_URL in .env.local)")
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz(pool))
	authService.Routes(mux)
	httpapi.New(database, pipeline, runner, resolver, githubBaseURL).Routes(mux, authService.Require)
	mux.Handle("GET /", spaHandler(static))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           withSecurityHeaders(mux, !selfHosted),
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

	// There is deliberately no sync-at-startup any more. With many accounts,
	// syncing everything on boot is both unbounded work and a scoping
	// violation; scheduled syncing needs a job queue that does not exist yet.
	log.Printf("listening on http://localhost:%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-shutdownDone
	return nil
}

// withSecurityHeaders sets CSP, framing, sniffing, referrer and HSTS headers.
func withSecurityHeaders(next http.Handler, https bool) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https://developers.google.com https://lh3.googleusercontent.com; " +
		"connect-src 'self'; " +
		"form-action 'self' https://accounts.google.com; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"object-src 'none'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if https {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// spaHandler serves the embedded frontend, falling back to index.html for any
// path that isn't a real file, so client-side routing survives a hard refresh.
func spaHandler(static fs.FS) http.Handler {
	files := http.FileServerFS(static)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API routes are deliberately excluded: a typo'd endpoint should 404
		// honestly instead of returning HTML the client fails to parse as JSON.
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
