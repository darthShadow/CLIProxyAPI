#!/opt/homebrew/bin/bash
# Direct Claude Code wire capture: scratch TLS Unix socket, no upstream.
set -euo pipefail
GATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLAUDE_BIN="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
ROOT="$(mktemp -d "${FIXTURE_TMP:-/tmp/claude-501}/r1-client-cc_wire.XXXXXX")"
mkdir "$ROOT/home" "$ROOT/config"
server_pid=""
stop() {
	if [ -n "$server_pid" ]; then
		kill "$server_pid" 2>/dev/null || true
		wait "$server_pid" 2>/dev/null || true
		server_pid=""
	fi
	rm -f "$ROOT/ca.key" "$ROOT/leaf.key"
}
trap stop EXIT
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=cc_wire scratch CA' \
	-keyout "$ROOT/ca.key" -out "$ROOT/ca.pem" >"$ROOT/openssl.log" 2>&1
openssl req -new -newkey rsa:2048 -nodes -subj '/CN=api.anthropic.com' \
	-keyout "$ROOT/leaf.key" -out "$ROOT/leaf.csr" >>"$ROOT/openssl.log" 2>&1
printf '%s\n' 'subjectAltName=DNS:api.anthropic.com' 'extendedKeyUsage=serverAuth' >"$ROOT/leaf.ext"
openssl x509 -req -in "$ROOT/leaf.csr" -CA "$ROOT/ca.pem" -CAkey "$ROOT/ca.key" \
	-set_serial 1 -days 1 -extfile "$ROOT/leaf.ext" -out "$ROOT/leaf.pem" >>"$ROOT/openssl.log" 2>&1
python3 -I "$GATE_DIR/cc_wire_server.py" "$ROOT" >"$ROOT/server.log" 2>&1 &
server_pid=$!
for ((i=0; i<100; i++)); do
	[ -S "$ROOT/s.sock" ] && break
	kill -0 "$server_pid" 2>/dev/null || break
	sleep 0.05
done
[ -S "$ROOT/s.sock" ] || { echo 'FAIL: cc_wire recorded no POST /v1/messages request'; exit 1; }
perl -e 'alarm 120; exec @ARGV' env -i PATH=/usr/bin:/bin \
	HOME="$ROOT/home" CLAUDE_CONFIG_DIR="$ROOT/config" \
	CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-PLACEHOLDER-NOT-A-TOKEN \
	HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 \
	CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 NODE_EXTRA_CA_CERTS="$ROOT/ca.pem" \
	ANTHROPIC_UNIX_SOCKET="$ROOT/s.sock" CLAUDE_CODE_GZIP_REQUEST_BODIES=1 \
	"$CLAUDE_BIN" -p "Reply with OK." </dev/null >"$ROOT/claude.out" 2>"$ROOT/claude.err" || true
stop
python3 -I - "$ROOT/client.jsonl" <<'PY'
import json, sys
from pathlib import Path
path = Path(sys.argv[1])
count = 0
if path.exists():
    with path.open(encoding="utf-8") as handle:
        count = sum(r["method"] == "POST" and r["path"].split("?")[0] == "/v1/messages" for r in map(json.loads, handle))
print("PASS: cc_wire recorded %d POST /v1/messages requests" % count if count else "FAIL: cc_wire recorded no POST /v1/messages request")
sys.exit(0 if count else 1)
PY
