#!/opt/homebrew/bin/bash
# Attended interactive Claude Code session (entrypoint cli): records the helper
# requests only an interactive session sends, the session title among them
# (run.sh, approved gaps). Fake keys, scratch HOME and CLAUDE_CONFIG_DIR,
# loopback dummies; non-essential traffic stays on, as in proxy sessions.
# Prints the command to run in a terminal, waits up to 60 minutes for
# $ROOT/done, then one line per recorded request.
# PASS: no request refused as unconfirmed, and a cli session title answered 200.
source "$(dirname "$0")/lib.sh"
fixture_init helper-cli 18590
start_stack ok
echo "In a terminal, from $ROOT, run:"
echo "env -i PATH=\"\$PATH\" TERM=\"\$TERM\" HOME=$ROOT/home CLAUDE_CONFIG_DIR=$ROOT/config DISABLE_AUTOUPDATER=1 $CLAUDE_BIN --permission-mode default --settings $ROOT/overlay.json"
echo "Type: Say hi. Wait for the reply. Then type: List the files here. Wait for the reply, then /exit."
echo "When done: touch $ROOT/done"
for _ in $(seq 1 720); do [ -e "$ROOT/done" ] && break; sleep 5; done
[ -e "$ROOT/done" ] || fail "no done file within 60 minutes"
python3 - "$ROOT/client.jsonl" <<'PY'
import json, sys

TITLE_KEYS = ["model", "messages", "system", "tools", "metadata", "max_tokens", "output_config", "stream"]


def header(record, name):
    return next((v for k, v in record["headers"].items() if k.lower() == name), "")


refused = titles = 0
for n, line in enumerate(open(sys.argv[1], encoding="utf-8"), 1):
    r = json.loads(line)
    body = json.loads(r.get("body") or "{}")
    system = body.get("system")
    first = system[0].get("text", "") if isinstance(system, list) and system else (system or "")
    print("%d %s %s %s ua=%r x-app=%r betas=%r max_tokens=%s thinking=%s output_config=%s tools=%d system=%r" % (
        n, r["path"].split("?")[0], body.get("model"), r["status"], header(r, "user-agent"), header(r, "x-app"),
        header(r, "anthropic-beta"), body.get("max_tokens"), (body.get("thinking") or {}).get("type"),
        sorted((body.get("output_config") or {}).keys()), len(body.get("tools") or []), first[:80]))
    refused += r["status"] == 400 and "client is not confirmed as native claude code" in str(r.get("error_body", "")).lower()
    titles += r["status"] == 200 and list(body) == TITLE_KEYS and first.startswith("You are Claude Code")
print("records:", sys.argv[1])
if refused or not titles:
    print("FAIL: %d requests refused, %d cli session titles answered 200" % (refused, titles))
    sys.exit(1)
print("PASS: no request refused, %d cli session titles answered 200" % titles)
PY
