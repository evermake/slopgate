---
globs:
  - '**/*.go'
---

# A failure must never be silently discarded

This project's whole premise is that a gate which quietly passes on garbage is
worse than no gate. The same standard applies to its own code: when an
operation can fail, the failure must either be handled, propagated, or
explicitly justified. It must never just vanish.

A violation is code that drops an error where the caller could still have acted
on it. The usual shapes:

- assigning an error to `_` when the operation's failure would change what
  happens next;
- an `if err != nil { }` block that is empty, or that only logs and then
  continues as though the call had succeeded;
- swallowing an error and returning a zero value, `nil`, or an empty result, so
  the caller cannot distinguish failure from a legitimately empty answer. This
  one is the most damaging and the easiest to miss.

## What is NOT a violation

Do not report these. Ignoring an error is often the correct choice, and a rule
that cannot tell the difference is just noise:

- **Best-effort cleanup**, typically deferred: `_ = f.Close()` on a read handle,
  `_ = os.Remove(tmp)` on an error path, `_ = conn.Close()`. There is nothing
  the caller could do with the error, and the operation has already served its
  purpose.
- **Signal and kill calls** whose target may already have exited, such as
  `_ = syscall.Kill(-pgid, syscall.SIGKILL)`. The failure means the process is
  already gone, which is the desired state.
- **Errors already recorded elsewhere**: collected into a result struct, logged
  with enough context to act on, or returned via a different path. The error is
  not lost, so it is not discarded.
- **A discard with a comment explaining why it is safe.** An author who wrote
  down the reason has made the decision deliberately. Take them at their word
  unless the stated reason is plainly wrong.

## How to fix a real violation

Return the error, wrapped with context (`fmt.Errorf("...: %w", err)`), so the
caller can decide. If dropping it genuinely is correct, say so in a short
comment on the same line or immediately above, and the finding goes away.
