Instructions for coding agents working in this repo.

## Remotes

- `origin` — the real GitHub repo (`evermake/slopgate`). PRs are opened against `origin/main`.
- `slopgate` — a local bare gate repo (`~/.slopgate/repos/<id>.git`). Pushing here runs the gate (`check.sh`, `test.sh`, and any matched rules under `.slopgate/rules/`) via a daemon (`slopgate daemon`, keep it running in its own terminal). This is *not* GitHub — it never gets a PR.

## Workflow for code changes

1. **Branch.** Never commit on `main`, and never commit in detached HEAD. Always start a branch: `git checkout -b <issue>-<slug>`.
2. **Commit.** If the change fixes a tracked issue, put `Fixes #<N>` in the commit body (and later the PR body). Do **not** manually close or comment "fixed in `<sha>`" on the issue yourself
3. **Gate it.** Push the branch to the `slopgate` remote (`git push --set-upstream slopgate <your-branch>`) and wait for the verdict *before* pushing to `origin` or opening a PR. On FAILED gate, fix and re-push to `slopgate` until it PASSES.
4. **Push and open a PR.** Only after a PASSED verdict.
5. **Never push directly to `main`** or merge without review, even after the gate passes — the gate checks code quality, not whether the change is wanted.
