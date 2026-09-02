package worktree

import (
	"fmt"

	"github.com/tilt-dev/tilt/pkg/apis"
	"github.com/tilt-dev/tilt/pkg/model"
)

// The engine-prefix rewrite pass at the engine boundary (plan §4.3, §7.3).
//
// It sits between a worktree run's TiltfileLoadResult and the two consumers
// of engine names — updateOwnedObjects (apiserver object names derive from
// manifest/target names) and ConfigsReloadedAction (engine state is
// name-keyed: trigger queue, log spans, deps, annotations). After the pass,
// a worktree's own resources run under clone names while the author's
// Tiltfile keeps bare names.
//
// Name formats:
//
//   - Engine-internal clone name: `wt:<worktree>_<name>` (ManifestName,
//     prefix.go). Path-segment safe (':' legal — the main Tiltfile CR is
//     "(Tiltfile)"; '/' illegal — tilt-apiserver BeforeCreate), so it serves
//     directly as manifest name AND apiserver object name: engine reducers
//     join UIResource/KubernetesApply objects to manifests by equal name.
//   - Shared manifests defined by the main run keep their bare name: they
//     flow through the worktree run, and the worktree's definition wins
//     over main's (plan §3 "the worktree's definition wins").

// BoundaryResult is the rewritten manifest set of one run, plus the names it
// contributes to the combined engine (clone names for this worktree's own
// manifests, bare names for shared manifests it redefined).
type BoundaryResult struct {
	Manifests []model.Manifest
	Defined   []model.ManifestName
}

// ApplyBoundary rewrites one worktree run's manifests at the engine boundary.
//
// main is the main run's manifest set (bare names), as loaded by the main CR;
// the pass needs it to tell shared manifests from this worktree's own (plan
// §4.3: deps resolve same-worktree first, else main-defined). main may be nil
// when the main CR has not produced a result yet: names are still rewritten,
// but bare deps cannot resolve and error (Combine's nil-main contract) — the
func ApplyBoundary(main []model.Manifest, run RunResult) (BoundaryResult, error) {
	combined, err := Combine(main, []RunResult{run})
	if err != nil {
		return BoundaryResult{}, err
	}

	// Combine returns main's manifests first (with the run's shared
	// redefinitions replaced in place — applyPrefix stamps their
	// SourceTiltfile), then the run's clones in run order. The pass's
	// output is the run's own definitions in run order: clones plus
	// shared redefinitions (bare — the worktree's definition wins, §3).
	clones := combined[len(main):]
	shared := make(map[model.ManifestName]model.Manifest)
	for _, m := range combined[:len(main)] {
		if m.SourceTiltfile == model.ManifestName(sourceTiltfilePrefix+run.Name) {
			shared[m.Name] = m
		}
	}

	out := make([]model.Manifest, 0, len(run.Manifests))
	nextClone := 0
	for _, raw := range run.Manifests {
		if m, ok := shared[raw.Name]; ok {
			out = append(out, m)
			continue
		}
		if nextClone >= len(clones) {
			return BoundaryResult{}, fmt.Errorf("internal error: worktree %q: rewrite lost manifest %q", run.Name, raw.Name)
		}
		out = append(out, clones[nextClone])
		nextClone++
	}

	defined := make([]model.ManifestName, 0, len(out))
	for _, m := range out {
		defined = append(defined, m.Name)
	}
	return BoundaryResult{Manifests: renameDerivedAll(out), Defined: defined}, nil
}

// other in the apiserver: per-manifest KubernetesApply, DockerImage,
// LiveUpdate, CmdImage, local_resource update Cmds, and target names.
//
// A no-op for shared manifests (bare names are already path-segment safe and
// rename to themselves) and correct for clones (each derived name gains the
// same clone prefix as its manifest).
func renameDerivedAll(manifests []model.Manifest) []model.Manifest {
	for i := range manifests {
		manifests[i] = renameDerived(manifests[i])
	}
	return manifests
}

// rewritten) manifest name m.Name.
func renameDerived(m model.Manifest) model.Manifest {
	// Copy before rewrite: the manifest struct copies done upstream
	// (applyPrefix) share the ImageTargets backing array with the caller's
	// input, and the reconciler re-reads bare names after the pass.
	targets := make([]model.ImageTarget, len(m.ImageTargets))
	copy(targets, m.ImageTargets)
	m.ImageTargets = targets

	// Deploy target names: WithDeployTarget stamps K8s/DC target names from
	// the manifest name, so those already carry the clone name. LocalTarget
	// holds its name independently (translateLocal sets it from the bare
	// resource name), so rename it here.
	if lt, ok := m.DeployTarget.(model.LocalTarget); ok {
		lt.Name = model.TargetName(m.Name)
		m.DeployTarget = lt
	}

	for j := range m.ImageTargets {
		iTarget := &m.ImageTargets[j]

		// The image target's ID name derives from the image REF, not the
		// manifest — it is shared by design across worktrees (same
		// Dockerfile → same ref; per-worktree ImageMap identity is task
		// tk-1zq's tag-suffix rewrite, not this pass). But the derived
		// object names embedded at load time (DockerImageName,
		// LiveUpdateName, CmdImageName — dockerimage.GetName(mn, id) et al,
		// tiltfile_state.go imgTargetsForDepsHelper) were stamped from the
		// BARE manifest name. Re-stamp them with the clone name so main and
		// worktree builds never share DockerImage/LiveUpdate/CmdImage
		// objects.
		if iTarget.LiveUpdateName != "" {
			iTarget.LiveUpdateName = apis.SanitizeName(
				derivedObjectName(m.Name, iTarget.ID().Name))
		}
		if iTarget.DockerImageName != "" {
			iTarget.DockerImageName = apis.SanitizeName(
				derivedObjectName(m.Name, iTarget.ID().Name))
		}
		if iTarget.CmdImageName != "" {
			iTarget.CmdImageName = apis.SanitizeName(
				derivedObjectName(m.Name, iTarget.ID().Name))
		}
		m.ImageTargets[j] = *iTarget
	}

	return m
}

// derivedObjectName mirrors the loader's derived-name format
// ("<manifest>:<target>", dockerimage.GetName/liveupdate.GetName) for the
// rewritten manifest name.
func derivedObjectName(mn model.ManifestName, targetName model.TargetName) string {
	return string(mn) + ":" + string(targetName)
}
