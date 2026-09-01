// Package portregistry allocates local ports for worktree resources.
//
// Allocate(owner, requested) honors a free requested port, falls back to the
// OS (:0) when requested is 0 or outside the configured range, stays stable
// for the same owner across Tiltfile reloads, and errors when the worktree
// port_range is exhausted. Reservation is a process-wide table, not a held
// socket: the consumer (portforward reconciler, tk-zfi) binds the port after
// allocation — that is what keeps ports stable across reloads instead of
// fighting the registry for the bind. Cross-process squatting in the
// hand-off gap surfaces as resource errors via the existing portforward
// error path (plan §5); the cross-process TOCTOU race in getAvailablePort
// (internal/k8s/portforward.go:221) is out of scope for own allocations.
// See .agents/drafts/multi-worktree-parallel.md §5.
package portregistry

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

// ErrRangeExhausted is returned by Allocate when every port in the configured
// range is taken. Callers surface it through the existing resource-error path
// (plan §5: collisions surface as resource errors).
var ErrRangeExhausted = errors.New("portregistry: port range exhausted")

const (
	// minEphemeralPort is the IANA dynamic/ephemeral floor. Ports below it are
	// privileged; a port_range must not hand them out.
	minEphemeralPort = 1024
	// maxPort is the TCP port ceiling.
	maxPort = 65535
)

// registry is the process-wide singleton. Tilt runs one engine per process
// (plan §1), so a package-level registry under a mutex is the whole API
// surface — no DI ceremony for a single-process concern.
type registry struct {
	mu      sync.Mutex
	byOwner map[string]int // owner -> allocated port
	byPort  map[int]string // port -> owner
	// rangeMin/rangeMax come from worktree_config port_range (plan §3);
	// (0, 0) means no range: requested ports are honored, fallback is OS :0.
	rangeMin, rangeMax int
}

var reg = &registry{
	byOwner: map[string]int{},
	byPort:  map[int]string{},
}

// SetPortRange configures the port_range from worktree_config. Called on
// Tiltfile load; (0, 0) or no call at all leaves OS-fallback mode. The range
// applies to subsequent allocations only — existing allocations are never
// revoked by a reload (ports do not churn on save — task contract).
func SetPortRange(min, max int) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.rangeMin, reg.rangeMax = min, max
}

// Allocate returns the port for owner.
//
//   - First call for an owner reserves a port; later calls return the same
//     port (stable across Tiltfile reloads — a reload re-declares the same
//     resources for the same worktree).
//   - requested != 0 is honored when it is free and inside the configured
//     range (any port is in range when none is set); an out-of-range or
//     taken requested port falls through to the fallback allocation.
//   - The fallback picks the first free port in the configured range; with
//     no range it defers to the OS (:0).
//   - Every port in the range taken → ErrRangeExhausted, never a port
//     outside the range.
//
// The port is free at return: the consumer binds it (transient probe socket
// closed inside Allocate). Within this process the reservation table is the
// sole authority, so no other Allocate call can hand the same port out.
func Allocate(owner string, requested int) (int, error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	if port, ok := reg.byOwner[owner]; ok {
		return port, nil
	}

	port, err := reg.pick(requested)
	if err != nil {
		return 0, err
	}

	reg.byOwner[owner] = port
	reg.byPort[port] = owner
	return port, nil
}

// Release drops owner's reservation so the port can be handed out again.
// Idempotent. Called when a worktree's resource goes away (tilt down, clone
// pruning) so its port returns to the pool.
func Release(owner string) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	port, ok := reg.byOwner[owner]
	if !ok {
		return
	}
	delete(reg.byOwner, owner)
	delete(reg.byPort, port)
}

// pick returns a free port: the requested one when usable, else the first
// free port in the configured range, else an OS-assigned one. The probe
// listener is closed before returning — the consumer binds the port after
// allocation. reg.mu held.
func (r *registry) pick(requested int) (int, error) {
	if requested != 0 && r.requestedInRange(requested) {
		if _, reserved := r.byPort[requested]; !reserved {
			if l, err := net.Listen("tcp", fmt.Sprintf(":%d", requested)); err == nil {
				_ = l.Close()
				return requested, nil
			}
		}
	}

	if r.rangeMin == 0 || r.rangeMax == 0 {
		for try := 0; try < 16; try++ {
			l, err := net.Listen("tcp", ":0")
			if err != nil {
				return 0, err
			}
			port := l.Addr().(*net.TCPAddr).Port
			_ = l.Close()
			if _, reserved := r.byPort[port]; !reserved {
				return port, nil
			}
			// OS handed back a port this registry reserved; retry — with
			// 16 attempts the chance of colliding every time is negligible.
		}
		return 0, fmt.Errorf("%w: OS fallback kept returning reserved ports", ErrRangeExhausted)
	}

	if r.rangeMin == 0 || r.rangeMax == 0 {
		l, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		return port, nil
	}

	for port := r.rangeMin; port <= r.rangeMax; port++ {
		if _, taken := r.byPort[port]; taken {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue // squatted by a non-Tilt process; try the next one
		}
		_ = l.Close()
		return port, nil
	}
	return 0, fmt.Errorf("%w: %d-%d", ErrRangeExhausted, r.rangeMin, r.rangeMax)
}

// requestedInRange reports whether a nonzero requested port is acceptable:
// in the configured range when one is set, otherwise any non-privileged
// port.
func (r *registry) requestedInRange(port int) bool {
	if port < minEphemeralPort || port > maxPort {
		return false
	}
	if r.rangeMin == 0 || r.rangeMax == 0 {
		return true
	}
	return port >= r.rangeMin && port <= r.rangeMax
}
