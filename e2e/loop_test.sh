#!/usr/bin/env bash
# slopgate end-to-end smoke test. Proves the push -> daemon -> wait loop.
set -u
SCRATCH="${SLOPGATE_E2E_ROOT:-/private/tmp/sg-e2e}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SG="$SCRATCH/slopgate"
mkdir -p "$SCRATCH"
go build -o "$SG" "$REPO/cmd/slopgate" || exit 1
ROOT="$SCRATCH/e2e"
rm -rf "$ROOT"; mkdir -p "$ROOT"

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export SLOPGATE_HOME="$ROOT/home"
PASS=0; FAIL=0
ok(){ echo "  PASS  $1"; PASS=$((PASS+1)); }
no(){ echo "  FAIL  $1"; echo "        $2"; FAIL=$((FAIL+1)); }

# --- fixture: upstream + clone on main ---
git init -q -b main --bare "$ROOT/up.git"
git init -q -b main "$ROOT/work"
cd "$ROOT/work"
git config user.email t@t; git config user.name t
git remote add origin "$ROOT/up.git"
mkdir -p src
printf 'export function a() {\n  return 1\n}\n' > src/a.ts
git add -A && git commit -qm init && git push -q origin main

echo "=== 1. init ==="
"$SG" init > "$ROOT/init.log" 2>&1 && ok "init succeeded" || no "init" "$(tail -3 "$ROOT/init.log")"
[ -f .slopgate/repo-id ] && ok "repo-id written" || no "repo-id" "missing"
git remote get-url slopgate >/dev/null 2>&1 && ok "slopgate remote added" || no "remote" "missing"
# Don't gate on the scaffolded example rule for the baseline runs.
rm -f .slopgate/rules/example.md
git add -A && git commit -qm "slopgate config" -q

echo "=== 2. push with daemon DOWN is rejected ==="
git push -q slopgate HEAD > "$ROOT/push_down.log" 2>&1
rc=$?
[ $rc -ne 0 ] && ok "push rejected (exit $rc)" || no "push rejected" "exit 0, expected non-zero"
grep -q "daemon is not running" "$ROOT/push_down.log" && ok "message tells user to start daemon" || no "message" "$(cat "$ROOT/push_down.log")"

echo "=== 3. daemon starts and holds singleton ==="
"$SG" daemon > "$ROOT/daemon.log" 2>&1 &
DPID=$!
for i in $(seq 1 50); do [ -S "$SLOPGATE_HOME/daemon.sock" ] && break; sleep 0.1; done
[ -S "$SLOPGATE_HOME/daemon.sock" ] && ok "daemon listening" || no "daemon" "$(cat "$ROOT/daemon.log")"
"$SG" daemon > "$ROOT/daemon2.log" 2>&1
[ $? -ne 0 ] && ok "second daemon refused" || no "singleton" "second daemon started"
grep -qi "already running" "$ROOT/daemon2.log" && ok "singleton message clear" || no "singleton msg" "$(cat "$ROOT/daemon2.log")"

echo "=== 4. happy path: push then IMMEDIATELY wait (the race) ==="
git checkout -q -b feat/one
printf 'export function b() {\n  return 2\n}\n' > src/b.ts
git add -A && git commit -qm "add b" -q
git push -q slopgate HEAD > "$ROOT/push1.log" 2>&1 && ok "push accepted" || no "push" "$(cat "$ROOT/push1.log")"
grep -q "slopgate wait" "$ROOT/push1.log" && ok "post-receive printed next action" || no "hook output" "$(cat "$ROOT/push1.log")"
"$SG" wait --timeout 3m > "$ROOT/wait1.log" 2>&1
rc=$?
grep -qi "no gate run\|unknown" "$ROOT/wait1.log" && no "RACE: wait beat registration" "$(head -3 "$ROOT/wait1.log")" || ok "no race: run was registered before push returned"
[ $rc -eq 0 ] && ok "verdict PASSED (exit 0)" || no "verdict" "exit $rc: $(head -5 "$ROOT/wait1.log")"

echo "=== 5. failing check.sh -> FAILED, exit 1 ==="
git checkout -q -b feat/two
printf '#!/usr/bin/env bash\necho "lint: src/b.ts:1 no-explicit-any"\nexit 1\n' > .slopgate/scripts/check.sh
printf 'export function c() {\n  return 3\n}\n' > src/c.ts
git add -A && git commit -qm "add c, failing check" -q
git push -q slopgate HEAD > "$ROOT/push2.log" 2>&1
"$SG" wait --timeout 3m > "$ROOT/wait2.log" 2>&1
rc=$?
[ $rc -eq 1 ] && ok "verdict FAILED (exit 1)" || no "verdict" "exit $rc: $(head -5 "$ROOT/wait2.log")"
grep -q "no-explicit-any" "$ROOT/wait2.log" && ok "check.sh log reached the feedback" || no "log tail" "$(head -20 "$ROOT/wait2.log")"
grep -qi "Gate config changed" "$ROOT/wait2.log" && ok "config drift reported (check.sh was modified)" || no "drift" "drift block absent"

kill $DPID 2>/dev/null; wait $DPID 2>/dev/null
echo
echo "=== $PASS passed, $FAIL failed ==="
exit $FAIL
