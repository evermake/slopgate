# AGENTS.md

Instructions for coding agents working in this repo. This repo dogfoods
slopgate on itself: `.slopgate/` at the root gates changes to slopgate's own
source.

## Remotes

- `origin` — the real GitHub repo (`evermake/slopgate`). PRs are opened
  against `origin/main`.
- `slopgate` — a local bare gate repo (`~/.slopgate/repos/<id>.git`). Pushing
  here runs the gate (`check.sh`, `test.sh`, and any matched rules under
  `.slopgate/rules/`) via a daemon (`slopgate daemon`, keep it running in its
  own terminal). This is *not* GitHub — it never gets a PR.

## Workflow for any code change

1. **Branch.** Never commit on `main`, and never commit in detached HEAD.
   Always start a branch: `git checkout -b fix/<issue>-<slug>` (or
   `feat/<slug>` for non-issue work). See PR #15 / branch
   `fix/5-read-repoid-error` for the naming convention.
2. **Commit.** If the change fixes a tracked issue, put `Fixes #<N>` in the
   commit body (and later the PR body) — GitHub auto-closes the issue when
   the PR merges. Do **not** manually close or comment "fixed in `<sha>`" on
   the issue yourself; that's premature until the PR actually merges, and
   duplicates what `Fixes #<N>` does automatically.
3. **Gate it.** Push the branch to the `slopgate` remote and wait for the
   verdict *before* pushing to `origin` or opening a PR:
   ```
   git push slopgate HEAD
   slopgate wait
   ```
   If the daemon isn't running, start it first (`slopgate daemon`, own
   terminal/background process). On FAILED, fix and re-push to `slopgate`
   until it PASSES. Do not skip this step — it's the entire point of the
   tool, and skipping it on slopgate's own repo defeats the dogfooding.
4. **Push and open a PR.** Only after a PASSED verdict:
   ```
   git push -u origin <branch>
   gh pr create --title "..." --body "...\n\nFixes #<N>"
   ```
5. **Never push directly to `main`** or merge without review, even after the
   gate passes — the gate checks code quality, not whether the change is
   wanted.

## Mistakes to avoid (made once, don't repeat)

- Committing directly on `main`/detached HEAD instead of a feature branch.
- Skipping `git push slopgate HEAD` + `slopgate wait` and going straight to
  a PR.
- Manually closing/commenting on the GitHub issue instead of letting
  `Fixes #<N>` in the PR handle it on merge.
