package worktree

import (
	"strings"

	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

const (
	// Prefix for engine-internal names of worktree-run manifests. Clone
	// names join with '_' (not '/'): the engine-internal name doubles as
	// apiserver object name (UIResource/KubernetesApply/etc. are keyed by
	// manifest name), and the tilt-apiserver's BeforeCreate rejects '/' in
	// names (IsPathSegmentName). ':' is legal — the main Tiltfile CR is
	// "(Tiltfile)" and FileWatches are "configs:(Tiltfile)".
	namePrefix = "wt:"

	// Prefix of the SourceTiltfile value for manifests produced by a
	// worktree run.
	sourceTiltfilePrefix = "tiltfile:"
)

// applyPrefix rewrites manifests from a worktree run at the engine boundary
// (plan §4.3): names get the engine-internal prefix `wt:<worktree>/<name>`;
// author-visible names stay bare. Deps resolve same-worktree first (rewritten
// to the clone name), else main-defined (stay bare).
//
// Undefined deps stay bare ONLY when wtOwned is nil (no main-run result
// available to resolve against; the pass does not invent names and the
// loader's dep validation errors on them by construction, plan §11). When
// wtOwned is non-nil, an undefined dep falls OUTSIDE wtOwned and is rewritten
// to `wt:<name>/<missing>` — the loader then errors on the unknown clone.
//
// wtOwned is the set of main-defined (shared) manifest names, from the main
// run's TiltfileLoadResult. A worktree-run manifest whose bare name is in
// wtOwned IS the shared manifest flowing through this run: it keeps its bare
// engine name and records a self-reference dep so the shared resource is
// pinned as a dependency of this run. Deps resolve by the same membership:
// inside wtOwned → shared, stays bare; outside → this worktree's own clone,
// rewritten to the prefixed name.
//
// A nil wtOwned means no main-run result is available to resolve against:
// names are still prefixed, but deps are left untouched rather than guessed.
// Main-run manifests (name == "") pass through unchanged (one-way dep
// invariant, plan §12: shared manifests never depend on clones).
func applyPrefix(manifests []model.Manifest, name string, wtOwned map[model.ManifestName]bool) []model.Manifest {
	if name == "" {
		return manifests
	}

	out := make([]model.Manifest, 0, len(manifests))
	for _, m := range manifests {
		m.SourceTiltfile = model.ManifestName(sourceTiltfilePrefix + name)
		if wtOwned == nil {
			// No main-run result: names are prefixed, deps left untouched.
			m.Name = ManifestName(name, string(m.Name))
			out = append(out, m)
			continue
		}
		// Deps are rewritten on a copy: this pass sits at the engine
		// boundary and must not corrupt the caller's runs (validate.go
		// reads the raw deps afterwards to report author-visible names).
		if len(m.ResourceDependencies) > 0 {
			deps := make([]model.ManifestName, len(m.ResourceDependencies))
			copy(deps, m.ResourceDependencies)
			m.ResourceDependencies = deps
		}
		// Deps: shared (main-defined) → bare; this worktree's own → clone name.
		for i, dep := range m.ResourceDependencies {
			if !wtOwned[dep] {
				m.ResourceDependencies[i] = ManifestName(name, string(dep))
			}
		}
		if wtOwned[m.Name] {
			// The shared manifest itself, flowing through this run: keep the
			// bare engine name and pin it as a dep of this run.
			if !containsName(m.ResourceDependencies, m.Name) {
				m.ResourceDependencies = append(m.ResourceDependencies, m.Name)
			}
		} else {
			m.Name = ManifestName(name, string(m.Name))
			// Stamp the worktree label (plan §4.2): the gateway host-router
			// (isWorktreeManifest), the endpoint-link gateway URLs
			// (withGatewayEndpointLinks) and the TUI/web worktree grouping
			// all resolve a manifest's worktree from this label.
			if m.Labels == nil {
				m.Labels = make(map[string]string, 1)
			}
			m.Labels[v1alpha1.LabelWorktree] = name
		}
		out = append(out, m)
	}
	return out
}

// RewriteRun rewrites one worktree run's manifests at the engine boundary
// (plan §4.3): names become the clone `wt:<worktree>/<name>`, deps resolve
// same-worktree first, else main-defined (shared, stays bare), and each
// manifest records its producing Tiltfile (`tiltfile:<worktree>`).
//
// main is the main run's manifests — nil when no main-run result is
// available (names still prefixed, deps left untouched rather than guessed).
// The input manifests are not mutated; the output never aliases their dep
// slices. Main-run manifests (name == "") pass through unchanged.
func RewriteRun(manifests []model.Manifest, worktree string, main []model.Manifest) []model.Manifest {
	var wtOwned map[model.ManifestName]bool
	if main != nil {
		wtOwned = make(map[model.ManifestName]bool, len(main))
		for _, m := range main {
			wtOwned[m.Name] = true
		}
	}
	return applyPrefix(manifests, worktree, wtOwned)
}

// ManifestName returns the engine-internal name of a worktree-run manifest:
// `wt:<worktree>_<name>`. Path-segment safe (see namePrefix above), so it
// can serve directly as apiserver object name (plan §4.3: engine-internal
// prefix preserves every name-keyed subsystem).
func ManifestName(worktree, name string) model.ManifestName {
	return model.ManifestName(namePrefix + worktree + "_" + name)
}

// IsCloneName reports whether name carries the worktree clone prefix
// (`wt:<worktree>/<name>`). Exported for the engine boundary: the loader
// stamps worktree-run manifests' specs with the worktree name (e.g.
// KubernetesApplySpec.Worktree) and the configs reconciler labels
// engine-side objects, keyed off this prefix.
func IsCloneName(name model.ManifestName) bool {
	return strings.HasPrefix(string(name), namePrefix)
}

func containsName(names []model.ManifestName, name model.ManifestName) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
