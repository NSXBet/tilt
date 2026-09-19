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

1. Declare each resource's scope. The SAME root Tiltfile executes once for
   the main checkout and once for every worktree; `scope` says which run
   instantiates what, so the Tiltfile needs no branching:

   ```python
   # foundation: instantiated once, in the main run only — every worktree
   # inherits it through dep resolution and never re-runs it
   local_resource("admin-bff-image", cmd="docker pull ...@sha256:...", scope="main")

   # branch-local: instantiated once per worktree, never in the main run;
   # the port registry deconflicts serve_port per worktree and always
   # injects the allocated port as $TILT_SERVE_PORT (the serve process
   # binds the env var, never the authored request)
   local_resource("app",
                  serve_cmd="./serve.sh %s $TILT_SERVE_PORT" % worktree.name(),
                  serve_port=8080, scope="worktree")

   # YAML/helm/shared-infra declarations have no resource identity to
   # scope on, so those blocks still branch on the run context:
   if worktree.name() == "":
       k8s_yaml(dedupe_env(helm("local", name="incidents", values=["local/values.yaml"])))
       k8s_yaml("local/infra.yaml")           # postgres + friends
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

   Discovery stays live for the process lifetime: worktrees created after
   startup are picked up automatically (a `tiltfile:<worktree>` run loads,
   exactly like a startup-discovered one — live reload included), and
   deleting a worktree directory tears its run down: the Tiltfile CR is
   removed, its manifests and owned objects are deleted, and the clone
   objects (labeled/annotated `tilt.dev/worktree`) are pruned. A checkout
   whose `Tiltfile` has not been checked out yet is picked up once it
   appears (the watcher rescans for a few seconds after the dir-create
   event).

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
  `worktree_config(port_range=(30000, 31000))`. The registry honors a
  free in-pool `serve_port` request; when the request is taken or
  out-of-pool, it hands out the next free pool port. Which worktree
  keeps a contested request depends on load order — the mapping from
  ports to checkouts is stable within a session but not across runs.
  For worktree runs the allocated port is always injected as
  `$TILT_SERVE_PORT` (even when the request is honored — the authored
  value is only a request; the serve process binds the env var, string
  or array `serve_cmd` alike). Keep the pool disjoint from main-run
  authored ports: the registry deconflicts only what it allocates.
- **Images**: each worktree builds its own tags
  (`api:api-<digest>-wt-<name>`) with its own build-cache lineage; a
  worktree never reuses main's stale image or vice versa.
- **Gateway**: every worktree HTTP endpoint is also reachable at
  `<worktree>.tilt.localhost:<port>` (RFC 6761 — browsers resolve
  `*.localhost` to 127.0.0.1, no system config). Raw `localhost:<port>`
  links stay in the UI as the fallback for corporate proxies that
  intercept `*.localhost`.
- **UI**: the web UI and TUI group resources by worktree.

## Gateway without a port

`*.localhost` URLs still carry a port number — `<worktree>.tilt.localhost:<port>`
— because the gateway rides on Tilt's main HTTP listener. `--gateway-port`
adds a dedicated listener so the same URLs work without one
(`http://fix-ui.tilt.localhost`), which is what bare-hostname links, OAuth
redirects, and some tooling expect:

```sh
tilt up --gateway-port 80   # plain HTTP on the privileged port
```

The extra listener serves exactly what the main HUD port serves — the
gateway host-routing, the web UI, and the same token auth — so opting in
changes nothing about the security posture.

Ports below 1024 are privileged. Tilt never requires sudo: on a permission
error it asks once to run a one-shot bind helper (`tilt gateway-bind`,
hidden) under `sudo`, which opens the socket, passes the file descriptor
back to the unprivileged Tilt process (SCM_RIGHTS), and exits. Nothing
keeps running as root; every request is served by Tilt itself. Declining
the prompt — or running without a terminal (CI, scripts) — leaves the
gateway port off and `tilt up` continues unaffected. Port-in-use and other
ordinary bind failures are fatal, so a typo'd `--gateway-port` cannot
silently no-op.

The listener belongs to this Tilt process and dies with it. Tilt
deliberately does not install root services of its own; for boot
persistence across Tilt restarts, run Tilt under a service manager that
owns the privileged socket and passes it down.

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

## Scoping resources: `local_resource(scope=)`

Resources declare where they instantiate; the loader enforces it. No
branching:

```python
local_resource("admin-bff-image", cmd="docker pull ...@sha256:...", scope="main")
local_resource("app", serve_cmd="./serve.sh $TILT_SERVE_PORT", serve_port=8080,
               scope="worktree")
```

| `scope=` | main run | worktree runs |
|----------|----------|---------------|
| `"main"` | instantiated | dropped |
| `"worktree"` | dropped | instantiated (engine-prefixed clone, worktree cwd, registry serve port) |
| `"all"` (default) | instantiated | instantiated (worktree runs get the clone treatment) |

A scoped resource is dropped — not re-defined — in the runs its scope does
not name, so shared resources keep main's authored shape (ports included)
and never trigger the shared-double-define error below.

## The `worktree.name()` escape hatch

`worktree.name()` (plus the branch helpers) is the single line of worktree
awareness for everything `scope=` cannot express (YAML/helm blocks,
worktree-aware scripts):

| call | main run | worktree run |
|------|----------|--------------|
| `worktree.name()` | `""` | `"feat-auth"` |
| `worktree.dir()` | `""` | the checkout dir (absolute) |
| `worktree.shared("postgres")` | `False` | `True` when `postgres` is defined by the main run |
| `worktree.branch()` | main checkout's branch | the worktree checkout's branch (`""` on a detached HEAD) |
| `worktree.eq("feat-auth")` | main checkout's branch == the arg | the worktree checkout's branch == the arg |

Both branch helpers resolve git (`git branch --show-current`) in the checkout
the run executes for: the injected worktree dir, or the main Tiltfile's
directory for the main run. A checkout outside any git repo is an error —
if a Tiltfile branches on branches, "not a repo" cannot degrade to False.

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

For checkouts whose directory name mirrors the branch (`git worktree add
.worktree/feat-auth -b feat-auth`), `worktree.eq()` reads better than
comparing names:

```python
if worktree.eq("main"):
    # shared foundation, exactly once
elif worktree.eq("release/2.3"):
    # release-branch-only hotfix resources
else:
    # branch resources
```

Rules the loader enforces across runs:

- A resource defined by BOTH main and a worktree run: the worktree's
  definition wins (it flows through with its bare engine name). Prefer
  `scope=` — a scoped resource is dropped instead of re-defined, which
  keeps the shared shape authored in exactly one place.

- The same resource name defined by TWO worktree runs: load error —
  per-branch resources must be named per branch (or share one
  `scope="worktree"` declaration).
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
