package worktree

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/pkg/apis"
	"github.com/tilt-dev/tilt/pkg/model"
)

// tk-ntx acceptance: the engine-prefix rewrite pass at the engine boundary
// (plan §4.3, §7.3). The pass rewrites one worktree run's manifests between
// TiltfileLoadResult and updateOwnedObjects/ConfigsReloadedAction:
//
//   - clone names are path-segment safe (`wt:<wt>_<name>`): the engine name
//     doubles as apiserver object name (engine reducers join UIResource /
//     KubernetesApply objects to manifests by equal name, and the
//     tilt-apiserver rejects '/' in names);
//   - names DERIVED from the bare manifest name at load time (DockerImage,
//     LiveUpdate, CmdImage object names; local target names) are re-stamped
//     with the clone name, so main and worktree never overwrite each other's
//     API objects;
//   - deps resolve same-worktree first, else main-defined (Combine);
//   - shared (main-defined) manifests stay bare — the worktree's definition
//     wins; main-run input passes through untouched.

// boundaryMd builds a manifest with a k8s deploy target and an image target,
// the shape a worktree run produces for `docker_build` + `k8s_resource`.
func boundaryMd(name string, deps ...string) model.Manifest {
	m := md(name, deps...)
	iTarget := model.ImageTarget{}
	iTarget.ImageMapSpec.Selector = "registry.example.com/" + name
	// Derived names in the loader's exact format: liveupdate.GetName /
	// dockerimage.GetName = SanitizeName("<manifest>:<targetName>").
	iTarget.LiveUpdateName = apis.SanitizeName(name + ":" + iTarget.ID().Name.String())
	iTarget.DockerImageName = apis.SanitizeName(name + ":" + iTarget.ID().Name.String())
	m.ImageTargets = []model.ImageTarget{iTarget}
	return m
}

// localMd builds a local_resource-shaped manifest: LocalTarget holds its
// name independently of the manifest name.
func localMd(name string, deps ...string) model.Manifest {
	m := md(name, deps...)
	updateCmd := model.Cmd{Argv: []string{"echo", "update"}}
	lt := model.NewLocalTarget(model.TargetName(name), updateCmd, model.Cmd{}, nil)
	m.DeployTarget = lt
	return m
}

// Main run: the pass is a passthrough — the main run defines the shared
// (bare) namespace and must not be rewritten.
func TestApplyBoundary_MainRunPassthrough(t *testing.T) {
	out, _ := ApplyBoundary(nil, RunResult{Name: "", Manifests: nil})
	require.Empty(t, out.Manifests)
	require.Empty(t, out.Defined)
}

// Worktree run over a main result: clones are prefixed; deps resolve
// main-defined → bare, own → clone.
func TestApplyBoundary_ClonesAndDeps(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name: "feat-auth",
		Manifests: []model.Manifest{
			boundaryMd("api", "postgres", "web"),
			boundaryMd("web"),
		},
	})
	require.NoError(t, err)

	require.Equal(t, []model.ManifestName{
		"wt:feat-auth_api",
		"wt:feat-auth_web",
	}, out.Defined)

	// Deps: main-defined stays bare; own worktree dep rewrites to the clone.
	api := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_api"), api.Name)
	require.Equal(t, []model.ManifestName{"postgres", "wt:feat-auth_web"}, api.ResourceDependencies)
	require.Equal(t, model.ManifestName("tiltfile:feat-auth"), api.SourceTiltfile)
}

// Shared manifest flowing through the run: keeps its bare engine name (the
// worktree's definition wins, plan §3) and its derived names stay bare too.
func TestApplyBoundary_SharedManifestStaysBare(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{boundaryMd("postgres")},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, []model.ManifestName{"postgres"}, out.Defined)
	require.Equal(t, model.ManifestName("postgres"), m.Name)
	// Derived names re-stamp from the bare name: unchanged.
	require.Equal(t, "postgres:"+m.ImageTargets[0].ID().Name.String(), m.ImageTargets[0].LiveUpdateName)
	require.Equal(t, "postgres:"+m.ImageTargets[0].ID().Name.String(), m.ImageTargets[0].DockerImageName)
}

// Derived names: a clone's DockerImage/LiveUpdate object names re-stamp with
// the clone manifest name (path-segment safe), and the image target's
// ref-derived identity — the ImageMap selector — is scoped to the worktree
// (tk-1zq): distinct ImageMap per worktree, name-matching its own build refs.
func TestApplyBoundary_DerivedNamesRestamped(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}
	out, err := ApplyBoundary(main, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{boundaryMd("api")},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	iTarget := m.ImageTargets[0]
	// ImageMap identity is scoped per worktree (tk-1zq): the clone of the
	// same Dockerfile gets its own ImageMap, not main's.
	require.Equal(t, "registry.example.com/api-wt-feat-auth", iTarget.ImageMapSpec.Selector)
	require.Equal(t, "registry.example.com_api-wt-feat-auth", iTarget.ImageMapName())
	// Derived object names carry the clone manifest name. SanitizeName
	// rewrites '/' in the ref to '_' but keeps the wt: prefix and ':'.
	cloneDerived := apis.SanitizeName("wt:feat-auth_api:" + iTarget.ID().Name.String())
	require.Equal(t, cloneDerived, iTarget.LiveUpdateName)
	require.Equal(t, cloneDerived, iTarget.DockerImageName)
}

// LocalTarget names: independent of the manifest name at load time, renamed
// to the clone name so the update Cmd (`<target>:update`) and the target's
// FileWatch (`local:<target>`) isolate per worktree.
func TestApplyBoundary_LocalTargetRenamed(t *testing.T) {
	out, err := ApplyBoundary(nil, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{localMd("web")},
	})
	require.NoError(t, err)

	m := out.Manifests[0]
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), m.Name)
	lt := m.LocalTarget()
	require.Equal(t, model.TargetName("wt:feat-auth_web"), lt.Name)
	require.Equal(t, "wt:feat-auth_web:update", lt.UpdateCmdName())
}

// No main result yet: names are still rewritten; bare deps cannot resolve
// and error (Combine's nil-main contract), surfaced as the run's load error.
func TestApplyBoundary_NilMainErrorsOnUnresolvedDep(t *testing.T) {
	_, err := ApplyBoundary(nil, RunResult{
		Name:      "feat-auth",
		Manifests: []model.Manifest{boundaryMd("api", "postgres")},
	})
	require.ErrorContains(t, err, `depends on "postgres"`)
}

// Double-define of the same shared name by two worktrees is a load error;
// distinct clone names across worktrees are not.
func TestApplyBoundary_DoubleDefineAcrossRuns(t *testing.T) {
	main := []model.Manifest{boundaryMd("postgres")}

	// Two worktrees may define the same OWN name — distinct clones.
	out1, err := ApplyBoundary(main, RunResult{Name: "feat-auth", Manifests: []model.Manifest{boundaryMd("web")}})
	require.NoError(t, err)
	out2, err := ApplyBoundary(main, RunResult{Name: "feat-ui", Manifests: []model.Manifest{boundaryMd("web")}})
	require.NoError(t, err)
	require.Equal(t, model.ManifestName("wt:feat-auth_web"), out1.Manifests[0].Name)
	require.Equal(t, model.ManifestName("wt:feat-ui_web"), out2.Manifests[0].Name)
}

// The pass must not mutate the caller's run input: the reconciler re-reads
// bare names for author-visible error reporting (validate.go's rawDeps
// contract) and re-runs the pass on reload.
func TestApplyBoundary_NoInputMutation(t *testing.T) {
	run := RunResult{Name: "feat-auth", Manifests: []model.Manifest{
		boundaryMd("api", "web"),
		boundaryMd("web"),
	}}
	in := run.Manifests
	_, err := ApplyBoundary([]model.Manifest{boundaryMd("postgres")}, run)
	require.NoError(t, err)

	require.Equal(t, model.ManifestName("api"), in[0].Name)
	require.Equal(t, []model.ManifestName{"web"}, in[0].ResourceDependencies)
	require.NotEmpty(t, in[0].ImageTargets)
	require.Equal(t,
		apis.SanitizeName("api:"+in[0].ImageTargets[0].ID().Name.String()),
		in[0].ImageTargets[0].LiveUpdateName,
		"input derived names stay bare")
}
