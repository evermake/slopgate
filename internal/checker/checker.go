// Package checker evaluates one project rule against one change by running a
// single `claude -p` agent and then deterministically verifying every finding it
// reports against the worktree on disk.
//
// Two properties are load-bearing and must survive any future edit:
//
//   - The agent gets read-only tools. See buildArgs.
//   - A failed invocation is an error, never an empty result. A gate that
//     silently passes on garbage is worse than no gate.
package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/evermake/slopgate/internal/model"
)

// Rule is the minimum a check needs: a name to stamp on findings and the prose
// to enforce. Deliberately decoupled from config.Rule — the checker has no
// business knowing about globs or file paths.
type Rule struct {
	Name string
	Body string
}

// Input is the change under review.
type Input struct {
	WorktreeDir  string
	BaseSHA      string
	HeadSHA      string
	ChangedFiles []string
	Diff         string
}

// Checker runs rule-check agents. The zero value is usable; New fills in the
// defaults.
type Checker struct {
	Bin     string        // default "claude"
	Model   string        // optional --model
	Timeout time.Duration // default 10m
}

const (
	// DefaultBin is the agent binary, resolved through PATH.
	DefaultBin = "claude"
	// DefaultTimeout bounds a single rule-check agent (MVP_PLAN.md § Defaults).
	DefaultTimeout = 10 * time.Minute

	// maxStderrInError caps how much of the agent's stderr is quoted back in an
	// error, keeping the tail: the useful part of a failure is at the end.
	maxStderrInError = 2000
)

// New returns a Checker with the documented defaults.
func New() *Checker {
	return &Checker{Bin: DefaultBin, Timeout: DefaultTimeout}
}

// Check runs the agent for one rule, with cwd set to in.WorktreeDir, and
// validates the findings it reports.
//
// The returned []byte is the raw JSON envelope the agent printed, for the caller
// to persist verbatim; it is returned on the error paths too, when there is
// anything to persist.
//
// A non-nil error means the rule could not be evaluated at all — non-zero exit,
// unparseable envelope, or absent/null structured_output. The caller must error
// the whole run. It is NEVER interpreted as "no findings": a hallucinating or
// crashing agent must not be able to turn a red gate green. The same message is
// mirrored into RuleResult.Error so a persisted run explains itself.
func (c *Checker) Check(ctx context.Context, rule Rule, in Input) (model.RuleResult, []byte, error) {
	started := time.Now()
	res := model.RuleResult{Name: rule.Name}

	fail := func(raw []byte, err error) (model.RuleResult, []byte, error) {
		res.DurationMS = time.Since(started).Milliseconds()
		res.Error = err.Error()
		return res, raw, err
	}

	stdout, stderr, runErr := c.run(ctx, in.WorktreeDir, buildArgs(c.Model, BuildPrompt(rule, in)))
	if runErr != nil {
		return fail(stdout, fmt.Errorf("rule %q: %w%s", rule.Name, runErr, stderrSuffix(stderr)))
	}

	findings, usage, err := parseEnvelope(stdout)
	if err != nil {
		return fail(stdout, fmt.Errorf("rule %q: %w%s", rule.Name, err, stderrSuffix(stderr)))
	}

	kept, discarded := ValidateFindings(rule.Name, findings, in.WorktreeDir, in.ChangedFiles)
	res.Findings = kept
	res.Discarded = discarded
	res.Usage = usage
	res.DurationMS = time.Since(started).Milliseconds()
	return res, stdout, nil
}

// buildArgs assembles the `claude -p` command line.
//
// DO NOT ADD A WRITE OR EXEC TOOL HERE. --allowedTools "Read,Glob,Grep" (no
// Bash, no Edit, no Write) together with --permission-mode dontAsk is not
// hygiene, it is the thing that makes the verdict mean anything: an agentic
// checker that can modify the worktree can delete the violating line and then
// *honestly* report no violations. The worktree is disposable, so the developer
// loses nothing — but the gate silently passes and defeats itself. If a future
// rule seems to need Bash, the answer is a different mechanism, not a wider
// allowlist.
func buildArgs(modelName, prompt string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--json-schema", FindingsSchema,
		"--allowedTools", "Read,Glob,Grep",
		"--permission-mode", "dontAsk",
	}
	if modelName != "" {
		args = append(args, "--model", modelName)
	}
	return args
}

// run executes the agent binary in dir and returns its stdout and stderr.
//
// The child is started in its own process group and the whole group is killed on
// cancellation or timeout. exec.CommandContext's default kill signals only the
// direct child, which would leave the agent's own subprocesses orphaned and
// still burning tokens after a cancelled run.
func (c *Checker) run(ctx context.Context, dir string, args []string) (stdout, stderr []byte, err error) {
	bin := c.Bin
	if bin == "" {
		bin = DefaultBin
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting %s: %w", bin, err)
	}

	// The watchdog and Wait coordinate through mu: once Wait has reaped the
	// child, its pid may be recycled, so no signal may be sent after that.
	var (
		mu       sync.Mutex
		finished bool
	)
	proc := cmd.Process
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			mu.Lock()
			if !finished {
				killProcessGroup(proc)
			}
			mu.Unlock()
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	mu.Lock()
	finished = true
	mu.Unlock()
	close(done)
	// Safe to read the buffers now: Wait has finished copying.
	stdout, stderr = outBuf.Bytes(), errBuf.Bytes()

	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return stdout, stderr, fmt.Errorf("agent timed out after %s", timeout)
		}
		return stdout, stderr, fmt.Errorf("agent cancelled: %w", ctxErr)
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			return stdout, stderr, fmt.Errorf("agent exited %d", ee.ExitCode())
		}
		return stdout, stderr, fmt.Errorf("running %s: %w", bin, waitErr)
	}
	return stdout, stderr, nil
}

// killProcessGroup SIGKILLs the whole group the child leads — the agent and
// every tool process it spawned — so nothing outlives a cancelled run. Setpgid
// guarantees the child is the group leader, so its pid is the pgid.
func killProcessGroup(proc *os.Process) {
	if pgid, err := syscall.Getpgid(proc.Pid); err == nil && pgid > 1 {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return
		}
	}
	// The group lookup raced or the group was already gone; at worst the direct
	// child is still around. Process.Kill is pid-recycle safe.
	_ = proc.Kill()
}

// parseEnvelope pulls findings and usage out of the JSON envelope claude prints
// under --output-format json.
//
// It is deliberately tolerant about the envelope's other fields, which change
// between CLI versions, and strict about structured_output, which is the whole
// payload. Absent, null, or wrongly shaped structured_output is an error and
// never an empty findings list.
func parseEnvelope(stdout []byte) ([]RawFinding, json.RawMessage, error) {
	objs := decodeJSONObjects(stdout)
	if len(objs) == 0 {
		return nil, nil, fmt.Errorf("could not parse claude JSON envelope (%d bytes of stdout)", len(stdout))
	}

	// Take the last object that carries structured_output: under
	// --output-format json there is exactly one object, but a future CLI that
	// prefixes diagnostics must not break us.
	var env map[string]json.RawMessage
	for i := len(objs) - 1; i >= 0; i-- {
		if _, ok := objs[i]["structured_output"]; ok {
			env = objs[i]
			break
		}
	}
	if env == nil {
		return nil, nil, errors.New("claude envelope has no structured_output")
	}

	so := env["structured_output"]
	if isJSONNull(so) {
		return nil, nil, errors.New("claude envelope has null structured_output")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(so, &payload); err != nil {
		return nil, nil, fmt.Errorf("structured_output is not an object: %w", err)
	}
	rawFindings, ok := payload["findings"]
	if !ok {
		return nil, nil, errors.New("structured_output has no findings array")
	}
	if isJSONNull(rawFindings) {
		return nil, nil, errors.New("structured_output.findings is null")
	}
	var findings []RawFinding
	if err := json.Unmarshal(rawFindings, &findings); err != nil {
		return nil, nil, fmt.Errorf("structured_output.findings is not a findings array: %w", err)
	}

	var usage json.RawMessage
	if u, ok := env["usage"]; ok && !isJSONNull(u) {
		usage = append(json.RawMessage(nil), u...)
	}
	return findings, usage, nil
}

func isJSONNull(b json.RawMessage) bool {
	return len(bytes.TrimSpace(b)) == 0 || string(bytes.TrimSpace(b)) == "null"
}

// decodeJSONObjects returns every top-level JSON object in b, skipping any
// non-JSON noise around them.
func decodeJSONObjects(b []byte) []map[string]json.RawMessage {
	const maxObjects = 64
	var out []map[string]json.RawMessage
	for start := 0; start < len(b) && len(out) < maxObjects; {
		idx := bytes.IndexByte(b[start:], '{')
		if idx < 0 {
			break
		}
		at := start + idx
		dec := json.NewDecoder(bytes.NewReader(b[at:]))
		var obj map[string]json.RawMessage
		if err := dec.Decode(&obj); err != nil {
			start = at + 1
			continue
		}
		out = append(out, obj)
		start = at + int(dec.InputOffset())
	}
	return out
}

// stderrSuffix renders the agent's stderr for inclusion in an error, keeping the
// tail. Without it, a failed invocation is undiagnosable from run.json alone.
func stderrSuffix(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	if s == "" {
		return ""
	}
	if len(s) > maxStderrInError {
		s = "...(truncated)... " + s[len(s)-maxStderrInError:]
	}
	return ": " + s
}
