# End-to-end acceptance tests

Shell rather than Go, because what is under test is the interaction between
`git push`, real git hooks, a long-lived daemon process and the CLI — driving
that from the shell is closer to how it actually runs.

```sh
./e2e/loop_test.sh    # push -> daemon -> wait, hook rejection, singleton, drift
./e2e/rules_test.sh   # rule matching, the citation filter, error handling
```

Both build the binary themselves and are hermetic: `SLOPGATE_HOME` points at a
temp store, and `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_SYSTEM` are neutralised so the
developer's own git config (including commit signing) cannot affect a run.

`rules_test.sh` never invokes the real `claude`. It points `SLOPGATE_CLAUDE_BIN`
at `stubclaude`, which echoes whichever canned envelope `STUB_RESPONSE` names,
so a full rule evaluation costs nothing.

They default to `/private/tmp/sg-e2e` and not the repo, because a unix socket
path has a hard OS limit near 104 bytes; override with `SLOPGATE_E2E_ROOT` only
if the replacement is comparably short.
