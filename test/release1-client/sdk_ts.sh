#!/opt/homebrew/bin/bash
# Agent SDK fixture (sdk-ts). The real Agent SDK (SDK_DIR, 0.3.290) runs
# CLAUDE_BIN through pathToClaudeCodeExecutable, as T3 Code and pi-claude-bridge
# run Claude Code, and sends one prompt over streaming input. The scratch
# settings name the Haiku model of the shared settings, so the session title is
# a Haiku helper request without claude-code-20250219. Non-essential traffic
# stays on, as in proxy sessions: with CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC
# set, the binary sends no session title. HTTPS_PROXY and HTTP_PROXY name a
# closed loopback port, so a proxied connection off 127.0.0.1 fails; NO_PROXY
# keeps the loopback relay direct.
# Optional arguments: CLAUDE_CODE_ENTRYPOINT label and port base (default 18570).
# PASS: every request answered 200, and one main turn and one session-title
# request, each forwarded to the dummy once with its User-Agent, X-App, and
# betas kept (the proxy answers count_tokens itself). FAIL: a refusal, a
# missing or extra main turn or title on either side, a changed forwarded
# identity, or a request that does not use the expected entrypoint.
source "$(dirname "$0")/lib.sh"
SDK_DIR="${SDK_DIR:-/tmp/claude-501/cliproxy-migration/sdk-pack/package}"
entrypoint="${1:-}"
port_base="${2:-18570}"
fixture_name=sdk_ts
entrypoint_env=()
if [ -n "$entrypoint" ]; then
	fixture_name="sdk_ts_${entrypoint//-/_}"
	entrypoint_env=("CLAUDE_CODE_ENTRYPOINT=$entrypoint")
fi
# fixture_init sdk_ts 18570
fixture_init "$fixture_name" "$port_base"
python3 - "$ROOT/config/settings.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as handle:
    settings = json.load(handle)
settings["env"]["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "claude-haiku-5-5[1m]"
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(settings, handle)
PY
start_stack ok
# env -i PATH="$PATH" HOME="$ROOT/home" CLAUDE_CONFIG_DIR="$ROOT/config" \
env -i PATH="$PATH" HOME="$ROOT/home" CLAUDE_CONFIG_DIR="$ROOT/config" "${entrypoint_env[@]}" \
	DISABLE_AUTOUPDATER=1 HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost \
	node "$FIXTURE_DIR/sdk_ts_driver.mjs" "$SDK_DIR/sdk.mjs" "$CLAUDE_BIN" "$ROOT/overlay.json" >"$ROOT/claude.out" 2>"$ROOT/claude.err" || true
set +e
# python3 - "$ROOT/client.jsonl" "$ROOT/upstream.jsonl" <<'PY'
python3 - "$ROOT/client.jsonl" "$ROOT/upstream.jsonl" "$entrypoint" <<'PY'
import json, sys

entrypoint = sys.argv[3] or "sdk-ts"


def load(path):
    try:
        return [json.loads(line) for line in open(path, encoding="utf-8")]
    except FileNotFoundError:
        return []


def header(record, name):
    return next((v for k, v in record["headers"].items() if k.lower() == name), "")


def betas(record):
    return {b.strip() for b in header(record, "anthropic-beta").split(",") if b.strip()}


def kind(record):
    if "count_tokens" in record["path"]:
        return "count_tokens"
    output = json.loads(record["body"] or "{}").get("output_config") or {}
    if ((output.get("format") or {}).get("schema") or {}).get("required") == ["title"]:
        return "title"
    if header(record, "x-claude-code-request-class") == "main":
        return "main"
    return "other"


client = []
for line, r in enumerate(load(sys.argv[1]), 1):
    if r["method"] != "POST" or not r["path"].startswith("/v1/messages"):
        continue
    client.append(r)
    print("client line %d %s %s %s ua=%r" % (line, kind(r), r["status"], r["path"], header(r, "user-agent")))
upstream = [r for r in load(sys.argv[2]) if r["method"] == "POST" and r["path"].startswith("/v1/messages")]
problems = []
# if any("(external, sdk-ts, agent-sdk/" not in header(r, "user-agent") for r in client):
if any("(external, %s, agent-sdk/" % entrypoint not in header(r, "user-agent") for r in client):
    # problems.append("a request is not sdk-ts")
    problems.append("a request is not %s" % entrypoint)
refused = [r for r in client if r["status"] != 200]
if refused:
    problems.append("%d requests not answered 200, first: %s" % (len(refused), refused[0]["error_body"][:200]))
# count_tokens stays out of the forwarding check: the proxy answers it itself
# for a loopback upstream, so it never reaches the dummy.
for want in ("main", "title"):
    sent = [r for r in client if kind(r) == want]
    forwarded = [r for r in upstream if kind(r) == want]
    if len(sent) != 1 or len(forwarded) != 1:
        problems.append("%s: %d sent, %d forwarded, want 1 and 1" % (want, len(sent), len(forwarded)))
    elif (header(forwarded[0], "user-agent") != header(sent[0], "user-agent")
          or header(forwarded[0], "x-app") != header(sent[0], "x-app")
          or not betas(sent[0]) <= betas(forwarded[0])):
        problems.append("%s: forwarded with another User-Agent or X-App, or without a client beta" % want)
for problem in problems:
    # print("problem: " + problem)
    print("problem: " + (sys.argv[3] + ": " if sys.argv[3] else "") + problem)
sys.exit(1 if problems else 0)
PY
rc=$?
set -e
# [ "$rc" = 0 ] || fail "sdk_ts: see the problem lines and $ROOT/client.jsonl"
[ "$rc" = 0 ] || fail "sdk_ts${entrypoint:+ $entrypoint}: see the problem lines and $ROOT/client.jsonl"
# pass "sdk_ts: one sdk-ts main turn and one session title, both answered and forwarded with identity kept"
pass "sdk_ts: one ${entrypoint:-sdk-ts} main turn and one session title, both answered and forwarded with identity kept"
