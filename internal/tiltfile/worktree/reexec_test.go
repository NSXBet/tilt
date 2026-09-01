package worktree

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	osplugin "github.com/tilt-dev/tilt/internal/tiltfile/os"
	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
)

// Phase 2 acceptance: root Tiltfile re-execution with injected worktree
// context (plan §2/§0).
//
// The engine's re-execution driver evaluates the ROOT Tiltfile once per
// worktree, injecting name/cwd through this plugin (plan §0: starkit thread
// local on a per-run Environment). Per the §0 amendment, path resolution is
// REQUIRED to re-root at the worktree for worktree runs, so
// docker_build(".")/sync()/local() read worktree files with zero path edits.
//
// Tests pin observable behavior — worktree.name()/dir() output and
// starkit.AbsWorkingDir-derived builtins (os.getcwd, os.path.abspath) — not
// the mechanism: plan §0 allows either a synthetic execingTiltfileKey path
// inside the worktree or a worktree-aware AbsWorkingDir. The pinned
// invariant: in a worktree run, worktree.dir() == os.getcwd(), and relative
// paths resolve inside the worktree.

// Main run (no worktree context): worktree.name()/dir() return "" and path
// resolution stays rooted at the executing Tiltfile — classic single-Tiltfile
// behavior preserved when no worktrees exist (plan §3).
func TestReexec_MainRun_ClassicBehavior(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin(), osplugin.NewPlugin())
	f.File("Tiltfile", `
print(worktree.name())
print(worktree.dir())
print(os.getcwd())
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "\n\n"+f.Path()+"\n", f.PrintOutput())
}

// Worktree run: the root Tiltfile re-executes with the worktree context
// injected via the plugin. worktree.name() and worktree.dir() report the
// injected context. The worktree has no Tiltfile of its own (plan §3
// one-Tiltfile style): the root Tiltfile's content is what executes.
func TestReexec_WorktreeRun_NameAndDir(t *testing.T) {
	wtDir := filepath.Join(testRoot(t), "feat-auth")
	f := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-auth", wtDir)))
	f.File("Tiltfile", `
print(worktree.name())
print(worktree.dir())
print("root-tiltfile-ran")
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t,
		"feat-auth\n"+wtDir+"\nroot-tiltfile-ran\n",
		f.PrintOutput())
}

// Worktree run: path resolution is re-rooted at the worktree (plan §0
// amendment, REQUIRED). os.getcwd() and os.path.abspath() — and through
// AbsWorkingDir, docker_build/sync/local contexts and load() paths — resolve
// inside the worktree, not at the root Tiltfile's directory. The worktree
// root reported by worktree.dir() and the AbsWorkingDir must agree.
func TestReexec_WorktreeRun_PathRerooted(t *testing.T) {
	wtDir := filepath.Join(testRoot(t), "feat-auth")
	f := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-auth", wtDir)), osplugin.NewPlugin())
	f.File("Tiltfile", `
print(os.getcwd())
print(os.path.abspath("./web"))
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t,
		wtDir+"\n"+join(wtDir, "web")+"\n",
		f.PrintOutput())
}

// Per-worktree Environment isolation (plan §0): each worktree run is its own
// starkit Environment (fresh load per BuildEntry). Sequential runs in one
// process must not leak name, dir, path root, or worktree_config state into
// each other.
func TestReexec_Isolation_NoCrossWorktreeLeak(t *testing.T) {
	root := testRoot(t)
	wtA := filepath.Join(root, "feat-auth")
	wtB := filepath.Join(root, "feat-ui")

	// Run A: feat-auth, with worktree_config overrides.
	fA := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-auth", wtA)), osplugin.NewPlugin())
	fA.File("Tiltfile", `
worktree_config(dir="custom-wts", gateway=False)
print(worktree.name())
print(os.getcwd())
`)
	modelA, err := fA.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "feat-auth\n"+wtA+"\n", fA.PrintOutput())
	sA := MustState(modelA)
	require.Equal(t, "custom-wts", sA.Dir)
	require.False(t, sA.Gateway)

	// Run B: a different worktree, same process — sees only its own context.
	fB := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-ui", wtB)), osplugin.NewPlugin())
	fB.File("Tiltfile", `
print(worktree.name())
print(os.getcwd())
`)
	modelB, err := fB.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "feat-ui\n"+wtB+"\n", fB.PrintOutput())

	// Run B's worktree_config state is at defaults: A's overrides did not leak.
	sB := MustState(modelB)
	require.Equal(t, ".worktree", sB.Dir)
	require.True(t, sB.Gateway)
}

// testRoot is a real, symlink-resolved temp dir. Worktree dirs live under
// it so re-rooted resolution lands on test real paths and string comparison is exact (macOS /var vs /private/var).
func testRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return path
}
