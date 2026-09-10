# Worktree example

A minimal, runnable example of Tilt's multi-worktree mode. Every directory in
`.worktree/` is a full checkout of the example, with the same `Tiltfile` and
`serve.sh` as the main checkout. Tilt discovers each nested `Tiltfile`, then
re-executes the root Tiltfile with that checkout as its working directory.

The Tiltfile has no branching. Each resource declares its `scope`, and each
run instantiates only what its scope names:

- `scope="main"` — the foundation (`shared-api`): instantiated once, in the
  main run; every worktree inherits it and never re-runs it.
- `scope="worktree"` — branch-local (`app`): instantiated once per worktree,
  never in the main run. `serve_port` is a request: the port registry
  deconflicts it per worktree and injects the allocated port as
  `$TILT_SERVE_PORT`, so no port dict is maintained by hand.

## Run it

From this directory:

```sh
tilt up --worktrees
```

It registers three local HTTP servers:

| Run | Resource | URL |
| --- | --- | --- |
| main | `shared-api` | http://localhost:10370 |
| feature-a | `wt:feature-a_app` | pool-assigned; see the UI links |
| feature-b | `wt:feature-b_app` | pool-assigned; see the UI links |

Local resources begin disabled so the example does not start servers without
an explicit selection. In the Tilt UI, enable `wt:feature-a_app` and
`wt:feature-b_app` (and `shared-api`, if it is not already enabled) to run
them. Each endpoint responds with its checkout name.

The feature runs are also routed through Tilt's gateway at
`http://feature-a.tilt.localhost:<Tilt HUD port>` and
`http://feature-b.tilt.localhost:<Tilt HUD port>` — the stable address for
the branch runs, regardless of which pool port each got. The UI's endpoint
links carry both the gateway URL and the raw localhost fallback.

## Structure

```text
.
├── Tiltfile
├── serve.sh
└── .worktree/
    ├── feature-a/          # full checkout: same Tiltfile + serve.sh
    │   ├── Tiltfile
    │   └── serve.sh
    └── feature-b/          # full checkout: same Tiltfile + serve.sh
        ├── Tiltfile
        └── serve.sh
```

The nested directories intentionally mirror the root files. In a real
repository, create them with `git worktree add .worktree/<branch>`: Git
provides the same tracked files at each branch's revision. `--worktrees`
discovers the nested `Tiltfile` entries, but executes the root Tiltfile in
each checkout; changes on a branch therefore apply only to that branch's run.

For the full API, lifecycle behavior, Docker Compose isolation, and the
hardcoded `kubectl -n` caveat, see [`../../docs/worktrees.md`](../../docs/worktrees.md).
