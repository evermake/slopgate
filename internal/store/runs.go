package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/evermake/slopgate/internal/model"
)

// SaveRun persists r as run.json under its RunDir, atomically (temp file +
// rename in the same directory). A daemon killed mid-write leaves either the
// previous run.json or the new one, never a half-written file.
func (s *Store) SaveRun(r *model.Run) error {
	if err := validateRepoID(r.RepoID); err != nil {
		return err
	}
	if err := validateSHA(r.SHA); err != nil {
		return err
	}

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal run.json: %w", err)
	}
	data = append(data, '\n')

	return writeFileAtomic(s.runJSONPath(r.RepoID, r.SHA), data, 0o644)
}

// GetRun reads the run record for (repoID, sha). Returns ErrNotFound if
// absent.
func (s *Store) GetRun(repoID, sha string) (*model.Run, error) {
	if err := validateRepoID(repoID); err != nil {
		return nil, err
	}
	if err := validateSHA(sha); err != nil {
		return nil, err
	}

	path := s.runJSONPath(repoID, sha)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("store: run %s/%s: %w", repoID, sha, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", path, err)
	}

	var r model.Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	return &r, nil
}

// ListRuns returns every run recorded for repoID, sorted by QueuedAt
// ascending. A repo with no runs yet returns an empty slice, nil error.
func (s *Store) ListRuns(repoID string) ([]*model.Run, error) {
	if err := validateRepoID(repoID); err != nil {
		return nil, err
	}

	repoRunsDir := filepath.Join(s.Root, "runs", repoID)
	entries, err := os.ReadDir(repoRunsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: list %s: %w", repoRunsDir, err)
	}

	var runs []*model.Run
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		r, err := s.GetRun(repoID, e.Name())
		if errors.Is(err, ErrNotFound) {
			// A run directory that exists but has no run.json yet (e.g.
			// still being created) is not a run to report.
			continue
		}
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].QueuedAt.Before(runs[j].QueuedAt)
	})

	return runs, nil
}
