#!/opt/homebrew/bin/bash
# Recovered row 115, fixture F24: the version pin through the real manager. A
# real Claude Code request at the version under test is recorded at the relay
# and replayed with only the User-Agent version changed: one patch newer and one
# older get a local 400 with zero upstream sends, on P directly and through a
# W instance's API-key
# delegate; the unchanged replay is accepted (control).
source "$(dirname "$0")/lib.sh"
fixture_init f24 18540
W_PORT=18544
start_stack ok
run_claude -p "Reply ok." --max-turns 1
sed -e "s/port: $CPA_PORT/port: $W_PORT/" -e "s#base-url: \"http://127.0.0.1:$DUMMY_PORT\"#base-url: \"http://127.0.0.1:$CPA_PORT\"#" \
	-e 's/fixture-upstream-key/fixture-proxy-key/' "$ROOT/cpa.yaml" >"$ROOT/w.yaml"
HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost \
	"$ROOT/cpa" -config "$ROOT/w.yaml" -local-model >"$ROOT/w.log" 2>&1 &
PIDS+=("$!"); sleep 2
NEWER="${CLAUDE_VERSION%.*}.$(( ${CLAUDE_VERSION##*.} + 1 ))"
OLDER="${CLAUDE_VERSION%.*}.$(( ${CLAUDE_VERSION##*.} - 1 ))"
replay() {
	python3 - "$ROOT/client.jsonl" "$1" "$2" <<'PY'
import http.client, json, re, sys
record, port, version = sys.argv[1], int(sys.argv[2]), sys.argv[3]
first = next(json.loads(l) for l in open(record) if json.loads(l)["path"].startswith("/v1/messages") and json.loads(l)["model"].startswith("claude-") and json.loads(l)["status"] == 200)
headers = {k: v for k, v in first["headers"].items() if k.lower() not in ("host", "content-length", "connection")}
headers = {k: ("Bearer fixture-proxy-key" if k.lower() == "authorization" else v) for k, v in headers.items()}
for k in list(headers):
    if k.lower() == "user-agent":
        headers[k] = re.sub(r"^claude-cli/[0-9]+\.[0-9]+\.[0-9]+", "claude-cli/" + version, headers[k])
conn = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
conn.request("POST", first["path"], body=first["body"].encode(), headers=headers)
resp = conn.getresponse(); resp.read()
print(resp.status)
PY
}
sends() { wc -l <"$ROOT/upstream.jsonl" | tr -d ' '; }
for port in "$CPA_PORT" "$W_PORT"; do
	for version in "$NEWER" "$OLDER"; do
		before=$(sends); status=$(replay "$port" "$version"); after=$(sends)
		[ "$status" = 400 ] || fail "port $port version $version got $status, want 400"
		[ "$before" = "$after" ] || fail "port $port version $version reached the upstream"
	done
	status=$(replay "$port" "$CLAUDE_VERSION")
	[ "$status" = 200 ] || fail "port $port unchanged replay got $status, want 200 (control)"
done
pass "F24: $NEWER and $OLDER refused with zero sends on P and through W; $CLAUDE_VERSION accepted"
