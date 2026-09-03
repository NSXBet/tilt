# Multi-Worktree Development

Run one Tilt session against several git worktrees (or plain checkouts) of
the same repo at once: each worktree's branch gets its own running
resources, its own ports, and its own `<worktree>.tilt.localhost` URL,
while the expensive shared stuff — databases, charts, image pulls — stays
single-instance. Modeled on the incidents-admin stack: shared postgres +
helm chart in the main checkout, worktree clones that build and serve only
the branch's own services.

The feature is dark-launched behind `--worktrees` (default off): without
the flag Tilt behaves exactly as upstream.

## Enabling worktrees

1. Flag your shared (main-run) services so only what a branch actually
   changes gets cloned, and branch the Tiltfile on the worktree name:

   ```python
   WT = worktree.name()   # "" in the main run, "feat-auth" in a worktree run

   if WT == "":
       # foundation: evaluated once, shared by every worktree
       k8s_yaml(dedupe_env(helm("local", name="incidents", values=["local/values.yaml"])))
       k8s_yaml("local/infra.yaml")           # postgres + friends
       local_resource("admin-bff-image", cmd="docker pull ...@sha256:...")
       k8s_resource("postgres", port_forwards=["5433:5432"])
       k8s_resource("admin-bff", port_forwards=["8080"])
   else:
       # what THIS branch tests — the only thing that gets cloned
       docker_build(REG + "/incidents-admin", ".", dockerfile="Dockerfile",
                    only=["web/"], live_update=[sync("web/src", "/app/src")])
       k8s_resource("incidents-admin", port_forwards=["3004:3004"])
   ```

   In a worktree run every path — `docker_build` contexts, `sync()`,
   `local()` — resolves inside the worktree checkout with zero path edits.
   Worktree runs execute the SAME root Tiltfile; a worktree checkout may
   also carry its own Tiltfile for discovery purposes, but the root
   Tiltfile is what executes.

2. Create the worktree dir and checkouts:

   ```sh
   mkdir .worktree
   git worktree add .worktree/feat-auth -b feat-auth
   # ...or just copy/symlink a checkout in there; Tilt only looks at the
   # filesystem, git metadata is not required
   ```

   Each checkout needs a `Tiltfile` (it can be a bare marker file) so
   discovery picks it up. Discovery is position-based: every subdirectory
   of `.worktree/` containing a Tiltfile is a worktree, named after its
   directory basename. The main checkout is the implicit worktree `main`;
   a `.worktree/main/` subdirectory is a load error.

3. Start everything:

   ```sh
   tilt up --worktrees
   ```

   `tilt down` tears down every worktree's resources, including the
   clone objects Tilt created (labeled/annotated `tilt.dev/worktree`).
   Deleting a worktree directory while Tilt runs is handled too: the
   reconciler prunes clones whose Tiltfile run is gone.

Per-run behavior you get for free:

- **Compose projects are suffixed per worktree** (`myproj-wt-feat-auth`),
  so containers/networks/volumes never collide; published host ports are
  rebound through Tilt's port registry (distinct, stable across reloads;
  configure a pool with `worktree_config(port_range=(30000, 31000))`).
- **`local_resource` serve ports**: give each worktree's serve command
  its own authored port (they run in parallel); the port registry
  deconflicts `port_forward=0` forwards and docker-compose published
  ports per worktree. Configure a pool with
  `worktree_config(port_range=(30000, 31000))`.
- **Images**: each worktree builds its own tags
  (`api:api-<digest>-wt-<name>`) with its own build-cache lineage; a
  worktree never reuses main's stale image or vice versa.
- **Gateway**: every worktree HTTP endpoint is also reachable at
  `<worktree>.tilt.localhost:<port>` (RFC 6761 — browsers resolve
  `*.localhost` to 127.0.0.1, no system config). Raw `localhost:<port>`
  links stay in the UI as the fallback for corporate proxies that
  intercept `*.localhost`.
- **UI**: the web UI and TUI group resources by worktree.

## The hardcoded `-n` namespace flag caveat

Tilt's worktree interception covers everything that flows through the
YAML Tilt parses: `k8s_yaml`, `helm`, `kustomize`, and the stdout of
`k8s_custom_deploy` apply commands. It **cannot** see inside scripts that
apply YAML themselves, e.g.:

```sh
kubectl apply -f manifests/ -n incidents   # Tilt never parses this output
```

If a worktree run relies on such a script, the applied objects land once,
un-namespaced by worktree — the second worktree to run it hits a conflict,
and neither gets clone isolation. Fixes, in order of preference:

1. Route the YAML through Tilt: `k8s_yaml(local(["bash", "apply.sh"]))`
   where the script prints the manifests instead of applying them (the
   incidents-admin `env-to-secret.sh` / configmap pattern).
2. Make the script worktree-aware with `worktree.name()`:

   ```python
   k8s_custom_deploy("migrate", apply_cmd="bash migrate.sh -n incidents -wt " + worktree.name(),
                     ...)
   ```

   and have the script suffix object names (`-wt-<name>`) and selectors
   exactly like Tilt's own clone stamping does. Only do this for objects
   Tilt doesn't manage (one-off Jobs are the usual case); everything Tilt
   deploys should stay in the parsed-YAML path so `tilt down`, clone
   pruning, and the UI all see it.

The same caveat applies to chart-internal hardcoded namespaces: keep
resources in the chart's own namespace (the default, no `-n` overrides)
— worktrees clone in the SAME namespace on purpose (see below).

## Why one namespace (not one namespace per worktree)

Worktrees deploy clones into the same namespace as main, with suffixed
names (`incidents-admin-wt-feat-auth`), worktree labels in the pod
template AND the Service selector, and a `tilt.dev/worktree` annotation
on every clone object. Namespace-per-worktree was evaluated and rejected:
charts hardcode `namespace:` values and relative DNS between services;
namespace isolation forces users to fix every occurrence (against the
one-line adoption goal) or hides cross-namespace DNS behind alias hacks.
Same-namespace clones keep chart-internal DNS, Secrets, and ConfigMaps
working untouched — only the flagged workload's pods move.

## The `worktree.name()` adoption pattern

`worktree.name()` is the single line of worktree awareness:

| call | main run | worktree run |
|------|----------|--------------|
| `worktree.name()` | `""` | `"feat-auth"` |
| `worktree.dir()` | `""` | the checkout dir (absolute) |
| `worktree.shared("postgres")` | `False` | `True` when `postgres` is defined by the main run |

Branching on it converts an existing Tiltfile:

```python
WT = worktree.name()

if WT == "":
    # everything that must exist exactly once
else:
    # only this branch's resources; deps on shared names
    # (resource_deps=['postgres']) resolve to main's definitions
    # automatically
```

Rules the loader enforces across runs:

- A resource defined by BOTH main and a worktree run: the worktree's
  definition wins (it flows through with its bare engine name).
- The same resource name defined by TWO worktree runs: load error —
  per-branch resources must be named per branch.
- Deps resolve same-worktree first, else main-defined; a dep on a
  resource defined only by a *different* worktree is a load error.
- A main-defined resource depending on a worktree clone is a load error
  (shared resources can never pull a branch's clone into every
  worktree).
- Two worktrees with the same basename: load error.

`worktree_config()` (main Tiltfile only) tunes the machinery:

```python
worktree_config(dir="local-wts",           # default ".worktree"
                port_range=(30000, 31000), # registry pool; default OS-assigned
                gateway=True)              # default; False disables *.localhost links
```

## Testing the feature end-to-end

- `integration/worktrees/` — hermetic fixture (no cluster, no docker):
  local_resource servers per worktree, asserting discovery, parallel
  serving, gateway routing by response body, and endpoint links.
  `go test -tags integration -run TestWorktrees ./integration`
- `integration/worktrees_dc/` — docker-compose fixture: two worktrees
  plus main compose up simultaneously; opt-in via
  `TILT_WORKTREES_DC_E2E=1` (three parallel compose projects need a
  docker daemon with headroom; see the test comment for the
  environment-race details).
