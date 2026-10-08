#!/opt/homebrew/bin/bash
# Recovered row 115, fixture F24: the version pin through the real manager. A
# real Claude Code 2.1.285 request is recorded at the relay and replayed with
# only the User-Agent version changed: 2.1.286 and 2.1.280 get a local 400 with
# zero upstream sends, on P directly and through a W instance's API-key
# delegate; the unchanged replay is accepted (control).
source "$(dirname "$0")/lib.sh"
fixture_init f24 18540
W_PORT=18544
start_stack ok
run_claude -p "Reply ok." --max-turns 1
sed -e "s/port: $CPA_PORT/port: $W_PORT/" -e "s#base-url: \"http://127.0.0.1:$DUMMY_PORT\"#base-url: \"http://127.0.0.1:$CPA_PORT\"#" \
	-e 's/fixture-upstream-key/fixture-proxy-key/' "$ROOT/cpa.yaml" >"$ROOT/w.yaml"
"$ROOT/cpa" -config "$ROOT/w.yaml" -local-model >"$ROOT/w.log" 2>&1 &
PIDS+=("$!"); sleep 2
replay() {
	python3 - "$ROOT/client.jsonl" "$1" "$2" <<'PY'
import http.client, json, sys
record, port, version = sys.argv[1], int(sys.argv[2]), sys.argv[3]
first = next(json.loads(l) for l in open(record) if json.loads(l)["path"].startswith("/v1/messages") and json.loads(l)["model"].startswith("claude-") and json.loads(l)["status"] == 200)
headers = {k: v for k, v in first["headers"].items() if k.lower() not in ("host", "content-length", "connection")}
headers = {k: ("Bearer fixture-proxy-key" if k.lower() == "authorization" else v) for k, v in headers.items()}
for k in list(headers):
    if k.lower() == "user-agent":
        headers[k] = headers[k].replace("2.1.285", version)
conn = http.client.HTTPConnection("127.0.0.1", port, timeout=60)
conn.request("POST", first["path"], body=first["body"].encode(), headers=headers)
resp = conn.getresponse(); resp.read()
print(resp.status)
PY
}
sends() { wc -l <"$ROOT/upstream.jsonl" | tr -d ' '; }
for port in "$CPA_PORT" "$W_PORT"; do
	for version in 2.1.286 2.1.280; do
		before=$(sends); status=$(replay "$port" "$version"); after=$(sends)
		[ "$status" = 400 ] || fail "port $port version $version got $status, want 400"
		[ "$before" = "$after" ] || fail "port $port version $version reached the upstream"
	done
	status=$(replay "$port" 2.1.285)
	[ "$status" = 200 ] || fail "port $port unchanged replay got $status, want 200 (control)"
done
pass "F24: 2.1.286 and 2.1.280 refused with zero sends on P and through W; 2.1.285 accepted"
