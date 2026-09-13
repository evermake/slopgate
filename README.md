# slopgate

> Protect your project from turning into slop.

## About

Think of **slopgate** as a local, fast and flexible CI gate. An agent pushes your work forward, slopgate pushes it back.

slopgate's goal is to prevent sloppy changes from reaching the main branch and expensive CI/CD via automatic local workflow that provides feedback for your agents.

slopgate's goal is *not* to make sure your project's architecture and design decisions are correct.

## Philosophy

Stop polluting agent's context with the rules and hoping it will follow all of them. Instead, let agent do their work and slopgate will verify project rules and quality gates.

## How to use

**Step 1 — install slopgate & configure daemon**

> After this step, slopgate CLI should be available (==and some daemon maybe?==)

**Step 2 — initialize slopgate in a repo**

> After this step there should be configured `.slopgate` config directory in repo, and remote repo in slopgate ready to accept pushes

**Step 3 — configure scripts**

> Here you adjust your configuration.

## Config directory structure

- `.slopgate/`
  - `rules/` — see ["Rules"](#Rules)
    - `no-type-casts.md` 
    - …
  - `scripts/`
    - `setup.sh` — how to setup a fresh worktree
    - `check.sh` — how to run deterministic checks (format, lint, typecheck, knip, build, etc.)
    - `test.sh` — how to run test suit

## Rules

Rules are constraints you want to be enforced in the project, explained in plain Markdown. Basically, they are *very* similar to [Cursor's rules](https://cursor.com/docs/rules.md) but with a core difference: instead of polluting agent's context window and hoping it will follow them, they are enforced by a gate before reaching `main`.

Each rule is a Markdown file with YAML frontmatter:
- YAML frontmatter configures the rule
- Markdown content explains the rule

**Rule config format**

```yaml
# When set to true, the rule will be checked on each gate run.
# alwaysCheck: <boolean>

# String or array of strings with glob patterns. The rule will only be checked if changes match at least one file matching the patterns.
# globs:
#  - 'src/**'
```
