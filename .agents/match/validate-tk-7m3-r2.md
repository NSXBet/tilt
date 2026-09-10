# Task: validate tk-7m3 (Ingress clones on master's clones-only seam)

# Target
Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-7m3 (branch tk-7m3-rework, commit 873665355)
Task board: tk CLI available on PATH. Task ID: tk-7m3.

# Change
Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/validation-context.md first — it has the full validation protocol.

Context specific to this round: this is a REWORK after a failed merge. The
prior merge (2dd4ff533) was reverted (c3f2443bd) because it referenced the
branch-side accumulator `out` while merged master's WorktreeClones
accumulates into `clones`. The rework (873665355) adapts the Ingress-clone
work to master's seam. Validate the CURRENT worktree tip (873665355),
not the branch history.

# Acceptance
- Run the validation protocol in the context file against tk-7m3:
  worktree diff vs master, real test runs, evidence file, then
  approved/rejected with evidence.
- Key criteria:
  * WorktreeClones returns ONLY clones (incl. Ingress clones) — `tilt down`
    (internal/cli/down.go deleteWorktreeClones) must see ingress clones in
    the returned set.
  * cloneIngresses/clonedServiceNames/rewriteSiblingRefs run on the FULL
    stamped set (stable + clones) — the clone-Service name set must be
    complete before any backend or container ref resolves.
  * Ingress clone carries tilt.dev/worktree annotation; backends rewritten
    to <svc>-wt-<worktree>; stable Ingress untouched; Ingress with no
    cloned backend not cloned; HTTPRoute/unstructured not cloned.
  * go test ./internal/controllers/core/kubernetesapply/ -count=1 green;
    go test ./internal/cli/ -count=1 -run 'Down' green (down consumes the
    returned set).
- Skip formatters, linters, project-wide test suites; run only the packages
  the task touched.
