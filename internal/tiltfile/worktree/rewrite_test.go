package worktree

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/pkg/model"
)

// RewriteRun is the reconciler-facing rewrite seam (plan §7.3): one worktree
// run's manifests at the engine boundary. Tests pin the exported contract —
// main == nil tolerated, shared deps bare, own deps cloned, inputs untouched.

func TestRewriteRun_NilMain_PrefixesWithoutDepResolution(t *testing.T) {
	in := []model.Manifest{md("api", "postgres")}
	out := RewriteRun(in, "feat-auth", nil)
	require.Equal(t, model.ManifestName("wt:feat-auth_api"), out[0].Name)
	require.Equal(t, []model.ManifestName{"postgres"}, out[0].ResourceDependencies)
}

func TestRewriteRun_MainResult_ResolvedDepsAndSource(t *testing.T) {
	in := []model.Manifest{md("api", "postgres", "web")}
	out := RewriteRun(in, "feat-auth", []model.Manifest{md("postgres")})
	require.Equal(t, model.ManifestName("wt:feat-auth_api"), out[0].Name)
	require.Equal(t, []model.ManifestName{"postgres", "wt:feat-auth_web"}, out[0].ResourceDependencies)
}

func TestRewriteRun_SharedManifestStaysBare(t *testing.T) {
	out := RewriteRun([]model.Manifest{md("postgres")}, "feat-auth", []model.Manifest{md("postgres")})
	require.Equal(t, model.ManifestName("postgres"), out[0].Name)
}

func TestRewriteRun_EmptyWorktreeIsNoop(t *testing.T) {
	in := []model.Manifest{md("api", "postgres")}
	out := RewriteRun(in, "", []model.Manifest{md("postgres")})
	require.Equal(t, in, out)
}

func TestRewriteRun_DoesNotMutateInput(t *testing.T) {
	in := []model.Manifest{md("api", "postgres", "web")}
	_ = RewriteRun(in, "feat-auth", []model.Manifest{md("postgres")})
	require.Equal(t, model.ManifestName("api"), in[0].Name)
	require.Equal(t, []model.ManifestName{"postgres", "web"}, in[0].ResourceDependencies)
}
