// Package komodo asks Komodo to redeploy the stacks whose files changed, right after dimuda
// pushes a bump, so updates do not wait for a schedule.
package komodo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	http    *http.Client
	baseURL string
	key     string
	secret  string
	pattern string
}

// New targets the stacks matching pattern ("*" for all); Komodo only redeploys those whose
// compose files or images changed since their last deploy.
func New(httpClient *http.Client, baseURL, key, secret, pattern string) *Client {
	return &Client{http: httpClient, baseURL: strings.TrimRight(baseURL, "/"), key: key, secret: secret, pattern: pattern}
}

// pollInterval is how often a running procedure is checked; a var so tests do not wait.
var pollInterval = 10 * time.Second

func (c *Client) DeployChanged(ctx context.Context) error {
	return c.call(ctx, "/execute/BatchDeployStackIfChanged", map[string]any{"pattern": c.pattern}, nil)
}

// update is the part of Komodo's Update record dimuda reads.
type update struct {
	ID struct {
		OID string `json:"$oid"`
	} `json:"_id"`
	Status  string `json:"status"`
	Success bool   `json:"success"`
}

// RunProcedure starts the named procedure and waits for it to finish, so stacks that depend on
// it (containers sharing gluetun's network) are deployed only after it is done.
func (c *Client) RunProcedure(ctx context.Context, name string) error {
	var u update
	if err := c.call(ctx, "/execute/RunProcedure", map[string]any{"procedure": name}, &u); err != nil {
		return err
	}
	if u.ID.OID == "" {
		return fmt.Errorf("komodo: procedure %s started without an update id", name)
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for u.Status != "Complete" {
		select {
		case <-ctx.Done():
			return fmt.Errorf("komodo: procedure %s still running: %w", name, ctx.Err())
		case <-ticker.C:
		}
		id := u.ID.OID
		if err := c.call(ctx, "/read/GetUpdate", map[string]any{"id": id}, &u); err != nil {
			return err
		}
		u.ID.OID = id
	}
	if !u.Success {
		return fmt.Errorf("komodo: procedure %s failed; see its update in Komodo", name)
	}
	return nil
}

func (c *Client) call(ctx context.Context, path string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.key)
	req.Header.Set("X-Api-Secret", c.secret)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("komodo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("komodo: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("komodo: decode %s response: %w", path, err)
	}
	return nil
}
