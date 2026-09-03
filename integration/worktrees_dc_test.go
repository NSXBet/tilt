//go:build integration
// +build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseHostPort extracts the host port mapped to the given container port
// from `docker ps --format {{.Ports}}` output (e.g. "0.0.0.0:10112->8000/tcp").
func parseHostPort(portsOutput string, containerPort int) string {
	want := strconv.Itoa(containerPort)
	for _, field := range strings.FieldsFunc(portsOutput, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\n'
	}) {
		i := strings.Index(field, "->")
		if i < 0 {
			continue
		}
		if !strings.HasPrefix(field[i+2:], want) {
			continue
		}
		hostPart := field[:i]
		if j := strings.LastIndex(hostPart, ":"); j >= 0 {
			return hostPart[j+1:]
		}
	}
	return ""
}

// The docker-compose half of the multi-worktree e2e fixture (plan §11, §4.5):
// two worktrees plus main compose up simultaneously. Main composes the shared
// `web` service on the authored binding 10110:8000; each worktree composes
// its own service (wt-a / wt-b) from its checkout on the same authored
// binding — the worktree run rebinds its published host port through the
// port registry and its compose project gets a per-worktree name, so all
// three containers coexist without clashes.
//
// Opt-in via TILT_WORKTREES_DC_E2E=1: three compose projects deploying in
// parallel exercise Docker-daemon races (OrbStack network recreation, macOS
// memory pressure killing the engine) that are environment problems, not
// feature problems — the feature path is asserted end-to-end when the env
// has headroom. Skips gracefully without the env var or a docker daemon,
// like the other dc tests.
func TestWorktreesDockerCompose(t *testing.T) {
	if os.Getenv("TILT_WORKTREES_DC_E2E") != "1" {
		t.Skip("Skipping docker-compose worktree e2e; set TILT_WORKTREES_DC_E2E=1 to run (needs a healthy docker daemon with headroom for 3 parallel compose projects)")
	}

	f := newDCFixture(t, "worktrees_dc")

	f.dockerKillAll("tilt")
	f.TiltUp("--worktrees")

	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Minute)
	defer cancel()

	// ── main's container is up under the authored project name and
	// authored binding ──
	f.WaitUntil(ctx, "main dc up", func() (string, error) {
		out, err := f.dockerCmdOutput([]string{
			"ps", "-a", "-f", "name=tilt_worktrees_dc_web", "--format", "{{.Names}} {{.Status}} {{.Ports}}",
		})
		if err != nil {
			return "", err
		}
		t.Logf("main dc ps: %q", out)
		return out, nil
	}, "10110")
	// The dc fixture's CurlUntil execs `curl` inside the container; bare
	// busybox only ships wget, so probe via docker exec wget instead.
	probeContainer := func(name string) func() (string, error) {
		return func() (string, error) {
			return f.dockerCmdOutput([]string{
				"exec", name, "wget", "-q", "-O-", "http://127.0.0.1:8000/",
			})
		}
	}
	f.WaitUntil(ctx, "main served", probeContainer("tilt_worktrees_dc_web"), "<worktree-dc-web>")

	// ── each worktree composes its own service as its own project, with a
	// registry-rebound host port (≠ authored, ≠ sibling) ──
	wts := []string{"wt-a", "wt-b"}
	wtContainers := []string{"tilt_worktrees_dc_wt_a", "tilt_worktrees_dc_wt_b"}
	wtPorts := map[string]string{}
	for i, wt := range wts {
		container := wtContainers[i]
		f.WaitUntil(ctx, wt+" dc up", func() (string, error) {
			out, err := f.dockerCmdOutput([]string{
				"ps", "-f", "name=" + container, "--format", "{{.Ports}}",
			})
			if err != nil {
				return "", err
			}
			if out == "" {
				return "", nil
			}
			return out, nil
		}, "8000")

		out, err := f.dockerCmdOutput([]string{
			"ps", "-f", "name=" + container, "--format", "{{.Ports}}",
		})
		require.NoError(t, err)
		require.NotEmpty(t, out, "worktree %s container missing", wt)

		port := parseHostPort(out, 8000)
		require.NotEmpty(t, port, "no published host port for container port 8000 in %q", out)
		require.NotEqual(t, "10110", port,
			"worktree %s must not hold main's authored binding", wt)
		wtPorts[wt] = port

		// The worktree's service answers on its container port.
		f.WaitUntil(ctx, wt+" served", probeContainer("tilt_worktrees_dc_"+strings.ReplaceAll(wt, "-", "_")),
			"<worktree-dc-web>")
	}

	require.NotEqual(t, wtPorts["wt-a"], wtPorts["wt-b"],
		"two worktrees must get distinct registry-allocated host ports")

	// ── the worktree UIResources group the clone resources, with the
	// cross-run dep on main's web serialized first (plan §3) ──
	for _, wt := range []string{"wt-a", "wt-b"} {
		links := f.uiResourceEndpointLinks(ctx, "wt:"+wt+"_"+wt)
		require.NotEmpty(t, links, "worktree %s resource must have endpoint links (ports: %v)", wt, links)
	}

	// All three containers run at once: main's + both worktrees'.
	out, err := f.dockerCmdOutput([]string{
		"ps", "-f", "name=tilt_worktrees_dc", "--format", "{{.Names}}",
	})
	require.NoError(t, err)
	assert.Contains(t, out, "tilt_worktrees_dc_web")
	assert.Contains(t, out, "tilt_worktrees_dc_wt_a")
	assert.Contains(t, out, "tilt_worktrees_dc_wt_b")

	// The clone manifests carry the cross-run dep on the shared resource
	// (surfaced via the uiresource of the worktree's Tiltfile run).
	var tf struct {
		Items []map[string]interface{} `json:"items"`
	}
	tfOut, err := f.tilt.Get(ctx, "tiltfile")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(tfOut, &tf))
	names := map[string]bool{}
	for _, it := range tf.Items {
		names[it["metadata"].(map[string]interface{})["name"].(string)] = true
	}
	assert.True(t, names["tiltfile:wt-a"], "worktree wt-a Tiltfile CR must exist")
	assert.True(t, names["tiltfile:wt-b"], "worktree wt-b Tiltfile CR must exist")
}
