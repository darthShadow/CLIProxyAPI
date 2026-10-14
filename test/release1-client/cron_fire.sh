#!/opt/homebrew/bin/bash
# Cron fire fixture, attended. A person runs interactive Claude Code against
# the scratch stack (relay, P built from this tree, dummy upstream in cron
# mode). The dummy answers the first request that offers CronCreate with one
# CronCreate call for the next minute; the fired turn carries ", workload/cron"
# in its User-Agent. After the done file appears, the script judges every
# recorded workload/cron request.
# PASS: each got 200 and reached the dummy with User-Agent and X-App unchanged
# and every client beta present. FAIL: one was refused or changed.
# INCONCLUSIVE: none arrived.
source "$(dirname "$0")/lib.sh"
fixture_init cron-fire 18560
start_stack cron
echo "Run: env -i PATH=\"\$PATH\" TERM=\"\$TERM\" HOME=$ROOT/home CLAUDE_CONFIG_DIR=$ROOT/config CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 DISABLE_AUTOUPDATER=1 $CLAUDE_BIN --permission-mode default --settings $ROOT/overlay.json"
echo "Type: Schedule a reply. Approve CronCreate, wait up to 3 minutes for a second ok, then /exit."
echo "When done: touch $ROOT/done"
for _ in $(seq 1 180); do [ -e "$ROOT/done" ] && break; sleep 5; done
[ -e "$ROOT/done" ] || fail "no done file within 15 minutes"
set +e
python3 - "$ROOT/client.jsonl" "$ROOT/upstream.jsonl" <<'PY'
import json, sys
client_path, upstream_path = sys.argv[1:3]
want = "workload/cron"

def load(path):
    try:
        return [json.loads(line) for line in open(path, encoding="utf-8")]
    except FileNotFoundError:
        return []

def header(record, name):
    return next((v for k, v in record["headers"].items() if k.lower() == name), "")

client = [r for r in load(client_path) if r["path"].startswith("/v1/messages")]
upstream = load(upstream_path)
for r in client:
    body = json.loads(r["body"] or "{}")
    cron = next((t for t in body.get("tools", []) if t.get("name") == "CronCreate"), None)
    print("client %s %s ua=%r x-app=%r betas=%r user_id=%s cron_fields=%s" % (
        r["status"], r["path"], header(r, "user-agent"), header(r, "x-app"), header(r, "anthropic-beta"),
        sorted(json.loads((body.get("metadata") or {}).get("user_id") or "{}")),
        sorted(((cron or {}).get("input_schema") or {}).get("properties", {}))))
judged = [r for r in client if want in header(r, "user-agent")]
if not judged:
    sys.exit(3)
bad = []
for r in judged:
    betas = {b.strip() for b in header(r, "anthropic-beta").split(",") if b.strip()}
    forwarded = [u for u in upstream if header(u, "user-agent") == header(r, "user-agent") and header(u, "x-app") == header(r, "x-app")
                 and betas <= {b.strip() for b in header(u, "anthropic-beta").split(",")}]
    if r["status"] != 200 or not forwarded:
        bad.append(r)
        print("judged %s %s %s" % (r["status"], r["path"], r["error_body"][:200]))
sys.exit(1 if bad else 0)
PY
rc=$?
set -e
case "$rc" in
	0) pass "cron fire: every workload/cron request forwarded with identity unchanged" ;;
	3) inconclusive "cron fire: no client request carried workload/cron" ;;
	*) fail "cron fire: a workload/cron request was refused or changed (see judged lines)" ;;
esac
