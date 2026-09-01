package worktree

import (
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
