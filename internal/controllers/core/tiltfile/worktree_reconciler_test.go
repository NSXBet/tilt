package tiltfile

import (
	"context"
	"testing"
	"time"

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
// (`wt:<worktree>_<name>` clone names), hold-until-main for early worktree loads, and
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
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), reloaded.Manifests[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, reloaded.Manifests[0].ResourceDependencies,
		"dep on a main-defined shared resource stays bare")

	// The KubernetesApply object is stamped with the worktree name.
	var ka v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "wt:feat-auth_web"}, &ka))
	assert.Equal(t, "feat-auth", ka.Spec.Worktree)

	// The main run's own KubernetesApply is untouched.
	var mainKA v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "postgres"}, &mainKA))
	assert.Empty(t, mainKA.Spec.Worktree)
}

// A worktree run created while the main run's load is in flight must not
// EXECUTE at all: Tiltfile execution has process-global side effects — the
// loader records the main run's authored serve ports in the port registry
// during the main load — so the reconciler parks the START (gatedStarts)
// until the main run settles, then kicks it (mainLoadCompleted). This
// replaces the older park-the-result behavior: a worktree load that ran
// before main could claim an authored port before main reserved it, and
// both serve processes then bound the same port (observed on the worktrees
// example: the feature-a clone took 30001 under the main app).
//
// The gate (mainSettled) only engages while the main run exists but
// hasn't finished a load, so the main run is created while its load is
// blocked on a channel (mainRunBlocker). Everything else loads normally.
func TestWorktreeRun_BeforeMain_GatedUntilMainLoads(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	release := make(chan struct{})
	released := false
	releaseOnce := func() {
		if !released {
			close(release)
			released = true
		}
	}
	defer releaseOnce()
	f.tfl.Delegate = newMainRunBlocker(f, release)

	// The main run starts and blocks inside its load.
	main := manifestbuilder.New(f.tempdir, "postgres").WithK8sYAML(testyaml.PostgresYAML).Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{main}}
	f.Create(mainTiltfile(p))
	f.waitForRunning(model.MainTiltfileManifestName.String())

	// The worktree CR arrives while the main run is still blocked: its
	// start parks on the gate and the Tiltfile never executes.
	wt := manifestbuilder.New(f.tempdir, "web").WithK8sYAML(testyaml.SanchoYAML).
		WithResourceDeps("postgres").Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{Manifests: []model.Manifest{wt}}

	wtName := "tiltfile:feat-auth"
	f.Create(worktreeTiltfile("feat-auth", p))
	f.MustReconcile(types.NamespacedName{Name: wtName})
	f.waitForGated(wtName)

	// Gated: no run record (the load never ran), no dispatch, no clone
	// objects.
	_, dispatched := configsReloadedFor(t, f.st, wtName)
	assert.False(t, dispatched, "gated worktree run must not dispatch before the main run loads")
	var ka v1alpha1.KubernetesApply
	assert.False(t, f.Get(types.NamespacedName{Name: "wt:feat-auth_web"}, &ka),
		"gated worktree run must not create owned objects")

	// The main run's load completes: its result dispatches and the gated
	// start is kicked (mainLoadCompleted). Drain the queue: one reconcile
	// re-tests the gate and starts the run, the load's loaded-step requeue
	// then dispatches the engine-prefixed clone with deps resolved against
	// the main run's manifests.
	releaseOnce()
	f.popQueueUntilTerminatedAfter(model.MainTiltfileManifestName.String(), time.Now())

	wtTs := time.Now()
	f.MustReconcile(types.NamespacedName{Name: wtName})
	f.popQueueUntilTerminatedAfter(wtName, wtTs)

	var cloneKA v1alpha1.KubernetesApply
	require.True(t, f.Get(types.NamespacedName{Name: "wt:feat-auth_web"}, &cloneKA),
		"gated worktree run must create owned objects after the main run loads")
	assert.Equal(t, "feat-auth", cloneKA.Spec.Worktree)

	reloaded, ok := configsReloadedFor(t, f.st, wtName)
	require.True(t, ok, "started worktree run must dispatch")
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), reloaded.Manifests[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, reloaded.Manifests[0].ResourceDependencies,
		"dep resolution must use the main run's manifests")
}

// waitForGated waits until the run's START is parked on the hold-until-main
// gate: the Tiltfile has not executed — no run record — and the reconcile
// parked the nn in gatedStarts for mainLoadCompleted to kick.
func (f *fixture) waitForGated(name string) {
	f.T().Helper()
	nn := types.NamespacedName{Name: name}
	require.Eventually(f.T(), func() bool {
		f.r.mu.Lock()
		defer f.r.mu.Unlock()
		return f.r.runs[nn] == nil && f.r.gatedStarts[nn]
	}, time.Second, time.Millisecond, "waiting for run start to park on the worktree gate")
}

// mainRunBlocker blocks ONLY the main Tiltfile's load on `release`; every
// other run (the worktree runs under test) loads normally. The main run's
// load result is snapshotted at construction (results[tfl]) so later Result
// swaps — made while the main load is blocked for the worktree runs — do
// not leak into the main run's dispatch.
type mainRunBlocker struct {
	tfl     *tiltfile.FakeTiltfileLoader
	results map[string]tiltfile.TiltfileLoadResult
	release chan struct{}
}

func newMainRunBlocker(f *fixture, release chan struct{}) mainRunBlocker {
	return mainRunBlocker{
		tfl: f.tfl,
		results: map[string]tiltfile.TiltfileLoadResult{
			model.MainTiltfileManifestName.String(): f.tfl.Result,
		},
		release: release,
	}
}

func (b mainRunBlocker) Load(ctx context.Context, tf *v1alpha1.Tiltfile, prevResult *tiltfile.TiltfileLoadResult) tiltfile.TiltfileLoadResult {
	if tf.Name == model.MainTiltfileManifestName.String() {
		// Park here until the test releases the main run's load: the
		// worktree runs under test load while the main run is in-flight.
		<-b.release
		return b.results[tf.Name]
	}
	return b.tfl.Result
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
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), reloaded.Manifests[0].Name)

	var ka v1alpha1.KubernetesApply
	require.NoError(t, f.Client.Get(f.Context(), types.NamespacedName{Name: "wt:feat-auth_web"}, &ka))
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
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), reloaded.Manifests[0].Name)
}
