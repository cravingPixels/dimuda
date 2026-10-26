// Package docker reads running containers from the Docker Engine API, typically through a
// read-only docker-socket-proxy (CONTAINERS=1, IMAGES=1).
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/cravingPixels/dimuda/internal/imageref"
)

const (
	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
	labelVersion = "org.opencontainers.image.version"
	labelSource  = "org.opencontainers.image.source"
)

type Container struct {
	Name        string
	Image       string
	State       string
	Project     string
	Service     string
	RepoDigests []string
	Version     string
	SourceURL   string
}

// DigestFor returns the registry digest this container's image was pulled with, for the given repository.
func (c Container) DigestFor(ref imageref.Ref) string {
	for _, rd := range c.RepoDigests {
		name, digest, ok := strings.Cut(rd, "@")
		if !ok {
			continue
		}
		parsed, err := imageref.Parse(name)
		if err == nil && parsed.Name() == ref.Name() {
			return digest
		}
	}
	return ""
}

type Client struct {
	http *http.Client
	base string
}

// New accepts the same DOCKER_HOST forms as the docker CLI: tcp://host:port or unix:///path.
func New(host string) (*Client, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse docker host %q: %w", host, err)
	}
	switch u.Scheme {
	case "tcp", "http":
		return &Client{http: &http.Client{}, base: "http://" + u.Host}, nil
	case "https":
		return &Client{http: &http.Client{}, base: "https://" + u.Host}, nil
	case "unix":
		socket := u.Path
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		return &Client{http: &http.Client{Transport: transport}, base: "http://docker"}, nil
	}
	return nil, fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
}

func (c *Client) Containers(ctx context.Context) ([]Container, error) {
	var list []struct {
		Names   []string
		Image   string
		ImageID string
		State   string
		Labels  map[string]string
	}
	if err := c.get(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}

	images := map[string]imageInfo{}
	containers := make([]Container, 0, len(list))
	for _, item := range list {
		info, ok := images[item.ImageID]
		if !ok {
			var err error
			if info, err = c.inspectImage(ctx, item.ImageID); err != nil {
				return nil, err
			}
			images[item.ImageID] = info
		}
		name := ""
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		containers = append(containers, Container{
			Name:        name,
			Image:       item.Image,
			State:       item.State,
			Project:     item.Labels[labelProject],
			Service:     item.Labels[labelService],
			RepoDigests: info.RepoDigests,
			Version:     info.Config.Labels[labelVersion],
			SourceURL:   info.Config.Labels[labelSource],
		})
	}
	return containers, nil
}

type imageInfo struct {
	RepoDigests []string
	Config      struct {
		Labels map[string]string
	}
}

func (c *Client) inspectImage(ctx context.Context, id string) (imageInfo, error) {
	var info imageInfo
	err := c.get(ctx, "/images/"+url.PathEscape(id)+"/json", &info)
	return info, err
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker API %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker API %s: HTTP %d (does the socket proxy allow CONTAINERS and IMAGES?)", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
