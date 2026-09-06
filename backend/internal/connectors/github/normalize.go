package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lwonsower/hackertracker/backend/internal/core"
)

// searchItem is the subset of a GitHub search result this connector reads.
// The full payload is still stored verbatim in raw_records, so widening this
// struct later needs no re-fetch — only a re-normalise.
type searchItem struct {
	Title         string     `json:"title"`
	HTMLURL       string     `json:"html_url"`
	Number        int        `json:"number"`
	RepositoryURL string     `json:"repository_url"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	ClosedAt      *time.Time `json:"closed_at"`
	Draft         bool       `json:"draft"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

func repoFromAPIURL(apiURL string) string {
	_, repo, ok := strings.Cut(apiURL, "/repos/")
	if !ok {
		return ""
	}
	return repo
}

// rawRecord wraps a verbatim search item with the external ID it will be
// stored under. The stream is encoded in the ID prefix rather than wrapped
// around the payload, which keeps raw_records byte-identical to what GitHub
// returned.
func rawRecord(stream string, payload json.RawMessage) (core.RawRecord, bool) {
	var item searchItem
	if err := json.Unmarshal(payload, &item); err != nil {
		return core.RawRecord{}, false
	}
	repo := repoFromAPIURL(item.RepositoryURL)
	if repo == "" || item.Number == 0 {
		return core.RawRecord{}, false
	}

	prefix := "pr"
	if stream == streamReviewed {
		prefix = "review"
	}

	return core.RawRecord{
		ExternalID: fmt.Sprintf("%s:%s#%d", prefix, repo, item.Number),
		Payload:    payload,
	}, true
}

// Normalize turns a stored GitHub search item into an event. The stream is
// recovered from the external ID prefix, so this needs nothing but the record.
func (Normalizer) Normalize(r core.RawRecord) ([]core.Event, error) {
	var item searchItem
	if err := json.Unmarshal(r.Payload, &item); err != nil {
		return nil, fmt.Errorf("github: raw record %q is not a search item: %w", r.ExternalID, err)
	}

	repo := repoFromAPIURL(item.RepositoryURL)
	if repo == "" {
		return nil, fmt.Errorf("github: raw record %q has no usable repository URL", r.ExternalID)
	}

	labels := make([]string, 0, len(item.Labels))
	for _, l := range item.Labels {
		labels = append(labels, l.Name)
	}

	payload := map[string]any{
		"repo":   repo,
		"number": item.Number,
	}
	if len(labels) > 0 {
		payload["labels"] = labels
	}

	var kind, title string
	var occurred time.Time

	switch {
	case strings.HasPrefix(r.ExternalID, "pr:"):
		kind = "pr_merged"
		title = item.Title
		// merged_at is the honest timestamp but is not guaranteed to appear in
		// search results, so fall back to closed_at — for a merged PR the two
		// are the same moment.
		switch {
		case item.PullRequest != nil && item.PullRequest.MergedAt != nil:
			occurred = *item.PullRequest.MergedAt
		case item.ClosedAt != nil:
			occurred = *item.ClosedAt
		default:
			occurred = item.UpdatedAt
		}

	case strings.HasPrefix(r.ExternalID, "review:"):
		kind = "review_submitted"
		title = "Reviewed: " + item.Title
		// The search API returns pull requests, not review events: there is no
		// review timestamp and no way to tell which review was yours. The PR's
		// updated_at is the closest available proxy.
		occurred = item.UpdatedAt
		// Recorded so the eventual upgrade to real review timestamps can find
		// exactly which events need re-normalising, instead of guessing.
		payload["timestamp_precision"] = "approximate"
		payload["timestamp_source"] = "pull_request.updated_at"

	default:
		return nil, fmt.Errorf("github: unrecognised external ID %q", r.ExternalID)
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	return []core.Event{{
		ID:              core.EventID(r.SourceAccountID, r.ExternalID, kind),
		SourceAccountID: r.SourceAccountID,
		ExternalID:      r.ExternalID,
		Kind:            kind,
		SubjectKey:      fmt.Sprintf("github:%s#%d", repo, item.Number),
		Title:           title,
		URL:             item.HTMLURL,
		OccurredAt:      occurred.UTC(),
		Payload:         encoded,
	}}, nil
}

// WhoAmI verifies a token and returns the login it belongs to.
//
// Used when connecting an account so a bad token fails immediately with a
// clear message, rather than producing syncs that quietly return nothing.
func WhoAmI(ctx context.Context, token, baseURL string) (string, error) {
	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", describeFailure(resp)
	}

	var body struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Login == "" {
		return "", fmt.Errorf("github returned no login for this token")
	}
	return body.Login, nil
}
