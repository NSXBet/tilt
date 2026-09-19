# Worktree example

A minimal, runnable example of Tilt's multi-worktree mode. Every directory in
`.worktree/` is a full checkout of the example, with the same `Tiltfile` and
`serve.sh` as the main checkout. Tilt discovers each nested `Tiltfile`, then
re-executes the root Tiltfile with that checkout as its working directory.

The Tiltfile has no branching. A declaration carries `worktree=True` or it
does not:

- Unflagged (`shared-api`) — instantiated once, in the main run; worktree
  runs skip it entirely and inherit it implicitly.
- `worktree=True` (`app`) — instantiates in every run. A worktree run gets
  its own clone (`wt:<worktree>_app`). `serve_port` is a request: the port
  registry deconflicts it per worktree and injects the allocated port as
  `$TILT_SERVE_PORT`, so no port dict is maintained by hand.

## Run it

From this directory:

```sh
tilt up --worktrees
```

This registers four local HTTP servers — one stable, one per checkout:

| Run       | Resource            | URL                            |
| --------- | ------------------- | ------------------------------ |
| main      | `shared-api`        | http://localhost:10370         |
| main      | `app` (stable)      | pool-assigned; see UI links    |
| feature-a | `wt:feature-a_app`  | pool-assigned; see UI links    |
| feature-b | `wt:feature-b_app`  | pool-assigned; see UI links    |
| feature-c | `wt:feature-c_app`  | pool-assigned; see UI links    |

Local resources begin disabled so the example doesn't run servers without
explicit selection. In the Tilt UI, enable the resources you want and run
them. Each endpoint responds with its checkout name.

Worktree runs are also routed through Tilt's gateway at
`http://<worktree>.tilt.localhost:<Tilt HUD port>` — a stable address per
branch run, regardless of which pool port each got. The UI's endpoint links
carry both the gateway URL and the raw localhost fallback.

## Structure

```text
.
├── Tiltfile
├── serve.sh
└── .worktree/
    ├── feature-a/          # full checkout: same Tiltfile + serve.sh
    │   ├── Tiltfile
    │   └── serve.sh
    ├── feature-b/
    │   ├── Tiltfile
    │   └── serve.sh
    └── feature-c/
        ├── Tiltfile
        └── serve.sh
```

These nested directories intentionally mirror root files. In a real
repository, create them with `git worktree add .worktree/<branch>`: Git
provides the same tracked files at each branch's revision. `--worktrees`
discovers the nested `Tiltfile` entries, but executes the root Tiltfile in
each checkout; changes on a branch therefore apply only to that branch's run.

For the full API, lifecycle behavior, Docker Compose isolation, and the
hardcoded `kubectl -n` caveat, see [`../../docs/worktrees.md`](../../docs/worktrees.md).
