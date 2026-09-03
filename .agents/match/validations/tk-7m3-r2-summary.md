# Validation evidence — tk-7m3 (rework round 2, commit 873665355)
Validator: validate-tk-7m3-r2 | 2026-09-03 | worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-7m3

## Scope reviewed
git log master..HEAD → 873665355 fork: ingress clones on master's clones-only seam (tk-7m3 rework)
Diff: internal/controllers/core/kubernetesapply/worktree_clone.go (+137/-13),
internal/controllers/core/kubernetesapply/worktree_clone_test.go (+126). No other code touched.

## Acceptance: Ingress/HTTPRoute clones for flagged workloads
- cloneIngresses/cloneIngress (networkingv1.Ingress): one clone per Ingress with a
  Service backend resolving to a cloned Service; suffixed name; worktree annotation;
  backends rewritten to clone Service names; DefaultBackend + rule paths covered;
  resource backends untouched; stable Ingress untouched.
- HTTPRoute NOT cloned — documented in code: no sigs.k8s.io/gateway-api in go.mod
  (verified: grep gateway-api go.mod → empty), HTTPRoutes arrive unstructured and
  stay uncloned; gateway (internal/hud/server/gateway.go) serves worktree hosts
  without cloned routes. Declared limitation, matches go.mod reality.
- clonedServiceNames derives clone-Service set from stamped set (annotation +
  CutSuffix check) — same derivation rewriteSiblingRefs uses; root cause of the
  failed merge (incomplete clone-Service name set) addressed by running route
  clones + rewrites on full stamped set.
- WorktreeClones contract preserved: returns clones only, ingress clones in
  returned set → tilt down deleteWorktreeClones (internal/cli/down.go:209) deletes
  them; worktree_gc.go keys off tilt.dev/worktree annotation → GC-eligible.
- No double-suffix risk: WorktreeClones input is user stable YAML (no annotation,
  no clone names) per reconciler.go:458,628 and down.go:215.

## Tests run (real output)
go build ./...                              → clean
go vet ./internal/controllers/core/kubernetesapply/ → clean
gofmt -l internal/controllers/core/kubernetesapply/ → clean

go test ./internal/controllers/core/kubernetesapply/ -count=1 -run Worktree -v:
  --- PASS: TestWorktreeSiblingDNSRewrite (incl. cloned sibling rewrites subtests)
  --- PASS: TestWorktreeSiblingDNSRewrite_Args
  --- PASS: TestWorktreeIngressClone
      applies: cache-ing:ingress, db-ing:ingress → cache-ing-wt-feat-auth:ingress
  --- PASS: TestWorktreeSiblingDNSRewrite_StableUntouched
  --- PASS: TestWorktreeGCClonesOrphanedClones
  --- PASS: TestWorktreeGCKeepsLiveAndForeignClones
  --- PASS: TestWorktreeGC_NoWorktreesNoop
  PASS  ok  github.com/tilt-dev/tilt/internal/controllers/core/kubernetesapply 1.493s

go test ./internal/controllers/core/kubernetesapply/ -count=1:
  ok  github.com/tilt-dev/tilt/internal/controllers/core/kubernetesapply 1.087s

go test ./internal/cli/ -count=1 -run 'Down|Worktree':
  ok  github.com/tilt-dev/tilt/internal/cli 5.272s

## Acceptance criterion check: "route clone carries worktree annotation"
TestWorktreeIngressClone asserts:
  clone.Annotations["tilt.dev/worktree"] == "feat-auth"  ✓ PASS
  clone backend == "cache-wt-feat-auth"                  ✓ PASS
  stable cache-ing not annotated, backend untouched       ✓ PASS
  db-ing (shared backend) not cloned                      ✓ PASS
Parse check: networking.k8s.io/v1 Ingress parses as typed *v1.Ingress via
k8s.ParseYAMLFromString (go run probe) → type-switch in cloneIngresses hits.

## Notes (non-blocking)
- Duplicated near-identical comment paragraph in WorktreeClones (lines ~248-258)
  despite commit msg saying "Doc comment deduped". Cosmetic only.
- Known-flaky internal/engine baseline (validation-context.md) not applicable —
  no internal/engine code touched.
