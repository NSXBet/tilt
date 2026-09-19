package worktree

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/pkg/model"
)

// Phase 1 acceptance: engine-prefix rewrite at the Tiltfile boundary
// (plan §4.3). Worktree-run manifests are engine-internal prefixed
// `wt:<name>_<name>`; author-visible names stay bare. Deps resolve
// same-worktree first, else main-defined.

func TestApplyPrefix_ManifestAndDeps(t *testing.T) {
	res := model.Manifest{Name: "incidents-admin"}
	res.ResourceDependencies = []model.ManifestName{"api"}

	out := applyPrefix([]model.Manifest{{Name: "postgres"}, res}, "feat-auth", map[model.ManifestName]bool{"postgres": true})

	require.Equal(t, model.ManifestName("wt:feat-auth_incidents-admin"), out[1].Name)
	// "api" is not main-defined → rewritten to the clone name
	// (same-worktree first)
	require.Equal(t, []model.ManifestName{"wt:feat-auth_api"}, out[1].ResourceDependencies)
	// The main-defined postgres manifest itself becomes this run's clone
	// (plan §4.3); its deps stay untouched (none here).
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), out[0].Name)
	require.Nil(t, out[0].ResourceDependencies)
}

// A worktree-run manifest named identically to a main-defined manifest is a
// clone: the author wrote the same name; the engine keeps them apart by
// prefix. Not a double-define error — double-define within one run is the
// tiltfile loader's job (local_resource already errors, local_resource.go:142).
func TestApplyPrefix_CloneOfMainManifest(t *testing.T) {
	out := applyPrefix([]model.Manifest{{Name: "incidents-admin"}}, "feat-auth", nil)
	require.Equal(t, model.ManifestName("wt:feat-auth_incidents-admin"), out[0].Name)
}

// SourceTiltfile: the manifest records which Tiltfile produced it, so
// per-worktree reloads and UI grouping can attribute resources (plan §1,
// Manifest.SourceTiltfile exists but is underused).
func TestApplyPrefix_SetsSourceTiltfile(t *testing.T) {
	out := applyPrefix([]model.Manifest{{Name: "web"}}, "feat-auth", nil)
	require.Equal(t, model.ManifestName("tiltfile:feat-auth"), out[0].SourceTiltfile)
}

// Worktree-run dep on a name defined by NEITHER this worktree's run nor main —
// undefined reference stays an error (plan §11 "one-way dep violation → load
// error by construction"). The prefix pass does not invent names.
func TestApplyPrefix_UndefinedDepStaysBare(t *testing.T) {
	m := model.Manifest{Name: "web"}
	m.ResourceDependencies = []model.ManifestName{"missing"}
	out := applyPrefix([]model.Manifest{m}, "feat-auth", nil)
	require.Equal(t, []model.ManifestName{"missing"}, out[0].ResourceDependencies)
}

// The shared (main-defined) manifest itself flowing through a worktree run:
// it becomes this run's clone (plan §4.3) and no dep on main's shared copy
// is invented. Its own deps resolve by wtOwned membership like any other
// manifest.
func TestApplyPrefix_SharedManifestClonesWithoutSelfDep(t *testing.T) {
	// "postgres" is defined by the main run; the worktree run also produces it.
	m := model.Manifest{Name: "postgres"}
	m.ResourceDependencies = []model.ManifestName{"api"}
	out := applyPrefix([]model.Manifest{m}, "feat-auth", map[model.ManifestName]bool{"postgres": true})
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), out[0].Name)
	// "api" is not main-defined -> rewritten to this worktree's clone name.
	require.Equal(t, []model.ManifestName{"wt:feat-auth_api"}, out[0].ResourceDependencies)

	// A pre-existing dep on the shared name stays bare (main-defined).
	m2 := model.Manifest{Name: "postgres"}
	m2.ResourceDependencies = []model.ManifestName{"postgres"}
	out2 := applyPrefix([]model.Manifest{m2}, "feat-auth", map[model.ManifestName]bool{"postgres": true})
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), out2[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, out2[0].ResourceDependencies)
}

// Main-run manifests are never rewritten.
func TestApplyPrefix_MainRunNoop(t *testing.T) {
	in := []model.Manifest{{Name: "api"}}
	in[0].ResourceDependencies = []model.ManifestName{"postgres"}
	out := applyPrefix(in, "", nil)
	require.Equal(t, in, out)
}

// tk-ntx acceptance: the rewrite pass sits at the engine boundary and must
// not mutate its inputs. validate.go captures raw (author-visible) deps from
// run.Manifests AFTER calling applyPrefix (validate.go rawDeps), so an
// in-place dep rewrite corrupts the caller's run results and turns
// author-visible dep errors into clone-name errors.
func TestApplyPrefix_NoInputMutation(t *testing.T) {
	shared := md("postgres")
	own := md("api", "postgres", "web")
	in := []model.Manifest{shared, own}

	_ = applyPrefix(in, "feat-auth", map[model.ManifestName]bool{"postgres": true})

	// Input manifests unchanged: names bare, deps author-visible.
	require.Equal(t, model.ManifestName("postgres"), in[0].Name)
	require.Equal(t, model.ManifestName("api"), in[1].Name)
	require.Equal(t, []model.ManifestName{"postgres", "web"}, in[1].ResourceDependencies)

	// Same for the nil-wtOwned path (no main-run result).
	in2 := []model.Manifest{md("api", "postgres")}
	_ = applyPrefix(in2, "feat-auth", nil)
	require.Equal(t, []model.ManifestName{"postgres"}, in2[0].ResourceDependencies)
}

// tk-ntx acceptance: output manifests must not alias the input's dep slices,
// so later callers can't be corrupted through shared backing arrays.
func TestApplyPrefix_OutputIndependentOfInput(t *testing.T) {
	in := []model.Manifest{md("api", "postgres", "web")}
	out := applyPrefix(in, "feat-auth", map[model.ManifestName]bool{})

	// Mutating the output must not touch the input's dep slice.
	out[0].ResourceDependencies[0] = "tampered"
	require.Equal(t, []model.ManifestName{"postgres", "web"}, in[0].ResourceDependencies)
}

// tk-ntx acceptance: dep rewrite matrix at the wtOwned boundary (plan §4.3
// "same-worktree first, else main-defined"). A non-shared manifest's deps:
// main-defined → stays bare; anything else (own or unknown) → clone name.
func TestApplyPrefix_DepRewriteMatrix(t *testing.T) {
	out := applyPrefix(
		[]model.Manifest{md("api", "postgres", "web", "ghost")},
		"feat-auth",
		map[model.ManifestName]bool{"postgres": true},
	)
	require.Equal(t, []model.ManifestName{
		"postgres",           // main-defined: shared, stays bare
		"wt:feat-auth_web",   // not main-defined: own clone
		"wt:feat-auth_ghost", // not main-defined: unknown falls outside wtOwned
	}, out[0].ResourceDependencies)
}

// tk-ntx acceptance: a nil-deps manifest stays nil-deps (no spurious empty
// slice), and ManifestName composes the `wt:<worktree>_<name>` grammar used
// by validate.go's isCloneName.
func TestApplyPrefix_NilDepsStaysNil(t *testing.T) {
	out := applyPrefix([]model.Manifest{{Name: "web"}}, "feat-auth", map[model.ManifestName]bool{})
	require.Nil(t, out[0].ResourceDependencies)

	require.Equal(t, model.ManifestName("wt:feat-auth_web"), ManifestName("feat-auth", "web"))
	require.True(t, strings.HasPrefix(string(ManifestName("feat-auth", "web")), "wt:"))
}
