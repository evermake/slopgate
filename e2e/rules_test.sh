#!/usr/bin/env bash
# Proves the rule engine: glob matching, agent invocation, and above all the
# citation filter that discards hallucinated findings.
set -u
SCRATCH="${SLOPGATE_E2E_ROOT:-/private/tmp/sg-e2e}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SG="$SCRATCH/slopgate"
mkdir -p "$SCRATCH"
go build -o "$SG" "$REPO/cmd/slopgate" || exit 1
ROOT="$SCRATCH/r"
rm -rf "$ROOT"; mkdir -p "$ROOT"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export SLOPGATE_HOME="$ROOT/h"
export SLOPGATE_CLAUDE_BIN="$REPO/e2e/stubclaude"
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
no(){ echo "  FAIL  $1"; echo "        $2"; FAIL=$((FAIL+1)); }

git init -q -b main --bare "$ROOT/up.git"
git init -q -b main "$ROOT/w"; cd "$ROOT/w"
git config user.email t@t; git config user.name t
git remote add origin "$ROOT/up.git"
mkdir -p src; echo 'export const z = 0' > src/z.ts
git add -A && git commit -qm init && git push -q origin main
"$SG" init >/dev/null 2>&1
rm -f .slopgate/rules/example.md
cat > .slopgate/rules/no-casts.md <<'RULE'
---
globs:
  - 'src/**/*.ts'
---
# No type assertions
Do not use `as X` to force a value into a type. Narrow with a type guard.
RULE
git add -A && git commit -qm "add rule" -q && git push -q origin main

export STUB_RESPONSE="$REPO/e2e/resp_mixed.json"
"$SG" daemon > "$ROOT/d.log" 2>&1 &
DPID=$!
for i in $(seq 1 50); do [ -S "$SLOPGATE_HOME/daemon.sock" ] && break; sleep 0.1; done

echo "=== rule fires, real finding kept, hallucinations discarded ==="
git checkout -q -b feat/cast
printf 'export function d(x: unknown) {\n  return x as User\n}\n' > src/d.ts
git add -A && git commit -qm "add d" -q
git push -q slopgate HEAD >/dev/null 2>&1
"$SG" wait --timeout 3m > "$ROOT/wait.log" 2>&1
rc=$?
[ $rc -eq 1 ] && ok "FAILED on a real violation (exit 1)" || no "verdict" "exit $rc: $(head -20 "$ROOT/wait.log")"
grep -q "REAL:" "$ROOT/wait.log" && ok "valid finding reached the feedback" || no "finding" "$(head -30 "$ROOT/wait.log")"
grep -q "HALLUCINATION" "$ROOT/wait.log" && no "FILTER LEAK: a hallucinated finding reached the user" "$(grep -n HALLUCINATION "$ROOT/wait.log" | head -3)" || ok "both hallucinations filtered out of the feedback"
grep -q "Rule: \`.slopgate/rules/no-casts.md\`" "$ROOT/wait.log" && ok "rule file path pointed to alongside the violation" || no "rule path" "rule file pointer absent from feedback"

SHA=$(git rev-parse HEAD); RID=$(cat .slopgate/repo-id)
RJ="$SLOPGATE_HOME/runs/$RID/$SHA/run.json"
python3 - "$RJ" <<'PY'
import json,sys
r=json.load(open(sys.argv[1]))
rule=r["rules"][0]
kept=len(rule.get("findings") or []); disc=rule.get("discarded") or []
print(f"  kept={kept} discarded={len(disc)} reasons={[d['reason'] for d in disc]}")
assert kept==1, f"expected 1 kept, got {kept}"
assert len(disc)==2, f"expected 2 discarded, got {len(disc)}"
assert {d["reason"] for d in disc}=={"file_not_in_changed_set","snippet_not_at_line"}, disc
print("  PASS  run.json records exactly 2 discards with the right reasons")
PY
[ $? -eq 0 ] && PASS=$((PASS+1)) || { FAIL=$((FAIL+1)); echo "  FAIL  discard bookkeeping"; }

echo "=== clean agent response -> PASSED ==="
export STUB_RESPONSE="$REPO/e2e/resp_clean.json"
kill $DPID 2>/dev/null; wait $DPID 2>/dev/null
"$SG" daemon > "$ROOT/d2.log" 2>&1 &
DPID=$!
for i in $(seq 1 50); do [ -S "$SLOPGATE_HOME/daemon.sock" ] && break; sleep 0.1; done
git checkout -q -b feat/clean
printf 'export function e() {\n  return 5\n}\n' > src/e.ts
git add -A && git commit -qm "add e" -q
git push -q slopgate HEAD >/dev/null 2>&1
"$SG" wait --timeout 3m > "$ROOT/wait2.log" 2>&1
[ $? -eq 0 ] && ok "PASSED when the rule finds nothing (exit 0)" || no "clean verdict" "$(head -10 "$ROOT/wait2.log")"

echo "=== broken agent output is ERRORED, never PASSED ==="
echo 'not json at all' > "$ROOT/resp_bad.json"
export STUB_RESPONSE="$ROOT/resp_bad.json"
kill $DPID 2>/dev/null; wait $DPID 2>/dev/null
"$SG" daemon > "$ROOT/d3.log" 2>&1 &
DPID=$!
for i in $(seq 1 50); do [ -S "$SLOPGATE_HOME/daemon.sock" ] && break; sleep 0.1; done
git checkout -q -b feat/broken
printf 'export function f() {\n  return 6\n}\n' > src/f.ts
git add -A && git commit -qm "add f" -q
git push -q slopgate HEAD >/dev/null 2>&1
"$SG" wait --timeout 3m > "$ROOT/wait3.log" 2>&1
rc=$?
[ $rc -eq 2 ] && ok "ERRORED on garbage output (exit 2, not a silent pass)" || no "garbage handling" "exit $rc: $(head -10 "$ROOT/wait3.log")"

kill $DPID 2>/dev/null; wait $DPID 2>/dev/null
echo; echo "=== $PASS passed, $FAIL failed ==="
exit $FAIL
