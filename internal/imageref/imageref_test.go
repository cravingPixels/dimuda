package imageref

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw  string
		want Ref
	}{
		{"redis:8-alpine", Ref{"docker.io", "library/redis", "8-alpine", ""}},
		{"redis", Ref{"docker.io", "library/redis", "latest", ""}},
		{"traefik:v3", Ref{"docker.io", "library/traefik", "v3", ""}},
		{"amir20/dozzle:latest", Ref{"docker.io", "amir20/dozzle", "latest", ""}},
		{"docker.io/valkey/valkey:7-alpine", Ref{"docker.io", "valkey/valkey", "7-alpine", ""}},
		{"docker.io/postgres:17", Ref{"docker.io", "library/postgres", "17", ""}},
		{"index.docker.io/library/redis:8", Ref{"docker.io", "library/redis", "8", ""}},
		{"lscr.io/linuxserver/sonarr:4.0.15-ls290", Ref{"lscr.io", "linuxserver/sonarr", "4.0.15-ls290", ""}},
		{"ghcr.io/immich-app/immich-server:v1.2.3", Ref{"ghcr.io", "immich-app/immich-server", "v1.2.3", ""}},
		{"localhost:5000/app:1", Ref{"localhost:5000", "app", "1", ""}},
		{"redis:8@sha256:abc", Ref{"docker.io", "library/redis", "8", "sha256:abc"}},
		{"redis@sha256:abc", Ref{"docker.io", "library/redis", "", "sha256:abc"}},
		{"komodo-mongo:latest", Ref{"docker.io", "library/komodo-mongo", "latest", ""}},
	}
	for _, c := range cases {
		got, err := Parse(c.raw)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{"", "  ", "ghcr.io/immich-app/immich-server:${IMMICH_VERSION}", ":tag"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) expected error", raw)
		}
	}
}

func TestShortNameAndString(t *testing.T) {
	r, _ := Parse("redis:8@sha256:abc")
	if r.ShortName() != "redis" || r.String() != "redis:8@sha256:abc" {
		t.Errorf("got %q / %q", r.ShortName(), r.String())
	}
	r, _ = Parse("ghcr.io/a/b:1")
	if r.ShortName() != "ghcr.io/a/b" || r.Name() != "ghcr.io/a/b" {
		t.Errorf("got %q / %q", r.ShortName(), r.Name())
	}
}
