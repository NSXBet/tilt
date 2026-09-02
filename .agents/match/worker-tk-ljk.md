# Task: implement tk-ljk (docker-compose project suffix + registry ports)

Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-ljk (branch tk-ljk, clean at master)
Task ID: tk-ljk

Spec: docker_compose_service worktree flag: project name suffix per worktree (default already dir-derived, docker_compose.go:135; explicit DockerComposeProject.Name override dockercomposeservice_types.go:215). Published ports deconflicted via port registry (phase 3). Test: two worktrees compose up simultaneously, distinct projects, no port clashes.

Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/worker-context.md for protocol. Sibling context: tk-mzx owns apply interception; tk-zfi owns port registry (internal/portregistry).
