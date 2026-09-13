// Package store implements the ~/.slopgate filesystem store: the repo
// registry, run records, feedback artifacts, script logs, and the daemon
// singleton lock.
//
// Layout (see MVP_PLAN.md § Store layout):
//
//	~/.slopgate/
//	  daemon.sock                      unix socket
//	  daemon.lock                      exclusive OS lock, held for daemon lifetime
//	  repos.json                       registry
//	  repos/<repo_id>.git/             bare gate repo, has `origin` -> real upstream
//	  worktrees/<repo_id>/<run_id>/    transient, removed on run end
//	  runs/<repo_id>/<sha>/
//	    run.json                       state, timings, verdict
//	    feedback.md                    canonical feedback artifact
//	    logs/{setup,check,test}.log    combined stdout+stderr, capped at 1 MiB
//	    rules/<rule-name>.json         raw structured_output + usage per rule
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evermake/slopgate/internal/model"
)

// ErrNotFound is returned (optionally wrapped) when a lookup by ID or path
// finds nothing. Callers can test with errors.Is.
var ErrNotFound = errors.New("not found")

// ErrDaemonRunning is returned by AcquireDaemonLock when another process
// already holds the daemon lock.
var ErrDaemonRunning = errors.New("another slopgate daemon is already running")

// Store is a handle onto the ~/.slopgate filesystem tree rooted at Root.
type Store struct {
	Root string
}

// DefaultRoot resolves the store root: $SLOPGATE_HOME if set and non-empty,
// else ~/.slopgate.
func DefaultRoot() (string, error) {
	if v := os.Getenv("SLOPGATE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("store: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".slopgate"), nil
}

// Open resolves the store root (DefaultRoot when root is "") and creates the
// directory tree if it does not already exist.
func Open(root string) (*Store, error) {
	if root == "" {
		r, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}

	s := &Store{Root: root}

	for _, dir := range []string{
		s.Root,
		filepath.Join(s.Root, "repos"),
		filepath.Join(s.Root, "worktrees"),
		filepath.Join(s.Root, "runs"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", dir, err)
		}
	}

	return s, nil
}

// SocketPath is the daemon's JSON-RPC unix socket.
func (s *Store) SocketPath() string {
	return filepath.Join(s.Root, "daemon.sock")
}

// LockPath is the daemon singleton flock target.
func (s *Store) LockPath() string {
	return filepath.Join(s.Root, "daemon.lock")
}

// reposJSONPath is the registry file.
func (s *Store) reposJSONPath() string {
	return filepath.Join(s.Root, "repos.json")
}

// GateRepoPath is the bare gate repo for a registered repo: repos/<id>.git
func (s *Store) GateRepoPath(repoID string) string {
	return filepath.Join(s.Root, "repos", repoID+".git")
}

// WorktreePath is the transient worktree for one run: worktrees/<repo>/<run>
func (s *Store) WorktreePath(repoID, runID string) string {
	return filepath.Join(s.Root, "worktrees", repoID, runID)
}

// RunDir is the durable directory for one (repo, sha) run: runs/<repo>/<sha>
func (s *Store) RunDir(repoID, sha string) string {
	return filepath.Join(s.Root, "runs", repoID, sha)
}

// runJSONPath is the run record file.
func (s *Store) runJSONPath(repoID, sha string) string {
	return filepath.Join(s.RunDir(repoID, sha), "run.json")
}

// FeedbackPath is the canonical feedback artifact for one run.
func (s *Store) FeedbackPath(repoID, sha string) string {
	return filepath.Join(s.RunDir(repoID, sha), "feedback.md")
}

// LogPath is the combined stdout+stderr log for one script of one run.
func (s *Store) LogPath(repoID, sha string, name model.ScriptName) string {
	return filepath.Join(s.RunDir(repoID, sha), "logs", string(name)+".log")
}

// rulesDir is where raw per-rule structured_output + usage is stored.
func (s *Store) rulesDir(repoID, sha string) string {
	return filepath.Join(s.RunDir(repoID, sha), "rules")
}

// ruleJSONPath is the raw structured_output + usage file for one rule.
func (s *Store) ruleJSONPath(repoID, sha, rule string) string {
	return filepath.Join(s.rulesDir(repoID, sha), rule+".json")
}
