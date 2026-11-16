package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cravingPixels/dimuda/internal/gitsource"
	"github.com/cravingPixels/dimuda/internal/scanner"
	"github.com/cravingPixels/dimuda/internal/updater"
	"github.com/cravingPixels/dimuda/internal/versioning"
)

type Git interface {
	Dir(repo gitsource.Repo) string
	Commit(ctx context.Context, repo gitsource.Repo, files []string, message string, author gitsource.Author) error
	Push(ctx context.Context, repo gitsource.Repo) error
	Discard(ctx context.Context, repo gitsource.Repo) error
}

type Deployer interface {
	DeployChanged(ctx context.Context) error
	RunProcedure(ctx context.Context, name string) error
}

// AutoUpdate commits the updates the policy allows to the git sources the scanner reads.
type AutoUpdate struct {
	Policy   updater.Policy
	Git      Git
	Repos    []gitsource.Repo
	Author   gitsource.Author
	Deployer Deployer
	// ProcedureTimeout bounds how long a procedure may run before dimuda moves on.
	ProcedureTimeout time.Duration
	Now              func() time.Time
}

func (au *AutoUpdate) now() time.Time {
	if au.Now != nil {
		return au.Now()
	}
	return time.Now()
}

type outcome struct {
	decisions []updater.Decision
	applied   []updater.Decision
	failures  []string
}

// autoUpdate runs right after a scan, while the git checkouts still match what was scanned.
func (a *App) autoUpdate(ctx context.Context, report scanner.Report) outcome {
	au := a.auto
	now := au.now()

	a.mu.Lock()
	for _, img := range report.Images {
		if img.Update == nil {
			continue
		}
		if target := updater.Target(img.Key, *img.Update); a.state.FirstSeen[target].IsZero() {
			a.state.FirstSeen[target] = now
		}
	}
	firstSeen := copyTimes(a.state.FirstSeen)
	applied := copyTimes(a.state.Applied)
	a.mu.Unlock()

	var out outcome
	out.decisions = updater.Decide(report, au.Policy, firstSeen, applied, now)

	pushed := false
	changed := map[string]bool{}
	for _, repo := range au.Repos {
		committed, failures := a.commitRepo(ctx, repo, out.decisions)
		out.failures = append(out.failures, failures...)
		if len(committed) == 0 {
			continue
		}
		if err := au.Git.Push(ctx, repo); err != nil {
			out.failures = append(out.failures, fmt.Sprintf("push to %s failed, retrying next scan: %v", repo.Name, err))
			for _, i := range committed {
				out.decisions[i].Action, out.decisions[i].Reason = updater.ActionWait, "push failed; retrying next scan"
			}
			continue
		}
		pushed = true
		for _, i := range committed {
			out.decisions[i].Reason = "committed and pushed"
			out.applied = append(out.applied, out.decisions[i])
			for stack := range changesByStack(out.decisions[i], repo) {
				changed[stack] = true
			}
		}
	}

	if pushed && au.Deployer != nil {
		out.failures = append(out.failures, a.deploy(ctx, changed, procedures(report))...)
	}

	a.mu.Lock()
	for _, d := range out.applied {
		a.state.Applied[updater.Target(d.Key, d.Update)] = now
	}
	a.state.Decisions = out.decisions
	a.mu.Unlock()
	return out
}

// deploy runs the procedures of changed stacks first and waits for them, then lets Komodo
// redeploy everything else that changed; a stack its procedure already deployed is no longer
// "changed" by then, so it is not deployed twice.
func (a *App) deploy(ctx context.Context, changed map[string]bool, procs map[string]string) []string {
	au := a.auto
	var failures []string
	for _, stack := range sortedKeys(changed) {
		name, ok := procs[stack]
		if !ok {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, au.procedureTimeout())
		err := au.Deployer.RunProcedure(pctx, name)
		cancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("pushed %s, but its procedure %s failed: %v", stack, name, err))
		}
	}
	if err := au.Deployer.DeployChanged(ctx); err != nil {
		failures = append(failures, fmt.Sprintf("pushed, but asking Komodo to deploy failed: %v", err))
	}
	return failures
}

// procedures maps stack folders to the procedure named by a dimuda.komodo.procedure label on
// any of their services, e.g. gluetun, whose dependents share its network namespace. It is read
// from the compose files on every scan, so changing the label needs no restart.
func procedures(report scanner.Report) map[string]string {
	out := map[string]string{}
	for _, img := range report.Images {
		for _, u := range img.Usages {
			if u.Procedure != "" {
				out[stackDir(updater.Change{File: u.File, Stack: u.Stack})] = u.Procedure
			}
		}
	}
	return out
}

func (au *AutoUpdate) procedureTimeout() time.Duration {
	if au.ProcedureTimeout > 0 {
		return au.ProcedureTimeout
	}
	return 30 * time.Minute
}

// commitRepo applies and commits every allowed decision touching repo. A bump gets its own
// commit per stack so it can be reverted alone; pins, which change no version, are grouped
// into one commit per stack. It returns the indexes of committed decisions.
func (a *App) commitRepo(ctx context.Context, repo gitsource.Repo, decisions []updater.Decision) ([]int, []string) {
	var committed []int
	var failures []string
	pins := map[string][]int{}
	for i, d := range decisions {
		if d.Action != updater.ActionApply {
			continue
		}
		byStack := changesByStack(d, repo)
		if len(byStack) == 0 {
			continue
		}
		if d.Update.Kind == versioning.KindPin {
			for stack := range byStack {
				pins[stack] = append(pins[stack], i)
			}
			continue
		}
		var err error
		for _, stack := range sortedKeys(byStack) {
			if err = a.commitStack(ctx, repo, byStack[stack], commitMessage(stack, d)); err != nil {
				break
			}
		}
		if err != nil {
			failures = append(failures, a.fail(ctx, repo, decisions, []int{i}, err))
			continue
		}
		committed = append(committed, i)
	}

	pinned := map[int]bool{}
	failed := map[int]bool{}
	for _, stack := range sortedKeys(pins) {
		var changes []updater.Change
		var group []updater.Decision
		for _, i := range pins[stack] {
			changes = append(changes, changesByStack(decisions[i], repo)[stack]...)
			group = append(group, decisions[i])
		}
		if err := a.commitStack(ctx, repo, changes, pinMessage(stack, group)); err != nil {
			failures = append(failures, a.fail(ctx, repo, decisions, pins[stack], err))
			for _, i := range pins[stack] {
				failed[i] = true
			}
			continue
		}
		for _, i := range pins[stack] {
			pinned[i] = true
		}
	}
	for i := range pinned {
		if !failed[i] {
			committed = append(committed, i)
		}
	}
	sort.Ints(committed)
	return committed, failures
}

// changesByStack groups a decision's changes in repo by the stack folder they edit.
func changesByStack(d updater.Decision, repo gitsource.Repo) map[string][]updater.Change {
	out := map[string][]updater.Change{}
	for _, c := range d.Changes {
		if c.Repo == repo.Name {
			out[stackDir(c)] = append(out[stackDir(c)], c)
		}
	}
	return out
}

// stackDir names a stack by its folder, as the stacks repo does in commit messages, rather
// than by the compose "name:" (komodo's is "ix-komodo").
func stackDir(c updater.Change) string {
	if dir := filepath.Base(filepath.Dir(c.File)); dir != "." && dir != string(filepath.Separator) {
		return dir
	}
	return c.Stack
}

// commitStack applies changes and commits the files they touched. Services sharing one image
// line (hbbs and hbbr) produce the same edit twice; it is applied once.
func (a *App) commitStack(ctx context.Context, repo gitsource.Repo, changes []updater.Change, message string) error {
	root := a.auto.Git.Dir(repo)
	done := map[string]bool{}
	var files []string
	for _, c := range changes {
		key := c.File + "\x00" + c.Old
		if done[key] {
			continue
		}
		done[key] = true
		if err := updater.Apply(root, c); err != nil {
			return err
		}
		files = appendUnique(files, c.File)
	}
	return a.auto.Git.Commit(ctx, repo, files, message, a.auto.Author)
}

// fail discards the uncommitted edit and holds the decisions it belonged to.
func (a *App) fail(ctx context.Context, repo gitsource.Repo, decisions []updater.Decision, idx []int, err error) string {
	if discardErr := a.auto.Git.Discard(ctx, repo); discardErr != nil {
		err = fmt.Errorf("%w; discarding the edit also failed: %v", err, discardErr)
	}
	var images []string
	for _, i := range idx {
		decisions[i].Action, decisions[i].Reason = updater.ActionHold, "could not apply: "+err.Error()
		images = append(images, decisions[i].Image)
	}
	return fmt.Sprintf("%s: %v", strings.Join(images, ", "), err)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pinMessage summarises a stack's pins in the subject and lists each in the body.
func pinMessage(stack string, pins []updater.Decision) string {
	if len(pins) == 1 {
		return commitMessage(stack, pins[0])
	}
	lines := make([]string, 0, len(pins))
	for _, d := range pins {
		lines = append(lines, fmt.Sprintf("- %s %s → %s", d.Image, d.Update.FromTag, target(d)))
	}
	sort.Strings(lines)
	return fmt.Sprintf("%s: pin %d images to exact tags and digests\n\n%s", stack, len(pins), strings.Join(lines, "\n"))
}

// commitMessage follows the stacks repo convention, "<stack>: <what>", with the concrete
// versions so the git log answers "what was the bump".
func commitMessage(stack string, d updater.Decision) string {
	verb := "bump"
	if d.Update.Kind == versioning.KindPin {
		verb = "pin"
	}
	kind := string(d.Update.Kind)
	if d.Update.Security {
		kind += ", security"
	}
	return fmt.Sprintf("%s: %s %s %s → %s (%s)", stack, verb, d.Image, d.Update.FromTag, target(d), kind)
}

// target shows the digest only when the tag stays the same, since then it is the whole change.
func target(d updater.Decision) string {
	if d.Update.FromTag == d.Update.ToTag {
		return d.Update.ToTag + "@" + shortDigest(d.Update.ToDigest)
	}
	return d.Update.ToTag
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

func copyTimes(m map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
