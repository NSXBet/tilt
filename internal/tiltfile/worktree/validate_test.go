package worktree

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance: plan §3 "Validation (loader, cross-Tiltfile pass)" + §11
// "double-define errors", exercised over the combined main + worktree results.

func md(name string, deps ...string) model.Manifest {
	m := model.Manifest{Name: model.ManifestName(name)}
	for _, d := range deps {
		m.ResourceDependencies = append(m.ResourceDependencies, model.ManifestName(d))
	}
	return m
}

// Worktree resources depend on main-defined resources → resolve to main's
// shared definitions (stay bare); worktree-owned deps rewrite to the clone.
func TestCombine_SharedAndCloneDeps(t *testing.T) {
	out, err := Combine(
		[]model.Manifest{md("postgres")},
		[]RunResult{{Name: "feat-auth", Manifests: []model.Manifest{
			md("api", "postgres", "web"),
			md("web"),
		}}},
	)
	require.NoError(t, err)

	// Main's manifest passes through unchanged.
	require.Equal(t, model.ManifestName("postgres"), out[0].Name)
	// Worktree clones are prefixed; deps resolved: shared bare, own clone.
	require.Equal(t, model.ManifestName("wt:feat-auth/api"), out[1].Name)
	require.Equal(t, []model.ManifestName{"postgres", "wt:feat-auth/web"}, out[1].ResourceDependencies)
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), out[2].Name)
}

// Same worktree name twice → load error (plan §3: two worktrees with the
// same basename).
func TestCombine_DuplicateWorktreeName(t *testing.T) {
	_, err := Combine(nil, []RunResult{
		{Name: "feat-auth", Manifests: []model.Manifest{md("web")}},
		{Name: "feat-auth", Manifests: []model.Manifest{md("api")}},
	})
	require.ErrorContains(t, err, `two worktrees named "feat-auth"`)
}

// Double-define within one run → load error (plan §3/§11; the per-run loader
// rejects this first, local_resource.go:142 — this pass must stand alone).
func TestCombine_DoubleDefineWithinRun(t *testing.T) {
	_, err := Combine(nil, []RunResult{
		{Name: "feat-auth", Manifests: []model.Manifest{md("api"), md("api")}},
	})
	require.ErrorContains(t, err, `defined twice in worktree "feat-auth"`)

	_, err = Combine([]model.Manifest{md("postgres"), md("postgres")}, nil)
	require.ErrorContains(t, err, `defined twice in the main run`)
}

// The same resource name defined by two DIFFERENT worktrees → two distinct
// clones, not a double-define (plan §4.3).
func TestCombine_SameNameAcrossWorktreesDistinctClones(t *testing.T) {
	out, err := Combine(
		[]model.Manifest{md("postgres")},
		[]RunResult{
			{Name: "feat-auth", Manifests: []model.Manifest{md("api")}},
			{Name: "fix-ui", Manifests: []model.Manifest{md("api")}},
		},
	)
	require.NoError(t, err)
	names := []model.ManifestName{out[1].Name, out[2].Name}
	require.Contains(t, names, model.ManifestName("wt:feat-auth/api"))
	require.Contains(t, names, model.ManifestName("wt:fix-ui/api"))
}

// A worktree's definition of a main-defined resource wins: the shared
// manifest keeps its bare engine name and flows through the run (plan §3),
// exactly once — not duplicated next to main's copy.
func TestCombine_WorktreeDefinitionWins(t *testing.T) {
	out, err := Combine(
		[]model.Manifest{md("api", "postgres")},
		[]RunResult{{Name: "feat-auth", Manifests: []model.Manifest{md("api")}}},
	)
	require.NoError(t, err)
	var count int
	for _, m := range out {
		if m.Name == "api" {
			count++
		}
	}
	require.Equal(t, 1, count, "shared manifest appears once")
}

// Dep on a resource defined only by a DIFFERENT worktree → load error
// (plan §4.3: same-worktree first, else main-defined — never another
// worktree's).
func TestCombine_CrossWorktreeDepError(t *testing.T) {
	_, err := Combine(
		[]model.Manifest{md("postgres")},
		[]RunResult{
			{Name: "feat-auth", Manifests: []model.Manifest{md("api")}},
			{Name: "fix-ui", Manifests: []model.Manifest{md("web", "api")}},
		},
	)
	require.ErrorContains(t, err, `depends on "api"`)
}

// Undefined dep (main or worktree) → load error; the pass does not invent
// names (plan §11: load error by construction).
func TestCombine_UndefinedDepError(t *testing.T) {
	_, err := Combine([]model.Manifest{md("postgres", "missing")}, nil)
	require.ErrorContains(t, err, `depends on "missing"`)

	_, err = Combine(nil, []RunResult{
		{Name: "feat-auth", Manifests: []model.Manifest{md("web", "ghost")}},
	})
	require.ErrorContains(t, err, `depends on "ghost"`)
}

// One-way dep invariant (plan §12): a shared (main-defined) manifest must
// not depend on a worktree clone.
func TestCombine_OneWayDepViolation(t *testing.T) {
	_, err := Combine(
		[]model.Manifest{md("postgres", "wt:feat-auth/api")},
		[]RunResult{{Name: "feat-auth", Manifests: []model.Manifest{md("api")}}},
	)
	require.ErrorContains(t, err, "cannot depend on worktree-owned resources")
}

// A resource depended on by two worktrees and defined in neither's run
// resolves to main's shared definition (plan §3 validation bullet 2).
func TestCombine_ResourceDependedByTwoWorktreesSharedFromMain(t *testing.T) {
	out, err := Combine(
		[]model.Manifest{md("admin-bff-image")},
		[]RunResult{
			{Name: "feat-auth", Manifests: []model.Manifest{md("api", "admin-bff-image")}},
			{Name: "fix-ui", Manifests: []model.Manifest{md("web", "admin-bff-image")}},
		},
	)
	require.NoError(t, err)
	require.Equal(t, []model.ManifestName{"admin-bff-image"}, out[1].ResourceDependencies)
	require.Equal(t, []model.ManifestName{"admin-bff-image"}, out[2].ResourceDependencies)
}

// No main-run result (nil): run manifests are still prefixed, but bare deps
// cannot resolve and error — matching applyPrefix's nil-wtOwned contract.
func TestCombine_NilMainStillValidates(t *testing.T) {
	out, err := Combine(nil, []RunResult{{Name: "feat-auth", Manifests: []model.Manifest{md("web")}}})
	require.NoError(t, err)
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), out[0].Name)

	_, err = Combine(nil, []RunResult{{Name: "feat-auth", Manifests: []model.Manifest{md("web", "ghost")}}})
	require.ErrorContains(t, err, `depends on "ghost"`)
}

// Classic behavior: no worktrees → main manifests pass through, still
// validated for double-define and dep resolution.
func TestCombine_NoWorktrees(t *testing.T) {
	in := []model.Manifest{md("api", "postgres"), md("postgres")}
	out, err := Combine(in, nil)
	require.NoError(t, err)
	require.Equal(t, in, out)
}
