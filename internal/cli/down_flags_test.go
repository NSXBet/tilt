package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression: `tilt down` must keep accepting --delete-volumes (and the
// fork-added --worktrees flag) at flag-parse time.
func TestDownFlagParsing(t *testing.T) {
	t.Run("both flags settable", func(t *testing.T) {
		c := newDownCmd()
		cmd := c.register()

		require.NoError(t, cmd.Flags().Parse([]string{"--delete-volumes", "--worktrees=false"}))
		require.True(t, c.deleteVolumes)
		require.False(t, c.worktrees)
	})

	t.Run("defaults: volumes off, worktrees on", func(t *testing.T) {
		c := newDownCmd()
		cmd := c.register()

		require.NoError(t, cmd.Flags().Parse([]string{}))
		require.False(t, c.deleteVolumes)
		require.True(t, c.worktrees)
	})
}
