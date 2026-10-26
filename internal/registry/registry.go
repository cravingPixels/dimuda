// Package registry talks to OCI distribution registries (Docker Hub, ghcr.io, lscr.io, quay.io, ...)
// anonymously, using the bearer-token challenge every public registry implements.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/cravingPixels/image-update-dashboard/internal/imageref"
)

const (
	pageSize = 1000
	// immich publishes ~30k tags; this leaves headroom without letting a broken Link header loop forever.
	maxPages = 200
)

var manifestAccept = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

var (
	challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)
	nextLink       = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

type Client struct {
	http     *http.Client
	endpoint func(registry string) string

	mu     sync.Mutex
	tokens map[string]string
}

func New(httpClient *http.Client) *Client {
	return &Client{http: httpClient, endpoint: DefaultEndpoint, tokens: map[string]string{}}
}

// WithEndpoint replaces how a registry name maps to a base URL; used to point tests at a fake registry.
func (c *Client) WithEndpoint(endpoint func(registry string) string) *Client {
	c.endpoint = endpoint
	return c
}

func DefaultEndpoint(registry string) string {
	switch registry {
	case imageref.DockerHub:
		return "https://registry-1.docker.io"
	case "lscr.io":
		// lscr.io is a redirecting front for ghcr.io; talking to ghcr.io directly avoids a redirect per request.
		return "https://ghcr.io"
	}
	return "https://" + registry
}

func (c *Client) ListTags(ctx context.Context, ref imageref.Ref) ([]string, error) {
	base := c.endpoint(ref.Registry)
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=%d", base, ref.Repository, pageSize)
	var all []string
	for page := 0; next != "" && page < maxPages; page++ {
		resp, err := c.do(ctx, http.MethodGet, next, ref, "")
		if err != nil {
			return nil, err
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode tags of %s: %w", ref.Name(), err)
		}
		all = append(all, body.Tags...)
		next = resolveNext(base, link)
	}
	return all, nil
}

func resolveNext(base, link string) string {
	m := nextLink.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "http") {
		return m[1]
	}
	return base + m[1]
}

// Digest returns the manifest (or index) digest a tag currently points to. It uses HEAD,
// which Docker Hub does not count against the anonymous pull rate limit.
func (c *Client) Digest(ctx context.Context, ref imageref.Ref, tag string) (string, error) {
	u := fmt.Sprintf("%s/v2/%s/manifests/%s", c.endpoint(ref.Registry), ref.Repository, url.PathEscape(tag))
	resp, err := c.do(ctx, http.MethodHead, u, ref, manifestAccept)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry returned no digest for %s:%s", ref.Name(), tag)
	}
	return digest, nil
}

func (c *Client) do(ctx context.Context, method, u string, ref imageref.Ref, accept string) (*http.Response, error) {
	resp, err := c.send(ctx, method, u, accept, c.token(ref))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err := c.fetchToken(ctx, challenge, ref)
		if err != nil {
			return nil, err
		}
		if resp, err = c.send(ctx, method, u, accept, token); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &StatusError{Code: resp.StatusCode, URL: u}
	}
	return resp, nil
}

// send retries once on transport errors: ghcr.io occasionally stalls on deep tag-list pages.
func (c *Client) send(ctx context.Context, method, u, accept, token string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := c.http.Do(req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) token(ref imageref.Ref) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[ref.Name()]
}

func (c *Client) fetchToken(ctx context.Context, challenge string, ref imageref.Ref) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("%s requires unsupported auth %q", ref.Registry, challenge)
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(challenge, -1) {
		params[m[1]] = m[2]
	}
	if params["realm"] == "" {
		return "", fmt.Errorf("%s auth challenge has no realm", ref.Registry)
	}
	q := url.Values{}
	if params["service"] != "" {
		q.Set("service", params["service"])
	}
	q.Set("scope", "repository:"+ref.Repository+":pull")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params["realm"]+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return "", &StatusError{Code: resp.StatusCode, URL: params["realm"]}
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode token from %s: %w", params["realm"], err)
	}
	token := body.Token
	if token == "" {
		token = body.AccessToken
	}
	c.mu.Lock()
	c.tokens[ref.Name()] = token
	c.mu.Unlock()
	return token, nil
}

type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("registry returned HTTP %d for %s", e.Code, e.URL)
}

func (e *StatusError) NotFound() bool {
	return e.Code == http.StatusNotFound || e.Code == http.StatusUnauthorized || e.Code == http.StatusForbidden
}
