#!/opt/homebrew/bin/bash
# Recovered row 93, fixture F1, with the management page of decision line 103:
# the proxy listens only on its loopback port; a wrong or missing client key
# gets 401 with zero upstream sends; the management API answers only the
# management key (the client key is not one); /management.html is the pinned
# local copy byte for byte; nothing tries to fetch the page at start or on
# access.
source "$(dirname "$0")/lib.sh"
fixture_init f1 18530
unset MANAGEMENT_PASSWORD
mkdir -p "$ROOT/static"
printf '%s\n' '<!doctype html><title>fixture pinned page</title>' >"$ROOT/static/management.html"
pinned_sha=$(shasum -a 256 "$ROOT/static/management.html")
export MANAGEMENT_STATIC_PATH="$ROOT/static/management.html"
start_stack ok 'debug: true
remote-management:
  allow-remote: false
  secret-key: "fixture-management-key"
  disable-control-panel: false
  disable-auto-update-panel: true'
cpa_pid="${PIDS[1]}"
listening=$(lsof -nP -a -p "$cpa_pid" -iTCP -sTCP:LISTEN | awk 'NR > 1 {print $9}' | sort -u)
[ "$listening" = "127.0.0.1:$CPA_PORT" ] || fail "proxy listens on: $listening (want only 127.0.0.1:$CPA_PORT)"
for key in wrong-key ""; do
	status=$(curl -s -o /dev/null -w '%{http_code}' ${key:+-H "Authorization: Bearer $key"} -H 'Content-Type: application/json' \
		-d '{"model":"claude-opus-5-5","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}' "http://127.0.0.1:$CPA_PORT/v1/messages")
	[ "$status" = 401 ] || fail "key '${key:-<none>}' got $status, want 401"
done
# Three failures stay under the handler's five-failure ban.
for key in "" wrong-management-key fixture-proxy-key; do
	mgmt=$(curl -s -o /dev/null -w '%{http_code}' ${key:+-H "Authorization: Bearer $key"} "http://127.0.0.1:$CPA_PORT/v0/management/config")
	[ "$mgmt" = 401 ] || fail "management with key '${key:-<none>}' answered $mgmt, want 401"
done
mgmt=$(curl -s -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer fixture-management-key' "http://127.0.0.1:$CPA_PORT/v0/management/config")
[ "$mgmt" = 200 ] || fail "management with the management key answered $mgmt, want 200"
page=$(curl -s -o "$ROOT/page.html" -w '%{http_code}' "http://127.0.0.1:$CPA_PORT/management.html")
[ "$page" = 200 ] || fail "/management.html answered $page, want 200"
cmp -s "$ROOT/page.html" "$ROOT/static/management.html" || fail "the page served is not the pinned copy"
[ "$(shasum -a 256 "$ROOT/static/management.html")" = "$pinned_sha" ] || fail "the pinned copy changed on disk"
grep -q 'management asset auto-updater skipped: disable-auto-update-panel is enabled' "$ROOT/cpa.log" \
	|| fail "no auto-updater skip line in the debug log, so the start-up check observed nothing"
if grep -Eq 'management asset (sync|is already up to date|updated)|management release|fallback management|failed to download management|static directory for management' "$ROOT/cpa.log"; then
	fail "the proxy tried to fetch the management page"
fi
[ ! -s "$ROOT/upstream.jsonl" ] || fail "a refused request reached the upstream"
pass "F1: one loopback listener; wrong and missing client keys 401; management only with its key; pinned page served unchanged; no page fetch; zero upstream sends"
