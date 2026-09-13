package checker

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/evermake/slopgate/internal/model"
)

// RawFinding is one finding exactly as the model reported it, before any
// verification. It carries no rule name: slopgate stamps that itself.
type RawFinding struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Snippet     string `json:"snippet"`
	Explanation string `json:"explanation"`
}

// snippetWindow is how far the reported line may drift from where the snippet
// actually sits before the finding is discarded. Model line numbers are off by
// a line or two routinely; they are not off by ten.
const snippetWindow = 3

// ValidateFindings is slopgate's deterministic hallucination filter.
//
// Agentic exploration is non-deterministic: the same rule over the same diff can
// produce different findings run to run, and a gate whose verdict flips on
// identical input loses trust immediately. Every finding therefore has to carry a
// citation that resolves in the worktree, and this function is what resolves it —
// no extra LLM call, no judgement, just the file on disk.
//
// A finding is discarded when:
//
//  1. its file is not in changedFiles (model.DiscardNotChanged) — this is what
//     stops slopgate reporting the entire legacy codebase on every run;
//  2. its file does not exist or cannot be read in the worktree
//     (model.DiscardMissingFile);
//  3. its snippet does not appear within ±3 lines of the reported line
//     (model.DiscardBadSnippet).
//
// The checks run in that order and the first failure wins, so the reason is
// always the most specific fact known about the finding.
//
// Survivors get Rule stamped from ruleName, their path normalised, and their
// Line corrected to where the snippet actually starts. Discards
// keep the path exactly as the model wrote it, because a wrong path is the
// diagnostic. The function is pure apart from reading files under worktreeDir.
func ValidateFindings(ruleName string, raw []RawFinding, worktreeDir string, changedFiles []string) (kept []model.Finding, discarded []model.DiscardedFinding) {
	changed := make(map[string]struct{}, len(changedFiles))
	for _, f := range changedFiles {
		if norm, ok := normalizePath(f); ok {
			changed[norm] = struct{}{}
		}
	}

	// Files are read at most once per call: a rule commonly reports several
	// findings in the same file.
	type fileEntry struct {
		lines []string
		ok    bool
	}
	cache := make(map[string]fileEntry)

	for _, rf := range raw {
		discard := func(reason model.DiscardReason) {
			discarded = append(discarded, model.DiscardedFinding{
				Finding: model.Finding{
					Rule:        ruleName,
					File:        rf.File,
					Line:        rf.Line,
					Snippet:     rf.Snippet,
					Explanation: rf.Explanation,
				},
				Reason: reason,
			})
		}

		// 1. In the changed set? An unnormalisable path (absolute, or escaping
		// the worktree with ..) can never be in it, so it lands here.
		norm, ok := normalizePath(rf.File)
		if !ok {
			discard(model.DiscardNotChanged)
			continue
		}
		if _, inSet := changed[norm]; !inSet {
			discard(model.DiscardNotChanged)
			continue
		}

		// 2. Present and readable in the worktree?
		entry, cached := cache[norm]
		if !cached {
			lines, err := readFileLines(worktreeDir, norm)
			entry = fileEntry{lines: lines, ok: err == nil}
			cache[norm] = entry
		}
		if !entry.ok {
			discard(model.DiscardMissingFile)
			continue
		}

		// 3. Does the snippet actually sit within ±3 lines of the citation?
		at, ok := snippetAt(entry.lines, rf.Line, rf.Snippet)
		if !ok {
			discard(model.DiscardBadSnippet)
			continue
		}

		kept = append(kept, model.Finding{
			Rule:        ruleName,
			File:        norm,
			Line:        at,
			Snippet:     rf.Snippet,
			Explanation: rf.Explanation,
		})
	}
	return kept, discarded
}

// normalizePath turns a model-reported path into a clean, slash-separated,
// worktree-relative path. It reports false for anything that cannot be one:
// an empty path, an absolute path, or a path that escapes the worktree.
func normalizePath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") || filepath.IsAbs(filepath.FromSlash(p)) {
		return "", false
	}
	// Reject NUL and other paths the OS will not accept anyway.
	if strings.ContainsRune(p, 0) {
		return "", false
	}
	p = path.Clean(p) // also strips "./" prefixes and collapses "a/../b"
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// readFileLines reads a worktree-relative file and splits it into lines with
// trailing whitespace trimmed, ready for snippet comparison.
func readFileLines(worktreeDir, relPath string) ([]string, error) {
	full := filepath.Join(worktreeDir, filepath.FromSlash(relPath))
	// normalizePath has already rejected escapes; this is the belt to that
	// braces, in case worktreeDir itself is relative or contains symlinks.
	if worktreeDir != "" {
		base := filepath.Clean(worktreeDir)
		if rel, err := filepath.Rel(base, full); err != nil ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, os.ErrPermission
		}
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, os.ErrInvalid
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return splitTrimmedLines(string(data)), nil
}

// splitTrimmedLines splits text into lines, dropping \r and trailing whitespace
// from each. Trailing whitespace is invisible in the model's context window, so
// comparing it would discard correct findings for no reason. Leading whitespace
// is significant and is kept.
func splitTrimmedLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\v\f\r")
	}
	return lines
}

// snippetAt reports whether snippet appears in fileLines, consecutively, with
// its first line landing anywhere in [line-3, line+3], and returns the 1-based
// line where it actually starts. line is 1-based; a line below 1 is not a valid
// citation and never matches.
//
// The matched line is returned rather than discarded because the citation is
// what the reader navigates to. Reporting the model's line when the code sits
// two lines further down sends the agent to the wrong place, which is a small
// lie in the one artifact this tool exists to produce.
func snippetAt(fileLines []string, line int, snippet string) (int, bool) {
	if line < 1 {
		return 0, false
	}
	snipLines := trimBlankEdges(splitTrimmedLines(snippet))
	if len(snipLines) == 0 {
		// An empty snippet cites nothing and can be "found" anywhere; it is
		// always a discard.
		return 0, false
	}
	if len(snipLines) > len(fileLines) {
		return 0, false
	}

	lo := line - snippetWindow
	if lo < 1 {
		lo = 1
	}
	hi := line + snippetWindow
	if maxStart := len(fileLines) - len(snipLines) + 1; hi > maxStart {
		hi = maxStart
	}
	// Prefer the line the model reported when the snippet matches there, so a
	// correct citation is never rewritten; otherwise take the nearest match.
	best, found := 0, false
	for start := lo; start <= hi; start++ {
		if !linesEqual(fileLines[start-1:start-1+len(snipLines)], snipLines) {
			continue
		}
		if start == line {
			return start, true
		}
		if !found || abs(start-line) < abs(best-line) {
			best, found = start, true
		}
	}
	return best, found
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// trimBlankEdges drops leading and trailing blank lines, which a quoted snippet
// picks up from the surrounding markdown or from a trailing newline.
func trimBlankEdges(lines []string) []string {
	i, j := 0, len(lines)
	for i < j && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for j > i && strings.TrimSpace(lines[j-1]) == "" {
		j--
	}
	return lines[i:j]
}
