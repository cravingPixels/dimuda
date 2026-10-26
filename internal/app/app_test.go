package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/cravingPixels/dimuda/internal/notify"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type fakeScanner struct{ report scanner.Report }

func (f *fakeScanner) Scan(context.Context) scanner.Report { return f.report }

type fakeNotifier struct {
	sent []notify.Message
	err  error
}

func (f *fakeNotifier) Send(_ context.Context, m notify.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func reportWith(images ...scanner.ImageReport) scanner.Report {
	return scanner.Report{Images: images}
}

func sonarr(latest string) scanner.ImageReport {
	return scanner.ImageReport{
		Key: "lscr.io/linuxserver/sonarr:4.0.15-ls290", Image: "lscr.io/linuxserver/sonarr", Tag: "4.0.15-ls290",
		Latest: latest, Kind: versioning.KindPatch, Usages: []scanner.Usage{{Stack: "arr"}},
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestFirstScanOnNewInstallDoesNotNotify(t *testing.T) {
	dir := t.TempDir()
	s := &fakeScanner{report: reportWith(sonarr("4.0.16-ls292"))}
	n := &fakeNotifier{}
	a, err := New(s, n, dir, "https://updates.example.com", quiet)
	if err != nil {
		t.Fatal(err)
	}
	a.RunScan(context.Background())
	if len(n.sent) != 0 {
		t.Fatalf("first scan sent %d notifications", len(n.sent))
	}

	a.RunScan(context.Background())
	if len(n.sent) != 0 {
		t.Fatal("same pending update must not notify again")
	}

	s.report = reportWith(sonarr("4.0.17-ls293"), scanner.ImageReport{Key: "x:1", Kind: versioning.KindRebuild})
	a.RunScan(context.Background())
	if len(n.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(n.sent))
	}
	m := n.sent[0]
	if !strings.Contains(m.Title, "4.0.17-ls293") || !strings.Contains(m.Body, "4.0.15-ls290 → 4.0.17-ls293 (patch) · arr") || m.Click != "https://updates.example.com" {
		t.Errorf("message = %+v", m)
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s := &fakeScanner{report: reportWith(sonarr("4.0.16-ls292"))}
	n := &fakeNotifier{}
	a, _ := New(s, n, dir, "", quiet)
	a.RunScan(context.Background())

	restarted, err := New(s, n, dir, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := restarted.Report(); !ok || len(r.Images) != 1 {
		t.Fatalf("report not restored: %+v %v", r, ok)
	}
	s.report = reportWith(sonarr("4.0.17-ls293"))
	restarted.RunScan(context.Background())
	if len(n.sent) != 1 {
		t.Errorf("after restart sent %d, want 1", len(n.sent))
	}
}

func TestFailedNotificationIsRetried(t *testing.T) {
	dir := t.TempDir()
	s := &fakeScanner{report: reportWith()}
	n := &fakeNotifier{}
	a, _ := New(s, n, dir, "", quiet)
	a.RunScan(context.Background())

	s.report = reportWith(sonarr("4.0.16-ls292"))
	n.err = errors.New("down")
	a.RunScan(context.Background())
	n.err = nil
	a.RunScan(context.Background())
	if len(n.sent) != 1 {
		t.Errorf("sent %d, want 1 after retry", len(n.sent))
	}
}

func TestNoReportBeforeFirstScan(t *testing.T) {
	a, _ := New(&fakeScanner{}, nil, t.TempDir(), "", quiet)
	if _, ok := a.Report(); ok {
		t.Error("expected no report")
	}
	if !a.RunScan(context.Background()) {
		t.Error("scan should run")
	}
}
