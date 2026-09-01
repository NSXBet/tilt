package build

import (
	"testing"

	typesimage "github.com/moby/moby/api/types/image"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/container"
	"github.com/tilt-dev/tilt/internal/docker"
	"github.com/tilt-dev/tilt/internal/testutils"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Phase 2 acceptance: build tag rewrite (plan §4.4, §11).
//
// Expected seam: the worktree token rides RefSet itself (RefSet.MustWithWorktree,
// mirroring MustWithRegistry), so AddTagSuffix appends `-wt-<worktree>` to the
// suffix it already composes. That single choke point gives DockerBuilder.TagRefs
// and CustomBuilder (normal + OutputTag paths) worktree parity by construction,
// and CanReuseRef keys on the retagged ref so a worktree never reuses main's
// stale image (and vice versa).

var testDigest = digest.Digest("sha256:cc5f4c463f81c55183d8d737ba2f0d30b3e6f3670dbe2da68f0aac168e93fbb1")

func TestAddTagSuffix_WorktreeTokenRoundTrip(t *testing.T) {
	digTag, err := digestAsTag(testDigest)
	require.NoError(t, err)
	require.Equal(t, "tilt-cc5f4c463f81c551", digTag)

	// Worktree build: worktree token appended AFTER the digest token.
	wtRefs := refSetFromString("gcr.io/foo/api").MustWithWorktree("feat-auth")
	tagged, err := wtRefs.AddTagSuffix(digTag)
	require.NoError(t, err)

	want := container.MustParseNamedTagged("gcr.io/foo/api:tilt-cc5f4c463f81c551-wt-feat-auth")
	assert.Equal(t, want.String(), tagged.LocalRef.String())
	assert.Equal(t, want.String(), tagged.ClusterRef.String())

	// Round-trip: the tagged ref parses back to the same name+tag.
	parsed, err := container.ParseNamedTagged(tagged.LocalRef.String())
	require.NoError(t, err)
	assert.Equal(t, want.Tag(), parsed.Tag())

	// Same repo, distinct tag per worktree (distinct ImageMap / cache lineage).
	otherWt, err := refSetFromString("gcr.io/foo/api").MustWithWorktree("fix-ui").AddTagSuffix(digTag)
	require.NoError(t, err)
	assert.NotEqual(t, tagged.LocalRef.String(), otherWt.LocalRef.String())

	// Main run (no worktree context): tag identical to today's behavior.
	mainTagged, err := refSetFromString("gcr.io/foo/api").AddTagSuffix(digTag)
	require.NoError(t, err)
	assert.Equal(t, "gcr.io/foo/api:"+digTag, mainTagged.LocalRef.String())
	assert.NotEqual(t, tagged.LocalRef.String(), mainTagged.LocalRef.String())
}

// Worktree names come from directory basenames and may contain characters that
// are invalid in docker tags. The composed suffix must escape so AddTagSuffix
// succeeds and the ref round-trips (plan §11: "escapes correctly").
func TestAddTagSuffix_WorktreeTokenEscapes(t *testing.T) {
	tagged, err := refSetFromString("gcr.io/foo/api").MustWithWorktree("feat+x auth").AddTagSuffix("tilt-d34db33f")
	require.NoError(t, err, "worktree token must be escaped into a valid docker tag")

	_, err = container.ParseNamedTagged(tagged.LocalRef.String())
	require.NoError(t, err)

	// Token stays appended after the base suffix.
	assert.Contains(t, tagged.LocalRef.Tag(), "tilt-d34db33f-wt-")
}

// Escape edges beyond the composite case above: non-ASCII characters (legal in
// directory basenames, illegal in docker tags) and a leading dot (illegal first
// char for a docker tag). The token must be sanitized into a fully valid tag,
// not just have its spaces replaced.
func TestAddTagSuffix_WorktreeTokenEscapeEdges(t *testing.T) {
	for _, name := range []string{"café-branch", ".tmp-main"} {
		tagged, err := refSetFromString("gcr.io/foo/api").MustWithWorktree(name).AddTagSuffix("tilt-d34db33f")
		require.NoErrorf(t, err, "worktree token %q must be escaped into a valid docker tag", name)

		_, err = container.ParseNamedTagged(tagged.LocalRef.String())
		require.NoErrorf(t, err, "escaped tag %q must round-trip", tagged.LocalRef.String())

		assert.Containsf(t, tagged.LocalRef.Tag(), "tilt-d34db33f-wt-", "token stays appended after base suffix (name %q)", name)
	}
}

// DockerBuilder.TagRefs (the digest-tag choke point for docker builds) must
// carry the worktree token when the RefSet has worktree context.
func TestDockerBuilderTagRefs_Worktree(t *testing.T) {
	ctx, _, _ := testutils.CtxAndAnalyticsForTest()
	d := NewDockerBuilder(docker.NewFakeClient(), nil)

	tagged, err := d.TagRefs(ctx, refSetFromString("gcr.io/foo/api").MustWithWorktree("feat-auth"), testDigest)
	require.NoError(t, err)
	assert.Equal(t, "gcr.io/foo/api:tilt-cc5f4c463f81c551-wt-feat-auth", tagged.LocalRef.String())
}

// custom_build parity (plan §4.4): the OutputTag suffix and the final digest
// re-tag both carry the worktree token, same as docker builds.
func TestCustomBuild_WorktreeOutputTagParity(t *testing.T) {
	f := newFakeCustomBuildFixture(t)

	sha := digest.Digest("sha256:11cd0eb38bc3ceb958ffb2f9bd70be3fb317ce7d255c8a4c3f4af30e298aa1aab")
	// Expected intermediate ref: OutputTag with the worktree token.
	f.dCli.Images["gcr.io/foo/bar:my-tag-wt-feat-auth"] = typesimage.InspectResponse{ID: string(sha)}

	cb := f.customBuild("exit 0")
	cb.CmdImageSpec.OutputTag = "my-tag"
	refs, err := f.Build(refSetFromString("gcr.io/foo/bar").MustWithWorktree("feat-auth"), cb, nil)
	require.NoError(t, err)

	assert.Equal(f.t,
		container.MustParseNamed("gcr.io/foo/bar:tilt-11cd0eb38bc3ceb9-wt-feat-auth"),
		refs.LocalRef)
}

// custom_build normal mode (no output_tag) parity: the expected tag handed to
// the build script (EXPECTED_TAG / EXPECTED_REF) and the final digest re-tag
// both carry the worktree token. Covers the CustomBuilder.Build tag-composition
// call site that every custom_build without output_tag takes.
func TestCustomBuild_WorktreeNormalModeParity(t *testing.T) {
	f := newFakeCustomBuildFixture(t)

	sha := digest.Digest("sha256:11cd0eb38bc3ceb958ffb2f9bd70be3fb317ce7d255c8a4c3f4af30e298aa1aab")
	// Expected intermediate ref: clock tag with the worktree token. Seeding only
	// this ref means Build succeeds only if EXPECTED_TAG carries the token.
	f.dCli.Images["gcr.io/foo/bar:tilt-build-1551202573-wt-feat-auth"] = typesimage.InspectResponse{ID: string(sha)}

	cb := f.customBuild("exit 0")
	refs, err := f.Build(refSetFromString("gcr.io/foo/bar").MustWithWorktree("feat-auth"), cb, nil)
	require.NoError(t, err)

	assert.Equal(f.t,
		container.MustParseNamed("gcr.io/foo/bar:tilt-11cd0eb38bc3ceb9-wt-feat-auth"),
		refs.LocalRef)
}

// Reuse checks key on the retagged ref (plan §4.4): a worktree-tagged ref is
// a distinct image — never reuses main's build, even when main's tag exists.
func TestCanReuseRef_WorktreeTagDistinctFromMain(t *testing.T) {
	ctx, _, _ := testutils.CtxAndAnalyticsForTest()
	dCli := docker.NewFakeClient()
	ib := NewImageBuilder(NewDockerBuilder(dCli, nil), nil, nil)
	iTarget := model.ImageTarget{BuildDetails: model.DockerBuild{}}

	mainRef := container.MustParseNamedTagged("gcr.io/foo/api:tilt-d34db33f")
	wtRef := container.MustParseNamedTagged("gcr.io/foo/api:tilt-d34db33f-wt-feat-auth")

	// Only main's image exists in the store.
	dCli.Images[mainRef.String()] = typesimage.InspectResponse{ID: string(testDigest)}

	ok, err := ib.CanReuseRef(ctx, iTarget, mainRef)
	require.NoError(t, err)
	assert.True(t, ok, "main's ref should still be reusable (existing behavior)")

	ok, err = ib.CanReuseRef(ctx, iTarget, wtRef)
	// The fake docker client's not-found error does not satisfy the
	// containerd/errdefs interface the real client path relies on
	// (fake_client.go notFoundError vs cerrdefs.IsNotFound), so a missing
	// image can surface here as an error. The observable contract stands:
	// a ref that does not exist is not reusable.
	assert.False(t, ok || err == nil, "worktree must not reuse main's image: distinct tag = distinct ref")

	// Once the worktree's own image exists, it reuses.
	dCli.Images[wtRef.String()] = typesimage.InspectResponse{ID: string(testDigest)}
	ok, err = ib.CanReuseRef(ctx, iTarget, wtRef)
	require.NoError(t, err)
	assert.True(t, ok)
}
