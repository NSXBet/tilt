package tiltfile

import (
	"github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/controllers/apiset"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Multi-tiltfile support (plan §7.3): every Tiltfile CR is one engine
// execution — the main "(Tiltfile)" and one `tiltfile:<worktree>` re-running
// the shared root Tiltfile per discovered worktree (CR shape: apis/tiltfile).
//
// Each run loads independently; what glues them into one engine is here:
//
//   - Per-run engine-prefix rewrite (§4.3): a worktree run's manifests get
//     engine-internal names `wt:<worktree>/<name>` and run in the worktree's
//     cwd (re-rooted at execution). Main-run manifests stay bare.
//   - Hold-until-main: worktree runs that finish before the main run cannot
//     resolve shared names against an empty engine, so their results are
//     parked and replayed once the main run has loaded.
//
// Every other subsystem is already per-Tiltfile: owned objects are scoped by
// controller reference (getExistingAPIObjects), UIResources/disable buttons
// name themselves after the run's manifests, and the session object exists
// only for the main CR (toSessionObjects). KubernetesApply clones ride the
// CR's worktree identity: the clone stamping pass is keyed on
// KubernetesApplySpec.Worktree, set per manifest below.

// worktreeNameOf returns the worktree a Tiltfile CR executes for ("" for the
// main run), by the tilt.dev/worktree label.
func worktreeNameOf(tf *v1alpha1.Tiltfile) string {
	if tf == nil {
		return ""
	}
	return tf.Labels[v1alpha1.LabelWorktree]
}

// prefixRun rewrites one run's manifests for the engine boundary and stamps
// each manifest with its producing Tiltfile name (SourceTiltfile feeds the
// "resource defined in two tiltfiles" check and per-worktree UI grouping).
//
// For a worktree run, `main` is the main run's latest manifests — nil when
func prefixRun(manifests []model.Manifest, tf *v1alpha1.Tiltfile, main []model.Manifest) []model.Manifest {
	wtName := worktreeNameOf(tf)
	if wtName == "" {
		// Main run: SourceTiltfile is stamped by the engine reducer from the
		// action name; names stay bare. One-way dep invariant (§12) is
		// enforced against the combined set by worktree validation at the
		// engine seam, not here.
		return manifests
	}
	out := worktree.RewriteRun(manifests, wtName, main)
	source := model.ManifestName(tiltfile.TiltfileName(wtName))
	for i := range out {
		// Stamp the worktree label (plan §4.2): the gateway host-router
		// (isWorktreeManifest), the endpoint-link gateway URLs
		// (withGatewayEndpointLinks) and the TUI/web worktree grouping
		// all resolve a manifest's worktree from this label. Rewriting
		// runs at the controller seam keeps prefix.go engine-only; shared
		// manifests (bare names, un-renamed) are skipped — their label
		// stays empty (they belong to no single worktree).
		if out[i].Labels == nil {
			out[i].Labels = make(map[string]string, 1)
		}
		out[i].Labels[v1alpha1.LabelWorktree] = wtName
		out[i].SourceTiltfile = source
		// Stamp the K8s target's apply spec with the worktree name, so
		// apply-time clone stamping (kubernetesapply/worktree_clone.go) fires
		// for this run's entities only. The spec rides the manifest into
		// toKubernetesApplyObjects (api.go), so the persisted KubernetesApply
		// CRs carry it.
		if out[i].IsK8s() {
			kTarget := out[i].K8sTarget()
			kTarget.KubernetesApplySpec.Worktree = wtName
			out[i] = out[i].WithDeployTarget(kTarget)
		}
	}
	return out
}

// stampWorktreeK8sApply sets KubernetesApplySpec.Worktree on every
// KubernetesApply object of a worktree run's builtin-registered object set.
// Main-run objects are untouched.
func stampWorktreeK8sApply(objects apiset.TypedObjectSet, wtName string) {
	if wtName == "" {
		return
	}
	for _, obj := range objects {
		ka := obj.(*v1alpha1.KubernetesApply)
		ka.Spec.Worktree = wtName
	}
}

// k8sApplyObjectsOf extracts the KubernetesApply set from a load result's
// object set, for the stamping pass above.
func k8sApplyObjectsOf(objectSet apiset.ObjectSet) apiset.TypedObjectSet {
	return objectSet.GetSetForType(&v1alpha1.KubernetesApply{})
}
