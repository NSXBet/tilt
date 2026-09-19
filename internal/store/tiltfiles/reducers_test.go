package tiltfiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// HandleTiltfileDeleteAction must remove the deleted run's manifests along
// with its Tiltfile record: nothing reloads a deleted Tiltfile CR, so its
// manifests would otherwise linger as ghost resources. The worktree
// auto-watch deletes a worktree's CR when its checkout vanishes.
//
// The removal rule is the same one HandleConfigsReloaded applies for
// manifests dropped from a reload: SourceTiltfile == the deleted CR's name.
func TestTiltfileDeleteRemovesSourcedManifests(t *testing.T) {
	state := store.NewState()

	mainTf := &v1alpha1.Tiltfile{ObjectMeta: metav1.ObjectMeta{Name: model.MainTiltfileManifestName.String()}}
	wtTf := &v1alpha1.Tiltfile{ObjectMeta: metav1.ObjectMeta{Name: "tiltfile:feat-a"}}
	HandleTiltfileUpsertAction(state, TiltfileUpsertAction{Tiltfile: mainTf})
	HandleTiltfileUpsertAction(state, TiltfileUpsertAction{Tiltfile: wtTf})

	mainManifest := model.Manifest{Name: "web"}
	mainManifest.SourceTiltfile = model.MainTiltfileManifestName
	wtManifest := model.Manifest{Name: "wt:feat-a/web"}
	wtManifest.SourceTiltfile = "tiltfile:feat-a"
	state.UpsertManifestTarget(store.NewManifestTarget(mainManifest))
	state.UpsertManifestTarget(store.NewManifestTarget(wtManifest))

	HandleTiltfileDeleteAction(state, TiltfileDeleteAction{Name: "tiltfile:feat-a"})

	_, ok := state.ManifestTargets[wtManifest.Name]
	assert.False(t, ok, "the deleted run's manifest must be removed")
	_, ok = state.ManifestTargets[mainManifest.Name]
	assert.True(t, ok, "other runs' manifests must be untouched")

	assert.NotContains(t, state.ManifestDefinitionOrder, wtManifest.Name,
		"the deleted run's manifest must leave the definition order")

	assert.NotContains(t, state.Tiltfiles, "tiltfile:feat-a", "the deleted Tiltfile record must go")
	assert.NotContains(t, state.TiltfileStates, model.ManifestName("tiltfile:feat-a"),
		"the deleted run's TiltfileState must go")
	assert.Contains(t, state.Tiltfiles, mainTf.Name, "the main Tiltfile record must stay")
}

// Deleting an unknown Tiltfile CR is a no-op (NotFound path in the
// reconciler dispatches a delete action regardless).
func TestTiltfileDeleteUnknownNoop(t *testing.T) {
	state := store.NewState()
	require.NotPanics(t, func() {
		HandleTiltfileDeleteAction(state, TiltfileDeleteAction{Name: "tiltfile:never-existed"})
	})
}
