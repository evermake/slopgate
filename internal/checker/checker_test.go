package checker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBin writes an executable shell script standing in for the real `claude`
// binary. Tests must never invoke the real one: it costs money and needs the
// network.
func fakeBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// envelope is a plausible `claude -p --output-format json` envelope. The extra
// fields are there on purpose: the parser must tolerate fields it does not know.
func envelope(structuredOutput string) string {
	return `{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,` +
		`"num_turns":3,"session_id":"abc-123","total_cost_usd":0.0421,` +
		`"usage":{"input_tokens":1200,"output_tokens":95,"cache_read_input_tokens":30},` +
		`"result":"done","structured_output":` + structuredOutput + `}`
}

func testInput(t *testing.T) Input {
	t.Helper()
	return Input{
		WorktreeDir:  fixtureWorktree(t),
		BaseSHA:      "d4e5f6a",
		HeadSHA:      "a3f9c21",
		ChangedFiles: []string{"src/a.go", "src/crlf.go"},
		Diff:         "diff --git a/src/a.go b/src/a.go\n+\tprintln(\"hi\")\n",
	}
}

var testRule = Rule{Name: "no-println", Body: "Do not call println in production code."}

func TestCheckHappyPath(t *testing.T) {
	in := testInput(t)
	argsFile := filepath.Join(t.TempDir(), "args")
	pwdFile := filepath.Join(t.TempDir(), "pwd")

	so := `{"findings":[
		{"file":"src/a.go","line":6,"snippet":"\tprintln(\"hi\")","explanation":"calls println"},
		{"file":"other/b.go","line":3,"snippet":"var Untouched = true","explanation":"not in the change"}
	]}`
	bin := fakeBin(t, `
for a in "$@"; do printf '%s\0' "$a" >> `+argsFile+`; done
pwd > `+pwdFile+`
cat <<'ENVELOPE'
`+envelope(so)+`
ENVELOPE
`)

	c := &Checker{Bin: bin, Timeout: 30 * time.Second}
	res, raw, err := c.Check(context.Background(), testRule, in)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if res.Name != "no-println" {
		t.Errorf("Name = %q", res.Name)
	}
	if res.Error != "" {
		t.Errorf("Error = %q, want empty", res.Error)
	}
	if len(res.Findings) != 1 || res.Findings[0].Rule != "no-println" || res.Findings[0].File != "src/a.go" {
		t.Fatalf("Findings = %+v", res.Findings)
	}
	if len(res.Discarded) != 1 || res.Discarded[0].Reason != "file_not_in_changed_set" {
		t.Fatalf("Discarded = %+v", res.Discarded)
	}
	var usage map[string]any
	if err := json.Unmarshal(res.Usage, &usage); err != nil {
		t.Fatalf("Usage %q: %v", res.Usage, err)
	}
	if usage["input_tokens"] != float64(1200) {
		t.Errorf("Usage = %s, want the envelope's usage block", res.Usage)
	}
	// The raw envelope is persisted verbatim by the caller.
	if !strings.Contains(string(raw), `"session_id":"abc-123"`) {
		t.Errorf("raw envelope not returned intact: %q", raw)
	}

	// cwd must be the worktree, so the agent's Read/Glob/Grep see the change.
	pwd, err := os.ReadFile(pwdFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(pwd)); !sameDir(got, in.WorktreeDir) {
		t.Errorf("cwd = %q, want %q", got, in.WorktreeDir)
	}

	args := readArgs(t, argsFile)
	assertFlag(t, args, "-p", BuildPrompt(testRule, in))
	assertFlag(t, args, "--output-format", "json")
	assertFlag(t, args, "--json-schema", FindingsSchema)
	assertFlag(t, args, "--allowedTools", "Read,Glob,Grep")
	assertFlag(t, args, "--permission-mode", "dontAsk")
	for _, a := range args {
		// Read-only tools are the whole basis of the verdict: an agent that can
		// write could delete the violation and honestly report clean.
		if strings.Contains(a, "Bash") || strings.Contains(a, "Write") || strings.Contains(a, "Edit") {
			t.Fatalf("agent was granted a write/exec tool: %q", a)
		}
		if a == "--dangerously-skip-permissions" || a == "--model" {
			t.Fatalf("unexpected argument %q", a)
		}
	}
}

func TestCheckPassesModelFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	bin := fakeBin(t, `
for a in "$@"; do printf '%s\0' "$a" >> `+argsFile+`; done
cat <<'ENVELOPE'
`+envelope(`{"findings":[]}`)+`
ENVELOPE
`)
	c := &Checker{Bin: bin, Model: "claude-opus-4", Timeout: 30 * time.Second}
	if _, _, err := c.Check(context.Background(), testRule, testInput(t)); err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertFlag(t, readArgs(t, argsFile), "--model", "claude-opus-4")
}

func TestCheckEmptyFindingsIsSuccess(t *testing.T) {
	bin := fakeBin(t, "cat <<'E'\n"+envelope(`{"findings":[]}`)+"\nE\n")
	c := &Checker{Bin: bin, Timeout: 30 * time.Second}
	res, _, err := c.Check(context.Background(), testRule, testInput(t))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(res.Findings) != 0 || len(res.Discarded) != 0 || res.Error != "" {
		t.Fatalf("res = %+v", res)
	}
}

// These are the failure modes that must never degrade into a clean verdict.
func TestCheckFailuresAreErrorsNotCleanRuns(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   string // substring of the error
	}{
		{
			name:   "non-zero exit with an otherwise valid envelope",
			script: "cat <<'E'\n" + envelope(`{"findings":[]}`) + "\nE\necho 'rate limit exceeded' >&2\nexit 3\n",
			want:   "agent exited 3",
		},
		{
			name:   "non-zero exit with no output at all",
			script: "echo 'boom: not logged in' >&2\nexit 1\n",
			want:   "boom: not logged in",
		},
		{
			name:   "malformed JSON",
			script: "printf 'Error: something went wrong\\n{\"type\": oops]\\n'\n",
			want:   "could not parse claude JSON envelope",
		},
		{
			name:   "empty stdout",
			script: "exit 0\n",
			want:   "could not parse claude JSON envelope",
		},
		{
			name:   "envelope without structured_output",
			script: `printf '%s' '{"type":"result","is_error":false,"result":"I could not comply."}'` + "\n",
			want:   "no structured_output",
		},
		{
			name:   "null structured_output",
			script: "cat <<'E'\n" + envelope(`null`) + "\nE\n",
			want:   "null structured_output",
		},
		{
			name:   "structured_output without findings",
			script: "cat <<'E'\n" + envelope(`{"violations":[]}`) + "\nE\n",
			want:   "no findings array",
		},
		{
			name:   "findings is not an array",
			script: "cat <<'E'\n" + envelope(`{"findings":"none"}`) + "\nE\n",
			want:   "not a findings array",
		},
		{
			name:   "structured_output is a string",
			script: "cat <<'E'\n" + envelope(`"no violations found"`) + "\nE\n",
			want:   "not an object",
		},
		{
			name:   "binary does not exist",
			script: "", // replaced below
			want:   "starting",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := fakeBin(t, tc.script)
			if tc.name == "binary does not exist" {
				bin = filepath.Join(t.TempDir(), "no-such-binary")
			}
			c := &Checker{Bin: bin, Timeout: 30 * time.Second}
			res, _, err := c.Check(context.Background(), testRule, testInput(t))
			if err == nil {
				t.Fatalf("want an error, got a clean result: %+v", res)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), `rule "no-println"`) && tc.name != "binary does not exist" {
				t.Errorf("error = %v, want it to name the rule", err)
			}
			if res.Error != err.Error() {
				t.Errorf("RuleResult.Error = %q, want it to mirror the error %q", res.Error, err)
			}
			if len(res.Findings) != 0 {
				t.Errorf("Findings = %+v, want none", res.Findings)
			}
			if res.Name != testRule.Name {
				t.Errorf("Name = %q, want %q", res.Name, testRule.Name)
			}
		})
	}
}

func TestCheckIncludesStderrInError(t *testing.T) {
	bin := fakeBin(t, "echo 'credit balance too low' >&2\nexit 1\n")
	c := &Checker{Bin: bin, Timeout: 30 * time.Second}
	_, _, err := c.Check(context.Background(), testRule, testInput(t))
	if err == nil || !strings.Contains(err.Error(), "credit balance too low") {
		t.Fatalf("err = %v, want the stderr text included", err)
	}
}

func TestCheckTimeout(t *testing.T) {
	bin := fakeBin(t, "sleep 30\n")
	c := &Checker{Bin: bin, Timeout: 150 * time.Millisecond}
	start := time.Now()
	_, _, err := c.Check(context.Background(), testRule, testInput(t))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("took %s, timeout was not enforced", elapsed)
	}
}

// A cancelled run must not leave orphaned agent processes behind. The fake
// binary spawns a grandchild, exactly as the real claude spawns tool processes;
// killing only the direct child (what exec.CommandContext does) would leave it
// running, so this asserts the whole process group dies.
func TestCheckCancelKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	bin := fakeBin(t, `
sh -c 'while true; do sleep 1; done' &
echo $! > `+pidFile+`
sleep 30
`)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Checker{Bin: bin, Timeout: time.Minute}

	errc := make(chan error, 1)
	go func() {
		_, _, err := c.Check(ctx, testRule, testInput(t))
		errc <- err
	}()

	grandchild := waitForPID(t, pidFile)
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d was not running to begin with", grandchild)
	}
	cancel()

	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("err = %v, want a cancellation error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Check did not return after cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(grandchild) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(grandchild, syscall.SIGKILL)
	t.Fatalf("grandchild %d survived cancellation: the process group was not killed", grandchild)
}

func TestParseEnvelopeTolerance(t *testing.T) {
	t.Run("noise around the envelope", func(t *testing.T) {
		stdout := "warning: config file not found\n" + envelope(`{"findings":[]}`) + "\n\n"
		findings, usage, err := parseEnvelope([]byte(stdout))
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if len(findings) != 0 || usage == nil {
			t.Fatalf("findings=%v usage=%s", findings, usage)
		}
	})

	t.Run("unknown fields and a missing usage block", func(t *testing.T) {
		stdout := `{"brand_new_field":{"nested":[1,2,3]},"structured_output":{"findings":[` +
			`{"file":"a.go","line":2,"snippet":"x","explanation":"y"}]}}`
		findings, usage, err := parseEnvelope([]byte(stdout))
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if len(findings) != 1 || findings[0].File != "a.go" || findings[0].Line != 2 {
			t.Fatalf("findings = %+v", findings)
		}
		if usage != nil {
			t.Errorf("usage = %s, want nil when absent", usage)
		}
	})

	t.Run("multiple objects, last with structured_output wins", func(t *testing.T) {
		stdout := `{"type":"system","subtype":"init"}` + "\n" + envelope(`{"findings":[{"file":"b.go","line":1,"snippet":"s","explanation":"e"}]}`)
		findings, _, err := parseEnvelope([]byte(stdout))
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if len(findings) != 1 || findings[0].File != "b.go" {
			t.Fatalf("findings = %+v", findings)
		}
	})
}

// --- helpers ---

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recorded args: %v", err)
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func assertFlag(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Fatalf("%s has no value", flag)
			}
			if args[i+1] != want {
				t.Errorf("%s = %q, want %q", flag, args[i+1], want)
			}
			return
		}
	}
	t.Errorf("%s not passed (args: %q)", flag, args)
}

func sameDir(a, b string) bool {
	ra, erra := filepath.EvalSymlinks(a)
	rb, errb := filepath.EvalSymlinks(b)
	if erra != nil || errb != nil {
		return a == b
	}
	return ra == rb
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake binary never wrote a pid to %s", path)
	return 0
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
