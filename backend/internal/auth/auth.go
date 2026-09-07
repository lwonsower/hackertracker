// Package auth handles sign-in and sessions.
//
// Google's authorization-code flow with PKCE. The ID token's signature is
// deliberately not verified: the code is exchanged directly with Google's token
// endpoint over TLS, and the profile is then read from the userinfo endpoint
// over the same channel, so there is no untrusted intermediary to guard
// against. That removes a JWT/JWKS dependency and a class of verification bugs.
//
// Sessions are opaque random tokens stored server-side, not JWTs: revocable
// immediately, and nothing sensitive rides in the cookie.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lwonsower/hackertracker/backend/internal/store"
)

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

	SessionCookie = "ht_session"
	stateTTL      = 10 * time.Minute
)

type Config struct {
	ClientID     string
	ClientSecret string
	// RedirectURL must match the authorised redirect URI on the Google client.
	RedirectURL string

	// SecureCookie should be false only for plain-HTTP local development.
	SecureCookie bool
	SessionTTL   time.Duration

	// AdoptOrphanAccount lets the first sign-in claim an account that holds
	// data but has no users — how single-user history survives this migration.
	// Must be false when hosted: it would hand a stranger someone else's data.
	AdoptOrphanAccount bool

	// DevSignInEmail signs in as this address without contacting Google.
	// Ignored unless self-hosted. Exists so the app is usable before a Google
	// client is configured; every use is logged loudly.
	DevSignInEmail string
}

type Service struct {
	db     *store.DB
	cfg    Config
	client *http.Client
}

func New(db *store.DB, cfg Config) *Service {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	return &Service{db: db, cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
}

// Configured reports whether Google sign-in can actually run.
func (s *Service) Configured() bool {
	return s.cfg.ClientID != "" && s.cfg.ClientSecret != "" && s.cfg.RedirectURL != ""
}

func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/providers", s.handleProviders)
	mux.HandleFunc("GET /api/auth/google/start", s.handleStart)
	mux.HandleFunc("GET /api/auth/google/callback", s.handleCallback)
	mux.HandleFunc("POST /api/auth/signout", s.handleSignOut)
	mux.HandleFunc("POST /api/auth/dev-signin", s.handleDevSignIn)
}

// ── context ──────────────────────────────────────────────────────────────

type contextKey struct{}

// UserFrom returns the signed-in user. Handlers behind Require can rely on it.
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(contextKey{}).(store.User)
	return u, ok
}

// Require rejects unauthenticated requests with a 401 the frontend turns into a
// redirect to the sign-in page.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil {
			unauthorized(w)
			return
		}

		user, err := s.db.UserBySessionToken(r.Context(), cookie.Value)
		if errors.Is(err, store.ErrNotFound) {
			// Expired or revoked: clear it so the browser stops sending it.
			s.clearCookie(w)
			unauthorized(w)
			return
		}
		if err != nil {
			log.Printf("session lookup: %v", err)
			http.Error(w, `{"error":"could not verify the session"}`, http.StatusInternalServerError)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, user)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not signed in"})
}

// ── handlers ─────────────────────────────────────────────────────────────

func (s *Service) handleProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"google": s.Configured(),
		"dev":    s.devSignInAllowed(),
	})
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Google sign-in is not configured. Set GOOGLE_CLIENT_ID, " +
				"GOOGLE_CLIENT_SECRET and OAUTH_REDIRECT_URL in .env.local, then restart.",
		})
		return
	}

	state, err := randomToken()
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	nonce, err := randomToken()
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}
	verifier, err := randomToken()
	if err != nil {
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}

	redirectTo := safeRedirect(r.URL.Query().Get("redirect_to"))

	if err := s.db.SaveAuthState(r.Context(), store.AuthState{
		State: state, Nonce: nonce, Verifier: verifier, RedirectTo: redirectTo,
	}, stateTTL); err != nil {
		log.Printf("save auth state: %v", err)
		http.Error(w, "could not start sign-in", http.StatusInternalServerError)
		return
	}

	challenge := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"client_id":             {s.cfg.ClientID},
		"redirect_uri":          {s.cfg.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	http.Redirect(w, r, googleAuthURL+"?"+params.Encode(), http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if failure := query.Get("error"); failure != "" {
		s.failSignIn(w, r, fmt.Sprintf("Google returned %q", failure))
		return
	}

	// Consuming deletes the row, so a replayed callback finds nothing.
	state, err := s.db.ConsumeAuthState(r.Context(), query.Get("state"))
	if err != nil {
		s.failSignIn(w, r, "this sign-in link has expired or was already used")
		return
	}

	profile, err := s.exchange(r.Context(), query.Get("code"), state.Verifier)
	if err != nil {
		log.Printf("google exchange: %v", err)
		s.failSignIn(w, r, "could not complete sign-in with Google")
		return
	}

	s.signIn(w, r, profile, state.RedirectTo)
}

// handleDevSignIn exists so the app is usable before a Google client is set up.
func (s *Service) handleDevSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.devSignInAllowed() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "developer sign-in is disabled"})
		return
	}

	log.Printf("WARNING: developer sign-in used for %s — this bypasses Google entirely "+
		"and must never be enabled outside local development", s.cfg.DevSignInEmail)

	s.signIn(w, r, store.Identity{
		Provider: "dev",
		Subject:  s.cfg.DevSignInEmail,
		Email:    s.cfg.DevSignInEmail,
		Name:     "Local developer",
	}, "")
}

func (s *Service) devSignInAllowed() bool {
	return s.cfg.DevSignInEmail != "" && s.cfg.AdoptOrphanAccount
}

func (s *Service) signIn(w http.ResponseWriter, r *http.Request, id store.Identity, redirectTo string) {
	user, err := s.db.UpsertUser(r.Context(), id, s.cfg.AdoptOrphanAccount)
	if err != nil {
		log.Printf("upsert user: %v", err)
		s.failSignIn(w, r, "could not create your account")
		return
	}

	token, expires, err := s.db.CreateSession(r.Context(), user.ID, s.cfg.SessionTTL, r.UserAgent())
	if err != nil {
		log.Printf("create session: %v", err)
		s.failSignIn(w, r, "could not start your session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true, // never readable from JavaScript
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode, // survives the redirect back from Google
	})

	if redirectTo == "" {
		// A fetch-driven sign-in wants JSON, not a redirect.
		writeJSON(w, http.StatusOK, map[string]any{"user": user})
		return
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (s *Service) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookie); err == nil {
		if err := s.db.DeleteSession(r.Context(), cookie.Value); err != nil {
			log.Printf("delete session: %v", err)
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode,
	})
}

// failSignIn sends the browser back to the sign-in page with a readable reason
// rather than dumping a raw error into the address bar.
func (s *Service) failSignIn(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/signin?error="+url.QueryEscape(reason), http.StatusFound)
}

// ── Google ───────────────────────────────────────────────────────────────

func (s *Service) exchange(ctx context.Context, code, verifier string) (store.Identity, error) {
	if code == "" {
		return store.Identity{}, errors.New("no authorization code returned")
	}

	form := url.Values{
		"code":          {code},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
		"redirect_uri":  {s.cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return store.Identity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return store.Identity{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return store.Identity{}, fmt.Errorf("token endpoint returned %s", resp.Status)
	}

	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return store.Identity{}, err
	}
	if token.AccessToken == "" {
		return store.Identity{}, errors.New("token endpoint returned no access token")
	}

	return s.userInfo(ctx, token.AccessToken)
}

func (s *Service) userInfo(ctx context.Context, accessToken string) (store.Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleUserInfoURL, nil)
	if err != nil {
		return store.Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return store.Identity{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return store.Identity{}, fmt.Errorf("userinfo endpoint returned %s", resp.Status)
	}

	var profile struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return store.Identity{}, err
	}
	if profile.Sub == "" {
		return store.Identity{}, errors.New("userinfo returned no subject")
	}
	// Unverified addresses must not be trusted: account linking matches on
	// email, so accepting one would let someone claim another user's account.
	if profile.Email != "" && !profile.EmailVerified {
		return store.Identity{}, errors.New("this Google account's email address is not verified")
	}

	return store.Identity{
		Provider:  "google",
		Subject:   profile.Sub,
		Email:     profile.Email,
		Name:      profile.Name,
		AvatarURL: profile.Picture,
	}, nil
}

// safeRedirect reduces a destination to a same-site path, rejecting "//host",
// backslash-prefixed paths, control characters, and any scheme or host.
func safeRedirect(raw string) string {
	if raw == "" || raw[0] != '/' {
		return "/"
	}
	if len(raw) > 1 && (raw[1] == '/' || raw[1] == '\\') {
		return "/"
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "/"
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" {
		return "/"
	}
	return raw
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
