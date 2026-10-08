#!/opt/homebrew/bin/bash
# Recovered row 98, fixture F6: billing pass-through through P and through W to
# P, streaming and not, and the in-stream ChatGPT failure frame (decision lines
# 34, 37, 42). Pass: the client sees the upstream status and body unchanged; one
# upstream send per client request; in-stream, the frame carries the upstream
# error object unchanged.
source "$(dirname "$0")/lib.sh"
fixture_init f6 18490
W_PORT=18494
check_direct() {
	local port="$1" label="$2" status body
	for stream in false true; do
		status=$(curl -s -o "$ROOT/$label-$stream.body" -w '%{http_code}' -H 'Authorization: Bearer fixture-proxy-key' \
			-H 'Content-Type: application/json' -H 'User-Agent: claude-cli/2.1.285 (external, cli)' -H 'anthropic-beta: claude-code-20250219' \
			-d "{\"model\":\"gpt-6.1-sol\",\"max_tokens\":8,\"stream\":$stream,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}" \
			"http://127.0.0.1:$port/v1/messages")
		[ "$status" = 402 ] || fail "$label stream=$stream status $status, want 402"
		grep -q '"fixture_field":1' "$ROOT/$label-$stream.body" || fail "$label stream=$stream body changed: $(head -c 300 "$ROOT/$label-$stream.body")"
	done
}
start_stack billing402
check_direct "$CPA_PORT" P
sed -e "s/port: $CPA_PORT/port: $W_PORT/" -e "s#http://127.0.0.1:$DUMMY_PORT/v1#http://127.0.0.1:$CPA_PORT/v1#" \
	-e "s#base-url: \"http://127.0.0.1:$DUMMY_PORT\"#base-url: \"http://127.0.0.1:$CPA_PORT\"#" \
	-e 's/fixture-upstream-key/fixture-proxy-key/' "$ROOT/cpa.yaml" >"$ROOT/w.yaml"
"$ROOT/cpa" -config "$ROOT/w.yaml" -local-model >"$ROOT/w.log" 2>&1 &
PIDS+=("$!"); sleep 2
check_direct "$W_PORT" W
sends=$(records "$ROOT/upstream.jsonl" 'r["path"].endswith("/responses")' | grep -c True || true)
[ "$sends" = 4 ] || fail "upstream sends $sends, want 4 (one per client request, no retry or account move)"
fixture_stop; PIDS=()
fixture_init f6s 18500
start_stack codex_instream
status=$(curl -s -N -o "$ROOT/instream.body" -w '%{http_code}' -H 'Authorization: Bearer fixture-proxy-key' -H 'Content-Type: application/json' \
	-H 'User-Agent: claude-cli/2.1.285 (external, cli)' -H 'anthropic-beta: claude-code-20250219' \
	-d '{"model":"gpt-6.1-sol","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}' "http://127.0.0.1:$CPA_PORT/v1/messages")
grep -q '^event: error' "$ROOT/instream.body" || fail "no event: error frame after stream start"
grep -q '"error":{"type":"billing_error","code":"payment_required","message":"fixture renewal needed","fixture_field":1}' "$ROOT/instream.body" \
	|| fail "in-stream frame does not carry the upstream error object unchanged: $(grep -A1 'event: error' "$ROOT/instream.body")"
run_claude -p "Reply ok." --model gpt-6.1-sol --max-turns 1
grep -q 'fixture renewal needed' "$ROOT/claude.out" "$ROOT/claude.err" || inconclusive "Claude Code did not print the upstream message; record its output for the user"
pass "F6: 402 unchanged through P and W to P (stream and not), one send each; in-stream frame unchanged (HTTP $status)"
