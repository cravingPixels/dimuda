// Package updater decides which image updates dimuda applies by itself and rewrites the
// compose files for them. Deciding is a pure function of the report, the policy and what
// was seen or applied before, so every rule is testable without git or registries.
package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cravingPixels/dimuda/internal/imageref"
	"github.com/cravingPixels/dimuda/internal/overrides"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

const gitSourcePrefix = "git:"

type Action string

const (
	// ActionApply: commit the change now.
	ActionApply Action = "apply"
	// ActionWait: the change will be applied once its waiting period or the window allows.
	ActionWait Action = "wait"
	// ActionHold: the change needs a person.
	ActionHold Action = "hold"
)

type Policy struct {
	MinAge    time.Duration
	Window    Window
	Overrides overrides.Set
}

type Decision struct {
	Key        string         `json:"key"`
	Image      string         `json:"image"`
	Update     scanner.Update `json:"update"`
	Action     Action         `json:"action"`
	Reason     string         `json:"reason"`
	ApplyAfter time.Time      `json:"applyAfter"`
	Changes    []Change       `json:"changes"`
}

// Change is one compose file line to rewrite.
type Change struct {
	Repo    string `json:"repo"`
	Stack   string `json:"stack"`
	File    string `json:"file"`
	Service string `json:"service"`
	Old     string `json:"old"`
	New     string `json:"new"`
}

// Target identifies an update by where it goes, so a new release restarts the waiting period
// and a reverted target is recognised when it shows up again.
func Target(key string, u scanner.Update) string {
	return key + " -> " + u.ToTag + "@" + u.ToDigest
}

// Decide returns one decision per image that has an update. firstSeen holds when each target
// was first offered; applied holds the targets dimuda already committed.
func Decide(report scanner.Report, p Policy, firstSeen, applied map[string]time.Time, now time.Time) []Decision {
	var out []Decision
	for _, img := range report.Images {
		if img.Update == nil {
			continue
		}
		d := Decision{Key: img.Key, Image: img.Image, Update: *img.Update}
		d.Changes, d.Reason = changes(img)
		d.Action, d.Reason, d.ApplyAfter = p.decide(img, d, firstSeen, applied, now)
		out = append(out, d)
	}
	return out
}

func (p Policy) decide(img scanner.ImageReport, d Decision, firstSeen, applied map[string]time.Time, now time.Time) (Action, string, time.Time) {
	if len(d.Changes) == 0 {
		return ActionHold, d.Reason, time.Time{}
	}
	target := Target(img.Key, d.Update)
	if _, done := applied[target]; done {
		return ActionHold, "applied before and then reverted in git; bump it by hand to resume", time.Time{}
	}

	ref, err := imageref.Parse(img.Key)
	if err != nil {
		return ActionHold, err.Error(), time.Time{}
	}
	level := p.Overrides.For(ref).AutoUpdate
	switch {
	case d.Update.Kind == versioning.KindDigest:
		return ActionHold, "the tag was re-pushed with different content; check before trusting it", time.Time{}
	case !level.Allows(d.Update.Kind):
		return ActionHold, fmt.Sprintf("%s update needs review (autoUpdate: %s)", d.Update.Kind, level), time.Time{}
	}

	if d.Update.Security {
		return ActionApply, "security fix: skips the waiting period and the window", time.Time{}
	}
	// A pin does not change what runs, so it does not wait for the release to age.
	if d.Update.Kind != versioning.KindPin {
		seen, ok := firstSeen[target]
		if !ok {
			seen = now
		}
		if ready := seen.Add(p.MinAge); now.Before(ready) {
			return ActionWait, "waiting for the release to age", p.Window.NextOpen(ready)
		}
	}
	if !p.Window.Contains(now) {
		return ActionWait, "outside the update window", p.Window.NextOpen(now)
	}
	return ActionApply, "", time.Time{}
}

// changes lists the lines to rewrite. Only git sources are writable, and an image built from
// ${VAR} cannot be edited without knowing which .env value to change.
func changes(img scanner.ImageReport) ([]Change, string) {
	var out []Change
	reason := "no compose line can be edited"
	for _, u := range img.Usages {
		switch {
		case !strings.HasPrefix(u.Source, gitSourcePrefix):
			reason = "used only from a mounted folder, which dimuda cannot commit to"
			continue
		case strings.Contains(u.Raw, "$"):
			reason = "the image comes from a variable; change it in .env by hand"
			continue
		}
		out = append(out, Change{
			Repo:    strings.TrimPrefix(u.Source, gitSourcePrefix),
			Stack:   u.Stack,
			File:    u.File,
			Service: u.Service,
			Old:     u.Raw,
			New:     Rewrite(u.Raw, img.Update.ToTag, img.Update.ToDigest),
		})
	}
	if len(out) > 0 {
		reason = ""
	}
	return out, reason
}

// Rewrite keeps the image name exactly as written ("docker.io/valkey/valkey" stays that way)
// and replaces tag and digest.
func Rewrite(raw, tag, digest string) string {
	name, _, _ := strings.Cut(raw, "@")
	if colon := strings.LastIndex(name, ":"); colon > strings.LastIndex(name, "/") {
		name = name[:colon]
	}
	return name + ":" + tag + "@" + digest
}

// Apply rewrites the image lines of one change in the checkout at root. It edits text instead
// of re-encoding YAML so comments and layout survive.
func Apply(root string, c Change) error {
	path := filepath.Join(root, c.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// The trailing group stops "app:v1" from matching inside "app:v1.2.0".
	line := regexp.MustCompile(`(image:[ \t]*["']?)` + regexp.QuoteMeta(c.Old) + `(["']?(?:[\s,}#]|$))`)
	if !line.Match(raw) {
		return fmt.Errorf("%s: no image line %q for service %s", c.File, c.Old, c.Service)
	}
	edited := line.ReplaceAll(raw, []byte("${1}"+strings.ReplaceAll(c.New, "$", "$$")+"${2}"))
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, edited, info.Mode().Perm())
}

// Window is a daily time range in local time; it may wrap past midnight. A zero Window is always open.
type Window struct {
	Start, End time.Duration
	Location   *time.Location
}

func ParseWindow(raw string, loc *time.Location) (Window, error) {
	if raw == "" {
		return Window{}, nil
	}
	start, end, ok := strings.Cut(raw, "-")
	if !ok {
		return Window{}, fmt.Errorf("window %q must look like 03:00-05:00", raw)
	}
	s, err := clock(start)
	if err != nil {
		return Window{}, err
	}
	e, err := clock(end)
	if err != nil {
		return Window{}, err
	}
	if s == e {
		return Window{}, fmt.Errorf("window %q is empty", raw)
	}
	return Window{Start: s, End: e, Location: loc}, nil
}

func clock(raw string) (time.Duration, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("time %q must be HH:MM", raw)
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

func (w Window) always() bool { return w.Start == w.End }

// IsSet reports whether updates are restricted to a time range at all.
func (w Window) IsSet() bool { return !w.always() }

func (w Window) sinceMidnight(t time.Time) (time.Time, time.Duration) {
	if w.Location != nil {
		t = t.In(w.Location)
	}
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return midnight, t.Sub(midnight)
}

func (w Window) Contains(t time.Time) bool {
	if w.always() {
		return true
	}
	_, of := w.sinceMidnight(t)
	if w.Start < w.End {
		return of >= w.Start && of < w.End
	}
	return of >= w.Start || of < w.End
}

// NextOpen returns t itself when the window is open then, otherwise the next opening.
func (w Window) NextOpen(t time.Time) time.Time {
	if w.Contains(t) {
		return t
	}
	return w.NextStart(t)
}

// NextStart is the next time the window opens strictly after t, even when t is inside it.
func (w Window) NextStart(t time.Time) time.Time {
	midnight, of := w.sinceMidnight(t)
	start := midnight.Add(w.Start)
	if of >= w.Start {
		start = midnight.AddDate(0, 0, 1).Add(w.Start)
	}
	return start
}
