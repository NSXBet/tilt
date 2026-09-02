package tiltfile

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/testutils/manifestbuilder"
	"github.com/tilt-dev/tilt/internal/tiltfile"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// tk-ntx acceptance: the engine-prefix rewrite pass wired into the
// reconciler (plan §4.3, §7.3). A worktree-labeled Tiltfile CR re-executes
// the root Tiltfile; its load result is rewritten to clone names
// (`wt:<worktree>_<name>`) before the ConfigsReloadedAction dispatch. The
// main CR is untouched (bare names).

// Worktree run: manifests dispatched to the engine carry clone names, and
// deps resolve against the main run's manifest set (main-defined → bare).
func TestReconciler_WorktreeBoundaryRewrite(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	// The main run already produced its (bare) result: postgres is shared,
	// web is this worktree's own.
	f.r.runs[types.NamespacedName{Name: model.MainTiltfileManifestName.String()}] = &runStatus{
		step: runStepDone,
		tlr: &tiltfile.TiltfileLoadResult{
			Manifests: []model.Manifest{manifestbuilder.New(f.tempdir, "postgres").WithLocalServeCmd(".").Build()},
		},
	}

	web := manifestbuilder.New(f.tempdir, "web").
		WithLocalServeCmd(".").
		WithResourceDeps("postgres").
		Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{
		Manifests: []model.Manifest{web},
	}

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", p, nil)
	f.createAndWaitForLoaded(tf)

	require.Equal(t, "", tf.Status.Terminated.Error)

	a := f.st.WaitForAction(t, actionTypeConfigsReloaded()).(ConfigsReloadedAction)
	require.NoError(t, a.Err)
	require.Equal(t, 1, len(a.Manifests))
	m := a.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), m.Name,
		"worktree run manifest must be rewritten to the clone name at the boundary")
	require.Equal(t, []model.ManifestName{"postgres"}, m.ResourceDependencies,
		"dep on a main-defined (shared) manifest stays bare")
	require.Equal(t, model.ManifestName("tiltfile:feat-auth"), m.SourceTiltfile)
}

// Main run: no worktree label — no rewrite. Bare names preserved.
func TestReconciler_MainRunUnchanged(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	m := manifestbuilder.New(f.tempdir, "foo").WithLocalServeCmd(".").Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{
		Manifests: []model.Manifest{m},
	}

	tf := v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{Name: model.MainTiltfileManifestName.String()},
		Spec:       v1alpha1.TiltfileSpec{Path: p},
	}
	f.createAndWaitForLoaded(&tf)

	a := f.st.WaitForAction(t, actionTypeConfigsReloaded()).(ConfigsReloadedAction)
	require.NoError(t, a.Err)
	require.Equal(t, 1, len(a.Manifests))
	assert.Equal(t, model.ManifestName("foo"), a.Manifests[0].Name)
}

// Worktree run with no main result available (no main CR ran — the main CR
// has never existed, or the main run failed): the rewrite is conservative —
// the clone name is rewritten, bare deps are left untouched, and the run
// still dispatches. The engine reports the unknown dep when the clone's
// resources reference a shared resource the main run never defined.
func TestReconciler_WorktreeBoundaryNoMainResult(t *testing.T) {
	f := newFixture(t)
	p := f.tempdir.JoinPath("Tiltfile")

	web := manifestbuilder.New(f.tempdir, "web").
		WithLocalServeCmd(".").
		WithResourceDeps("postgres").
		Build()
	f.tfl.Result = tiltfile.TiltfileLoadResult{
		Manifests: []model.Manifest{web},
	}

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", p, nil)
	f.createAndWaitForLoaded(tf)

	a := f.st.WaitForAction(t, actionTypeConfigsReloaded()).(ConfigsReloadedAction)
	require.NoError(t, a.Err, "nil main = conservative rewrite; the run dispatches")
	require.Equal(t, 1, len(a.Manifests))
	assert.Equal(t, model.ManifestName("wt:feat-auth_web"), a.Manifests[0].Name)
	assert.Equal(t, []model.ManifestName{"postgres"}, a.Manifests[0].ResourceDependencies,
		"deps are left untouched when no main result exists to resolve against")
}

// actionTypeConfigsReloaded is the reflect.Type of ConfigsReloadedAction,
// for store.WaitForAction.
func actionTypeConfigsReloaded() reflect.Type {
	return reflect.TypeOf(ConfigsReloadedAction{})
}
