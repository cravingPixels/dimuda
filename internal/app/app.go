// Package app owns the latest report, runs scans on a schedule, persists state across
// restarts and turns new upstream releases into notifications.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cravingPixels/dimuda/internal/notify"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/updater"
)

type Scanner interface {
	Scan(ctx context.Context) scanner.Report
}

type Notifier interface {
	Send(ctx context.Context, m notify.Message) error
}

type state struct {
	Report   *scanner.Report   `json:"report"`
	Notified map[string]string `json:"notified"`
	// FirstSeen and Applied are keyed by updater.Target: when an update was first offered, and
	// which ones dimuda committed (so a reverted one is not applied again).
	FirstSeen map[string]time.Time `json:"firstSeen"`
	Applied   map[string]time.Time `json:"applied"`
	Decisions []updater.Decision   `json:"decisions"`
}

func (s *state) init() {
	if s.Notified == nil {
		s.Notified = map[string]string{}
	}
	if s.FirstSeen == nil {
		s.FirstSeen = map[string]time.Time{}
	}
	if s.Applied == nil {
		s.Applied = map[string]time.Time{}
	}
}

type App struct {
	scanner   Scanner
	notifier  Notifier
	statePath string
	publicURL string
	log       *slog.Logger
	auto      *AutoUpdate

	tokenChecker TokenChecker
	tokens       []Token

	scanMu sync.Mutex
	mu     sync.RWMutex
	state  state
	// fresh is true until the first scan after a start without saved state; that scan only
	// records what is pending so a new install does not send a notification for every image.
	fresh    bool
	scanning bool
}

func New(s Scanner, n Notifier, dataDir, publicURL string, log *slog.Logger) (*App, error) {
	a := &App{
		scanner:   s,
		notifier:  n,
		statePath: filepath.Join(dataDir, "state.json"),
		publicURL: publicURL,
		log:       log,
	}
	a.state.init()
	raw, err := os.ReadFile(a.statePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.fresh = true
	case err != nil:
		return nil, fmt.Errorf("read state: %w", err)
	default:
		if err := json.Unmarshal(raw, &a.state); err != nil {
			return nil, fmt.Errorf("decode state %s: %w", a.statePath, err)
		}
		a.state.init()
	}
	return a, nil
}

func (a *App) Report() (scanner.Report, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state.Report == nil {
		return scanner.Report{}, false
	}
	return *a.state.Report, true
}

// EnableAutoUpdate makes every scan commit the updates au's policy allows.
func (a *App) EnableAutoUpdate(au AutoUpdate) {
	a.auto = &au
}

// Decisions maps image keys to what auto-update decided for them in the last scan.
func (a *App) Decisions() map[string]updater.Decision {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[string]updater.Decision, len(a.state.Decisions))
	for _, d := range a.state.Decisions {
		out[d.Key] = d
	}
	return out
}

func (a *App) Scanning() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.scanning
}

// Run scans immediately and then every interval until ctx is cancelled. With an update window it
// also scans just after the window opens, so waiting updates do not depend on the interval
// happening to land inside it.
func (a *App) Run(ctx context.Context, interval time.Duration) {
	a.RunScan(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		window := make(<-chan time.Time)
		var timer *time.Timer
		if a.auto != nil && a.auto.Policy.Window.IsSet() {
			opens := a.auto.Policy.Window.NextStart(a.auto.now()).Add(time.Minute)
			timer = time.NewTimer(time.Until(opens))
			window = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ticker.C:
		case <-window:
		}
		if timer != nil {
			timer.Stop()
		}
		a.RunScan(ctx)
	}
}

// RunScan returns false without scanning when another scan is in progress.
func (a *App) RunScan(ctx context.Context) bool {
	if !a.scanMu.TryLock() {
		return false
	}
	defer a.scanMu.Unlock()
	a.setScanning(true)
	defer a.setScanning(false)

	report := a.scanner.Scan(ctx)
	a.log.Info("scan finished", "images", len(report.Images), "errors", len(report.Errors), "duration", report.Duration)
	for _, e := range report.Errors {
		a.log.Warn("scan error", "err", e)
	}

	var notes []note
	if a.auto != nil {
		out := a.autoUpdate(ctx, report)
		for _, d := range out.applied {
			a.log.Info("update applied", "image", d.Image, "from", d.Update.FromTag, "to", d.Update.ToTag, "kind", d.Update.Kind)
		}
		for _, f := range out.failures {
			a.log.Error("auto-update", "err", f)
		}
		notes = a.autoNotes(report, out)
	} else if pending := a.newUpstreamUpdates(report); len(pending) > 0 {
		marks := map[string]string{}
		for _, p := range pending {
			marks[p.Key] = p.target
		}
		notes = append(notes, note{msg: a.message(pending), marks: marks})
	}

	if a.tokenChecker != nil {
		notes = append(notes, a.tokenNotes(ctx, time.Now())...)
	}

	var delivered []note
	for _, n := range notes {
		if n.msg.Title == "" || a.notifier == nil || (a.fresh && !n.always) {
			delivered = append(delivered, n)
			continue
		}
		if err := a.notifier.Send(ctx, n.msg); err != nil {
			a.log.Error("notification failed, will retry next scan", "err", err)
			continue
		}
		delivered = append(delivered, n)
	}

	a.mu.Lock()
	a.state.Report = &report
	for _, n := range delivered {
		for k, v := range n.marks {
			a.state.Notified[k] = v
		}
		for _, k := range n.clear {
			delete(a.state.Notified, k)
		}
	}
	a.fresh = false
	a.mu.Unlock()

	if err := a.save(); err != nil {
		a.log.Error("save state", "err", err)
	}
	return true
}

func (a *App) setScanning(v bool) {
	a.mu.Lock()
	a.scanning = v
	a.mu.Unlock()
}

type update struct {
	scanner.ImageReport
	target string
}

func (a *App) newUpstreamUpdates(report scanner.Report) []update {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []update
	for _, img := range report.Images {
		if !img.Kind.IsUpstream() {
			continue
		}
		target := img.Latest
		if img.Floating && img.ResolvesTo != "" {
			target = img.ResolvesTo
		}
		if a.state.Notified[img.Key] != target {
			out = append(out, update{ImageReport: img, target: target})
		}
	}
	return out
}

func (a *App) message(updates []update) notify.Message {
	lines := make([]string, 0, len(updates))
	for _, u := range updates {
		from := u.Tag
		if u.Floating {
			for _, r := range u.Running {
				if r.Version != "" {
					from = r.Version
					break
				}
			}
		}
		lines = append(lines, fmt.Sprintf("%s %s → %s (%s) · %s", u.Image, from, u.target, u.Kind, strings.Join(u.Stacks(), ", ")))
	}
	title := fmt.Sprintf("%d upstream updates", len(updates))
	if len(updates) == 1 {
		title = fmt.Sprintf("%s: %s available", updates[0].Image, updates[0].target)
	}
	return notify.Message{Title: title, Body: strings.Join(lines, "\n"), Click: a.publicURL, Tags: []string{"package"}}
}

func (a *App) save() error {
	a.mu.RLock()
	raw, err := json.Marshal(a.state)
	a.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath)
}
