#!/usr/bin/env bash
# The Go test suite. The e2e/ suite is deliberately not run here: it builds a
# binary, starts daemons and pushes to real git remotes, which is too slow and
# too stateful for every gate run. Run ./e2e/*.sh by hand.
set -uo pipefail

go test ./internal/... ./cmd/...
