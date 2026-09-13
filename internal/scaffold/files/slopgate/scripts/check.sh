#!/usr/bin/env bash
# Deterministic checks: format, lint, typecheck, build, dead-code, etc.
# Exit non-zero to fail the gate. Output is captured and the tail is shown
# back to the agent, so make failures explain themselves.
set -euo pipefail

# npm run lint
# npm run typecheck
