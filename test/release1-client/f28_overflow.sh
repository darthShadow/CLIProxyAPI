#!/opt/homebrew/bin/bash
# Recovered row 119, fixture F28: with R1-D, a ChatGPT context overflow makes
# Claude Code send x-claude-code-compaction when auto-compaction is on and fail
# with a clear message when it is off.
source "$(dirname "$0")/lib.sh"
fixture_init f28 18510
start_stack overflow
seed_conversation() {
	SESSION_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
	run_claude -p "Reply ok." --model gpt-6.1-sol --max-turns 2 --session-id "$SESSION_ID"
	grep -qx 'ok' "$ROOT/claude.out" || fail "F28 setup: no completed turn before overflow (see $ROOT/claude.out)"
	cat "$ROOT/claude.out" >"$ROOT/$1-bootstrap.out"
	cat "$ROOT/claude.err" >"$ROOT/$1-bootstrap.err"
	echo "F28 setup: $1 conversation has a completed turn; session $SESSION_ID"
}
seed_conversation on
run_claude -p "Reply ok again." --model gpt-6.1-sol --max-turns 2 --resume "$SESSION_ID"
compaction=$(records "$ROOT/client.jsonl" 'any(k.lower() == "x-claude-code-compaction" for k in r["headers"])' | grep -c True || true)
[ "$compaction" -ge 1 ] || fail "no x-claude-code-compaction header after the overflow with auto-compaction on"
python3 - "$ROOT/overlay.json" <<'PY'
import json, sys
o = json.load(open(sys.argv[1])); o["env"]["DISABLE_AUTO_COMPACT"] = "1"; o["autoCompactEnabled"] = False
json.dump(o, open(sys.argv[1], "w"))
PY
seed_conversation off
run_claude -p "Reply ok again." --model gpt-6.1-sol --max-turns 2 --resume "$SESSION_ID"
grep -qi 'prompt is too long' "$ROOT/claude.out" "$ROOT/claude.err" || fail "overflow with compaction off did not show prompt is too long"
pass "F28: overflow compacts when on and fails clearly when off"
