---
# Check this rule on every run, whatever changed.
alwaysCheck: false
# Otherwise: only check when a changed file matches one of these patterns.
globs:
  - 'src/**'
---

# No silent error swallowing

Do not catch an error and discard it. An empty catch block, or one that only
logs and continues, turns a real failure into corrupted state further down.

Either handle the error meaningfully, or let it propagate to a caller that can.

Delete this file -- it is a scaffold example, not a rule slopgate imposes on you.
