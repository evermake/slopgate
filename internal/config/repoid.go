package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ReadRepoID reads the stable repo identity written at `slopgate init` from
// <repoRoot>/.slopgate/repo-id.
func ReadRepoID(repoRoot string) (string, error) {
	path := filepath.Join(repoRoot, Dir, "repo-id")

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading repo id file %s: %w", path, err)
	}

	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("repo id file %s is empty", path)
	}

	return id, nil
}

// WriteRepoID writes id to <repoRoot>/.slopgate/repo-id, creating the
// .slopgate directory if needed.
func WriteRepoID(repoRoot, id string) error {
	dir := filepath.Join(repoRoot, Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	path := filepath.Join(dir, "repo-id")
	if err := os.WriteFile(path, []byte(id+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing repo id file %s: %w", path, err)
	}

	return nil
}
