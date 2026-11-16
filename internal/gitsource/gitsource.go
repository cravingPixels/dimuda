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
	dir := s.Dir(repo)
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

type Author struct {
	Name  string
	Email string
}

// Dir is where repo is checked out.
func (s *Syncer) Dir(repo Repo) string {
	return filepath.Join(s.baseDir, repo.Name)
}

// Commit records the given files, relative to the checkout, as one commit.
func (s *Syncer) Commit(ctx context.Context, repo Repo, files []string, message string, author Author) error {
	dir := s.Dir(repo)
	if err := s.run(ctx, dir, append([]string{"add", "--"}, files...)...); err != nil {
		return err
	}
	return s.run(ctx, dir, "-c", "user.name="+author.Name, "-c", "user.email="+author.Email, "commit", "-q", "-m", message)
}

// Push sends local commits to the branch the checkout tracks. A rejected push (someone pushed
// meanwhile) is returned as an error; the next Sync resets the checkout and the next scan
// recomputes the changes on top of the new history.
func (s *Syncer) Push(ctx context.Context, repo Repo) error {
	return s.run(ctx, s.Dir(repo), "push", "-q", "origin", "HEAD")
}

// Discard drops uncommitted edits, e.g. after one change of a batch failed to apply.
func (s *Syncer) Discard(ctx context.Context, repo Repo) error {
	return s.run(ctx, s.Dir(repo), "checkout", "-q", "--", ".")
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
		return fmt.Errorf("git %s: %w: %s", subcommand(args), err, s.redact(strings.TrimSpace(string(out))))
	}
	return nil
}

func (s *Syncer) redact(msg string) string {
	if s.token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.token, "***")
}

// subcommand names the git command in errors, skipping "-c key=value" options so a commit
// failure does not print the author or the auth header.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return "git"
}
