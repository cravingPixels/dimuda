package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/updates" || r.Header.Get("Title") != "t" || r.Header.Get("Tags") != "a,b" ||
			r.Header.Get("Click") != "https://x" || r.Header.Get("Authorization") != "Bearer tok" || string(body) != "hello" {
			t.Errorf("unexpected request %s %v %q", r.URL.Path, r.Header, body)
		}
	}))
	defer srv.Close()
	n := NewNtfy(srv.Client(), srv.URL+"/", "updates", "tok")
	if err := n.Send(context.Background(), Message{Title: "t", Body: "hello", Click: "https://x", Tags: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
}

func TestSendFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if err := NewNtfy(srv.Client(), srv.URL, "x", "").Send(context.Background(), Message{}); err == nil {
		t.Error("expected error")
	}
}
