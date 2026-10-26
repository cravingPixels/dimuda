// Package notify sends update alerts to ntfy.
package notify

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type Message struct {
	Title string
	Body  string
	Click string
	Tags  []string
}

type Ntfy struct {
	http  *http.Client
	url   string
	token string
}

// NewNtfy posts to baseURL/topic; token is optional for public topics.
func NewNtfy(httpClient *http.Client, baseURL, topic, token string) *Ntfy {
	return &Ntfy{http: httpClient, url: strings.TrimRight(baseURL, "/") + "/" + topic, token: token}
}

func (n *Ntfy) Send(ctx context.Context, m Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, strings.NewReader(m.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", m.Title)
	if m.Click != "" {
		req.Header.Set("Click", m.Click)
	}
	if len(m.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(m.Tags, ","))
	}
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
	}
	return nil
}
