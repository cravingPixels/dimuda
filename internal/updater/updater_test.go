package updater

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

var rome = mustLoad("Europe/Rome")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(hhmm string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-16 "+hhmm, rome)
	return t
}

func policy(t *testing.T, ov string) Policy {
	t.Helper()
	set, err := overrides.Parse([]byte(ov))
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWindow("03:00-05:00", rome)
	if err != nil {
		t.Fatal(err)
	}
	return Policy{MinAge: 72 * time.Hour, Window: w, Overrides: set}
}

func image(key, raw string, u scanner.Update) scanner.ImageReport {
	return scanner.ImageReport{
		Key: key, Image: key, Update: &u,
		Usages: []scanner.Usage{{Source: "git:stacks", Stack: "apps", Service: "svc", File: "apps/compose.yaml", Raw: raw}},
	}
}

func TestDecide(t *testing.T) {
	patch := scanner.Update{FromTag: "v1.2.0", ToTag: "v1.2.1", ToDigest: "sha256:b", Kind: versioning.KindPatch}
	minor := scanner.Update{FromTag: "v1.2.0", ToTag: "v1.3.0", ToDigest: "sha256:c", Kind: versioning.KindMinor}
	pin := scanner.Update{FromTag: "v3", ToTag: "v3.41.3", ToDigest: "sha256:d", Kind: versioning.KindPin}
	repush := scanner.Update{FromTag: "1.0.0", FromDigest: "sha256:x", ToTag: "1.0.0", ToDigest: "sha256:y", Kind: versioning.KindDigest}
	security := patch
	security.Security = true

	old := at("04:00").Add(-100 * time.Hour)
	seenLongAgo := map[string]time.Time{Target("acme/app:v1.2.0", patch): old, Target("acme/app:v1.2.0", minor): old}

	cases := []struct {
		name    string
		img     scanner.ImageReport
		ov      string
		seen    map[string]time.Time
		applied map[string]time.Time
		now     time.Time
		want    Action
	}{
		{"aged patch inside window", image("acme/app:v1.2.0", "acme/app:v1.2.0", patch), "", seenLongAgo, nil, at("04:00"), ActionApply},
		{"aged patch outside window", image("acme/app:v1.2.0", "acme/app:v1.2.0", patch), "", seenLongAgo, nil, at("12:00"), ActionWait},
		{"fresh patch waits for age", image("acme/app:v1.2.0", "acme/app:v1.2.0", patch), "", nil, nil, at("04:00"), ActionWait},
		{"security skips age and window", image("acme/app:v1.2.0", "acme/app:v1.2.0", security), "", nil, nil, at("12:00"), ActionApply},
		{"minor held by default", image("acme/app:v1.2.0", "acme/app:v1.2.0", minor), "", seenLongAgo, nil, at("04:00"), ActionHold},
		{"minor allowed by override", image("acme/app:v1.2.0", "acme/app:v1.2.0", minor), "images:\n  acme/app: {autoUpdate: minor}\n", seenLongAgo, nil, at("04:00"), ActionApply},
		{"autoUpdate none holds even a security fix", image("acme/app:v1.2.0", "acme/app:v1.2.0", security), "images:\n  acme/app: {autoUpdate: none}\n", nil, nil, at("04:00"), ActionHold},
		{"pin needs no age, only the window", image("qmcgaw/gluetun:v3", "qmcgaw/gluetun:v3", pin), "", nil, nil, at("04:00"), ActionApply},
		{"re-pushed tag is held", image("acme/old:1.0.0@sha256:x", "acme/old:1.0.0@sha256:x", repush), "", nil, nil, at("04:00"), ActionHold},
		{"reverted target is held", image("acme/app:v1.2.0", "acme/app:v1.2.0", patch), "", seenLongAgo, map[string]time.Time{Target("acme/app:v1.2.0", patch): old}, at("04:00"), ActionHold},
		{"variable image is held", image("acme/app:v1.2.0", "acme/app:${APP_TAG}", patch), "", seenLongAgo, nil, at("04:00"), ActionHold},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := scanner.Report{Images: []scanner.ImageReport{c.img}}
			got := Decide(report, policy(t, c.ov), c.seen, c.applied, c.now)
			if len(got) != 1 || got[0].Action != c.want {
				t.Fatalf("decisions = %+v, want action %s", got, c.want)
			}
			if c.want == ActionHold && got[0].Reason == "" {
				t.Error("a held update must say why")
			}
		})
	}

	t.Run("mounted folder usages are not editable", func(t *testing.T) {
		img := image("acme/app:v1.2.0", "acme/app:v1.2.0", security)
		img.Usages[0].Source = "dockge"
		got := Decide(scanner.Report{Images: []scanner.ImageReport{img}}, policy(t, ""), nil, nil, at("04:00"))
		if got[0].Action != ActionHold || len(got[0].Changes) != 0 {
			t.Errorf("decision = %+v", got[0])
		}
	})

	t.Run("waiting patch reports when it will apply", func(t *testing.T) {
		got := Decide(scanner.Report{Images: []scanner.ImageReport{image("acme/app:v1.2.0", "acme/app:v1.2.0", patch)}}, policy(t, ""), nil, nil, at("12:00"))
		if want := at("03:00").AddDate(0, 0, 4); !got[0].ApplyAfter.Equal(want) {
			t.Errorf("applyAfter = %s, want %s (aged at 12:00 three days later, so the next window opens the morning after)", got[0].ApplyAfter, want)
		}
	})
}

func TestRewrite(t *testing.T) {
	cases := map[string]string{
		"qmcgaw/gluetun:v3":                   "qmcgaw/gluetun:v3.41.3@sha256:d",
		"docker.io/valkey/valkey:9":           "docker.io/valkey/valkey:v3.41.3@sha256:d",
		"registry:5000/team/app:1@sha256:old": "registry:5000/team/app:v3.41.3@sha256:d",
		"registry:5000/team/app":              "registry:5000/team/app:v3.41.3@sha256:d",
	}
	for raw, want := range cases {
		if got := Rewrite(raw, "v3.41.3", "sha256:d"); got != want {
			t.Errorf("Rewrite(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestApply(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "apps", "compose.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	original := `services:
  app:
    # Pinned: newest release.
    image: acme/app:v1  # keep this comment
  other:
    image: "acme/app:v1.2.0"
  flow: {image: acme/app:v1, restart: always}
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	err := Apply(root, Change{File: "apps/compose.yaml", Service: "app", Old: "acme/app:v1", New: "acme/app:v1.2.1@sha256:b"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `services:
  app:
    # Pinned: newest release.
    image: acme/app:v1.2.1@sha256:b  # keep this comment
  other:
    image: "acme/app:v1.2.0"
  flow: {image: acme/app:v1.2.1@sha256:b, restart: always}
`
	if string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the original 0640", info.Mode().Perm())
	}
	if err := Apply(root, Change{File: "apps/compose.yaml", Service: "gone", Old: "acme/gone:1"}); err == nil {
		t.Error("a missing image line must fail instead of committing nothing")
	}
}

func TestWindow(t *testing.T) {
	w, err := ParseWindow("23:00-02:00", rome)
	if err != nil {
		t.Fatal(err)
	}
	for hhmm, want := range map[string]bool{"23:30": true, "01:59": true, "02:00": false, "12:00": false} {
		if got := w.Contains(at(hhmm)); got != want {
			t.Errorf("Contains(%s) = %v, want %v", hhmm, got, want)
		}
	}
	if got, want := w.NextOpen(at("12:00")), at("23:00"); !got.Equal(want) {
		t.Errorf("NextOpen = %s, want %s", got, want)
	}
	if got, want := w.NextStart(at("23:30")), at("23:00").AddDate(0, 0, 1); !got.Equal(want) {
		t.Errorf("NextStart inside the window = %s, want the next day's opening %s", got, want)
	}
	if (Window{}).Contains(at("12:00")) != true {
		t.Error("an unset window is always open")
	}
	for _, bad := range []string{"3-5", "03:00", "05:00-05:00", "25:00-26:00"} {
		if _, err := ParseWindow(bad, rome); err == nil {
			t.Errorf("ParseWindow(%q) should fail", bad)
		}
	}
}
