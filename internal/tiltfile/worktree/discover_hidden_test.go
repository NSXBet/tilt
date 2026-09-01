package worktree

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Hidden (dot-prefixed) subdirectories are never discovered as worktrees,
// even when they contain a Tiltfile. Plan §2: discovery is position-based on
// `.worktree/` subdirs; a dot-prefixed dir is engine-internal state, not
// user intent, so it must never surface as a worktree.
func TestDiscover_SkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, writeFile(root, ".worktree/feat-auth/Tiltfile", ""))
	require.NoError(t, writeFile(root, ".worktree/.hidden/Tiltfile", ""))

	wts, err := Discover(root, "")
	require.NoError(t, err)
	require.Len(t, wts, 1, "hidden dir must not be discovered")
	require.Equal(t, "feat-auth", wts[0].Name)
}
