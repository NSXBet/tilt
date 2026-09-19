// Package worktrees auto-watches the worktree dir (worktree auto-watch):
// it re-runs worktree.Discover whenever the dir changes and dispatches the
// fresh set as store.WorktreesChangedAction. The configs controller syncs
// the worktree Tiltfile CRs against that set — creating a "tiltfile:<name>"
// CR for a worktree that appeared (which loads it exactly like a
// startup-discovered one, live reload included) and deleting the CR of one
// that vanished (whose teardown cascades through the tiltfile reconciler
// and the clone GC).
//
// Watched paths are the worktree dir itself — non-recursive, only direct
// children matter — falling back to the repo root until the dir exists, so
// the first-ever worktree is picked up too. Watching stays out of git
// metadata on purpose: discovery is position-based (plan §2), so plain
// checkouts work and `git worktree list` is never consulted.
package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tilt-dev/tilt/internal/controllers/core/filewatch/fsevent"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/internal/watch"
	"github.com/tilt-dev/tilt/pkg/logger"
)

// defaultMinRest coalesces the event burst of a worktree operation
// (`git worktree add` mkdirs then checks out) into one rescan.
const defaultMinRest = 200 * time.Millisecond

// defaultRetryDelay bounds the late-Tiltfile race: `git worktree add`
// creates the subdir before checking out files, and child-dir contents do
// not fire events on the watched dir (fs watching is non-recursive). When a
// rescan leaves a Tiltfile-less candidate dir behind, the watcher rescans on
// this cadence.
const defaultRetryDelay = 500 * time.Millisecond

// maxRetries caps the late-Tiltfile retry budget (~5s after the last real
// event) so a directory that never grows a Tiltfile stops scanning. A real
// worktree op later fires a new event, which restarts the loop.
const maxRetries = 10

// defaultHeartbeat re-checks the watched dir while watching: fs backends do
// not reliably report the watched dir's own deletion, and one stat per
// second is cheaper than a stranded watch.
const defaultHeartbeat = time.Second

// Watcher dispatches store.WorktreesChangedAction when the worktree dir
// changes. Run is the only entrypoint; one Watcher owns one goroutine.
type Watcher struct {
	st    store.RStore
	maker fsevent.WatcherMaker

	// Test knobs; zero means the defaults above.
	minRest    time.Duration
	retryDelay time.Duration
	heartbeat  time.Duration

	rootTiltfilePath string
}

func NewWatcher(st store.RStore, maker fsevent.WatcherMaker) *Watcher {
	return &Watcher{
		st:    st,
		maker: maker,
	}
}

// Run blocks until ctx is cancelled; start it in a goroutine. It arms the
// watch on the worktree dir (or the repo root until that dir exists) and
// rescans on debounced events until shutdown.
func (w *Watcher) Run(ctx context.Context, rootTiltfilePath string) {
	w.rootTiltfilePath = rootTiltfilePath
	wtDir := w.worktreeDir()
	for ctx.Err() == nil {
		if dirExists(wtDir) {
			w.watchDir(ctx, wtDir)
		} else {
			w.waitForDir(ctx, wtDir)
		}
	}
}

func (w *Watcher) worktreeDir() string {
	return filepath.Join(filepath.Dir(w.rootTiltfilePath), worktree.DefaultDir)
}

// watchDir watches the worktree dir and rescans on debounced events. It
// returns (letting Run re-arm) when the dir vanishes, the backend errors,
// or ctx is done.
func (w *Watcher) watchDir(ctx context.Context, wtDir string) {
	notify, err := w.maker([]string{wtDir}, watch.EmptyMatcher{}, logger.Get(ctx))
	if err == nil {
		err = notify.Start()
		if err != nil {
			notify.Close()
		}
	}
	if err != nil {
		// Lost a race with a delete between dirExists and the arm, or the
		// backend failed: back off, then re-arm.
		logger.Get(ctx).Debugf("worktree auto-watch: watching %q: %v", wtDir, err)
		select {
		case <-ctx.Done():
		case <-time.After(w.retryDelayDuration()):
		}
		return
	}
	defer notify.Close()

	// Rescan once at arm time. The create event that re-armed us from
	// waitForDir was consumed there — without this, a worktree whose dir
	// just appeared would wait for a second event. At plain startup the
	// rescan matches the seeded set and dispatches nothing.
	w.rescan(ctx)

	events := notify.Events()
	errs := notify.Errors()

	// Watchdog: backends do not reliably report the watched dir's own
	// deletion (FSEvents goes silent instead of emitting a remove for the
	// watched path), and a dead watch would strand the last set forever.
	// The heartbeat re-checks the dir and re-arms through Run on removal.
	heartbeat := time.NewTicker(w.heartbeatDuration())
	defer heartbeat.Stop()

	// One timer per debounce window: a fresh timer never carries a stale
	// fire, and Stop discards unfired ones.
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	var debounced <-chan time.Time

	retries := 0
	for {
		select {
		case <-ctx.Done():
			return

		case _, ok := <-events:
			if !ok {
				return
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(w.minRestDuration())
			debounced = timer.C

		case err := <-errs:
			// The backend gave up (e.g. the dir was deleted out from under
			// the watch): rescan reports the true state, then re-arm.
			logger.Get(ctx).Debugf("worktree auto-watch: %v", err)
			w.rescan(ctx)
			return

		case <-heartbeat.C:
			if !dirExists(wtDir) {
				// The whole worktree dir is gone — the rescan dispatches
				// the empty set. Re-arm on the parent.
				w.rescan(ctx)
				return
			}

		case <-debounced:
			debounced = nil
			w.rescan(ctx)
			if w.pendingDir(wtDir) {
				retries++
				if retries <= maxRetries {
					timer = time.NewTimer(w.retryDelayDuration())
					debounced = timer.C
				}
			} else {
				retries = 0
			}
			if !dirExists(wtDir) {
				// The whole worktree dir is gone — the rescan above already
				// dispatched the empty set. Re-arm on the parent.
				return
			}
		}
	}
}

// waitForDir watches the repo root until the worktree dir appears, so the
// first-ever worktree is picked up even when the dir doesn't exist at
// startup. Non-recursive watching means only the dir's own creation fires
// here; its children are picked up after re-arming.
func (w *Watcher) waitForDir(ctx context.Context, wtDir string) {
	parent := filepath.Dir(wtDir)
	notify, err := w.maker([]string{parent}, watch.EmptyMatcher{}, logger.Get(ctx))
	if err == nil {
		err = notify.Start()
		if err != nil {
			notify.Close()
		}
	}
	if err != nil {
		// Nothing watchable (the repo root itself is missing?): poll.
		logger.Get(ctx).Debugf("worktree auto-watch: watching %q: %v", parent, err)
		for ctx.Err() == nil && !dirExists(wtDir) {
			time.Sleep(w.retryDelayDuration())
		}
		return
	}
	defer notify.Close()

	for {
		select {
		case <-ctx.Done():
			return

		case _, ok := <-notify.Events():
			if !ok {
				return
			}
			// Event-path shapes differ across backends (the dir itself vs
			// something inside it); re-arm as soon as the dir exists.
			if dirExists(wtDir) {
				return
			}

		case err := <-notify.Errors():
			logger.Get(ctx).Debugf("worktree auto-watch: %v", err)
			return // Run re-arms
		}
	}
}

// rescan re-runs the position-based discovery and dispatches the new set
// when it changed. A failed discovery (e.g. a subdir named "main" conflicts
// with the implicit main worktree, plan §2) keeps the last good set and
// logs — the next event retries.
func (w *Watcher) rescan(ctx context.Context) {
	wts, err := worktree.Discover(filepath.Dir(w.rootTiltfilePath), worktree.DefaultDir)
	if err != nil {
		logger.Get(ctx).Infof("worktree auto-watch: %v", err)
		return
	}

	s := w.st.RLockState()
	current := append([]worktree.Worktree(nil), s.Worktrees...)
	w.st.RUnlockState()
	if slicesEqualWorktrees(current, wts) {
		return
	}

	w.st.Dispatch(store.WorktreesChangedAction{Worktrees: wts})
}

// pendingDir reports whether the worktree dir holds a candidate Discover
// skips for now because its Tiltfile hasn't been checked out yet — the
// late-Tiltfile race the bounded retry covers.
func (w *Watcher) pendingDir(wtDir string) bool {
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || name == "main" {
			continue
		}
		if _, err := os.Stat(filepath.Join(wtDir, name, "Tiltfile")); err != nil {
			return true
		}
	}
	return false
}

func (w *Watcher) minRestDuration() time.Duration {
	if w.minRest == 0 {
		return defaultMinRest
	}
	return w.minRest
}

func (w *Watcher) retryDelayDuration() time.Duration {
	if w.retryDelay == 0 {
		return defaultRetryDelay
	}
	return w.retryDelay
}

func (w *Watcher) heartbeatDuration() time.Duration {
	if w.heartbeat == 0 {
		return defaultHeartbeat
	}
	return w.heartbeat
}

func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// slicesEqualWorktrees compares two discovered sets. Discover sorts by name,
// so both sides are ordered; the explicit compare keeps that assumption
// local instead of spread across callers.
func slicesEqualWorktrees(a, b []worktree.Worktree) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
