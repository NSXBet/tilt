package hud

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

func TestStateToTerminalViewWorktreeFromLabels(t *testing.T) {
	// Worktree runs stamp tilt.dev/worktree on their manifests (plan §9);
	// StateToTerminalView propagates it into the terminal view.
	m := model.Manifest{
		Name:   "wt:feat-auth/api",
		Labels: map[string]string{v1alpha1.LabelWorktree: "feat-auth"},
	}.WithDeployTarget(model.K8sTarget{})
	shared := model.Manifest{Name: "postgres"}.WithDeployTarget(model.K8sTarget{})

	state := newState([]model.Manifest{shared, m})
	v := StateToTerminalView(*state, &sync.RWMutex{})

	require.Len(t, v.Resources, 3) // (Tiltfile) row + 2 manifests

	r, ok := v.Resource("wt:feat-auth/api")
	require.True(t, ok)
	assert.Equal(t, "feat-auth", r.Worktree)

	r, ok = v.Resource("postgres")
	require.True(t, ok)
	assert.Equal(t, "", r.Worktree)
}

func TestStateToTerminalViewWorktreeTiltfileRow(t *testing.T) {
	state := newState(nil)

	// Worktree runs load a `tiltfile:<worktree>` CR (plan §2): its row is
	// named after the run and carries the worktree for grouping.
	wtName := model.ManifestName("tiltfile:feat-auth")
	state.TiltfileDefinitionOrder = append(state.TiltfileDefinitionOrder, wtName)
	state.TiltfileStates[wtName] = store.NewTiltfileManifestState(wtName)

	v := StateToTerminalView(*state, &sync.RWMutex{})

	require.Len(t, v.Resources, 2)
	assert.Equal(t, model.MainTiltfileManifestName, v.Resources[0].Name)
	assert.Equal(t, "", v.Resources[0].Worktree)
	assert.Equal(t, wtName, v.Resources[1].Name)
	assert.Equal(t, "feat-auth", v.Resources[1].Worktree)
	assert.True(t, v.Resources[1].IsTiltfile)
}
