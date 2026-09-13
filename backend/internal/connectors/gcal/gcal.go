// Package gcal reads Google Calendar for the review flow.
//
// Unlike a pull connector this never writes on its own. It fetches a window,
// applies the noise filters, and hands back proposals; only what a person
// picks is promoted into an event. Nothing unpicked is persisted anywhere,
// which is the whole point — a calendar holds interviews and doctors'
// appointments, and this tool keeps raw payloads forever.
package gcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Scope is deliberately events.readonly rather than calendar.readonly:
	// the narrowest scope that can list events.
	Scope = "https://www.googleapis.com/auth/calendar.events.readonly"

	DefaultAPIBase   = "https://www.googleapis.com/calendar/v3"
	DefaultOAuthBase = "https://oauth2.googleapis.com"
	AuthURL          = "https://accounts.google.com/o/oauth2/v2/auth"

	maxPages    = 10
	pageSize    = 250
	httpTimeout = 30 * time.Second
)

// Config carries the endpoints so tests can point at a stub.
type Config struct {
	ClientID     string
	ClientSecret string
	APIBase      string
	OAuthBase    string
	HTTP         *http.Client
}

func (c Config) apiBase() string {
	if c.APIBase == "" {
		return DefaultAPIBase
	}
	return strings.TrimSuffix(c.APIBase, "/")
}

func (c Config) oauthBase() string {
	if c.OAuthBase == "" {
		return DefaultOAuthBase
	}
	return strings.TrimSuffix(c.OAuthBase, "/")
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: httpTimeout}
}

// ErrReconnect means the stored grant no longer works, which a person fixes by
// reconnecting rather than by retrying.
var ErrReconnect = errors.New("google calendar access was revoked or expired; reconnect the calendar")

// ── OAuth ────────────────────────────────────────────────────────────────

// AuthCodeURL builds the consent URL. offline access plus prompt=consent is
// what makes Google return a refresh token; without prompt=consent a second
// authorisation returns none, and the connection silently cannot be renewed.
func (c Config) AuthCodeURL(redirectURL, state, challenge string) string {
	params := url.Values{
		"client_id":              {c.ClientID},
		"redirect_uri":           {redirectURL},
		"response_type":          {"code"},
		"scope":                  {Scope},
		"state":                  {state},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"code_challenge":         {challenge},
		"code_challenge_method":  {"S256"},
	}
	return AuthURL + "?" + params.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func (c Config) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.oauthBase()+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.client().Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, err
	}
	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return tokenResponse{}, fmt.Errorf("google returned %d with an unreadable body", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK || out.Error != "" {
		// The response body can contain the token on success, so only the
		// error code is surfaced, never the payload.
		if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
			return tokenResponse{}, ErrReconnect
		}
		return tokenResponse{}, fmt.Errorf("google token endpoint returned %d", res.StatusCode)
	}
	return out, nil
}

// Exchange trades an authorisation code for a refresh token.
func (c Config) Exchange(ctx context.Context, code, redirectURL, verifier string) (refreshToken, accessToken string, err error) {
	out, err := c.token(ctx, url.Values{
		"code":          {code},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"redirect_uri":  {redirectURL},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	})
	if err != nil {
		return "", "", err
	}
	if out.RefreshToken == "" {
		return "", "", errors.New("google did not return a refresh token; remove the app's access at myaccount.google.com and connect again")
	}
	return out.RefreshToken, out.AccessToken, nil
}

// AccessToken renews a short-lived token from the stored refresh token. Called
// per request rather than cached: a review is a handful of calls, and holding
// access tokens in memory buys nothing worth the exposure.
func (c Config) AccessToken(ctx context.Context, refreshToken string) (string, error) {
	out, err := c.token(ctx, url.Values{
		"refresh_token": {refreshToken},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", err
	}
	return out.AccessToken, nil
}

// ── events ───────────────────────────────────────────────────────────────

// Event is the subset worth keeping. Description and location are deliberately
// absent: they hold dial-ins, agendas and occasionally very personal detail,
// and none of it is evidence.
type Event struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	URL           string    `json:"url,omitempty"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end,omitempty"`
	AllDay        bool      `json:"all_day,omitempty"`
	Recurring     bool      `json:"recurring,omitempty"`
	Organizer     string    `json:"organizer,omitempty"`
	SelfOrganizer bool      `json:"self_organizer,omitempty"`
	// Display names only. A permanent list of colleagues' email addresses is a
	// liability with no evidentiary upside.
	Attendees []string `json:"attendees,omitempty"`
	Response  string   `json:"response,omitempty"`
	// Excluded names the filter that would hide this by default, so the UI can
	// explain itself instead of silently dropping things.
	Excluded string `json:"excluded,omitempty"`
}

type rawEvent struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	Summary          string  `json:"summary"`
	HTMLLink         string  `json:"htmlLink"`
	RecurringEventID string  `json:"recurringEventId"`
	Start            rawTime `json:"start"`
	End              rawTime `json:"end"`
	Organizer        struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Self        bool   `json:"self"`
	} `json:"organizer"`
	Attendees []struct {
		DisplayName    string `json:"displayName"`
		Email          string `json:"email"`
		Self           bool   `json:"self"`
		Resource       bool   `json:"resource"`
		ResponseStatus string `json:"responseStatus"`
	} `json:"attendees"`
}

type rawTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

func (r rawTime) parse() (time.Time, bool) {
	if r.DateTime != "" {
		t, err := time.Parse(time.RFC3339, r.DateTime)
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), false
	}
	if r.Date != "" {
		t, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return time.Time{}, true
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

type listResponse struct {
	Items         []rawEvent `json:"items"`
	NextPageToken string     `json:"nextPageToken"`
}

// List returns every event in the window, each already tagged with the filter
// that would exclude it. Filtering is reported rather than applied here so the
// caller can offer "show what was hidden" without a second round trip.
func (c Config) List(ctx context.Context, accessToken, calendarID string, from, to time.Time) ([]Event, error) {
	if calendarID == "" {
		calendarID = "primary"
	}

	out := []Event{}
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		params := url.Values{
			"timeMin": {from.UTC().Format(time.RFC3339)},
			"timeMax": {to.UTC().Format(time.RFC3339)},
			// Expanded, because a proposal list is about occasions that
			// happened, not about the rule that generated them.
			"singleEvents": {"true"},
			"orderBy":      {"startTime"},
			"maxResults":   {fmt.Sprint(pageSize)},
			"showDeleted":  {"false"},
		}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}

		endpoint := c.apiBase() + "/calendars/" + url.PathEscape(calendarID) + "/events?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		res, err := c.client().Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		switch res.StatusCode {
		case http.StatusOK:
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, ErrReconnect
		default:
			return nil, fmt.Errorf("google calendar returned %d", res.StatusCode)
		}

		var parsed listResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("could not read google calendar's response: %w", err)
		}
		for _, item := range parsed.Items {
			if item.Status == "cancelled" {
				continue
			}
			out = append(out, convert(item))
		}
		if parsed.NextPageToken == "" {
			return out, nil
		}
		pageToken = parsed.NextPageToken
	}
	return out, nil
}

func convert(r rawEvent) Event {
	start, allDay := r.Start.parse()
	end, _ := r.End.parse()

	e := Event{
		ID:            r.ID,
		Title:         strings.TrimSpace(r.Summary),
		URL:           r.HTMLLink,
		Start:         start,
		End:           end,
		AllDay:        allDay,
		Recurring:     r.RecurringEventID != "",
		SelfOrganizer: r.Organizer.Self,
	}
	if e.Title == "" {
		e.Title = "(no title)"
	}
	e.Organizer = personName(r.Organizer.DisplayName, r.Organizer.Email)

	for _, a := range r.Attendees {
		if a.Resource {
			continue // meeting rooms are not colleagues
		}
		if a.Self {
			e.Response = a.ResponseStatus
			continue
		}
		e.Attendees = append(e.Attendees, personName(a.DisplayName, a.Email))
	}
	e.Excluded = excludedBy(e)
	return e
}

// personName prefers a display name and falls back to the local part of the
// address, so a name is shown without storing the address itself.
func personName(displayName, email string) string {
	if name := strings.TrimSpace(displayName); name != "" {
		return name
	}
	if local, _, ok := strings.Cut(email, "@"); ok {
		return local
	}
	return email
}

// excludedBy names the first default filter that hides an event, in order of
// how confidently it is noise.
func excludedBy(e Event) string {
	switch {
	case e.Response == "declined":
		return "declined"
	case e.Response == "needsAction" && !e.SelfOrganizer:
		return "no response"
	case len(e.Attendees) == 0:
		return "solo"
	case e.AllDay:
		return "all day"
	case e.Recurring:
		return "recurring"
	}
	return ""
}
