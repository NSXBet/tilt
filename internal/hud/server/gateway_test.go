package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

// Gateway host-router routing matrix (plan §6, .agents/drafts/multi-worktree-parallel.md):
//   Host == <wt>.tilt.localhost -> that worktree's HTTP endpoint
//   tilt.localhost / bare host  -> main UI (today's behavior)
//   WebSocket upgrade passthrough on worktree hosts.
//
// The worktree endpoint registry is an observable input: the host-router maps
// the manifest labeled tilt.dev/worktree=<wt> to its current endpoint
// (EndpointLinks / portforward status). The tests seed engine state directly
// through the store — the same source the reconcilers feed in production.

const worktreeResponseBody = "worktree-backend-response"

// setWorktreeEndpoint registers a k8s manifest labeled as belonging to the
// given worktree, with a port-forward whose endpoint is the one the gateway
// must route to.
func (f *serverFixture) setWorktreeEndpoint(worktreeName string, localPort int) {
	m := model.Manifest{Name: model.ManifestName("wt-" + worktreeName)}.
		WithLabels(map[string]string{"tilt.dev/worktree": worktreeName}).
		WithDeployTarget(model.K8sTarget{
			KubernetesApplySpec: v1alpha1.KubernetesApplySpec{
				PortForwardTemplateSpec: &v1alpha1.PortForwardTemplateSpec{
					Forwards: []v1alpha1.Forward{{LocalPort: int32(localPort), ContainerPort: 3000}},
				},
			},
		}.WithRefInjectCounts(map[string]int{}))
	mt := store.NewManifestTarget(m)
	state := f.st.LockMutableStateForTesting()
	state.UpsertManifestTarget(mt)
	f.st.UnlockMutableState()
}

func (f *serverFixture) routerHostReq(host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
	req.Host = host
	rr := httptest.NewRecorder()
	f.serv.Router().ServeHTTP(rr, req)
	return rr
}

func worktreeBackend(t *testing.T, port int) *httptest.Server {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(worktreeResponseBody))
	}))
	t.Cleanup(backend.Close)
	u, err := url.Parse(backend.URL)
	require.NoError(t, err)
	actualPort := u.Port()
	if port != 0 && actualPort != fmt.Sprint(port) {
		t.Fatalf("backend bound to port %s, test seeded state for %d", actualPort, port)
	}
	return backend
}

func TestGatewayWorktreeHostRoutesToWorktreeEndpoint(t *testing.T) {
	backend := worktreeBackend(t, 0)
	u, _ := url.Parse(backend.URL)
	port := 0
	_, err := fmt.Sscanf(u.Port(), "%d", &port)
	require.NoError(t, err)

	f := newTestFixture(t)
	f.setWorktreeEndpoint("feat-auth", port)

	rr := f.routerHostReq("feat-auth.tilt.localhost")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, worktreeResponseBody, rr.Body.String(),
		"Host <wt>.tilt.localhost must be proxied to that worktree's endpoint")
}

func TestGatewayMainUIHosts(t *testing.T) {
	f := newTestFixture(t)
	f.setWorktreeEndpoint("feat-auth", 32777)

	for _, host := range []string{"tilt.localhost", "localhost:10350", "127.0.0.1:10350"} {
		rr := f.routerHostReq(host)
		require.Equal(t, http.StatusOK, rr.Code, "host %q must be served by the main UI", host)
		require.NotContains(t, rr.Body.String(), worktreeResponseBody,
			"host %q must NOT be proxied to a worktree endpoint", host)
	}
}

func TestGatewayHostWithPortRoutesToWorktreeEndpoint(t *testing.T) {
	backend := worktreeBackend(t, 0)
	u, _ := url.Parse(backend.URL)
	port := 0
	_, err := fmt.Sscanf(u.Port(), "%d", &port)
	require.NoError(t, err)

	f := newTestFixture(t)
	f.setWorktreeEndpoint("feat-auth", port)

	// Browsers always send the port in the Host header; the worktree is
	// matched on the hostname alone.
	rr := f.routerHostReq("feat-auth.tilt.localhost:10350")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, worktreeResponseBody, rr.Body.String())
}

func TestGatewayUnknownWorktreeReturns503(t *testing.T) {
	f := newTestFixture(t)
	f.setWorktreeEndpoint("feat-auth", 32777)

	rr := f.routerHostReq("no-such-worktree.tilt.localhost")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "no-such-worktree")
}

func TestGatewayUnknownWorktreeNoManifestsReturns503(t *testing.T) {
	f := newTestFixture(t)

	// No manifests labeled with a worktree: a gateway host for an unknown
	// worktree must 503, not fall through and serve the main UI under a
	// worktree hostname.
	rr := f.routerHostReq("feat-auth.tilt.localhost")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "feat-auth")
	require.NotContains(t, rr.Body.String(), worktreeResponseBody)
}

func TestGatewayWebSocketUpgradePassthrough(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(mt, []byte("echo:"+string(msg))); err != nil {
				return
			}
		}
	}))
	defer backend.Close()
	u, err := url.Parse(backend.URL)
	require.NoError(t, err)
	port := 0
	_, err = fmt.Sscanf(u.Port(), "%d", &port)
	require.NoError(t, err)

	f := newTestFixture(t)
	f.setWorktreeEndpoint("feat-auth", port)

	// Dial through the fixture's gateway router on a real listener: the
	// websocket dialer needs an actual TCP endpoint, and the Host header
	// (not the dial URL) is what the gateway routes on.
	srv := httptest.NewServer(f.serv.Router())
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	header := http.Header{}
	header.Set("Host", "feat-auth.tilt.localhost")
	c, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		defer c.Close()
	}
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte("ping")))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := c.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, "echo:ping", string(msg))
}

// Unused imports guard for metav1/types — exercised by newTestFixture's
// Tiltfile creation; keep the references meaningful for go vet.
var _ = metav1.ObjectMeta{}
var _ = types.NamespacedName{}
