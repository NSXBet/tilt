# Validation evidence — tk-n6q (Per-worktree FileWatch scoping)

Validator: ValidatorN6q · Date: 2026-09-02 · Worktree: .worktrees/tk-n6q (branch tk-n6q)
Commit under review: 1317a34e4 "fork: worktree FileWatch scoping — re-root local_resource ignores at the worktree"
Baseline: master ee0cfda53 (merge-base = master HEAD; diff vs master == diff vs baseline)

## SPEC (task comment, plan §7.8)

"per-worktree FileWatch scoping — worktree runs watch the worktree's file tree
(WatchInputs cwd override). FileWatches for prefixed resources (feat-auth/api)
resolve watch roots against the worktree dir, not the main repo; main resources
unchanged. Coordinate with tk-ntx prefix pass: watch scoping keys off the
engine-prefixed ManifestName. Acceptance: file change in worktree dir triggers
only that worktree's resources; change in main repo triggers shared/main
resources; tests green; go test ./internal/... green for touched packages."

## Diff review (3 files, +44/-2)

1. internal/tiltfile/local_resource.go (+1/-2)
   - threadDir: filepath.Dir(starkit.CurrentExecPath(thread)) -> starkit.AbsWorkingDir(thread)
   - Behavior: identical for main runs (AbsWorkingDir falls back to
     filepath.Dir(CurrentExecPath) — verified starkit/path.go:18-23); for
     worktree runs it follows the worktree thread-local re-root
     (starkit/path.go:19-20, set via worktree plugin OnStart ->
     env.SetWorktreeContext, worktree/worktree.go:64, from
     worktree.WithWorktree(name, worktree.DirOf(...)) in
     tiltfile_state.go:214-217/248).
   - threadDir is consumed at tiltfile_state.go translateLocal():
     (a) appended to watched paths (repoIgnoresForPaths), (b) as IgnoreDef
     BasePath for local_resource(ignore=...). So local_resource ignore=
     patterns are now evaluated against the worktree checkout, matching where
     the watched deps live. The change is minimal and correctly targets the
     last gap in the .tiltignore re-rooting slice already on master
     (tiltfile.go:188-195 confirmed unchanged from master = pre-existing).

2. internal/tiltfile/worktree/discover_test.go (+5)
   - Hermetic git fixture: adds -c commit.gpgsign=false -c gpg.format=openpgp
     to the fixture commit. Rationale verified: the validator machine has
     global commit.gpgsign=true + gpg.format=ssh + a signing agent
     (git config output in validator runlog), which made the un-pinned
     fixture hang/fail. Legitimate test-hermeticity fix, no behavior change.

3. internal/tiltfile/worktree_filewatch_test.go (+38)
   - New TestWorktreeFileWatch_LocalResourceIgnoreRootedAtWorktree: worktree-
     labeled run (tf.Labels tilt.dev/worktree=feat-auth) of a Tiltfile with
     local_resource("x", "true", deps=["web"], ignore=["logs"]); asserts deps
     resolve inside the worktree AND the ignore IgnoreDef.BasePath == worktree
     dir. This pins exactly the production change in local_resource.go.

Cross-checks performed:
- FileWatchIgnores composition unchanged (filewatch.go specForTarget/
  addGlobalIgnoresToSpec/ToFileWatchObjects — target ignores pass through
  GetFileWatchIgnores -> FileWatchSpec.Ignores; pkg/model LocalTarget accessor
  pre-existing on master).
- Label key consistency: worktree.LabelWorktree ("tilt.dev/worktree") used in
  both test and loader; DirOf computes <dir-of-root-Tiltfile>/.worktree/<name>,
  matching the test's expectation.
- Watch scoping keys off the worktree label on the Tiltfile CR (the re-
  execution context), consistent with the tk-ntx prefix pass boundary named
  in SPEC.

## Test evidence (all real output captured in validator runlog)

Environment: go vet clean for touched packages (exit 0). Machine-wide disk
emergency forced several retries (ENOSPC build failures — environmental, NOT
test failures); final green runs with private GOCACHE.

### RUN 1 — internal/tiltfile/worktree (full package, -count=1)
PASS — ok github.com/tilt-dev/tilt/internal/tiltfile/worktree (also 4.032s on
final rerun). 43 tests incl. all TestDiscover_* (real git worktree fixture now
gpg-hermetic), TestCombine_* (shared/clone dep boundary), worktree builtins,
TestReexec_Isolation_NoCrossWorktreeLeak, TestWorktreeConfig_Parse/Defaults.

### RUN 2 — internal/tiltfile targeted (-run 'TestWorktreeFileWatch|TestWorktreeConfig' etc.), verbose
38/38 PASS, 0 FAIL, including:
- TestWorktreeConfig_LoadResultDefaults / Overrides / WorktreeRunContext
- TestWorktreeFileWatch_MainRunTiltignoreUnchanged
- TestWorktreeFileWatch_WorktreeRunTiltignoreRootedAtWorktree
- TestWorktreeFileWatch_WorktreeRunWatchedPathsInsideWorktree
- TestWorktreeFileWatch_LocalResourceIgnoreRootedAtWorktree (NEW — pins this
  task's production change) PASS

### RUN 3 — full ./internal/tiltfile package (-count=1)
FAIL — exactly 2 failures, both ENVIRONMENTAL baseline, not caused by this
work:
- TestKustomizeFlags: expects "kustomize build --enable-helm" output; machine
  has NO standalone kustomize binary (kubectl fallback logged). REPRODUCED
  IDENTICALLY on the main checkout /Users/yuri/Workdir/Nsx/tilt at master:
  same assertion failure, same output. Matches the validation-context
  allowance for pre-existing env failures; kustomize files untouched by diff.
- TestKustomizeBin: test fixture shells out to a fake ./kustomize script which
  execs `kustomize` from PATH — "exec: kustomize: not found" (exit 127).
  Same missing-binary root cause.
All other tests in the package (incl. every test the diff touches) PASS.

### Acceptance mapping (SPEC -> evidence)
- "FileWatches for prefixed resources resolve watch roots against the
  worktree dir, not the main repo": watched deps re-rooted via
  AbsWorkingDir (pre-existing, pinned by TestWorktreeFileWatch_
  WorktreeRunWatchedPathsInsideWorktree PASS); this commit closes the
  remaining gap — local_resource ignore= BasePath now also worktree-rooted
  (pinned by NEW TestWorktreeFileWatch_LocalResourceIgnoreRootedAtWorktree
  PASS).
- "main resources unchanged": threadDir identical for main runs (code path
  fallback = old expression); TestWorktreeFileWatch_MainRunTiltignoreUnchanged
  PASS + full package green apart from environmental kustomize pair.
- "tests green ... for touched packages": RUN 1 + RUN 2 fully green; RUN 3
  failures limited to the documented environmental kustomize-binary pair,
  reproduced at master.
- File change triggering semantics (worktree vs main reload): pinned by
  TestWorktreeFileWatch_WorktreeRunTiltignoreRootedAtWorktree (ConfigFiles =
  root .tiltignore + root Tiltfile -> worktree run reloads on either; PASS)
  and WatchedPaths-inside-worktree (PASS).

## Verdict

APPROVED — criterion -> evidence mapping complete; production change minimal,
correct, and test-pinned; no regressions outside the documented environmental
baseline.
