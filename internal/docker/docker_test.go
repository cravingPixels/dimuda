package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cravingPixels/image-update-dashboard/internal/imageref"
)

func TestContainers(t *testing.T) {
	imageCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			w.Write([]byte(`[
				{"Names":["/immich_redis"],"Image":"redis:8-alpine","ImageID":"sha256:img1","State":"running",
				 "Labels":{"com.docker.compose.project":"immich","com.docker.compose.service":"redis"}},
				{"Names":["/linkwarden-cache"],"Image":"redis:8-alpine","ImageID":"sha256:img1","State":"exited","Labels":{}}
			]`))
		case "/images/sha256:img1/json":
			imageCalls++
			w.Write([]byte(`{"RepoDigests":["redis@sha256:aaa"],"Config":{"Labels":{
				"org.opencontainers.image.version":"8.10.2","org.opencontainers.image.source":"https://github.com/redis/docker-library-redis"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(strings.Replace(srv.URL, "http://", "tcp://", 1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || imageCalls != 1 {
		t.Fatalf("got %d containers with %d image calls, want 2 and 1", len(got), imageCalls)
	}
	first := got[0]
	if first.Name != "immich_redis" || first.Project != "immich" || first.Service != "redis" || first.Version != "8.10.2" || first.State != "running" {
		t.Errorf("first = %+v", first)
	}
	ref, _ := imageref.Parse("redis:8-alpine")
	if d := first.DigestFor(ref); d != "sha256:aaa" {
		t.Errorf("DigestFor = %q", d)
	}
	other, _ := imageref.Parse("valkey/valkey:8")
	if d := first.DigestFor(other); d != "" {
		t.Errorf("DigestFor other repo = %q, want empty", d)
	}
}

func TestErrorMentionsProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "socket proxy") {
		t.Errorf("err = %v", err)
	}
}

func TestNewRejectsUnknownScheme(t *testing.T) {
	if _, err := New("ssh://host"); err == nil {
		t.Error("expected error")
	}
	if _, err := New("unix:///var/run/docker.sock"); err != nil {
		t.Error(err)
	}
}
