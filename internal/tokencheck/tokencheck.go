// Package tokencheck reads when a GitHub token expires, so the admin is warned before git
// pushes and release notes stop working.
package tokencheck

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// expiryHeader is set by GitHub on every API response for a token that expires.
const expiryHeader = "GitHub-Authentication-Token-Expiration"

var ErrRejected = fmt.Errorf("GitHub rejected the token (expired or revoked)")

type GitHub struct {
	http    *http.Client
	baseURL string
}

func NewGitHub(httpClient *http.Client) *GitHub {
	return &GitHub{http: httpClient, baseURL: "https://api.github.com"}
}

func (g *GitHub) WithBaseURL(u string) *GitHub {
	g.baseURL = strings.TrimRight(u, "/")
	return g
}

// Expiry returns when token expires; ok is false for a token without an expiry date.
// /rate_limit is used because calling it does not count against the rate limit.
func (g *GitHub) Expiry(ctx context.Context, token string) (expires time.Time, ok bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/rate_limit", nil)
	if err != nil {
		return time.Time{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.http.Do(req)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("github: %w", err)
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return time.Time{}, false, ErrRejected
	case resp.StatusCode != http.StatusOK:
		return time.Time{}, false, fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	raw := resp.Header.Get(expiryHeader)
	if raw == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("github: unreadable token expiration %q", raw)
}
