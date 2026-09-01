package tiltfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
)

// Acceptance (plan §3, §7.1): worktree_config() parsed by the plugin
// (internal/tiltfile/worktree) surfaces on TiltfileLoadResult.WorktreeConfig,
// so the engine's discovery/re-execution driver can read dir/port_range/
// gateway from the load result.

// Defaults: no worktree_config() call → default dir ".worktree", gateway on,
// port range unset (0s → OS :0 fallback, plan §5), main run (Worktree "").
func TestWorktreeConfig_LoadResultDefaults(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
local_resource("x", "true")
`)

	f.load()
	s := f.loadResult.WorktreeConfig
	assert.Equal(t, ".worktree", s.Dir)
	assert.Equal(t, 0, s.PortMin)
	assert.Equal(t, 0, s.PortMax)
	assert.True(t, s.Gateway)
	assert.Equal(t, "", s.Worktree, "main run has no worktree context")
}

// Overrides declared in the Tiltfile flow through to the load result.
func TestWorktreeConfig_LoadResultOverrides(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
worktree_config(dir="local-wts", port_range=(15000, 15100), gateway=False)
local_resource("x", "true")
`)

	f.load()
	s := f.loadResult.WorktreeConfig
	assert.Equal(t, "local-wts", s.Dir)
	assert.Equal(t, 15000, s.PortMin)
	assert.Equal(t, 15100, s.PortMax)
	assert.False(t, s.Gateway)
}

// Worktree runs re-execute the root Tiltfile with the worktree name on the
// Tiltfile CR's tilt.dev/worktree label (plan §3). The load result reports
// the injected context; WorktreeConfig.Dir stays a worktree_config() override
// (the worktree's checkout dir is worktree.dir(), not the config dir).
func TestWorktreeConfig_WorktreeRunContext(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
worktree_config(dir="local-wts")
local_resource("x", "true")
`)

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	if tlr.Error != nil {
		t.Fatal(tlr.Error)
	}
	f.loadResult = tlr

	assert.Equal(t, "feat-auth", f.loadResult.WorktreeConfig.Worktree)
	// worktree_config() still applies on the re-executed root Tiltfile.
	assert.Equal(t, "local-wts", f.loadResult.WorktreeConfig.Dir)
	assert.True(t, f.loadResult.WorktreeConfig.Gateway)
}
