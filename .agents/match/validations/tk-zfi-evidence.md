tk-zfi validation evidence — Wire registry into portforward reconciler
Validator: ValidatorZfi · 2026-09-02 · Worktree: .worktrees/tk-zfi @ 650687890cf362f77a7449eecd0db842c0c63bc2

Task (from tk show tk-zfi):
  internal/controllers/core/portforward/reconciler.go:189 — when LocalPort==0,
  consult the port registry before the OS. ForwardStatus.LocalPort reports the
  allocation; collisions surface as resource errors via the existing portforward
  error path. Acceptance: two worktrees requesting the same local port get
  distinct allocations; tests in registry_test.go.

Diff review (git diff master..HEAD; single commit 650687890, 2 files, +52/-10):
  - internal/controllers/core/portforward/reconciler.go:
      * onePortForward gains `index` param; portForwardLoop passes loop index.
        Only LocalPort==0 consults portregistry.Allocate(owner, 0) where owner =
        "ns/name#index" — distinct per forward, stable per PortForward object,
        hence stable across Tiltfile reloads (spec change recreates forwards,
        same owner key).
      * Allocation error sets ForwardStatus.Error and requeues via the existing
        error path (same shape as a CreatePortForwarder failure).
      * Explicit LocalPorts pass through verbatim (registry not consulted).
      * Status writes on error now report int32(localPort) (allocated port)
        instead of forward.LocalPort (0) — gateway/UI see the real port.
      * No other callers of portForwardLoop/onePortForward exist (grep verified).
  - internal/controllers/core/portforward/registry_test.go: fixed
    TestRegistryAllocatesDistinctPortsPerForward, which previously scanned the
    same sorted status list twice (deterministic false pass); now collects both
    statuses in one Eventually pass. Other 4 tests unchanged from spec baseline.
  - internal/portregistry/ unchanged vs master (diff master..HEAD on
    internal/portregistry is empty; package was pre-existing baseline).

Environment note (coordination evidence, not code): several earlier attempts
failed with "no space left on device" / linker "cannot open file" errors —
shared GOCACHE corruption + 12-way cold-build race during the validation wave.
All failures env-shaped; final clean runs below used a private GOCACHE.

TEST RUN 1 — internal/portregistry (go test -count=1 -race -v):
  TestAllocateHonorsRequestedPort            PASS (0.00s)
  TestAllocateOSFallback                     PASS (0.00s)
  TestAllocateOutOfRangeRequestedNotHonored  PASS (0.00s)
  TestAllocateStableAcrossReload             PASS (0.00s)
  TestAllocateRangeExhaustionError           PASS (0.00s)
  TestAllocateDistinctOwnersSameRequested    PASS (0.00s)
  TestAllocateRangeFallbackDistinctPorts     PASS (0.00s)
  TestReleaseReturnsPortToPool               PASS (0.00s)
  TestAllocateConcurrentDistinct             PASS (0.00s)  [race detector clean]
  TestAllocateRequestedTakenFallsBack        PASS (0.00s)
  TestAllocateRejectsPrivilegedRequested     PASS (0.00s)
  ok  github.com/tilt-dev/tilt/internal/portregistry 4.238s   -> 11/11 PASS

TEST RUN 2 — internal/controllers/core/portforward (go test -count=1 -v,
private GOCACHE, complete uninterrupted run):
  TestCreatePortForward                       PASS (0.06s)
  TestCreatePortForwardDisabled               PASS (0.00s)
  TestDeletePortForward                       PASS (0.02s)
  TestModifyPortForward                       PASS (0.04s)
  TestModifyPortForwardManifestName           PASS (0.04s)
  TestMultipleForwardsForOnePod               PASS (0.02s)
  TestMultipleForwardsMultiplePods            PASS (0.02s)
  TestPortForwardStartFailure                 PASS (0.03s)
  TestPortForwardRuntimeFailure               PASS (0.04s)
  TestPortForwardPartialSuccess               PASS (0.04s)
  TestIndexing                                PASS (0.00s)
  TestClusterChange                           PASS (0.04s)
  TestRegistryAllocatesDistinctPortsPerWorktree PASS (0.02s)  <- acceptance
  TestRegistryAllocatesDistinctPortsPerForward  PASS (0.02s)  <- acceptance
  TestRegistryPortStableAcrossReload            PASS (0.04s)
  TestRegistryExhaustionSurfacesAsForwardError  PASS (0.02s)
  TestRegistryDoesNotHijackExplicitLocalPort    PASS (0.02s)
  ok  github.com/tilt-dev/tilt/internal/controllers/core/portforward 1.891s
  -> 16/16 PASS (11 pre-existing reconciler tests + 5 registry tests)

  Observed log line in run 2 confirms the error-path contract end-to-end:
  "Error port-forwarding wt:wt-e/api (0 -> 8080): portregistry: port range
  exhausted: 20300-20300" — ErrRangeExhausted surfaces through the existing
  portforward error path (ForwardStatus.Error + requeue), not a panic/crash.

TEST RUN 3 — warm-cache re-confirmation after disk-pressure retries:
  go test -count=1 -run 'TestRegistry|TestCreatePortForward$|TestModifyPortForward$|TestPortForwardStartFailure$'
    ./internal/controllers/core/portforward/  -> ok 1.357s
  go test -count=1 ./internal/portregistry/   -> ok 0.328s

go build ./internal/controllers/core/portforward/ ./internal/portregistry/  -> OK
go vet   (same two packages)                                                -> OK

Acceptance mapping:
  - "reconciler consults registry when LocalPort==0, before OS" -> diff of
    onePortForward implements exactly this; contract tests exercise it via the
    ForwardStatus boundary: PASS.
  - "two worktrees requesting same local port get distinct allocations" ->
    TestRegistryAllocatesDistinctPortsPerWorktree PASS (both forwards
    LocalPort==0, range [20000,20010], distinct ports asserted); also
    TestRegistryAllocatesDistinctPortsPerForward PASS for per-forward keys.
  - "registry_test.go exists" -> present on branch with 5 tests, all PASS.
  - "gateway and UI consume ForwardStatus unchanged" -> no API-type changes
    (pkg/apis untouched); allocation reported through existing
    maybeUpdateStatus path.
  - "collisions surface as resource errors via existing error path" ->
    TestRegistryExhaustionSurfacesAsForwardError PASS with observed error line.

Verdict: APPROVED
Packages tested: internal/controllers/core/portforward, internal/portregistry
