package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes data to path atomically: a temp file is created in
// the same directory as path (so the rename is same-filesystem), written,
// fsynced, closed, and then renamed over path. The directory is fsynced too,
// so the rename itself is durable. A process killed at any point either
// leaves the old path untouched or the new one fully written; it never
// leaves a partially-written path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("store: create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("store: create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// If we return early, make sure no temp file is left behind.
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: fsync temp file %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("store: chmod temp file %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("store: rename %s to %s: %w", tmpPath, path, err)
	}
	removeTmp = false

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}
