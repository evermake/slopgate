package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evermake/slopgate/internal/git"
)

// initRepo creates a real git repo in a fresh t.TempDir(), with a local
// identity configured so commits work on a machine with no global git
// identity.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if _, err := git.Run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	return dir
}

func commit(t *testing.T, dir, msg, file string) string {
	t.Helper()
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if _, err := git.Run(ctx, dir, "add", file); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := git.Run(ctx, dir, "commit", "-q", "-m", msg); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	sha, err := git.Run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return sha
}

// TestResolveBase_NoRefAndFetchFailed is issue #17: when the fetch of
// origin/<default> failed and there is also no local <default> ref to fall
// back on, slopgate cannot tell a genuinely first branch apart from a broken
// gate config (e.g. a stale/missing origin remote). It must refuse to guess
// by diffing against the empty tree, and error the run instead.
func TestResolveBase_NoRefAndFetchFailed(t *testing.T) {
	wt := initRepo(t)
	commit(t, wt, "only commit on some other branch", "f.txt")
	// Rename the branch away from "main" so no local "main" ref exists either.
	if _, err := git.Run(context.Background(), wt, "branch", "-m", "not-main"); err != nil {
		t.Fatalf("rename branch: %v", err)
	}

	r := &Runner{}
	_, err := r.resolveBase(context.Background(), wt, "main", true /* fetchFailed */)
	if err == nil {
		t.Fatal("resolveBase: expected an error when fetch failed and no base ref exists, got nil")
	}
	if !strings.Contains(err.Error(), "could not determine a base") {
		t.Errorf("resolveBase error = %q, want it to explain the missing base", err.Error())
	}
}

// TestResolveBase_NoRefButFetchSucceeded is the legitimate case the empty-tree
// fallback exists for: this repo's very first branch, with no default branch
// anywhere to merge-base against, but the fetch itself did not fail.
func TestResolveBase_NoRefButFetchSucceeded(t *testing.T) {
	wt := initRepo(t)
	commit(t, wt, "first commit ever", "f.txt")
	if _, err := git.Run(context.Background(), wt, "branch", "-m", "not-main"); err != nil {
		t.Fatalf("rename branch: %v", err)
	}

	r := &Runner{}
	base, err := r.resolveBase(context.Background(), wt, "main", false /* fetchFailed */)
	if err != nil {
		t.Fatalf("resolveBase: unexpected error: %v", err)
	}
	if base != git.EmptyTreeSHA {
		t.Errorf("resolveBase = %q, want the empty tree %q", base, git.EmptyTreeSHA)
	}
}

// TestResolveBase_FetchFailedButLocalRefExists covers the documented offline
// case: fetching origin/<default> failed, but a local <default> ref from an
// earlier successful fetch is still around, so the run gates against the
// last known base instead of erroring.
func TestResolveBase_FetchFailedButLocalRefExists(t *testing.T) {
	wt := initRepo(t)
	baseSHA := commit(t, wt, "base", "f.txt")
	if _, err := git.Run(context.Background(), wt, "branch", "-m", "main"); err != nil {
		t.Fatalf("rename branch: %v", err)
	}
	if _, err := git.Run(context.Background(), wt, "checkout", "-q", "-b", "feature"); err != nil {
		t.Fatalf("checkout -b feature: %v", err)
	}
	commit(t, wt, "feature work", "g.txt")

	r := &Runner{}
	base, err := r.resolveBase(context.Background(), wt, "main", true /* fetchFailed */)
	if err != nil {
		t.Fatalf("resolveBase: unexpected error: %v", err)
	}
	if base != baseSHA {
		t.Errorf("resolveBase = %q, want the local main ref's merge-base %q", base, baseSHA)
	}
}
