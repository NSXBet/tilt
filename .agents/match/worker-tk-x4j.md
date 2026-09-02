# Task: implement tk-x4j (Gateway host-router in HUD server)

Worktree: /Users/yuri/Workdir/Nsx/tilt/.worktrees/tk-x4j (branch tk-x4j, clean at master)
Task ID: tk-x4j

Spec: HeadsUpServer (internal/hud/server/server.go:61) host-router ahead of existing routes: httputil.ReverseProxy, Host==<wt>.tilt.localhost -> that worktree HTTP endpoint (EndpointLinks / portforward status). tilt.localhost or bare host -> main UI unchanged. WebSocket upgrade passthrough (log streams). One bearer token for everything (token.go). Test: route matrix, WS upgrade through proxy.

Read /Users/yuri/Workdir/Nsx/tilt/.agents/match/worker-context.md for protocol. Sibling context: tk-zfi owns port registry + portforward wiring; endpoint data flows from EndpointLinks / portforward status.
