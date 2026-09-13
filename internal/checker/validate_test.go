package checker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/evermake/slopgate/internal/model"
)

// fixtureWorktree writes a small tree used by the validation tests.
//
// src/a.go (line numbers are 1-based, as they are everywhere in slopgate):
//
//	1  package main
//	2
//	3  func main() {
//	4      x := 1
//	5      _ = x          <- has trailing spaces on disk
//	6      println("hi")
//	7  }
func fixtureWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(rel, content string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("src/a.go", "package main\n\nfunc main() {\n\tx := 1\n\t_ = x   \n\tprintln(\"hi\")\n}\n")
	write("src/crlf.go", "package main\r\n\r\nvar Answer = 42\r\n")
	write("other/b.go", "package other\n\nvar Untouched = true\n")
	write("short.txt", "only line\n")
	if err := os.MkdirAll(filepath.Join(dir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// defaultChanged is the changed-file set used by most cases. Note that
// other/b.go exists on disk but is deliberately NOT in it.
var defaultChanged = []string{"src/a.go", "src/crlf.go", "src/gone.go", "short.txt", "emptydir"}

func TestValidateFindings(t *testing.T) {
	dir := fixtureWorktree(t)

	const (
		lineX       = "\tx := 1"
		lineUnderX  = "\t_ = x"
		linePrintln = "\tprintln(\"hi\")"
	)

	tests := []struct {
		name    string
		finding RawFinding
		changed []string
		keep    bool
		reason  model.DiscardReason
		wantFil string // expected normalised File on a kept finding
		// wantLine is the corrected line a kept finding should carry when the
		// snippet was found somewhere other than where the model said. Zero
		// means "the reported line was right and must be preserved".
		wantLine int
	}{
		{
			name:    "exact line match",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: linePrintln},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "window lower edge: reported 3, actual 6",
			finding: RawFinding{File: "src/a.go", Line: 3, Snippet: linePrintln},
			keep:    true, wantFil: "src/a.go", wantLine: 6,
		},
		{
			name:    "window upper edge: reported 9, actual 6",
			finding: RawFinding{File: "src/a.go", Line: 9, Snippet: linePrintln},
			keep:    true, wantFil: "src/a.go", wantLine: 6,
		},
		{
			name:    "four lines below the window is discarded",
			finding: RawFinding{File: "src/a.go", Line: 2, Snippet: linePrintln},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "four lines above the window is discarded",
			finding: RawFinding{File: "src/a.go", Line: 10, Snippet: linePrintln},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "multi-line snippet matching consecutively",
			finding: RawFinding{File: "src/a.go", Line: 4, Snippet: lineX + "\n" + lineUnderX + "\n" + linePrintln},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "multi-line snippet drifted within the window",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: lineX + "\n" + lineUnderX},
			keep:    true, wantFil: "src/a.go", wantLine: 4,
		},
		{
			name:    "multi-line snippet whose lines are not consecutive",
			finding: RawFinding{File: "src/a.go", Line: 4, Snippet: lineX + "\n" + linePrintln},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "multi-line snippet longer than the file",
			finding: RawFinding{File: "short.txt", Line: 1, Snippet: "only line\nonly line\nonly line\nonly line"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "trailing whitespace on disk is ignored",
			finding: RawFinding{File: "src/a.go", Line: 5, Snippet: lineUnderX},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "trailing whitespace in the snippet is ignored",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: linePrintln + "   \n"},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "leading whitespace is significant",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: "println(\"hi\")"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "CRLF in the file is normalised",
			finding: RawFinding{File: "src/crlf.go", Line: 3, Snippet: "var Answer = 42"},
			keep:    true, wantFil: "src/crlf.go",
		},
		{
			name:    "empty snippet",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: ""},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "whitespace-only snippet",
			finding: RawFinding{File: "src/a.go", Line: 6, Snippet: "  \n\t\n"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "snippet nowhere in the file",
			finding: RawFinding{File: "src/a.go", Line: 4, Snippet: "\tpanic(\"nope\")"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "file exists but is not in the changed set",
			finding: RawFinding{File: "other/b.go", Line: 3, Snippet: "var Untouched = true"},
			reason:  model.DiscardNotChanged,
		},
		{
			name:    "file in the changed set but absent from the worktree",
			finding: RawFinding{File: "src/gone.go", Line: 1, Snippet: "package main"},
			reason:  model.DiscardMissingFile,
		},
		{
			name:    "directory instead of a file",
			finding: RawFinding{File: "emptydir", Line: 1, Snippet: "anything"},
			reason:  model.DiscardMissingFile,
		},
		{
			name:    "relative path escaping the worktree",
			finding: RawFinding{File: "../outside.go", Line: 1, Snippet: "package main"},
			changed: append([]string{"../outside.go"}, defaultChanged...),
			reason:  model.DiscardNotChanged,
		},
		{
			name:    "path escaping through the middle",
			finding: RawFinding{File: "src/../../outside.go", Line: 1, Snippet: "package main"},
			reason:  model.DiscardNotChanged,
		},
		{
			name:    "absolute path",
			finding: RawFinding{File: "/etc/passwd", Line: 1, Snippet: "root"},
			changed: append([]string{"/etc/passwd"}, defaultChanged...),
			reason:  model.DiscardNotChanged,
		},
		{
			name:    "empty path",
			finding: RawFinding{File: "", Line: 1, Snippet: "package main"},
			reason:  model.DiscardNotChanged,
		},
		{
			name:    "leading ./ is stripped and matched against the changed set",
			finding: RawFinding{File: "./src/a.go", Line: 1, Snippet: "package main"},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "redundant path segments are cleaned",
			finding: RawFinding{File: "src/./sub/../a.go", Line: 1, Snippet: "package main"},
			keep:    true, wantFil: "src/a.go",
		},
		{
			name:    "line 0 is not a valid 1-based citation",
			finding: RawFinding{File: "src/a.go", Line: 0, Snippet: "package main"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "negative line",
			finding: RawFinding{File: "src/a.go", Line: -4, Snippet: "package main"},
			reason:  model.DiscardBadSnippet,
		},
		{
			name:    "line past EOF but within the window of the last line",
			finding: RawFinding{File: "src/a.go", Line: 9, Snippet: "}"},
			keep:    true, wantFil: "src/a.go", wantLine: 7,
		},
		{
			name:    "line far past EOF",
			finding: RawFinding{File: "src/a.go", Line: 400, Snippet: "package main"},
			reason:  model.DiscardBadSnippet,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.changed
			if changed == nil {
				changed = defaultChanged
			}
			tc.finding.Explanation = "because the rule says so"

			kept, discarded := ValidateFindings("no-casts", []RawFinding{tc.finding}, dir, changed)

			if tc.keep {
				if len(kept) != 1 || len(discarded) != 0 {
					t.Fatalf("want 1 kept 0 discarded, got %d kept %d discarded (%+v)", len(kept), len(discarded), discarded)
				}
				got := kept[0]
				if got.Rule != "no-casts" {
					t.Errorf("Rule = %q, want it stamped as %q", got.Rule, "no-casts")
				}
				if got.File != tc.wantFil {
					t.Errorf("File = %q, want normalised %q", got.File, tc.wantFil)
				}
				wantLine := tc.wantLine
				if wantLine == 0 {
					wantLine = tc.finding.Line
				}
				if got.Line != wantLine {
					t.Errorf("Line = %d, want %d (reported %d)", got.Line, wantLine, tc.finding.Line)
				}
				if got.Snippet != tc.finding.Snippet || got.Explanation != tc.finding.Explanation {
					t.Errorf("snippet/explanation not carried through: %+v", got)
				}
				return
			}

			if len(kept) != 0 || len(discarded) != 1 {
				t.Fatalf("want 0 kept 1 discarded, got %d kept %d discarded (%+v)", len(kept), len(discarded), kept)
			}
			if discarded[0].Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", discarded[0].Reason, tc.reason)
			}
			if discarded[0].Rule != "no-casts" {
				t.Errorf("discarded Rule = %q, want it stamped", discarded[0].Rule)
			}
			if discarded[0].File != tc.finding.File {
				t.Errorf("discarded File = %q, want the path as reported (%q)", discarded[0].File, tc.finding.File)
			}
		})
	}
}

func TestValidateFindingsPreservesOrderAndMixes(t *testing.T) {
	dir := fixtureWorktree(t)
	raw := []RawFinding{
		{File: "src/a.go", Line: 4, Snippet: "\tx := 1", Explanation: "first"},
		{File: "other/b.go", Line: 3, Snippet: "var Untouched = true", Explanation: "second"},
		{File: "src/a.go", Line: 6, Snippet: "\tprintln(\"hi\")", Explanation: "third"},
		{File: "src/gone.go", Line: 1, Snippet: "package main", Explanation: "fourth"},
		{File: "src/a.go", Line: 1, Snippet: "package other", Explanation: "fifth"},
	}
	kept, discarded := ValidateFindings("r", raw, dir, defaultChanged)

	if len(kept) != 2 || kept[0].Explanation != "first" || kept[1].Explanation != "third" {
		t.Fatalf("kept = %+v", kept)
	}
	wantReasons := []model.DiscardReason{model.DiscardNotChanged, model.DiscardMissingFile, model.DiscardBadSnippet}
	if len(discarded) != len(wantReasons) {
		t.Fatalf("discarded = %+v", discarded)
	}
	for i, want := range wantReasons {
		if discarded[i].Reason != want {
			t.Errorf("discarded[%d].Reason = %q, want %q", i, discarded[i].Reason, want)
		}
	}
}

func TestValidateFindingsEmptyInput(t *testing.T) {
	kept, discarded := ValidateFindings("r", nil, t.TempDir(), []string{"a.go"})
	if len(kept) != 0 || len(discarded) != 0 {
		t.Fatalf("kept=%v discarded=%v", kept, discarded)
	}
}

func TestValidateFindingsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	rel := "secret.go"
	full := filepath.Join(dir, rel)
	if err := os.WriteFile(full, []byte("package main\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o644) })

	_, discarded := ValidateFindings("r",
		[]RawFinding{{File: rel, Line: 1, Snippet: "package main"}}, dir, []string{rel})
	if len(discarded) != 1 || discarded[0].Reason != model.DiscardMissingFile {
		t.Fatalf("want an unreadable file discarded as %q, got %+v", model.DiscardMissingFile, discarded)
	}
}

func TestNormalizePath(t *testing.T) {
	ok := map[string]string{
		"a.go":              "a.go",
		"./a.go":            "a.go",
		"././src/a.go":      "src/a.go",
		"src/./a.go":        "src/a.go",
		"src/sub/../a.go":   "src/a.go",
		" src/a.go ":        "src/a.go",
		"src//a.go":         "src/a.go",
		"dir/with space.go": "dir/with space.go",
	}
	for in, want := range ok {
		got, valid := normalizePath(in)
		if !valid || got != want {
			t.Errorf("normalizePath(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	bad := []string{"", "   ", "/abs/path.go", "../a.go", "..", ".", "a/../../b.go", "./../x"}
	for _, in := range bad {
		if got, valid := normalizePath(in); valid {
			t.Errorf("normalizePath(%q) = %q, true; want rejected", in, got)
		}
	}
}
