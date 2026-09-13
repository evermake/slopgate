package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evermake/slopgate/internal/model"
	"github.com/evermake/slopgate/internal/store"
)

// writeScript drops an executable-by-bash script at
// wt/.slopgate/scripts/<name>.sh.
func writeScript(t *testing.T, wt string, name model.ScriptName, body string) {
	t.Helper()
	dir := filepath.Join(wt, ".slopgate", "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	p := filepath.Join(dir, string(name)+".sh")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestRunScript_LogCloseFailure exercises the case from issue #4: the log
// writer's Close is where the buffered log actually gets flushed to disk, so
// a failure there must not be silently dropped in a defer. It must fail the
// script result and it must not leave res.LogPath pointing at a file that
// was never written.
func TestRunScript_LogCloseFailure(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	repoID, sha := "repo1", "sha1"
	logPath := s.LogPath(repoID, sha, model.ScriptCheck)

	// Force the log Close/flush to fail: pre-create the log's parent
	// directory as a regular file, so writeFileAtomic's MkdirAll cannot
	// create it as a directory.
	logsDir := filepath.Dir(logPath)
	if err := os.MkdirAll(filepath.Dir(logsDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(logsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wt := t.TempDir()
	writeScript(t, wt, model.ScriptCheck, "#!/bin/sh\nexit 0\n")

	r := &Runner{Store: s}
	res := r.runScript(context.Background(), model.ScriptCheck, wt, repoID, sha, scriptEnv{})

	if res.Error == "" {
		t.Fatalf("res.Error = %q, want non-empty: a log flush failure must not be silently dropped", res.Error)
	}
	if !strings.Contains(res.Error, "write log") {
		t.Errorf("res.Error = %q, want it to mention the log flush failure", res.Error)
	}
	if res.LogPath != "" {
		t.Errorf("res.LogPath = %q, want empty: the log was never written, nothing should point at it", res.LogPath)
	}
	if res.OK() {
		t.Errorf("res.OK() = true, want false: a lost log must fail the script")
	}
	if res.ExitCode != 0 {
		t.Errorf("res.ExitCode = %d, want 0: the script itself still ran and exited cleanly", res.ExitCode)
	}
}

// TestRunScript_ExitFailurePreservedWithLogCloseFailure checks that when both
// the script fails AND the log fails to flush, the original failure reason
// (a non-zero exit code, surfaced via ExitCode/OK rather than Error) is not
// clobbered, and the log-flush failure is appended rather than dropped.
func TestRunScript_ExitFailurePreservedWithLogCloseFailure(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	repoID, sha := "repo1", "sha1"
	logPath := s.LogPath(repoID, sha, model.ScriptCheck)
	logsDir := filepath.Dir(logPath)
	if err := os.MkdirAll(filepath.Dir(logsDir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(logsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	wt := t.TempDir()
	writeScript(t, wt, model.ScriptCheck, "#!/bin/sh\nexit 7\n")

	r := &Runner{Store: s}
	res := r.runScript(context.Background(), model.ScriptCheck, wt, repoID, sha, scriptEnv{})

	if res.ExitCode != 7 {
		t.Errorf("res.ExitCode = %d, want 7", res.ExitCode)
	}
	if !strings.Contains(res.Error, "write log") {
		t.Errorf("res.Error = %q, want it to mention the log flush failure", res.Error)
	}
	if res.LogPath != "" {
		t.Errorf("res.LogPath = %q, want empty", res.LogPath)
	}
}

// TestRunScript_Success is the control: when the log flushes fine, LogPath
// is populated and the script is reported as OK.
func TestRunScript_Success(t *testing.T) {
	root := t.TempDir()
	s, err := store.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	wt := t.TempDir()
	writeScript(t, wt, model.ScriptCheck, "#!/bin/sh\necho hi\nexit 0\n")

	r := &Runner{Store: s}
	res := r.runScript(context.Background(), model.ScriptCheck, wt, "repo1", "sha1", scriptEnv{})

	if res.Error != "" {
		t.Fatalf("res.Error = %q, want empty", res.Error)
	}
	if !res.OK() {
		t.Fatalf("res.OK() = false, want true")
	}
	if res.LogPath == "" {
		t.Fatalf("res.LogPath is empty, want the log path")
	}
	if _, err := os.Stat(res.LogPath); err != nil {
		t.Errorf("log file not written at %s: %v", res.LogPath, err)
	}
}
