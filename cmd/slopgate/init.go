package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/evermake/slopgate/internal/config"
	"github.com/evermake/slopgate/internal/git"
	"github.com/evermake/slopgate/internal/model"
	"github.com/evermake/slopgate/internal/scaffold"
	"github.com/evermake/slopgate/internal/store"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	fs.Parse(args)

	ctx := context.Background()
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := git.RepoRoot(ctx, cwd)
	if err != nil {
		return fmt.Errorf("not inside a git repository: %w", err)
	}

	st, err := store.Open("")
	if err != nil {
		return err
	}

	// Reuse the existing identity when re-running. init is idempotent: it
	// refreshes hooks and the default branch without minting a new repo_id,
	// which would orphan every run recorded so far.
	repoID, err := reuseOrMintRepoID(root)
	if err != nil {
		return err
	}

	upstream, err := git.GetRemoteURL(ctx, root, "origin")
	if err != nil {
		upstream = ""
	}

	gateRepo := st.GateRepoPath(repoID)
	if _, err := os.Stat(gateRepo); os.IsNotExist(err) {
		if err := git.InitBare(ctx, gateRepo); err != nil {
			return fmt.Errorf("create gate repo: %w", err)
		}
	}

	// The gate repo's own origin is what a run worktree fetches the base
	// branch through, so it inherits the developer's existing credentials and
	// slopgate needs no credential handling of its own.
	if upstream != "" {
		if err := git.AddRemote(ctx, gateRepo, "origin", upstream); err != nil {
			return fmt.Errorf("point gate repo at upstream: %w", err)
		}
	}

	defaultBranch := git.DefaultBranch(ctx, root)

	// Seed the gate repo so the first push transfers a delta instead of the
	// entire history, and so merge-base works even if a later fetch fails.
	if upstream != "" {
		if err := git.Fetch(ctx, gateRepo, "origin", defaultBranch, 120*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "slopgate: warning: could not pre-fetch origin/%s: %v\n", defaultBranch, err)
		}
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the slopgate binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	if err := scaffold.InstallHooks(gateRepo, scaffold.HookParams{Binary: self, RepoID: repoID}); err != nil {
		return err
	}

	// The named remote is the consent boundary: gating happens only when you
	// explicitly push to it.
	if err := git.AddRemote(ctx, root, "slopgate", gateRepo); err != nil {
		return fmt.Errorf("add slopgate remote: %w", err)
	}

	created, err := scaffold.EnsureRepoConfig(root)
	if err != nil {
		return fmt.Errorf("scaffold .slopgate: %w", err)
	}
	if err := config.WriteRepoID(root, repoID); err != nil {
		return err
	}

	if err := st.SaveRepo(model.Repo{
		ID:            repoID,
		Path:          root,
		GateRepo:      gateRepo,
		DefaultBranch: defaultBranch,
		UpstreamURL:   upstream,
	}); err != nil {
		return err
	}

	fmt.Printf("slopgate: registered %s\n", root)
	fmt.Printf("  repo id        %s\n", repoID)
	fmt.Printf("  gate repo      %s\n", gateRepo)
	fmt.Printf("  default branch %s\n", defaultBranch)
	if len(created) > 0 {
		fmt.Printf("  scaffolded     .slopgate/ (%d files)\n", len(created))
	}
	fmt.Printf("\nNext:\n  slopgate daemon          # in another terminal\n  git push slopgate HEAD\n  slopgate wait\n")
	return nil
}

// reuseOrMintRepoID returns the repo id already on disk, or mints one when
// the file is absent. Any other read error (permissions, empty/corrupt file)
// is returned so init cannot silently orphan run history.
func reuseOrMintRepoID(root string) (string, error) {
	repoID, err := config.ReadRepoID(root)
	if err == nil {
		return repoID, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return store.NewID(), nil
	}
	return "", fmt.Errorf("read repo id: %w", err)
}
