# Task: validate tk-ja0 (Engine-prefix rewrite pass)

# Target
Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-ja0 (branch tk-ja0, commits ahead of master)
Task board: tk CLI available on PATH. Task ID: tk-ja0.

# Change
Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/validation-context.md first — it has the full validation protocol.

# Acceptance
- Run the validation protocol in the context file against tk-ja0.
- Set approved or rejected on the task with evidence.
- Skip formatters, linters, project-wide test suites; run only the packages the task touched.
