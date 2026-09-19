package configs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/controllers/apis/uibutton"
	"github.com/tilt-dev/tilt/internal/controllers/fake"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance tests for multi-Tiltfile CR creation (plan §7.2, fixture tk-jq8):
//
//  1. With worktrees discovered, the configs controller creates one Tiltfile
//     CR per worktree — named "tiltfile:<worktree>", carrying the worktree
//     name — alongside the main "(Tiltfile)" CR.
//  2. The main CR is unchanged: same name, path, args, RestartOn/StopOn
//     wiring as the single-Tiltfile case.
//  3. With no worktrees, only the main CR exists (classic behavior).
//
// These encode the contract the Striker implements; worktree assertions
// below are EXPECTED to fail until that lands.
//
// Worktree discovery itself is covered in internal/tiltfile/worktree
// (discover_test.go); these tests stub the discovered set at the controller
// boundary, since discovery and creation are separate contracts.

// The worktree Tiltfile CRs the controller is expected to create: one per
// discovered worktree, named "tiltfile:<worktree>", alongside the main
// "(Tiltfile)" CR.
func worktreeTiltfileName(name string) string {
	return "tiltfile:" + name
}

// listTiltfileNames returns all Tiltfile CR names currently in the apiserver.
func listTiltfileNames(t *testing.T, cl ctrlclient.Client) []string {
	t.Helper()
	var list v1alpha1.TiltfileList
	require.NoError(t, cl.List(context.Background(), &list))
	names := make([]string, 0, len(list.Items))
	for _, tf := range list.Items {
		names = append(names, tf.Name)
	}
	return names
}

// worktreeLabelOf returns the worktree a Tiltfile CR was created for, or ""
// if the CR does not carry one. The carrier is a `tilt.dev/worktree` label on
// ObjectMeta (plan §3: "plus a marker the reconciler sets"). If the striker
// implements the marker as an annotation or a spec field instead, update this
// helper only — the behavioral assertions below stay fixed.
func worktreeLabelOf(t *testing.T, cl ctrlclient.Client, name string) string {
	t.Helper()
	var tf v1alpha1.Tiltfile
	err := cl.Get(context.Background(), types.NamespacedName{Name: name}, &tf)
	if err != nil {
		t.Fatalf("get tiltfile %q: %v", name, err)
	}
	return tf.ObjectMeta.Labels["tilt.dev/worktree"]
}

// TestCreateTiltfile_Worktrees pins the multi-Tiltfile contract: one Tiltfile
// CR per discovered worktree plus the unchanged main CR.
func TestCreateTiltfile_Worktrees(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.DesiredTiltfilePath = "./fake-tiltfile-path"
		s.UserConfigState = model.NewUserConfigState([]string{"arg1"})
		// Worktrees "feature-a" and "feature-b" are discovered and re-executed.
		// Discovered set is observable at the store boundary
		// (EngineState.Worktrees — the seam the discovery driver seeds and the
		// configs controller consumes); discovery itself is covered in
		// internal/tiltfile/worktree (discover_test.go).
		s.Worktrees = []worktree.Worktree{
			{Name: "feature-a", Dir: "/tmp/fake/.worktree/feature-a"},
			{Name: "feature-b", Dir: "/tmp/fake/.worktree/feature-b"},
		}
	})
	ctx := context.Background()
	cl := fake.NewFakeTiltClient()
	cc := NewConfigsController(cl)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	// One CR per discovered worktree, named tiltfile:<worktree>.
	for _, wt := range []string{"feature-a", "feature-b"} {
		var wtf v1alpha1.Tiltfile
		err := cl.Get(ctx, types.NamespacedName{Name: worktreeTiltfileName(wt)}, &wtf)
		assert.NoError(t, err, "Tiltfile CR for worktree %q must exist", wt)
		if err != nil {
			continue
		}
		assert.Equal(t, tiltfile.ResolveFilename("fake-tiltfile-path"), wtf.Spec.Path,
			"worktree Tiltfile must point at the root Tiltfile")
		assert.Equal(t, wt, worktreeLabelOf(t, cl, worktreeTiltfileName(wt)),
			"worktree Tiltfile must carry its worktree name")
	}

	// Plus the main CR: unchanged.
	assert.ElementsMatch(t, []string{
		model.MainTiltfileManifestName.String(),
		worktreeTiltfileName("feature-a"),
		worktreeTiltfileName("feature-b"),
	}, listTiltfileNames(t, cl), "exactly one CR per worktree plus main")

	// Main CR spec is identical to the single-Tiltfile contract.
	var tf v1alpha1.Tiltfile
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: model.MainTiltfileManifestName.String()}, &tf))
	expectedTfSpec := v1alpha1.TiltfileSpec{
		Path: tiltfile.ResolveFilename("fake-tiltfile-path"),
		Args: []string{"arg1"},
		RestartOn: &v1alpha1.RestartOnSpec{
			FileWatches: []string{"configs:(Tiltfile)"},
		},
		StopOn: &v1alpha1.StopOnSpec{
			UIButtons: []string{uibutton.StopBuildButtonName("(Tiltfile)")},
		},
	}
	assert.Equal(t, expectedTfSpec, tf.Spec, "main Tiltfile CR must be unchanged by worktrees")

	assert.Empty(t, worktreeLabelOf(t, cl, model.MainTiltfileManifestName.String()),
		"main Tiltfile must not carry a worktree label")
}

// TestCreateTiltfile_NoWorktrees pins classic behavior: no worktrees
// discovered → only the main Tiltfile CR exists.
func TestCreateTiltfile_NoWorktrees(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.DesiredTiltfilePath = "./fake-tiltfile-path"
	})
	ctx := context.Background()
	cl := fake.NewFakeTiltClient()
	cc := NewConfigsController(cl)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	assert.Equal(t, []string{model.MainTiltfileManifestName.String()},
		listTiltfileNames(t, cl), "no worktree Tiltfile CRs without worktrees")
}

// Worktree auto-watch sync: EngineState.Worktrees changes at runtime when
// the watcher re-discovers the worktree dir, and the controller must
// reconcile the worktree Tiltfile CRs against it — creating a CR (+ stop
// button) for each new worktree and deleting the CR (+ stop button) of each
// removed one. The main CR is not part of the synced set.
//
// Delete scoping: only CRs this process created are deleted. A
// worktree-labeled CR the controller never synced (e.g. from another Tilt
// process — one engine per repo, but the CR set is process-local) is never
// swept, mirroring the clone GC scoping rule.

func setWorktrees(st *store.TestingStore, wts ...string) {
	st.WithState(func(s *store.EngineState) {
		s.Worktrees = make([]worktree.Worktree, 0, len(wts))
		for _, name := range wts {
			s.Worktrees = append(s.Worktrees, worktree.Worktree{
				Name: name,
				Dir:  "/tmp/fake/.worktree/" + name,
			})
		}
	})
}

func buttonExists(t *testing.T, cl ctrlclient.Client, name string) bool {
	t.Helper()
	var b v1alpha1.UIButton
	err := cl.Get(context.Background(), types.NamespacedName{Name: name}, &b)
	return err == nil
}

// A worktree appearing in the desired set after the initial load gets its
// Tiltfile CR — the same shape a startup-discovered worktree gets.
func TestSyncWorktrees_PicksUpNewWorktree(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.DesiredTiltfilePath = "./fake-tiltfile-path"
	})
	ctx := context.Background()
	cl := fake.NewFakeTiltClient()
	cc := NewConfigsController(cl)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))
	require.Equal(t, []string{model.MainTiltfileManifestName.String()}, listTiltfileNames(t, cl))

	setWorktrees(st, "feat-late")
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	var wtf v1alpha1.Tiltfile
	require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: worktreeTiltfileName("feat-late")}, &wtf),
		"a worktree discovered at runtime must get its Tiltfile CR")
	assert.Equal(t, "feat-late", worktreeLabelOf(t, cl, worktreeTiltfileName("feat-late")))
	assert.True(t, buttonExists(t, cl, uibutton.StopBuildButtonName(worktreeTiltfileName("feat-late"))),
		"a runtime-discovered worktree must get its stop button")
	assert.ElementsMatch(t, []string{
		model.MainTiltfileManifestName.String(),
		worktreeTiltfileName("feat-late"),
	}, listTiltfileNames(t, cl))
}

// A worktree leaving the desired set (checkout removed while tilt runs) has
// its Tiltfile CR and stop button deleted — the teardown path the clone GC
// and the owned-object deletion cascade through.
func TestSyncWorktrees_DeletesRemovedWorktree(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.DesiredTiltfilePath = "./fake-tiltfile-path"
	})
	setWorktrees(st, "feat-a", "feat-b")
	ctx := context.Background()
	cl := fake.NewFakeTiltClient()
	cc := NewConfigsController(cl)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))
	require.Len(t, listTiltfileNames(t, cl), 3, "main + two worktree CRs")

	setWorktrees(st, "feat-a")
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	assert.ElementsMatch(t, []string{
		model.MainTiltfileManifestName.String(),
		worktreeTiltfileName("feat-a"),
	}, listTiltfileNames(t, cl), "the removed worktree's CR must be deleted")
	assert.False(t, buttonExists(t, cl, uibutton.StopBuildButtonName(worktreeTiltfileName("feat-b"))),
		"the removed worktree's stop button must be deleted")
	assert.True(t, buttonExists(t, cl, uibutton.StopBuildButtonName(worktreeTiltfileName("feat-a"))),
		"a kept worktree's stop button must stay")
}

// Only synced CRs are deleted: a worktree-labeled Tiltfile CR this process
// never created is never swept, mirroring the clone GC scoping rule.
func TestSyncWorktrees_LeavesForeignWorktreeCR(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.DesiredTiltfilePath = "./fake-tiltfile-path"
	})
	setWorktrees(st, "feat-a")
	ctx := context.Background()
	cl := fake.NewFakeTiltClient()
	cc := NewConfigsController(cl)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	foreign := &v1alpha1.Tiltfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:   worktreeTiltfileName("other-process"),
			Labels: map[string]string{v1alpha1.LabelWorktree: "other-process"},
		},
	}
	require.NoError(t, cl.Create(ctx, foreign))

	setWorktrees(st)
	require.NoError(t, cc.OnChange(ctx, st, store.ChangeSummary{}))

	assert.Equal(t, []string{
		model.MainTiltfileManifestName.String(),
		worktreeTiltfileName("other-process"),
	}, listTiltfileNames(t, cl),
		"the unsynced CR must survive; only CRs this process created are deleted")
}
