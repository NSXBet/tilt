# Task: implement tk-usj (TUI worktree grouping)

Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-usj (branch tk-usj, clean at master)
Task ID: tk-usj

Spec: internal/hud/view/view.go Resource gains Worktree string (from labels); StateToTerminalView (internal/hud/view.go:19) populates. renderer.go/resourceview.go/tabview.go: group headers per worktree, filter matches worktree/ prefix, name shown in rows. Renderer tests per renderer_test.go patterns.

Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/worker-context.md for protocol. Full spec: plan section 9 in /Users/yuri/Workdir/Nsx/tilt/.agents/drafts/multi-worktree-parallel.md.
