package komodo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeployChanged(t *testing.T) {
	var got struct {
		path, key, secret string
		body              map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.key, got.secret = r.URL.Path, r.Header.Get("X-Api-Key"), r.Header.Get("X-Api-Secret")
		json.NewDecoder(r.Body).Decode(&got.body)
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	if err := New(srv.Client(), srv.URL+"/", "k", "s", "*").DeployChanged(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.path != "/execute/BatchDeployStackIfChanged" || got.key != "k" || got.secret != "s" || got.body["pattern"] != "*" {
		t.Errorf("request = %+v", got)
	}
}

func TestDeployChangedReportsHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid api key", http.StatusUnauthorized)
	}))
	defer srv.Close()
	err := New(srv.Client(), srv.URL, "k", "bad", "*").DeployChanged(context.Background())
	if err == nil || err.Error() != "komodo: HTTP 401: invalid api key" {
		t.Errorf("err = %v", err)
	}
}

func TestRunProcedureWaitsForCompletion(t *testing.T) {
	pollInterval = time.Millisecond
	var calls []string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, r.URL.Path+" "+body["procedure"]+body["id"])
		switch r.URL.Path {
		case "/execute/RunProcedure":
			w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"Queued","success":false}`))
		case "/read/GetUpdate":
			polls++
			if polls < 2 {
				w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"InProgress","success":false}`))
				return
			}
			w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"Complete","success":true}`))
		}
	}))
	defer srv.Close()

	if err := New(srv.Client(), srv.URL, "k", "s", "*").RunProcedure(context.Background(), "gluetun-redeploy"); err != nil {
		t.Fatal(err)
	}
	want := "/execute/RunProcedure gluetun-redeploy,/read/GetUpdate u1,/read/GetUpdate u1"
	if got := strings.Join(calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
}

func TestRunProcedureReportsFailure(t *testing.T) {
	pollInterval = time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/execute/RunProcedure" {
			w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"InProgress"}`))
			return
		}
		w.Write([]byte(`{"_id":{"$oid":"u1"},"status":"Complete","success":false}`))
	}))
	defer srv.Close()
	err := New(srv.Client(), srv.URL, "k", "s", "*").RunProcedure(context.Background(), "gluetun-redeploy")
	if err == nil || !strings.Contains(err.Error(), "gluetun-redeploy failed") {
		t.Errorf("err = %v", err)
	}
}
