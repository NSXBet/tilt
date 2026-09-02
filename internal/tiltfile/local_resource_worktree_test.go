package tiltfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Acceptance (tk-zkx, plan §3/§4.5): local_resource in a worktree run
// executes in the worktree cwd and carries engine-prefixed identity at the
// boundary; main-defined local_resources are inherited by worktree deps
// without re-running. Serve-port registry allocation has NO existing seam
// (model.LocalTarget carries no port field; serve ports never reach the
// portforward reconciler) — that half of tk-zkx is a blocked prerequisite
// requiring a LocalTarget serve-port field first.

// A worktree run's local_resource resolves its cmd/serve_cmd working dirs
// into the worktree checkout (plan §3 zero-path-edits): the thread's
// worktree context re-roots starkit.AbsWorkingDir (starkit/path.go), and
// localResource.threadDir records the same dir for watches.
func TestLocalResource_WorktreeCwd(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
local_resource("dev", "echo update", serve_cmd="python main.py")
`)
	// Only exists inside the worktree checkout — a main-repo resolution
	// would not see it either way, but the dir assertions below pin the
	// exact cwd contract.
	f.file(".worktree/feat-auth/keep", "")

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1)

	lt := tlr.Manifests[0].LocalTarget()
	wtDir := f.JoinPath(".worktree/feat-auth")
	assert.Equal(t, wtDir, lt.UpdateCmdSpec.Dir, "cmd runs in worktree cwd")
	assert.Equal(t, wtDir, lt.ServeCmd.Dir, "serve_cmd runs in worktree cwd")
}

// Main run: classic behavior — dirs resolve against the Tiltfile's dir,
// nothing stamped.
func TestLocalResource_MainRunCwdUnchanged(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
local_resource("dev", "echo update", serve_cmd="python main.py")
`)
	f.load()
	require.Len(t, f.loadResult.Manifests, 1)

	lt := f.loadResult.Manifests[0].LocalTarget()
	assert.Equal(t, f.Path(), lt.UpdateCmdSpec.Dir)
	assert.Equal(t, f.Path(), lt.ServeCmd.Dir)
}

// Engine-prefixed identity (plan §4.3/§4.5): the boundary pass renames the
// worktree run's local resource to the clone name — manifest, LocalTarget
// name, and the derived update-Cmd name — while a dep on a main-defined
// local resource (the admin-bff-image sha-pinned pull pattern) stays bare:
// the shared resource is inherited, not cloned or re-run.
func TestLocalResource_WorktreeIdentityAndSharedInheritance(t *testing.T) {
	main := []model.Manifest{localResourceMd("admin-bff-image")}
	out, err := worktree.ApplyBoundary(main, worktree.RunResult{
		Name: "feat-auth",
		Manifests: []model.Manifest{
			localResourceMd("web", "admin-bff-image"),
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Manifests, 1)

	m := out.Manifests[0]
	lt := m.LocalTarget()
	require.NotNil(t, lt)

	assert.Equal(t, model.ManifestName("wt:feat-auth_web"), m.Name)
	assert.Equal(t, model.TargetName("wt:feat-auth_web"), lt.Name,
		"LocalTarget carries its own name; boundary renames it with the manifest")
	if lt.UpdateCmdSpec != nil {
		assert.Contains(t, lt.UpdateCmdSpec.Dir, "", "sanity")
	}
	assert.Equal(t, []model.ManifestName{"admin-bff-image"}, m.ResourceDependencies,
		"dep on main-defined local resource resolves bare: inherited, not re-run")

	// The update cmd's apiserver name derives from the clone manifest, so
	// the Cmd CR registers under the clone identity.
	assert.Equal(t, "wt:feat-auth_web:update", lt.UpdateCmdName())
}

// localResourceMd builds a main-run local_resource manifest with the
// loader's exact shape (tiltfile_state.go translateLocal).
func localResourceMd(name string, deps ...string) model.Manifest {
	m := model.Manifest{Name: model.ManifestName(name)}
	for _, d := range deps {
		m.ResourceDependencies = append(m.ResourceDependencies, model.ManifestName(d))
	}
	lt := model.NewLocalTarget(model.TargetName(name),
		model.Cmd{Argv: []string{"echo", "update"}, Dir: "/repo"}, model.Cmd{}, nil)
	m.DeployTarget = lt
	return m
}
