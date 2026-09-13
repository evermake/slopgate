package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/evermake/slopgate/internal/model"
)

// reposFile is the on-disk shape of repos.json.
type reposFile struct {
	Repos []model.Repo `json:"repos"`
}

// reposLockPath is a dedicated lock file guarding read-modify-write of
// repos.json, separate from repos.json itself so the lock is unaffected by
// the atomic rename that replaces the registry's underlying inode on every
// write.
func (s *Store) reposLockPath() string {
	return filepath.Join(s.Root, "repos.json.lock")
}

// withReposLock takes a blocking exclusive flock around fn, serializing
// concurrent read-modify-write cycles against repos.json (e.g. two
// concurrent `slopgate init` runs) so neither loses the other's update.
func (s *Store) withReposLock(fn func() error) error {
	path := s.reposLockPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("store: open %s: %w", path, err)
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("store: flock %s: %w", path, err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	return fn()
}

// readRepos reads repos.json. A missing file is treated as an empty
// registry.
func (s *Store) readRepos() (reposFile, error) {
	data, err := os.ReadFile(s.reposJSONPath())
	if errors.Is(err, os.ErrNotExist) {
		return reposFile{}, nil
	}
	if err != nil {
		return reposFile{}, fmt.Errorf("store: read repos.json: %w", err)
	}
	var rf reposFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return reposFile{}, fmt.Errorf("store: parse repos.json: %w", err)
	}
	return rf, nil
}

// ListRepos returns all registered repos.
func (s *Store) ListRepos() ([]model.Repo, error) {
	rf, err := s.readRepos()
	if err != nil {
		return nil, err
	}
	return rf.Repos, nil
}

// GetRepo looks up a registered repo by ID. Returns ErrNotFound if absent.
func (s *Store) GetRepo(id string) (model.Repo, error) {
	rf, err := s.readRepos()
	if err != nil {
		return model.Repo{}, err
	}
	for _, r := range rf.Repos {
		if r.ID == id {
			return r, nil
		}
	}
	return model.Repo{}, fmt.Errorf("store: repo %q: %w", id, ErrNotFound)
}

// FindRepoByPath looks up a registered repo by its working-directory path.
// Comparison is on cleaned, symlink-evaluated absolute paths, so e.g. on
// macOS a repo under the /tmp -> /private/tmp symlink still matches.
// Returns ErrNotFound if no repo matches.
func (s *Store) FindRepoByPath(path string) (model.Repo, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return model.Repo{}, fmt.Errorf("store: resolve path %q: %w", path, err)
	}

	rf, err := s.readRepos()
	if err != nil {
		return model.Repo{}, err
	}
	for _, r := range rf.Repos {
		rResolved, err := resolvePath(r.Path)
		if err != nil {
			// A registered repo whose path no longer resolves (moved,
			// deleted) simply cannot match by path; keep looking.
			continue
		}
		if rResolved == resolved {
			return r, nil
		}
	}
	return model.Repo{}, fmt.Errorf("store: repo at %q: %w", path, ErrNotFound)
}

// resolvePath returns the cleaned, symlink-evaluated absolute form of path.
func resolvePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// The path may not exist (yet); fall back to the cleaned absolute
		// form rather than failing the lookup outright.
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Clean(abs), nil
		}
		return "", err
	}
	return resolved, nil
}

// SaveRepo upserts r into repos.json by ID: an existing entry with the same
// ID is replaced in place, otherwise r is appended. The read-modify-write is
// guarded by a file lock and the write is atomic (temp file + rename in the
// same directory), so concurrent `slopgate init` runs cannot lose a repo.
func (s *Store) SaveRepo(r model.Repo) error {
	if err := validateRepoID(r.ID); err != nil {
		return err
	}

	return s.withReposLock(func() error {
		rf, err := s.readRepos()
		if err != nil {
			return err
		}

		found := false
		for i, existing := range rf.Repos {
			if existing.ID == r.ID {
				rf.Repos[i] = r
				found = true
				break
			}
		}
		if !found {
			rf.Repos = append(rf.Repos, r)
		}

		data, err := json.MarshalIndent(rf, "", "  ")
		if err != nil {
			return fmt.Errorf("store: marshal repos.json: %w", err)
		}
		data = append(data, '\n')

		return writeFileAtomic(s.reposJSONPath(), data, 0o644)
	})
}
