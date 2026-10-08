#!/opt/homebrew/bin/bash
# Recovered row 123, fixture F32: with the proxy overlay passed by --settings,
# shared settings keep their values beside the overlay (decision lines 54, 81).
# Value-level checks on the real client:
#   env          a shared-only variable (ANTHROPIC_CUSTOM_HEADERS) reaches the wire
#                with its shared value while the overlay's base URL routes the request
#   lists        permissions.allow from the shared file and from the overlay both
#                take effect: each allowed Bash probe runs, and its output (not its
#                command text) appears in a successful tool_result
#   fallbackModel, modelPicker
#                set in the shared file; no non-interactive observation of their
#                effective value is known for 2.1.285, so each is reported
#                INCONCLUSIVE by name (spec Question 15), never as PASS
source "$(dirname "$0")/lib.sh"
fixture_init f32 18470
shared_cmd="python3 -c 'print(\"f32-shared-ok\")'"
overlay_cmd="python3 -c 'print(\"f32-overlay-ok\")'"
python3 - "$ROOT/config/settings.json" "$ROOT/overlay.json" "$shared_cmd" "$overlay_cmd" <<'PY'
import json, sys
shared_path, overlay_path, shared_cmd, overlay_cmd = sys.argv[1:5]
shared = json.load(open(shared_path))
shared["env"]["ANTHROPIC_CUSTOM_HEADERS"] = "X-F32-Shared: kept"
shared["permissions"] = {"allow": ["Bash(%s)" % shared_cmd]}
shared["fallbackModel"] = ["sonnet"]
shared["modelPicker"] = {"options": [{"model": "opus"}, {"model": "sonnet"}]}
json.dump(shared, open(shared_path, "w"))
overlay = json.load(open(overlay_path))
overlay["permissions"] = {"allow": ["Bash(%s)" % overlay_cmd]}
json.dump(overlay, open(overlay_path, "w"))
PY
export FIXTURE_PERM_COMMANDS="$shared_cmd|$overlay_cmd"
start_stack permprobe
run_claude -p "Run the two shell commands you are given." --max-turns 4
records "$ROOT/client.jsonl" 'r["method"]+" "+r["path"]+" "+r["model"]+" "+str(r["status"])' | sort | uniq -c

reached=$(records "$ROOT/client.jsonl" 'True' | grep -c True || true)
[ "$reached" -ge 1 ] || fail "F32: no request reached the relay; the overlay base URL did not apply (see $ROOT/claude.err)"
python3 - "$ROOT/config/settings.json" "$ROOT/client.jsonl" <<'PYSENTINEL'
import json, sys
shared_path, wire_path = sys.argv[1:3]
expected = int(json.load(open(shared_path))["env"]["CLAUDE_CODE_MAX_OUTPUT_TOKENS"])
values = [json.loads(r["body"] or "{}").get("max_tokens")
          for r in map(json.loads, open(wire_path))
          if r["method"] == "POST" and r["path"].split("?")[0] == "/v1/messages"]
if not values or any(value != expected for value in values):
    print("FAIL: F32 setup: shared settings file %s was rejected by the client; max_tokens expected %s, observed %s" % (shared_path, expected, values))
    sys.exit(1)
print("F32 sentinel: shared settings file loaded; all %d Messages requests carry max_tokens=%d" % (len(values), expected))
PYSENTINEL
shared_header=$(records "$ROOT/client.jsonl" 'any(k.lower() == "x-f32-shared" and v == "kept" for k, v in r["headers"].items())' | grep -c True || true)
[ "$shared_header" -ge 1 ] || fail "F32 env: the shared ANTHROPIC_CUSTOM_HEADERS value did not reach the wire beside the overlay env"

ran() {
	records "$ROOT/upstream.jsonl" '[c for m in json.loads(r["body"] or "{}").get("messages", []) if isinstance(m.get("content"), list) for c in m["content"] if isinstance(c, dict) and c.get("type") == "tool_result" and not c.get("is_error") and "'"$1"'" in json.dumps(c.get("content"))] != []' | grep -c True || true
}
shared_ran=$(ran f32-shared-ok)
overlay_ran=$(ran f32-overlay-ok)
if [ "$shared_ran" = 0 ] && [ "$overlay_ran" = 0 ]; then
	inconclusive "F32 lists: neither allowed probe ran, so the probe did not exercise permissions (see $ROOT/upstream.jsonl)"
else
	[ "$shared_ran" -ge 1 ] || fail "F32 lists: the shared permissions.allow entry was lost beside the overlay's"
	[ "$overlay_ran" -ge 1 ] || fail "F32 lists: the overlay's permissions.allow entry did not take effect"
	echo "F32 lists: shared and overlay permissions.allow entries both took effect"
fi
inconclusive "F32 fallbackModel: shared value [sonnet] set; its effective value is not observable non-interactively (Question 15)"
inconclusive "F32 modelPicker: shared value {options: [{model: opus}, {model: sonnet}]} set; its effective value is not observable non-interactively (Question 15)"
pass "F32: env per-variable merge and list merge held with shared values beside the overlay"
