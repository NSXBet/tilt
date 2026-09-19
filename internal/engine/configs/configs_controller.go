package configs

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/controllers/apis/uibutton"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

type ConfigsController struct {
	ctrlClient               ctrlclient.Client
	isInitialTiltfileCreated bool
	// Worktree names this process has synced (worktree auto-watch): the
	// delete side of the sync only ever touches CRs this process created —
	// a worktree-labeled CR from another Tilt process is never swept (same
	// scoping rule as the clone GC, kubernetesapply/worktree_gc.go).
	syncedWorktrees map[string]bool
}

func NewConfigsController(ctrlClient ctrlclient.Client) *ConfigsController {
	return &ConfigsController{
		ctrlClient: ctrlClient,
	}
}

func (cc *ConfigsController) OnChange(ctx context.Context, st store.RStore, summary store.ChangeSummary) error {
	if summary.IsLogOnly() {
		return nil
	}

	if !cc.isInitialTiltfileCreated {
		err := cc.maybeCreateInitialTiltfile(ctx, st)
		if err != nil {
			return err
		}
	}

	// Worktree auto-watch: the worktree set changes at runtime when the
	// watcher (internal/engine/worktrees) re-discovers the worktree dir.
	// Reconcile the worktree Tiltfile CRs against the desired set on every
	// relevant change; a no-op while the set is unchanged.
	return cc.syncWorktreeTiltfiles(ctx, st)
}

// Register the tiltfile with the APIServer, then dispatch an action to also copy it into the EngineState.
//
// With worktrees discovered (plan §2/§7.2), this is the generalized
// "one Tiltfile CR per execution": the main "(Tiltfile)" CR unchanged, plus
// one "tiltfile:<worktree>" CR per discovered worktree, each re-executing the
// SAME root Tiltfile with its worktree name on the tilt.dev/worktree label.
// The worktree set is observed at the store boundary (EngineState.Worktrees,
// seeded by the discovery/re-execution driver) — discovery and CR creation
// are separate contracts.
func (cc *ConfigsController) maybeCreateInitialTiltfile(ctx context.Context, st store.RStore) error {
	state := st.RLockState()
	desired := state.DesiredTiltfilePath
	ucs := state.UserConfigState
	st.RUnlockState()

	err := cc.ctrlClient.Create(ctx, tiltfile.MainTiltfile(desired, ucs.Args))
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	err = cc.ctrlClient.Create(ctx, uibutton.StopBuildButton(model.MainTiltfileManifestName.String()))
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	cc.isInitialTiltfileCreated = true
	return cc.syncWorktreeTiltfiles(ctx, st)
}

// syncWorktreeTiltfiles reconciles the worktree Tiltfile CRs against the
// desired set (EngineState.Worktrees): creates the "tiltfile:<worktree>" CR
// (+ stop button) for each name this process hasn't synced yet, and deletes
// the CR of each synced name that left the set. Deleting a worktree CR tears
// down its run through existing machinery: the tiltfile reconciler deletes
// the owned objects and drops the run's manifests from engine state, and the
// clone GC prunes the -wt-<name> cluster objects.
//
// Main CR creation stays create-once (isInitialTiltfileCreated): the main
// run is the process's own configuration, not part of the synced set.
func (cc *ConfigsController) syncWorktreeTiltfiles(ctx context.Context, st store.RStore) error {
	state := st.RLockState()
	desired := append([]worktree.Worktree(nil), state.Worktrees...)
	desiredTiltfilePath := state.DesiredTiltfilePath
	ucs := state.UserConfigState
	st.RUnlockState()

	if cc.syncedWorktrees == nil {
		cc.syncedWorktrees = map[string]bool{}
	}

	for _, wt := range desired {
		if cc.syncedWorktrees[wt.Name] {
			continue
		}
		err := cc.ctrlClient.Create(ctx, tiltfile.WorktreeTiltfile(wt.Name, desiredTiltfilePath, ucs.Args))
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		err = cc.ctrlClient.Create(ctx, uibutton.StopBuildButton(tiltfile.TiltfileName(wt.Name)))
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		cc.syncedWorktrees[wt.Name] = true
	}

	desiredNames := make(map[string]bool, len(desired))
	for _, wt := range desired {
		desiredNames[wt.Name] = true
	}
	for name := range cc.syncedWorktrees {
		if desiredNames[name] {
			continue
		}
		crName := tiltfile.TiltfileName(name)
		err := cc.ctrlClient.Delete(ctx, &v1alpha1.Tiltfile{ObjectMeta: metav1.ObjectMeta{Name: crName}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		err = cc.ctrlClient.Delete(ctx, &v1alpha1.UIButton{ObjectMeta: metav1.ObjectMeta{Name: uibutton.StopBuildButtonName(crName)}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		delete(cc.syncedWorktrees, name)
	}

	return nil
}
