// Package model holds the types shared across slopgate's packages. It is the
// contract every other package builds against: nothing here imports anything
// else in this repo, and nothing here executes anything.
package model

import (
	"encoding/json"
	"time"
)

// RunState is the lifecycle of a gate run: queued -> running -> terminal.
type RunState string

const (
	StateQueued    RunState = "queued"
	StateRunning   RunState = "running"
	StatePassed    RunState = "passed"
	StateFailed    RunState = "failed"
	StateErrored   RunState = "errored"
	StateCancelled RunState = "cancelled"
)

// Terminal reports whether the run has reached a state it will never leave.
func (s RunState) Terminal() bool {
	switch s {
	case StatePassed, StateFailed, StateErrored, StateCancelled:
		return true
	}
	return false
}

// Repo is one registered repository, as stored in repos.json.
type Repo struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	GateRepo      string `json:"gate_repo"`
	DefaultBranch string `json:"default_branch"`
	UpstreamURL   string `json:"upstream_url"`
}

// Finding is a single rule violation with a citation that has been verified
// against the worktree. Rule is stamped by slopgate, never asked of the model.
type Finding struct {
	Rule        string `json:"rule"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Snippet     string `json:"snippet"`
	Explanation string `json:"explanation"`
}

// DiscardReason explains why a finding failed citation validation.
type DiscardReason string

const (
	DiscardNotChanged  DiscardReason = "file_not_in_changed_set"
	DiscardMissingFile DiscardReason = "file_not_found"
	DiscardBadSnippet  DiscardReason = "snippet_not_at_line"
)

// DiscardedFinding is a finding that did not survive validation. Kept so a
// rule that discards most of what it reports is visible as a signal.
type DiscardedFinding struct {
	Finding
	Reason DiscardReason `json:"reason"`
}

// ScriptName identifies one of the three repo-supplied scripts.
type ScriptName string

const (
	ScriptSetup ScriptName = "setup"
	ScriptCheck ScriptName = "check"
	ScriptTest  ScriptName = "test"
)

// ScriptResult records one script invocation. A missing script is Skipped and
// counts as passing: absence is not an error.
type ScriptResult struct {
	Name       ScriptName `json:"name"`
	Skipped    bool       `json:"skipped"`
	ExitCode   int        `json:"exit_code"`
	TimedOut   bool       `json:"timed_out"`
	DurationMS int64      `json:"duration_ms"`
	LogPath    string     `json:"log_path"`
	Error      string     `json:"error,omitempty"`
}

// OK reports whether the script counts as passing for verdict purposes.
func (r ScriptResult) OK() bool {
	return r.Skipped || (r.ExitCode == 0 && !r.TimedOut && r.Error == "")
}

// RuleResult is the outcome of evaluating one matched rule.
type RuleResult struct {
	Name       string             `json:"name"`
	Findings   []Finding          `json:"findings"`
	Discarded  []DiscardedFinding `json:"discarded,omitempty"`
	DurationMS int64              `json:"duration_ms"`
	// Error is set when the agent could not be evaluated at all (non-zero
	// exit, unparseable envelope, missing structured_output). A non-empty
	// Error errors the whole run; it is never read as "no findings".
	Error string          `json:"error,omitempty"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

// DriftEntry is one changed path under .slopgate/ relative to the merge-base.
// Status is a git --name-status letter (A, M, D, R...).
type DriftEntry struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// Run is the durable record of one gate run, persisted as run.json.
type Run struct {
	ID     string   `json:"id"`
	RepoID string   `json:"repo_id"`
	SHA    string   `json:"sha"`
	Ref    string   `json:"ref"`
	Branch string   `json:"branch"`
	State  RunState `json:"state"`
	// Error is set only for StateErrored: slopgate could not complete. It is
	// never a statement about the user's code.
	Error string `json:"error,omitempty"`

	BaseSHA       string `json:"base_sha,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	// FetchWarning records a non-fatal base-branch fetch failure.
	FetchWarning string `json:"fetch_warning,omitempty"`

	ChangedFiles []string     `json:"changed_files,omitempty"`
	Drift        []DriftEntry `json:"drift,omitempty"`

	Scripts map[ScriptName]ScriptResult `json:"scripts,omitempty"`
	Rules   []RuleResult                `json:"rules,omitempty"`

	Warnings []string `json:"warnings,omitempty"`

	QueuedAt  time.Time  `json:"queued_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`

	FeedbackPath string `json:"feedback_path,omitempty"`
}

// Findings flattens the surviving findings across all rules.
func (r *Run) Findings() []Finding {
	var out []Finding
	for _, rr := range r.Rules {
		out = append(out, rr.Findings...)
	}
	return out
}
