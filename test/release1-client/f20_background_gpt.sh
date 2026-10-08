#!/opt/homebrew/bin/bash
# Recovered row 111, fixture F20: in a GPT session every background request
# (title, summary, memory selection, checks, auto-mode setup, classifier) uses
# a Claude model and is confirmed, not refused. Each class is identified by its
# own request marker; a class this run did not trigger is reported as
# INCONCLUSIVE by name and never counted as proof by another class.
# Markers (decision line 24, from the 2.1.285 binary read recorded there):
#   memory selection  top-level output_format whose schema names selected_memories
#   auto-mode setup   top-level output_format without selected_memories
#   check             output_config.format schema with properties ok, reason, impossible
#   title             output_config.format schema of any other shape
#   classifier        stop_sequences present (the only class that sends them)
#   summary           no marker is recorded; always reported INCONCLUSIVE here
source "$(dirname "$0")/lib.sh"
fixture_init f20 18450
start_stack ok
run_claude -p "Reply ok." --model gpt-6.1-sol --max-turns 1
run_claude -p "Reply ok again." --model gpt-6.1-sol --max-turns 1 --permission-mode auto
records "$ROOT/client.jsonl" 'r["path"]+" "+r["model"]+" "+str(r["status"])' | sort | uniq -c

refused=$(records "$ROOT/client.jsonl" 'r["status"] == 400 and "client is not confirmed as native claude code" in r["error_body"].lower()' | grep -c True || true)
[ "$refused" = 0 ] || fail "$refused requests refused as unconfirmed; list: $ROOT/client.jsonl"
gpt_side=$(records "$ROOT/client.jsonl" 'r["model"].startswith("gpt-")' | grep -c True || true)
[ "$gpt_side" -ge 1 ] || fail "no GPT main-thread request reached the proxy"

# One line per recorded request: "<class> <model> <status>", class "-" when no marker matches.
classify='(lambda b: (
  "memory-selection" if "selected_memories" in json.dumps(b.get("output_format", ""))
  else "auto-mode-setup" if "output_format" in b
  else "check" if all(k in json.dumps(b.get("output_config", {}).get("format", {})) for k in ("\"ok\"", "\"reason\"", "\"impossible\""))
  else "title" if b.get("output_config", {}).get("format")
  else "classifier" if b.get("stop_sequences")
  else "-"))(json.loads(r["body"] or "{}"))'
records "$ROOT/client.jsonl" "$classify + \" \" + r[\"model\"] + \" \" + str(r[\"status\"])" >"$ROOT/classes.txt"
grep -v '^- ' "$ROOT/classes.txt" | sort | uniq -c || true

missing=()
for class in title memory-selection check auto-mode-setup classifier; do
	seen=$(grep -c "^$class " "$ROOT/classes.txt" || true)
	if [ "$seen" = 0 ]; then
		missing+=("$class")
		continue
	fi
	on_gpt=$(grep -c "^$class gpt-" "$ROOT/classes.txt" || true)
	[ "$on_gpt" = 0 ] || fail "$class: $on_gpt requests used a GPT model; background tasks must stay on Claude (decision line 32)"
	not_claude=$(grep "^$class " "$ROOT/classes.txt" | grep -vc "^$class claude-" || true)
	[ "$not_claude" = 0 ] || fail "$class: $not_claude requests used no Claude model"
	not_ok=$(grep "^$class " "$ROOT/classes.txt" | grep -vc ' 200$' || true)
	[ "$not_ok" = 0 ] || fail "$class: $not_ok requests did not reach the upstream with status 200 (not confirmed)"
	echo "class $class: $seen requests, all on Claude and confirmed"
done
missing+=(summary)
inconclusive "F20 classes not triggered or without a recorded marker in this run: ${missing[*]}; each stays unproved for row 111 until observed (Question 15)"
pass "F20: every observed background class used Claude and was confirmed beside $gpt_side GPT requests"
