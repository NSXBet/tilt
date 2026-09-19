# Worktrees + Helm example

A minimal, runnable example of Tilt's multi-worktree mode on Kubernetes: the
same root Tiltfile executes once per checkout — main plus every
`.worktree/<name>/` — and each run instantiates exactly what its `scope`
names. There is no branching anywhere in the Tiltfile.

- `scope="main"` — the foundation, instantiated once in the main run and
  shared by every worktree clone: one Secret (`wt-helm-env`) and one
  postgres (`infra.yaml`). Secrets and ConfigMaps pass through clones
  untouched; PVCs never clone (RWO), which is why the database must be
  main-scoped rather than re-rendered per branch.
- default (`"all"`) — the branch-local stack, rendered by helm in every run.
  In worktree runs the apply pass clone-stamps every workload and Service:
  `api` becomes `api-wt-<worktree>`, with its own Service DNS and its own
  image build from that checkout.

Every run builds from its own checkout. Worktree runs build from THEIR
checkout into their own image lineage (`-wt-<worktree>` tag), and edits
inside a worktree live-update only that worktree's clone.

Port forwards use the registry: `k8s_resource("api",
port_forwards=[port_forward(0, container_port=8080)])` asks for an
auto-allocated local port, so the main run and every worktree get a
distinct, stable port with no collisions and no port dict. Postgres is
main-scoped, so its forward keeps the authored `5433:5432`.

## Run it

From this directory:

```sh
tilt up --worktrees
```

It registers three api resources over one shared postgres:

| Run | Resource | Endpoint |
| main | `api` (stable) + `postgres` | registry-allocated port; see the UI links |
| wt-a | `wt:wt-a_api` | registry-allocated port; see the UI links |
| wt-b | `wt:wt-b_api` | registry-allocated port; see the UI links |

With a gateway (`tilt up --worktrees --gateway-port 443`), each worktree's
clone Service is also routed at `http://<worktree>.tilt.localhost` — the
stable address for a branch, regardless of which port it got.

The wt-a checkout ships a tiny code divergence (its `main.go` labels its
responses with `branch wt-a`), so clone isolation is visible: `curl` each
endpoint and see which checkout served it.

```sh
curl http://localhost:<registry port for api>
# hello from the shared foundation | served by pod api-...
curl http://localhost:<registry port for wt:wt-a_api>
# hello from the shared foundation | served by pod api-wt-wt-a-... | branch wt-a
```

## Structure

```text
.
├── Tiltfile
├── api/                    # tiny Go server (image wt-helm/api)
├── chart/                  # helm chart: Deployment + Service (clone-stamped)
├── infra.yaml              # postgres, main-scoped
├── env-to-secret.sh, .env  # the shared Secret
└── .worktree/
    ├── wt-a/               # full checkout (same files as the root)
    └── wt-b/               # full checkout (same files as the root)
```

The nested directories intentionally mirror the root files. In a real repo
they are `git worktree add .worktree/<name> <branch>` checkouts; Tilt
discovers each nested `Tiltfile` and re-executes the root Tiltfile with that
checkout as its working directory.

## What to try

1. Edit `api/main.go` inside `.worktree/wt-a/` — only `wt:wt-a_api`
   live-updates; the stable `api` and `wt:wt-b_api` are untouched.
2. Edit `api/main.go` at the root — only the stable `api` live-updates.
3. `tilt down` in the same directory tears down every run's objects,
   including the clones.
