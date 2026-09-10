package tiltfile

import (
	"fmt"
	"path/filepath"
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

// Acceptance (tk-kxc, plan §4.5/§5): a worktree run's local_resource serve
// port is deconflicted through the port registry — distinct from the
// authored binding, inside the configured range, carried on LocalTarget
// (what the engine/gateway consume), and injected to the process through
// TILT_SERVE_PORT. Main runs keep the authored port untouched.
func TestLocalResource_WorktreeServePortDeconflict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		worktree string
	}{
		{"main run keeps authored port", ""},
		{"worktree run rebinds", "feat-auth"},
		{"second worktree rebinds again", "fix-bug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := wtPortRangeFor(t, "lrpd")
			defer releaseLRPorts(t, tc.worktree)

			f := newFixture(t)
			// The pool must be declared in the Tiltfile (not poked via
			// portregistry.SetPortRange): every load re-applies
			// worktree_config's range, and a load without worktree_config
			// resets the registry to OS-fallback mode AFTER translation.
			f.file("Tiltfile", fmt.Sprintf(`worktree_config(port_range=(%d, %d))
local_resource("web", "echo update", serve_cmd="python main.py", serve_port=8080)`, min, max))
			f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")
			f.file(filepath.Join(worktree.DefaultDir, "fix-bug", "keep"), "")

			tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
			if tc.worktree != "" {
				tf = ctrltiltfile.WorktreeTiltfile(tc.worktree, f.JoinPath("Tiltfile"), nil)
			}
			tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
			require.NoError(t, tlr.Error)
			require.Len(t, tlr.Manifests, 1)
			lt := tlr.Manifests[0].LocalTarget()
			if tc.worktree == "" {
				assert.Equal(t, 8080, lt.ServePort, "main keeps the authored port")
				assert.NotContains(t, lt.ServeCmd.Env, "TILT_SERVE_PORT=8080",
					"main needs no injection: the process already binds the authored port")
				return
			}

			require.NotEqual(t, 8080, lt.ServePort, "worktree must not hold the authored port")
			require.GreaterOrEqual(t, lt.ServePort, min)
			require.LessOrEqual(t, lt.ServePort, max)
			assert.Contains(t, lt.ServeCmd.Env, fmt.Sprintf("TILT_SERVE_PORT=%d", lt.ServePort),
				"serve process learns the deconflicted port through the env")
		})
	}
}

// The acceptance test: two worktrees with the same authored serve port get
// distinct registry allocations, and a same-worktree reload hands back the
// same port (sticky contract — plan §5).
func TestLocalResource_TwoWorktreesNoServePortClash(t *testing.T) {
	min, max := wtPortRangeFor(t, "lrwt")
	releaseLRPorts(t, "feat-auth", "fix-bug")

	load := func(t *testing.T, f *fixture, wtName string, prev *TiltfileLoadResult) model.Manifest {
		t.Helper()
		tf := ctrltiltfile.WorktreeTiltfile(wtName, f.JoinPath("Tiltfile"), nil)
		tlr := f.newTiltfileLoader().Load(f.ctx, tf, prev)
		require.NoError(t, tlr.Error)
		return tlr.Manifests[0]
	}

	newWtFixture := func(t *testing.T) *fixture {
		f := newFixture(t)
		// The pool must be declared in the Tiltfile (not poked via
		// portregistry.SetPortRange): every load re-applies worktree_config's
		// range, and a load without worktree_config resets the registry to
		// OS-fallback mode AFTER translation — so a second load in the same
		// test would otherwise pick with no range and honor the out-of-pool
		// request.
		f.file("Tiltfile", fmt.Sprintf(`worktree_config(port_range=(%d, %d))
local_resource("web", "echo update", serve_cmd="python main.py", serve_port=8080)`, min, max))
		f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")
		f.file(filepath.Join(worktree.DefaultDir, "fix-bug", "keep"), "")
		return f
	}

	f1 := newWtFixture(t)
	m1 := load(t, f1, "feat-auth", nil)
	f2 := newWtFixture(t)
	m2 := load(t, f2, "fix-bug", nil)

	p1 := m1.LocalTarget().ServePort
	p2 := m2.LocalTarget().ServePort
	require.NotEqual(t, p1, p2, "two worktrees must not hold the same serve port")
	require.NotEqual(t, 8080, p1)
	require.NotEqual(t, 8080, p2)

	// Reload stability: the same worktree re-declaring the same resource
	// keeps its allocation (the port does not churn on save).
	reload := load(t, f1, "feat-auth", nil)
	require.Equal(t, p1, reload.LocalTarget().ServePort)
}

// serve_port= without a registry conflict: the allocation honors the
// requested port when it is free (portregistry contract), so a single
// worktree still serves what it asked for when nothing else holds it.
// The request here is IN-POOL and free — allocated == requested — and the
// env var must STILL be injected: the authored serve_port is only a
// request, the serve process binds $TILT_SERVE_PORT (regression: the var
// was previously injected only when the allocation changed the port, so
// the literal $TILT_SERVE_PORT reached the shell whenever the request was
// honored).
func TestLocalResource_WorktreeServePortRequestedFree(t *testing.T) {
	min, max := wtPortRangeFor(t, "lrfree")
	releaseLRPorts(t, "feat-auth")
	f := newFixture(t)

	f.file("Tiltfile", fmt.Sprintf(`worktree_config(port_range=(%d, %d))
local_resource("web", "echo update", serve_cmd="python main.py", serve_port=%d)`, min, max, min))
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	lt := tlr.Manifests[0].LocalTarget()
	require.Equal(t, min, lt.ServePort, "free in-pool request must be honored")
	require.Less(t, lt.ServePort, max)
	require.Contains(t, lt.ServeCmd.Env, fmt.Sprintf("%s=%d", ServePortEnvVar, min),
		"$TILT_SERVE_PORT must be injected even when the request is honored")
}

// A serve_resource without serve_port= (or without serve_cmd) is untouched
// by the seam in both runs — the registry is only consulted for explicit
// ports.
func TestLocalResource_WorktreeNoServePortUnchanged(t *testing.T) {
	wtPortRangeFor(t, "lrnone")
	releaseLRPorts(t, "feat-auth")

	f := newFixture(t)
	f.file("Tiltfile", `local_resource("web", "echo update", serve_cmd="python main.py")`)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	lt := tlr.Manifests[0].LocalTarget()
	assert.Equal(t, 0, lt.ServePort)
	assert.Empty(t, lt.ServeCmd.Env)
}

// Acceptance (declarative scoping, plan §3): local_resource(scope=) declares
// where a resource instantiates across the main and worktree runs, replacing
// branching on worktree.name() for resource declarations. A scope that does
// not name the current run drops the resource from that run's load result —
// silently, so the same Tiltfile text executes in every run.

// scope="worktree": branch-local. The main run never sees the resource —
// nothing to skip with an if, nothing to double-define at the boundary.
func TestLocalResource_ScopeWorktreeDroppedInMain(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `local_resource("app", "echo update", serve_cmd="python main.py", scope="worktree")`)
	f.load()
	require.Empty(t, f.loadResult.Manifests, "branch-local resource must not instantiate in the main run")
}

// scope="main": shared foundation. Worktree runs never re-instantiate it —
// so Combine's shared-double-define error cannot fire across worktrees, and
// main's authored shape (serve port included) stands untouched.
func TestLocalResource_ScopeMainDroppedInWorktree(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `local_resource("db", "echo update", serve_cmd="python main.py", serve_port=10070, scope="main")`)
	f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

	tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)
	require.Empty(t, tlr.Manifests, "main-scoped resource must not instantiate in a worktree run")
}

// The run a scope names instantiates the resource with its authored shape.
// Loader-level names stay bare; the engine boundary prefixes clones
// (worktree/prefix.go).
func TestLocalResource_ScopeInstantiatesInNamedRun(t *testing.T) {
	t.Run("main", func(t *testing.T) {
		f := newFixture(t)
		f.file("Tiltfile", `local_resource("db", "echo update", serve_cmd="python main.py", serve_port=10070, scope="main")`)
		f.load()
		require.Len(t, f.loadResult.Manifests, 1)
		assert.Equal(t, "db", string(f.loadResult.Manifests[0].Name))
		assert.Equal(t, 10070, f.loadResult.Manifests[0].LocalTarget().ServePort)
	})
	t.Run("worktree", func(t *testing.T) {
		f := newFixture(t)
		f.file("Tiltfile", `local_resource("app", "echo update", serve_cmd="python main.py", scope="worktree")`)
		f.file(filepath.Join(worktree.DefaultDir, "feat-auth", "keep"), "")

		tf := ctrltiltfile.WorktreeTiltfile("feat-auth", f.JoinPath("Tiltfile"), nil)
		tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
		require.NoError(t, tlr.Error)
		require.Len(t, tlr.Manifests, 1)
		assert.Equal(t, "app", string(tlr.Manifests[0].Name))
	})
}

// Unknown scope values error loudly in every run — typo protection — instead
// of silently degrading to the default.
func TestLocalResource_ScopeInvalid(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `local_resource("app", "echo update", scope="wrktree")`)
	tlr := f.newTiltfileLoader().Load(f.ctx, ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil), nil)
	require.Error(t, tlr.Error)
	assert.Contains(t, tlr.Error.Error(), `scope must be one of "main", "worktree", "all"`)
}
