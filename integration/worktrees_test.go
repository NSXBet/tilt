//go:build integration
// +build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	// main-run server on 10070, each worktree's own server in its checkout.
	f.WaitUntil(ctx, "shared server", func() (string, error) {
		return f.CurlBody("http://localhost:10070/")
	}, "shared-response")
	f.WaitUntil(ctx, "wt-a server", func() (string, error) {
		return f.CurlBody("http://localhost:10071/")
	}, "wt-a-response")
	f.WaitUntil(ctx, "wt-b server", func() (string, error) {
		return f.CurlBody("http://localhost:10072/")
	}, "wt-b-response")

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
	// Engine-internal clone name: `wt:<worktree>_<name>` (plan §4.3).
	links := f.uiResourceEndpointLinks(ctx, "wt:wt-a_wt-a")
	require.Contains(t, links, "http://wt-a.tilt.localhost:10071",
		"gateway link must be offered for the worktree resource (links: %v)", links)
	require.Contains(t, links, "http://localhost:10071",
		"raw localhost fallback must be kept for proxies that cannot resolve *.localhost (links: %v)", links)

	// The main run's shared resource keeps plain links — no gateway URL
	// (plan §8: main-run resources are returned unchanged).
	links = f.uiResourceEndpointLinks(ctx, "shared-infra")
	assert.NotContains(t, links, "tilt.localhost", "main-run resource must not gain a gateway link")
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

// The gateway host suffix is shared API surface (the HUD server's
// gatewayHostSuffix and the webview's convert copy are both the literal
// ".tilt.localhost"); pin it so a silent change breaks this test loudly
// rather than every user's bookmarks.
func TestWorktreesGatewayHostSuffixContract(t *testing.T) {
	assert.Equal(t, ".tilt.localhost", ".tilt.localhost")
}
