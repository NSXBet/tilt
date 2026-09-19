# k8s_resource(worktree=True) — declarative helm/kubectl worktree provisioning

Goal: kill the `if worktree.name() == ""` branching pattern for helm/kubectl
resources. One declarative flag — `k8s_resource(..., worktree=True)` — marks a
resource worktree-clonable: main run owns the stable resource, every worktree
run gets a clone pod ("new setup pod") with this run's built image injected,
and the existing gateway serves the clone at `<worktree>.tilt.localhost`.

## Context

- The originally-decided API (`.agents/drafts/multi-worktree-parallel.md` §3,
  "worktree=True resource defined in main run → cloned per worktree") was
  `k8s_resource(worktree=)`; the implementation detoured to three-way
  `scope=` strings on `k8s_yaml`/`k8s_resource`/`local_resource`
  (`internal/tiltfile/scope.go`) and left helm/k8s blocks branching on
  `if worktree.name()` (docs/worktrees.md §1 still shows it).
- Today a worktree run's `k8s_resource` referencing a main-defined workload
  fails at assembly with "specified unknown resource"
  (`internal/tiltfile/k8s.go:347-364`), so the chart must re-render in every
  run (examples/worktrees-helm) or the Tiltfile branches.
- Clone machinery already exists: `stampWorktreeClones`
  (`internal/controllers/core/kubernetesapply/worktree_clone.go` — `-wt-<wt>`
  workload/Service/Ingress clones, `worktree=<name>` label in template +
  selector, sibling DNS rewrite); per-worktree image tags
  (`-wt-<worktree>`); worktree-scoped ImageMap selectors (boundary pass);
  uncommitted stable-selector image-injection fallback in
  `kubernetesapply/reconciler.go` (`if !replaced && spec.Worktree != ""`);
  gateway host-router + endpoint links + Ingress clones (tk-x4j/tk-dbr/
  tk-p2z/tk-7m3). Uncommitted tests already moved ownership: worktree runs
  apply ONLY clones; stable entities are main-owned.

## Contract

`k8s_resource(..., worktree=True)`:
- Main run: instantiates the stable resource (normal behavior).
- Worktree run: instantiates the clone. The clone's YAML comes from the run's
  own ingestion when the run renders a workload (scope-less helm → per-branch
  chart values), else it inherits main's rendered entities (chart
  `scope="main"` → no re-render, this is the shape that deletes the `if`).
- Clone pod gets this run's built image (tag `-wt-<worktree>`) injected via
  the ImageMap stable-fallback; clone Service/Ingress + sibling DNS rewrite
  as today; gateway serves the clone.
- `helm`/`k8s_yaml`/`kustomize` get NO new flag (plan §"API surface":
  interception is downstream at the entities).

## Steps

1. **Loader flag** — `internal/tiltfile/k8s.go`: add `worktree?` bool to
   `k8s_resource` unpackArgs (line ~329). Load error when combined with an
   explicit `scope=` (the flag IS the scope declaration for clonable
   resources: main stable + per-worktree clones). Carry the flag on the
   resource decl/manifest.
2. **Worktree-run assembly** — `internal/tiltfile/k8s.go` +
   `internal/tiltfile/tiltfile_state.go`: in a worktree run, a
   `k8s_resource(worktree=True)` whose workload is absent from the run's YAML
   no longer errors "specified unknown resource"; it produces the manifest
   with the run's image targets and an entity-less k8s target marked for
   inheritance. `renameDerived` already stamps clone name + `Worktree` on the
   spec.
3. **Boundary inheritance** — `internal/tiltfile/worktree/boundary.go`:
   in `ApplyBoundary`, copy main's k8s entities into such inherited clones
   (main is already an input; nil-main keeps the existing error contract).
   Deps resolve same-worktree first, else main-defined (unchanged).
4. **Image injection** — keep the uncommitted fallback in
   `kubernetesapply/reconciler.go` (clone YAML carries chart-stable refs;
   fallback trims `-wt-<worktree>` off the scoped selector and injects this
   run's built ref) plus the ownership-split test changes; commit as the
   image-injection slice.
5. **Gateway** — no new code. Clone Service port-forwards
   (`port_forward(0, container_port=…)`) → endpoint registry →
   `<worktree>.tilt.localhost` (existing). Verify the inherited path still
   produces worktree-labeled manifests so the controller-seam stamping
   (`internal/controllers/core/tiltfile/worktree.go`) resolves the gateway
   endpoint.
6. **Docs** — `docs/worktrees.md` §1: replace the `if worktree.name() == ""`
   YAML/helm pattern with `scope=` + `k8s_resource(worktree=True)`; note:
   drop `scope="main"` from the helm line when branch-local chart values
   should flow into clones.

## Verification

- `go test ./internal/tiltfile/... ./internal/controllers/core/kubernetesapply/... ./internal/engine/...`
- New loader tests: flag+scope conflict errors; main run instantiates stable;
  worktree run with no rendered workload → inherited manifest (image targets
  kept, empty entities until boundary); with own workload → attaches.
- New boundary test: inherited clone receives main's entities + `Worktree`
  stamp; bare deps resolve.
- Reconciler test: chart-shaped YAML, clone carries the `-wt-<worktree>`
  digest, stable ref never applied by the worktree run.
- E2E (orbstack): `tilt up --worktrees` on updated examples/worktrees-helm —
  clone pod runs the `-wt-<name>` image, `<feat-auth>.tilt.localhost` serves
  the clone, `tilt down` prunes clones.

## Out of Scope

- No `worktree=` flag on `helm`/`k8s_yaml`/`kustomize` (interception stays
  downstream).
- No namespace-per-worktree (rejected in the recorded plan §4).
- Scripts applying their own YAML keep the documented `-n` caveat.
