Validate tk-jq8 (Multi-Tiltfile CR creation).

Read first:
- /Users/yuri/Workdir/Nsx/tilt/.agents/match/validation-context.md (protocol)
- /Users/yuri/Workdir/Nsx/tilt/.agents/match/validate-tk-ntx.md (template — substitute tk-jq8)

Special notes for this task:
- Worker's DONE comment claims the implementation was already committed in the phase-1 baseline ee0cfda53 (now merged to master): internal/engine/configs/configs_controller.go + configs_controller_worktree_test.go. Validate on MERGED MASTER (/Users/yuri/Workdir/Nsx/tilt), not a task worktree — there are no task-specific commits.
- Acceptance: TestCreateTiltfile_Worktrees (2 worktrees → exactly 3 CRs, label tilt.dev/worktree, shared root path, per-worktree FileWatch + stop button) and TestCreateTiltfile_NoWorktrees both pass in ./internal/engine/configs/.
- Run: cd /Users/yuri/Workdir/Nsx/tilt && go test ./internal/engine/configs/... -count=1 -run TestCreateTiltfile (plus full package). Use private GOCACHE=$HOME/.cache/go-build-val-jq8.
- Evidence file + commit: commit evidence under .agents/match/validations/ in a scratch branch is NOT needed — instead paste evidence into the tk comment itself and (optionally) save under /Users/yuri/Workdir/Nsx/tilt/.agents/match/validations/tk-jq8-evidence.txt committed to master via a `chore:` commit.
- End state: tk-jq8 approved or rejected with evidence comment.
