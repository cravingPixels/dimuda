// Package scanner builds the update report: it collects image usages from compose sources,
// matches them with running containers, and asks registries and GitHub what is newer.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cravingPixels/dimuda/internal/changelog"
	"github.com/cravingPixels/dimuda/internal/compose"
	"github.com/cravingPixels/dimuda/internal/docker"
	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/imageref"
	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/registry"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

const (
	concurrency = 6
	// How many release tags are HEAD-checked to find which version a floating tag points to.
	maxDigestProbes = 12
)

type Registry interface {
	ListTags(ctx context.Context, ref imageref.Ref) ([]string, error)
	Digest(ctx context.Context, ref imageref.Ref, tag string) (string, error)
}

type Releases interface {
	Releases(ctx context.Context, repo string) ([]changelog.Release, error)
}

type Containers interface {
	Containers(ctx context.Context) ([]docker.Container, error)
}

type GitSyncer interface {
	Sync(ctx context.Context, repo gitsource.Repo) (string, error)
}

type Dir struct {
	Name string
	Path string
}

type Scanner struct {
	Dirs      []Dir
	GitRepos  []gitsource.Repo
	Git       GitSyncer
	Docker    Containers
	Registry  Registry
	Releases  Releases
	Overrides overrides.Set
	Logger    *slog.Logger
}

type Report struct {
	GeneratedAt time.Time     `json:"generatedAt"`
	Duration    string        `json:"duration"`
	Images      []ImageReport `json:"images"`
	Errors      []string      `json:"errors"`
	Stacks      []string      `json:"stacks"`
}

type Usage struct {
	Source  string `json:"source"`
	Stack   string `json:"stack"`
	Service string `json:"service"`
	File    string `json:"file"`
}

type Running struct {
	Container string `json:"container"`
	State     string `json:"state"`
	Stack     string `json:"stack"`
	Version   string `json:"version"`
	Digest    string `json:"digest"`
	Outdated  bool   `json:"outdated"`
}

type ImageReport struct {
	Key           string              `json:"key"`
	Image         string              `json:"image"`
	Tag           string              `json:"tag"`
	Usages        []Usage             `json:"usages"`
	Running       []Running           `json:"running"`
	Floating      bool                `json:"floating"`
	Latest        string              `json:"latest"`
	ResolvesTo    string              `json:"resolvesTo"`
	PinTo         string              `json:"pinTo"`
	Kind          versioning.Kind     `json:"kind"`
	RemoteDigest  string              `json:"remoteDigest"`
	Upstream      string              `json:"upstream"`
	UpstreamURL   string              `json:"upstreamUrl"`
	Unpackaged    bool                `json:"unpackaged"`
	ChangelogRepo string              `json:"changelogRepo"`
	Releases      []changelog.Release `json:"releases"`
	Note          string              `json:"note"`
	Ignored       bool                `json:"ignored"`
	LocalBuild    bool                `json:"localBuild"`
	Err           string              `json:"error"`
}

func (r ImageReport) Stacks() []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range r.Usages {
		if !seen[u.Stack] {
			seen[u.Stack] = true
			out = append(out, u.Stack)
		}
	}
	sort.Strings(out)
	return out
}

type group struct {
	ref        imageref.Ref
	usages     []compose.Usage
	localBuild bool
}

func (s *Scanner) Scan(ctx context.Context) Report {
	start := time.Now()
	report := Report{GeneratedAt: start}

	usages, errs := s.collectUsages(ctx)
	report.Errors = append(report.Errors, errs...)

	var containers []docker.Container
	if s.Docker != nil {
		var err error
		if containers, err = s.Docker.Containers(ctx); err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
	}

	groups, invalid := groupUsages(usages)
	report.Images = append(report.Images, invalid...)

	byRepo := map[string][]*group{}
	for _, g := range groups {
		byRepo[g.ref.Name()] = append(byRepo[g.ref.Name()], g)
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, repoGroups := range byRepo {
		wg.Add(1)
		sem <- struct{}{}
		go func(repoGroups []*group) {
			defer wg.Done()
			defer func() { <-sem }()
			results := s.checkRepo(ctx, repoGroups, containers)
			mu.Lock()
			report.Images = append(report.Images, results...)
			mu.Unlock()
		}(repoGroups)
	}
	wg.Wait()

	sort.Slice(report.Images, func(i, j int) bool {
		a, b := report.Images[i], report.Images[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a.Key < b.Key
	})
	report.Stacks = stacksOf(report.Images)
	report.Duration = time.Since(start).Round(time.Millisecond).String()
	return report
}

func rank(r ImageReport) int {
	switch {
	case r.Kind == versioning.KindMajor:
		return 0
	case r.Kind == versioning.KindMinor:
		return 1
	case r.Kind == versioning.KindPatch:
		return 2
	case r.Kind == versioning.KindRebuild:
		return 3
	case r.Kind == versioning.KindDigest:
		return 4
	case r.Err != "":
		return 5
	case r.Kind == versioning.KindUnknown:
		return 6
	}
	return 7
}

func stacksOf(images []ImageReport) []string {
	seen := map[string]bool{}
	var out []string
	for _, img := range images {
		for _, st := range img.Stacks() {
			if !seen[st] {
				seen[st] = true
				out = append(out, st)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Scanner) collectUsages(ctx context.Context) ([]compose.Usage, []string) {
	var all []compose.Usage
	var errs []string
	for _, d := range s.Dirs {
		found, err := compose.Scan(d.Name, d.Path)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, found...)
	}
	for _, repo := range s.GitRepos {
		dir, err := s.Git.Sync(ctx, repo)
		if err != nil {
			errs = append(errs, fmt.Sprintf("git %s: %v", repo.Name, err))
			continue
		}
		found, err := compose.Scan("git:"+repo.Name, dir)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		all = append(all, found...)
	}
	return all, errs
}

func groupUsages(usages []compose.Usage) ([]*group, []ImageReport) {
	byKey := map[string]*group{}
	var order []string
	var invalid []ImageReport
	for _, u := range usages {
		if u.Err != "" {
			invalid = append(invalid, ImageReport{
				Key: u.Source + "/" + u.File + "/" + u.Service, Image: u.Image, Kind: versioning.KindUnknown, Err: u.Err,
				Usages: []Usage{toUsage(u)},
			})
			continue
		}
		key := u.Ref.Name() + ":" + u.Ref.Tag + "@" + u.Ref.Digest
		g, ok := byKey[key]
		if !ok {
			g = &group{ref: u.Ref, localBuild: true}
			byKey[key] = g
			order = append(order, key)
		}
		g.usages = append(g.usages, u)
		g.localBuild = g.localBuild && u.LocalBuild
	}
	groups := make([]*group, 0, len(order))
	for _, k := range order {
		groups = append(groups, byKey[k])
	}
	return groups, invalid
}

func toUsage(u compose.Usage) Usage {
	return Usage{Source: u.Source, Stack: u.Stack, Service: u.Service, File: u.File}
}

func (s *Scanner) checkRepo(ctx context.Context, groups []*group, containers []docker.Container) []ImageReport {
	ref := groups[0].ref
	ov := s.Overrides.For(ref)

	var tags []versioning.Tag
	var listErr error
	needsRegistry := false
	for _, g := range groups {
		needsRegistry = needsRegistry || (!g.localBuild && !ov.Ignore)
	}
	if needsRegistry {
		var raw []string
		raw, listErr = s.Registry.ListTags(ctx, ref)
		tags = ov.Parser().ParseAll(raw)
	}

	results := make([]ImageReport, 0, len(groups))
	for _, g := range groups {
		r := s.newReport(g, ov, containers)
		switch {
		case g.localBuild:
			r.LocalBuild = true
			r.Kind = versioning.KindNone
		case ov.Ignore:
			r.Kind = versioning.KindNone
		case listErr != nil:
			r.Kind = versioning.KindUnknown
			r.Err = describe(listErr)
		default:
			s.checkTag(ctx, &r, g.ref, ov, tags)
		}
		results = append(results, r)
	}
	return results
}

func describe(err error) string {
	var se *registry.StatusError
	if errors.As(err, &se) && se.NotFound() {
		return "image not found on registry (private, renamed or local build)"
	}
	return err.Error()
}

func (s *Scanner) newReport(g *group, ov overrides.Image, containers []docker.Container) ImageReport {
	r := ImageReport{
		Key:     g.ref.String(),
		Image:   g.ref.ShortName(),
		Tag:     g.ref.Tag,
		Note:    ov.Note,
		Ignored: ov.Ignore,
		Kind:    versioning.KindNone,
	}
	if g.ref.Tag == "" {
		r.Tag = "@" + g.ref.Digest
	}
	for _, u := range g.usages {
		r.Usages = append(r.Usages, toUsage(u))
	}
	for _, c := range containers {
		cref, err := imageref.Parse(c.Image)
		if err != nil || cref.Name() != g.ref.Name() || cref.Tag != g.ref.Tag {
			continue
		}
		r.Running = append(r.Running, Running{
			Container: c.Name, State: c.State, Stack: c.Project, Version: c.Version, Digest: c.DigestFor(g.ref),
		})
		if r.ChangelogRepo == "" {
			r.ChangelogRepo, _ = changelog.RepoFromURL(c.SourceURL)
		}
	}
	if ov.Changelog != "" {
		r.ChangelogRepo = ov.Changelog
	}
	return r
}

func (s *Scanner) checkTag(ctx context.Context, r *ImageReport, ref imageref.Ref, ov overrides.Image, tags []versioning.Tag) {
	if ref.Tag == "" {
		r.Note = joinNote(r.Note, "pinned by digest only")
		return
	}
	remote, err := s.Registry.Digest(ctx, ref, ref.Tag)
	if err != nil {
		r.Kind = versioning.KindUnknown
		r.Err = describe(err)
		var se *registry.StatusError
		if errors.As(err, &se) && se.NotFound() {
			r.Err = fmt.Sprintf("tag %q no longer exists on the registry", ref.Tag)
		}
		return
	}
	r.RemoteDigest = remote
	digestMoved := false
	for i := range r.Running {
		if r.Running[i].Digest != "" && r.Running[i].Digest != remote {
			r.Running[i].Outdated = true
			digestMoved = true
		}
	}

	parser := ov.Parser()
	cur, parsed := parser.Parse(ref.Tag)
	if parsed {
		latest := versioning.Latest(cur, tags)
		r.Latest = latest.Raw
		r.Kind = versioning.Classify(cur, latest)
		if specific, ok := versioning.MostSpecific(cur, tags); ok {
			r.Floating = true
			r.PinTo = specific.Raw
			r.ResolvesTo = s.resolve(ctx, ref, remote, candidatesIn(cur, tags))
		}
	} else {
		r.Floating = true
		candidates := versioning.ReleaseCandidates(tags)
		if len(candidates) > 0 {
			r.Latest = candidates[0].Raw
		}
		r.ResolvesTo = s.resolve(ctx, ref, remote, candidates)
		r.PinTo = r.ResolvesTo
		if r.ResolvesTo != "" {
			r.Latest = r.ResolvesTo
		}
	}

	if r.Kind == versioning.KindNone && r.Floating {
		r.Kind = s.floatingKind(r, parser, digestMoved)
	} else if r.Kind == versioning.KindNone && digestMoved {
		r.Kind = versioning.KindDigest
	}

	s.attachChangelog(ctx, r, parser, cur, parsed, tags)
}

// floatingKind upgrades a moved floating tag to a real upstream kind when the running
// container's version label tells us what it is running.
func (s *Scanner) floatingKind(r *ImageReport, parser versioning.Parser, digestMoved bool) versioning.Kind {
	if !digestMoved {
		return versioning.KindNone
	}
	target, ok := parser.Parse(r.ResolvesTo)
	if !ok {
		return versioning.KindDigest
	}
	for _, run := range r.Running {
		if !run.Outdated {
			continue
		}
		if running, ok := parser.Parse(run.Version); ok {
			if kind := versioning.Classify(running, target); kind != versioning.KindNone {
				return kind
			}
		}
	}
	return versioning.KindDigest
}

func candidatesIn(cur versioning.Tag, tags []versioning.Tag) []versioning.Tag {
	var line []versioning.Tag
	for _, t := range tags {
		if t.Prefix == cur.Prefix && t.Suffix == cur.Suffix && len(t.Version) > len(cur.Version) && versioning.CompareVersions(t, cur) >= 0 {
			line = append(line, t)
		}
	}
	sort.SliceStable(line, func(i, j int) bool {
		if len(line[i].Version) != len(line[j].Version) {
			return len(line[i].Version) > len(line[j].Version)
		}
		return versioning.Compare(line[i], line[j]) > 0
	})
	return line
}

// resolve finds which concrete version tag shares the floating tag's digest. That is the
// only reliable way to know what "latest" or "8-alpine" actually contains right now.
func (s *Scanner) resolve(ctx context.Context, ref imageref.Ref, digest string, candidates []versioning.Tag) string {
	for i, c := range candidates {
		if i >= maxDigestProbes {
			break
		}
		d, err := s.Registry.Digest(ctx, ref, c.Raw)
		if err != nil {
			continue
		}
		if d == digest {
			return c.Raw
		}
	}
	return ""
}

func (s *Scanner) attachChangelog(ctx context.Context, r *ImageReport, parser versioning.Parser, cur versioning.Tag, parsed bool, tags []versioning.Tag) {
	if r.ChangelogRepo == "" || s.Releases == nil {
		return
	}
	releases, err := s.Releases.Releases(ctx, r.ChangelogRepo)
	if err != nil {
		s.log().Warn("changelog unavailable", "repo", r.ChangelogRepo, "err", err)
		return
	}

	packaged := r.Latest
	if r.Floating && r.ResolvesTo != "" {
		packaged = r.ResolvesTo
	}
	upstream := releases
	if parsed && r.Floating {
		upstream = releasesInLine(releases, cur)
	}
	if newest, newestTag, ok := changelog.Newest(upstream); ok {
		r.Upstream = newest.Tag
		r.UpstreamURL = newest.URL
		if pkg, ok := parser.Parse(packaged); ok && !versioning.Packaged(newestTag, pkg, tags) {
			r.Unpackaged = true
		}
	}

	from, ok := s.currentVersion(r, parser, cur, parsed)
	if !ok {
		return
	}
	to, ok := parser.Parse(packaged)
	if !ok {
		return
	}
	r.Releases = changelog.Between(releases, from, to)
}

// currentVersion is what is deployed today: the pinned tag, or for floating tags the
// version label of a running container (falling back to what the tag resolves to).
func (s *Scanner) currentVersion(r *ImageReport, parser versioning.Parser, cur versioning.Tag, parsed bool) (versioning.Tag, bool) {
	if parsed && !r.Floating {
		return cur, true
	}
	for _, run := range r.Running {
		if v, ok := parser.Parse(run.Version); ok {
			return v, true
		}
	}
	return parser.Parse(r.ResolvesTo)
}

// releasesInLine keeps releases a partial tag can ever receive: "8-alpine" never gets 9.x.
func releasesInLine(releases []changelog.Release, line versioning.Tag) []changelog.Release {
	var out []changelog.Release
	for _, r := range releases {
		if v, ok := changelog.ParseReleaseTag(r.Tag); ok && versioning.InLine(v, line) {
			out = append(out, r)
		}
	}
	return out
}

func joinNote(a, b string) string {
	return strings.TrimPrefix(a+"; "+b, "; ")
}

func (s *Scanner) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
