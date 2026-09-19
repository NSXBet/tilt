package worktree

import (
	"fmt"

	"github.com/tilt-dev/tilt/pkg/model"
)

// RunResult is one worktree run's contribution to the combined manifest set:
// the manifests its Tiltfile execution produced, before engine-prefix
// rewriting (plan §4.3), tagged with the run's worktree name.
type RunResult struct {
	Name      string
	Manifests []model.Manifest
}

// Combine validates the main run and every worktree run together (plan §3,
// "Validation (loader, cross-Tiltfile pass)") and returns the combined,
// engine-prefixed manifest set the engine runs (plan §4.3).
//
// Rules:
//
//   - Worktree names are unique: two worktrees with the same basename are a
//     load error (§3). Discover already rejects a subdir named "main"; this
//     is the pass-level guarantee over assembled results.
//   - A name defined twice within one run is a double-define error (§3). The
//     per-run loader usually rejects this first (checkResourceConflict,
//     local_resource.go:142); the pass must stand on its own over assembled
//     results.
//   - Each run's manifests become clones `wt:<name>_<name>` (§4.3): under
//     the binary worktree=True flag (§2), unflagged definitions are skipped
//     in worktree runs and flagged ones are per-worktree instances, so a run
//     never redefines a bare name — clones from different worktrees always
//     stay distinct, not a double-define.
//   - A clone whose k8s deploy target has no YAML of its own is the stub the
//     loader instantiated for a worktree=True resource whose YAML producer
//     was skipped in this run (worktree/stub); it inherits main's copy's
//     YAML and image locators (fillStub).
//   - Every dependency must resolve to a manifest defined by the main run or
//     by the depending run's own worktree (§4.3: "same-worktree first, else
//     main-defined"); anything else is a load error — including a dep on a
//     resource defined only by a DIFFERENT worktree.
//   - One-way dep invariant (§12): a main-defined manifest must not depend on
//     a worktree clone — shared resources can never pull one worktree's
//     clone into every other worktree. Shared-flow manifests' clone deps are
//     the designed exception (see applyPrefix).
//
// main == nil (no main-run result available) is tolerated: run manifests are
// still prefixed, but bare deps cannot resolve and error, matching
// applyPrefix's nil-wtOwned contract.
func Combine(main []model.Manifest, runs []RunResult) ([]model.Manifest, error) {
	for i, run := range runs {
		if run.Name == "" {
			return nil, fmt.Errorf("worktree run %d has no name", i)
		}
		for _, prev := range runs[:i] {
			if prev.Name == run.Name {
				return nil, fmt.Errorf("two worktrees named %q; rename one", run.Name)
			}
		}
	}

	// Double-define within a single run.
	seen := make(map[model.ManifestName]struct{}, len(main))
	for _, m := range main {
		if _, ok := seen[m.Name]; ok {
			return nil, fmt.Errorf("manifest %q defined twice in the main run", m.Name)
		}
		seen[m.Name] = struct{}{}
	}
	mainIdx := make(map[model.ManifestName]int, len(main))

	var wtOwned map[model.ManifestName]bool
	if main != nil {
		wtOwned = make(map[model.ManifestName]bool, len(main))
		for i, m := range main {
			wtOwned[m.Name] = true
			mainIdx[m.Name] = i
		}
	}
	rawName := make(map[model.ManifestName]model.ManifestName)
	rawDeps := make(map[model.ManifestName][]model.ManifestName)
	rawRun := make(map[model.ManifestName]string)

	combined := make([]model.Manifest, 0, len(main))
	combined = append(combined, main...)

	defined := make(map[model.ManifestName]bool, len(main))
	for _, m := range main {
		defined[m.Name] = true
	}

	for _, run := range runs {
		runSeen := make(map[model.ManifestName]struct{}, len(run.Manifests))
		for _, m := range run.Manifests {
			if _, ok := runSeen[m.Name]; ok {
				return nil, fmt.Errorf("manifest %q defined twice in worktree %q", m.Name, run.Name)
			}
			runSeen[m.Name] = struct{}{}
		}
		prefixed := applyPrefix(run.Manifests, run.Name, wtOwned)
		for i, m := range prefixed {
			if defined[m.Name] {
				return nil, fmt.Errorf("clone name %q (worktree %q) collides with an existing manifest", m.Name, run.Name)
			}
			// Stub inheritance (worktree/stub): a clone whose k8s deploy
			// target has neither YAML nor an apply command is the stub the
			// loader instantiated for a worktree=True resource whose YAML
			// producer was skipped in this run. It inherits main's copy's
			// YAML and image locators — those only. The clone keeps its own
			// worktree stamp, port forwards, links, and image maps: the
			// inherited YAML references images by name, and this run's own
			// builds inject wherever it matches, so the clone deploys this
			// worktree's builds, not main's.
			if main != nil {
				if raw := run.Manifests[i]; defined[raw.Name] {
					m = fillStub(m, main[mainIdx[raw.Name]])
				}
			}
			combined = append(combined, m)
			defined[m.Name] = true
			raw := run.Manifests[i]
			rawName[m.Name] = raw.Name
			rawDeps[m.Name] = raw.ResourceDependencies
			rawRun[m.Name] = run.Name
		}
	}

	// Every dep resolves to a manifest in the combined set. Errors report
	// author-visible names: dep resolution happened pre-prefix (applyPrefix),
	// so a run manifest's deps are stored pre-rewrite in rawDeps.
	//
	// Skipped when main is nil: deps were deliberately left untouched
	// (applyPrefix's nil-wtOwned contract), so a bare dep that names a
	// main-defined resource has nothing to resolve against yet — the
	// run was kicked conservatively (main failed or produced no result)
	// and the engine, not this pass, reports the unknown dep.
	if main != nil {
		for _, m := range combined {
			deps := m.ResourceDependencies
			reportName := m.Name
			runName := ""
			if raw, ok := rawName[m.Name]; ok {
				reportName = raw
				deps = rawDeps[m.Name]
				runName = rawRun[m.Name]
			}
			for _, dep := range deps {
				// Raw deps resolve same-worktree first (the run's own clone,
				// §4.3), else main-defined (bare). Anything else is a load
				// error, reported with the author-visible dep name.
				if defined[dep] {
					continue
				}
				if runName != "" && defined[ManifestName(runName, string(dep))] {
					continue
				}
				return nil, fmt.Errorf("manifest %q depends on %q, which is not defined by the main run or its worktree", reportName, dep)
			}
		}
	}
	// One-way dep invariant (§12): shared (main-defined) manifests never
	// depend on clones. Runs after the completeness loop, so only resolved
	// clone deps reach this check.
	for _, m := range main {
		for _, dep := range m.ResourceDependencies {
			if isCloneName(dep) {
				return nil, fmt.Errorf("shared manifest %q depends on worktree clone %q; shared resources cannot depend on worktree-owned resources", m.Name, dep)
			}
		}
	}

	return combined, nil
}

// isCloneName reports whether name carries the worktree clone prefix
// (`wt:<worktree>_<name>`, prefix.go).
func isCloneName(name model.ManifestName) bool {
	return IsCloneName(name)
}

// fillStub completes a worktree stub from main's manifest of the same
// bare name (worktree/stub). The stub is a k8s resource the loader
// instantiated for worktree=True when this run skipped the YAML producer
// (helm/k8s_yaml); it deploys nothing until filled here.
//
// Only main's YAML and image locators are inherited — everything else stays
// the clone's: the worktree stamp and port forwards (loader-side), links,
// labels, and this run's image maps. The inherited YAML references images
// by name, and this run's builds inject wherever they match, so the clone
// deploys this worktree's builds; extra image maps only inject where the
// YAML actually references them.
func fillStub(m model.Manifest, main model.Manifest) model.Manifest {
	if !m.IsK8s() || !main.IsK8s() {
		return m
	}
	clone := m.K8sTarget()
	src := main.K8sTarget()
	clone.YAML = src.YAML
	clone.ImageLocators = src.ImageLocators
	return m.WithDeployTarget(clone)
}
