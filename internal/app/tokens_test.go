package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cravingPixels/dimuda/internal/tokencheck"
)

type fakeChecker struct {
	expires time.Time
	err     error
}

func (f *fakeChecker) Expiry(context.Context, string) (time.Time, bool, error) {
	return f.expires, !f.expires.IsZero(), f.err
}

func TestTokenWarnings(t *testing.T) {
	s := &fakeScanner{report: reportWith()}
	n := &fakeNotifier{}
	a, _ := New(s, n, t.TempDir(), "", quiet)
	c := &fakeChecker{expires: time.Now().Add(90 * 24 * time.Hour)}
	a.WatchTokens(c, []Token{{Name: "GITHUB_TOKEN", Value: "pat"}, {Name: "SAME_VALUE", Value: "pat"}, {Name: "UNSET"}})

	a.RunScan(context.Background())
	if len(n.sent) != 0 {
		t.Fatalf("90 days left must not warn: %+v", n.sent)
	}

	c.expires = time.Now().Add(20 * 24 * time.Hour)
	a.RunScan(context.Background())
	a.RunScan(context.Background())
	if len(n.sent) != 1 || n.sent[0].Title != "GITHUB_TOKEN needs renewal" || !strings.Contains(n.sent[0].Body, "19 days left") {
		t.Fatalf("30-day warning once (and once for a token shared by two variables): %+v", n.sent)
	}

	c.expires = time.Now().Add(5 * 24 * time.Hour)
	a.RunScan(context.Background())
	if len(n.sent) != 2 || n.sent[1].Priority != priorityReview {
		t.Fatalf("7-day warning: %+v", n.sent)
	}

	c.expires = time.Time{}
	c.err = tokencheck.ErrRejected
	a.RunScan(context.Background())
	if len(n.sent) != 3 || n.sent[2].Priority != 5 || !strings.Contains(n.sent[2].Body, "no longer works") {
		t.Fatalf("rejected token: %+v", n.sent)
	}

	c.err = nil
	c.expires = time.Now().Add(365 * 24 * time.Hour)
	a.RunScan(context.Background())
	c.expires = time.Now().Add(10 * 24 * time.Hour)
	a.RunScan(context.Background())
	if len(n.sent) != 4 {
		t.Errorf("a renewed token must warn again when its new date approaches: %+v", n.sent)
	}
}
