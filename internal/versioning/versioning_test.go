package versioning

import (
	"reflect"
	"testing"
)

func mustParse(t *testing.T, p Parser, raw string) Tag {
	t.Helper()
	tag, ok := p.Parse(raw)
	if !ok {
		t.Fatalf("Parse(%q) failed", raw)
	}
	return tag
}

func TestParseDefault(t *testing.T) {
	p := Parser{}
	cases := []struct {
		raw  string
		want Tag
	}{
		{"v2.34.0", Tag{Prefix: "v", Version: []int{2, 34, 0}}},
		{"8-alpine", Tag{Version: []int{8}, Suffix: "-alpine"}},
		{"8.10.2-alpine3.23", Tag{Version: []int{8, 10, 2}, Suffix: "-alpine3.23"}},
		{"4.0.15-ls290", Tag{Version: []int{4, 0, 15}, Build: "290"}},
		{"v2.1.7-ls234", Tag{Prefix: "v", Version: []int{2, 1, 7}, Build: "234"}},
		{"3.1.6.5079-ls46", Tag{Version: []int{3, 1, 6, 5079}, Build: "46"}},
		{"php8-24.0.3-ls80", Tag{Prefix: "php8-", Version: []int{24, 0, 3}, Build: "80"}},
		{"release-4.0.20.3014", Tag{Prefix: "release-", Version: []int{4, 0, 20, 3014}}},
		{"nightly-1.0.0.2278-ls63", Tag{Prefix: "nightly-", Version: []int{1, 0, 0, 2278}, Build: "63"}},
		{"2026.10.0", Tag{Version: []int{2026, 10, 0}}},
		{"2026.10.25-54ca1d9cf", Tag{Version: []int{2026, 10, 25}, Suffix: hashPlaceholder}},
		{"2026-07-14c", Tag{Version: []int{2026, 7, 14}, Build: "c"}},
		{"2026-07-14", Tag{Version: []int{2026, 7, 14}}},
		{"v3-rc-20261026", Tag{Prefix: "v", Version: []int{3}, Suffix: "-rc-20261026"}},
	}
	for _, c := range cases {
		got := mustParse(t, p, c.raw)
		c.want.Raw = c.raw
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestParseRejectsFloatingAndHashes(t *testing.T) {
	for _, raw := range []string{"latest", "stable", "main", "edge", "sha-1cba1f0", "commit-2995fd3", "git-7f3daa1"} {
		if tag, ok := (Parser{}).Parse(raw); ok {
			t.Errorf("Parse(%q) = %+v, want no match", raw, tag)
		}
	}
}

func TestCustomParser(t *testing.T) {
	p, err := NewParser(`^(?P<version>\d+\.\d+\.\d+)-ubuntu(?P<build>\d+)$`)
	if err != nil {
		t.Fatal(err)
	}
	got := mustParse(t, p, "1.2.3-ubuntu7")
	if !reflect.DeepEqual(got.Version, []int{1, 2, 3}) || got.Build != "7" {
		t.Errorf("got %+v", got)
	}
	if _, ok := p.Parse("1.2.3"); ok {
		t.Error("custom parser must only accept its own pattern")
	}
	if _, err := NewParser(`^(\d+)$`); err == nil {
		t.Error("expected error for regex without version group")
	}
	if _, err := NewParser(`(`); err == nil {
		t.Error("expected error for invalid regex")
	}
}

func TestLatestAndClassify(t *testing.T) {
	p := Parser{}
	cases := []struct {
		name    string
		current string
		tags    []string
		latest  string
		kind    Kind
	}{
		{"semver patch", "v2.33.1", []string{"v2.33.1", "v2.34.0", "v2.34", "v2", "v2.34.0-amd64"}, "v2.34.0", KindMinor},
		{"major", "1.2.3", []string{"1.2.3", "2.0.0", "2.0.0-rc1"}, "2.0.0", KindMajor},
		{"patch", "1.2.3", []string{"1.2.4", "1.2.10"}, "1.2.10", KindPatch},
		{"up to date", "1.2.3", []string{"1.2.3", "1.2.2"}, "1.2.3", KindNone},
		{"partial major line", "8-alpine", []string{"8-alpine", "9-alpine", "9.0.1-alpine", "9"}, "9-alpine", KindMajor},
		{"lsio upstream release", "4.0.15-ls290", []string{"4.0.15-ls291", "4.0.16-ls292", "4.0.16", "amd64-4.0.16-ls292"}, "4.0.16-ls292", KindPatch},
		{"lsio rebuild only", "4.0.15-ls290", []string{"4.0.15-ls290", "4.0.15-ls291"}, "4.0.15-ls291", KindRebuild},
		{"hotio", "release-4.0.15.2941", []string{"release-4.0.20.3014", "nightly-4.0.21.1", "release-4.0.20"}, "release-4.0.20.3014", KindPatch},
		{"calver with hash", "2026.10.19-81371861f", []string{"2026.10.25-54ca1d9cf", "2026.10.19-d47cb96bf"}, "2026.10.25-54ca1d9cf", KindPatch},
		{"date with letter", "2026-07-14a", []string{"2026-07-14c", "2025-05-14b"}, "2026-07-14c", KindRebuild},
		{"prerelease ignored", "2.45.4", []string{"2.46.0-rc.1", "2.45.4"}, "2.45.4", KindNone},
	}
	for _, c := range cases {
		cur := mustParse(t, p, c.current)
		latest := Latest(cur, p.ParseAll(c.tags))
		if latest.Raw != c.latest {
			t.Errorf("%s: Latest = %q, want %q", c.name, latest.Raw, c.latest)
		}
		if kind := Classify(cur, latest); kind != c.kind {
			t.Errorf("%s: Classify = %q, want %q", c.name, kind, c.kind)
		}
	}
}

func TestMostSpecific(t *testing.T) {
	p := Parser{}
	tags := p.ParseAll([]string{"v3", "v3.7", "v3.7.16", "v3.7.9", "v2.11.60", "3.7.16"})
	got, ok := MostSpecific(mustParse(t, p, "v3"), tags)
	if !ok || got.Raw != "v3.7.16" {
		t.Errorf("MostSpecific(v3) = %q, %v", got.Raw, ok)
	}
	tags = p.ParseAll([]string{"8-alpine", "8.10.2-alpine", "8.10-alpine", "8.10.2", "9.0.0-alpine"})
	got, ok = MostSpecific(mustParse(t, p, "8-alpine"), tags)
	if !ok || got.Raw != "8.10.2-alpine" {
		t.Errorf("MostSpecific(8-alpine) = %q, %v", got.Raw, ok)
	}
	if _, ok := MostSpecific(mustParse(t, p, "1.2.3"), tags); ok {
		t.Error("a full version has nothing more specific")
	}
}

func TestReleaseCandidates(t *testing.T) {
	p := Parser{}
	tags := p.ParseAll([]string{"v3", "v3.7.16", "v3.7", "v2.11.60", "nightly-4.0.0", "v3.8.0-rc1", "amd64-3.9.0", "2026.10.25-54ca1d9cf"})
	var got []string
	for _, t := range ReleaseCandidates(tags) {
		got = append(got, t.Raw)
	}
	want := []string{"v3.7.16", "v2.11.60", "2026.10.25-54ca1d9cf", "v3.7", "v3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReleaseCandidates = %v, want %v", got, want)
	}
}

func TestReleaseCandidatesPreferDominantFamily(t *testing.T) {
	p := Parser{}
	tags := p.ParseAll([]string{"v5.7.2-ls1", "2021.12.16", "0.6.26-ls405", "0.6.27-ls406", "0.6.27-ls407"})
	if got := ReleaseCandidates(tags)[0].Raw; got != "0.6.27-ls407" {
		t.Errorf("first candidate = %q, want the newest lsio release", got)
	}
}

func TestBuildNumbersAreNotVersions(t *testing.T) {
	for _, raw := range []string{"254622078", "8400", "release-9874757", "20230811"} {
		if tag, ok := (Parser{}).Parse(raw); ok {
			t.Errorf("Parse(%q) = %+v, want no match", raw, tag)
		}
	}
	if _, ok := (Parser{}).Parse("17"); !ok {
		t.Error("small single numbers such as postgres major tags are versions")
	}
}

func TestPackaged(t *testing.T) {
	p := Parser{}
	tags := p.ParseAll([]string{"3.1.6.5079-ls46", "8.1.2135", "10.11.11", "12.3"})
	upstream := mustParse(t, p, "v3.2.0.5144")
	if Packaged(upstream, mustParse(t, p, "3.1.6.5079-ls46"), tags) {
		t.Error("lsio has not packaged 3.2.0 yet; the stray 8.1.2135 tag is a different family")
	}
	if !Packaged(mustParse(t, p, "v12.3"), mustParse(t, p, "10.11.11"), tags) {
		t.Error("12.3 is packaged as a shorter tag of the same family")
	}
}

func TestIsUpstream(t *testing.T) {
	for k, want := range map[Kind]bool{KindMajor: true, KindMinor: true, KindPatch: true, KindRebuild: false, KindDigest: false, KindNone: false} {
		if k.IsUpstream() != want {
			t.Errorf("%s.IsUpstream() = %v", k, !want)
		}
	}
}
