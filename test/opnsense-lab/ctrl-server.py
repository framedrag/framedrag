#!/usr/bin/env python3
"""framedrag OPNsense lab, host-side control server.

Loopback-only. GET /phase2 swaps the feed fixture to v2 and re-runs
`framedrag update`, so the guest can trigger a genuine host-side update
mid-scenario and then watch it propagate through its own alias refresh.

argv: <port> <phase2-shell-command>
"""

import subprocess
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
PHASE2_CMD = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/phase2":
            self.send_response(404)
            self.end_headers()
            return
        r = subprocess.run(["/bin/sh", "-c", PHASE2_CMD], capture_output=True, text=True)
        body = ("ok\n" if r.returncode == 0 else "phase2 failed rc=%d\n%s%s" % (r.returncode, r.stdout, r.stderr)).encode()
        self.send_response(200 if r.returncode == 0 else 500)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        sys.stderr.write("[ctrl] /phase2 rc=%d\n%s%s" % (r.returncode, r.stdout, r.stderr))
        sys.stderr.flush()

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
