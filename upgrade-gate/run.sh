#!/opt/homebrew/bin/bash
# Claude Code upgrade gate. From projects/CLIProxyAPI, sandbox off:
#   /opt/homebrew/bin/bash upgrade-gate/run.sh <current claude binary> <candidate claude binary>
# Runs the gate fixtures (the loop below) on each binary (fake keys,
# scratch HOME and CLAUDE_CONFIG_DIR, loopback dummies; lib.sh pins each scratch
# proxy to the version of the binary under test and fences proxy HTTP(S) traffic
# to loopback). Then compare.py proves itself
# on planted faults, compares the recorded client and upstream requests of the
# two runs, and diffs the binaries' environment and settings names (record
# only). Blocks on a fixture FAIL, a refused request, or a changed compared
# field that upgrade-gate/allowed/<candidate version>.txt does not list.
# Precondition, not checked here: before a run, re-fetch with npm pack the
# current binary's package (@anthropic-ai/claude-code-darwin-arm64 at the
# current version) and the Agent SDK in SDK_DIR
# (@anthropic-ai/claude-agent-sdk@0.3.290). Each tarball's sha512 must equal
# npm view <package>@<version> dist.integrity. The packed package/claude and
# the <current claude binary> argument must have the sha256 recorded when that
# version was pinned (2.1.295:
# 0116ee2e0a513900b633d9951367f18747686478e2b462805b8c31609f047f70), and the
# packed package/sdk.mjs must equal SDK_DIR/sdk.mjs.
#
# Approved gaps: the gate does not compare or decide
# - helper requests that only interactive sessions send (the fixtures run -p,
#   entrypoint sdk-cli, or the Agent SDK, entrypoint sdk-ts);
# - the client's TLS ClientHello (the proxy sends its own compiled hello);
# - the effective fallbackModel and modelPicker values (F32: INCONCLUSIVE);
# - the production transport, the OAuth identity rewrite, and CCH signer
#   equivalence (an API key on a loopback upstream; no Go wire tests);
# - a coverage manifest of the fork's dependencies;
# - account-flag behavior, allowance weight, and the subscriber TTL branch
#   (fake keys load no account);
# - sdk-py and workload/cron requests (no fixture sends them);
# - compaction thresholds beyond F26's GPT subagent point and F28's reactive
#   path;
# - request fields outside the compared list; a change there that breaks
#   admission shows as a refused request.
set -euo pipefail
GATE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURE_DIR="$GATE_DIR/../test/release1-client"
OLD_BIN="${1:?usage: run.sh <current claude> <candidate claude>}"
NEW_BIN="${2:?usage: run.sh <current claude> <candidate claude>}"
NEW_VERSION="$(env -i PATH="$PATH" "$NEW_BIN" --version | awk '{print $1}')"
OUT="$(mktemp -d /tmp/claude-501/upgrade-gate.XXXXXX)"
status=0
for label in old new; do
	bin="$OLD_BIN"
	[ "$label" = old ] || bin="$NEW_BIN"
	mkdir "$OUT/$label"
	for name in f1_listener_keys f24_version_pin f8_real_client f20_background_gpt f32_settings_merge f34_cloak_inputs f5_client_429 f6_billing f28_overflow f26_gpt_window sdk_ts; do
		rc=0
		CLAUDE_BIN="$bin" FIXTURE_TMP="$OUT/$label" /opt/homebrew/bin/bash "$FIXTURE_DIR/$name.sh" >"$OUT/$label/$name.out" 2>&1 || rc=$?
		result="$(LC_ALL=C /usr/bin/grep -E '^(PASS|FAIL|INCONCLUSIVE):' "$OUT/$label/$name.out" | tail -n 1 | cut -c 1-160 || true)"
		echo "RECORD fixture $label $name exit=$rc $result"
		if [ "$rc" != 0 ] || LC_ALL=C /usr/bin/grep -q '^FAIL:' "$OUT/$label/$name.out"; then
			echo "BLOCK fixture-fail $label $name"
			status=1
		fi
	done
done
python3 "$GATE_DIR/compare.py" --self-test "$OUT"/old/r1-client-f8.*/client.jsonl || status=1
python3 "$GATE_DIR/compare.py" "$OUT/old" "$OUT/new" "$GATE_DIR/allowed/$NEW_VERSION.txt" || status=1
python3 "$GATE_DIR/compare.py" --names "$OLD_BIN" "$NEW_BIN"
echo "GATE $([ "$status" = 0 ] && echo PASS || echo FAIL) new=$NEW_VERSION records=$OUT"
exit "$status"
