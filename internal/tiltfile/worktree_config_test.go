package tiltfile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
	"github.com/tilt-dev/tilt/internal/k8s/testyaml"
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

// The loader produces the apply-interception input (plan §4.1, tk-mzx):
// a worktree run stamps the worktree name onto every K8s target's
// KubernetesApplySpec.Worktree, so the KubernetesApply controller's
// clone-stamping stage (stampWorktreeClones) fires for this run's
// resources. The main run leaves it empty (Worktree == "" is a no-op).
func TestWorktreeConfig_K8sApplySpecStampedWithWorktree(t *testing.T) {
	f := newFixture(t)
	f.file("Tiltfile", `
k8s_yaml('sancho.yaml')
`)
	f.file("sancho.yaml", testyaml.SanchoYAML)
	// Path re-rooting (plan §0 amendment) makes the worktree run resolve
	// sancho.yaml inside its checkout; place it there.
	f.file(".worktree/feat-auth/sancho.yaml", testyaml.SanchoYAML)

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)

	require.Len(t, tlr.Manifests, 1)
	assert.Equal(t, "feat-auth", tlr.Manifests[0].K8sTarget().KubernetesApplySpec.Worktree,
		"worktree run manifests must carry the worktree on their apply spec")

	// Main run: no stamping — classic behavior preserved.
	f2 := newFixture(t)
	f2.file("Tiltfile", `
k8s_yaml('sancho.yaml')
`)
	f2.file("sancho.yaml", testyaml.SanchoYAML)
	f2.load()
	assert.Equal(t, "", f2.loadResult.Manifests[0].K8sTarget().KubernetesApplySpec.Worktree,
		"main run apply specs must stay unstamped")
}
