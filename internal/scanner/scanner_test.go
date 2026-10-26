package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cravingPixels/dimuda/internal/changelog"
	"github.com/cravingPixels/dimuda/internal/docker"
	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/imageref"
	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/registry"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type fakeRegistry struct {
	tags    map[string][]string
	digests map[string]string
}

func (f fakeRegistry) ListTags(_ context.Context, ref imageref.Ref) ([]string, error) {
	tags, ok := f.tags[ref.Name()]
	if !ok {
		return nil, &registry.StatusError{Code: 404, URL: ref.Name()}
	}
	return tags, nil
}

func (f fakeRegistry) Digest(_ context.Context, ref imageref.Ref, tag string) (string, error) {
	d, ok := f.digests[ref.Name()+":"+tag]
	if !ok {
		return "", &registry.StatusError{Code: 404, URL: ref.Name() + ":" + tag}
	}
	return d, nil
}

type fakeReleases map[string][]changelog.Release

func (f fakeReleases) Releases(_ context.Context, repo string) ([]changelog.Release, error) {
	return f[repo], nil
}

type fakeDocker []docker.Container

func (f fakeDocker) Containers(context.Context) ([]docker.Container, error) { return f, nil }

type fakeGit struct{ dir string }

func (f fakeGit) Sync(context.Context, gitsource.Repo) (string, error) { return f.dir, nil }

func writeStack(t *testing.T, root, stack, content string) {
	t.Helper()
	dir := filepath.Join(root, stack)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) *Scanner {
	t.Helper()
	host := t.TempDir()
	writeStack(t, host, "immich", `
services:
  redis: {image: "redis:8-alpine"}
  server: {image: "ghcr.io/immich-app/immich-server:v2.1.0"}
`)
	writeStack(t, host, "linkwarden", `
services:
  cache: {image: "redis:8-alpine"}
  mongo: {image: "komodo-mongo:latest", build: .}
  broken: {image: "${UNSET}"}
`)
	writeStack(t, host, "arr", `
services:
  sonarr: {image: "lscr.io/linuxserver/sonarr:4.0.15-ls290"}
  prowlarr: {image: "lscr.io/linuxserver/prowlarr:2.6.5.5623-ls165"}
  gone: {image: "someone/deleted:1.0.0"}
  firefox: {image: "lscr.io/linuxserver/firefox:latest"}
`)
	gitDir := t.TempDir()
	writeStack(t, gitDir, "dozzle", `
services:
  dozzle: {image: "amir20/dozzle:latest"}
`)

	ov, err := overrides.Parse([]byte(`
images:
  lscr.io/linuxserver/sonarr: {changelog: Sonarr/Sonarr}
  lscr.io/linuxserver/prowlarr: {changelog: Prowlarr/Prowlarr}
  lscr.io/linuxserver/firefox: {ignore: true, note: no versions}
  redis: {changelog: redis/redis}
`))
	if err != nil {
		t.Fatal(err)
	}

	reg := fakeRegistry{
		tags: map[string][]string{
			"docker.io/library/redis":          {"8-alpine", "8.10.2-alpine", "8.8.3-alpine", "8.10-alpine", "8.10.2", "latest"},
			"ghcr.io/immich-app/immich-server": {"v2.0.0", "v2.1.0", "v2.2.1", "v3.0.0-rc1", "release"},
			"lscr.io/linuxserver/sonarr":       {"4.0.15-ls290", "4.0.16-ls291", "4.0.16-ls292", "amd64-4.0.17-ls293", "latest"},
			"lscr.io/linuxserver/prowlarr":     {"2.6.5.5623-ls165", "2.6.5.5623-ls166"},
			"docker.io/amir20/dozzle":          {"v11.3.4", "v11.3.5", "v11", "v11.3", "latest", "pr-5522"},
		},
		digests: map[string]string{
			"docker.io/library/redis:8-alpine":              "sha256:redis-new",
			"docker.io/library/redis:8.10.2-alpine":         "sha256:redis-new",
			"docker.io/library/redis:8.8.3-alpine":          "sha256:redis-old",
			"ghcr.io/immich-app/immich-server:v2.1.0":       "sha256:immich",
			"lscr.io/linuxserver/sonarr:4.0.15-ls290":       "sha256:sonarr",
			"lscr.io/linuxserver/prowlarr:2.6.5.5623-ls165": "sha256:prowlarr",
			"docker.io/amir20/dozzle:latest":                "sha256:dozzle-1135",
			"docker.io/amir20/dozzle:v11.3.5":               "sha256:dozzle-1135",
			"docker.io/amir20/dozzle:v11.3.4":               "sha256:dozzle-1134",
		},
	}

	containers := fakeDocker{
		{Name: "immich_redis", Image: "redis:8-alpine", State: "running", Project: "immich", Version: "8.8.3", RepoDigests: []string{"redis@sha256:redis-old"}},
		{Name: "linkwarden-cache", Image: "redis:8-alpine", State: "running", Project: "linkwarden", Version: "8.10.2", RepoDigests: []string{"redis@sha256:redis-new"}},
		{Name: "dozzle", Image: "amir20/dozzle:latest", State: "running", Project: "dozzle", Version: "v11.3.4", SourceURL: "https://github.com/amir20/dozzle", RepoDigests: []string{"amir20/dozzle@sha256:dozzle-1134"}},
	}

	releases := fakeReleases{
		"Sonarr/Sonarr":     {{Tag: "v4.0.17"}, {Tag: "v4.0.16", Name: "4.0.16"}, {Tag: "v4.0.15"}},
		"Prowlarr/Prowlarr": {{Tag: "v2.7.0.6000"}, {Tag: "v2.6.5.5623"}},
		"amir20/dozzle":     {{Tag: "v11.3.5", Name: "fixes"}, {Tag: "v11.3.4"}},
		"redis/redis":       {{Tag: "9.0.0"}, {Tag: "8.10.2"}, {Tag: "8.8.3"}},
	}

	return &Scanner{
		Dirs:      []Dir{{Name: "dockge", Path: host}},
		GitRepos:  []gitsource.Repo{{Name: "stacks", URL: "unused"}},
		Git:       fakeGit{dir: gitDir},
		Docker:    containers,
		Registry:  reg,
		Releases:  releases,
		Overrides: ov,
	}
}

func byImage(t *testing.T, report Report, key string) ImageReport {
	t.Helper()
	for _, img := range report.Images {
		if img.Key == key {
			return img
		}
	}
	t.Fatalf("no report for %q; have %d images", key, len(report.Images))
	return ImageReport{}
}

func TestScan(t *testing.T) {
	report := fixture(t).Scan(context.Background())
	if len(report.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", report.Errors)
	}

	t.Run("lsio upstream patch with changelog and unpackaged release", func(t *testing.T) {
		r := byImage(t, report, "lscr.io/linuxserver/sonarr:4.0.15-ls290")
		if r.Kind != versioning.KindPatch || r.Latest != "4.0.16-ls292" {
			t.Errorf("kind=%s latest=%s", r.Kind, r.Latest)
		}
		if len(r.Releases) != 1 || r.Releases[0].Tag != "v4.0.16" {
			t.Errorf("releases = %+v", r.Releases)
		}
		if !r.Unpackaged || r.Upstream != "v4.0.17" {
			t.Errorf("upstream=%s unpackaged=%v", r.Upstream, r.Unpackaged)
		}
	})

	t.Run("lsio rebuild only", func(t *testing.T) {
		r := byImage(t, report, "lscr.io/linuxserver/prowlarr:2.6.5.5623-ls165")
		if r.Kind != versioning.KindRebuild || len(r.Releases) != 0 || !r.Unpackaged {
			t.Errorf("kind=%s releases=%d unpackaged=%v", r.Kind, len(r.Releases), r.Unpackaged)
		}
	})

	t.Run("shared partial tag shows every stack and which container is behind", func(t *testing.T) {
		r := byImage(t, report, "redis:8-alpine")
		if got := r.Stacks(); len(got) != 2 || got[0] != "immich" || got[1] != "linkwarden" {
			t.Errorf("stacks = %v", got)
		}
		if !r.Floating || r.ResolvesTo != "8.10.2-alpine" || r.PinTo != "8.10.2-alpine" {
			t.Errorf("floating=%v resolvesTo=%s pinTo=%s", r.Floating, r.ResolvesTo, r.PinTo)
		}
		if r.Kind != versioning.KindMinor {
			t.Errorf("kind = %s, want minor (8.8.3 running, tag now 8.10.2)", r.Kind)
		}
		outdated := map[string]bool{}
		for _, run := range r.Running {
			outdated[run.Container] = run.Outdated
		}
		if !outdated["immich_redis"] || outdated["linkwarden-cache"] {
			t.Errorf("outdated = %v", outdated)
		}
	})

	t.Run("floating latest resolved by digest with changelog from source label", func(t *testing.T) {
		r := byImage(t, report, "amir20/dozzle:latest")
		if r.ResolvesTo != "v11.3.5" || r.Kind != versioning.KindPatch {
			t.Errorf("resolvesTo=%s kind=%s", r.ResolvesTo, r.Kind)
		}
		if r.ChangelogRepo != "amir20/dozzle" || len(r.Releases) != 1 || r.Releases[0].Name != "fixes" {
			t.Errorf("changelog=%s releases=%+v", r.ChangelogRepo, r.Releases)
		}
		if r.Usages[0].Source != "git:stacks" {
			t.Errorf("source = %s", r.Usages[0].Source)
		}
	})

	t.Run("up to date pinned tag", func(t *testing.T) {
		r := byImage(t, report, "ghcr.io/immich-app/immich-server:v2.1.0")
		if r.Kind != versioning.KindMinor || r.Latest != "v2.2.1" {
			t.Errorf("kind=%s latest=%s", r.Kind, r.Latest)
		}
	})

	t.Run("local build, ignored, missing and invalid", func(t *testing.T) {
		if r := byImage(t, report, "komodo-mongo:latest"); !r.LocalBuild || r.Err != "" {
			t.Errorf("mongo = %+v", r)
		}
		if r := byImage(t, report, "lscr.io/linuxserver/firefox:latest"); !r.Ignored || r.Note != "no versions" || r.Kind != versioning.KindNone {
			t.Errorf("firefox = %+v", r)
		}
		if r := byImage(t, report, "someone/deleted:1.0.0"); r.Kind != versioning.KindUnknown || r.Err == "" {
			t.Errorf("deleted = %+v", r)
		}
		found := false
		for _, img := range report.Images {
			if img.Err != "" && len(img.Usages) == 1 && img.Usages[0].Service == "broken" {
				found = true
			}
		}
		if !found {
			t.Error("unresolved image variable should be reported")
		}
	})

	t.Run("partial tag only compares upstream releases in its line", func(t *testing.T) {
		r := byImage(t, report, "redis:8-alpine")
		if r.Upstream != "8.10.2" || r.Unpackaged {
			t.Errorf("upstream=%s unpackaged=%v; redis 9.0.0 must not count for the 8 line", r.Upstream, r.Unpackaged)
		}
	})

	t.Run("upstream updates sort first", func(t *testing.T) {
		if k := report.Images[0].Kind; !k.IsUpstream() {
			t.Errorf("first image kind = %s", k)
		}
	})

	if len(report.Stacks) != 4 {
		t.Errorf("stacks = %v", report.Stacks)
	}
}

func TestScanReportsSourceErrors(t *testing.T) {
	s := &Scanner{Dirs: []Dir{{Name: "missing", Path: filepath.Join(t.TempDir(), "nope")}}, Registry: fakeRegistry{}}
	report := s.Scan(context.Background())
	if len(report.Errors) != 1 || len(report.Images) != 0 {
		t.Errorf("report = %+v", report)
	}
}
