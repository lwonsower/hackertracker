// Command server is the crossedstars backend.
//
// For now it does two things: serve the (blank) web page and answer a health
// check. Chart calculation, accounts, and synastry come later.
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
	"strings"
	"syscall"
	"time"
)

// web holds the static frontend. Today that is a hand-written blank page;
// later it will be Vite's build output, which is why the directory is named
// to match Vite's configured outDir.
//
//go:embed web
var webFS embed.FS

const (
	defaultPort     = "8080"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("crossedstars: %v", err)
	}
}

func run() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Strip the "web/" prefix so the embedded files are served from the root.
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /", spaHandler(static))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down cleanly on Ctrl-C or SIGTERM so in-flight requests finish.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig

		log.Println("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("listening on http://localhost:%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-shutdownDone
	return nil
}

// spaHandler serves the embedded frontend, falling back to index.html for any
// path that isn't a real file. That fallback is what makes client-side routing
// work on a hard refresh: hitting /login directly must return the app shell so
// react-router can take over, rather than a 404.
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
			// Not a real file — hand it to the client-side router.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			files.ServeHTTP(w, r)
			return
		}

		files.ServeHTTP(w, r)
	})
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
