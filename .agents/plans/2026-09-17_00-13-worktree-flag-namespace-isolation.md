# worktree=True — binary flag, no prefixes, namespace isolation

Supersedes `.agents/plans/2026-09-16_23-39-k8s-resource-worktree-flag.md`.

Directive: (1) never prefix/mangle names with a `wt-` token — complying with a
naming scheme is not the user's responsibility; (2) remove `scope=` — bad
name, passing "main" is redundant; (3) the surface is binary: unflagged =
main by default, `worktree=True` = per-worktree instance.

## Contract

Every declaration (docker_build/custom_build, helm, k8s_yaml, kustomize,
k8s_resource, local_resource, docker_compose) takes one optional
`worktree=True`. No other syntax:

- unflagged: instantiates in the main run only. Rendered once; shared by
  every worktree (foundation: Secrets, postgres, shared charts).
- `worktree=True`: main runs it stable; each worktree run instantiates its
  own instance — applied into a derived namespace, this run's built image
  injected, gateway serves it at `<worktree>.tilt.localhost`.

Names stay exactly as authored everywhere the user looks. Tilt derives,
rewrites, and cleans up everything else.

## Naming rules (the "never prefix" directive)

- Cluster objects: identical names (`api`, `postgres`) — no suffixes, no
  selector mutation, no clone Services. Isolation is the namespace:
  `<base-ns>-<worktree>` (e.g. `default-feat-auth`), Tilt-created, annotated
  `tilt.dev/worktree`, Tilt-deleted. No `wt-` token anywhere.
- Engine-internal manifest names: suffix form `<name>-<worktree>` replaces
  the `wt:<worktree>_<name>` prefix; never user-visible (UI shows bare
  names, grouped by worktree).
- Image tags: `api:api-<digest>-<worktree>` (tag suffix, no `wt-` token);
  ImageMap selector stays worktree-scoped.
- Gateway: `<worktree>.tilt.localhost` unchanged (the branch name IS the
  host; nothing mangled).

This reverses the recorded same-namespace decision
(docs/worktrees.md "Why one namespace") — namespace isolation is the only
way to keep object names unmangled while clones coexist with stable.

## Steps

1. **Binary flag, delete scope** — `internal/tiltfile/`:
   add `worktree?` bool to the seven declarations; instantiate iff main run
   OR flagged. Delete `scope.go` (parseScope/skipScope/scopeInstantiates),
   the `scope?` params, and the scope tests; update
   examples/worktrees-helm + worktrees fixture Tiltfiles. A worktree run's
   `k8s_resource(worktree=True)` whose workload is absent from the run's
   YAML inherits main's entities at the boundary (no
   "specified unknown resource" error).
2. **Namespace isolation** — replaces
   `internal/controllers/core/kubernetesapply/worktree_clone.go` (deleted):
   - worktree run applies flagged entities into `<base-ns>-<worktree>`;
     Tilt creates the namespace before first apply (annotated), deletes it
     on worktree teardown and `tilt down` (replaces per-object clone GC).
   - helm/kustomize render with `--namespace <derived>` so
     `.Release.Namespace` resolves per worktree; explicit-namespace objects
     targeting a FOREIGN namespace are a load error (chart must be
     unflagged/shared) — loud, never silent mis-deployment.
   - shared (unflagged) siblings stay in the base namespace; the existing
     sibling-DNS rewrite machinery retargets to cross-namespace FQDN
     (`postgres` → `postgres.<base-ns>`) inside worktree entities.
   - namespace on every namespaced entity set to the derived ns.
3. **Engine names** — `internal/tiltfile/worktree/boundary.go`,
   `prefix.go`, `display.go`: rewrite to the suffix form
   `<name>-<worktree>`; owned-object derivation keeps working (`-` is
   path-safe for apiserver names).
4. **Image injection** — keep the uncommitted reconciler fallback
   (stable-selector trim) so chart-stable refs still receive this run's
   `-<worktree>`-tagged image; worktree runs apply only their own
   namespace's objects (ownership split already in the uncommitted tests).
5. **Forwards + gateway** — port-forwards bind pods in the run's namespace
   (namespace flows through the k8s target); gateway endpoint resolution
   via manifest labels unchanged; endpoint links/`<worktree>.tilt.localhost`
   serve the clone. No new code; verify the namespace stamping path keeps
   manifests worktree-labeled (controller seam).
6. **Docs** — rewrite docs/worktrees.md: binary flag table (unflagged=main,
   worktree=True=per-worktree), drop the `if worktree.name()` pattern, drop
   scope docs, replace "Why one namespace" with the namespace model + the
   foreign-namespace load-error caveat; keep the compose project-suffix note
   (compose has no namespaces; suffix stays internal to compose).

## Verification

- `go test ./internal/tiltfile/... ./internal/controllers/core/kubernetesapply/... ./internal/engine/...`
- Loader: unflagged drops in worktree runs; flagged instantiates both;
  flag on k8s_resource inherits main entities when the run renders none;
  foreign-namespace object in a flagged blob is a load error.
- Boundary: suffix names, dep resolution same-worktree-first, bare shared
  names.
- Reconciler: flagged entities carry the derived namespace; image injected
  is this run's `<digest>-<worktree>` tag; stable objects untouched in base ns.
- Lifecycle: namespace created annotated on first apply; deleted on
  worktree-dir removal (auto-watch teardown) and `tilt down`.
- E2E (orbstack): `tilt up --worktrees` on the updated example —
  `kubectl -n default-feat-auth get deploy` shows `api` (unmangled) running
  this branch's image; `<feat-auth>.tilt.localhost` serves it; teardown
  removes the namespace.

## Out of Scope

- docker-compose keeps its internal project-name suffix (no namespace
  concept in compose); never user-authored.
- `worktree.name()` escape hatch stays for the rare not-expressible cases.
- No compat shims for the removed `scope=` (fork-internal API, clean
  cutover).
