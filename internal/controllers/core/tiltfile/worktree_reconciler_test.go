package tiltfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/k8s/testyaml"
	"github.com/tilt-dev/tilt/internal/testutils/manifestbuilder"
	"github.com/tilt-dev/tilt/internal/tiltfile"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance (plan §7.3): the Tiltfile reconciler handles multiple Tiltfiles.
// Every Tiltfile CR is one engine execution — the main "(Tiltfile)" and one
// "tiltfile:<worktree>" per worktree — with per-run engine-prefix rewriting
// (wt:<worktree>/<name>), hold-until-main for early worktree loads, and
// worktree stamping on KubernetesApply objects so apply-time clone stamping
// fires. Retires TODO(nick) at the old reconciler.go:365.

// worktreeTiltfile builds a Tiltfile CR for a worktree run, carrying the
// tilt.dev/worktree label (the CR shape from internal/controllers/apis/tiltfile).
func worktreeTiltfile(name, path string) *v1alpha1.Tiltfile {
	return &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "tiltfile:" + name,
			Labels: map[string]string{v1alpha1.LabelWorktree: name},
		},
		Spec: v1alpha1.TiltfileSpec{
			Path: path,
		},
	}
}

func mainTiltfile(path string) *v1alpha1.Tiltfile {
	return &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{Name: model.MainTiltfileManifestName.String()},
		Spec:       v1alpha1.TiltfileSpec{Path: path},
	}
}

// configsReloadedFor returns the ConfigsReloadedAction dispatched for the
// given run name, if any.
func configsReloadedFor(t *testing.T, st *testStore, name string) (ConfigsReloadedAction, bool) {
	t.Helper()
	var found ConfigsReloadedAction
	for _, a := range st.Actions() {
		if cra, ok := a.(ConfigsReloadedAction); ok && cra.Name.String() == name {
			found = cra
		}
	}
	return found, found.Name != ""
}

// A worktree run whose load completes after the main run's: its manifests are
// engine-prefixed and dispatched as clones, and its KubernetesApply objects
// carry the worktree stamp.
func TestWorktreeRun_AfterMain_PrefixedAndStamped(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	// Main run defines the shared resource.
	main := manifestbuilder.New(f.tempdir, "postgres").WithK8sYAML(testyaml.PostgresYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{main}}
	f.createAndWaitForLoaded(mainTiltfile(p))

	// Worktree run re-defines its own resource and depends on the shared one.
	wt := manifestbuilder.New(f.tempdir, "web").WithK8sYAML(testyaml.SanchoYAML).Build()
	wt.ResourceDependencies = []model.ManifestName{"postgres"}
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{wt}}

	f.createAndWaitForLoaded(worktreeTiltfile("feat-auth", p))

	// The dispatch carries the engine-prefixed clone.
	reloaded, ok := configsReloadedFor(t, f.st, "tiltfile:feat-auth")
	require.True(t, ok, "no ConfigsReloadedAction for the worktree run")
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), reloaded.Manifests[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, reloaded.Manifests[0].ResourceDependencies,
		"dep on a main-defined shared resource stays bare")

	// The KubernetesApply object is stamped with the worktree name.
	var ka v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "wt:feat-auth/web"}, &ka))
	assert.Equal(t, "feat-auth", ka.Spec.Worktree)

	// The main run's own KubernetesApply is untouched.
	var mainKA v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "postgres"}, &mainKA))
	assert.Empty(t, mainKA.Spec.Worktree)
}

// A worktree run whose load completes BEFORE the main run's is parked
// (hold-until-main) and replayed when the main run's load completes.
func TestWorktreeRun_BeforeMain_HeldUntilMainLoads(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	wt := manifestbuilder.New(f.tempdir, "web").WithK8sYAML(testyaml.SanchoYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{wt}}

	f.createAndWaitForLoaded(worktreeTiltfile("feat-auth", p))

	// Held: no ConfigsReloadedAction for the worktree run yet, no clone
	// objects in the apiserver.
	_, dispatched := configsReloadedFor(t, f.st, "tiltfile:feat-auth")
	assert.False(t, dispatched, "held worktree run must not dispatch before the main run loads")
	var ka v1alpha1.KubernetesApply
	assert.False(t, f.Get(types.NamespacedName{Name: "wt:feat-auth/web"}, &ka),
		"held worktree run must not create owned objects")

	// The main run loads: the held run replays against it.
	main := manifestbuilder.New(f.tempdir, "postgres").WithK8sYAML(testyaml.PostgresYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{main}}
	f.createAndWaitForLoaded(mainTiltfile(p))

	var cloneKA v1alpha1.KubernetesApply
	require.Eventually(t, func() bool {
		return f.Get(types.NamespacedName{Name: "wt:feat-auth/web"}, &cloneKA)
	}, 1e9, 1e6, "held worktree run must replay after the main run loads")
	assert.Equal(t, "feat-auth", cloneKA.Spec.Worktree)

	reloaded, ok := configsReloadedFor(t, f.st, "tiltfile:feat-auth")
	require.True(t, ok, "replayed worktree run must dispatch")
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), reloaded.Manifests[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, reloaded.Manifests[0].ResourceDependencies,
		"dep resolution must use the main run's manifests after replay")
}

// A worktree run on its own — no main Tiltfile CR exists: the hold releases
// and the run replays as a standalone worktree.
func TestWorktreeRun_Standalone_NoMainCR(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	wt := manifestbuilder.New(f.tempdir, "web").WithK8sYAML(testyaml.SanchoYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{wt}}

	f.createAndWaitForLoaded(worktreeTiltfile("feat-auth", p))

	// The main CR has never existed: the hold released on the second
	// reconcile (popQueueUntilTerminatedAfter drove it), and the run
	// dispatched as a standalone worktree.
	reloaded, ok := configsReloadedFor(t, f.st, "tiltfile:feat-auth")
	require.True(t, ok, "standalone worktree run must dispatch once the hold releases")
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), reloaded.Manifests[0].Name)

	var ka v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "wt:feat-auth/web"}, &ka))
	assert.Equal(t, "feat-auth", ka.Spec.Worktree)
}

// The main run is never prefixed, even when worktrees exist.
func TestMainRun_NeverPrefixed(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	main := manifestbuilder.New(f.tempdir, "postgres").WithK8sYAML(testyaml.PostgresYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{main}}

	f.createAndWaitForLoaded(mainTiltfile(p))

	var ka v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "postgres"}, &ka))
	assert.Empty(t, ka.Spec.Worktree)

	reloaded, ok := configsReloadedFor(t, f.st, model.MainTiltfileManifestName.String())
	require.True(t, ok, "main run must dispatch")
	require.Equal(t, model.ManifestName("postgres"), reloaded.Manifests[0].Name)
}

// A worktree run loading after a FAILED main run: the hold releases (main is
// settled), the manifests still prefix, and the failure is visible in the run
// status — no deadlock waiting for a main result that never comes.
func TestWorktreeRun_AfterFailedMain(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	main := manifestbuilder.New(f.tempdir, "postgres").WithK8sYAML(testyaml.PostgresYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{
		Manifests: []model.Manifest{main},
		Error:     assert.AnError,
	}
	f.createAndWaitForLoaded(mainTiltfile(p))

	wt := manifestbuilder.New(f.tempdir, "web").WithK8sYAML(testyaml.SanchoYAML).Build()
	wt.ResourceDependencies = []model.ManifestName{"postgres"}
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{wt}}

	f.createAndWaitForLoaded(worktreeTiltfile("feat-auth", p))

	// The dispatch still happened (main settled, albeit with an error).
	reloaded, ok := configsReloadedFor(t, f.st, "tiltfile:feat-auth")
	require.True(t, ok, "worktree run must dispatch after a failed main run settles")
	require.Equal(t, model.ManifestName("wt:feat-auth/web"), reloaded.Manifests[0].Name)
}
