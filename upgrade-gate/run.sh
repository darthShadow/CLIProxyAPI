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
# cc_wire runs the binary with no proxy against an offline TLS socket and records Claude Code's direct wire shape.
# Precondition, not checked here: before a run, re-fetch with npm pack the
# current binary's package (@anthropic-ai/claude-code-darwin-arm64 at the
# current version) and the Agent SDK in SDK_DIR
# (@anthropic-ai/claude-agent-sdk@0.3.290). Each tarball's sha512 must equal
# npm view <package>@<version> dist.integrity. The packed package/claude and
# the <current claude binary> argument must have the sha256 recorded when that
# version was pinned (2.1.311:
# 85d8c08707fcfd1e78ffa0204eefbc6ec9e4fe22f375fb2767ef85d957ed3161), and the
# packed package/sdk.mjs must equal SDK_DIR/sdk.mjs.
#
# CPA_BIN_OLD (optional) is the proxy build of the current pair for the old
# side; the new side uses CPA_BIN. Without it both sides use CPA_BIN.
#
# Linux-x64 mode: a linux-x64 build of a version is admitted only after this
# script ends GATE PASS in the upgrade-gate/linux.Dockerfile image with no
# network, given the new/ records of that version's PASS run in place of the
# current binary and the linux-x64 package/claude as the candidate. Only the
# candidate runs; compare.py checks its requests against the darwin records
# under allowed/<version>-linux-x64.txt. Precondition, not checked here: the
# packed @anthropic-ai/claude-code-linux-x64 tarball's sha512 equals npm view
# dist.integrity, and CPA_BIN is cli-proxy-api from the fork release's
# linux_amd64_no-plugin archive. test/release1-client/helper_cli.sh must also
# print PASS in the same image. From this checkout on the Linux host; the
# container runs as its root, which rootless Docker maps to the calling user:
#   docker build --platform linux/amd64 -t cpa-gate-linux - <upgrade-gate/linux.Dockerfile
#   docker run --rm --platform linux/amd64 --network none \
#     -v "$PWD:/fork:ro" -v <darwin new/>:/darwin:ro -v <linux package/>:/claude:ro \
#     -v <SDK_DIR>:/sdk:ro -v <archive dir>:/cpa:ro -v <out dir>:/tmp/claude-501 \
#     -e SDK_DIR=/sdk -e CPA_BIN=/cpa/cli-proxy-api -w /fork cpa-gate-linux \
#     bash upgrade-gate/run.sh /darwin /claude/claude
#
# Approved gaps: the gate does not compare or decide
# - in Linux-x64 mode, the C library and loader of the host that installs the
#   build (the image's glibc runs the candidate), and that host's kernel and
#   CPU when the run's machine is another; the host's first accepted request
#   covers them;
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
ALLOWED="$GATE_DIR/allowed/$NEW_VERSION.txt"
labels="old new"
# Linux-x64 mode (header): the old side is the darwin PASS records of this version.
if [ -d "$OLD_BIN" ]; then
	# Interfaces are the symlinks; a host with the bonding module also shows the file bonding_masters.
	# [ "$(ls /sys/class/net 2>/dev/null)" = lo ] || { echo "STOP: a records directory runs only under docker run --network none"; exit 2; }
	[ "$(find /sys/class/net -mindepth 1 -maxdepth 1 -type l -printf '%f\n' 2>/dev/null)" = lo ] || { echo "STOP: a records directory runs only under docker run --network none"; exit 2; }
	cp -R "$OLD_BIN" "$OUT/old"
	LC_ALL=C /usr/bin/grep -qF "claude-cli/$NEW_VERSION (" "$OUT"/old/r1-client-f8.*/client.jsonl \
		|| { echo "STOP: $OLD_BIN holds no $NEW_VERSION records"; exit 2; }
	ALLOWED="$GATE_DIR/allowed/$NEW_VERSION-linux-x64.txt"
	labels="new"
fi
status=0
for label in $labels; do
	bin="$OLD_BIN"
	cpa="${CPA_BIN_OLD:-${CPA_BIN:-}}"
	[ "$label" = old ] || bin="$NEW_BIN"
	[ "$label" = old ] || cpa="${CPA_BIN:-}"
	mkdir "$OUT/$label"
	# for name in f1_listener_keys f24_version_pin f8_real_client f20_background_gpt f32_settings_merge f34_cloak_inputs f5_client_429 f6_billing f28_overflow f26_gpt_window sdk_ts; do
	for name in f1_listener_keys f24_version_pin f8_real_client f20_background_gpt f32_settings_merge f34_cloak_inputs f5_client_429 f6_billing f28_overflow f26_gpt_window sdk_ts cc_wire; do
		rc=0
		script="$FIXTURE_DIR/$name.sh"; [ -f "$script" ] || script="$GATE_DIR/$name.sh"
		# CPA_BIN="$cpa" CLAUDE_BIN="$bin" FIXTURE_TMP="$OUT/$label" "$BASH" "$FIXTURE_DIR/$name.sh" >"$OUT/$label/$name.out" 2>&1 || rc=$?
		CPA_BIN="$cpa" CLAUDE_BIN="$bin" FIXTURE_TMP="$OUT/$label" "$BASH" "$script" >"$OUT/$label/$name.out" 2>&1 || rc=$?
		result="$(LC_ALL=C /usr/bin/grep -E '^(PASS|FAIL|INCONCLUSIVE):' "$OUT/$label/$name.out" | tail -n 1 | cut -c 1-160 || true)"
		echo "RECORD fixture $label $name exit=$rc $result"
		if [ "$rc" != 0 ] || LC_ALL=C /usr/bin/grep -q '^FAIL:' "$OUT/$label/$name.out"; then
			echo "BLOCK fixture-fail $label $name"
			status=1
		fi
	done
done
python3 "$GATE_DIR/compare.py" --self-test "$OUT"/old/r1-client-f8.*/client.jsonl || status=1
python3 "$GATE_DIR/compare.py" "$OUT/old" "$OUT/new" "$ALLOWED" || status=1
[ -d "$OLD_BIN" ] || python3 "$GATE_DIR/compare.py" --names "$OLD_BIN" "$NEW_BIN"
echo "GATE $([ "$status" = 0 ] && echo PASS || echo FAIL) new=$NEW_VERSION allowed=${ALLOWED##*/} records=$OUT"
exit "$status"
