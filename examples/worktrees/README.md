# Worktree example

A minimal, runnable example of Tilt's multi-worktree mode. One root
`Tiltfile` starts shared infrastructure once, then Tilt discovers each
`.worktree/<name>/Tiltfile` and re-executes the root Tiltfile in that
checkout.

## Run it

From this directory:

```sh
tilt up --worktrees
```

It registers three local HTTP servers:

| Run | Resource | URL |
| --- | --- | --- |
| main | `shared-api` | http://localhost:10370 |
| feature-a | `app-feature-a` | http://localhost:10371 |
| feature-b | `app-feature-b` | http://localhost:10372 |

Local resources begin disabled so the example does not start servers without
an explicit selection. In the Tilt UI, enable `app-feature-a` and
`app-feature-b` (and `shared-api`, if it is not already enabled) to run them.
Each endpoint responds with its checkout name.

The feature runs are also routed through Tilt's gateway at
`http://feature-a.tilt.localhost:<Tilt HUD port>` and
`http://feature-b.tilt.localhost:<Tilt HUD port>`. The raw localhost links
remain available as the fallback.

## Structure

```text
.
├── Tiltfile
├── serve.sh
└── .worktree/
    ├── feature-a/
    │   ├── Tiltfile
    │   └── serve.sh
    └── feature-b/
        ├── Tiltfile
        └── serve.sh
```

The two nested Tiltfiles are intentionally only discovery markers:

```python
load_dynamic('../Tiltfile')
```

They model checkouts that keep one shared root Tiltfile. The launchers are
copied into each checkout because `serve_cmd` executes with that checkout as
its working directory. In a real repository, replace these sample directories
with `git worktree add .worktree/<branch>` checkouts; each only needs a
`Tiltfile` to be discovered.

For the full API, lifecycle behavior, Docker Compose isolation, and the
hardcoded `kubectl -n` caveat, see [`../../docs/worktrees.md`](../../docs/worktrees.md).
