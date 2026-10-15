#!/opt/homebrew/bin/bash
# Recovered row 117, fixture F26: a GPT subagent in a Claude session reaches
# compaction near 400,000 tokens (window 433,000) while the Claude main thread
# keeps its 1M window. The dummy reports large ChatGPT input usage; the relay
# shows whether the client sends x-claude-code-compaction for the GPT model.
# The dummy answers the subagent's first two GPT requests with one tool call each,
# so the subagent has an earlier assistant turn to summarize at its third request
# (Claude Code compacts only groups older than the newest one).
source "$(dirname "$0")/lib.sh"
fixture_init f26 18520
mkdir -p "$ROOT/config/agents"
cat >"$ROOT/config/agents/gpt-fixture.md" <<'MD'
---
name: gpt-fixture
description: Use when the fixture asks for the GPT subagent
model: gpt-6.1-sol
---
Reply ok, then call no tools.
MD
FIXTURE_SUBAGENT_TYPE=gpt-fixture FIXTURE_GPT_INPUT_TOKENS=402000 FIXTURE_GPT_TOOL_FIRST=2 start_stack agent
run_claude -p "Use the gpt-fixture subagent." --max-turns 4 --allowedTools Agent
records "$ROOT/client.jsonl" 'r["model"]+" "+str(r["status"])+" "+str(any(k.lower()=="x-claude-code-compaction" for k in r["headers"]))' | sort | uniq -c
gpt_turns=$(records "$ROOT/client.jsonl" 'r["model"].startswith("gpt-")' | grep -c True || true)
gpt_compact=$(records "$ROOT/client.jsonl" 'r["model"].startswith("gpt-") and any(k.lower()=="x-claude-code-compaction" for k in r["headers"])' | grep -c True || true)
[ "$gpt_turns" -ge 1 ] || fail "the GPT subagent never reached the proxy (plugin or agent model pass-through)"
[ "$gpt_turns" -ge 3 ] || fail "fewer than three GPT turns: the subagent did not take both dummy tool calls"
[ "$gpt_compact" -ge 1 ] || fail "GPT subagent reported 402,000 input tokens and sent no x-claude-code-compaction header"
pass "F26: GPT subagent sent x-claude-code-compaction at 402,000 reported tokens"
