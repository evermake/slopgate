package store

import (
	"fmt"
	"strings"
)

// maxSegmentLen bounds any single path component (repo ID, SHA, rule name)
// derived from external input (hook arguments, IPC params) before it is used
// to build a filesystem path.
const maxSegmentLen = 128

// validSegment reports whether s is safe to use as one path component: no
// empty string, no path separator, no "..", no leading dot, and only
// characters that plausibly appear in a ULID, a git SHA, or a rule/repo
// name (letters, digits, '-', '_', '.').
func validSegment(kind, s string) error {
	if s == "" {
		return fmt.Errorf("store: empty %s", kind)
	}
	if len(s) > maxSegmentLen {
		return fmt.Errorf("store: %s too long: %d bytes", kind, len(s))
	}
	if s == "." || s == ".." {
		return fmt.Errorf("store: invalid %s: %q", kind, s)
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("store: %s contains a path separator: %q", kind, s)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("store: %s contains '..': %q", kind, s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("store: %s contains invalid character %q: %q", kind, r, s)
		}
	}
	return nil
}

// validateRepoID validates a repo ID (a ULID) used as a path component.
func validateRepoID(id string) error {
	return validSegment("repo id", id)
}

// validateSHA validates a git SHA (or symbolic short form) used as a path
// component.
func validateSHA(sha string) error {
	return validSegment("sha", sha)
}

// validateRuleName validates a rule name used as a path component.
func validateRuleName(name string) error {
	return validSegment("rule name", name)
}
