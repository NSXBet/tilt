package worktree

import (
	"testing"

	"github.com/distribution/reference"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/container"
	"github.com/tilt-dev/tilt/pkg/apis"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// tk-1zq acceptance: per-worktree ImageMap identity.
//
// ImageMap identity is ref-derived (ImageTarget.ImageMapName ←
// ImageTarget.ID ← ImageMapSpec.Selector, pkg/model/image_target.go). Two
// worktrees building the same Dockerfile produce the same bare selector —
// the same ImageMap name — so the boundary pass scopes each clone's
// selectors with the -wt-<worktree> token (the same token tk-7gg's tag
// rewrite uses). Each worktree then builds and registers its refs under its
// own worktree-scoped ImageMap: distinct apiserver objects, distinct
// build-cache lineage, no cross-worktree image reuse, while the default
// name-match selector still matches the worktree's own build refs (the
// build RefSet derives from the same scoped selector via
// ImageTarget.Refs → container.RefSetFromImageMap).

// Scoped selector: <ref>-wt-<worktree>, escaping the worktree name exactly
// like the tag surface (container.WorktreeTagSuffix).
func TestWorktreeImageMapSelector(t *testing.T) {
	got, err := WorktreeImageMapSelector("wt:feat-auth_api", "gcr.io/foo/api")
	require.NoError(t, err)
	require.Equal(t, "gcr.io/foo/api-wt-feat-auth", got)

	_, err = reference.ParseNormalizedNamed(got)
	require.NoError(t, err, "scoped selector must stay a parseable image reference")
}

// Distinct worktrees → distinct selectors → distinct ImageMap names.
func TestWorktreeImageMapSelector_DistinctPerWorktree(t *testing.T) {
	gotA, err := WorktreeImageMapSelector("wt:feat-auth_api", "sancho")
	require.NoError(t, err)
	gotB, err := WorktreeImageMapSelector("wt:fix-ui_api", "sancho")
	require.NoError(t, err)

	require.NotEqual(t, gotA, gotB)
	require.Equal(t, "sancho-wt-feat-auth", gotA)
	require.Equal(t, "sancho-wt-fix-ui", gotB)
}

// The scoped selector must name-match the worktree's own build refs — which
// derive from the same scoped selector — but never main's or another
// worktree's.
func TestWorktreeImageMapSelector_MatchesOwnBuildRefs(t *testing.T) {
	scoped, err := WorktreeImageMapSelector("wt:feat-auth_api", "gcr.io/foo/api")
	require.NoError(t, err)
	sel := container.MustParseSelector(scoped)

	// The worktree's own build ref: repo carries the scoped name (the
	// RefSet is derived from the scoped selector), tag carries the digest.
	own, err := container.ParseNamed("gcr.io/foo/api-wt-feat-auth:tilt-d34db33f")
	require.NoError(t, err)
	require.True(t, sel.Matches(own), "scoped selector must match the worktree's build ref")

	// Main's build ref and another worktree's build ref carry different
	// repo names — the scoped selector matches neither.
	main, err := container.ParseNamed("gcr.io/foo/api:tilt-d34db33f")
	require.NoError(t, err)
	require.False(t, sel.Matches(main), "scoped selector must not match main's build ref")
	other, err := container.ParseNamed("gcr.io/foo/api-wt-fix-ui:tilt-d34db33f")
	require.NoError(t, err)
	require.False(t, sel.Matches(other), "scoped selector must not match another worktree's build ref")
}

// The scoped selector drives the full build-ref chain: ImageTarget.Refs →
// RefSetFromImageMap → AddTagSuffix yields refs in the scoped repo with the
// digest tag — distinct from main's build of the same Dockerfile.
func TestWorktreeImageMapSelector_DrivesBuildRefs(t *testing.T) {
	scoped, err := WorktreeImageMapSelector("wt:feat-auth_api", "gcr.io/foo/api")
	require.NoError(t, err)

	refs, err := container.RefSetFromImageMap(v1alpha1.ImageMapSpec{Selector: scoped}, nil)
	require.NoError(t, err)
	tagged, err := refs.AddTagSuffix("tilt-d34db33f")
	require.NoError(t, err)
	require.Equal(t, "gcr.io/foo/api-wt-feat-auth:tilt-d34db33f", tagged.LocalRef.String())

	// Main's build of the same Dockerfile lands in the bare repo.
	refsMain, err := container.RefSetFromImageMap(v1alpha1.ImageMapSpec{Selector: "gcr.io/foo/api"}, nil)
	require.NoError(t, err)
	taggedMain, err := refsMain.AddTagSuffix("tilt-d34db33f")
	require.NoError(t, err)
	require.NotEqual(t, tagged.LocalRef.String(), taggedMain.LocalRef.String(),
		"same Dockerfile built in main vs worktree must yield distinct refs")
}

// A clone name that does not carry the wt: prefix is an internal error,
// never silently rewritten.
func TestWorktreeImageMapSelector_NonCloneNameErrors(t *testing.T) {
	_, err := WorktreeImageMapSelector("postgres", "sancho")
	require.ErrorContains(t, err, "not a worktree clone name")
}

// End-to-end through the boundary pass: a worktree clone's image targets
// carry worktree-scoped ImageMap identity; shared manifests keep the bare
// selector.
func TestApplyBoundary_ImageMapIdentityScoped(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name: "feat-auth",
		Manifests: []model.Manifest{
			boundaryMd("api"),
		},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_api"), m.Name)
	iTarget := m.ImageTargets[0]
	require.Equal(t, "registry.example.com/api-wt-feat-auth", iTarget.ImageMapSpec.Selector,
		"clone's ImageMap selector must be scoped to the worktree")
	require.Equal(t, "registry.example.com_api-wt-feat-auth", iTarget.ImageMapName(),
		"ImageMap identity must be worktree-scoped")
}

// A shared (main-defined) name flowing through the run becomes that
// worktree's clone, so its ImageMap identity scopes to the worktree like any
// clone's — locals included (TestApplyBoundary_K8sSharedNameBecomesClone).
func TestApplyBoundary_SharedImageMapClonesWithScopedIdentity(t *testing.T) {
	shared := localMd("postgres")
	iTarget := model.ImageTarget{}
	iTarget.ImageMapSpec.Selector = "registry.example.com/postgres"
	iTarget.LiveUpdateName = apis.SanitizeName("postgres:" + iTarget.ID().Name.String())
	iTarget.DockerImageName = apis.SanitizeName("postgres:" + iTarget.ID().Name.String())
	shared.ImageTargets = []model.ImageTarget{iTarget}

	main := []model.Manifest{shared}
	out, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{shared},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), m.Name)
	require.Equal(t, "registry.example.com/postgres-wt-feat-auth", m.ImageTargets[0].ImageMapSpec.Selector,
		"clone's ImageMap selector is scoped to the worktree")
}

// Two worktrees building the same Dockerfile: distinct ImageMap identities
// from the same author-visible selector.
func TestApplyBoundary_SameDockerfileTwoWorktreesDistinctIdentity(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	outA, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{boundaryMd("api")},
	})
	require.NoError(t, err)
	outB, err := ApplyBoundary(main, RunResult{
		Name:      "fix-ui",
		Manifests: []model.Manifest{boundaryMd("api")},
	})
	require.NoError(t, err)

	imA := outA.Manifests[0].ImageTargets[0]
	imB := outB.Manifests[0].ImageTargets[0]
	require.NotEqual(t, imA.ImageMapName(), imB.ImageMapName(),
		"two worktrees building the same Dockerfile must get distinct ImageMaps")
	require.Equal(t, "registry.example.com_api-wt-feat-auth", imA.ImageMapName())
	require.Equal(t, "registry.example.com_api-wt-fix-ui", imB.ImageMapName())
}

// Multi-stage builds: a clone's base-image ImageMap dep must point at the
// worktree-scoped dep ImageMap, keeping the dependency graph inside the
// worktree.
func TestApplyBoundary_ImageMapDepsRewritten(t *testing.T) {
	m := boundaryMd("api")
	iTarget := m.ImageTargets[0]
	iTarget = iTarget.WithDockerImage(v1alpha1.DockerImageSpec{Context: "."}).
		WithImageMapDeps([]string{"registry.example.com/base"})
	m.ImageTargets = []model.ImageTarget{iTarget}

	out, err := ApplyBoundary(nil, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{m},
	})
	require.NoError(t, err)

	got := out.Manifests[0].ImageTargets[0]
	require.Equal(t, "registry.example.com/base-wt-feat-auth", got.ImageMapDeps()[0],
		"clone's ImageMap dep must be scoped to the worktree alongside its own identity")
}

// The input manifests are never mutated (rawDeps / bare-name re-read
// contract from the reconciler).
func TestApplyBoundary_SelectorInputNotMutated(t *testing.T) {
	m := boundaryMd("api")
	in := []model.Manifest{m}
	_, err := ApplyBoundary(nil, RunResult{Name: "feat-auth", Manifests: in})
	require.NoError(t, err)

	require.Equal(t, "registry.example.com/api", in[0].ImageTargets[0].ImageMapSpec.Selector,
		"input ImageMap selector must stay bare")
}

// k8sMd builds the full clone shape a worktree run produces for
// docker_build + k8s_resource, mirroring the loader exactly:
//   - the K8s deploy target's name is the BARE manifest name
//     (tiltfile_state.go k8sDeployTarget(mn.TargetName(), ...)),
//   - its ImageMaps list carries the load-time BARE ref-derived ImageMap
//     name — SANITIZED, apis.SanitizeName(ref), exactly what
//     builder.ImageMapName() feeds r.imageMapDeps (WithImageDependencies).
func k8sMd(name string, bareImageMapNames ...string) model.Manifest {
	m := boundaryMd(name)
	iTarget := m.ImageTargets[0]
	iTarget = iTarget.WithDockerImage(v1alpha1.DockerImageSpec{Context: "."})
	m.ImageTargets = []model.ImageTarget{iTarget}
	kTarget := model.K8sTarget{Name: model.TargetName(name)}.
		WithImageDependencies(bareImageMapNames)
	m = m.WithDeployTarget(kTarget)
	return m
}

// dcMd builds the docker-compose clone shape, mirroring the loader
// (docker_compose.go: DockerComposeTarget{Name: TargetName(service.Name)},
// WithImageMapDeps(FilterLiveUpdateOnly(svc.ImageMapDeps, ...))).
func dcMd(name string, bareImageMapNames ...string) model.Manifest {
	m := boundaryMd(name)
	iTarget := m.ImageTargets[0]
	iTarget = iTarget.WithDockerImage(v1alpha1.DockerImageSpec{Context: "."})
	m.ImageTargets = []model.ImageTarget{iTarget}
	dcTarget := model.DockerComposeTarget{Name: model.TargetName(name)}.
		WithImageMapDeps(bareImageMapNames)
	m = m.WithDeployTarget(dcTarget)
	return m
}

// bareImageMapName mirrors the load-time stamping: the ImageMap name is the
// SANITIZED selector (apis.SanitizeName in ImageTarget.ID).
func bareImageMapName(ref string) string {
	return apis.SanitizeName(ref)
}

// BREAK A regression (validator repro TestReproCloneK8sImageMapsStale): the
// pass must rewrite the clone deploy target's ImageMaps list to the scoped
// names, or the K8sTarget.DependencyIDs() keep referencing bare names that
// no longer exist (ImageMap CRs register scoped) and the production
// sequence hard-fails in TopologicalSort.
func TestApplyBoundary_CloneK8sDeployImageMapsScoped(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{k8sMd("api", bareImageMapName("registry.example.com/api"))},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_api"), m.Name)
	kTarget := m.K8sTarget()
	require.Equal(t, []string{"registry.example.com_api-wt-feat-auth"}, kTarget.ImageMaps,
		"clone K8s deploy target's ImageMaps list must be scoped to the worktree")

	// The exact production failure shape: InferLiveUpdateSelectors (updateOwnedObjects,
	// runs after ApplyBoundary in handleLoaded) builds the target graph from
	// DependencyIDs — the rewritten list must resolve.
	require.NoError(t, m.InferLiveUpdateSelectors())
}

// BREAK A regression, DC path: DockerComposeTarget.Spec.ImageMaps scope too.
func TestApplyBoundary_CloneDCDeployImageMapsScoped(t *testing.T) {
	out, err := ApplyBoundary(nil, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{dcMd("api", bareImageMapName("registry.example.com/api"))},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	dcTarget := m.DockerComposeTarget()
	require.Equal(t, []string{"registry.example.com_api-wt-feat-auth"}, dcTarget.Spec.ImageMaps,
		"clone DC deploy target's ImageMaps list must be scoped to the worktree")
	require.NoError(t, m.InferLiveUpdateSelectors())
}

// BREAK B regression (validator repro TestReproSharedRedefinitionDepsError):
// a multi-stage redefinition of a main-defined name becomes a clone, and its
// dep graph re-scopes with it — the deps block is clone-gated like the
// selector block.
func TestApplyBoundary_SharedRedefinitionClonesWithScopedDeps(t *testing.T) {
	// The loader (imgTargetsForDepsHelper) puts a multi-stage resource's base
	// image target AND the dependent target in the SAME manifest — the graph
	// is per-manifest, so the base target must be present for
	// InferLiveUpdateSelectors to resolve. As a clone, both targets scope to
	// the worktree (own cache lineage), and the dependent's ImageMap dep
	// points at the scoped base, never main's.
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name: "feat-auth",
		Manifests: []model.Manifest{
			multiStageMd("postgres", "registry.example.com/base"),
		},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), m.Name)
	require.Equal(t, "registry.example.com/base-wt-feat-auth", m.ImageTargets[0].ImageMapSpec.Selector,
		"base target of the multi-stage clone scopes to the worktree")
	require.Equal(t, "registry.example.com/postgres-wt-feat-auth", m.ImageTargets[1].ImageMapSpec.Selector,
		"dependent target of the multi-stage clone scopes to the worktree")
	require.Equal(t, []string{"registry.example.com_base-wt-feat-auth"}, m.ImageTargets[1].ImageMapDeps(),
		"multi-stage clone's ImageMap deps point at the scoped base")
}

// multiStageMd is the multi-stage shape the loader produces for a manifest
// whose Dockerfile FROMs another built image: base target first, then the
// dependent target with the ImageMap dep.
func multiStageMd(name string, baseRef string) model.Manifest {
	base := model.ImageTarget{}
	base.ImageMapSpec.Selector = baseRef
	base = base.WithDockerImage(v1alpha1.DockerImageSpec{Context: "."})
	dep := model.ImageTarget{}
	dep.ImageMapSpec.Selector = "registry.example.com/" + name
	dep = dep.WithDockerImage(v1alpha1.DockerImageSpec{Context: "."}).
		WithImageMapDeps([]string{apis.SanitizeName(baseRef)})
	m := md(name)
	m.ImageTargets = []model.ImageTarget{base, dep}
	return m
}

// tk-1zq integration criterion (plan §4.4): the full production sequence —
// boundary rewrite for two worktrees building the SAME Dockerfile, then the
// updateOwnedObjects pass (InferLiveUpdateSelectors) — yields distinct
// ImageMaps per worktree, distinct build tags, and leaves main's identity
// untouched.
func TestIntegration_TwoWorktreesSameDockerfileDistinctIdentity(t *testing.T) {
	// Both worktrees re-execute the root Tiltfile: each defines "api" with
	// the same docker_build ref, and a k8s "postgres" with the same shape.
	// Both are k8s manifests, so under the additive-siblings contract
	// (plan §4.1/§4.2) BOTH become each worktree's own clones; main's
	// definitions stay untouched.
	main := []model.Manifest{boundaryMd("postgres")}
	outA, err := ApplyBoundary(main, RunResult{
		Name: "feat-auth",
		Manifests: []model.Manifest{
			k8sMd("api", bareImageMapName("registry.example.com/api")),
			boundaryMd("postgres"),
		},
	})
	require.NoError(t, err)
	outB, err := ApplyBoundary(main, RunResult{
		Name: "fix-ui",
		Manifests: []model.Manifest{
			k8sMd("api", bareImageMapName("registry.example.com/api")),
			boundaryMd("postgres"),
		},
	})
	require.NoError(t, err)

	// Post-pass, post-updateOwnedObjects: the exact sequence of handleLoaded.
	manifests := append(append([]model.Manifest{main[0]}, outA.Manifests...), outB.Manifests...)
	for _, m := range manifests {
		require.NoError(t, m.InferLiveUpdateSelectors(), "manifest %s must survive the production sequence", m.Name)
	}

	// Distinct ImageMap identities per worktree.
	apiA := outA.Manifests[0].ImageTargets[0]
	apiB := outB.Manifests[0].ImageTargets[0]
	require.Equal(t, "registry.example.com_api-wt-feat-auth", apiA.ImageMapName())
	require.Equal(t, "registry.example.com_api-wt-fix-ui", apiB.ImageMapName())
	require.Equal(t, "registry.example.com/api-wt-feat-auth", apiA.ImageMapSpec.Selector)
	require.Equal(t, "registry.example.com/api-wt-fix-ui", apiB.ImageMapSpec.Selector)

	// Distinct build tags: the RefSet derives from each scoped selector, so
	// the same digest lands in different repos per worktree.
	cluster := &v1alpha1.Cluster{}
	refsA, err := apiA.Refs(cluster)
	require.NoError(t, err)
	taggedA, err := refsA.AddTagSuffix("tilt-d34db33f")
	require.NoError(t, err)
	refsB, err := apiB.Refs(cluster)
	require.NoError(t, err)
	taggedB, err := refsB.AddTagSuffix("tilt-d34db33f")
	require.NoError(t, err)
	require.Equal(t, "registry.example.com/api-wt-feat-auth:tilt-d34db33f", taggedA.LocalRef.String())
	require.Equal(t, "registry.example.com/api-wt-fix-ui:tilt-d34db33f", taggedB.LocalRef.String())
	require.NotEqual(t, taggedA.LocalRef.String(), taggedB.LocalRef.String(),
		"two worktrees building the same Dockerfile must produce distinct tags")

	// The worktree runs' k8s postgres redefinition is ALSO each worktree's
	// own clone (additive siblings), while main's postgres identity is
	// untouched by both runs.
	sharedA := outA.Manifests[1]
	sharedB := outB.Manifests[1]
	require.Equal(t, model.ManifestName("wt:feat-auth_postgres"), sharedA.Name)
	require.Equal(t, model.ManifestName("wt:fix-ui_postgres"), sharedB.Name)
	require.Equal(t, "registry.example.com/postgres", main[0].ImageTargets[0].ImageMapSpec.Selector,
		"main's postgres identity stays bare")
	require.Equal(t, "registry.example.com/postgres-wt-feat-auth", sharedA.ImageTargets[0].ImageMapSpec.Selector)
	require.Equal(t, "registry.example.com/postgres-wt-fix-ui", sharedB.ImageTargets[0].ImageMapSpec.Selector)

	// Clone deploy targets reference their own scoped ImageMap names.
	require.Equal(t, []string{"registry.example.com_api-wt-feat-auth"}, outA.Manifests[0].K8sTarget().ImageMaps)
	require.Equal(t, []string{"registry.example.com_api-wt-fix-ui"}, outB.Manifests[0].K8sTarget().ImageMaps)
}
