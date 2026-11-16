package overrides

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cravingPixels/dimuda/internal/imageref"
	"github.com/cravingPixels/dimuda/internal/versioning"
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
	if (set.For(other) != Image{AutoUpdate: LevelPatch}) {
		t.Error("unknown image should have no override beyond the default patch level")
	}
}

func TestParseRejectsBadRegex(t *testing.T) {
	if _, err := Parse([]byte("images:\n  redis:\n    tagRegex: '^(\\d+)$'\n")); err == nil {
		t.Error("expected error for regex without version group")
	}
}

func TestLoadMissingFileFails(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("expected error for missing overrides file")
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

func TestAutoUpdateLevel(t *testing.T) {
	set, err := Parse([]byte("images:\n  getmeili/meilisearch: {autoUpdate: none}\n  traefik: {autoUpdate: minor}\n  redis: {note: x}\n"))
	if err != nil {
		t.Fatal(err)
	}
	level := func(name string) Level {
		ref, _ := imageref.Parse(name)
		return set.For(ref).AutoUpdate
	}
	if level("getmeili/meilisearch") != LevelNone || level("traefik") != LevelMinor || level("redis") != LevelPatch {
		t.Errorf("levels = %s %s %s", level("getmeili/meilisearch"), level("traefik"), level("redis"))
	}
	if _, err := Parse([]byte("images:\n  redis: {autoUpdate: major}\n")); err == nil {
		t.Error("autoUpdate: major must be rejected; majors always need review")
	}

	cases := []struct {
		level Level
		kind  versioning.Kind
		want  bool
	}{
		{LevelPatch, versioning.KindPatch, true},
		{LevelPatch, versioning.KindRebuild, true},
		{LevelPatch, versioning.KindPin, true},
		{LevelPatch, versioning.KindMinor, false},
		{LevelMinor, versioning.KindMinor, true},
		{LevelMinor, versioning.KindMajor, false},
		{LevelMinor, versioning.KindDigest, false},
		{LevelNone, versioning.KindPin, false},
	}
	for _, c := range cases {
		if got := c.level.Allows(c.kind); got != c.want {
			t.Errorf("%s.Allows(%s) = %v, want %v", c.level, c.kind, got, c.want)
		}
	}
}
