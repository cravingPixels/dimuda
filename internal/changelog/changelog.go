// Package changelog fetches upstream GitHub releases so an update can be shown with its release notes.
package changelog

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cravingPixels/dimuda/internal/versioning"
)

const (
	perPage     = 50
	maxBodySize = 6000
)

var githubRepo = regexp.MustCompile(`^(?:https?://|git@)?github\.com[/:]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

type Release struct {
	Tag       string    `json:"tag"`
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
}

// RepoFromURL turns an OCI source label or a plain "owner/repo" into "owner/repo".
func RepoFromURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if m := githubRepo.FindStringSubmatch(s); m != nil {
		return m[1] + "/" + m[2], true
	}
	if parts := strings.Split(s, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(s, ":") {
		return s, true
	}
	return "", false
}

type cached struct {
	at       time.Time
	releases []Release
	err      error
}

type Client struct {
	http    *http.Client
	baseURL string
	token   string
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]cached

	log *slog.Logger
}

func New(httpClient *http.Client, token string, ttl time.Duration) *Client {
	return &Client{http: httpClient, baseURL: "https://api.github.com", token: token, ttl: ttl, cache: map[string]cached{}}
}

func (c *Client) WithLogger(log *slog.Logger) *Client {
	c.log = log
	return c
}

func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = u
	return c
}

// Releases returns the latest non-prerelease releases of repo, cached for the client's TTL
// because unauthenticated GitHub API calls are limited to 60 per hour.
func (c *Client) Releases(ctx context.Context, repo string) ([]Release, error) {
	c.mu.Lock()
	entry, ok := c.cache[repo]
	c.mu.Unlock()
	if ok && time.Since(entry.at) < c.ttl {
		return entry.releases, entry.err
	}
	releases, err := c.fetch(ctx, repo)
	c.mu.Lock()
	c.cache[repo] = cached{at: time.Now(), releases: releases, err: err}
	c.mu.Unlock()
	return releases, err
}

func (c *Client) fetch(ctx context.Context, repo string) ([]Release, error) {
	token := c.currentToken()
	resp, err := c.get(ctx, repo, token)
	if err != nil {
		return nil, err
	}
	// Public release lists never need auth, so a rejected token must not cost us the changelogs.
	if resp.StatusCode == http.StatusUnauthorized && token != "" {
		resp.Body.Close()
		c.dropToken()
		if resp, err = c.get(ctx, repo, ""); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github releases of %s: HTTP %d", repo, resp.StatusCode)
	}
	var raw []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode releases of %s: %w", repo, err)
	}
	releases := make([]Release, 0, len(raw))
	for _, r := range raw {
		if r.Draft || r.Prerelease {
			continue
		}
		releases = append(releases, Release{Tag: r.TagName, Name: r.Name, Body: truncate(r.Body), URL: r.HTMLURL, Published: r.PublishedAt})
	}
	return releases, nil
}

func (c *Client) get(ctx context.Context, repo, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases?per_page=%d", c.baseURL, repo, perPage), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github releases of %s: %w", repo, err)
	}
	return resp, nil
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) dropToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return
	}
	c.token = ""
	log := c.log
	if log == nil {
		log = slog.Default()
	}
	log.Warn("GITHUB_TOKEN rejected by GitHub (HTTP 401); continuing without it at 60 requests/hour. Fix or empty GITHUB_TOKEN.")
}

func truncate(s string) string {
	if len(s) <= maxBodySize {
		return s
	}
	return s[:maxBodySize] + "\n\n… (truncated, open the release for the full notes)"
}

// Between keeps the releases newer than from and up to and including to, comparing
// versions only, so an lsio tag "4.0.15-ls290" matches the upstream release "v4.0.15".
func Between(releases []Release, from, to versioning.Tag) []Release {
	var out []Release
	for _, r := range releases {
		v, ok := ParseReleaseTag(r.Tag)
		if !ok {
			continue
		}
		if versioning.CompareVersions(v, from) > 0 && versioning.CompareVersions(v, to) <= 0 {
			out = append(out, r)
		}
	}
	return out
}

// Newest returns the highest-versioned release, which can be ahead of what the image publisher has packaged yet.
func Newest(releases []Release) (Release, versioning.Tag, bool) {
	var best Release
	var bestTag versioning.Tag
	found := false
	for _, r := range releases {
		v, ok := ParseReleaseTag(r.Tag)
		if ok && (!found || versioning.CompareVersions(v, bestTag) > 0) {
			best, bestTag, found = r, v, true
		}
	}
	return best, bestTag, found
}

// ParseReleaseTag also accepts monorepo-style tags such as "n8n@2.45.4".
func ParseReleaseTag(tag string) (versioning.Tag, bool) {
	if i := strings.LastIndex(tag, "@"); i >= 0 {
		tag = tag[i+1:]
	}
	return (versioning.Parser{}).Parse(tag)
}
