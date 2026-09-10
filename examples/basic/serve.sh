#!/bin/bash
# Usage: serve.sh <name> <port>
# A tiny local HTTP server whose response identifies the active resource.
set -euo pipefail

name="$1"
port="$2"

exec python3 - "$name" "$port" <<'PYEOF'
import http.server
import sys

name, port = sys.argv[1], int(sys.argv[2])
body = (name + " example\n").encode()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
PYEOF
