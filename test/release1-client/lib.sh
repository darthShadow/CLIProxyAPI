#!/opt/homebrew/bin/bash
# Shared harness for the release 1 real-client fixtures. A fake
# proxy key, fake upstream keys, a new scratch HOME and CLAUDE_CONFIG_DIR, and
# loopback dummies. Proxy launches fence HTTP(S) traffic to loopback. Run each fixture with
# /opt/homebrew/bin/bash from projects/CLIProxyAPI after VERIFY A.
set -euo pipefail

FIXTURE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$FIXTURE_DIR/../.." && pwd)"
CLAUDE_BIN="${CLAUDE_BIN:-$HOME/.local/bin/claude}"
PIDS=()

fixture_init() {
	local name="$1" base="$2"
	ROOT="$(mktemp -d "${FIXTURE_TMP:-/tmp/claude-501}/r1-client-$name.XXXXXX")"
	DUMMY_PORT="$base" CPA_PORT=$((base + 1)) RELAY_PORT=$((base + 2))
	mkdir -p "$ROOT/home" "$ROOT/config" "$ROOT/auths"
	# A new CLAUDE_CONFIG_DIR maps to a Keychain item that does not exist.
	test -z "$(ls -A "$ROOT/config")" || { echo "STOP: config dir not new"; exit 2; }
	trap fixture_stop EXIT
	cat >"$ROOT/config/settings.json" <<JSON
{"model":"opus","env":{"CLAUDE_CODE_ATTRIBUTION_HEADER":"0","ANTHROPIC_DEFAULT_OPUS_MODEL":"claude-opus-5-5[1m]","ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-sonnet-5-5[1m]","ANTHROPIC_DEFAULT_HAIKU_MODEL":"claude-haiku-5-5[1m]","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"433000","DISABLE_AUTO_COMPACT":"1","CLAUDE_CODE_MAX_OUTPUT_TOKENS":"64000","DISABLE_AUTOUPDATER":"1","X_FIXTURE_SHARED":"kept"}}
JSON
	# The proxy overlay as cc-switch generates it from proxy-settings.json.
	cat >"$ROOT/overlay.json" <<JSON
{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:$RELAY_PORT","ANTHROPIC_AUTH_TOKEN":"fixture-proxy-key","CLAUDE_CODE_PROMPT_CACHE_TTL":"1h","CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL":"5m","ENABLE_TOOL_SEARCH":"auto","CLAUDE_CODE_GATEWAY_HINT_HEADERS":"1","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"433000","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"1000000","DISABLE_AUTO_COMPACT":"0"},"autoCompactEnabled":true}
JSON
	# The scratch proxy admits exactly the version of the binary under test.
	CLAUDE_VERSION="$(env -i PATH="$PATH" HOME="$ROOT/home" CLAUDE_CONFIG_DIR="$ROOT/config" "$CLAUDE_BIN" --version | awk '{print $1}')"
	[[ "$CLAUDE_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "STOP: no version from $CLAUDE_BIN --version"; exit 2; }
	CLAUDE_UA="claude-cli/$CLAUDE_VERSION (external, cli)"
}

fixture_stop() {
	local pid
	for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
}

start_stack() {
	local mode="${1:-ok}" extra_yaml="${2:-}"
	FIXTURE_MODE="$mode" python3 "$FIXTURE_DIR/dummy_upstream.py" "$ROOT/upstream.jsonl" "$DUMMY_PORT" &
	PIDS+=("$!")
	local management_yaml=$'remote-management:\n  disable-auto-update-panel: true'
	if [[ "$extra_yaml" == *remote-management:* ]]; then
		management_yaml=""
	fi
	cat >"$ROOT/cpa.yaml" <<YAML
host: "127.0.0.1"
port: $CPA_PORT
auth-dir: "$ROOT/auths"
api-keys:
  - "fixture-proxy-key"
request-retry: 0
max-retry-credentials: 1
passthrough-headers: true
disable-claude-cloak-mode: true
claude-code:
  disable-cloaking-model-list: true
  refuse-unconfirmed-clients: true
claude-header-defaults:
  user-agent: "$CLAUDE_UA"
claude-api-key:
  - api-key: "fixture-upstream-key"
    base-url: "http://127.0.0.1:$DUMMY_PORT"
codex-api-key:
  - api-key: "fixture-upstream-key"
    base-url: "http://127.0.0.1:$DUMMY_PORT/v1"
$management_yaml
$extra_yaml
YAML
	# CPA_BIN names one prebuilt server at a fixed path, so a firewall sees one
	# binary across fixtures; unset, each fixture builds its own.
	if [ -n "${CPA_BIN:-}" ]; then
		ln -s "$CPA_BIN" "$ROOT/cpa"
	else
		(cd "$REPO_ROOT" && go build -o "$ROOT/cpa" ./cmd/server)
	fi
	HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost \
		"$ROOT/cpa" -config "$ROOT/cpa.yaml" -local-model >"$ROOT/cpa.log" 2>&1 &
	PIDS+=("$!")
	python3 "$FIXTURE_DIR/relay.py" "$ROOT/client.jsonl" "$RELAY_PORT" "$CPA_PORT" &
	PIDS+=("$!")
	sleep 2
}

run_claude() {
	env -i PATH="$PATH" HOME="$ROOT/home" CLAUDE_CONFIG_DIR="$ROOT/config" \
		CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_AUTOUPDATER=1 \
		"$CLAUDE_BIN" --permission-mode default --settings "$ROOT/overlay.json" "$@" >"$ROOT/claude.out" 2>"$ROOT/claude.err" || true
}

# jq-free JSON-lines query: prints the Python expression for each record.
records() {
	python3 - "$1" "$2" <<'PY'
import json, sys
for line in open(sys.argv[1], encoding="utf-8"):
    r = json.loads(line)
    print(eval(sys.argv[2], {"r": r, "json": json}))
PY
}

# A fixture prints exactly one of PASS, FAIL, or INCONCLUSIVE as its result
# (spec "Tests per Patch"). Once any check has called inconclusive, pass prints
# INCONCLUSIVE instead of PASS, so an unproven check never reads as a pass.
INCONCLUSIVE_SEEN=0
inconclusive() { INCONCLUSIVE_SEEN=1; echo "INCONCLUSIVE: $*"; }
pass() {
	if [ "$INCONCLUSIVE_SEEN" = 1 ]; then
		echo "INCONCLUSIVE: $* (not PASS: a check above is INCONCLUSIVE)"
		return 0
	fi
	echo "PASS: $*"
}
fail() { echo "FAIL: $*"; exit 1; }
