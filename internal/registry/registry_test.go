package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cravingPixels/dimuda/internal/imageref"
)

func fakeRegistry(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var tokenCalls int32
	mux := http.NewServeMux()
	var srv *httptest.Server
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		if r.URL.Query().Get("scope") != "repository:org/app:pull" || r.URL.Query().Get("service") != "fake" {
			t.Errorf("unexpected token query %q", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "secret"})
	})
	mux.HandleFunc("/v2/org/app/tags/list", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		if r.URL.Query().Get("last") == "" {
			w.Header().Set("Link", `</v2/org/app/tags/list?n=1000&last=b>; rel="next"`)
			json.NewEncoder(w).Encode(map[string][]string{"tags": {"a", "b"}})
			return
		}
		json.NewEncoder(w).Encode(map[string][]string{"tags": {"c"}})
	})
	mux.HandleFunc("/v2/org/app/manifests/1.0", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		if r.Method != http.MethodHead || !strings.Contains(r.Header.Get("Accept"), "image.index") {
			t.Errorf("unexpected manifest request %s accept=%q", r.Method, r.Header.Get("Accept"))
		}
		w.Header().Set("Docker-Content-Digest", "sha256:abc")
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &tokenCalls
}

func newTestClient(srv *httptest.Server) *Client {
	return New(srv.Client()).WithEndpoint(func(string) string { return srv.URL })
}

func TestListTagsFollowsPaginationWithToken(t *testing.T) {
	srv, tokenCalls := fakeRegistry(t)
	c := newTestClient(srv)
	ref := imageref.Ref{Registry: "fake.io", Repository: "org/app"}

	tags, err := c.ListTags(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tags, []string{"a", "b", "c"}) {
		t.Errorf("tags = %v", tags)
	}
	if _, err := c.Digest(context.Background(), ref, "1.0"); err != nil {
		t.Fatal(err)
	}
	if *tokenCalls != 1 {
		t.Errorf("token fetched %d times, want it cached after the first", *tokenCalls)
	}
}

func TestDigest(t *testing.T) {
	srv, _ := fakeRegistry(t)
	digest, err := newTestClient(srv).Digest(context.Background(), imageref.Ref{Registry: "fake.io", Repository: "org/app"}, "1.0")
	if err != nil || digest != "sha256:abc" {
		t.Errorf("Digest = %q, %v", digest, err)
	}
}

func TestMissingImageIsNotFound(t *testing.T) {
	srv, _ := fakeRegistry(t)
	_, err := newTestClient(srv).Digest(context.Background(), imageref.Ref{Registry: "fake.io", Repository: "org/app"}, "missing")
	var se *StatusError
	if !errors.As(err, &se) || !se.NotFound() {
		t.Errorf("err = %v, want not-found StatusError", err)
	}
}

func TestDefaultEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"docker.io": "https://registry-1.docker.io",
		"lscr.io":   "https://ghcr.io",
		"quay.io":   "https://quay.io",
	} {
		if got := DefaultEndpoint(in); got != want {
			t.Errorf("DefaultEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
