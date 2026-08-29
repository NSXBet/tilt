# NSXBet/tilt fork — agent instructions

Fork of [tilt-dev/tilt](https://github.com/tilt-dev/tilt) (Go, Tilt dev environment).
Fork policy: mirror upstream; fork-specific changes live on top, prefixed `fork: `.
Upstream: `https://github.com/tilt-dev/tilt` (default branch `master`).

## Repo conventions (adhere, do not invent)

- Match the surrounding code's style, naming, and package layout. This is a large
  vendored Go repo — package-oriented architecture: small focused packages under
  `internal/` and `pkg/`, behavior lives with the package that owns it, exported
  APIs stay minimal.
- DRY: reuse existing helpers before writing new ones. Search `internal/`/`pkg/`
  first; do not duplicate logic that already exists.
- Imports are grouped std / external / tilt; goimports formatting.
- Tests: table-driven `_test.go` next to the code under test, standard library
  style as used across the repo.
- Do not touch `vendor/`, `node_modules/`, `web/` build output, or generated
  code (`tygo.yaml` outputs) unless the task requires it.
- Keep fork-only commits minimal and prefixed `fork: ` so upstream sync/rebase
  stays cheap.

## Build / run

- Go toolchain: see `go.mod`. Local: `go run ./cmd/tilt up` from repo root.
- Entrypoints live under `cmd/`; most behavior in `internal/`.

## Upstream sync

- `.github/workflows/sync-upstream.yml` rebases fork commits onto upstream
  `master` on schedule (every 6h). No AI conflict resolution — on conflict the
  run fails and must be resolved manually.
