#!/opt/homebrew/bin/bash
# Recovered row 97, fixture F5 (client reaction part): Claude Code's reaction to
# a proxy 429 that carries Retry-After (the per-minute throttle shape; the
# held-session 429 of spec Question 4 has the same envelope and header, step A4
# test). The Go tests cover each stop status and the scope split (A8b, A9).
source "$(dirname "$0")/lib.sh"
fixture_init f5 18480
start_stack throttle429
run_claude -p "Reply ok." --max-turns 1
sends=$(records "$ROOT/upstream.jsonl" 'r["path"].startswith("/v1/messages")' | grep -c True || true)
client=$(records "$ROOT/client.jsonl" 'r["status"]' | sort | uniq -c)
echo "client statuses: $client"
echo "upstream sends: $sends (each one is a client retry; the proxy itself sends once per client request)"
grep -i -E 'rate|429|limit' "$ROOT/claude.out" "$ROOT/claude.err" | head -5 || true
# Predicates (review finding M6): inference reached the dummy, the client got
# the 429, and the proxy added no send of its own (request-retry 0,
# max-retry-credentials 1: one upstream send per client request at most).
client_429=$(records "$ROOT/client.jsonl" 'r["status"] == 429' | grep -c True || true)
[ "$sends" -ge 1 ] || fail "F5: no request reached the dummy upstream (the client stopped before inference; see $ROOT/claude.err)"
[ "$client_429" -ge 1 ] || fail "F5: no 429 reached the client; statuses: $client"
[ "$sends" -le "$client_429" ] || fail "F5: $sends upstream sends for $client_429 client requests answered 429 (the proxy retried)"
# Claude Code's own retry budget on a 429 has no recorded expected value for
# 2.1.285, so the count is reported for the user, never as PASS.
inconclusive "F5: Claude Code sent $client_429 requests on the proxy 429 (its retry count); compare with the client's retry budget"
pass "F5: proxy 429 reached the client after one upstream send per request"
