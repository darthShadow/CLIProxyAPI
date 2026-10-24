#!/opt/homebrew/bin/bash
# Recovered row 125, fixture F34: with every cloak input planted (global switch
# off and a per-key cloak block with mode "always"), confirmed Claude
# Code requests reach the upstream without cloak rewrites, and an unconfirmed
# caller gets a local 400 with zero upstream sends. The OAuth cloak_mode
# attribute needs a login file and stays with the step A3 Go tests.
source "$(dirname "$0")/lib.sh"
fixture_init f34 18470
start_stack ok
python3 - "$ROOT/cpa.yaml" <<'PY'
import sys
path = sys.argv[1]
text = open(path).read()
text = text.replace("disable-claude-cloak-mode: true", "disable-claude-cloak-mode: false")
text = text.replace('''    base-url: "http://127.0.0.1:''', '''    cloak:
      mode: "always"
    base-url: "http://127.0.0.1:''', 1)
open(path, "w").write(text)
PY
kill "${PIDS[1]}"; sleep 1
HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 NO_PROXY=127.0.0.1,localhost \
	"$ROOT/cpa" -config "$ROOT/cpa.yaml" -local-model >"$ROOT/cpa2.log" 2>&1 &
PIDS+=("$!"); sleep 2
run_claude -p "Reply ok." --max-turns 1
before=$(wc -l <"$ROOT/upstream.jsonl")
curl -s -o "$ROOT/unconfirmed.json" -w '%{http_code}' -H 'Authorization: Bearer fixture-proxy-key' -H 'Content-Type: application/json' \
	-H 'User-Agent: curl/8.0' -d '{"model":"claude-opus-5-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}' \
	"http://127.0.0.1:$CPA_PORT/v1/messages" >"$ROOT/unconfirmed.status"
after=$(wc -l <"$ROOT/upstream.jsonl")
[ "$(cat "$ROOT/unconfirmed.status")" = 400 ] || fail "unconfirmed caller got $(cat "$ROOT/unconfirmed.status"), want 400"
[ "$before" = "$after" ] || fail "unconfirmed caller reached the upstream"
cloaked=$(records "$ROOT/upstream.jsonl" '"fallbacks" in r["body"]' | grep -c True || true)
[ "$cloaked" = 0 ] || fail "a confirmed request carries a cloak rewrite (injected fallbacks)"
pass "F34: cloak inputs planted, confirmed request unrewritten, unconfirmed request refused with zero sends"
