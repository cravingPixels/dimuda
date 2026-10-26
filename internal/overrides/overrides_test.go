package overrides

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cravingPixels/dimuda/internal/imageref"
)

func TestParseAndLookup(t *testing.T) {
	set, err := Parse([]byte(`
images:
  redis:
    changelog: redis/redis
  lscr.io/linuxserver/firefox:
    ignore: true
    note: Ubuntu package versions
  dokuwiki/dokuwiki:
    tagRegex: '^(?P<version>\d{4}-\d{2}-\d{2})(?P<build>[a-z]?)$'
`))
	if err != nil {
		t.Fatal(err)
	}
	redis, _ := imageref.Parse("docker.io/library/redis:8")
	if got := set.For(redis).Changelog; got != "redis/redis" {
		t.Errorf("redis changelog = %q", got)
	}
	firefox, _ := imageref.Parse("lscr.io/linuxserver/firefox:latest")
	if !set.For(firefox).Ignore {
		t.Error("firefox should be ignored")
	}
	doku, _ := imageref.Parse("dokuwiki/dokuwiki:stable")
	if tag, ok := set.For(doku).Parser().Parse("2026-07-14c"); !ok || tag.Build != "c" {
		t.Errorf("custom parser result %+v %v", tag, ok)
	}
	other, _ := imageref.Parse("nginx")
	if (set.For(other) != Image{}) {
		t.Error("unknown image should have no override")
	}
}

func TestParseRejectsBadRegex(t *testing.T) {
	if _, err := Parse([]byte("images:\n  redis:\n    tagRegex: '^(\\d+)$'\n")); err == nil {
		t.Error("expected error for regex without version group")
	}
}

func TestLoadMissingFile(t *testing.T) {
	set, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := imageref.Parse("redis")
	if set.For(ref).Changelog != "" {
		t.Error("expected empty set")
	}
}

func TestLoadLayered(t *testing.T) {
	dir := t.TempDir()
	bundled := filepath.Join(dir, "bundled.yaml")
	user := filepath.Join(dir, "user.yaml")
	os.WriteFile(bundled, []byte("images:\n  redis: {changelog: redis/redis}\n  traefik: {changelog: traefik/traefik}\n"), 0o644)
	os.WriteFile(user, []byte("images:\n  redis: {ignore: true}\n"), 0o644)

	set, err := LoadLayered(bundled, user)
	if err != nil {
		t.Fatal(err)
	}
	redis, _ := imageref.Parse("redis")
	traefik, _ := imageref.Parse("traefik")
	if img := set.For(redis); !img.Ignore || img.Changelog != "" {
		t.Errorf("user entry should replace bundled one, got %+v", img)
	}
	if set.For(traefik).Changelog != "traefik/traefik" {
		t.Error("bundled entries without a user entry must remain")
	}

	if _, err := LoadLayered(bundled, filepath.Join(dir, "missing.yaml")); err != nil {
		t.Errorf("missing user file must be fine: %v", err)
	}
	if _, err := LoadLayered(filepath.Join(dir, "missing.yaml"), user); err == nil {
		t.Error("missing bundled file must fail")
	}
}

func TestShippedOverridesFileIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "overrides.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skip("no shipped overrides.yaml")
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}
