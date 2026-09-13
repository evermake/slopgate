#!/usr/bin/env bash
# Prepare a fresh worktree: install dependencies, generate code, etc.
#
# slopgate creates a brand-new worktree for every run, so this starts from a
# clean checkout each time. A non-zero exit ERRORS the run (slopgate could not
# do its job) rather than FAILING it (your code has a problem).
#
# Delete this file if the project needs no setup -- a missing script is skipped,
# not an error.
set -euo pipefail

# npm ci
