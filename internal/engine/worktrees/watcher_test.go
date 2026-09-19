package worktrees

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/controllers/core/filewatch/fsevent"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/tiltfile/worktree"
	"github.com/tilt-dev/tilt/internal/watch"
	"github.com/tilt-dev/tilt/pkg/logger"
)

// Acceptance tests for the worktree auto-watch: fs events on the worktree
// dir re-run the position-based discovery and dispatch the fresh set into
// the engine (store.WorktreesChangedAction). The tests drive a fake fs
// watcher (fsevent.FakeMultiWatcher) against a real store Loop, with the
// debounce/retry knobs dialed down for speed.
//
// Contracts pinned here:
//
//   - a worktree created after startup is picked up, whether or not the
//     worktree dir existed at startup (the waitForDir re-arm rescans at arm
//     time — the dir-create event itself was consumed by the re-arm),
//   - a worktree whose dir is removed is dropped from the set,
//   - a subdir whose Tiltfile has not been checked out yet (the
//     `git worktree add` late-Tiltfile race) is picked up by the bounded
//     retry without any further fs events,
//   - a failed discovery (subdir "main" conflicts with the implicit main
//     worktree) keeps the last good set,
//   - hidden dirs are never worktrees.

type watcherFixture struct {
	t       *testing.T
	root    string
	st      *store.Store
	fmw     *fsevent.FakeMultiWatcher
	watcher *Watcher

	cancel   context.CancelFunc
	storeErr chan error
}

// newWatcherTestStore returns a store whose reducer applies
// WorktreesChangedAction — the same state effect as the upper reducer,
// without importing internal/engine (the watcher lives under it).
func newWatcherTestStore() *store.Store {
	return store.NewStore(store.Reducer(func(ctx context.Context, s *store.EngineState, action store.Action) {
		if a, ok := action.(store.WorktreesChangedAction); ok {
			s.Worktrees = append(s.Worktrees[:0], a.Worktrees...)
		}
	}), store.LogActionsFlag(false))
}

func newWatcherFixture(t *testing.T, withWorktreeDir bool) *watcherFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "Tiltfile"), []byte("# main"), 0644))
	if withWorktreeDir {
		require.NoError(t, os.MkdirAll(filepath.Join(root, worktree.DefaultDir), 0777))
	}

	st := newWatcherTestStore()
	ctx, cancel := context.WithCancel(context.Background())
	// The watcher logs discovery failures via logger.Get(ctx).
	ctx = logger.WithLogger(ctx, logger.NewTestLogger(io.Discard))
	f := &watcherFixture{
		t:        t,
		root:     root,
		st:       st,
		fmw:      fsevent.NewFakeMultiWatcher(),
		cancel:   cancel,
		storeErr: make(chan error, 1),
	}
	go func() { f.storeErr <- st.Loop(ctx) }()

	f.watcher = NewWatcher(st, f.fmw.NewSub)
	f.watcher.minRest = 10 * time.Millisecond
	f.watcher.retryDelay = 10 * time.Millisecond
	go f.watcher.Run(ctx, filepath.Join(root, "Tiltfile"))

	t.Cleanup(cancel)
	return f
}

func (f *watcherFixture) wtPath(name string) string {
	return filepath.Join(f.root, worktree.DefaultDir, name)
}

// makeWorktree writes a worktree checkout: a subdir with a Tiltfile.
func (f *watcherFixture) makeWorktree(name string) {
	f.t.Helper()
	require.NoError(f.t, os.MkdirAll(f.wtPath(name), 0777))
	require.NoError(f.t, os.WriteFile(filepath.Join(f.wtPath(name), "Tiltfile"), []byte("# wt"), 0644))
}

// seedState overwrites EngineState.Worktrees — the startup-discovery seam.
func (f *watcherFixture) seedState(wts []worktree.Worktree) {
	f.t.Helper()
	s := f.st.LockMutableStateForTesting()
	s.Worktrees = append(s.Worktrees[:0], wts...)
	f.st.UnlockMutableState()
}

func (f *watcherFixture) worktreeNames() []string {
	s := f.st.RLockState()
	wts := append([]worktree.Worktree(nil), s.Worktrees...)
	f.st.RUnlockState()
	names := make([]string, len(wts))
	for i, wt := range wts {
		names[i] = wt.Name
	}
	return names
}

// emitUntil fires fs events for path (retrying — the watcher arms
// asynchronously, and pre-arm events are dropped) until cond holds.
func (f *watcherFixture) emitUntil(path string, cond func() bool, msg string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case f.fmw.Events <- watch.NewFileEvent(path):
		default:
		}
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("%s (timed out; worktrees=%v)", msg, f.worktreeNames())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f *watcherFixture) waitFor(cond func() bool, msg string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("%s (timed out; worktrees=%v)", msg, f.worktreeNames())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// emit fires one fs event for path (best effort — dropped when the watcher
// has not armed yet or its queue is full).
func (f *watcherFixture) emit(path string) {
	select {
	case f.fmw.Events <- watch.NewFileEvent(path):
	default:
	}
}

// settle waits long enough for pending events (10ms debounce) and retries
// (10ms cadence) to be fully processed, for assertions on what did NOT
// change.
func (f *watcherFixture) settle() {
	time.Sleep(400 * time.Millisecond)
}

func (f *watcherFixture) hasWorktree(name string) bool {
	for _, n := range f.worktreeNames() {
		if n == name {
			return true
		}
	}
	return false
}

// A worktree created after startup is picked up when the worktree dir
// already existed.
func TestWatcher_PicksUpNewWorktree(t *testing.T) {
	f := newWatcherFixture(t, true)

	f.makeWorktree("feat-a")
	f.emitUntil(f.wtPath("feat-a"), func() bool { return f.hasWorktree("feat-a") },
		"worktree created after startup must be picked up")
	require.Equal(t, []string{"feat-a"}, f.worktreeNames())
}

// A worktree created when the worktree dir itself did not exist at startup:
// the watcher must re-arm from the parent onto the dir and pick it up.
func TestWatcher_WorktreeDirCreatedAfterStartup(t *testing.T) {
	f := newWatcherFixture(t, false)

	f.makeWorktree("feat-a")
	f.emitUntil(f.wtPath("feat-a"), func() bool { return f.hasWorktree("feat-a") },
		"first-ever worktree (dir created after startup) must be picked up")
}

// A worktree whose dir is removed is dropped from the set.
func TestWatcher_RemovesDeletedWorktree(t *testing.T) {
	f := newWatcherFixture(t, true)
	f.makeWorktree("feat-a")
	f.makeWorktree("feat-b")
	f.seedState([]worktree.Worktree{
		{Name: "feat-a", Dir: f.wtPath("feat-a")},
		{Name: "feat-b", Dir: f.wtPath("feat-b")},
	})
	f.waitFor(func() bool { return len(f.worktreeNames()) == 2 }, "seeded set intact")

	require.NoError(t, os.RemoveAll(f.wtPath("feat-b")))
	f.emitUntil(f.wtPath("feat-b"), func() bool { return !f.hasWorktree("feat-b") },
		"removed worktree must leave the set")
	require.Equal(t, []string{"feat-a"}, f.worktreeNames())
}

// The whole worktree dir is removed while watched, with no fs event for it:
// backends do not reliably report the watched dir's own deletion (FSEvents
// goes silent instead of emitting a remove for the watched path), so the
// heartbeat must notice and drop the set. Regression for the stranded-watch
// bug the first integration run of the auto-watch hit.
func TestWatcher_WorktreeDirRemovedWhileWatched(t *testing.T) {
	f := newWatcherFixture(t, true)
	// Fast heartbeat: the removal arrives with no event to react to.
	f.watcher.heartbeat = 20 * time.Millisecond

	f.makeWorktree("feat-a")
	f.emitUntil(f.wtPath("feat-a"), func() bool { return f.hasWorktree("feat-a") },
		"worktree created after startup must be picked up")

	require.NoError(t, os.RemoveAll(filepath.Join(f.root, worktree.DefaultDir)))
	f.waitFor(func() bool { return len(f.worktreeNames()) == 0 },
		"removing the whole worktree dir must drop the set (no event, heartbeat only)")

	// …and the watcher re-arms on the parent: the next worktree is picked up.
	f.makeWorktree("feat-late")
	f.emitUntil(f.wtPath("feat-late"), func() bool { return f.hasWorktree("feat-late") },
		"worktree created after dir removal must be picked up (re-armed)")
}

// The late-Tiltfile race: `git worktree add` creates the subdir before
// checking out files, and child-dir contents do not fire events on the
// watched dir. One event, then a Tiltfile appearing later — the bounded
// retry must pick it up with no further events.
func TestWatcher_LateTiltfileRetry(t *testing.T) {
	f := newWatcherFixture(t, true)
	// A slower retry cadence keeps the retry budget (~10 x 50ms) alive
	// across the settling sleep below.
	f.watcher.retryDelay = 50 * time.Millisecond

	require.NoError(t, os.MkdirAll(f.wtPath("feat-a"), 0777))
	// Exactly one event: everything after this must come from the retry.
	f.fmw.Events <- watch.NewFileEvent(f.wtPath("feat-a"))

	// Give the event's rescan and several retries time to run: no Tiltfile
	// yet, so the set must stay empty.
	time.Sleep(200 * time.Millisecond)
	require.False(t, f.hasWorktree("feat-a"),
		"a Tiltfile-less dir must not become a worktree (worktrees=%v)", f.worktreeNames())

	require.NoError(t, os.WriteFile(filepath.Join(f.wtPath("feat-a"), "Tiltfile"), []byte("# wt"), 0644))
	f.waitFor(func() bool { return f.hasWorktree("feat-a") },
		"the late Tiltfile must be picked up by the retry rescan")
}

// A failed discovery keeps the last good set: a subdir named "main"
// conflicts with the implicit main worktree (plan §2) and must not wipe the
// discovered set.
func TestWatcher_DiscoveryErrorKeepsLastSet(t *testing.T) {
	f := newWatcherFixture(t, true)
	f.makeWorktree("feat-a")
	f.emitUntil(f.wtPath("feat-a"), func() bool { return f.hasWorktree("feat-a") },
		"worktree created after startup must be picked up")

	require.NoError(t, os.MkdirAll(f.wtPath("main"), 0777))
	require.NoError(t, os.WriteFile(filepath.Join(f.wtPath("main"), "Tiltfile"), []byte("# wt"), 0644))
	f.emit(f.wtPath("main"))
	f.settle()

	require.Equal(t, []string{"feat-a"}, f.worktreeNames(),
		"a failed discovery must keep the last good set")
}

// Hidden dirs are never worktrees (position-based discovery rule).
func TestWatcher_HiddenDirSkipped(t *testing.T) {
	f := newWatcherFixture(t, true)

	require.NoError(t, os.MkdirAll(filepath.Join(f.root, worktree.DefaultDir, ".tmp-x"), 0777))
	require.NoError(t, os.WriteFile(filepath.Join(f.root, worktree.DefaultDir, ".tmp-x", "Tiltfile"), []byte("# wt"), 0644))
	f.emit(filepath.Join(f.root, worktree.DefaultDir, ".tmp-x"))
	f.settle()

	require.Empty(t, f.worktreeNames(), "hidden dirs must not become worktrees")
}
