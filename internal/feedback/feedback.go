// Package feedback renders a model.Run into the Markdown feedback.md artifact
// described in MVP_PLAN.md § feedback.md format. It is a pure rendering
// package: no file I/O, no exec, no clock reads. Every timestamp comes from
// the Run passed in.
package feedback

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/evermake/slopgate/internal/model"
)

// Input bundles a Run with the two pieces of context the renderer cannot
// derive from the Run alone: rule prose (for the per-rule blockquote) and
// already-tailed script log output.
type Input struct {
	Run       *model.Run
	RuleProse map[string]string           // rule name -> markdown body, for the blockquote
	LogTail   map[model.ScriptName]string // already-tailed script output
}

// ShouldWrite reports whether a run's verdict warrants a feedback.md
// artifact: the run failed outright, or gate config drifted relative to the
// merge-base. A PASSED run that quietly dropped a rule must still leave an
// artifact saying so.
func ShouldWrite(run *model.Run) bool {
	if run == nil {
		return false
	}
	return run.State == model.StateFailed || len(run.Drift) > 0
}

// Render produces the full Markdown document for a run.
func Render(in Input) string {
	run := in.Run
	if run == nil {
		return ""
	}

	var sections []string

	title := fmt.Sprintf("# slopgate: %s", strings.ToUpper(string(run.State)))
	sections = append(sections, title+"\n\n"+headerLine(run))

	if len(run.Drift) > 0 {
		sections = append(sections, renderDrift(run))
	}

	errored := run.State == model.StateErrored
	if errored {
		sections = append(sections, renderError(run))
	}

	if w := renderWarnings(run); w != "" {
		sections = append(sections, w)
	}

	if !errored {
		sections = append(sections, renderSummary(run))

		if findings := run.Findings(); len(findings) > 0 {
			sections = append(sections, renderRuleViolations(run, in.RuleProse))
		}
	}

	for _, name := range []model.ScriptName{model.ScriptSetup, model.ScriptCheck, model.ScriptTest} {
		r, ok := run.Scripts[name]
		if !ok || r.OK() {
			continue
		}
		sections = append(sections, renderScriptFailure(name, r, in.LogTail[name]))
	}

	return strings.Join(sections, "\n\n") + "\n"
}

// headerLine renders the commit/branch/base/time identification line.
func headerLine(run *model.Run) string {
	parts := []string{fmt.Sprintf("Commit `%s` on `%s`", shortSHA(run.SHA), run.Branch)}
	if run.DefaultBranch != "" && run.BaseSHA != "" {
		parts = append(parts, fmt.Sprintf("base `origin/%s` @ `%s`", run.DefaultBranch, shortSHA(run.BaseSHA)))
	}
	if run.EndedAt != nil {
		parts = append(parts, run.EndedAt.UTC().Format("2006-01-02 15:04")+" UTC")
	}
	return strings.Join(parts, " · ")
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// renderDrift renders the gate-config-drift block, exactly as specified in
// MVP_PLAN.md § feedback.md format. This block is placed first in the
// document (above the summary) by the caller, on passed and failed runs
// alike.
func renderDrift(run *model.Run) string {
	var b strings.Builder
	b.WriteString("## ⚠ Gate config changed on this branch\n\n")
	for _, d := range run.Drift {
		fmt.Fprintf(&b, "    %s  %s\n", d.Status, d.Path)
	}
	b.WriteString("\nThis verdict was computed with the branch's `.slopgate/`, not the default branch's.")
	return b.String()
}

// renderError renders the explanation block for an errored run: slopgate
// itself could not complete, which is never a statement about the user's
// code.
func renderError(run *model.Run) string {
	explanation := "slopgate could not complete this run. This is not a statement about your code."
	errText := run.Error
	if strings.TrimSpace(errText) == "" {
		errText = "(no error message recorded)"
	}
	content := strings.TrimRight(errText, "\n")
	fence := codeFence(content)
	body := fence + "\n" + content + "\n" + fence
	return strings.Join([]string{"## Error", explanation, body}, "\n\n")
}

// renderWarnings surfaces non-fatal problems the run hit along the way: a
// failed (but non-fatal) base fetch means this verdict may be stale.
func renderWarnings(run *model.Run) string {
	var lines []string
	if run.FetchWarning != "" {
		lines = append(lines, fmt.Sprintf(
			"- **Base fetch failed:** %s — this verdict may be computed against a stale base.",
			run.FetchWarning,
		))
	}
	for _, w := range run.Warnings {
		lines = append(lines, "- "+w)
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Warnings\n\n" + strings.Join(lines, "\n")
}

// renderSummary renders the finding/rule counts and one line per script that
// ran, with its status. A skipped script says "not configured" rather than
// silently claiming to pass.
func renderSummary(run *model.Run) string {
	lines := []string{summaryCountLine(run)}

	for _, name := range []model.ScriptName{model.ScriptSetup, model.ScriptCheck, model.ScriptTest} {
		r, ok := run.Scripts[name]
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("- `%s.sh` — %s", name, scriptStatus(r)))
	}

	return "## Summary\n\n" + strings.Join(lines, "\n")
}

func summaryCountLine(run *model.Run) string {
	findings := run.Findings()
	if len(findings) == 0 {
		return "- 0 rule violations"
	}
	ruleCount := 0
	for _, rr := range run.Rules {
		if len(rr.Findings) > 0 {
			ruleCount++
		}
	}
	return fmt.Sprintf("- %s in %s", pluralize(len(findings), "rule violation"), pluralize(ruleCount, "rule"))
}

func scriptStatus(r model.ScriptResult) string {
	switch {
	case r.Skipped:
		return "not configured"
	case r.OK():
		return "passed"
	case r.TimedOut:
		return "FAILED (timed out)"
	default:
		return "FAILED"
	}
}

func pluralize(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// renderRuleViolations renders the "## Rule violations" section: one
// "### <rule>" subsection per rule that has surviving findings, in the
// stable order rules appear in run.Rules, each finding in its existing
// order.
func renderRuleViolations(run *model.Run, ruleProse map[string]string) string {
	var ruleSections []string
	for _, rr := range run.Rules {
		if len(rr.Findings) == 0 {
			continue
		}
		ruleSections = append(ruleSections, renderRuleSection(rr, ruleProse[rr.Name]))
	}
	return "## Rule violations\n\n" + strings.Join(ruleSections, "\n\n")
}

func renderRuleSection(rr model.RuleResult, prose string) string {
	blocks := []string{"### " + rr.Name}

	if strings.TrimSpace(prose) != "" {
		blocks = append(blocks, blockquote(prose))
	}

	findingBlocks := make([]string, 0, len(rr.Findings))
	for _, f := range rr.Findings {
		findingBlocks = append(findingBlocks, renderFinding(f))
	}
	blocks = append(blocks, strings.Join(findingBlocks, "\n\n---\n\n"))

	return strings.Join(blocks, "\n\n")
}

func blockquote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

// renderFinding renders one finding: a bold file:line reference, the
// snippet in a fenced code block (language inferred from the file
// extension, fence widened to survive a snippet that itself contains
// backtick fences), then the explanation.
func renderFinding(f model.Finding) string {
	lang := langForFile(f.File)
	snippet := strings.TrimRight(f.Snippet, "\n")
	fence := codeFence(snippet)
	codeBlock := fence + lang + "\n" + snippet + "\n" + fence

	header := fmt.Sprintf("**`%s:%d`**", f.File, f.Line)

	return strings.Join([]string{header, codeBlock, f.Explanation}, "\n\n")
}

// renderScriptFailure renders the log excerpt section for one failing
// script, stating its exit code (and timeout/error detail, if any).
func renderScriptFailure(name model.ScriptName, r model.ScriptResult, logTail string) string {
	heading := fmt.Sprintf("## `%s.sh` — %s", name, scriptFailureDetail(r))

	trimmed := strings.TrimSpace(logTail)
	var body string
	if trimmed == "" {
		body = "*(no log captured)*"
	} else {
		content := strings.TrimRight(logTail, "\n")
		fence := codeFence(content)
		body = fence + "\n" + content + "\n" + fence
	}

	return heading + "\n\n" + body
}

func scriptFailureDetail(r model.ScriptResult) string {
	detail := fmt.Sprintf("exit %d", r.ExitCode)
	if r.TimedOut {
		detail += " (timed out)"
	}
	if r.Error != "" {
		detail += fmt.Sprintf(" (error: %s)", r.Error)
	}
	return detail
}

// codeFence returns a backtick fence long enough to survive a content body
// that itself contains backtick runs: one longer than the longest run of
// consecutive backticks in content, minimum three.
func codeFence(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// langForFile infers a fenced-code-block language tag from a file's
// extension, falling back to no language (empty string) for anything
// unrecognized.
func langForFile(file string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(file), "."))
	return extLang[ext]
}

var extLang = map[string]string{
	"ts":       "ts",
	"tsx":      "tsx",
	"go":       "go",
	"py":       "py",
	"rs":       "rs",
	"js":       "js",
	"jsx":      "jsx",
	"mjs":      "js",
	"cjs":      "js",
	"json":     "json",
	"json5":    "json",
	"sh":       "sh",
	"bash":     "sh",
	"zsh":      "sh",
	"md":       "md",
	"markdown": "md",
	"yaml":     "yaml",
	"yml":      "yaml",
	"toml":     "toml",
	"java":     "java",
	"c":        "c",
	"h":        "c",
	"cpp":      "cpp",
	"cc":       "cpp",
	"hpp":      "cpp",
	"rb":       "rb",
	"php":      "php",
	"css":      "css",
	"scss":     "scss",
	"html":     "html",
	"htm":      "html",
	"sql":      "sql",
	"kt":       "kotlin",
	"kts":      "kotlin",
	"swift":    "swift",
	"proto":    "proto",
	"graphql":  "graphql",
	"vue":      "vue",
	"svelte":   "svelte",
	"lua":      "lua",
	"xml":      "xml",
}
