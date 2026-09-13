#!/usr/bin/env bash
# Deterministic checks. Anything a tool can decide on its own belongs here,
# not in a rule -- rules cost an LLM call and should be spent on judgement.
set -uo pipefail

status=0

echo "==> gofmt"
# Scoped to tracked files: `gofmt -l .` would also descend into .local/, the
# gitignored prior-art checkout, which is not ours to format.
unformatted="$(git ls-files '*.go' | xargs gofmt -l 2>&1)"
if [ -n "$unformatted" ]; then
  echo "not gofmt'd:"
  echo "$unformatted"
  status=1
fi

echo "==> go vet"
go vet ./... || status=1

echo "==> go build"
go build ./... || status=1

exit $status
