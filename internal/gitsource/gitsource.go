// Package gitsource keeps shallow clones of the git repositories that hold compose files.
package gitsource

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repo struct {
	Name string
	URL  string
}

type Syncer struct {
	baseDir string
	token   string
	git     string
}

// NewSyncer clones into baseDir. A non-empty token is sent as an HTTP header instead of
// being embedded in the URL, so it never lands in .git/config on the data volume.
func NewSyncer(baseDir, token string) *Syncer {
	return &Syncer{baseDir: baseDir, token: token, git: "git"}
}

// Sync clones or fast-forwards repo and returns its working tree path.
func (s *Syncer) Sync(ctx context.Context, repo Repo) (string, error) {
	dir := filepath.Join(s.baseDir, repo.Name)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if err := s.run(ctx, dir, "fetch", "--depth", "1", "origin"); err != nil {
			return "", err
		}
		return dir, s.run(ctx, dir, "reset", "--hard", "FETCH_HEAD")
	}
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return "", err
	}
	os.RemoveAll(dir)
	return dir, s.run(ctx, s.baseDir, "clone", "--depth", "1", repo.URL, repo.Name)
}

func (s *Syncer) run(ctx context.Context, dir string, args ...string) error {
	if s.token != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + s.token))
		args = append([]string{"-c", "http.extraHeader=Authorization: Basic " + auth}, args...)
	}
	cmd := exec.CommandContext(ctx, s.git, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, s.redact(strings.TrimSpace(string(out))))
	}
	return nil
}

func (s *Syncer) redact(msg string) string {
	if s.token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.token, "***")
}
