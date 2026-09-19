//go:build integration
// +build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The worktree auto-watch e2e: worktrees created — and removed — while
// `tilt up --worktrees` runs must be picked up and torn down without a
// restart.
//
// The fixture starts from a root with NO .worktree/ dir at all, so it also
// covers the watcher's first-worktree path (re-arming from the repo root
// once the dir appears). Everything lives in the fixture's own temp dir —
// no repo fixture files are touched.
func TestWorktreesLivePickup(t *testing.T) {
	f := newFixture(t, "")

	require.NoError(t, os.WriteFile(f.testDirPath("Tiltfile"), []byte(liveRootTiltfile), 0644))
	require.NoError(t, os.WriteFile(f.testDirPath("serve.sh"), []byte(serveScript), 0755))

	f.TiltUp("--worktrees")

	ctx, cancel := context.WithTimeout(f.ctx, 2*time.Minute)
	defer cancel()

	// Main probe up = Tiltfile loaded and the engine healthy.
	f.WaitUntil(ctx, "main probe", func() (string, error) {
		return f.CurlBody("http://localhost:10080/")
	}, "main-response")

	// ── a worktree created while tilt runs ──
	wtDir := f.testDirPath(".worktree/wt-live")
	require.NoError(t, os.MkdirAll(wtDir, 0777))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "Tiltfile"), []byte(wtPointerTiltfile), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "serve.sh"), []byte(serveScript), 0755))

	// The worktree run appears: its Tiltfile CR is created (auto-watch →
	// configs sync), the loader runs, and the resource instantiates —
	// exactly like a startup-discovered worktree.
	requireUIResourceEventually(ctx, f, "wt:wt-live_app", true)

	// …and its server serves from the worktree's own pool range.
	f.WaitUntil(ctx, "worktree server", func() (string, error) {
		return f.CurlBody("http://localhost:10081/")
	}, "wt-live-response")

	// ── a worktree removed while tilt runs ──
	require.NoError(t, os.RemoveAll(f.testDirPath(".worktree")))

	// The worktree's Tiltfile CR is deleted (sync delete side), which tears
	// down the run: owned objects (the UIResource among them) deleted, the
	// run's manifests dropped from engine state, clones pruned by the GC.
	requireUIResourceEventually(ctx, f, "wt:wt-live_app", false)

	// The removed worktree's server process is torn down too.
	requireServerDown(ctx, f, "http://localhost:10081/")
}

// requireUIResourceEventually polls `tilt get uiresource <name>` until the
// resource's presence matches want.
func requireUIResourceEventually(ctx context.Context, f *fixture, name string, want bool) {
	f.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		_, err := f.tilt.Get(ctx, "uiresource", name)
		present := err == nil
		if present == want {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("uiresource %q presence=%v, want %v (last err: %v)", name, present, want, err)
		}
		select {
		case <-f.activeTiltDone():
			f.t.Fatalf("Tilt died while waiting: %v", f.activeTiltErr())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// requireServerDown polls until the URL stops responding.
func requireServerDown(ctx context.Context, f *fixture, url string) {
	f.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		if _, _, err := f.Curl(url); err != nil {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("server at %s never stopped responding", url)
		}
		select {
		case <-f.activeTiltDone():
			f.t.Fatalf("Tilt died while waiting: %v", f.activeTiltErr())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

const liveRootTiltfile = `# -*- mode: Python -*-

# Live-pickup fixture: the SAME root Tiltfile is re-executed for every
# worktree discovered in .worktree/ (see TestWorktreesLivePickup). Pool
# range 10081-10090 stays clear of the static worktrees fixture.
worktree_config(port_range=(10081, 10090))

local_resource('main-probe',
               cmd='true',
               serve_cmd='./serve.sh main 10080',
               serve_port=10080,
               scope='main')

local_resource('app',
               cmd='true',
               serve_cmd='./serve.sh ' + worktree.name() + ' $TILT_SERVE_PORT',
               serve_port=10081,
               scope='worktree')
`

const wtPointerTiltfile = `# -*- mode: Python -*-

# Pure-pointer checkout: the run executes the root Tiltfile re-rooted here.
load_dynamic('../Tiltfile')
`

// serveScript is the fixture's tiny HTTP server (same as
// integration/worktrees/serve.sh): serves "<name>-response" on <port>.
const serveScript = `#!/bin/bash
set -euo pipefail

name="$1"
port="$2"

exec python3 - "$name" "$port" <<'PYEOF'
import http.server
import sys

name, port = sys.argv[1], int(sys.argv[2])
body = f"{name}-response".encode()


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass  # keep the fixture logs quiet


http.server.ThreadingHTTPServer(("127.0.0.1", port), H).serve_forever()
PYEOF
`
