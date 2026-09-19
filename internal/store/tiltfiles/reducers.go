package tiltfiles

import (
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/model"
)

func HandleTiltfileUpsertAction(state *store.EngineState, action TiltfileUpsertAction) {
	n := action.Tiltfile.Name
	mn := model.ManifestName(n)
	state.Tiltfiles[n] = action.Tiltfile

	_, ok := state.TiltfileStates[mn]
	if !ok {
		state.TiltfileStates[mn] = store.NewTiltfileManifestState(mn)
	}

	// Only the main run feeds UserConfigState (worktree audit, plan §7.7):
	// worktree Tiltfile CRs share the main run's args (kept in sync by
	// SetTiltfileArgs), so letting them write here too would be redundant;
	// extensions carry their own args but are not this process's config.
	if mn == model.MainTiltfileManifestName {
		state.UserConfigState.Args = action.Tiltfile.Spec.Args
	}

	for _, x := range state.TiltfileDefinitionOrder {
		if x == mn {
			return // already in the order array
		}
	}
	state.TiltfileDefinitionOrder = append(state.TiltfileDefinitionOrder, mn)
}

func HandleTiltfileDeleteAction(state *store.EngineState, action TiltfileDeleteAction) {
	n := action.Name
	mn := model.ManifestName(n)
	delete(state.Tiltfiles, n)
	delete(state.TiltfileStates, mn)

	// Remove every manifest this run sourced (same removal rule as
	// HandleConfigsReloaded). Nothing reloads a deleted Tiltfile CR, so the
	// manifests would otherwise linger as ghost resources — the worktree
	// auto-watch deletes a worktree's CR when its checkout vanishes, and its
	// manifests must go with it. The CR's owned apiserver objects (Cmds,
	// KubernetesApplys, ...) are deleted by the reconciler's updateOwnedObjects,
	// and the clone objects by the worktree GC pass.
	for _, mt := range state.Targets() {
		if mt.Manifest.SourceTiltfile == mn {
			state.RemoveManifestTarget(mt.Manifest.Name)
		}
	}

	for i, x := range state.TiltfileDefinitionOrder {
		if x == mn {
			state.TiltfileDefinitionOrder = append(
				state.TiltfileDefinitionOrder[:i],
				state.TiltfileDefinitionOrder[i+1:]...)
			return
		}
	}
}
