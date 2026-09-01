package configs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
