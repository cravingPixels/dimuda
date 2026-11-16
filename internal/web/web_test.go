package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravingPixels/dimuda/internal/changelog"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/updater"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type fakeSource struct {
	report    scanner.Report
	ready     bool
	scans     atomic.Int32
	decisions map[string]updater.Decision
}

func (f *fakeSource) Report() (scanner.Report, bool) { return f.report, f.ready }
func (f *fakeSource) Scanning() bool                 { return false }
func (f *fakeSource) RunScan(context.Context) bool   { f.scans.Add(1); return true }
func (f *fakeSource) Decisions() map[string]updater.Decision {
	return f.decisions
}

func newServer(t *testing.T, src *fakeSource) http.Handler {
	t.Helper()
	s, err := New(context.Background(), src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func sample() scanner.Report {
	return scanner.Report{
		GeneratedAt: time.Now().Add(-2 * time.Hour),
		Duration:    "3s",
		Stacks:      []string{"arr", "immich"},
		Images: []scanner.ImageReport{
			{
				Key: "lscr.io/linuxserver/sonarr:4.0.15-ls290", Image: "lscr.io/linuxserver/sonarr", Tag: "4.0.15-ls290",
				Latest: "4.0.16-ls292", Kind: versioning.KindPatch, ChangelogRepo: "Sonarr/Sonarr",
				Upstream: "v4.0.17", UpstreamURL: "https://github.com/Sonarr/Sonarr/releases/tag/v4.0.17", Unpackaged: true,
				Usages:   []scanner.Usage{{Source: "dockge", Stack: "arr", Service: "sonarr", File: "arr/compose.yaml"}},
				Releases: []changelog.Release{{Tag: "v4.0.16", Body: "<script>alert(1)</script> fixed things"}},
			},
			{
				Key: "redis:8-alpine", Image: "redis", Tag: "8-alpine", Floating: true, ResolvesTo: "8.10.2-alpine", PinTo: "8.10.2-alpine",
				Kind:    versioning.KindDigest,
				Usages:  []scanner.Usage{{Stack: "immich", Service: "redis"}, {Stack: "arr", Service: "cache"}},
				Running: []scanner.Running{{Container: "immich_redis", State: "running", Version: "8.8.3", Outdated: true}},
			},
			{Key: "x", Image: "someone/gone", Tag: "1", Kind: versioning.KindUnknown, Err: "image not found on registry"},
		},
	}
}

func TestIndexRendersReport(t *testing.T) {
	h := newServer(t, &fakeSource{report: sample(), ready: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{
		"Last scan 2 hours ago",
		"4.0.16-ls292",
		"upstream <a href=\"https://github.com/Sonarr/Sonarr/releases/tag/v4.0.17\"",
		"not packaged yet",
		`data-stacks="|arr|immich|"`,
		"8.10.2-alpine",
		"behind",
		"image not found on registry",
		"&lt;script&gt;alert(1)&lt;/script&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("release notes must be escaped")
	}
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("ready and idle page must not auto-refresh")
	}
}

func TestIndexBeforeFirstScan(t *testing.T) {
	h := newServer(t, &fakeSource{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "First scan running") || !strings.Contains(rec.Body.String(), `http-equiv="refresh"`) {
		t.Error("expected first-scan placeholder with refresh")
	}
}

func TestScanTriggersAndRedirects(t *testing.T) {
	src := &fakeSource{ready: true}
	h := newServer(t, src)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/scan", nil))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d", rec.Code)
	}
	deadline := time.Now().Add(time.Second)
	for src.scans.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if src.scans.Load() != 1 {
		t.Error("scan not triggered")
	}
}

func TestAPIReport(t *testing.T) {
	h := newServer(t, &fakeSource{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before first scan status %d", rec.Code)
	}

	h = newServer(t, &fakeSource{report: sample(), ready: true})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))
	var got scanner.Report
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || len(got.Images) != 3 {
		t.Errorf("decode %v, images %d", err, len(got.Images))
	}
}

func TestSummarize(t *testing.T) {
	s := summarize(sample())
	if s.Upstream != 1 || s.Digest != 1 || s.Errors != 1 || s.Unpackaged != 1 || s.Current != 0 {
		t.Errorf("summary = %+v", s)
	}
}

func TestIndexShowsAutoUpdateDecisions(t *testing.T) {
	src := &fakeSource{ready: true, report: scanner.Report{Images: []scanner.ImageReport{
		{Key: "traefik:v3.7.19", Image: "traefik", Tag: "v3.7.19", Kind: versioning.KindMinor},
		{Key: "redis:8.10.2-alpine", Image: "redis", Tag: "8.10.2-alpine", Kind: versioning.KindPatch},
	}}}
	src.decisions = map[string]updater.Decision{
		"traefik:v3.7.19": {Key: "traefik:v3.7.19", Action: updater.ActionHold, Reason: "minor update needs review (autoUpdate: patch)",
			Update: scanner.Update{ToTag: "v3.8.0", Kind: versioning.KindMinor}},
		"redis:8.10.2-alpine": {Key: "redis:8.10.2-alpine", Action: updater.ActionWait, Reason: "waiting for the release to age",
			ApplyAfter: time.Date(2026, 11, 19, 3, 0, 0, 0, time.Local), Update: scanner.Update{ToTag: "8.10.3-alpine", Kind: versioning.KindPatch, Security: false}},
	}
	rec := httptest.NewRecorder()
	newServer(t, src).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"needs review: minor update needs review (autoUpdate: patch)",
		`<span class="tag">v3.8.0</span>`,
		"auto from Thu 19 Nov 03:00",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}
