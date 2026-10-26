package changelog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cravingPixels/image-update-dashboard/internal/versioning"
)

func TestRepoFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/redis/docker-library-redis":    "redis/docker-library-redis",
		"https://github.com/linuxserver/docker-sonarr.git": "linuxserver/docker-sonarr",
		"git@github.com:amir20/dozzle.git":                 "amir20/dozzle",
		"github.com/immich-app/immich/":                    "immich-app/immich",
		"Sonarr/Sonarr":                                    "Sonarr/Sonarr",
	}
	for in, want := range cases {
		got, ok := RepoFromURL(in)
		if !ok || got != want {
			t.Errorf("RepoFromURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "https://gitlab.com/a/b", "a/b/c"} {
		if got, ok := RepoFromURL(in); ok {
			t.Errorf("RepoFromURL(%q) = %q, want no match", in, got)
		}
	}
}

func TestReleasesFiltersAndCaches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/repos/o/r/releases" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`[
			{"tag_name":"v2.0.0-rc1","prerelease":true},
			{"tag_name":"v1.1.0","name":"One one","body":"` + strings.Repeat("x", maxBodySize+10) + `","html_url":"u"},
			{"tag_name":"v1.0.0","draft":true}
		]`))
	}))
	defer srv.Close()

	c := New(srv.Client(), "tok", time.Hour).WithBaseURL(srv.URL)
	got, err := c.Releases(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tag != "v1.1.0" || !strings.HasSuffix(got[0].Body, "full notes)") {
		t.Fatalf("got %+v", got)
	}
	c.Releases(context.Background(), "o/r")
	if calls != 1 {
		t.Errorf("GitHub called %d times, want 1 (cached)", calls)
	}
}

func TestBetween(t *testing.T) {
	releases := []Release{{Tag: "v4.0.17"}, {Tag: "v4.0.16"}, {Tag: "v4.0.15"}, {Tag: "nightly"}, {Tag: "v4.0.14"}}
	p := versioning.Parser{}
	from, _ := p.Parse("4.0.15-ls290")
	to, _ := p.Parse("4.0.16-ls292")
	got := Between(releases, from, to)
	if len(got) != 1 || got[0].Tag != "v4.0.16" {
		t.Errorf("Between = %+v", got)
	}
}

func TestNewest(t *testing.T) {
	r, tag, ok := Newest([]Release{{Tag: "n8n@2.44.0"}, {Tag: "stable"}, {Tag: "n8n@2.45.4"}})
	if !ok || r.Tag != "n8n@2.45.4" || tag.VersionString() != "2.45.4" {
		t.Errorf("Newest = %+v %+v %v", r, tag, ok)
	}
	if _, _, ok := Newest([]Release{{Tag: "stable"}}); ok {
		t.Error("expected no parseable release")
	}
}
