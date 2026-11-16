package tokencheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func server(t *testing.T, status int, expiry string) *GitHub {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rate_limit" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		if expiry != "" {
			w.Header().Set(expiryHeader, expiry)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return NewGitHub(srv.Client()).WithBaseURL(srv.URL)
}

func TestExpiry(t *testing.T) {
	got, ok, err := server(t, 200, "2027-11-16 11:00:00 +0100").Expiry(context.Background(), "tok")
	if err != nil || !ok || !got.Equal(time.Date(2027, 11, 16, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("expiry = %s %v %v", got, ok, err)
	}
	got, ok, err = server(t, 200, "2027-11-16 10:00:00 UTC").Expiry(context.Background(), "tok")
	if err != nil || !ok || got.Hour() != 10 {
		t.Errorf("UTC form = %s %v %v", got, ok, err)
	}
	if _, ok, err := server(t, 200, "").Expiry(context.Background(), "tok"); ok || err != nil {
		t.Errorf("a token without expiry must report ok=false, err=nil: %v %v", ok, err)
	}
	if _, _, err := server(t, 401, "").Expiry(context.Background(), "tok"); !errors.Is(err, ErrRejected) {
		t.Errorf("401 = %v, want ErrRejected", err)
	}
}
