package worktree

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"

	"github.com/tilt-dev/tilt/internal/container"
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
	out, err = renameDerivedAll(out)
	if err != nil {
		return BoundaryResult{}, err
	}
	return BoundaryResult{Manifests: out, Defined: defined}, nil
}

// renameDerivedAll re-stamps every name DERIVED from the bare manifest name
// across the run's own definitions — per-manifest KubernetesApply, DockerImage,
// LiveUpdate, CmdImage, local_resource update Cmds, target names, and the
// worktree-scoped ImageMap identity (selectors, image-target deps, and the
// deploy targets' ImageMaps lists).
//
// A no-op for shared manifests (bare names are already path-segment safe and
// rename to themselves) and correct for clones (each derived name gains the
// same clone prefix as its manifest).
func renameDerivedAll(manifests []model.Manifest) ([]model.Manifest, error) {
	for i := range manifests {
		m, err := renameDerived(manifests[i])
		if err != nil {
			return nil, err
		}
		manifests[i] = m
	}
	return manifests, nil
}

// renameDerived rewrites one manifest's derived names against the rewritten
// manifest name m.Name. ImageMap identity (the ref-derived
// ImageMapSpec.Selector) is scoped to the worktree here too — see
// WorktreeImageMapSelector.
func renameDerived(m model.Manifest) (model.Manifest, error) {
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

	isClone := IsCloneName(m.Name)

	for j := range m.ImageTargets {
		iTarget := &m.ImageTargets[j]

		// ImageMap identity is ref-derived (ImageTarget.ImageMapName →
		// ImageTarget.ID → ImageMapSpec.Selector, pkg/model/image_target.go).
		// Two worktrees building the same Dockerfile produce the same bare
		// selector — the same ImageMap name — so without scoping they would
		// share one ImageMap object (and its build-cache lineage) even
		// though their builds retag differently (tk-7gg's -wt-<worktree>
		// tag token). Scope the selector to this worktree: the identity
		// becomes <ref>-wt-<worktree>, mirroring the build tag token, so
		// the worktree-scoped ImageMap is a distinct apiserver object with
		// its own cache lineage, and its default (name-match) selector
		// still matches the worktree's retagged build ref.
		//
		// Only clones get scoped identity. Shared (main-defined) manifests
		// keep the bare selector: they are main's resources — one shared
		// ImageMap by design (plan §3 shared-hack inheritance) — and their
		// bare name is not a clone name.
		if isClone && iTarget.ImageMapSpec.Selector != "" {
			selector, err := WorktreeImageMapSelector(string(m.Name), iTarget.ImageMapSpec.Selector)
			if err != nil {
				return model.Manifest{}, err
			}
			iTarget.ImageMapSpec.Selector = selector
		}
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
		if isClone && len(iTarget.ImageMapDeps()) > 0 {
			// Base-image deps are ImageMap names (ref-derived identities).
			// Scope them like the target's own identity, so a multi-stage
			// build inside the worktree consumes the worktree-scoped base
			// ImageMap (its own cache lineage), never main's. Clone-gated
			// like the selector above: shared (bare-named) redefinitions
			// keep their bare deps — their graphs point at main's shared
			// ImageMaps.
			deps := iTarget.ImageMapDeps()
			scopedDeps := make([]string, 0, len(deps))
			for _, dep := range deps {
				scopedDep, err := WorktreeImageMapSelector(string(m.Name), dep)
				if err != nil {
					return model.Manifest{}, err
				}
				scopedDeps = append(scopedDeps, scopedDep)
			}
			*iTarget = iTarget.WithImageMapDeps(scopedDeps)
		}
		m.ImageTargets[j] = *iTarget
	}

	// Rewrite the deploy target's ImageMaps list to the scoped names.
	//
	// The loader stamps it with the BARE ref-derived ImageMap names
	// (tiltfile_state.go: WithImageDependencies(FilterLiveUpdateOnly(
	// r.imageMapDeps, ...)) for K8s; svc.ImageMapDeps for DockerCompose),
	// and the reconcilers index ImageMap CRs by exactly these names:
	// NamesToObjects(spec.ImageMaps) in shouldDeployOnReconcile /
	// ComputeInputHash / indexer keys — and the engine's target graph
	// resolves K8sTarget.DependencyIDs()/DC DependencyIDs against them
	// (TopologicalSort). Without this rewrite the deploy target would
	// reference bare names that no longer exist (the ImageMap CRs register
	// under scoped names), failing the load in InferLiveUpdateSelectors and
	// stalling deploys on "not built yet". Clone-gated like the blocks
	// above: shared redefinitions keep the bare list.
	if isClone {
		scoped, err := scopeDeployTargetImageMaps(m.DeployTarget, string(m.Name))
		if err != nil {
			return model.Manifest{}, err
		}
		m.DeployTarget = scoped

		// Stamp the K8s apply spec with the worktree name, so apply-time
		// clone stamping (kubernetesapply/worktree_clone.go) fires for this
		// run's entities only. The loader stamps this per run
		// (tiltfile_state.go k8sDeployTarget: Worktree: s.worktree); the
		// boundary pass re-stamps because the spec rides the manifest into
		// toKubernetesApplyObjects (tiltfile/api.go) — the persisted
		// KubernetesApply CR must carry the worktree identity even when the
		// load result came from a loader that predates run-context injection
		// (e.g. the engine's replay of a parked TLR).
		if kt, ok := m.DeployTarget.(model.K8sTarget); ok {
			wt, _ := SplitName(m.Name)
			kt.KubernetesApplySpec.Worktree = string(wt)
			m.DeployTarget = kt
		}
	}

	return m, nil
}

// scopeDeployTargetImageMaps rewrites the ImageMap-name lists a deploy
// target carries (K8sTarget.ImageMaps, DockerComposeTarget.Spec.ImageMaps)
// to their worktree-scoped forms, matching the scoped image-target identity,
// and re-stamps the target's own name with the clone manifest name.
//
// The loader stamps the target name from the BARE manifest name
// (tiltfile_state.go k8sDeployTarget(mn.TargetName(), ...);
// docker_compose.go DockerComposeTarget{Name: TargetName(service.Name)}),
// and that name is the apiserver lookup key — buildcontrol resolves
// kTargetNN/dcTargetNN from DeployTarget.ID() against the KubernetesApply /
// DockerComposeService CRs, which register under the clone name.
func scopeDeployTargetImageMaps(deployTarget model.TargetSpec, cloneName string) (model.TargetSpec, error) {
	targetName := model.TargetName(cloneName)
	switch t := deployTarget.(type) {
	case model.K8sTarget:
		scoped, err := scopeImageMapNames(t.ImageMaps, cloneName)
		if err != nil {
			return nil, err
		}
		t = t.WithImageDependencies(scoped)
		t.Name = targetName
		return t, nil
	case model.DockerComposeTarget:
		scoped, err := scopeImageMapNames(t.Spec.ImageMaps, cloneName)
		if err != nil {
			return nil, err
		}
		t = t.WithImageMapDeps(scoped)
		t.Name = targetName
		return t, nil
	default:
		// LocalTarget and others carry no ImageMaps list.
		return deployTarget, nil
	}
}

func scopeImageMapNames(names []string, cloneName string) ([]string, error) {
	if len(names) == 0 {
		return names, nil
	}
	scoped := make([]string, 0, len(names))
	for _, name := range names {
		s, err := WorktreeImageMapSelector(cloneName, name)
		if err != nil {
			return nil, err
		}
		scoped = append(scoped, s)
	}
	return scoped, nil
}

// derivedObjectName mirrors the loader's derived-name format
// ("<manifest>:<target>", dockerimage.GetName/liveupdate.GetName) for the
// rewritten manifest name.
func derivedObjectName(mn model.ManifestName, targetName model.TargetName) string {
	return string(mn) + ":" + string(targetName)
}

// WorktreeImageMapSelector scopes an image map's ref-derived selector to one
// worktree run: `<selector>-wt-<worktree>`, the same `-wt-<worktree>` token
// the build-side tag rewrite (tk-7gg, container.WorktreeTagSuffix) appends
// to built image tags. Identity stays ref-derived — ImageTarget.ImageMapName
// hashes the selector — so a worktree clone of a shared Dockerfile gets its
// own ImageMap object (own build-cache lineage; CanReuseRef keys on the
// retagged ref) while the default name-match selector still matches the
// worktree's retagged build refs and nothing else's.
//
// The token is escaped through container.WorktreeTagSuffix so the scoped
// selector is always a parseable image reference, identical to the tag
// surface. cloneName is the engine-internal clone manifest name
// (`wt:<worktree>_<name>`); the worktree name is extracted from its
// worktree-scoped segment. Bare names (shared manifests) must never reach
// this — callers scope only their own clones.
func WorktreeImageMapSelector(cloneName, selector string) (string, error) {
	wt, ok := worktreeOfCloneName(model.ManifestName(cloneName))
	if !ok {
		return "", fmt.Errorf("internal error: %q is not a worktree clone name", cloneName)
	}
	token, err := container.WorktreeTagSuffix(wt)
	if err != nil {
		return "", err
	}
	scoped := selector + token
	if _, err := reference.ParseNormalizedNamed(scoped); err != nil {
		return "", fmt.Errorf("scoping image selector %q to worktree %q: %v", selector, wt, err)
	}
	return scoped, nil
}

// worktreeOfCloneName extracts the worktree name from a clone manifest name
// (`wt:<worktree>_<name>` → worktree). Worktree names are directory
// basenames and cannot contain '_' per Discover; the name prefix is `wt:`.
func worktreeOfCloneName(name model.ManifestName) (string, bool) {
	s := string(name)
	if !strings.HasPrefix(s, namePrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(s, namePrefix)
	idx := strings.Index(rest, "_")
	if idx <= 0 {
		return "", false
	}
	return rest[:idx], true
}
