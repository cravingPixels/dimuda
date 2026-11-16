package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cravingPixels/dimuda/internal/notify"
	"github.com/cravingPixels/dimuda/internal/tokencheck"
)

const tokenMark = "token|"

// tokenWarnings are the days-left thresholds that each send one alert.
var tokenWarnings = []int{1, 7, 30}

type TokenChecker interface {
	Expiry(ctx context.Context, token string) (time.Time, bool, error)
}

// Token is a credential to watch, named as the admin knows it (its environment variable).
type Token struct {
	Name  string
	Value string
}

// WatchTokens makes every scan check when tokens expire and warn ahead of time.
func (a *App) WatchTokens(c TokenChecker, tokens []Token) {
	seen := map[string]bool{}
	for _, t := range tokens {
		if t.Value == "" || seen[t.Value] {
			continue
		}
		seen[t.Value] = true
		a.tokens = append(a.tokens, t)
	}
	a.tokenChecker = c
}

// tokenNotes sends at most one alert per threshold crossed, plus one if the token stops
// working; renewing it (a new expiry date) starts over.
func (a *App) tokenNotes(ctx context.Context, now time.Time) []note {
	a.mu.RLock()
	notified := make(map[string]string, len(a.tokens))
	for _, t := range a.tokens {
		notified[t.Name] = a.state.Notified[tokenMark+t.Name]
	}
	a.mu.RUnlock()

	var notes []note
	for _, t := range a.tokens {
		expires, ok, err := a.tokenChecker.Expiry(ctx, t.Value)
		var stage, body string
		priority := priorityReview
		switch {
		case errors.Is(err, tokencheck.ErrRejected):
			stage, body, priority = "rejected", fmt.Sprintf("%s no longer works: GitHub rejects it. Create a new one and update dimuda's .env.age.", t.Name), 5
		case err != nil:
			a.log.Warn("token expiry check failed", "token", t.Name, "err", err)
			continue
		case !ok:
			continue
		default:
			days := int(expires.Sub(now).Hours() / 24)
			for _, w := range tokenWarnings {
				if days < w {
					stage = fmt.Sprintf("%d|%s", w, expires.Format(time.DateOnly))
					body = fmt.Sprintf("%s expires on %s (%d days left). Create a new one and update dimuda's .env.age.", t.Name, expires.Local().Format("Mon 2 Jan 2006"), max(days, 0))
					if w == 1 {
						priority = 5
					}
					break
				}
			}
		}
		if stage == "" || notified[t.Name] == stage {
			continue
		}
		notes = append(notes, note{
			msg:    notify.Message{Title: t.Name + " needs renewal", Body: body, Click: a.publicURL, Tags: []string{"key"}, Priority: priority},
			marks:  map[string]string{tokenMark + t.Name: stage},
			always: true,
		})
	}
	return notes
}
