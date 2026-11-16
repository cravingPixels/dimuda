package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/cravingPixels/dimuda/internal/notify"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/updater"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

const (
	holdMark  = "hold|"
	errorMark = "error|"

	priorityReview = 4
)

// note is a notification plus what to remember once it is delivered, so the same event is
// not sent every scan. always means it is sent even on the first scan of a new install.
type note struct {
	msg    notify.Message
	marks  map[string]string
	clear  []string
	always bool
}

func (a *App) autoNotes(report scanner.Report, out outcome) []note {
	a.mu.RLock()
	notified := make(map[string]string, len(a.state.Notified))
	for k, v := range a.state.Notified {
		notified[k] = v
	}
	a.mu.RUnlock()

	byKey := map[string]scanner.ImageReport{}
	for _, img := range report.Images {
		byKey[img.Key] = img
	}

	var notes []note
	if len(out.applied) > 0 {
		notes = append(notes, note{msg: a.appliedMessage(out.applied, byKey), always: true})
	}
	if n, ok := a.reviewNote(out.decisions, report, byKey, notified); ok {
		notes = append(notes, n)
	}
	notes = append(notes, a.failureNote(out.failures, notified))
	return notes
}

func (a *App) appliedMessage(applied []updater.Decision, byKey map[string]scanner.ImageReport) notify.Message {
	lines := make([]string, 0, len(applied))
	for _, d := range applied {
		lines = append(lines, describe(d, byKey[d.Key]))
	}
	title := fmt.Sprintf("%d updates applied", len(applied))
	if len(applied) == 1 {
		title = fmt.Sprintf("%s updated to %s", applied[0].Image, applied[0].Update.ToTag)
	}
	return notify.Message{Title: title, Body: strings.Join(lines, "\n"), Click: a.publicURL, Tags: []string{"package"}}
}

// reviewNote lists what needs a person: held updates and new majors, each once per reason.
func (a *App) reviewNote(decisions []updater.Decision, report scanner.Report, byKey map[string]scanner.ImageReport, notified map[string]string) (note, bool) {
	var lines []string
	marks := map[string]string{}
	for _, d := range decisions {
		if d.Action != updater.ActionHold {
			continue
		}
		key := holdMark + updater.Target(d.Key, d.Update)
		if notified[key] == d.Reason {
			continue
		}
		lines = append(lines, describe(d, byKey[d.Key])+"\n  "+d.Reason)
		marks[key] = d.Reason
	}
	for _, img := range report.Images {
		if img.Kind != versioning.KindMajor || notified[img.Key] == img.Latest {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s → %s (major) · %s", img.Image, img.Tag, img.Latest, strings.Join(img.Stacks(), ", ")))
		marks[img.Key] = img.Latest
	}
	if len(lines) == 0 {
		return note{}, false
	}
	title := fmt.Sprintf("%d updates need review", len(lines))
	if len(lines) == 1 {
		title = "1 update needs review"
	}
	msg := notify.Message{Title: title, Body: strings.Join(lines, "\n"), Click: a.publicURL, Tags: []string{"eyes"}, Priority: priorityReview}
	return note{msg: msg, marks: marks}, true
}

// failureNote alerts once per distinct failure and forgets failures that stopped happening,
// so a problem that comes back is reported again.
func (a *App) failureNote(failures []string, notified map[string]string) note {
	current := map[string]bool{}
	var lines []string
	marks := map[string]string{}
	for _, f := range failures {
		key := errorMark + f
		current[key] = true
		if notified[key] != "" {
			continue
		}
		lines = append(lines, f)
		marks[key] = "1"
	}
	var clear []string
	for key := range notified {
		if strings.HasPrefix(key, errorMark) && !current[key] {
			clear = append(clear, key)
		}
	}
	sort.Strings(clear)
	n := note{marks: marks, clear: clear, always: true}
	if len(lines) > 0 {
		n.msg = notify.Message{Title: "dimuda could not apply updates", Body: strings.Join(lines, "\n"), Click: a.publicURL, Tags: []string{"warning"}, Priority: priorityReview}
	}
	return n
}

func describe(d updater.Decision, img scanner.ImageReport) string {
	kind := string(d.Update.Kind)
	if d.Update.Security {
		kind += ", security"
	}
	return fmt.Sprintf("%s %s → %s (%s) · %s", d.Image, d.Update.FromTag, d.Update.ToTag, kind, strings.Join(img.Stacks(), ", "))
}
