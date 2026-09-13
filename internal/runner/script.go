package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/evermake/slopgate/internal/model"
)

// ScriptTimeout bounds a single repo-supplied script. A hung test suite must
// not pin a worktree and a daemon slot forever.
const ScriptTimeout = 15 * time.Minute

// scriptEnv is the environment slopgate hands every script. SLOPGATE_CHANGED_FILES
// is a path to a newline-delimited file rather than the list itself: a large
// change would otherwise blow past the environment size limit.
type scriptEnv struct {
	RunID            string
	SHA              string
	BaseSHA          string
	ChangedFilesPath string
}

// runScript executes .slopgate/scripts/<name>.sh in the worktree.
//
// A missing script is Skipped and counts as passing -- absence is not an error,
// it just means the project has nothing to do at that stage.
func (r *Runner) runScript(ctx context.Context, name model.ScriptName, wt string, repoID, sha string, env scriptEnv) model.ScriptResult {
	res := model.ScriptResult{Name: name}
	path := filepath.Join(wt, ".slopgate", "scripts", string(name)+".sh")
	if _, err := os.Stat(path); err != nil {
		res.Skipped = true
		return res
	}

	logw, err := r.Store.OpenLogWriter(repoID, sha, name)
	if err != nil {
		res.Error = "open log: " + err.Error()
		return res
	}
	defer logw.Close()
	res.LogPath = r.Store.LogPath(repoID, sha, name)

	ctx, cancel := context.WithTimeout(ctx, r.scriptTimeout())
	defer cancel()

	start := r.now()
	cmd := exec.Command("bash", path)
	cmd.Dir = wt
	cmd.Stdout = logw
	cmd.Stderr = logw
	cmd.Env = append(os.Environ(),
		"SLOPGATE_RUN_ID="+env.RunID,
		"SLOPGATE_SHA="+env.SHA,
		"SLOPGATE_BASE_SHA="+env.BaseSHA,
		"SLOPGATE_CHANGED_FILES="+env.ChangedFilesPath,
	)
	// Setpgid so the whole tree dies on timeout. A test runner that spawns
	// its own children would otherwise survive the kill and keep running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		res.Error = err.Error()
		res.DurationMS = r.since(start)
		return res
	}
	pgid := cmd.Process.Pid

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		res.ExitCode = -1
		if !res.TimedOut {
			res.Error = "cancelled"
		}
	case err := <-done:
		res.ExitCode = exitCodeOf(err)
		if err != nil && res.ExitCode == -1 {
			res.Error = err.Error()
		}
	}
	res.DurationMS = r.since(start)
	return res
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// writeChangedFiles materialises the changed-file list for SLOPGATE_CHANGED_FILES.
func writeChangedFiles(dir string, files []string) (string, error) {
	p := filepath.Join(dir, "changed-files.txt")
	body := strings.Join(files, "\n")
	if body != "" {
		body += "\n"
	}
	return p, os.WriteFile(p, []byte(body), 0o644)
}
