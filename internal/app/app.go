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
}

type App struct {
	scanner   Scanner
	notifier  Notifier
	statePath string
	publicURL string
	log       *slog.Logger

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
		state:     state{Notified: map[string]string{}},
	}
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
		if a.state.Notified == nil {
			a.state.Notified = map[string]string{}
		}
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

func (a *App) Scanning() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.scanning
}

// Run scans immediately and then every interval until ctx is cancelled.
func (a *App) Run(ctx context.Context, interval time.Duration) {
	a.RunScan(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.RunScan(ctx)
		}
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

	pending := a.newUpstreamUpdates(report)
	if len(pending) > 0 && !a.fresh && a.notifier != nil {
		if err := a.notifier.Send(ctx, a.message(pending)); err != nil {
			a.log.Error("notification failed, will retry next scan", "err", err)
			pending = nil
		}
	}

	a.mu.Lock()
	a.state.Report = &report
	for _, p := range pending {
		a.state.Notified[p.Key] = p.target
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
