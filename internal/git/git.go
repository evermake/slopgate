// Package git is a thin, dependency-free set of os/exec wrappers around the
// git binary. It imports nothing from this repo and no third-party modules.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// EmptyTreeSHA is git's well-known empty-tree object, useful as a diff base
// when there is no real merge-base (e.g. an orphan branch).
const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ErrFetchTimeout is the context.Cause set when Fetch's deadline elapses, so
// a hung SSH fetch against a dead connection reports something distinguishable
// from a bare "context deadline exceeded".
var ErrFetchTimeout = errors.New("upstream fetch timed out")

// NameStatusEntry is one line of `git diff --name-status` output. For rename
// entries (status "R100" etc.) Path is the destination path.
type NameStatusEntry struct {
	Status string
	Path   string
}

// Run invokes git with args in dir and returns trimmed stdout. On failure the
// returned error wraps git's stderr, since a git error with stderr swallowed
// is useless to debug. GIT_TERMINAL_PROMPT=0 is always set so a credential
// prompt fails fast instead of hanging the caller forever.
func Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderrText)
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// IsZeroSHA reports whether s is git's all-zero SHA, used to signal a ref
// being created or deleted (e.g. in pre-receive/post-receive hook input).
func IsZeroSHA(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}

// BranchFromRef strips a "refs/heads/" prefix, e.g. "refs/heads/feat/x" ->
// "feat/x". Refs without that prefix are returned unchanged.
func BranchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// RepoRoot returns the top-level working directory of the repo containing dir.
func RepoRoot(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "rev-parse", "--show-toplevel")
}

// Head returns the SHA HEAD currently points to.
func Head(ctx context.Context, dir string) (string, error) {
	return Run(ctx, dir, "rev-parse", "HEAD")
}

// RevParse resolves rev to a SHA.
func RevParse(ctx context.Context, dir, rev string) (string, error) {
	return Run(ctx, dir, "rev-parse", rev)
}

// InitBare creates a bare repository at path, creating leading directories
// as needed.
func InitBare(ctx context.Context, path string) error {
	_, err := Run(ctx, "", "init", "--bare", path)
	return err
}

// AddRemote adds remote name pointing at url. Idempotent: if the remote
// already exists, its URL is updated with set-url instead of failing.
func AddRemote(ctx context.Context, dir, name, url string) error {
	if _, err := Run(ctx, dir, "remote", "get-url", name); err == nil {
		_, err := Run(ctx, dir, "remote", "set-url", name, url)
		return err
	}
	_, err := Run(ctx, dir, "remote", "add", name, url)
	return err
}

// GetRemoteURL returns the configured URL for remote.
func GetRemoteURL(ctx context.Context, dir, remote string) (string, error) {
	return Run(ctx, dir, "remote", "get-url", remote)
}

// AddWorktree checks out sha into a new detached worktree at worktreePath,
// linked to the repo at gateRepo.
func AddWorktree(ctx context.Context, gateRepo, worktreePath, sha string) error {
	_, err := Run(ctx, gateRepo, "worktree", "add", "--detach", worktreePath, sha)
	return err
}

// RemoveWorktree force-removes the worktree at worktreePath and prunes stale
// worktree metadata afterwards.
func RemoveWorktree(ctx context.Context, gateRepo, worktreePath string) error {
	_, err := Run(ctx, gateRepo, "worktree", "remove", "--force", worktreePath)
	if err != nil {
		return err
	}
	_, err = Run(ctx, gateRepo, "worktree", "prune")
	return err
}

// Fetch runs `git fetch remote branch` in dir, bounded by timeout. If the
// timeout elapses, the returned error is (or wraps, via context.Cause)
// ErrFetchTimeout rather than a bare context-deadline error — an SSH fetch
// against a dead connection otherwise hangs indefinitely with nothing else
// to cancel it.
func Fetch(ctx context.Context, dir, remote, branch string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeoutCause(ctx, timeout, ErrFetchTimeout)
	defer cancel()

	_, err := Run(cctx, dir, "fetch", remote, branch)
	if err != nil {
		if cctx.Err() != nil {
			if cause := context.Cause(cctx); cause != nil {
				return cause
			}
		}
		return err
	}
	return nil
}

// DefaultBranch resolves the remote origin's HEAD symref via
// `git ls-remote --symref origin HEAD`, e.g. parsing a line like
// "ref: refs/heads/main\tHEAD". It never returns an error: any failure
// (offline, no origin, malformed output) falls back to "main".
func DefaultBranch(ctx context.Context, dir string) string {
	out, err := Run(ctx, dir, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "main"
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		fields := strings.Fields(line)
		// fields: "ref:", "refs/heads/<branch>", "HEAD"
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		branch := BranchFromRef(ref)
		if branch != "" && branch != ref {
			return branch
		}
	}
	return "main"
}

// MergeBase returns the best common ancestor of a and b.
func MergeBase(ctx context.Context, dir, a, b string) (string, error) {
	return Run(ctx, dir, "merge-base", a, b)
}

// ChangedFiles returns the paths that differ between base and head. Returns
// nil (not a one-element slice) when there are no changes.
func ChangedFiles(ctx context.Context, dir, base, head string) ([]string, error) {
	out, err := Run(ctx, dir, "diff", "--name-only", base, head)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// NameStatus returns the status/path pairs that differ between base and
// head, optionally restricted to pathspec. Rename entries (status "R100"
// etc.) carry two paths in git's raw output; NameStatus keeps the raw status
// letters and the destination path. Returns nil (not a one-element slice
// containing "") when there are no changes.
func NameStatus(ctx context.Context, dir, base, head string, pathspec ...string) ([]NameStatusEntry, error) {
	args := []string{"diff", "--name-status", base, head}
	if len(pathspec) > 0 {
		args = append(args, "--")
		args = append(args, pathspec...)
	}
	out, err := Run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	lines := strings.Split(out, "\n")
	entries := make([]NameStatusEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		entries = append(entries, NameStatusEntry{
			Status: fields[0],
			Path:   fields[len(fields)-1],
		})
	}
	return entries, nil
}

// Diff returns the full unified diff between base and head.
func Diff(ctx context.Context, dir, base, head string) (string, error) {
	return Run(ctx, dir, "diff", base, head)
}
