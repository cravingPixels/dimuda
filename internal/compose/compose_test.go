package compose

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "immich", "compose.yaml"), `
name: immich
services:
  server:
    image: ghcr.io/immich-app/immich-server:${IMMICH_VERSION:-release}
  redis:
    image: redis:8-alpine
  db:
    image: ${DB_IMAGE}
`)
	write(t, filepath.Join(root, "immich", ".env"), "IMMICH_VERSION=\"v2.1.0\" # pinned\n")
	write(t, filepath.Join(root, "linkwarden", "docker-compose.yml"), `
services:
  cache:
    image: redis:8-alpine
  mongo:
    image: komodo-mongo:latest
    build: ../images/mongo
  builder:
    build: .
`)
	write(t, filepath.Join(root, "old", "compose.yaml.bkp"), "services: {a: {image: ignored:1}}")
	write(t, filepath.Join(root, ".git", "compose.yaml"), "services: {a: {image: ignored:1}}")
	write(t, filepath.Join(root, "broken", "compose.yaml"), "services: [")

	usages, err := Scan("host", root)
	if err != nil {
		t.Fatal(err)
	}

	byKey := map[string]Usage{}
	for _, u := range usages {
		byKey[u.Stack+"/"+u.Service] = u
	}
	if len(usages) != 6 {
		t.Fatalf("got %d usages: %+v", len(usages), usages)
	}
	if u := byKey["immich/server"]; u.Image != "ghcr.io/immich-app/immich-server:v2.1.0" || u.Ref.Tag != "v2.1.0" || u.File != filepath.Join("immich", "compose.yaml") {
		t.Errorf("immich/server = %+v", u)
	}
	if u := byKey["immich/db"]; u.Err == "" {
		t.Errorf("unresolved variable should be reported, got %+v", u)
	}
	if u := byKey["linkwarden/cache"]; u.Ref.Name() != "docker.io/library/redis" || u.Source != "host" {
		t.Errorf("linkwarden/cache = %+v", u)
	}
	if u := byKey["linkwarden/mongo"]; !u.LocalBuild {
		t.Errorf("linkwarden/mongo should be a local build: %+v", u)
	}
	if u := byKey["broken/"]; u.Err == "" {
		t.Errorf("broken YAML should be reported: %+v", u)
	}
}

func TestScanMissingDir(t *testing.T) {
	if _, err := Scan("host", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing directory")
	}
}

func TestInterpolate(t *testing.T) {
	env := map[string]string{"SET": "x", "EMPTY": ""}
	cases := map[string]string{
		"${SET}":          "x",
		"$SET":            "x",
		"${EMPTY:-d}":     "d",
		"${EMPTY-d}":      "",
		"${UNSET-d}":      "d",
		"${UNSET:-d}":     "d",
		"${UNSET}":        "${UNSET}",
		"img:${SET}-tail": "img:x-tail",
	}
	for in, want := range cases {
		if got := interpolate(in, env); got != want {
			t.Errorf("interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanValue(t *testing.T) {
	cases := map[string]string{
		`"quoted # not a comment"`: "quoted # not a comment",
		`'single'`:                 "single",
		`plain  # comment`:         "plain",
		`/mnt/pool`:                "/mnt/pool",
	}
	for in, want := range cases {
		if got := cleanValue(in); got != want {
			t.Errorf("cleanValue(%q) = %q, want %q", in, got, want)
		}
	}
}
