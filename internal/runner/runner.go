// Package runner executes one gate run: worktree, base diff, scripts, rules,
// verdict, feedback. It is the implementation of MVP_PLAN.md § Gate run
// algorithm, and it modifies nothing outside the disposable worktree and the
// slopgate store.
package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/evermake/slopgate/internal/checker"
	"github.com/evermake/slopgate/internal/config"
	"github.com/evermake/slopgate/internal/feedback"
	"github.com/evermake/slopgate/internal/git"
	"github.com/evermake/slopgate/internal/model"
	"github.com/evermake/slopgate/internal/store"
)

// FetchTimeout bounds the base-branch fetch. Failure is non-fatal: slopgate
// fails open on freshness so it still works offline, and fails closed on
// everything else.
const FetchTimeout = 120 * time.Second

// Runner executes gate runs. One instance is shared by the daemon.
type Runner struct {
	Store   *store.Store
	Checker *checker.Checker

	// Log receives progress lines. The daemon points it at stdout.
	Log func(format string, args ...any)

	// Now and ScriptTimeoutOverride exist for tests.
	Now                   func() time.Time
	ScriptTimeoutOverride time.Duration
}

func New(s *store.Store, c *checker.Checker) *Runner {
	return &Runner{Store: s, Checker: c}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) since(t time.Time) int64 { return r.now().Sub(t).Milliseconds() }

func (r *Runner) scriptTimeout() time.Duration {
	if r.ScriptTimeoutOverride > 0 {
		return r.ScriptTimeoutOverride
	}
	return ScriptTimeout
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Execute runs the gate to completion and persists the terminal run record.
// It returns an error only for bugs in slopgate itself; an unhealthy repo or a
// failing check is reported through the run's state, not through this error.
func (r *Runner) Execute(ctx context.Context, repo model.Repo, run *model.Run) {
	started := r.now()
	run.State = model.StateRunning
	run.StartedAt = &started
	run.DefaultBranch = repo.DefaultBranch
	if err := r.Store.SaveRun(run); err != nil {
		r.logf("run %s: save: %v", run.SHA[:7], err)
	}

	// Rule paths are relative to the repo root, so they stay valid after the
	// worktree is removed and by the time feedback renders.
	rulePaths := map[string]string{}
	if err := r.execute(ctx, repo, run, rulePaths); err != nil {
		run.State = model.StateErrored
		run.Error = err.Error()
	}

	ended := r.now()
	run.EndedAt = &ended
	if !run.State.Terminal() {
		run.State = model.StateErrored
		run.Error = "run ended in a non-terminal state"
	}

	if err := r.writeFeedback(repo, run, rulePaths); err != nil {
		r.logf("run %s: feedback: %v", short(run.SHA), err)
	}
	if err := r.Store.SaveRun(run); err != nil {
		r.logf("run %s: save: %v", short(run.SHA), err)
	}
	r.logf("run %s: %s", short(run.SHA), strings.ToUpper(string(run.State)))
}

func (r *Runner) execute(ctx context.Context, repo model.Repo, run *model.Run, rulePaths map[string]string) error {
	// 1. Fresh worktree. No warm pool, no stale state; the cost is accepted.
	wt := r.Store.WorktreePath(repo.ID, run.ID)
	if err := git.AddWorktree(ctx, repo.GateRepo, wt, run.SHA); err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	defer func() {
		if err := git.RemoveWorktree(context.WithoutCancel(ctx), repo.GateRepo, wt); err != nil {
			r.logf("run %s: remove worktree: %v", short(run.SHA), err)
		}
	}()

	// 2. Fetch the base branch inside the worktree, reusing the developer's
	// existing git credentials via its origin. Failure is a warning.
	if err := git.Fetch(ctx, wt, "origin", repo.DefaultBranch, FetchTimeout); err != nil {
		run.FetchWarning = fmt.Sprintf("could not fetch origin/%s: %v (gated against the last known base)", repo.DefaultBranch, err)
		r.logf("run %s: %s", short(run.SHA), run.FetchWarning)
	}

	// 3. Diff against the merge-base, never against the tip of the default
	// branch: commits that landed on the default branch since this branch
	// started would otherwise appear as reversed changes and fire rules on
	// files nobody touched.
	base, err := r.resolveBase(ctx, wt, repo.DefaultBranch)
	if err != nil {
		return err
	}
	run.BaseSHA = base
	changed, err := git.ChangedFiles(ctx, wt, base, run.SHA)
	if err != nil {
		return fmt.Errorf("diff changed files: %w", err)
	}
	run.ChangedFiles = changed
	r.logf("run %s: %d changed file(s) against %s", short(run.SHA), len(changed), short(base))

	// 4. Gate-config drift. Recorded and reported; never changes the verdict.
	if drift, err := git.NameStatus(ctx, wt, base, run.SHA, ".slopgate/"); err == nil {
		for _, d := range drift {
			run.Drift = append(run.Drift, model.DriftEntry{Status: d.Status, Path: d.Path})
		}
	} else {
		run.Warnings = append(run.Warnings, "could not determine gate-config drift: "+err.Error())
	}

	if err := r.Store.SaveRun(run); err != nil {
		r.logf("run %s: save: %v", short(run.SHA), err)
	}

	changedPath, err := writeChangedFiles(r.Store.RunDir(repo.ID, run.SHA), changed)
	if err != nil {
		return fmt.Errorf("write changed-file list: %w", err)
	}
	env := scriptEnv{RunID: run.ID, SHA: run.SHA, BaseSHA: base, ChangedFilesPath: changedPath}
	run.Scripts = map[model.ScriptName]model.ScriptResult{}

	// 5. setup.sh. A failure here errors the run: slopgate could not do its
	// job, which is not a statement about the user's code.
	setup := r.runScript(ctx, model.ScriptSetup, wt, repo.ID, run.SHA, env)
	run.Scripts[model.ScriptSetup] = setup
	if !setup.OK() {
		return fmt.Errorf("setup.sh failed (exit %d); the gate could not run", setup.ExitCode)
	}

	// 6. Everything runs; nothing fails fast. Each round trip costs an agent
	// turn, so one verdict carrying every finding beats three sequential ones.
	rules, err := r.matchedRules(wt, changed)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		rulePaths[rule.Name] = filepath.Join(".slopgate", "rules", rule.Name+".md")
	}
	r.logf("run %s: %d rule(s) matched", short(run.SHA), len(rules))

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, name := range []model.ScriptName{model.ScriptCheck, model.ScriptTest} {
		wg.Add(1)
		go func(n model.ScriptName) {
			defer wg.Done()
			res := r.runScript(ctx, n, wt, repo.ID, run.SHA, env)
			mu.Lock()
			run.Scripts[n] = res
			mu.Unlock()
		}(name)
	}

	ruleResults, ruleErr := r.runRules(ctx, repo, run, wt, base, changed, rules)
	wg.Wait()

	run.Rules = ruleResults
	if ruleErr != nil {
		return ruleErr
	}

	// 8. A rule is a constraint, not a suggestion: any surviving finding fails
	// the gate. Config drift never changes the verdict.
	passed := run.Scripts[model.ScriptCheck].OK() && run.Scripts[model.ScriptTest].OK() && len(run.Findings()) == 0
	if passed {
		run.State = model.StatePassed
	} else {
		run.State = model.StateFailed
	}
	return nil
}

// resolveBase returns the merge-base with the default branch, preferring the
// freshly fetched remote-tracking ref and falling back to a local branch of the
// same name so an offline run still produces a sane diff.
func (r *Runner) resolveBase(ctx context.Context, wt, defaultBranch string) (string, error) {
	for _, ref := range []string{"origin/" + defaultBranch, defaultBranch} {
		if mb, err := git.MergeBase(ctx, wt, "HEAD", ref); err == nil && mb != "" {
			return mb, nil
		}
	}
	// A repo whose very first branch is being gated has no default branch to
	// merge-base against; the empty tree makes every file a changed file,
	// which is the correct reading of "all of this is new".
	return git.EmptyTreeSHA, nil
}

func (r *Runner) matchedRules(wt string, changed []string) ([]config.Rule, error) {
	all, err := config.LoadRules(filepath.Join(wt, ".slopgate", "rules"))
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}
	return config.MatchRules(all, changed), nil
}

// runRules fans out one agent per matched rule, bounded by maxConcurrentAgents.
//
// Deliberately not errgroup.WithContext: that cancels siblings on the first
// error, which contradicts "everything runs, nothing fails fast". Failures are
// collected and reduced at the end.
func (r *Runner) runRules(ctx context.Context, repo model.Repo, run *model.Run, wt, base string, changed []string, rules []config.Rule) ([]model.RuleResult, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	settings, err := config.LoadSettings(filepath.Join(wt, ".slopgate"))
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}

	diff, err := git.Diff(ctx, wt, base, run.SHA)
	if err != nil {
		return nil, fmt.Errorf("build diff: %w", err)
	}
	in := checker.Input{
		WorktreeDir:  wt,
		BaseSHA:      base,
		HeadSHA:      run.SHA,
		ChangedFiles: changed,
		Diff:         diff,
	}

	results := make([]model.RuleResult, len(rules))
	var g errgroup.Group
	g.SetLimit(settings.MaxConcurrentAgents)
	for i, rule := range rules {
		g.Go(func() error {
			r.logf("run %s: rule %s: checking", short(run.SHA), rule.Name)
			res, raw, err := r.Checker.Check(ctx, checker.Rule{Name: rule.Name, Body: rule.Body}, in)
			if len(raw) > 0 {
				if werr := r.Store.WriteRuleRaw(repo.ID, run.SHA, rule.Name, raw); werr != nil {
					r.logf("run %s: rule %s: persist raw: %v", short(run.SHA), rule.Name, werr)
				}
			}
			if err != nil {
				// Recorded, not returned: a sibling rule must still finish.
				res.Name = rule.Name
				res.Error = err.Error()
			}
			results[i] = res
			r.logf("run %s: rule %s: %d finding(s)", short(run.SHA), rule.Name, len(res.Findings))
			return nil
		})
	}
	_ = g.Wait()

	// Malformed or failed agent output fails the run. It is never read as
	// "no findings": a gate that silently passes on garbage is worse than no
	// gate at all.
	var failed []string
	for _, res := range results {
		if res.Error != "" {
			failed = append(failed, fmt.Sprintf("%s: %s", res.Name, res.Error))
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return results, fmt.Errorf("rule evaluation failed:\n  %s", strings.Join(failed, "\n  "))
	}
	return results, nil
}

// writeFeedback persists the artifact when the run failed or the gate config
// drifted. A passed run that quietly dropped a rule must still leave a record.
func (r *Runner) writeFeedback(repo model.Repo, run *model.Run, rulePaths map[string]string) error {
	// ShouldWrite covers failed-or-drifted. An errored run is added here: the
	// agent still needs to know why the gate could not complete, and a failed
	// setup.sh has a log tail that is exactly that diagnostic.
	if !feedback.ShouldWrite(run) && run.State != model.StateErrored {
		return nil
	}
	tails := map[model.ScriptName]string{}
	for name, res := range run.Scripts {
		if res.Skipped || res.OK() {
			continue
		}
		if t, err := r.Store.TailLog(repo.ID, run.SHA, name, 100); err == nil {
			tails[name] = t
		}
	}
	out := feedback.Render(feedback.Input{Run: run, RulePaths: rulePaths, LogTail: tails})
	path, err := r.Store.WriteFeedback(repo.ID, run.SHA, out)
	if err != nil {
		return err
	}
	run.FeedbackPath = path
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
