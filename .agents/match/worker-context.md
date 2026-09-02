# Wave-2 worker context (5 tasks)

You are a Worker for one tk task in the NSXBet/tilt fork (Go, fork of tilt-dev/tilt).
Fork policy: mirror upstream; fork-specific commits prefixed `fork: `.

## Protocol
1. `tk show <task-id>` and `tk comments <task-id>` — read the spec; latest comment wins.
2. Work ONLY inside your worktree (given in your prompt). Commit there. Never commit to the main checkout, never merge, never set approved/rejected/closed.
3. Implement the acceptance criteria, matching repo style (table-driven _test.go next to code, goimports, std-lib style). Run the packages you touched: `go test ./<pkg>/ -count=1`.
4. Update `.tasks/` state is already handled by tk itself; do not hand-edit `.tasks/` files. If you accidentally dirty them, `git checkout -- .tasks/`.
5. End with: `tk comment <task-id> "DONE: <one line per change + verification>"` then `tk update <task-id> --status ready-to-review`, then exit.

## Plan reference
The overall design lives in /Users/yuri/Workdir/Nsx/tilt/.agents/drafts/multi-worktree-parallel.md (376 lines) — read the sections your task cites (plan §N references in the spec).

## Sibling context you build on (merged/committed branches exist in sibling worktrees)
- tk-jxb: clone stamping for workloads (label worktree=<name> in pod template AND selector, annotation, name suffix) — internal/controllers/core/kubernetesapply
- tk-mzx: apply interception stage in createEntitiesToDeploy (internal/controllers/core/kubernetesapply/reconciler.go)
- tk-7gg: per-worktree tag rewrite in image builds (internal/build, internal/container/reference.go AddTagSuffix with worktree token)
- tk-zfi: port registry wired into portforward reconciler (internal/portregistry exists, internal/controllers/core/portforward)
- tk-ntx: engine-prefix rewrite pass (internal/tiltfile/worktree/prefix.go — names `wt:<worktree>/<name>`)
- Worktree label: pkg/apis/core/v1alpha1/register.go LabelWorktree = "tilt.dev/worktree"

If you need an interface a sibling owns, code against what exists in THEIR worktree if yours lacks it, and note the dependency in your DONE comment — do not re-implement sibling work.
