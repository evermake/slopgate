package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRules_MissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rules")

	got, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("LoadRules() = %v, want nil", got)
	}
}

func TestLoadRules_SortedByName(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "zeta.md", "---\nalwaysCheck: true\n---\nZ.\n")
	writeRule(t, dir, "alpha.md", "---\nalwaysCheck: true\n---\nA.\n")
	writeRule(t, dir, "mid.md", "---\nalwaysCheck: true\n---\nM.\n")

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("len(rules) = %d, want 3", len(rules))
	}
	names := []string{rules[0].Name, rules[1].Name, rules[2].Name}
	want := []string{"alpha", "mid", "zeta"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("rules names = %v, want %v", names, want)
		}
	}
}

func TestLoadRules_IgnoresNonMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "real.md", "---\nalwaysCheck: true\n---\nBody.\n")
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "real" {
		t.Fatalf("rules = %+v, want just [real]", rules)
	}
}

func TestLoadRules_NameAndPathAndBody(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "no-type-casts.md", "---\nalwaysCheck: true\n---\nDon't cast.\n")

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	r := rules[0]
	if r.Name != "no-type-casts" {
		t.Errorf("Name = %q, want %q", r.Name, "no-type-casts")
	}
	wantPath := filepath.Join(dir, "no-type-casts.md")
	if r.Path != wantPath {
		t.Errorf("Path = %q, want %q", r.Path, wantPath)
	}
	if r.Body != "Don't cast." {
		t.Errorf("Body = %q, want %q", r.Body, "Don't cast.")
	}
}

func TestLoadRules_MalformedFileReturnsErrorNamingFile(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "good.md", "---\nalwaysCheck: true\n---\nGood.\n")
	writeRule(t, dir, "bad.md", "---\nalwaysCheck: [oops\n---\nBad.\n")

	rules, err := LoadRules(dir)
	if err == nil {
		t.Fatalf("LoadRules() error = nil, want error; rules = %+v", rules)
	}
	if !strings.Contains(err.Error(), "bad.md") {
		t.Fatalf("error = %q, want it to name bad.md", err.Error())
	}
}

func TestLoadRules_UnreadableFileReturnsErrorNamingFile(t *testing.T) {
	dir := t.TempDir()
	path := writeRule(t, dir, "noperm.md", "---\nalwaysCheck: true\n---\nBody.\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions are not enforced")
	}

	_, err := LoadRules(dir)
	if err == nil {
		t.Fatal("LoadRules() error = nil, want error for unreadable file")
	}
	if !strings.Contains(err.Error(), "noperm.md") {
		t.Fatalf("error = %q, want it to name noperm.md", err.Error())
	}
}

func writeRule(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing rule %s: %v", name, err)
	}
	return path
}

func TestRule_Matches(t *testing.T) {
	tests := []struct {
		name         string
		rule         Rule
		changedFiles []string
		want         bool
	}{
		{
			name:         "alwaysCheck true overrides non-matching globs",
			rule:         Rule{AlwaysCheck: true, Globs: []string{"docs/**"}},
			changedFiles: []string{"src/main.go"},
			want:         true,
		},
		{
			name:         "neither key present always runs",
			rule:         Rule{},
			changedFiles: []string{"anything.txt"},
			want:         true,
		},
		{
			name:         "neither key present always runs even with no changed files",
			rule:         Rule{},
			changedFiles: nil,
			want:         true,
		},
		{
			name:         "globs present, matching file",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: []string{"src/a/b.ts"},
			want:         true,
		},
		{
			name:         "globs present, non-matching file",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: []string{"docs/readme.md"},
			want:         false,
		},
		{
			name:         "globs present, no changed files",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: nil,
			want:         false,
		},
		{
			name:         "src/** matches direct child",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: []string{"src/a.ts"},
			want:         true,
		},
		{
			name:         "src/** matches deeply nested path",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: []string{"src/a/b/c/d.ts"},
			want:         true,
		},
		{
			name:         "**/*.ts matches nested path",
			rule:         Rule{Globs: []string{"**/*.ts"}},
			changedFiles: []string{"a/b/c.ts"},
			want:         true,
		},
		{
			name:         "**/*.ts matches top-level file",
			rule:         Rule{Globs: []string{"**/*.ts"}},
			changedFiles: []string{"c.ts"},
			want:         true,
		},
		{
			name:         "**/*.ts does not match wrong extension",
			rule:         Rule{Globs: []string{"**/*.ts"}},
			changedFiles: []string{"a/b/c.go"},
			want:         false,
		},
		{
			name:         "one of multiple changed files matches",
			rule:         Rule{Globs: []string{"src/**"}},
			changedFiles: []string{"docs/readme.md", "src/a.ts"},
			want:         true,
		},
		{
			name:         "one of multiple patterns matches",
			rule:         Rule{Globs: []string{"docs/**", "src/**"}},
			changedFiles: []string{"src/a.ts"},
			want:         true,
		},
		{
			name:         "alwaysCheck false with globs behaves like globs-only",
			rule:         Rule{AlwaysCheck: false, Globs: []string{"src/**"}},
			changedFiles: []string{"docs/readme.md"},
			want:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Matches(tc.changedFiles); got != tc.want {
				t.Errorf("Matches(%v) = %v, want %v", tc.changedFiles, got, tc.want)
			}
		})
	}
}

func TestMatchRules(t *testing.T) {
	rules := []Rule{
		{Name: "always", AlwaysCheck: true},
		{Name: "scoped-src", Globs: []string{"src/**"}},
		{Name: "scoped-docs", Globs: []string{"docs/**"}},
		{Name: "unscoped"},
	}

	got := MatchRules(rules, []string{"src/a.ts"})

	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}

	want := []string{"always", "scoped-src", "unscoped"}
	if len(names) != len(want) {
		t.Fatalf("MatchRules() names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("MatchRules() names = %v, want %v", names, want)
		}
	}
}

func TestLoadRules_EndToEnd_GlobsStringVsList(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "string-glob.md", "---\nglobs: 'src/**'\n---\nBody.\n")
	writeRule(t, dir, "list-glob.md", "---\nglobs:\n  - 'src/**'\n  - 'test/**'\n---\nBody.\n")

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("len(rules) = %d, want 2", len(rules))
	}

	byName := map[string]Rule{}
	for _, r := range rules {
		byName[r.Name] = r
	}

	if got := byName["string-glob"].Globs; len(got) != 1 || got[0] != "src/**" {
		t.Errorf("string-glob Globs = %v, want [src/**]", got)
	}
	if got := byName["list-glob"].Globs; len(got) != 2 || got[0] != "src/**" || got[1] != "test/**" {
		t.Errorf("list-glob Globs = %v, want [src/** test/**]", got)
	}
}

func TestLoadRules_NoFrontmatterFileAlwaysRuns(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "freeform.md", "# Just prose\n\nNo frontmatter here.\n")

	rules, err := LoadRules(dir)
	if err != nil {
		t.Fatalf("LoadRules() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	r := rules[0]
	if r.AlwaysCheck {
		t.Errorf("AlwaysCheck = true, want false (key absent)")
	}
	if len(r.Globs) != 0 {
		t.Errorf("Globs = %v, want empty", r.Globs)
	}
	if r.Body != "# Just prose\n\nNo frontmatter here." {
		t.Errorf("Body = %q, unexpected", r.Body)
	}
	if !r.Matches([]string{"anything/at/all.xyz"}) {
		t.Errorf("Matches() = false, want true for unscoped rule with no frontmatter")
	}
	if !r.Matches(nil) {
		t.Errorf("Matches(nil) = false, want true for unscoped rule with no frontmatter")
	}
}
