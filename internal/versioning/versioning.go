// Package versioning understands the tag conventions found on container registries:
// semver ("v2.34.0"), partial versions ("8-alpine"), LinuxServer.io builds ("4.0.15-ls290"),
// hotio releases ("release-4.0.20.3014"), calendar versions ("2026.10.0", "2026-07-14c")
// and commit-suffixed calendar versions ("2026.10.25-54ca1d9cf").
package versioning

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Kind string

const (
	KindNone    Kind = "none"
	KindMajor   Kind = "major"
	KindMinor   Kind = "minor"
	KindPatch   Kind = "patch"
	KindRebuild Kind = "rebuild"
	KindDigest  Kind = "digest"
	KindUnknown Kind = "unknown"
)

// IsUpstream reports whether the kind means the packaged application itself changed,
// as opposed to a repackaging (lsio rebuild) or a moved floating tag.
func (k Kind) IsUpstream() bool {
	return k == KindMajor || k == KindMinor || k == KindPatch
}

const (
	hashPlaceholder = "-<hash>"
	// Single numbers above this are CI build or PR numbers ("8400", "release-9874757"), not versions.
	maxSingleComponent = 999
)

type Tag struct {
	Raw     string
	Prefix  string
	Version []int
	Build   string
	Suffix  string
}

func (t Tag) VersionString() string {
	parts := make([]string, len(t.Version))
	for i, v := range t.Version {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ".")
}

var (
	defaultPattern = regexp.MustCompile(`^(?P<prefix>(?:[a-z][a-z0-9]*-)*?v?)(?P<version>\d+(?:\.\d+)*)(?:-ls(?P<build>\d+))?(?P<suffix>[-_+].*)?$`)
	datePattern    = regexp.MustCompile(`^(?P<prefix>)(?P<version>\d{4}-\d{2}-\d{2})(?P<build>[a-z]?)(?P<suffix>)$`)
	hashSuffix     = regexp.MustCompile(`^-[0-9a-f]{7,40}$`)
	versionSplit   = regexp.MustCompile(`[.-]`)
	floatingPrefix = map[string]bool{"": true, "v": true, "release-": true}
)

type Parser struct {
	custom *regexp.Regexp
}

// NewParser returns the default parser, or one driven by a per-image override regex.
// An override must name a "version" group and may name "build", "prefix" and "suffix".
func NewParser(pattern string) (Parser, error) {
	if pattern == "" {
		return Parser{}, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Parser{}, fmt.Errorf("compile tag regex %q: %w", pattern, err)
	}
	if re.SubexpIndex("version") < 0 {
		return Parser{}, fmt.Errorf("tag regex %q has no (?P<version>...) group", pattern)
	}
	return Parser{custom: re}, nil
}

func (p Parser) Parse(raw string) (Tag, bool) {
	if p.custom != nil {
		return parseWith(p.custom, raw)
	}
	if t, ok := parseWith(datePattern, raw); ok {
		return t, true
	}
	t, ok := parseWith(defaultPattern, raw)
	if ok && len(t.Version) == 1 && t.Version[0] > maxSingleComponent {
		return Tag{}, false
	}
	return t, ok
}

func (p Parser) ParseAll(raws []string) []Tag {
	tags := make([]Tag, 0, len(raws))
	for _, raw := range raws {
		if t, ok := p.Parse(raw); ok {
			tags = append(tags, t)
		}
	}
	return tags
}

func parseWith(re *regexp.Regexp, raw string) (Tag, bool) {
	m := re.FindStringSubmatch(raw)
	if m == nil {
		return Tag{}, false
	}
	group := func(name string) string {
		if i := re.SubexpIndex(name); i >= 0 {
			return m[i]
		}
		return ""
	}
	version, ok := parseNumbers(group("version"))
	if !ok {
		return Tag{}, false
	}
	suffix := group("suffix")
	if hashSuffix.MatchString(suffix) {
		suffix = hashPlaceholder
	}
	return Tag{Raw: raw, Prefix: group("prefix"), Version: version, Build: group("build"), Suffix: suffix}, true
}

func parseNumbers(s string) ([]int, bool) {
	if s == "" {
		return nil, false
	}
	fields := versionSplit.Split(s, -1)
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// Compare orders by version components, then by build. A shorter version that is a
// prefix of a longer one sorts first, so "3" < "3.7.16".
func Compare(a, b Tag) int {
	for i := 0; i < len(a.Version) && i < len(b.Version); i++ {
		if a.Version[i] != b.Version[i] {
			return cmpInt(a.Version[i], b.Version[i])
		}
	}
	if len(a.Version) != len(b.Version) {
		return cmpInt(len(a.Version), len(b.Version))
	}
	return compareBuild(a.Build, b.Build)
}

// CompareVersions compares only the numeric versions, ignoring packaging builds. It is how an
// image tag ("4.0.15-ls290") is compared with an upstream release tag ("v4.0.15").
func CompareVersions(a, b Tag) int {
	return Compare(Tag{Version: a.Version}, Tag{Version: b.Version})
}

func compareBuild(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	if aErr == nil && bErr == nil {
		return cmpInt(an, bn)
	}
	return strings.Compare(a, b)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// SameShape means both tags belong to the same tag family: same channel prefix, same
// variant suffix and the same packaging (lsio build or not), at any precision.
func SameShape(a, b Tag) bool {
	return a.Prefix == b.Prefix && a.Suffix == b.Suffix && (a.Build == "") == (b.Build == "")
}

// Compatible means cand is a valid replacement for cur: same shape and same precision.
func Compatible(cur, cand Tag) bool {
	return SameShape(cur, cand) && len(cur.Version) == len(cand.Version)
}

// Packaged reports whether any tag of ref's family carries version v or newer, i.e.
// whether the image publisher has caught up with an upstream release.
func Packaged(v, ref Tag, tags []Tag) bool {
	for _, t := range tags {
		if SameShape(t, ref) && CompareVersions(t, v) >= 0 {
			return true
		}
	}
	return false
}

// Latest returns the highest tag compatible with cur, or cur itself when nothing is newer.
func Latest(cur Tag, tags []Tag) Tag {
	best := cur
	for _, t := range tags {
		if Compatible(cur, t) && Compare(t, best) > 0 {
			best = t
		}
	}
	return best
}

// MostSpecific returns the highest tag inside the line cur points to, e.g. "v3.7.16" for
// "v3" or "8.10.2-alpine" for "8-alpine". It is what a partial tag should be pinned to.
func MostSpecific(cur Tag, tags []Tag) (Tag, bool) {
	var best Tag
	found := false
	for _, t := range tags {
		if t.Prefix != cur.Prefix || t.Suffix != cur.Suffix || len(t.Version) <= len(cur.Version) {
			continue
		}
		if !hasPrefix(t.Version, cur.Version) {
			continue
		}
		if !found || len(t.Version) > len(best.Version) || (len(t.Version) == len(best.Version) && Compare(t, best) > 0) {
			best, found = t, true
		}
	}
	return best, found
}

// InLine reports whether v belongs to the version line a partial tag tracks, e.g. 8.10.3 is in line "8".
func InLine(v, line Tag) bool {
	return hasPrefix(v.Version, line.Version)
}

func hasPrefix(v, prefix []int) bool {
	if len(prefix) > len(v) {
		return false
	}
	for i := range prefix {
		if v[i] != prefix[i] {
			return false
		}
	}
	return true
}

// Classify says what kind of change moving from cur to latest is.
func Classify(cur, latest Tag) Kind {
	for i := 0; i < len(cur.Version) && i < len(latest.Version); i++ {
		if latest.Version[i] > cur.Version[i] {
			switch i {
			case 0:
				return KindMajor
			case 1:
				return KindMinor
			default:
				return KindPatch
			}
		}
		if latest.Version[i] < cur.Version[i] {
			return KindNone
		}
	}
	if compareBuild(latest.Build, cur.Build) > 0 {
		return KindRebuild
	}
	return KindNone
}

// ReleaseCandidates returns stable release tags a floating tag such as "latest" could be
// resolving to. Tags of the image's dominant family come first (e.g. "0.6.27-ls407" for
// an lsio image that also carries a few stray legacy tags), newest first within a family.
func ReleaseCandidates(tags []Tag) []Tag {
	out := make([]Tag, 0)
	families := map[string]int{}
	for _, t := range tags {
		if isRelease(t) {
			out = append(out, t)
			families[family(t)]++
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		fi, fj := families[family(out[i])], families[family(out[j])]
		if fi != fj {
			return fi > fj
		}
		return Compare(out[i], out[j]) > 0
	})
	return out
}

func family(t Tag) string {
	return fmt.Sprintf("%s|%s|%t|%d", t.Prefix, t.Suffix, t.Build != "", len(t.Version))
}

func isRelease(t Tag) bool {
	return floatingPrefix[t.Prefix] && (t.Suffix == "" || t.Suffix == hashPlaceholder)
}
