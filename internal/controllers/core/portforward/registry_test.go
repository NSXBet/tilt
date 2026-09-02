package portforward

// Tests for wiring the worktree port registry (internal/portregistry) into the
// portforward reconciler (tk-zfi): when Forward.LocalPort == 0, the reconciler
// consults the registry before falling back to the OS, so two worktrees
// declaring the same forward get distinct, stable local ports. Explicit
// LocalPorts pass through unchanged (the registry is only consulted for 0).
//
// All assertions observe the ForwardStatus boundary — the same surface the
// gateway and UI consume — not the implementation.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/portregistry"
)

// requireAllocatedPort waits until the named PortForward reports a started
// forward whose LocalPort was allocated from [lo, hi], and returns it.
// requireAllocatedPorts is the n-forward variant.
func requireAllocatedPorts(t *testing.T, f *pfrFixture, name string, lo, hi, n int) []int32 {
	t.Helper()
	ports := make([]int32, 0, n)
	require.Eventuallyf(t, func() bool {
		var pf PortForward
		if !f.Get(types.NamespacedName{Name: name}, &pf) {
			return false
		}
		ports = ports[:0]
		for _, s := range pf.Status.ForwardStatuses {
			if s.Error != "" || s.StartedAt.IsZero() {
				continue
			}
			if int(s.LocalPort) >= lo && int(s.LocalPort) <= hi {
				ports = append(ports, s.LocalPort)
			}
		}
		return len(ports) == n
	}, 2*time.Second, 20*time.Millisecond,
		"PortForward %q never reported %d started forwards with LocalPort in [%d,%d]", name, n, lo, hi)
	return ports
}

func requireAllocatedPort(t *testing.T, f *pfrFixture, name string, lo, hi int) int32 {
	t.Helper()
	var got int32
	require.Eventuallyf(t, func() bool {
		var pf PortForward
		if !f.Get(types.NamespacedName{Name: name}, &pf) {
			return false
		}
		for _, s := range pf.Status.ForwardStatuses {
			if s.Error != "" || s.StartedAt.IsZero() {
				continue
			}
			if int(s.LocalPort) >= lo && int(s.LocalPort) <= hi {
				got = s.LocalPort
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond,
		"PortForward %q never reported a started forward with LocalPort in [%d,%d]", name, lo, hi)
	return got
}

// The fixture's named acceptance: two worktrees requesting the same local
// port (both declare the same forward, i.e. LocalPort == 0 at the reconciler)
// must get distinct registry allocations, inside the configured range.
func TestRegistryAllocatesDistinctPortsPerWorktree(t *testing.T) {
	f := newPFRFixture(t)
	portregistry.SetPortRange(20000, 20010)
	t.Cleanup(func() { portregistry.SetPortRange(0, 0) })

	pfA := f.makePF("pf-wta-api", "wt:wt-a/api", "pod-wta-api", "", []Forward{f.makeForward(0, 8080, "")})
	pfB := f.makePF("pf-wtb-api", "wt:wt-b/api", "pod-wtb-api", "", []Forward{f.makeForward(0, 8080, "")})
	f.Create(pfA)
	f.Create(pfB)

	portA := requireAllocatedPort(t, f, "pf-wta-api", 20000, 20010)
	portB := requireAllocatedPort(t, f, "pf-wtb-api", 20000, 20010)
	require.NotEqual(t, portA, portB,
		"two worktrees requesting the same forward must get distinct local ports")
}

// Within one worktree, two auto-allocated forwards (e.g. web + db) must still
// get distinct ports — the registry owner key must be per-forward, not just
// per worktree or per PortForward object.
func TestRegistryAllocatesDistinctPortsPerForward(t *testing.T) {
	f := newPFRFixture(t)
	portregistry.SetPortRange(20100, 20110)
	t.Cleanup(func() { portregistry.SetPortRange(0, 0) })

	pf := f.makePF("pf-wtc-both", "wt:wt-c/api", "pod-wtc-api", "", []Forward{
		f.makeForward(0, 8080, ""),
		f.makeForward(0, 5432, ""),
	})
	f.Create(pf)

	var ports []int32
	require.Eventually(t, func() bool {
		var pfOut PortForward
		if !f.Get(types.NamespacedName{Name: "pf-wtc-both"}, &pfOut) {
			return false
		}
		ports = ports[:0]
		for _, s := range pfOut.Status.ForwardStatuses {
			if s.Error != "" || s.StartedAt.IsZero() {
				return false
			}
			if int(s.LocalPort) < 20100 || int(s.LocalPort) > 20110 {
				return false
			}
			ports = append(ports, s.LocalPort)
		}
		return len(ports) == 2
	}, 2*time.Second, 20*time.Millisecond,
		"both forwards never reported started statuses with LocalPort in [20100,20110]")
	require.NotEqual(t, ports[0], ports[1],
		"two forwards of one worktree resource must get distinct local ports")
}

// Ports do not churn on save: a Tiltfile reload that changes the resource's
// spec (here: the container port) recreates the forward, and the recreated
// forward must keep the previously allocated local port.
func TestRegistryPortStableAcrossReload(t *testing.T) {
	f := newPFRFixture(t)

	pf := f.makePF("pf-wtd-api", "wt:wt-d/api", "pod-wtd-api", "", []Forward{f.makeForward(0, 8080, "")})
	f.Create(pf)
	portBefore := requireAllocatedPort(t, f, "pf-wtd-api", 1, 65535)
	require.NotZero(t, portBefore, "registry must allocate a nonzero local port for LocalPort == 0")

	updated := f.makePF("pf-wtd-api", "wt:wt-d/api", "pod-wtd-api", "", []Forward{f.makeForward(0, 9090, "")})
	f.GetAndUpdate(updated)
	f.requirePortForwardStarted("pf-wtd-api", portBefore, 9090)
}

// Registry allocation failure (worktree port_range exhausted) surfaces through
// the existing portforward error path: ForwardStatus.Error, forward not started.
func TestRegistryExhaustionSurfacesAsForwardError(t *testing.T) {
	f := newPFRFixture(t)
	portregistry.SetPortRange(20300, 20300)
	t.Cleanup(func() { portregistry.SetPortRange(0, 0) })

	_, err := portregistry.Allocate("tkzfi-hog", 0)
	require.NoError(t, err)

	pf := f.makePF("pf-wte-api", "wt:wt-e/api", "pod-wte-api", "", []Forward{f.makeForward(0, 8080, "")})
	f.Create(pf)

	f.requirePortForwardStatus("pf-wte-api", 0, 8080, func(status ForwardStatus) (bool, string) {
		if status.Error == "" {
			return false, fmt.Sprintf("expected allocation failure as forward error, got status %+v", status)
		}
		if !status.StartedAt.IsZero() {
			return false, fmt.Sprintf("forward must not start when allocation fails, got status %+v", status)
		}
		return strings.Contains(status.Error, "exhausted"),
			fmt.Sprintf("error %q does not report range exhaustion", status.Error)
	})
}

// Boundary of the contract: only LocalPort == 0 consults the registry. An
// explicit LocalPort outside the configured range must reach the forwarder
// verbatim (upstream behavior), not be silently reallocated into the range.
func TestRegistryDoesNotHijackExplicitLocalPort(t *testing.T) {
	f := newPFRFixture(t)
	portregistry.SetPortRange(20400, 20410)
	t.Cleanup(func() { portregistry.SetPortRange(0, 0) })

	pf := f.makePF("pf-wtf-api", "wt:wt-f/api", "pod-wtf-api", "", []Forward{f.makeForward(8011, 8080, "")})
	f.Create(pf)

	f.requirePortForwardStarted("pf-wtf-api", 8011, 8080)
}
