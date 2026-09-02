package worktree

import (
	"testing"

	"github.com/distribution/reference"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/container"
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

// Shared manifests flowing through the run keep the bare selector: they are
// main's resources, not clones.
func TestApplyBoundary_SharedImageMapStaysBare(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{boundaryMd("postgres")},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("postgres"), m.Name)
	require.Equal(t, "registry.example.com/postgres", m.ImageTargets[0].ImageMapSpec.Selector,
		"shared manifest's ImageMap selector stays bare")
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
