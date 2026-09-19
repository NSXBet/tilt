package tiltfile

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/portregistry"
	ctrltiltfile "github.com/tilt-dev/tilt/internal/controllers/apis/tiltfile"
)

func TestTestFnDeprecated(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
test("test", "echo hi")
`)
	f.loadAssertWarnings(testDeprecationMsg)
}

func TestLocalResourceDirWithoutCmd(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", serve_cmd="python server.py", dir="./foo")
`)
	f.loadErrString("'dir' only affects 'cmd', not 'serve_cmd'. Did you mean to use 'serve_dir' instead?")
}

func TestLocalResourceDirWithoutCmdNoServe(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", dir="./foo")
`)
	f.loadErrString("'dir' specified but 'cmd' is empty")
}

func TestLocalResourceServeDirWithoutServeCmd(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", cmd="echo hi", serve_dir="./foo")
`)
	f.loadErrString("'serve_dir' specified but 'serve_cmd' is empty")
}

func TestLocalResourceServePortWithoutServeCmd(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", cmd="echo hi", serve_port=8080)
`)
	f.loadErrString("'serve_port' specified but 'serve_cmd' is empty")
}

func TestLocalResourceDirWithCmdWorks(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", cmd="echo hi", dir="./foo")
`)
	f.load()
}

func TestLocalResourceServeDirWithServeCmdWorks(t *testing.T) {
	f := newFixture(t)

	f.file("Tiltfile", `
local_resource("test", serve_cmd="python server.py", serve_dir="./foo")
`)
	f.load()
}

// Main-run serve-port reservation (plan §4.5): a main run of a
// worktree=True local_resource binds its authored serve port directly (no
// TILT_SERVE_PORT injection), so translateLocal reserves that port in the
// registry under the main-run owner before any worktree run loads. A
// clone's Allocate must then fall back instead of handing out the port
// main is about to bind — the deterministic main/clone collision the
// worktrees example hit (main on 30001, wt-a allocated the same 30001).
func TestLocalResourceWorktree_MainRunReservesServePort(t *testing.T) {
	f := newFixture(t)
	min, max := wtPortRangeFor(t, "lr-servetest")
	f.file("Tiltfile", fmt.Sprintf(`
worktree_config(port_range=(%d, %d))
local_resource("web", cmd="true", serve_cmd="serve", serve_port=%d, worktree=True)
`, min, max, min))
	t.Cleanup(func() {
		portregistry.Release(wtLocalResourcePortOwner("", "web"))       // main-run reservation
		portregistry.Release(wtLocalResourcePortOwner("feat-auth", "web"))
	})

	// Main run keeps the authored port...
	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1)
	assert.Equal(t, min, tlr.Manifests[0].LocalTarget().ServePort,
		"main run must keep the authored serve port")

	// ...and the reservation must push the worktree clone's allocation off
	// it (fallback within the test's narrow range).
	wtf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	wtf.Labels = map[string]string{"tilt.dev/worktree": "feat-auth"}
	wtlr := f.newTiltfileLoader().Load(f.ctx, wtf, nil)
	require.NoError(t, wtlr.Error)
	require.Len(t, wtlr.Manifests, 1)
	clonePort := wtlr.Manifests[0].LocalTarget().ServePort
	assert.NotEqual(t, min, clonePort, "clone must not be allocated the main run's port")
	assert.GreaterOrEqual(t, clonePort, min)
	assert.LessOrEqual(t, clonePort, max)

	// Sticky across reloads: re-running the main Tiltfile keeps its port.
	tlr2 := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr2.Error)
	assert.Equal(t, min, tlr2.Manifests[0].LocalTarget().ServePort)
}

// Unflagged main-run resources stay reservation-free: they never
// instantiate in worktree runs, so their authored port needs no registry
// entry and none is taken.
func TestLocalResourceWorktree_UnflaggedMainRunNoReservation(t *testing.T) {
	f := newFixture(t)
	min, _ := wtPortRangeFor(t, "lr-servetest2")
	f.file("Tiltfile", fmt.Sprintf(`
local_resource("web", cmd="true", serve_cmd="serve", serve_port=%d)
`, min))
	t.Cleanup(func() {
		portregistry.Release(wtLocalResourcePortOwner("", "web"))
	})

	tf := ctrltiltfile.MainTiltfile(f.JoinPath("Tiltfile"), nil)
	tlr := f.newTiltfileLoader().Load(f.ctx, tf, nil)
	require.NoError(t, tlr.Error)
	require.Len(t, tlr.Manifests, 1)
	assert.Equal(t, min, tlr.Manifests[0].LocalTarget().ServePort)

	// No reservation: an explicit Allocate for the same port still honors
	// it (the unflagged resource never runs in a worktree).
	got, err := portregistry.Allocate("lr:probe/web", min)
	require.NoError(t, err)
	assert.Equal(t, min, got)
	portregistry.Release("lr:probe/web")
}
