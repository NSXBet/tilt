package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Phase 1 acceptance: position-based discovery (plan §2).
// Every subdirectory of the worktree dir (default `.worktree/`) containing a
// Tiltfile is a worktree, named by its directory basename. The main checkout is
// the implicit worktree "main" and is NOT part of Discover's result. A missing
// or empty worktree dir means no worktrees: classic single-Tiltfile behavior.
//
// Expected seam: Discover(root, dir string) ([]Worktree, error) where dir ""
// means the default ".worktree", and Worktree{Name, Dir string}.

func TestDiscover_NoWorktreeDir(t *testing.T) {
	root := t.TempDir()
	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Empty(t, wts)
}

func TestDiscover_EmptyWorktreeDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, mkdir(join(root, ".worktree")))
	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Empty(t, wts)
}

func TestDiscover_SubdirsWithTiltfile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeFile(root, ".worktree/fix-ui/Tiltfile", ""))
	require.NoError(t, writeFile(root, ".worktree/feat-auth/Tiltfile", ""))
	// no Tiltfile -> not a worktree
	require.NoError(t, writeFile(root, ".worktree/node_modules/some.js", ""))

	wts, err := Discover(root, "")
	require.NoError(t, err)
	// Deterministic order (sorted by name); named by directory basename.
	require.Equal(t, []Worktree{
		{Name: "feat-auth", Dir: join(root, ".worktree", "feat-auth")},
		{Name: "fix-ui", Dir: join(root, ".worktree", "fix-ui")},
	}, wts)
}

func TestDiscover_CustomDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeFile(root, "local-wts/feat-auth/Tiltfile", ""))
	// default dir exists but must be ignored when overridden
	require.NoError(t, writeFile(root, ".worktree/other/Tiltfile", ""))

	wts, err := Discover(root, "local-wts")
	require.NoError(t, err)
	require.Equal(t, []Worktree{
		{Name: "feat-auth", Dir: join(root, "local-wts", "feat-auth")},
	}, wts)
}

// Plan §3 validation: "Two worktrees with the same basename → load error."
// A subdir named "main" collides with the implicit main worktree, which is the
// one same-basename collision reachable via position-based discovery in a
// single flat dir.
func TestDiscover_MainSubdirCollides(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeFile(root, ".worktree/main/Tiltfile", ""))
	_, err := Discover(root, "")
	require.Error(t, err)
}

// Plan §2: "git worktree list is consulted only for a sanity warning ..., not
// as the source of truth — plain checkouts in .worktree/ work too." Real `git
// worktree add` checkouts (branch + detached HEAD) and a plain directory
// checkout are discovered identically by position; git metadata is irrelevant.
func TestDiscover_RealGitWorktreesAndPlainCheckouts(t *testing.T) {
	root := initGitRepo(t)

	runGit(t, root, "worktree", "add", ".worktree/feat-auth", "-b", "feat-auth")
	// A fresh `git worktree add` checks out only committed files; the repo's
	// single commit is empty, so the worktree gets its Tiltfile written here.
	// Discovery keys on the Tiltfile's position, not on git state.
	require.NoError(t, writeFile(root, ".worktree/feat-auth/Tiltfile", ""))
	runGit(t, root, "worktree", "add", "--detach", ".worktree/plain-checkout")
	require.NoError(t, writeFile(root, ".worktree/plain-checkout/Tiltfile", ""))
	// A plain directory (no git metadata at all) containing a Tiltfile.
	require.NoError(t, writeFile(root, ".worktree/just-a-dir/Tiltfile", ""))
	// A real worktree whose Tiltfile has not been created yet: not discovered.
	runGit(t, root, "worktree", "add", ".worktree/empty-wt", "-b", "empty-wt")

	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Equal(t, []Worktree{
		{Name: "feat-auth", Dir: join(root, ".worktree", "feat-auth")},
		{Name: "just-a-dir", Dir: join(root, ".worktree", "just-a-dir")},
		{Name: "plain-checkout", Dir: join(root, ".worktree", "plain-checkout")},
	}, wts)
}

// The worktree dir may itself be inside the git repo's ignored/untracked
// space (default ".worktree" starts with a dot); discovery must not depend on
// the entries being tracked or ignored — position only.
func TestDiscover_TiltfileContentIrrelevant(t *testing.T) {
	root := t.TempDir()
	// Content-bearing and empty Tiltfiles both count; only presence matters.
	require.NoError(t, writeFile(root, ".worktree/has-content/Tiltfile", "load('ext://x', 'x')\n"))
	require.NoError(t, writeFile(root, ".worktree/empty-file/Tiltfile", ""))

	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Len(t, wts, 2)
}

// Non-directory entries (stray files) in the worktree dir are skipped, and a
// directory without a Tiltfile stays undiscovered even when a Tiltfile-sized
// file sits elsewhere in it.
func TestDiscover_RequiresTiltfileInSubdirRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeFile(root, ".worktree/stray-file", "not a dir"))
	require.NoError(t, writeFile(root, ".worktree/deep/nested/Tiltfile", ""))

	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Empty(t, wts, "Tiltfile must be at the subdir root; nested does not count")
}

// initGitRepo creates a git repo with one commit, returning its (resolved)
// path so string comparisons are exact on macOS (/var vs /private/var).
func initGitRepo(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	runGit(t, root, "init", "-q", ".")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@t.local",
		"commit", "--allow-empty", "-q", "-m", "init")
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}
