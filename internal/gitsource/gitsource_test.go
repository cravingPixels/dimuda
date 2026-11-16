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

func bareUpstream(t *testing.T) (bare, work string) {
	t.Helper()
	bare, work = t.TempDir(), t.TempDir()
	git(t, bare, "init", "-q", "--bare", "-b", "main")
	git(t, work, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "compose.yaml"), []byte("image: a:1\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "one")
	git(t, work, "push", "-q", "file://"+bare, "main")
	return bare, work
}

func output(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCommitAndPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare, _ := bareUpstream(t)
	s := NewSyncer(t.TempDir(), "")
	repo := Repo{Name: "stacks", URL: "file://" + bare}
	dir, err := s.Sync(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("image: a:2\n"), 0o644)
	if err := s.Commit(context.Background(), repo, []string{"compose.yaml"}, "apps: bump a 1 → 2 (patch)", Author{Name: "dimuda", Email: "dimuda@localhost"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if got := output(t, bare, "log", "-1", "--format=%an|%s", "main"); got != "dimuda|apps: bump a 1 → 2 (patch)" {
		t.Errorf("upstream head = %q", got)
	}
}

func TestPushRejectedWhenUpstreamMoved(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare, work := bareUpstream(t)
	s := NewSyncer(t.TempDir(), "")
	repo := Repo{Name: "stacks", URL: "file://" + bare}
	dir, err := s.Sync(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(work, "README.md"), []byte("x"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "someone else")
	git(t, work, "push", "-q", "file://"+bare, "main")

	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("image: a:2\n"), 0o644)
	if err := s.Commit(context.Background(), repo, []string{"compose.yaml"}, "bump", Author{Name: "dimuda", Email: "d@l"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Push(context.Background(), repo); err == nil {
		t.Fatal("push over a moved upstream must fail, not force")
	}
	if _, err := s.Sync(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != "image: a:1\n" {
		t.Errorf("after the next sync the unpushed edit must be gone, got %q", b)
	}
}

func TestDiscard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	bare, _ := bareUpstream(t)
	s := NewSyncer(t.TempDir(), "")
	repo := Repo{Name: "stacks", URL: "file://" + bare}
	dir, _ := s.Sync(context.Background(), repo)
	os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("half-applied"), 0o644)
	if err := s.Discard(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "compose.yaml")); string(b) != "image: a:1\n" {
		t.Errorf("after discard got %q", b)
	}
}
