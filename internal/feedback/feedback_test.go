package feedback

import (
	"strings"
	"testing"
	"time"

	"github.com/evermake/slopgate/internal/model"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return tm
}

func baseRun(t *testing.T, state model.RunState) *model.Run {
	t.Helper()
	ended := mustTime(t, "2026-09-13T14:22:00Z")
	return &model.Run{
		ID:            "01JB2X0000000000000000",
		RepoID:        "repo1",
		SHA:           "a3f9c21ffffffffffffffffffffffffffffffff",
		Ref:           "refs/heads/feat/auth",
		Branch:        "feat/auth",
		State:         state,
		BaseSHA:       "d4e5f6a0000000000000000000000000000000",
		DefaultBranch: "main",
		QueuedAt:      ended.Add(-time.Minute),
		EndedAt:       &ended,
	}
}

func TestShouldWrite(t *testing.T) {
	tests := []struct {
		name string
		run  *model.Run
		want bool
	}{
		{"nil run", nil, false},
		{"passed no drift", &model.Run{State: model.StatePassed}, false},
		{"failed no drift", &model.Run{State: model.StateFailed}, true},
		{"errored no drift", &model.Run{State: model.StateErrored}, false},
		{"cancelled no drift", &model.Run{State: model.StateCancelled}, false},
		{"passed with drift", &model.Run{State: model.StatePassed, Drift: []model.DriftEntry{{Status: "M", Path: ".slopgate/rules/x.md"}}}, true},
		{"failed with drift", &model.Run{State: model.StateFailed, Drift: []model.DriftEntry{{Status: "D", Path: ".slopgate/rules/x.md"}}}, true},
		{"errored with drift", &model.Run{State: model.StateErrored, Drift: []model.DriftEntry{{Status: "A", Path: ".slopgate/rules/x.md"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldWrite(tt.run); got != tt.want {
				t.Errorf("ShouldWrite() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRender_FailedWithFindings(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Rules = []model.RuleResult{
		{
			Name: "no-type-casts",
			Findings: []model.Finding{
				{Rule: "no-type-casts", File: "src/api.ts", Line: 42, Snippet: "const user = data as User", Explanation: "`data` is `unknown` here."},
				{Rule: "no-type-casts", File: "src/api.ts", Line: 88, Snippet: "const x = y as Z", Explanation: "Second violation."},
			},
		},
	}
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 1},
		model.ScriptTest:  {Name: model.ScriptTest, ExitCode: 0},
	}

	in := Input{
		Run: run,
		RulePaths: map[string]string{
			"no-type-casts": ".slopgate/rules/no-type-casts.md",
		},
		LogTail: map[model.ScriptName]string{
			model.ScriptCheck: "npm run check\nerror TS2345: ...\n",
		},
	}

	out := Render(in)

	if !ShouldWrite(run) {
		t.Fatal("ShouldWrite() = false, want true for failed run")
	}

	mustContain(t, out, "# slopgate: FAILED")
	mustContain(t, out, "Commit `a3f9c21` on `feat/auth`")
	mustContain(t, out, "base `origin/main` @ `d4e5f6a`")
	mustContain(t, out, "2026-09-13 14:22 UTC")
	mustContain(t, out, "## Summary")
	mustContain(t, out, "- 2 rule violations in 1 rule")
	mustContain(t, out, "- `check.sh` — FAILED")
	mustContain(t, out, "- `test.sh` — passed")
	mustContain(t, out, "## Rule violations")
	mustContain(t, out, "### no-type-casts")
	mustContain(t, out, "Rule: `.slopgate/rules/no-type-casts.md`")
	mustContain(t, out, "**`src/api.ts:42`**")
	mustContain(t, out, "```ts\nconst user = data as User\n```")
	mustContain(t, out, "`data` is `unknown` here.")
	mustContain(t, out, "---")
	mustContain(t, out, "**`src/api.ts:88`**")
	mustContain(t, out, "## `check.sh` — exit 1")
	mustContain(t, out, "npm run check\nerror TS2345: ...")

	// test.sh passed, so no failure section for it.
	if strings.Contains(out, "## `test.sh` — exit") {
		t.Error("did not expect a test.sh failure section")
	}
	// setup.sh never ran (absent from Scripts), so no summary line or section.
	if strings.Contains(out, "setup.sh") {
		t.Error("did not expect any setup.sh mention when it never ran")
	}

	// Findings must appear in stable order: 42 before 88.
	if strings.Index(out, "src/api.ts:42") > strings.Index(out, "src/api.ts:88") {
		t.Error("findings out of order")
	}
}

func TestRender_PassedWithDrift(t *testing.T) {
	run := baseRun(t, model.StatePassed)
	run.Drift = []model.DriftEntry{
		{Status: "M", Path: ".slopgate/scripts/check.sh"},
		{Status: "D", Path: ".slopgate/rules/no-any.md"},
	}
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 0},
		model.ScriptTest:  {Name: model.ScriptTest, ExitCode: 0},
	}

	in := Input{Run: run}
	out := Render(in)

	if !ShouldWrite(run) {
		t.Fatal("ShouldWrite() = false, want true for passed-with-drift run")
	}

	mustContain(t, out, "# slopgate: PASSED")
	mustContain(t, out, "## ⚠ Gate config changed on this branch")
	mustContain(t, out, "    M  .slopgate/scripts/check.sh")
	mustContain(t, out, "    D  .slopgate/rules/no-any.md")
	mustContain(t, out, "This verdict was computed with the branch's `.slopgate/`, not the default branch's.")
	mustContain(t, out, "- 0 rule violations")

	// The drift block must appear before the Summary section, and before
	// everything else in the document body (right after the header).
	driftIdx := strings.Index(out, "## ⚠ Gate config changed")
	summaryIdx := strings.Index(out, "## Summary")
	if driftIdx == -1 || summaryIdx == -1 {
		t.Fatalf("expected both drift block and summary, got:\n%s", out)
	}
	if driftIdx > summaryIdx {
		t.Error("drift block must appear before the summary")
	}
	titleIdx := strings.Index(out, "# slopgate: PASSED")
	if titleIdx > driftIdx {
		t.Error("title must precede the drift block")
	}

	// No rule violations section, since there are no findings.
	if strings.Contains(out, "## Rule violations") {
		t.Error("did not expect a Rule violations section on a clean passed run")
	}
}

func TestRender_FailedWithDrift(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Drift = []model.DriftEntry{{Status: "M", Path: ".slopgate/rules/x.md"}}
	run.Rules = []model.RuleResult{
		{Name: "x", Findings: []model.Finding{{Rule: "x", File: "a.go", Line: 1, Snippet: "foo()", Explanation: "bad"}}},
	}

	out := Render(Input{Run: run})

	if !ShouldWrite(run) {
		t.Fatal("ShouldWrite() = false, want true")
	}
	mustContain(t, out, "# slopgate: FAILED")
	mustContain(t, out, "## ⚠ Gate config changed on this branch")

	driftIdx := strings.Index(out, "## ⚠ Gate config changed")
	summaryIdx := strings.Index(out, "## Summary")
	if driftIdx > summaryIdx {
		t.Error("drift block must appear before the summary on a failed run too")
	}
}

func TestRender_ScriptFailureWithLogTail(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 1},
	}
	in := Input{
		Run: run,
		LogTail: map[model.ScriptName]string{
			model.ScriptCheck: "line one\nline two\nFAILED\n",
		},
	}
	out := Render(in)

	mustContain(t, out, "## `check.sh` — exit 1")
	mustContain(t, out, "```\nline one\nline two\nFAILED\n```")
}

func TestRender_SkippedScripts(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Rules = []model.RuleResult{
		{Name: "r", Findings: []model.Finding{{Rule: "r", File: "a.go", Line: 1, Snippet: "x", Explanation: "e"}}},
	}
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptSetup: {Name: model.ScriptSetup, Skipped: true},
		model.ScriptCheck: {Name: model.ScriptCheck, Skipped: true},
		model.ScriptTest:  {Name: model.ScriptTest, ExitCode: 0},
	}
	out := Render(Input{Run: run})

	mustContain(t, out, "- `setup.sh` — not configured")
	mustContain(t, out, "- `check.sh` — not configured")
	mustContain(t, out, "- `test.sh` — passed")
	if strings.Contains(out, "check.sh` — passed") {
		t.Error("a skipped script must never claim to pass")
	}
}

func TestRender_ErroredRun(t *testing.T) {
	run := baseRun(t, model.StateErrored)
	run.Error = "setup.sh: worktree creation failed: exit status 128"
	run.Rules = []model.RuleResult{
		{Name: "r", Findings: []model.Finding{{Rule: "r", File: "a.go", Line: 1, Snippet: "x", Explanation: "e"}}},
	}
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 1},
	}

	out := Render(Input{Run: run, LogTail: map[model.ScriptName]string{model.ScriptCheck: "boom"}})

	mustContain(t, out, "# slopgate: ERRORED")
	mustContain(t, out, "slopgate could not complete this run. This is not a statement about your code.")
	mustContain(t, out, "setup.sh: worktree creation failed: exit status 128")

	// An errored run does not claim a verdict on the code: no Summary or
	// Rule violations section, even though a rule happened to report a
	// finding before another rule errored the whole run.
	if strings.Contains(out, "## Summary") {
		t.Error("errored run must not render a Summary section")
	}
	if strings.Contains(out, "## Rule violations") {
		t.Error("errored run must not render a Rule violations section")
	}

	// But a script failure that ran before the error is still useful
	// diagnostic context and should still be shown.
	mustContain(t, out, "## `check.sh` — exit 1")
	mustContain(t, out, "boom")
}

func TestRender_ErroredRunWithWarnings(t *testing.T) {
	run := baseRun(t, model.StateErrored)
	run.Error = "boom"
	run.FetchWarning = "fetch origin main: timed out"
	run.Warnings = []string{"branch built on a local default branch ahead of origin/main"}

	out := Render(Input{Run: run})

	mustContain(t, out, "## Warnings")
	mustContain(t, out, "**Base fetch failed:** fetch origin main: timed out")
	mustContain(t, out, "stale base")
	mustContain(t, out, "branch built on a local default branch ahead of origin/main")
}

func TestRender_SnippetWithBacktickFence(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	snippet := "const s = `template ${x}`\n```\nnested fence in a string\n```"
	run.Rules = []model.RuleResult{
		{Name: "no-templates", Findings: []model.Finding{
			{Rule: "no-templates", File: "src/x.ts", Line: 5, Snippet: snippet, Explanation: "uses a template literal"},
		}},
	}

	out := Render(Input{Run: run})

	// The fence used to wrap the snippet must be longer than the longest
	// run of backticks inside it (3), so at least 4 backticks.
	if !strings.Contains(out, "````ts\n") {
		t.Errorf("expected a widened 4-backtick fence around a snippet containing a ``` run, got:\n%s", out)
	}
	// The whole document must still contain the raw snippet content intact.
	mustContain(t, out, "nested fence in a string")
	mustContain(t, out, "const s = `template ${x}`")

	// Sanity: the document must not be shredded — the explanation that
	// follows the snippet must still be present and not swallowed into the
	// code block.
	mustContain(t, out, "uses a template literal")
}

func TestRender_MissingRulePath(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Rules = []model.RuleResult{
		{Name: "no-path-rule", Findings: []model.Finding{
			{Rule: "no-path-rule", File: "a.go", Line: 1, Snippet: "x", Explanation: "e"},
		}},
	}
	// in.RulePaths deliberately has no entry for "no-path-rule".
	out := Render(Input{Run: run, RulePaths: map[string]string{"other-rule": ".slopgate/rules/other-rule.md"}})

	mustContain(t, out, "### no-path-rule")
	if strings.Contains(out, "Rule: `") {
		t.Error("must not render a rule-path line when the rule's path is missing")
	}
}

func TestRender_EmptyFindings(t *testing.T) {
	run := baseRun(t, model.StateFailed)
	run.Rules = []model.RuleResult{
		{Name: "clean-rule", Findings: nil},
	}
	run.Scripts = map[model.ScriptName]model.ScriptResult{
		model.ScriptCheck: {Name: model.ScriptCheck, ExitCode: 1},
	}
	out := Render(Input{Run: run})

	mustContain(t, out, "- 0 rule violations")
	if strings.Contains(out, "## Rule violations") {
		t.Error("did not expect a Rule violations section when no rule has findings")
	}
	if strings.Contains(out, "clean-rule") {
		t.Error("a rule with zero findings should not be mentioned in the body")
	}
}

func mustContain(t *testing.T, doc, substr string) {
	t.Helper()
	if !strings.Contains(doc, substr) {
		t.Errorf("expected document to contain %q, got:\n%s", substr, doc)
	}
}
