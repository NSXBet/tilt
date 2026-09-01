package portregistry_test

import (
	"fmt"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/portregistry"
)

// freePort finds a port that is free right now by binding :0 and closing.
// There is a small TOCTOU window between close and the registry's bind; the
// tests tolerate that by not asserting WHICH fallback port was picked, only
// range membership and listenability.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// Allocate must return the requested port when it is free — in-range when a
// range is configured, and any port when none is. A second owner must never
// receive a port already allocated to someone else.
func TestAllocateHonorsRequestedPort(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-req")
		portregistry.Release("other-owner")
	})
	portregistry.SetPortRange(0, 0)

	requested := freePort(t)
	p, err := portregistry.Allocate("wt-req", requested)
	require.NoError(t, err)
	require.Equal(t, requested, p, "free requested port must be honored")

	p2, err := portregistry.Allocate("other-owner", requested)
	if err == nil {
		require.NotEqual(t, requested, p2,
			"second owner must not receive a port already allocated to wt-req")
		portregistry.Release("other-owner")
	}
}

// Allocate with requested=0 must fall back to the OS (listen on :0), like
// getAvailablePort in internal/k8s/portforward.go. The allocation must be
// real: the port is actually listenable (the registry holds it, so the
// re-listen here is expected to fail — instead assert the port is sane and
// that the registry keeps it stable).
func TestAllocateOSFallback(t *testing.T) {
	t.Cleanup(func() { portregistry.Release("wt-os-fallback") })

	p, err := portregistry.Allocate("wt-os-fallback", 0)
	require.NoError(t, err)
	require.Greater(t, p, 0)
	require.Less(t, p, 65536)

	// Stability is the observable proof the registry holds the port: a second
	// Allocate for the same owner returns it without re-binding.
	p2, err := portregistry.Allocate("wt-os-fallback", 0)
	require.NoError(t, err)
	require.Equal(t, p, p2)
}

// Out-of-range requested ports are not honored, even when free: the
// worktree_config port_range is a hard boundary (plan §3/§5). The allocation
// falls back into the range.
func TestAllocateOutOfRangeRequestedNotHonored(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-out-of-range")
	})
	portregistry.SetPortRange(32800, 32900)

	p, err := portregistry.Allocate("wt-out-of-range", 40000)
	require.NoError(t, err)
	require.GreaterOrEqual(t, p, 32800)
	require.LessOrEqual(t, p, 32900, "fallback must stay inside the configured range")
}

// Reload stability: allocating the same (owner, requested) twice — as happens
// when a Tiltfile reload re-declares the same port_forwards — must return the
// same port, not a fresh one. A range change between the calls (reload with a
// new worktree_config) must not churn already-allocated ports.
func TestAllocateStableAcrossReload(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-reload")
	})

	p1, err := portregistry.Allocate("wt-reload", 0)
	require.NoError(t, err)
	require.Greater(t, p1, 0)

	// Simulated reload: range re-set (even to a range not containing p1).
	portregistry.SetPortRange(33000, 33100)
	p2, err := portregistry.Allocate("wt-reload", 0)
	require.NoError(t, err)
	require.Equal(t, p1, p2, "same owner must be stable across reload")

	// And with the originally requested port restated, as the re-executed
	// Tiltfile would do.
	p3, err := portregistry.Allocate("wt-reload", p1)
	require.NoError(t, err)
	require.Equal(t, p1, p3)
}

// Range exhaustion: when the configured port_range is fully consumed,
// Allocate must return an error (surfacing as a resource error via the
// existing portforward error path), never a port outside the range. The
// error must match portregistry.ErrRangeExhausted.
func TestAllocateRangeExhaustionError(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		for port := 32768; port <= 32777; port++ {
			portregistry.Release(fmt.Sprintf("wt-exhaust-%d", port))
		}
		portregistry.Release("wt-exhaust-over")
	})
	portregistry.SetPortRange(32768, 32777)

	// Exhaust every port in the range with distinct owners. Ports may be
	// squatted by the OS in this range, so allow either success or an
	// exhaustion error mid-loop; the invariant tested is the tail behavior.
	for port := 32768; port <= 32777; port++ {
		_, err := portregistry.Allocate(fmt.Sprintf("wt-exhaust-%d", port), port)
		if err != nil {
			require.ErrorIs(t, err, portregistry.ErrRangeExhausted)
			break
		}
		require.NoError(t, err, "port %d should be allocatable", port)
	}

	_, err := portregistry.Allocate("wt-exhaust-over", 0)
	require.ErrorIs(t, err, portregistry.ErrRangeExhausted,
		"exhausted range must error instead of leaking outside the range")
}

// Distinct owners asking for the same free requested port are deconflicted:
// the first gets it, the second gets a different allocation.
func TestAllocateDistinctOwnersSameRequested(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-a")
		portregistry.Release("wt-b")
	})
	portregistry.SetPortRange(0, 0)

	requested := freePort(t)
	p1, err := portregistry.Allocate("wt-a", requested)
	require.NoError(t, err)
	require.Equal(t, requested, p1)

	p2, err := portregistry.Allocate("wt-b", requested)
	require.NoError(t, err)
	require.NotEqual(t, requested, p2,
		"second owner must not receive the port already held by wt-a")
	require.NotEqual(t, p1, p2)
}

// Allocating from a configured range with requested=0 hands out distinct
// in-range ports to distinct owners (the two-worktrees-same-Tiltfile case,
// plan §5), and never reuses one.
func TestAllocateRangeFallbackDistinctPorts(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		for i := 0; i < 3; i++ {
			portregistry.Release(fmt.Sprintf("wt-range-%d", i))
		}
	})
	portregistry.SetPortRange(33200, 33204)

	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		p, err := portregistry.Allocate(fmt.Sprintf("wt-range-%d", i), 0)
		require.NoError(t, err)
		require.GreaterOrEqual(t, p, 33200)
		require.LessOrEqual(t, p, 33204)
		require.False(t, seen[p], "port %d handed out twice", p)
		seen[p] = true
	}
}

// Release returns the port to the pool: a new owner can then claim it.
func TestReleaseReturnsPortToPool(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-rel-first")
		portregistry.Release("wt-rel-second")
	})
	portregistry.SetPortRange(33300, 33300)

	p1, err := portregistry.Allocate("wt-rel-first", 0)
	require.NoError(t, err)

	portregistry.Release("wt-rel-first")

	p2, err := portregistry.Allocate("wt-rel-second", 0)
	require.NoError(t, err)
	require.Equal(t, p1, p2, "released port must be reclaimable")

	// Release is idempotent.
	portregistry.Release("wt-rel-first")
	portregistry.Release("wt-rel-nonexistent")
}

// Allocate must be safe under concurrent use: N owners allocating in
// parallel all get distinct ports within the range (race detector runs this).
func TestAllocateConcurrentDistinct(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		for i := 0; i < 8; i++ {
			portregistry.Release(fmt.Sprintf("wt-conc-%d", i))
		}
	})
	portregistry.SetPortRange(33400, 33407)

	const n = 8
	ports := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ports[i], errs[i] = portregistry.Allocate(fmt.Sprintf("wt-conc-%d", i), 0)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.GreaterOrEqual(t, ports[i], 33400)
		require.LessOrEqual(t, ports[i], 33407)
		for j := i + 1; j < n; j++ {
			require.NotEqual(t, ports[i], ports[j],
				"concurrent allocations must not collide (%d vs %d)", i, j)
		}
	}
}

// A requested port taken by a non-Tilt process is skipped: Allocate falls
// back without honoring the squatting port.
func TestAllocateRequestedTakenFallsBack(t *testing.T) {
	t.Cleanup(func() {
		portregistry.SetPortRange(0, 0)
		portregistry.Release("wt-squat")
	})
	portregistry.SetPortRange(0, 0)

	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, l.Close()) })
	taken := l.Addr().(*net.TCPAddr).Port

	p, err := portregistry.Allocate("wt-squat", taken)
	require.NoError(t, err)
	require.NotEqual(t, taken, p, "must not honor a port already bound by another process")
}

// Privileged ports are never handed out, even when requested and free.
func TestAllocateRejectsPrivilegedRequested(t *testing.T) {
	t.Cleanup(func() { portregistry.Release("wt-privileged") })

	// Port 80 is privileged and virtually always free to bind as root only;
	// the registry must refuse to honor it rather than try to bind it.
	p, err := portregistry.Allocate("wt-privileged", 80)
	if err == nil {
		t.Cleanup(func() { portregistry.Release("wt-privileged") })
		require.NotEqual(t, 80, p, "privileged port must not be honored")
	}
}
