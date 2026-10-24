#!/usr/bin/env python3
"""Record Claude Code's direct HTTP/1.1 wire shape on a scratch TLS Unix socket.

Usage: cc_wire_server.py <dir>, with leaf.pem and leaf.key in that directory.
Every request gets an origin-shaped 400 without triggering a gzip fallback.
"""
import gzip
import http.client
import json
import re
import socketserver
import ssl
import sys
import threading
from pathlib import Path

ERROR = b'{"type":"error","error":{"type":"invalid_request_error","message":"offline capture"}}'
RESPONSE = (b"HTTP/1.1 400 Bad Request\r\nrequest-id: req_offlineprobe\r\n"
            b"content-type: application/json\r\nContent-Length: " + str(len(ERROR)).encode()
            + b"\r\n\r\n" + ERROR)


class Capture(socketserver.BaseRequestHandler):
    def handle(self):
        with self.server.context.wrap_socket(self.request, server_side=True) as conn:
            with conn.makefile("rb") as reader:
                while line := reader.readline():
                    method, path, version = line.decode("iso-8859-1").strip().split()
                    headers = http.client.parse_headers(reader)
                    if headers.get("Transfer-Encoding", "").lower() == "chunked":
                        chunks = []
                        while True:
                            size = int(reader.readline().split(b";", 1)[0], 16)
                            if not size:
                                while reader.readline() not in (b"\r\n", b"\n", b""):
                                    pass
                                break
                            chunks.append(reader.read(size))
                            if reader.read(2) != b"\r\n":
                                raise ValueError("invalid chunk terminator")
                        raw = b"".join(chunks)
                    else:
                        length = int(headers.get("Content-Length", "0"))
                        raw = reader.read(length)
                        if len(raw) != length:
                            raise ValueError("incomplete request body")
                    codec = headers.get("Content-Encoding") or "none"
                    text = (gzip.decompress(raw) if codec.lower() == "gzip" else raw).decode("utf-8")
                    end = text.rfind("}") + 1
                    body, padding = (text[:end], text[end:]) if end else (text, "")
                    padding_kind = ("none" if not padding else "newline-ws"
                                    if re.fullmatch(r"\n[ \t]*", padding) else "other")
                    try:
                        payload = json.loads(body or "{}")
                    except ValueError:
                        payload = {}
                    system = payload.get("system") if isinstance(payload, dict) else None
                    first = system[0].get("text", "") if isinstance(system, list) and system and isinstance(system[0], dict) else ""
                    cch = None
                    if isinstance(first, str) and first.startswith("x-anthropic-billing-header:"):
                        cch = re.search(r"(?:^|;)\s*cch=([0-9a-f]{5})(?=;|\s*$)", first.split(":", 1)[1])
                    cch_kind = "zero" if cch and cch[1] == "00000" else "filled" if cch else "absent"
                    entry = {
                        "method": method, "path": path,
                        "headers": {k: "<redacted>" if k.lower() == "authorization" else v for k, v in headers.items()},
                        "body": body, "status": 400,
                        "wire": ["codec=" + codec,
                                 "gzip-header=" + (raw[:10].hex() if codec.lower() == "gzip" else "none"),
                                 "padding=" + padding_kind, "cch=" + cch_kind],
                    }
                    with self.server.record_lock:
                        with self.server.record.open("a", encoding="utf-8") as handle:
                            handle.write(json.dumps(entry) + "\n")
                    conn.sendall(RESPONSE)
                    if headers.get("Connection", "").lower() == "close":
                        break


def main():
    root = Path(sys.argv[1])
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(root / "leaf.pem", root / "leaf.key")
    with socketserver.ThreadingUnixStreamServer(str(root / "s.sock"), Capture) as server:
        server.daemon_threads = True
        server.context = context
        server.record = root / "client.jsonl"
        server.record_lock = threading.Lock()
        server.serve_forever()


if __name__ == "__main__":
    main()
