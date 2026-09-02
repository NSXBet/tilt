# Validation context for validators (12 tasks)

You are validating a tk task in the NSXBet/tilt fork (Go, tilt-dev/tilt upstream fork).
Fork policy: mirror upstream; fork-specific changes prefixed `fork: `.

## How to validate
1. `tk show <task-id> --json` and `tk comments <task-id>` — acceptance criteria + worker report.
2. Review the worktree diff (worktree path given in your prompt): `git -C <worktree> log --oneline master..HEAD` and `git -C <worktree> diff master..HEAD`.
3. Run the task's tests in the worktree (`cd <worktree> && go test ./<pkg>/ -count=1`). These are Go/controller/web tasks — no browser surface is needed for most; if the task is UI (tk-a6u: web TS types), verify `web/src/webview.d.ts` contains the worktree label fields and vitest tests pass if present.
4. Evidence: append command output to `.agents/match/validations/<task-id>-<what>.txt` in the worktree, commit it there (`fork: validation evidence for <task-id>`).
5. Decide:
   - Approved: `tk comment <task-id> "APPROVED: <criterion → evidence>"` then `tk update <task-id> --status approved`
   - Rejected: `tk comment <task-id> "REJECTED: <what fails> Fix: <specific instruction>"` then `tk update <task-id> --status rejected`
6. Exit. One review pass, then die.

## Hard rules
- Read-only on code. Never merge, never close.
- Tests must actually run; paste real output. A task whose tests fail = REJECTED with the failing test named.
- Ignore dirty `.tasks/` files in worktrees; never commit them.
- Known-flaky baseline (not your concern): internal/engine tests with 5.23s timeouts (TestTriggerModes/manual_with_auto_init, TestUpperPodLogInCrashLoopThirdInstanceStillUp, TestUpper_ShowErrorPodLog, TestDisabledResourceRemovedFromTriggerQueue) fail intermittently on HEAD.
