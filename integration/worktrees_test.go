//go:build integration
// +build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tilt-dev/tilt/internal/hud/server"
	"github.com/tilt-dev/tilt/internal/hud/webview"
	v1alpha1 "github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// The multi-worktree e2e fixture (plan §11): a git-worktree-shaped tree —
// a root Tiltfile plus .worktree/<name>/ checkouts — driven by
// `tilt up --worktrees` with NO Kubernetes cluster and no orchestrator at
// all (plain local_resources only). It pins the user-visible contract:
//
//   - position-based discovery: every .worktree/<name>/ with a Tiltfile is
//     a worktree run of the SAME root Tiltfile (worktree.name() injected),
//   - each worktree's resources run in parallel with main's,
//   - the HUD gateway serves <wt>.tilt.localhost:<port> at that worktree's
//     endpoint — and never the main UI or a sibling worktree,
//   - endpoint links carry the gateway URL alongside the raw localhost
//     fallback (plan §12 corporate-proxy escape hatch).
//
// Auth note: the fixture curls the HUD directly, so it runs Tilt with
// TILT_DISABLE_HUD_AUTH=1 (the integration equivalent of the bearer-token
// deployments the gateway doc describes).
func TestWorktrees(t *testing.T) {
	f := newFixture(t, "worktrees")

	// The gateway is exercised over real HTTP against the HUD server.
	f.tilt.Environ["TILT_DISABLE_HUD_AUTH"] = "1"
	port := f.tilt.port

	f.TiltUp("--worktrees")

	// The servers print nothing to the Tilt logs (the fixture's serve.sh
	// stays quiet), so liveness is asserted by the HTTP probes below.
	ctx, cancel := context.WithTimeout(f.ctx, time.Minute)
	defer cancel()

	// Wait for all three servers to come up via their raw ports: the shared
	// main-run server on 10070, and the two worktree servers somewhere in
	// the pool. Both worktree runs request serve_port=10071; whichever
	// Tiltfile loads first keeps it and the registry hands the other the
	// next free pool port — so the port→checkout mapping is load-order
	// dependent. Require the exact response SET on {10071, 10072}: both
	// checkouts served, on distinct ports, and neither stole 10070.
	f.WaitUntil(ctx, "shared server", func() (string, error) {
		return f.CurlBody("http://localhost:10070/")
	}, "shared-response")

	portsByWt := waitWorktreeServers(ctx, f)
	require.Len(t, portsByWt, 2, "both worktree servers must come up on distinct pool ports")

	// ── gateway routing: <wt>.tilt.localhost:<hud port> hits that
	// worktree's server, by body — the strongest signal the reverse proxy
	// landed on the right backend (a fall-through would serve the main UI's
	// HTML; a mis-route would name a different worktree).
	for _, tc := range []struct{ wt, want string }{
		{"wt-a", "wt-a-response"},
		{"wt-b", "wt-b-response"},
	} {
		host := fmt.Sprintf("%s.tilt.localhost:%d", tc.wt, port)
		f.WaitUntil(ctx, "gateway route "+host, func() (string, error) {
			return f.CurlBody("http://" + host + "/")
		}, tc.want)
	}

	// Bare localhost:<hud port> is the main UI, not a worktree (plan §6:
	// every other host falls through unchanged).
	_, body, err := f.Curl(fmt.Sprintf("http://localhost:%d/", port))
	require.NoError(t, err)
	assert.NotContains(t, body, "-response")

	// Unknown worktree host: 503 (falling through would serve the main UI
	// under a worktree hostname). Direct probe — fixture.Curl flags non-2xx.
	unknownHost := fmt.Sprintf("no-such-wt.tilt.localhost:%d", port)
	resp, err := http.Get("http://" + unknownHost + "/")
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	resp.Body.Close()

	// ── endpoint links: the gateway URL goes first, the raw localhost
	// fallback follows (plan §8/§12) ──
	// Engine-internal clone name: `wt:<worktree>_<name>` (plan §4.3). The
	// links are synthesized from the RUNTIME serve port, so the expected
	// port comes from the probe above, not from the authored request.
	wtAPort, ok := portsByWt["wt-a"]
	require.True(t, ok, "probe must have located wt-a's server")
	links := f.uiResourceEndpointLinks(ctx, "wt:wt-a_app")
	require.Contains(t, links, fmt.Sprintf("http://wt-a.tilt.localhost:%d/", wtAPort),
		"gateway link must be offered for the worktree resource (links: %v)", links)
	require.Contains(t, links, fmt.Sprintf("http://localhost:%d/", wtAPort),
		"raw localhost fallback must be kept for proxies that cannot resolve *.localhost (links: %v)", links)

	// The main run's shared resource keeps plain links — no gateway URL
	// (plan §8: main-run resources are returned unchanged).
	links = f.uiResourceEndpointLinks(ctx, "shared-infra")
	assert.NotContains(t, links, "tilt.localhost", "main-run resource must not gain a gateway link")
}

// The opt-in gateway port (--gateway-port) is a second listener serving the
// exact same handler as the main HUD port. This pins the contract that the
// extra port carries the full gateway host-routing — worktree hosts proxy
// to their backends, bare hosts fall through to the main UI — which is the
// prerequisite for running it on a privileged port via the one-shot
// bind-and-drop helper (docs/worktrees.md, "Gateway without a port").
func TestWorktreesGatewayPort(t *testing.T) {
	f := newFixture(t, "worktrees")
	f.tilt.Environ["TILT_DISABLE_HUD_AUTH"] = "1"
	port := f.tilt.port

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gatewayPort := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	f.TiltUp("--worktrees", "--gateway-port", strconv.Itoa(gatewayPort))

	// Worktree host on the opt-in port proxies to that worktree's backend.
	// Raw polling (no fixture.Curl): transient 502/503 while the worktree
	// servers come up must not fail the test — only the final require does.
	deadline := time.Now().Add(45 * time.Second)
	gotA := ""
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://wt-a.tilt.localhost:" + strconv.Itoa(gatewayPort) + "/")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				gotA = string(body)
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	require.Equal(t, "wt-a-response", gotA,
		"wt-a.tilt.localhost on the gateway port must proxy to wt-a's server")

	// Bare host on the opt-in port falls through to the main UI; the main
	// HUD port is untouched by the extra listener.
	for _, u := range []string{
		fmt.Sprintf("http://localhost:%d/", gatewayPort),
		fmt.Sprintf("http://localhost:%d/", port),
	} {
		resp, err := http.Get(u)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		assert.NotContains(t, string(body), "-response",
			"%s must serve the main UI, not a worktree", u)
	}
}

// waitWorktreeServers polls both pool ports until each has returned one of
// the two known worktree responses (in either order — the port→checkout
// mapping depends on which worktree's Tiltfile loads first) and returns the
// worktree → port mapping. On context timeout it returns whatever was
// identified; the caller asserts completeness.
func waitWorktreeServers(ctx context.Context, f *fixture) map[string]int {
	responses := map[string]string{
		"wt-a-response": "wt-a",
		"wt-b-response": "wt-b",
	}
	portsByWt := map[string]int{}
	for {
		for _, p := range []int{10071, 10072} {
			body, err := f.CurlBody(fmt.Sprintf("http://localhost:%d/", p))
			if err != nil {
				continue
			}
			if wt, ok := responses[body]; ok {
				portsByWt[wt] = p
			}
		}
		if len(portsByWt) == 2 {
			return portsByWt
		}
		select {
		case <-ctx.Done():
			return portsByWt
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// CurlBody fetches a URL and returns the body, tolerating transient
// connection errors (server not up yet).
func (f *fixture) CurlBody(url string) (string, error) {
	_, body, err := f.Curl(url)
	if err != nil {
		return "", err
	}
	return body, nil
}

// uiResourceEndpointLinks polls `tilt get uiresource <name>` until the
// resource exists and returns its endpoint links.
func (f *fixture) uiResourceEndpointLinks(ctx context.Context, name string) []string {
	f.t.Helper()
	var links []string
	f.WaitUntil(ctx, "uiresource "+name, func() (string, error) {
		out, err := f.tilt.Get(ctx, "uiresource", name)
		if err != nil {
			return "", fmt.Errorf("tilt get uiresource %s: %v (out: %s)", name, err, string(out))
		}
		var res v1alpha1.UIResource
		if err := json.Unmarshal(out, &res); err != nil {
			return "", err
		}
		var urls []string
		for _, l := range res.Status.EndpointLinks {
			urls = append(urls, l.URL)
		}
		if len(urls) == 0 {
			return "", fmt.Errorf("no endpoint links yet on %s", name)
		}
		links = urls
		return strings.Join(urls, "\n"), nil
	}, "localhost")
	return links
}

// The gateway host suffix is shared API surface: the HUD server's
// host-router (server.GatewayHostSuffix) and the webview's copy
// (webview.GatewayHostSuffix) must both stay ".tilt.localhost". Pin them
// so a silent change breaks this test loudly rather than every user's
// bookmarks.
func TestWorktreesGatewayHostSuffixContract(t *testing.T) {
	assert.Equal(t, ".tilt.localhost", server.GatewayHostSuffix)
	assert.Equal(t, ".tilt.localhost", webview.GatewayHostSuffix)
}
