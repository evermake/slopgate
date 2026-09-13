package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// initRepo creates a real git repo in a fresh t.TempDir(), with a local
// identity configured so commits work on a machine with no global git
// identity, and with commit signing disabled so a machine with a global
// commit.gpgsign=true (e.g. via a signing agent) doesn't break commits here.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if _, err := Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commit(t *testing.T, dir, msg string, files ...string) string {
	t.Helper()
	ctx := context.Background()
	args := append([]string{"add"}, files...)
	if _, err := Run(ctx, dir, args...); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := Run(ctx, dir, "commit", "-q", "-m", msg); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	sha, err := Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return sha
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	writeFile(t, dir, "f.txt", "hello\n")
	sha := commit(t, dir, "init", "f.txt")

	t.Run("success trims stdout", func(t *testing.T) {
		out, err := Run(ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out != sha {
			t.Fatalf("got %q, want %q", out, sha)
		}
		if strings.ContainsAny(out, "\n\r") {
			t.Fatalf("output not trimmed: %q", out)
		}
	})

	t.Run("failure includes stderr", func(t *testing.T) {
		_, err := Run(ctx, dir, "show", "does-not-exist-ref")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		// git's stderr for a bad revision mentions the bad name.
		if !strings.Contains(err.Error(), "does-not-exist-ref") {
			t.Fatalf("error does not surface stderr: %v", err)
		}
	})
}

func TestIsZeroSHA(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"40 zeros", strings.Repeat("0", 40), true},
		{"64 zeros (sha256)", strings.Repeat("0", 64), true},
		{"empty", "", false},
		{"real sha", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", false},
		{"mixed zeros and letters", "000000000000000000000000000000000000a0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsZeroSHA(tt.in); got != tt.want {
				t.Errorf("IsZeroSHA(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBranchFromRef(t *testing.T) {
	tests := []struct{ ref, want string }{
		{"refs/heads/feat/x", "feat/x"},
		{"refs/heads/main", "main"},
		{"refs/tags/v1.0.0", "refs/tags/v1.0.0"},
		{"main", "main"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := BranchFromRef(tt.ref); got != tt.want {
			t.Errorf("BranchFromRef(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestRepoRootHeadRevParse(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	writeFile(t, dir, "f.txt", "hello\n")
	sha := commit(t, dir, "init", "f.txt")

	root, err := RepoRoot(ctx, dir)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	wantRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if gotRoot != wantRoot {
		t.Errorf("RepoRoot = %q, want %q", gotRoot, wantRoot)
	}

	head, err := Head(ctx, dir)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != sha {
		t.Errorf("Head = %q, want %q", head, sha)
	}

	rp, err := RevParse(ctx, dir, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if rp != sha {
		t.Errorf("RevParse(HEAD) = %q, want %q", rp, sha)
	}

	if _, err := RevParse(ctx, dir, "does-not-exist"); err == nil {
		t.Error("RevParse of a bad rev: expected error, got nil")
	}
}

func TestInitBare(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	path := filepath.Join(base, "nested", "dir", "repo.git")

	if err := InitBare(ctx, path); err != nil {
		t.Fatalf("InitBare: %v", err)
	}

	out, err := Run(ctx, path, "rev-parse", "--is-bare-repository")
	if err != nil {
		t.Fatalf("rev-parse --is-bare-repository: %v", err)
	}
	if out != "true" {
		t.Errorf("expected bare repo, got --is-bare-repository=%q", out)
	}
}

func TestAddRemote(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)

	if err := AddRemote(ctx, dir, "origin", "https://example.com/a.git"); err != nil {
		t.Fatalf("AddRemote (add): %v", err)
	}
	url, err := GetRemoteURL(ctx, dir, "origin")
	if err != nil {
		t.Fatalf("GetRemoteURL: %v", err)
	}
	if url != "https://example.com/a.git" {
		t.Fatalf("url = %q, want %q", url, "https://example.com/a.git")
	}

	// Idempotent: calling again with a different URL updates via set-url
	// rather than failing.
	if err := AddRemote(ctx, dir, "origin", "https://example.com/b.git"); err != nil {
		t.Fatalf("AddRemote (idempotent set-url): %v", err)
	}
	url, err = GetRemoteURL(ctx, dir, "origin")
	if err != nil {
		t.Fatalf("GetRemoteURL after update: %v", err)
	}
	if url != "https://example.com/b.git" {
		t.Fatalf("url after update = %q, want %q", url, "https://example.com/b.git")
	}
}

func TestGetRemoteURL_MissingRemote(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	if _, err := GetRemoteURL(ctx, dir, "nope"); err == nil {
		t.Error("expected error for missing remote, got nil")
	}
}

// setupGateRepo builds a source repo with one commit, then a bare "gate"
// repo that fetches from it, mimicking how the daemon's gate repo tracks a
// real upstream via `origin`.
func setupGateRepo(t *testing.T) (gateRepo, sha string) {
	t.Helper()
	ctx := context.Background()

	src := initRepo(t)
	writeFile(t, src, "f.txt", "hello\n")
	wantSHA := commit(t, src, "init", "f.txt")

	gateRepo = filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(ctx, gateRepo); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	if err := AddRemote(ctx, gateRepo, "origin", src); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := Fetch(ctx, gateRepo, "origin", "master", 10*time.Second); err != nil {
		// Default branch name depends on the local git config; try main too.
		if err2 := Fetch(ctx, gateRepo, "origin", "main", 10*time.Second); err2 != nil {
			t.Fatalf("Fetch: %v / %v", err, err2)
		}
	}
	return gateRepo, wantSHA
}

func TestAddWorktreeRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	gateRepo, sha := setupGateRepo(t)

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, gateRepo, worktreePath, sha); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worktreePath, "f.txt"))
	if err != nil {
		t.Fatalf("read worktree file: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("worktree file content = %q", data)
	}

	// Worktree is detached at sha.
	head, err := RevParse(ctx, worktreePath, "HEAD")
	if err != nil {
		t.Fatalf("RevParse HEAD in worktree: %v", err)
	}
	if head != sha {
		t.Fatalf("worktree HEAD = %q, want %q", head, sha)
	}

	if err := RemoveWorktree(ctx, gateRepo, worktreePath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree dir removed, stat err = %v", err)
	}

	list, err := Run(ctx, gateRepo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if strings.Contains(list, worktreePath) {
		t.Fatalf("worktree still listed after remove+prune: %s", list)
	}
}

func TestFetch_Success(t *testing.T) {
	ctx := context.Background()
	src := initRepo(t)
	writeFile(t, src, "f.txt", "hello\n")
	commit(t, src, "init", "f.txt")
	branch, err := Run(ctx, src, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("determine branch: %v", err)
	}

	dst := initRepo(t)
	if err := AddRemote(ctx, dst, "origin", src); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if err := Fetch(ctx, dst, "origin", branch, 10*time.Second); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := RevParse(ctx, dst, "origin/"+branch); err != nil {
		t.Fatalf("origin/%s not fetched: %v", branch, err)
	}
}

func TestFetch_Timeout(t *testing.T) {
	ctx := context.Background()
	dir := initRepo(t)
	if err := AddRemote(ctx, dir, "origin", "ssh://git@example.invalid/repo.git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}

	// A fake GIT_SSH_COMMAND that hangs, simulating an SSH connection to a
	// dead host: git spawns it and never gets a response.
	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh.sh")
	if err := os.WriteFile(fakeSSH, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	oldSSHCmd, hadSSHCmd := os.LookupEnv("GIT_SSH_COMMAND")
	os.Setenv("GIT_SSH_COMMAND", fakeSSH)
	t.Cleanup(func() {
		if hadSSHCmd {
			os.Setenv("GIT_SSH_COMMAND", oldSSHCmd)
		} else {
			os.Unsetenv("GIT_SSH_COMMAND")
		}
	})

	start := time.Now()
	err := Fetch(ctx, dir, "origin", "main", 300*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrFetchTimeout) {
		t.Fatalf("error = %v, want it to wrap ErrFetchTimeout", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Fetch took %v, expected it to return promptly after the timeout", elapsed)
	}
}

func TestDefaultBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("resolves from real remote", func(t *testing.T) {
		src := initRepo(t)
		writeFile(t, src, "f.txt", "hello\n")
		commit(t, src, "init", "f.txt")
		wantBranch, err := Run(ctx, src, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Fatalf("determine branch: %v", err)
		}

		dst := initRepo(t)
		if err := AddRemote(ctx, dst, "origin", src); err != nil {
			t.Fatalf("AddRemote: %v", err)
		}
		got := DefaultBranch(ctx, dst)
		if got != wantBranch {
			t.Fatalf("DefaultBranch = %q, want %q", got, wantBranch)
		}
	})

	t.Run("falls back to main with no remote", func(t *testing.T) {
		dir := initRepo(t)
		got := DefaultBranch(ctx, dir)
		if got != "main" {
			t.Fatalf("DefaultBranch = %q, want %q", got, "main")
		}
	})

	t.Run("falls back to main in a non-repo dir", func(t *testing.T) {
		got := DefaultBranch(ctx, t.TempDir())
		if got != "main" {
			t.Fatalf("DefaultBranch = %q, want %q", got, "main")
		}
	})
}

// setupDiffFixture builds: a base commit with a.txt, b.txt, d.txt on the
// repo's initial branch, then a "feature" branch that modifies a.txt,
// deletes b.txt, adds c.txt, and renames d.txt -> e.txt.
func setupDiffFixture(t *testing.T) (dir, base, head string) {
	t.Helper()
	ctx := context.Background()
	dir = initRepo(t)

	writeFile(t, dir, "a.txt", "base a\n")
	writeFile(t, dir, "b.txt", "base b\n")
	writeFile(t, dir, "d.txt", "content for rename testing purposes only\n")
	base = commit(t, dir, "base", "a.txt", "b.txt", "d.txt")

	if _, err := Run(ctx, dir, "checkout", "-q", "-b", "feature"); err != nil {
		t.Fatalf("checkout -b feature: %v", err)
	}

	writeFile(t, dir, "a.txt", "base a\nextra\n")
	if _, err := Run(ctx, dir, "rm", "-q", "b.txt"); err != nil {
		t.Fatalf("git rm b.txt: %v", err)
	}
	writeFile(t, dir, "c.txt", "new c\n")
	if _, err := Run(ctx, dir, "add", "a.txt", "c.txt"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := Run(ctx, dir, "mv", "d.txt", "e.txt"); err != nil {
		t.Fatalf("git mv: %v", err)
	}
	if _, err := Run(ctx, dir, "commit", "-q", "-am", "feature changes"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	sha, err := Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	head = sha
	return dir, base, head
}

func TestMergeBase(t *testing.T) {
	ctx := context.Background()
	dir, base, head := setupDiffFixture(t)

	got, err := MergeBase(ctx, dir, "master", "feature")
	if err != nil {
		// Some environments default the initial branch to "main".
		got, err = MergeBase(ctx, dir, "main", "feature")
		if err != nil {
			t.Fatalf("MergeBase: %v", err)
		}
	}
	if got != base {
		t.Errorf("MergeBase = %q, want %q", got, base)
	}
	_ = head
}

func TestChangedFiles(t *testing.T) {
	ctx := context.Background()
	dir, base, head := setupDiffFixture(t)

	got, err := ChangedFiles(ctx, dir, base, head)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"a.txt", "b.txt", "c.txt", "e.txt"}
	if !equalSlices(got, want) {
		t.Errorf("ChangedFiles = %v, want %v", got, want)
	}

	// No changes between a SHA and itself -> nil, not [""].
	none, err := ChangedFiles(ctx, dir, head, head)
	if err != nil {
		t.Fatalf("ChangedFiles (no diff): %v", err)
	}
	if none != nil {
		t.Errorf("ChangedFiles with no diff = %#v, want nil", none)
	}
}

func TestNameStatus(t *testing.T) {
	ctx := context.Background()
	dir, base, head := setupDiffFixture(t)

	got, err := NameStatus(ctx, dir, base, head)
	if err != nil {
		t.Fatalf("NameStatus: %v", err)
	}

	byPath := map[string]string{}
	for _, e := range got {
		byPath[e.Path] = e.Status
	}

	if byPath["a.txt"] != "M" {
		t.Errorf("a.txt status = %q, want M", byPath["a.txt"])
	}
	if byPath["b.txt"] != "D" {
		t.Errorf("b.txt status = %q, want D", byPath["b.txt"])
	}
	if byPath["c.txt"] != "A" {
		t.Errorf("c.txt status = %q, want A", byPath["c.txt"])
	}
	// Rename: destination path "e.txt" with the raw rename status (R100),
	// not "d.txt".
	status, ok := byPath["e.txt"]
	if !ok {
		t.Fatalf("expected an entry for e.txt (rename destination), got %v", got)
	}
	if !strings.HasPrefix(status, "R") {
		t.Errorf("e.txt status = %q, want an R### rename status", status)
	}
	if _, ok := byPath["d.txt"]; ok {
		t.Errorf("d.txt (rename source) should not appear as its own path entry")
	}

	// No changes -> nil, not a one-element slice containing "".
	none, err := NameStatus(ctx, dir, head, head)
	if err != nil {
		t.Fatalf("NameStatus (no diff): %v", err)
	}
	if none != nil {
		t.Errorf("NameStatus with no diff = %#v, want nil", none)
	}
}

func TestNameStatus_Pathspec(t *testing.T) {
	ctx := context.Background()
	dir, base, head := setupDiffFixture(t)

	got, err := NameStatus(ctx, dir, base, head, "a.txt")
	if err != nil {
		t.Fatalf("NameStatus: %v", err)
	}
	if len(got) != 1 || got[0].Path != "a.txt" {
		t.Fatalf("NameStatus with pathspec = %#v, want only a.txt", got)
	}
}

func TestDiff(t *testing.T) {
	ctx := context.Background()
	dir, base, head := setupDiffFixture(t)

	out, err := Diff(ctx, dir, base, head)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, want := range []string{"a.txt", "b.txt", "c.txt", "extra"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output missing %q:\n%s", want, out)
		}
	}

	none, err := Diff(ctx, dir, head, head)
	if err != nil {
		t.Fatalf("Diff (no diff): %v", err)
	}
	if none != "" {
		t.Errorf("Diff with no changes = %q, want empty", none)
	}
}

func equalSlices(a, b []string) bool {
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
