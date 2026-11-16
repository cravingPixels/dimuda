package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/updater"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type fakeGit struct {
	dir      string
	commits  []string
	pushErr  error
	pushes   int
	discards int
}

func (f *fakeGit) Dir(gitsource.Repo) string { return f.dir }
func (f *fakeGit) Commit(_ context.Context, _ gitsource.Repo, files []string, msg string, _ gitsource.Author) error {
	f.commits = append(f.commits, strings.Join(files, ",")+" | "+msg)
	return nil
}
func (f *fakeGit) Push(context.Context, gitsource.Repo) error { f.pushes++; return f.pushErr }
func (f *fakeGit) Discard(context.Context, gitsource.Repo) error {
	f.discards++
	return nil
}

type fakeDeployer struct {
	calls int
	log   []string
}

func (f *fakeDeployer) DeployChanged(context.Context) error {
	f.calls++
	f.log = append(f.log, "batch")
	return nil
}

func (f *fakeDeployer) RunProcedure(_ context.Context, name string) error {
	f.log = append(f.log, "procedure "+name)
	return nil
}

const original = "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3.41.3@sha256:old  # vpn\n"

func gluetun(u scanner.Update) scanner.ImageReport {
	return scanner.ImageReport{
		Key: "qmcgaw/gluetun:v3.41.3@sha256:old", Image: "qmcgaw/gluetun", Tag: "v3.41.3", Kind: u.Kind, Update: &u,
		Usages: []scanner.Usage{{Source: "git:stacks", Stack: "gluetun", Service: "gluetun", File: "gluetun/compose.yaml", Raw: "qmcgaw/gluetun:v3.41.3@sha256:old", Procedure: "gluetun-redeploy"}},
	}
}

func autoFixture(t *testing.T) (*App, *fakeScanner, *fakeNotifier, *fakeGit, *fakeDeployer, string) {
	t.Helper()
	checkout := t.TempDir()
	path := filepath.Join(checkout, "gluetun", "compose.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(original), 0o644)

	s := &fakeScanner{report: reportWith()}
	n := &fakeNotifier{}
	a, err := New(s, n, t.TempDir(), "https://dimuda.example.com", quiet)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := overrides.Parse([]byte("images: {}\n"))
	g := &fakeGit{dir: checkout}
	d := &fakeDeployer{}
	a.EnableAutoUpdate(AutoUpdate{
		Policy: updater.Policy{MinAge: 72 * time.Hour, Overrides: set},
		Git:    g, Repos: []gitsource.Repo{{Name: "stacks"}}, Deployer: d,
		Author: gitsource.Author{Name: "dimuda", Email: "dimuda@localhost"},
		Now:    func() time.Time { return time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC) },
	})
	a.RunScan(context.Background()) // first scan of a new install: nothing pending yet
	return a, s, n, g, d, path
}

func TestAutoUpdateAppliesSecurityPatch(t *testing.T) {
	a, s, n, g, d, path := autoFixture(t)
	patch := scanner.Update{FromTag: "v3.41.3", FromDigest: "sha256:old", ToTag: "v3.41.4", ToDigest: "sha256:new", Kind: versioning.KindPatch, Security: true}
	s.report = reportWith(gluetun(patch))
	a.RunScan(context.Background())

	got, _ := os.ReadFile(path)
	if want := "services:\n  gluetun:\n    image: qmcgaw/gluetun:v3.41.4@sha256:new  # vpn\n"; string(got) != want {
		t.Errorf("compose =\n%s", got)
	}
	if len(g.commits) != 1 || g.commits[0] != "gluetun/compose.yaml | gluetun: bump qmcgaw/gluetun v3.41.3 → v3.41.4 (patch, security)" {
		t.Errorf("commits = %v", g.commits)
	}
	if g.pushes != 1 || d.calls != 1 {
		t.Errorf("pushes=%d deploys=%d", g.pushes, d.calls)
	}
	if got := strings.Join(d.log, ","); got != "procedure gluetun-redeploy,batch" {
		t.Errorf("deploy order = %s, want the gluetun procedure to finish before the batch deploy", got)
	}
	if len(n.sent) != 1 || n.sent[0].Title != "qmcgaw/gluetun updated to v3.41.4" {
		t.Errorf("notifications = %+v", n.sent)
	}

	// The user reverts the bump in git: the next scan offers the same target again.
	os.WriteFile(path, []byte(original), 0o644)
	a.RunScan(context.Background())
	if len(g.commits) != 1 {
		t.Fatalf("a reverted update must not be re-applied, commits = %v", g.commits)
	}
	if d := a.Decisions()[gluetun(patch).Key]; d.Action != updater.ActionHold || !strings.Contains(d.Reason, "reverted") {
		t.Errorf("decision after revert = %+v", d)
	}
	if len(n.sent) != 2 || n.sent[1].Priority != priorityReview || !strings.Contains(n.sent[1].Body, "reverted") {
		t.Errorf("review notification = %+v", n.sent)
	}
	a.RunScan(context.Background())
	if len(n.sent) != 2 {
		t.Errorf("the same hold must notify once, sent %d", len(n.sent))
	}
}

func TestAutoUpdatePushFailureIsReportedOnceAndRetried(t *testing.T) {
	a, s, n, g, d, path := autoFixture(t)
	g.pushErr = errors.New("rejected")
	// The scanner's git sync resets the checkout to upstream, dropping the unpushed edit.
	sync := func() { os.WriteFile(path, []byte(original), 0o644) }
	patch := scanner.Update{FromTag: "v3.41.3", ToTag: "v3.41.4", ToDigest: "sha256:new", Kind: versioning.KindPatch, Security: true}
	s.report = reportWith(gluetun(patch))

	a.RunScan(context.Background())
	if d.calls != 0 {
		t.Error("nothing was pushed, so Komodo must not be asked to deploy")
	}
	if dec := a.Decisions()[gluetun(patch).Key]; dec.Action != updater.ActionWait {
		t.Errorf("decision = %+v, want wait for the retry", dec)
	}
	if len(n.sent) != 1 || !strings.Contains(n.sent[0].Body, "push to stacks failed") {
		t.Fatalf("notifications = %+v", n.sent)
	}

	sync()
	a.RunScan(context.Background())
	if g.pushes != 2 || len(n.sent) != 1 {
		t.Errorf("retry must push again without repeating the alert: pushes=%d sent=%d", g.pushes, len(n.sent))
	}

	g.pushErr = nil
	sync()
	a.RunScan(context.Background())
	if d.calls != 1 || len(n.sent) != 2 || !strings.Contains(n.sent[1].Title, "updated") {
		t.Errorf("after recovery deploys=%d sent=%+v", d.calls, n.sent)
	}
}

func TestAutoUpdateHoldsMinorAndNotifiesMajor(t *testing.T) {
	a, s, n, g, _, _ := autoFixture(t)
	minor := scanner.Update{FromTag: "v3.41.3", ToTag: "v3.42.0", ToDigest: "sha256:m", Kind: versioning.KindMinor}
	img := gluetun(minor)
	img.Kind, img.Latest = versioning.KindMajor, "v4.0.0"
	s.report = reportWith(img)
	a.RunScan(context.Background())

	if len(g.commits) != 0 {
		t.Errorf("minor must be held without review, commits = %v", g.commits)
	}
	if len(n.sent) != 1 || n.sent[0].Title != "2 updates need review" {
		t.Fatalf("notifications = %+v", n.sent)
	}
	if body := n.sent[0].Body; !strings.Contains(body, "v3.42.0 (minor)") || !strings.Contains(body, "v4.0.0 (major)") {
		t.Errorf("body = %s", body)
	}
}

func TestAutoUpdateGroupsPinsPerStackAndEditsSharedLinesOnce(t *testing.T) {
	a, s, _, g, _, _ := autoFixture(t)
	path := filepath.Join(g.dir, "rustdesk", "compose.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("name: ix-rustdesk\nservices:\n  hbbs:\n    image: rustdesk/rustdesk-server:1\n  hbbr:\n    image: rustdesk/rustdesk-server:1\n  proxy:\n    image: tecnativa/docker-socket-proxy:v0.5.0\n"), 0o644)

	pin := func(key, raw, to, digest string, services ...string) scanner.ImageReport {
		u := scanner.Update{FromTag: strings.SplitN(raw, ":", 2)[1], ToTag: to, ToDigest: digest, Kind: versioning.KindPin}
		img := scanner.ImageReport{Key: key, Image: strings.SplitN(raw, ":", 2)[0], Update: &u}
		for _, svc := range services {
			img.Usages = append(img.Usages, scanner.Usage{Source: "git:stacks", Stack: "ix-rustdesk", Service: svc, File: "rustdesk/compose.yaml", Raw: raw})
		}
		return img
	}
	s.report = reportWith(
		pin("rustdesk/rustdesk-server:1", "rustdesk/rustdesk-server:1", "1.1.16", "sha256:rd", "hbbs", "hbbr"),
		pin("tecnativa/docker-socket-proxy:v0.5.0", "tecnativa/docker-socket-proxy:v0.5.0", "v0.5.0", "sha256:1f5038b54f06aaaa", "proxy"),
	)
	a.RunScan(context.Background())

	got, _ := os.ReadFile(path)
	if strings.Count(string(got), "rustdesk/rustdesk-server:1.1.16@sha256:rd") != 2 || !strings.Contains(string(got), "docker-socket-proxy:v0.5.0@sha256:1f5038b54f06aaaa") {
		t.Errorf("compose =\n%s", got)
	}
	want := "rustdesk/compose.yaml | rustdesk: pin 2 images to exact tags and digests\n\n" +
		"- rustdesk/rustdesk-server 1 → 1.1.16\n- tecnativa/docker-socket-proxy v0.5.0 → v0.5.0@1f5038b54f06"
	if len(g.commits) != 1 || g.commits[0] != want {
		t.Errorf("commits = %q\nwant one commit named after the folder:\n%q", g.commits, want)
	}
	for key, d := range a.Decisions() {
		if d.Action != updater.ActionApply {
			t.Errorf("%s: %+v", key, d)
		}
	}
}
