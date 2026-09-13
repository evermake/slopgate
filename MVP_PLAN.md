# slopgate — MVP Implementation Plan

Companion to [DESIGN.md](./DESIGN.md), which holds the decisions and their rationale. This
doc holds the exact artifacts, interfaces and defaults needed to build. Where the two
disagree, DESIGN.md is wrong and should be updated.

Prior-art references live in `.local/` (**gitignored**, so absent from a fresh clone): the
upstream `no-mistakes` source checkout at `.local/no-mistakes/`, and a feature-by-feature
analysis of it at `.local/NO_MISTAKES_LEARNINGS.md`. Useful for background; **not required
to build anything below.** Everything this plan specifies is self-contained.

## MVP definition

**One sentence:** an agent commits, pushes to the `slopgate` remote, runs `slopgate wait`,
and gets back a verdict plus actionable feedback derived from `.slopgate/rules/` and the
repo's own check/test scripts.

Success is that loop working end to end on one repo, on macOS, with Claude as the only
driver. Everything else is later.

### In scope

- `slopgate init`, `slopgate daemon`, `slopgate wait`, `slopgate status`
- Bare gate repo + `pre-receive` (daemon liveness, rejects push) and `post-receive`
  (synchronous run registration) hooks
- Single-daemon enforcement via an exclusive OS file lock
- Fresh worktree per run; `setup.sh`, `check.sh`, `test.sh`
- Base-branch fetch + merge-base diff
- Gate-config drift detection against the merge-base
- Rule matching by `globs` / `alwaysCheck`
- One `claude -p` invocation per matched rule, bounded by `maxConcurrentAgents`
- Citation validation of findings
- `feedback.md` in the store; stdout delivery
- Plain (non-TTY) output with correct exit codes
- Run state persisted as JSON files, durable across daemon restart

### Deferred, deliberately

| Deferred | Why it's safe to defer |
|---|---|
| **bubbletea TUI** | The agent path is the product; the TUI is for humans watching. Nothing in the core loop needs it, and it can't be built until live run state exists. M6. |
| **Suppression** | slopgate can't block a push, so a repeated finding costs noise, not progress. Revisit if noise makes feedback go unread. |
| **Trusted-branch gate config** | Replaced by drift reporting, which preserves rule authoring on branches. |
| **In-repo feedback copy** | stdout + store path covers it without writing to the repo. |
| **Result caching** | Correctness doesn't depend on it, only cost. The cache key is specified below so it slots in without rework. |
| **Supersede / cancel** | Redundant runs waste money, not correctness. |
| **Crash recovery** | MVP marks any `running` run `errored` on daemon start. Re-push to retry. |
| **launchd install, daemon log files** | Daemon is started by hand in a terminal; its logs are that terminal's output. |
| **SQLite** | JSON files per run are enough until queries get real. |
| **`on_verdict` hook, pruning, metrics** | Not needed for the loop to work. |
| **Unpushed-local-base detection** | Real correctness issue, but needs the core loop to exist first. M5. |

## Store layout

```
~/.slopgate/
  daemon.sock                      # unix socket
  daemon.lock                      # exclusive OS lock, held for daemon lifetime
  repos.json                       # registry
  repos/<repo_id>.git/             # bare gate repo, has `origin` -> real upstream
  worktrees/<repo_id>/<run_id>/    # transient, removed on run end
  runs/<repo_id>/<sha>/
    run.json                       # state, timings, verdict
    feedback.md                    # canonical feedback artifact
    logs/{setup,check,test}.log    # combined stdout+stderr, capped at 1 MiB
    rules/<rule-name>.json         # raw structured_output + usage per rule
```

`repos.json`:

```json
{
  "repos": [
    {
      "id": "01JB2X...",
      "path": "/Users/me/code/myproject",
      "gate_repo": "/Users/me/.slopgate/repos/01JB2X....git",
      "default_branch": "main",
      "upstream_url": "git@github.com:me/myproject.git"
    }
  ]
}
```

`repo_id` is a generated ULID, written to `.slopgate/repo-id` at init and used as the
identity everywhere. Path is a lookup convenience only — it changes when the repo moves.

### Run states

```
queued -> running -> passed | failed | errored | cancelled
```

- `failed` — the gate ran fully and found problems. Normal, expected outcome.
- `errored` — slopgate itself could not complete (setup.sh failed, malformed agent output,
  worktree creation failed). Distinct from `failed` because it is not a statement about
  the user's code.

## CLI surface

| Command | Behaviour |
|---|---|
| `slopgate init` | Create gate repo, generate `repo_id`, write `.slopgate/repo-id`, add `slopgate` git remote, set gate repo's `origin` to the real upstream, resolve default branch via `git ls-remote --symref origin HEAD` (fallback `main`), install both hooks, register in `repos.json`, scaffold `.slopgate/` if absent. |
| `slopgate daemon` | Run daemon in foreground; logs to stdout. Acquires `daemon.lock` (non-blocking `flock`, `LOCK_EX`); exits non-zero with `already running` if another holds it. Unlinks a stale socket before binding. |
| `slopgate wait [<sha>]` | Default `HEAD`. Block until verdict. Exit `0` passed, `1` failed, `2` error. Flags: `--timeout` (default 30m), `--json`, `--plain`. |
| `slopgate status [<sha>]` | Non-blocking current state. Same exit codes; exit `2` if still running. |

`init` is idempotent — re-running against an already-registered repo refreshes the hook and
default branch without regenerating `repo_id`.

## IPC

JSON-RPC 2.0 over `~/.slopgate/daemon.sock`.

| Method | Params | Returns |
|---|---|---|
| `daemon.ping` | — | `{"ok": true}` |
| `run.submit` | `{repo_id, ref, sha}` | `{run_id}` — **returns only after the run is durably registered** |
| `run.get` | `{repo_id, sha}` | run.json contents |
| `run.wait` | `{repo_id, sha, timeout_ms}` | terminal run.json, or timeout error |

`run.submit` returning after durable registration is what makes the push→wait sequence
race-free. See DESIGN.md § Submit & identity.

## Hooks

Both are installed in the bare gate repo by `init`.

**`pre-receive`** — calls `daemon.ping`. On failure, exit non-zero so the push is
**rejected**:

```
remote: slopgate: daemon is not running
remote:   start it with:  slopgate daemon
```

**`post-receive`** — calls `run.submit` and waits for it to return, then prints:

```
slopgate: gate started for a3f9c21
slopgate: run `slopgate wait a3f9c21` for the verdict
```

The split is forced by git: `post-receive` runs after refs are updated and its exit code is
ignored, so only `pre-receive` can reject. But registration cannot move to `pre-receive`,
because since git 2.11 pushed objects sit in a quarantine directory until `pre-receive`
succeeds — the daemon, a separate process, cannot yet read the commit.

If the daemon dies between the two hooks, the push has already succeeded and `post-receive`
can only print an error. Rare, accepted, not a bug.

## Gate run algorithm

1. **Worktree.** `git worktree add --detach <worktree> <sha>` from the bare gate repo.
2. **Fetch base.** `git fetch origin <default_branch>` in the worktree, 120s timeout. On
   failure: log a warning, continue.
3. **Diff.** `base = git merge-base origin/<default_branch> HEAD`. Changed files =
   `git diff --name-only <base> HEAD`. Store `base` in run.json.
4. **Gate-config drift.** `git diff --name-status <base> HEAD -- .slopgate/`. Any result is
   recorded in run.json and reported in the feedback. Does not affect the verdict.
5. **setup.sh** — if present. Non-zero exit → run `errored`, stop.
6. **Fan out in parallel:** `check.sh`, `test.sh`, and all matched rule checks
   (rules bounded by `maxConcurrentAgents`). Nothing fails fast; everything runs.
7. **Validate findings** (citation checks, below).
8. **Verdict.** `passed` iff `check.sh` ok **and** `test.sh` ok **and** zero surviving
   findings. Otherwise `failed`. Config drift never changes the verdict.
9. **Write** `feedback.md` if the verdict is `failed` **or** drift was detected. Remove
   worktree. Mark terminal.

### Script contract

- Invoked as `bash <script>`, cwd = worktree root.
- Missing script = skipped, treated as passing. Absence is not an error.
- Exit 0 = pass; anything else = fail.
- Combined stdout+stderr captured to `logs/<name>.log`, capped at 1 MiB (keep the tail —
  failure output is at the end).
- Timeout 15m each; timeout counts as failure, recorded as such.
- Env: `SLOPGATE_RUN_ID`, `SLOPGATE_SHA`, `SLOPGATE_BASE_SHA`, `SLOPGATE_CHANGED_FILES`
  (path to a newline-delimited file list).

### Rule matching

- `alwaysCheck: true` → always runs.
- `globs` → runs if any changed file matches any pattern.
- **Neither key present → the rule always runs.** A rule with no scoping is unscoped.
- Patterns match paths relative to repo root. Use `github.com/bmatcuk/doublestar/v4` —
  stdlib `filepath.Match` has no `**`.

## Rule evaluation

### Invocation

```bash
claude -p "<prompt>" \
  --output-format json \
  --json-schema "<schema>" \
  --allowedTools "Read,Glob,Grep" \
  --permission-mode dontAsk
```

cwd = worktree root. Parse `structured_output` from the JSON envelope; record `usage`
into `runs/<repo_id>/<sha>/rules/<rule-name>.json`.

Non-zero exit, unparseable envelope, or missing `structured_output` → the **run** is
`errored`. Never interpreted as "no findings".

### Schema

```json
{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file":        { "type": "string" },
          "line":        { "type": "integer" },
          "snippet":     { "type": "string" },
          "explanation": { "type": "string" }
        },
        "required": ["file", "line", "snippet", "explanation"],
        "additionalProperties": false
      }
    }
  },
  "required": ["findings"],
  "additionalProperties": false
}
```

Note there is no `rule` field: slopgate knows which rule it invoked and stamps it on the
way out. Never ask the model for information you already hold.

### Prompt template

```
You are checking a code change against ONE project rule. Report only violations of
this rule. Ignore every other quality concern.

<rule name="{{name}}">
{{rule_markdown_body}}
</rule>

The change under review is the diff between {{base_sha}} and {{head_sha}}.

Files changed:
{{changed_files}}

Diff:
{{diff}}

You may use Read, Glob and Grep to inspect the wider repository for context before
deciding. Surrounding code often determines whether something is actually a violation.

Rules for reporting:
- Report a violation ONLY if it appears in one of the files changed above.
  Pre-existing violations in untouched files are out of scope.
- Every finding must cite the exact file path, the 1-based line number, and a verbatim
  snippet copied from that file. A finding whose snippet does not appear at that
  location will be discarded.
- Explain what the rule requires and how this code departs from it. Be specific.
- If there are no violations, return an empty findings array. Finding nothing is a
  normal and expected outcome.
```

Diff is passed whole for MVP. If it exceeds a size cap, truncate per-file and note the
truncation in the prompt — do not silently drop files.

### Citation validation

Every finding is checked before it counts. Discard, recording the reason, if:

1. `file` is not in the changed-file set.
2. `file` does not exist in the worktree.
3. `snippet` does not appear within ±3 lines of `line` (compare with trailing whitespace
   trimmed per line; the model's line numbers drift by a line or two routinely).

A discarded finding never reaches `feedback.md` and never affects the verdict. Counts of
discards go in `run.json` — a rule discarding most of its findings is a signal its prose
is unclear.

Check (1) is what stops slopgate reporting the entire legacy codebase on every run.

## feedback.md format

Written when the verdict is `failed` **or** gate-config drift was detected. A PASSED run
that quietly dropped a rule must still leave an artifact saying so.

When drift is present the block below goes **first**, above the summary, on passed and
failed runs alike:

```markdown
## ⚠ Gate config changed on this branch

    M  .slopgate/scripts/check.sh
    D  .slopgate/rules/no-any.md

This verdict was computed with the branch's `.slopgate/`, not the default branch's.
```

Otherwise:

```markdown
# slopgate: FAILED

Commit `a3f9c21` on `feat/auth` · base `origin/main` @ `d4e5f6a` · 2026-09-13 14:22 UTC

## Summary

- 2 rule violations in 1 rule
- `check.sh` — FAILED
- `test.sh` — passed

## Rule violations

### no-type-casts

> Type assertions (`as X`) bypass the type checker. Narrow with a type guard instead.

**`src/api.ts:42`**

```ts
const user = data as User
```

`data` is `unknown` here and is asserted directly to `User`. The rule requires narrowing
via a type guard before use.

---

**`src/api.ts:88`**

...

## `check.sh` — exit 1

```
...last 100 lines of logs/check.log...
```
```

The blockquote under each rule heading is the rule's own prose, so the agent reading the
feedback sees the constraint alongside the violation and does not have to go open the rule
file.

## Milestones

Each milestone is independently verifiable. Do not start the next until the previous
demonstrably works.

**M1 — Walking skeleton.** `init` creates the gate repo, both hooks and the registry.
`daemon` runs, holds the singleton lock, serves IPC. Push with the daemon down is rejected;
push with it up registers a run that immediately completes `passed` with no checks. `wait`
returns exit 0. *Proves: push → daemon → wait, no races, no second daemon.*

**M2 — Worktree and scripts.** Worktree creation, base fetch, merge-base diff, config-drift
detection, the three scripts with the contract above. Verdict from script exit codes only.
*Proves: real verdicts from real repo state.*

**M3 — Rule engine.** Rule parsing, glob matching, per-rule `claude -p` with schema,
bounded concurrency, citation validation. *Proves: the core product. Expect to iterate on
the prompt here more than anywhere else.*

**M4 — Feedback.** `feedback.md` generation including the drift block, store layout, plain
output formatting, exit codes, `status`. *Proves: end-to-end MVP.*

**M5 — Hardening.** Unpushed-local-base detection, daemon restart handling, timeouts,
truncation, error paths.

**M6 — TUI.** bubbletea over live run state, with TTY auto-detection per DESIGN.md.

## Defaults

| Setting | Default |
|---|---|
| `maxConcurrentAgents` | 4 |
| Script timeout | 15m |
| Rule agent timeout | 10m |
| Base fetch timeout | 120s |
| `wait --timeout` | 30m |
| Log capture cap | 1 MiB |
| Feedback log excerpt | last 100 lines |

## Risks

- **The prompt is the product.** M3 will need several iterations against real diffs. Budget
  for it, and keep a handful of known-good and known-bad diffs to test against — informally,
  but consistently. This is where an eval harness would eventually earn its place.
- **False positives kill trust faster than false negatives.** A gate that cries wolf gets
  bypassed. Prefer under-reporting while tuning.
- **`--json-schema` combined with tool use is untested here.** Verify early in M3 that
  schema-conforming output still comes back when the model has explored with Read/Grep.
- **Cold start cost.** Each rule is a full agent spawn. Measure wall-clock on a realistic
  rule set at M3; if it's bad, caching moves up from deferred.
