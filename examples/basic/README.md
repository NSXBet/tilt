# Basic example

A minimal, runnable example of classic Tilt: one checkout, one Tiltfile, and
no worktree mode. It starts two local HTTP services:

- `api` at http://localhost:10380
- `web` at http://localhost:10381; its first build waits for `api` through
  `resource_deps`

Each endpoint responds with its resource name.

## Run it

From this directory:

```sh
tilt up
```

Tilt opens its UI and starts both servers. The resource cards include their
endpoint links. Stop with Ctrl-C, or run `tilt down` to tear down the session.

## Structure

```text
.
├── Tiltfile
└── serve.sh
```

`serve.sh <name> <port>` is a tiny Python HTTP server. For the multi-worktree
version of this shape, see [`../worktrees`](../worktrees).
