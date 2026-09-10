package worktree

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/tiltfile/starkit"
)

// Phase 1 acceptance: worktree builtins + root Tiltfile re-execution with
// injected worktree context (plan §2 trigger, §3 syntax, §7.1).

// worktree.name() returns "" in the main run (root Tiltfile evaluated without
// worktree context) — classic behavior preserved.
func TestWorktreeName_MainRunEmpty(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `
print(worktree.name())
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "\n", f.PrintOutput())
}

// Re-execution: the same root Tiltfile evaluated WITH a worktree context.
// worktree.name() returns the injected name; worktree.dir() returns the
// worktree's directory. Injection goes through the plugin (its OnStart sets
// the context, plan §0 "starkit thread local"); the engine's re-execution
// driver (plan §7.1) supplies it per run.
func TestWorktreeNameAndDir_WorktreeRun(t *testing.T) {
	wtDir := "/fake/.worktree/feat-auth"
	f := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-auth", wtDir)))
	f.File("Tiltfile", `
print(worktree.name())
print(worktree.dir())
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "feat-auth\n"+wtDir+"\n", f.PrintOutput())
}

// worktree.shared(name): true only for main-run-defined (shared) resources —
// the ones a worktree inherits instead of clones (plan §3 "Shared-hack
// inheritance", §4.3 "same-worktree first, else main-defined"). The gate is
// the main-run manifest-name set, injected via WithShared by the engine's
// re-execution driver (plan §0/§7.1: context travels through the plugin, one
// Environment per run).
func TestWorktreeShared_WorktreeRun(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin(
		WithWorktree("feat-auth", "/fake/.worktree/feat-auth"),
		WithShared([]string{"postgres", "admin-bff-image"}),
	))
	f.File("Tiltfile", `
print(worktree.shared("postgres"))
print(worktree.shared("admin-bff-image"))
print(worktree.shared("incidents-admin"))
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "True\nTrue\nFalse\n", f.PrintOutput())
}

// The main run has no worktree context and no shared set: worktree.shared is
// false for everything (nothing is shared with itself).
func TestWorktreeShared_MainRunFalse(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `
print(worktree.shared("postgres"))
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "False\n", f.PrintOutput())
}

// The gate is the worktree-run context, not just the supplied set: even if
// the engine miswires WithShared without WithWorktree, the main run shares
// nothing with itself (plan §3 — shared resources are defined by the main
// run, so sharing with itself is meaningless).
func TestWorktreeShared_MainRunIgnoresSuppliedSet(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin(WithShared([]string{"postgres"})))
	f.File("Tiltfile", `
print(worktree.shared("postgres"))
`)
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "False\n", f.PrintOutput())
}

// An empty name can never be a real resource: error, not silent False.
func TestWorktreeShared_EmptyNameErrors(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin(
		WithWorktree("feat-auth", "/fake/.worktree/feat-auth"),
		WithShared([]string{"postgres"}),
	))
	f.File("Tiltfile", `
worktree.shared("")
`)
	_, err := f.ExecFile("Tiltfile")
	require.Error(t, err)
	require.Contains(t, err.Error(), "name must be non-empty")
}

// worktree.shared takes exactly one positional name argument.
func TestWorktreeShared_RequiresName(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin(WithShared([]string{"postgres"})))
	f.File("Tiltfile", `
worktree.shared()
`)
	_, err := f.ExecFile("Tiltfile")
	require.Error(t, err)
}

// worktree_config(): the main Tiltfile declares overrides; values surface in
// the plugin state (feeds TiltfileLoadResult.WorktreeConfig, plan §7.1).
func TestWorktreeConfig_Parse(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `
worktree_config(dir="local-wts", port_range=(15000,15100), gateway=False)
`)
	model, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	s := MustState(model)
	require.Equal(t, "local-wts", s.Dir)
	require.Equal(t, 15000, s.PortMin)
	require.Equal(t, 15100, s.PortMax)
	require.False(t, s.Gateway)
}

// Defaults: no worktree_config() call → default dir ".worktree", gateway on.
// (Port range unset → 0s: fall back to OS :0 allocation, plan §5.)
func TestWorktreeConfig_Defaults(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `
`)
	model, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	s := MustState(model)
	require.Equal(t, ".worktree", s.Dir)
	require.Equal(t, 0, s.PortMin)
	require.Equal(t, 0, s.PortMax)
	require.True(t, s.Gateway)
}

// Plan §3 validation: "dir missing/empty → no worktrees, classic behavior."
// An explicitly empty dir= leaves the default ".worktree" — an empty
// override must not clobber it into ""; the explicit gateway override
// alongside still applies.
func TestWorktreeConfig_EmptyDirKeepsDefault(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `
worktree_config(dir="", gateway=False)
`)
	model, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	s := MustState(model)
	require.Equal(t, ".worktree", s.Dir)
	require.False(t, s.Gateway)
}

// worktree.branch(): the checked-out git branch of the checkout this run
// executes for. Shells out to real git — the branch contract is git's, not
// something to fake.
func TestWorktreeBranch_RealGitRepo(t *testing.T) {
	root := initGitRepo(t)
	require.NoError(t, writeFile(root, "Tiltfile", `
print(worktree.branch())
print(worktree.eq("main"))
print(worktree.eq("feat-auth"))
`))
	runGit(t, root, "worktree", "add", join(DefaultDir, "feat-auth"), "-b", "feat-auth")
	require.NoError(t, writeFile(root, join(DefaultDir, "feat-auth/Tiltfile"), `
print(worktree.branch())
print(worktree.eq("feat-auth"))
print(worktree.eq("main"))
`))

	// The main run's branch resolves from the executing Tiltfile's position,
	// so the fixture's Tiltfile must physically live in the repo: swap the
	// fixture temp dir for a symlink to the repo root (ExecFile reads the
	// root Tiltfile through it).
	fMain := starkit.NewFixture(t, NewPlugin())
	fMain.UseRealFS()
	require.NoError(t, os.RemoveAll(fMain.Path()))
	require.NoError(t, os.Symlink(root, fMain.Path()))
	_, err := fMain.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "main\nTrue\nFalse\n", fMain.PrintOutput())

	// The worktree run resolves the branch from the injected dir (a real git
	// checkout), so its Tiltfile can come from the fixture's fake FS.
	fWt := starkit.NewFixture(t, NewPlugin(WithWorktree("feat-auth", join(root, DefaultDir, "feat-auth"))))
	fWt.File(join(DefaultDir, "feat-auth/Tiltfile"), `
print(worktree.branch())
print(worktree.eq("feat-auth"))
print(worktree.eq("main"))
`)
	_, err = fWt.ExecFile(join(DefaultDir, "feat-auth/Tiltfile"))
	require.NoError(t, err)
	require.Equal(t, "feat-auth\nTrue\nFalse\n", fWt.PrintOutput())
}

// A detached-HEAD checkout has no branch: worktree.branch() is "" and
// worktree.eq matches only "". The worktree-side effect of `git worktree
// add --detach` is asserted in discover_test.go; here the MAIN checkout is
// detached, the position the main run resolves.
func TestWorktreeBranch_DetachedHead(t *testing.T) {
	root := initGitRepo(t)
	runGit(t, root, "checkout", "--detach", "HEAD")
	require.NoError(t, writeFile(root, "Tiltfile", `
print(worktree.branch())
print(worktree.eq(""))
print(worktree.eq("main"))
`))

	f := starkit.NewFixture(t, NewPlugin())
	f.UseRealFS()
	require.NoError(t, os.RemoveAll(f.Path()))
	require.NoError(t, os.Symlink(root, f.Path()))
	_, err := f.ExecFile("Tiltfile")
	require.NoError(t, err)
	require.Equal(t, "\nTrue\nFalse\n", f.PrintOutput())
}

// A checkout outside any git repo errors on both builtins: a Tiltfile
// branching on branches cannot run there silently — with scope= covering
// the no-branching case, an error beats a misleading False.
func TestWorktreeBranch_OutsideGitErrors(t *testing.T) {
	for _, expr := range []string{
		`print(worktree.branch())`,
		`print(worktree.eq("main"))`,
	} {
		f := starkit.NewFixture(t, NewPlugin())
		f.UseRealFS()
		f.File("Tiltfile", expr)
		_, err := f.ExecFile("Tiltfile")
		require.Error(t, err)
		require.Contains(t, err.Error(), "resolving the checked-out branch")
		require.Contains(t, err.Error(), "not a git repository")
	}
}

// worktree.eq takes exactly one positional branch argument.
func TestWorktreeEq_RequiresBranch(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `worktree.eq()`)
	_, err := f.ExecFile("Tiltfile")
	require.Error(t, err)
	require.Contains(t, err.Error(), "worktree.eq: missing argument for branch")
}

// worktree.branch takes no arguments.
func TestWorktreeBranch_RequiresNoArgs(t *testing.T) {
	f := starkit.NewFixture(t, NewPlugin())
	f.File("Tiltfile", `worktree.branch("main")`)
	_, err := f.ExecFile("Tiltfile")
	require.Error(t, err)
	require.Contains(t, err.Error(), "worktree.branch: got 1 arguments, want at most 0")
}
