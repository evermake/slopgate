package store

import (
	"errors"
	"fmt"
	"os"
)

// WriteFeedback writes the rendered feedback.md for (repoID, sha) and
// returns the path it was written to. The write is atomic.
func (s *Store) WriteFeedback(repoID, sha, content string) (string, error) {
	if err := validateRepoID(repoID); err != nil {
		return "", err
	}
	if err := validateSHA(sha); err != nil {
		return "", err
	}

	path := s.FeedbackPath(repoID, sha)
	if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ReadFeedback reads back a previously written feedback.md. Returns
// ErrNotFound if absent.
func (s *Store) ReadFeedback(repoID, sha string) (string, error) {
	if err := validateRepoID(repoID); err != nil {
		return "", err
	}
	if err := validateSHA(sha); err != nil {
		return "", err
	}

	path := s.FeedbackPath(repoID, sha)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("store: feedback %s/%s: %w", repoID, sha, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: read %s: %w", path, err)
	}
	return string(data), nil
}

// WriteRuleRaw writes the raw structured_output + usage envelope for one
// rule invocation to rules/<rule>.json under the run directory. The write is
// atomic.
func (s *Store) WriteRuleRaw(repoID, sha, rule string, data []byte) error {
	if err := validateRepoID(repoID); err != nil {
		return err
	}
	if err := validateSHA(sha); err != nil {
		return err
	}
	if err := validateRuleName(rule); err != nil {
		return err
	}

	return writeFileAtomic(s.ruleJSONPath(repoID, sha, rule), data, 0o644)
}
