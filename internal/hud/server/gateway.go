package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

// The gateway host-router (plan §6): `<wt>.tilt.localhost` serves that
// worktree's current HTTP endpoint through a reverse proxy; every other host
// (tilt.localhost, bare localhost:10350, IPs) falls through to the main UI,
// unchanged. One bearer token for everything — the proxy sits ahead of the
// existing routes, which keep their own auth middleware.
//
// WebSocket upgrades pass through: httputil.ReverseProxy handles the
// 101 Switching Protocols hop for h1 upgrades natively.

const gatewayHostSuffix = ".tilt.localhost"

// gatewayWorktree extracts the worktree name from a gateway Host header
// ("<wt>.tilt.localhost"), or "" when the host is not a gateway host.
func gatewayWorktree(host string) string {
	h := host
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	h = strings.ToLower(h)
	if !strings.HasSuffix(h, gatewayHostSuffix) {
		return ""
	}
	return strings.TrimSuffix(h, gatewayHostSuffix)
}

// worktreeEndpoint returns the HTTP endpoint to serve for the given worktree,
// or nil when the worktree has no live HTTP endpoint (port-forward or LB).
//
// The lookup is the same source the UI's EndpointLinks use
// (store.ManifestTargetEndpoints), over manifests stamped with the
// tilt.dev/worktree label.
func (s *HeadsUpServer) worktreeEndpoint(worktree string) *url.URL {
	state := s.store.RLockState()
	defer s.store.RUnlockState()

	for _, mt := range state.ManifestTargets {
		if !isWorktreeManifest(mt, worktree) {
			continue
		}
		for _, link := range store.ManifestTargetEndpoints(mt) {
			u, err := url.Parse(link.URLString())
			if err != nil || u.Host == "" {
				continue
			}
			if u.Scheme == "https" {
				u.Scheme = "http"
			}
			if u.Scheme != "http" {
				continue
			}
			return u
		}
	}
	return nil
}

// isWorktreeManifest reports whether the manifest belongs to the given
// worktree (tilt.dev/worktree label).
func isWorktreeManifest(mt *store.ManifestTarget, worktree string) bool {
	labels := mt.Manifest.Labels
	if labels == nil {
		return false
	}
	return labels[v1alpha1.LabelWorktree] == worktree
}

// newGatewayProxy builds the reverse proxy for one worktree endpoint.
func newGatewayProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	orig := proxy.Director
	proxy.Director = func(req *http.Request) {
		orig(req)
		// The upstream sees a normal Host header, not the gateway host.
		req.Host = target.Host
	}
	return proxy
}

// gatewayHandler routes gateway hosts to their worktree endpoints; requests
// that are not gateway-hosted fall through to next. A gateway host whose
// worktree is unknown or has no live endpoint gets a 503 — falling through
// would serve the main UI under a worktree hostname.
func (s *HeadsUpServer) gatewayHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		worktree := gatewayWorktree(r.Host)
		if worktree == "" {
			next.ServeHTTP(w, r)
			return
		}

		target := s.worktreeEndpoint(worktree)
		if target == nil {
			http.Error(w, "no live endpoint for worktree "+worktree, http.StatusServiceUnavailable)
			return
		}

		newGatewayProxy(target).ServeHTTP(w, r)
	})
}
