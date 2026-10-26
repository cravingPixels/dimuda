// Package config reads the dashboard settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/scanner"
)

type Config struct {
	ListenAddr    string
	DataDir       string
	PublicURL     string
	ComposeDirs   []scanner.Dir
	GitRepos      []gitsource.Repo
	GitToken      string
	DockerHost    string
	GitHubToken   string
	OverridesFile string
	UserOverrides string
	ScanInterval  time.Duration
	PUID          int
	PGID          int
	ExtraGIDs     []int
	NtfyURL       string
	NtfyTopic     string
	NtfyToken     string
}

// Load validates everything up front so a misconfigured container exits at start instead
// of serving an empty dashboard.
func Load(getenv func(string) string) (Config, error) {
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		ListenAddr:    get("LISTEN_ADDR", ":8080"),
		DataDir:       get("DATA_DIR", "/data"),
		PublicURL:     get("PUBLIC_URL", ""),
		GitToken:      get("GIT_TOKEN", ""),
		DockerHost:    get("DOCKER_HOST", ""),
		GitHubToken:   get("GITHUB_TOKEN", ""),
		OverridesFile: get("OVERRIDES_FILE", "/app/overrides.yaml"),
		UserOverrides: get("USER_OVERRIDES_FILE", "/config/overrides.yaml"),
		NtfyURL:       get("NTFY_URL", ""),
		NtfyTopic:     get("NTFY_TOPIC", ""),
		NtfyToken:     get("NTFY_TOKEN", ""),
	}

	var errs []error
	var err error
	if c.ScanInterval, err = time.ParseDuration(get("SCAN_INTERVAL", "6h")); err != nil {
		errs = append(errs, fmt.Errorf("SCAN_INTERVAL: %w", err))
	} else if c.ScanInterval < 5*time.Minute {
		errs = append(errs, fmt.Errorf("SCAN_INTERVAL must be at least 5m to stay inside registry and GitHub rate limits"))
	}

	if c.PUID, err = parseID(get("PUID", "65532")); err != nil {
		errs = append(errs, fmt.Errorf("PUID: %w", err))
	}
	if c.PGID, err = parseID(get("PGID", "65532")); err != nil {
		errs = append(errs, fmt.Errorf("PGID: %w", err))
	}
	for _, raw := range strings.Split(get("STACKS_READ_GID", ""), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		gid, err := parseID(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("STACKS_READ_GID: %w", err))
			continue
		}
		c.ExtraGIDs = append(c.ExtraGIDs, gid)
	}

	dirs, err := parseNamed(get("COMPOSE_DIRS", ""))
	if err != nil {
		errs = append(errs, fmt.Errorf("COMPOSE_DIRS: %w", err))
	}
	for name, path := range dirs {
		c.ComposeDirs = append(c.ComposeDirs, scanner.Dir{Name: name, Path: path})
	}

	repos, err := parseNamed(get("GIT_REPOS", ""))
	if err != nil {
		errs = append(errs, fmt.Errorf("GIT_REPOS: %w", err))
	}
	for name, url := range repos {
		c.GitRepos = append(c.GitRepos, gitsource.Repo{Name: name, URL: url})
	}

	if len(c.ComposeDirs) == 0 && len(c.GitRepos) == 0 {
		errs = append(errs, errors.New("set COMPOSE_DIRS and/or GIT_REPOS, otherwise there is nothing to scan"))
	}
	if (c.NtfyURL == "") != (c.NtfyTopic == "") {
		errs = append(errs, errors.New("NTFY_URL and NTFY_TOPIC must be set together"))
	}
	return c, errors.Join(errs...)
}

// parseNamed reads "name=value,name2=value2". A bare value is named after its last path element.
func parseNamed(raw string) (map[string]string, error) {
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			value = item
			name = strings.TrimSuffix(filepath.Base(strings.TrimRight(item, "/")), ".git")
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if name == "" || value == "" {
			return nil, fmt.Errorf("invalid entry %q", item)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("duplicate name %q", name)
		}
		out[name] = value
	}
	return out, nil
}

func parseID(raw string) (int, error) {
	id, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	if id <= 0 {
		return 0, fmt.Errorf("%d is not allowed; dimuda never runs as root", id)
	}
	return id, nil
}

func (c Config) NtfyEnabled() bool {
	return c.NtfyURL != "" && c.NtfyTopic != ""
}
