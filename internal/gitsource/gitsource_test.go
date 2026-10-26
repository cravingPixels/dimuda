package gitsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSyncClonesThenUpdates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	upstream := t.TempDir()
	git(t, upstream, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(upstream, "compose.yaml"), []byte("v1"), 0o644)
	git(t, upstream, "add", ".")
	git(t, upstream, "commit", "-qm", "one")

	s := NewSyncer(t.TempDir(), "")
	repo := Repo{Name: "stacks", URL: "file://" + upstream}
	dir, err := s.Sync(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != "v1" {
		t.Fatalf("after clone got %q", b)
	}

	os.WriteFile(filepath.Join(upstream, "compose.yaml"), []byte("v2"), 0o644)
	git(t, upstream, "commit", "-qam", "two")
	if _, err := s.Sync(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != "v2" {
		t.Fatalf("after update got %q", b)
	}
}

func TestSyncRedactsToken(t *testing.T) {
	s := NewSyncer(t.TempDir(), "s3cr3t")
	_, err := s.Sync(context.Background(), Repo{Name: "x", URL: "file:///nonexistent/s3cr3t"})
	if err == nil {
		t.Fatal("expected clone error")
	}
	if got := err.Error(); strings.Contains(got, "s3cr3t") {
		t.Errorf("token leaked in error: %s", got)
	}
}
