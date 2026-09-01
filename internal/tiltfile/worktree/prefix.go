package worktree

import (
	"github.com/tilt-dev/tilt/pkg/model"
)

const (
	// Prefix for engine-internal names of worktree-run manifests.
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
		}
		out = append(out, m)
	}
	return out
}

// ManifestName returns the engine-internal name of a worktree-run manifest:
// `wt:<worktree>/<name>`.
func ManifestName(worktree, name string) model.ManifestName {
	return model.ManifestName(namePrefix + worktree + "/" + name)
}

func containsName(names []model.ManifestName, name model.ManifestName) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
