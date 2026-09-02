# Task: implement tk-kxk (Clone Services + sibling DNS rewrite)

Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-kxk (branch tk-kxk, clean at master)
Task ID: tk-kxk

Spec: For each Service selecting a stamped workload: clone Service with same name suffix + mutated selector -> cluster DNS <svc>-wt-<name> reaches only worktree pods. Env/arg rewrite in clone entities: sibling refs resolve to clone names for worktree-flagged resources, stay bare for shared. No mesh required. Tests: selector uniqueness, env rewrite.

Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/worker-context.md for protocol. Sibling context: tk-jxb owns workload clone stamping (name suffix -wt-<name>, label+selector) in internal/controllers/core/kubernetesapply.
