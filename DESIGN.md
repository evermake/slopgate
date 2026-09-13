# slopgate — Design

Working design doc. Every decision here is settled; what was deliberately postponed is in
§ Deferred at the end, with the reason it was safe to postpone.

### Local references

The closest prior art is **no-mistakes**, a tool with a similar premise and a different set
of bets. Two references sit in `.local/`, which is **gitignored** — present on the author's
machine, absent from a fresh clone:

| Path | What it is |
|---|---|
| `.local/no-mistakes/` | Full source checkout of the upstream project. |
| `.local/NO_MISTAKES_LEARNINGS.md` | Feature-by-feature analysis of it: what each feature does, the idea behind its implementation, and a code pointer. |

Source paths cited in this document (e.g. `internal/pipeline/steps/common_git.go:190`) are
relative to `.local/no-mistakes/`. If that directory is absent, treat those citations as
provenance notes rather than something to go read — the decisions here stand on their own
and none of them require consulting it.

## Core constraints

These are non-negotiable and most of the design falls out of them.

1. **slopgate never modifies anything.** No commits, no fixes, no staging, no pushes to
   `origin`, no PRs, no branch rewrites. Its only output is *feedback*.
2. **slopgate is never a child of the agent process.** A daemon owns gate runs, so a run
   survives the agent's turn ending and the agent cannot manipulate or kill it.
3. **Scope ends at verified-locally.** Push, PR, CI monitoring and repair are explicitly
   out of scope.
4. **The LLM judges rules only.** Not architecture, not design, not open-ended review.

Constraints 1 and 3 together delete roughly two thirds of what no-mistakes does: the
auto-fix loop, the fixer agent, the document/lint fix steps, push, PR, attestation, CI
monitoring, and with them the entire custody / private-mirror-reconciliation /
force-push-proof cluster. That machinery exists because no-mistakes rewrites history and
forwards branches. slopgate does neither.

## Stack

**Go**, with **bubbletea** for the CLI's TUI mode.

Go earns its place here on specifics, not taste: a single static binary makes daemon
install trivial, `os/exec` plus process groups gives the control needed to spawn and reap N
concurrent `claude` processes, and goroutines make the bounded-parallel rule fan-out
natural. It is also what no-mistakes is written in, so its `internal/git` helpers,
`internal/procreap`, and JSON-RPC-over-Unix-socket IPC are reference implementations we can
read directly rather than translate.

Notes that follow from the choice:

- **Bounded fan-out**: `errgroup` with `SetLimit(maxConcurrentAgents)`. Do *not* use
  `errgroup.WithContext` and return errors from the per-rule funcs — that cancels siblings
  on the first failure, which contradicts "everything runs; nothing fails fast". Collect
  per-rule results (including failures) and reduce at the end.
- **Process groups**: spawn rule-check agents with `Setpgid` and kill the whole group on
  cancel. A superseded run must not leave orphaned `claude` processes behind.
- **Storage**: file artifacts (`feedback.md`, raw check/test logs) on disk under the run
  directory; SQLite for run/finding metadata and cache lookups. Use a pure-Go driver
  (`modernc.org/sqlite`) — a cgo driver forfeits the static-binary and
  easy-cross-compilation properties that motivated Go in the first place. **OPEN** whether
  v1 needs SQLite at all, or whether a JSON index file suffices until queries get real.
- **IPC**: JSON-RPC 2.0 over a Unix socket between CLI and daemon, per no-mistakes.

## Shape

```
agent/dev                  slopgate daemon                 slopgate CLI
    |                            |                              |
    | git push slopgate feat/x   |                              |
    |--------------------------->|                              |
    |   pre-receive: daemon up?  |  (down -> push REJECTED)     |
    |   post-receive: register   |                              |
    |   prints: run `slopgate wait <sha>`                       |
    |                            | fresh worktree @ <sha>       |
    |                            | fetch origin/<default>       |
    |                            | merge-base -> diff           |
    |                            | setup.sh                     |
    |                            | check.sh | test.sh | rules   |
    |                            | -> verdict + findings        |
    |                            | persisted to store           |
    |                            |                              |
    | slopgate wait <sha>        |                              |
    |------------------------------------------------------->   |
    |   <- PASSED (exit 0) | FAILED (exit 1) + feedback         |
```

- **Gate repo** — a local bare repo per registered repo, added as a `slopgate` git remote
  alongside an untouched `origin`. Pushing to it is the submit action, and being an
  explicitly named remote makes it opt-in per push.
- **Daemon** — owns worktrees, run execution, persistence, crash recovery.
- **CLI** — thin client. `slopgate wait` talks to the daemon; it does not run the gate.

Because the CLI is a thin client, the agent dying mid-`wait` does not kill the run. The
verdict is durable and collectable afterwards.

## Configuration

```
.slopgate/
  settings.json    # tunables
  repo-id          # stable identity, written at init
  rules/*.md       # see Rules
  scripts/
    setup.sh       # prepare a fresh worktree
    check.sh       # deterministic checks (format, lint, typecheck, build, …)
    test.sh        # test suite
```

slopgate writes nothing into the repo after `init`. There is no in-repo feedback directory
and therefore no `.gitignore` to manage.

`settings.json` holds tunables that are not rules and not scripts:

| Key | Meaning |
|---|---|
| `maxConcurrentAgents` | Ceiling on simultaneous rule-check agent processes. |

Further settings land here as they arise (fetch/agent timeouts, `on_verdict` hook). JSON
rather than YAML so a published `$schema` gives editor completion and validation for free,
even though rule frontmatter is YAML.

### Gate config is read from the pushed branch, and drift is reported

`.slopgate/` is read from the **pushed branch**, not from a trusted default-branch commit.
Authoring rules stays easy: edit a rule on a branch, push, see it take effect.

The cost is that the gate config is agent-writable. When a run goes red, changing
`check.sh` to `exit 0`, deleting it outright (a missing script is treated as passing), or
narrowing a failing rule's `globs` all turn the gate green — and unlike an explicit
suppression, none of it announces itself.

slopgate therefore **diffs `.slopgate/` against the merge-base and reports any change
prominently in the feedback, including on a PASSED run**. It does not block, because
slopgate blocks nothing; consistent with the rest of the tool, it reports a fact and leaves
the judgement to whoever reads it. A branch that quietly disabled a rule still says so on
its own gate report.

## Submit & identity

The agent commits, then pushes to the `slopgate` remote. slopgate only ever sees real
commits — no dirty-tree snapshotting.

`post-receive` prints the next action straight into the agent's terminal:

```
slopgate: gate started for a3f9c21
slopgate: run `slopgate wait a3f9c21` for the verdict
```

This is the zero-integration feedback channel: the agent's next action is in the output it
just received, with no harness-specific wiring.

The push is never rejected. Gating is async and takes minutes; `pre-receive` cannot know
the verdict yet. The gate repo is a delivery mechanism, not a rejection point.

### Two hooks, two jobs

- **`pre-receive`** — pings the daemon. If it is not running, exit non-zero: the push is
  **rejected** with a message telling the user to start it. Nothing else happens here.
- **`post-receive`** — registers the run **synchronously** via `run.submit`, then prints the
  `slopgate wait <sha>` line.

The split is forced by git, not chosen. `post-receive` runs after refs are already updated
and its exit code is ignored, so it cannot reject anything — rejection must be
`pre-receive`. But registration cannot move to `pre-receive` either: since git 2.11 the
pushed objects sit in a **quarantine directory** until `pre-receive` succeeds, so the
daemon, a separate process, cannot yet see the commit it is being asked to gate.

Synchronous registration is not an optimisation — it closes a race otherwise built into the
primary flow. `git push` does not return until its hooks finish, so registering before
`post-receive` returns guarantees that by the time the agent reads `slopgate wait <sha>`,
that SHA is known to the daemon. The obvious alternative (fire an async notification and
return) lets `git push … && slopgate wait <sha>` reach `wait` first, and the agent gets an
"unknown SHA" error on a perfectly good gate. Registration is synchronous; the *gate run
itself* is not.

Residual race: if the daemon dies between the two hooks, the push has already succeeded and
`post-receive` can only print an error. Rare, accepted, documented so it is not mistaken for
a bug.

**Run identity** is `(repo_id, commit_sha)`. `repo_id` is a stable ID assigned at
`slopgate init` and stored in `.slopgate/` — *not* the repo path, which breaks when the
repo moves or when a second `git worktree` is in play.

**Cache key** is wider than run identity:

```
(repo_id, commit_sha, merge_base_sha, rules_hash, scripts_hash)
```

Identity alone would produce a false PASSED: gate `abc123` green, then tighten a rule,
re-gate `abc123`, cache hit, still green — against the old rule. Rules are hashed per rule
so editing one rule only invalidates that rule's result, not the whole run.

## Base branch

Adopted from no-mistakes (`internal/pipeline/steps/common_git.go:190`, `rebase.go:42`):

1. Default branch resolved once at `init` via `git ls-remote --symref origin HEAD`,
   falling back to `main`. Stored on the repo record.
2. Before each run, `git fetch origin <default>` **inside the run worktree**, so it reuses
   the dev's existing git credentials via the worktree's `origin`. No separate credential
   handling.
3. **120s hard timeout** with a distinct error cause — an SSH fetch against a dead
   connection otherwise hangs indefinitely with nothing to cancel it.
4. **Fetch failure is non-fatal**: warn and continue against whatever ref exists.
   Fail-open on freshness, fail-closed on everything else, so slopgate still works offline.

Diff is `git diff $(git merge-base origin/<default> HEAD) HEAD` — merge-base, never the
tip of `<default>`. Diffing the tip makes commits that landed on the default branch since
the branch started appear as reversed changes, firing rules on files the agent never
touched.

**Must detect:** a branch built on a *local* default branch that is ahead of
`origin/<default>` with unpushed commits. Those commits enter the merge-base diff and
slopgate reports violations on another workstream's files. This is no-mistakes' issue #283
(`rebase_local_default_test.go`); for them it silently widened a PR, for us it is the most
credibility-destroying failure mode a feedback tool has. Detect and warn at minimum.

## Run execution

Fresh worktree per run, `setup.sh` every time. Accepted cost — no warm pool, no dependency
cache, no stale state. Revisit only if it measurably hurts.

**Everything runs; nothing fails fast.** If `check.sh` fails, rules and `test.sh` still
run. Each round trip costs an agent turn, which is the expensive unit here — one verdict
carrying every finding beats three sequential red ones.

**Supersede per branch.** A newer push to the same branch cancels the in-flight run for the
older SHA. Don't burn compute gating a commit nobody is waiting on.

`check.sh` and `test.sh` run alongside the rule checks; they are independent.

## Rules

A rule is a Markdown file in `.slopgate/rules/`: YAML frontmatter configures, prose
explains.

```yaml
---
# Check on every run regardless of what changed.
alwaysCheck: false
# Only check when changed files match at least one pattern.
globs:
  - 'src/**/*.ts'
---
```

`alwaysCheck` and `globs` are the only supported keys. Deliberately minimal.

**There is no severity, on rules or on findings.** A rule is a constraint you want
enforced, not a suggestion, so every violation is fatal: any finding at all → FAILED.
Anything that shouldn't fail the gate doesn't belong in `rules/`.

### Evaluation

**One `claude -p` invocation per matched rule.** Isolated, parallel, independently
cacheable, and every finding is provably traceable to exactly one rule. Batching rules into
one prompt dilutes attention and reliably under-reports once the rule count grows.

The checker is **agentic** — it can explore the worktree for context beyond the diff hunks,
which is what avoids false positives from missing context (a cast that is fine because of a
guard twenty lines up).

```bash
claude -p "<rule prompt>" \
  --output-format json \
  --json-schema '<findings schema>' \
  --allowedTools "Read,Glob,Grep" \
  --permission-mode dontAsk
```

- `--json-schema` returns validated findings in `structured_output`, and exits 1 on an
  invalid schema so a bad schema fails loudly instead of degrading to prose.
- `--allowedTools "Read,Glob,Grep"` + `--permission-mode dontAsk` (deny anything not
  pre-approved) is **load-bearing, not hygiene**. An agentic checker with write access can
  edit the file to remove the violation and then honestly report no violations. The
  worktree is disposable so the dev loses nothing, but the verdict is silently wrong and
  the gate defeats itself. No `Bash`.
- Malformed or unparseable output **fails the run**. It is never read as "no findings" — a
  gate that silently passes on garbage is worse than no gate.
- Fresh, session-free invocation per rule. No session reuse.

**Concurrency is capped** by `maxConcurrentAgents` (see [Configuration](#configuration)).
N rules is N agent processes, each with a real cold start, all hitting rate limits
simultaneously.

### Finding shape

```
{ rule, file, line, snippet, explanation }
```

No severity (every violation fails the gate) and no `action` field (nothing auto-fixes).

**Every finding must cite `file:line` plus a quoted snippet, and any finding whose citation
does not resolve in the actual file is dropped deterministically.** Agentic exploration is
non-deterministic — the same rule against the same diff can yield different findings run to
run, and a gate that flips verdicts on identical input loses trust immediately. Citation
checking kills most hallucinated findings with no extra LLM call. Aggressive caching covers
the rest by not re-rolling the dice on unchanged inputs.

## Feedback

`slopgate wait [<sha>]` — defaults to current `HEAD`. Blocks until the verdict is ready,
returns immediately if the run already finished.

| Exit | Meaning |
|---|---|
| 0 | PASSED |
| 1 | FAILED |
| 2 | Error — unknown SHA, daemon down, timeout |

An unknown SHA errors immediately rather than hanging — safe because `post-receive`
registers the run synchronously, so any SHA the agent was told to wait on already exists.
`--timeout` has a sane default. `--json` for machine consumption.

### Output modes

`wait` has two renderings of the same run, chosen by **TTY auto-detection**:

- **TTY** → bubbletea TUI: live rule-by-rule progress, `check.sh`/`test.sh` output,
  findings as they land. For humans watching a gate run.
- **Not a TTY** → plain line-oriented output, then the feedback on completion. For agents.

Detection must be automatic, never an opt-in flag, because an agent capturing output has no
idea it should have passed one — and bubbletea's ANSI escape sequences and cursor
addressing landing in an agent's captured stdout would render the feedback unreadable,
which is the one thing this tool exists to deliver. `--plain` and `--json` force the
non-TTY path explicitly; there is no flag that forces the TUI on.

The TUI is a view over daemon state, not a driver of it. Quitting it cancels nothing — the
run continues and the verdict stays collectable, same as killing a `wait`.

**Feedback lives only in slopgate's store** (`~/.slopgate/runs/<repo_id>/<sha>/`), which is
needed for durability anyway. `wait` prints it to stdout — the zero-config path that works
with any agent, since they all read command output — and names the store path so the agent
can re-read it.

Nothing is written into the repo. An in-repo copy was considered for ergonomics and
rejected: it would require slopgate to manage a `.gitignore`, which means writing to a file
the developer owns, for a convenience stdout already provides.

Delivery beyond this is a thin pluggable layer, not core: an `on_verdict` command hook lets
a project wire its own loop (spawn a fixer, notify a queue) without slopgate having an
opinion about which agent or harness is in play. Harness-native adapters (e.g. a Claude
Code `Stop` hook that blocks the turn and injects findings — the strongest version of this
loop) come later, as adapters.

## Persistence

Daemon-owned store holds runs, per-rule results, findings, verdicts, agent invocation
metrics (tokens, duration per rule) and intermediate state, so a crash loses nothing and a
resumed daemon can tell a finished run from an abandoned one.

Per-rule cost tracking matters more here than it would elsewhere: with one invocation per
rule, "which rule is expensive" is directly attributable, which is what would justify a
per-rule model override later if cost becomes a problem.

## Carried over from no-mistakes

Kept: the fresh-session-per-check discipline; fail-the-step-on-malformed-structured-output;
bounded and recorded work; the named-remote consent boundary; the base-branch fetch
strategy; local-only detailed metrics.

Dropped: fixed 9-step pipeline, auto-fix, intent extraction, reviewer/fixer separation
(no fixer), push/PR/CI tail, custody and reconciliation, six-forge abstraction, fork
routing, forge profiles, wizard, telemetry, eval harness, agent-agnostic adapter layer
(Claude only for now).

## Daemon lifecycle

The daemon is **started manually** — `slopgate daemon`, in the foreground, typically a
separate terminal tab, before working with agents. Its logs are that terminal's output.
If it is not running, pushes are rejected by `pre-receive` with a message saying so.

**Exactly one daemon may run**, enforced by a non-blocking exclusive OS file lock on
`daemon.lock` held for the whole process lifetime. The kernel releases it even on SIGKILL,
which a PID file cannot promise — a stale PID file would let a second daemon bind the socket
and operate on another daemon's live runs. A second instance exits non-zero. On acquiring
the lock, any stale socket file is unlinked before binding.

Postponed: launchd/systemd installation, persisting logs to file, crash recovery beyond
marking interrupted runs as `errored`.

## Deferred, with reasons

- **Suppression / decision history.** A deliberately-accepted finding is re-reported on
  every subsequent run. Accepted: slopgate cannot block a push, so a repeated finding costs
  noise, not progress — in the worst case you ignore it and push, exactly as you would have
  anyway. Revisit if noise makes the feedback go unread.
- **Trusted-branch gate config.** Decided against for now in favour of reading the pushed
  branch and reporting drift (see Configuration). Revisit the moment slopgate gates a branch
  written by someone whose judgement you are not willing to inherit.
- **TUI.** Agreed stack, built last (M6). Nothing in the core loop needs it, and it cannot
  be built until there is live run state to render.
- **In-repo feedback copy.** Dropped; stdout plus the store path covers it without writing
  to the repo.
