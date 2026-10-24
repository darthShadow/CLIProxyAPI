#!/usr/bin/env python3
"""Loopback recording relay between Claude Code and the scratch proxy.

Each client request is forwarded unchanged to the proxy; the relay records the
method, path, model, request headers and body, response status, and the
first 2,000 bytes of an error body as one JSON line. Usage: relay.py <record-file> <port> <proxy-port>
"""
import http.client
import http.server
import json
import sys

RECORD, PORT, PROXY = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
HOP = {"connection", "keep-alive", "transfer-encoding", "host", "content-length"}


class Relay(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def forward(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        conn = http.client.HTTPConnection("127.0.0.1", PROXY, timeout=120)
        headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP}
        conn.request(self.command, self.path, body=body, headers=headers)
        resp = conn.getresponse()
        data = resp.read()
        try:
            model = json.loads(body or b"{}").get("model", "")
        except ValueError:
            model = ""
        entry = {"method": self.command, "path": self.path, "model": model, "status": resp.status,
                 "headers": {k: ("<redacted>" if k.lower() in ("authorization", "x-api-key") else v) for k, v in self.headers.items()},
                 "error_body": data[:2000].decode("utf-8", "replace") if resp.status >= 400 else "",
                 "body": body.decode("utf-8", "replace")}
        with open(RECORD, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry) + "\n")
        self.send_response(resp.status)
        for key, value in resp.getheaders():
            if key.lower() not in HOP:
                self.send_header(key, value)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    do_GET = do_POST = do_PUT = do_DELETE = forward


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Relay).serve_forever()
