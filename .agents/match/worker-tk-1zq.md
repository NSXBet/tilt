# Task: implement tk-1zq (Per-worktree ImageMap identity)

Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-1zq (branch tk-1zq, clean at master)
Task ID: tk-1zq

Spec (from title): ImageMap identity is ref-derived (ImageTarget.ImageMapName pkg/model/image_target.go:60). Register retagged refs under worktree-scoped ImageMaps so two worktrees building the same Dockerfile never share an ImageMap. Cache: same branch warm, different branches diverge after first changed layer. Integration test: two worktrees build same Dockerfile -> distinct tags, distinct ImageMaps, stable pods keep main image.

Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/worker-context.md for protocol and sibling context (tk-7gg owns tag rewrite: internal/build + internal/container/reference.go).
