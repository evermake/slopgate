package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/evermake/slopgate/internal/model"
)

// logCapBytes is the maximum size of a persisted script log: MVP_PLAN.md §
// Store layout, "capped at 1 MiB".
const logCapBytes = 1 << 20 // 1 MiB

// ringLogWriter is a bounded in-memory buffer that keeps only the most
// recently written logCapBytes bytes, flushing to its target file on Close.
// Safe for concurrent Write calls, since a script's stdout and stderr pipes
// are drained into it from separate goroutines.
type ringLogWriter struct {
	mu     sync.Mutex
	path   string
	buf    []byte
	closed bool
}

// Write appends p to the buffer, then trims from the front (oldest bytes)
// down to logCapBytes if needed, so the buffer always keeps the tail of
// everything written, never the head.
func (w *ringLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, fmt.Errorf("store: write to closed log writer for %s", w.path)
	}

	w.buf = append(w.buf, p...)
	if len(w.buf) > logCapBytes {
		start := len(w.buf) - logCapBytes
		trimmed := make([]byte, logCapBytes)
		copy(trimmed, w.buf[start:])
		w.buf = trimmed
	}

	return len(p), nil
}

// Close flushes the buffered tail to the target file atomically and marks
// the writer closed. Subsequent writes fail; a second Close is a no-op.
func (w *ringLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return nil
	}
	w.closed = true

	return writeFileAtomic(w.path, w.buf, 0o644)
}

// OpenLogWriter returns a writer for the combined stdout+stderr log of one
// script invocation, capped at 1 MiB and keeping the tail (a failing
// script's useful output is at the end). Nothing is written to disk until
// Close.
func (s *Store) OpenLogWriter(repoID, sha string, name model.ScriptName) (io.WriteCloser, error) {
	if err := validateRepoID(repoID); err != nil {
		return nil, err
	}
	if err := validateSHA(sha); err != nil {
		return nil, err
	}

	return &ringLogWriter{path: s.LogPath(repoID, sha, name)}, nil
}

// TailLog returns the last `lines` lines of a script's log file. Returns
// ErrNotFound if the log does not exist (e.g. the script was skipped).
func (s *Store) TailLog(repoID, sha string, name model.ScriptName, lines int) (string, error) {
	if err := validateRepoID(repoID); err != nil {
		return "", err
	}
	if err := validateSHA(sha); err != nil {
		return "", err
	}

	path := s.LogPath(repoID, sha, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("store: log %s/%s/%s: %w", repoID, sha, name, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: read %s: %w", path, err)
	}

	if lines <= 0 {
		return "", nil
	}

	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return "", nil
	}

	all := strings.Split(trimmed, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}
