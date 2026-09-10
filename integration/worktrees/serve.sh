#!/bin/bash
# Tiny HTTP server for the worktrees fixtures: serves a body naming this
# checkout's worktree on the given port, forever.
#
# usage: serve.sh <name> <port>
#
# The response doubles as the routing probe: the gateway test asserts the
# body names THIS run's worktree, proving <wt>.tilt.localhost did not land
# on the main UI or a sibling worktree.
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
