#!/opt/homebrew/bin/bash
# Recovered row 100, fixture F8: real Claude Code (CLAUDE_BIN) with the proxy overlay.
# Pass: no request is refused; the main thread writes 1-hour cache markers and
# the subagent 5-minute markers; any client prompt IDs reach the upstream unchanged;
# no billing block. The added "stream": false is covered by step A2. Answers spec Questions 1-3 locally.
source "$(dirname "$0")/lib.sh"
fixture_init f8 18440
start_stack agent
run_claude -p "Use one general-purpose subagent to reply ok." --max-turns 3 --allowedTools Agent

refused=$(records "$ROOT/client.jsonl" 'r["status"] == 400 and "client is not confirmed as native claude code" in r["error_body"].lower()' | grep -c True || true)
[ "$refused" = 0 ] || fail "$refused client requests refused as unconfirmed (helper or main shape not confirmed; see $ROOT/client.jsonl)"
records "$ROOT/client.jsonl" 'r["method"]+" "+r["path"]+" "+r["model"]+" "+str(r["status"])' | sort | uniq -c
main_1h=$(records "$ROOT/upstream.jsonl" '"\"ttl\": \"1h\"" in json.dumps(json.loads(r["body"] or "{}"))' | grep -c True || true)
sub_5m=$(records "$ROOT/upstream.jsonl" '"\"ttl\": \"5m\"" in json.dumps(json.loads(r["body"] or "{}")) or ("cache_control" in r["body"] and "\"ttl\"" not in r["body"])' | grep -c True || true)
billing=$(records "$ROOT/upstream.jsonl" '"x-anthropic-billing-header" in r["body"]' | grep -c True || true)
[ "$main_1h" -ge 1 ] || fail "no 1-hour cache marker on any upstream request (Question 2)"
[ "$sub_5m" -ge 1 ] || fail "no 5-minute or default-TTL marker on a subagent request"
prompt_id=$(python3 - "$ROOT/client.jsonl" "$ROOT/upstream.jsonl" <<'PYHEADER'
import collections, json, sys

def prompt_ids(path):
    values = []
    for line in open(path, encoding="utf-8"):
        record = json.loads(line)
        for key, value in record["headers"].items():
            if key.lower() == "x-claude-code-prompt-id":
                values.append(value)
    return collections.Counter(values)

sent = prompt_ids(sys.argv[1])
forwarded = prompt_ids(sys.argv[2])
missing = sent - forwarded
if missing:
    print("FAIL: x-claude-code-prompt-id not forwarded unchanged (R1-C7)", file=sys.stderr)
    sys.exit(1)
print(sum(sent.values()))
PYHEADER
) || fail "x-claude-code-prompt-id forwarding check failed (R1-C7)"
if [ "$prompt_id" = 0 ]; then
	inconclusive "F8: Claude Code sent no x-claude-code-prompt-id; forwarding is unproved (R1-C7)"
fi
[ "$billing" = 0 ] || fail "billing block present upstream (R1-C3 or CLAUDE_CODE_ATTRIBUTION_HEADER)"
pass "F8: all classes confirmed; 1h main, 5m subagent, no billing block; prompt-ID forwarding checked when sent"
