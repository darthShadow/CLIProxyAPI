#!/usr/bin/env python3
"""Loopback dummy upstream for the release 1 client fixtures.

It answers Anthropic Messages, count_tokens, and ChatGPT Responses requests and
records each request (path, headers, body) as one JSON line. It forwards
nothing. FIXTURE_MODE selects the answer:
  ok                 normal answers (default)
  billing402         Anthropic and ChatGPT requests get 402 with a fixed body
  billing403         the same with 403
  codex_instream     a ChatGPT stream starts, then fails with response.failed
  throttle429        429 with Retry-After: 30 (a per-minute throttle shape)
  overflow           ChatGPT answers ok for a new conversation, then 400
                     context_length_exceeded when input contains a completed assistant turn
  (FIXTURE_SUBAGENT_TYPE names the agent; FIXTURE_GPT_INPUT_TOKENS sets the
  ChatGPT usage input_tokens the dummy reports; FIXTURE_GPT_TOOL_FIRST=N gives
  each of the first N ChatGPT requests that offer a function tool one call to that tool)
  agent              the first main-thread Anthropic request that offers an Agent or
                     Task tool gets one tool_use for it; later requests get "ok"
  permprobe          a main-thread Anthropic request that offers Bash gets one Bash
                     tool_use per command in FIXTURE_PERM_COMMANDS (separated by
                     "|"), one per turn, then "ok"
Usage: dummy_upstream.py <record-file> <port>
"""
import http.server
import json
import os
import sys

RECORD, PORT = sys.argv[1], int(sys.argv[2])
MODE = os.environ.get("FIXTURE_MODE", "ok")
ANTHROPIC_BILLING = b'{"type":"error","error":{"type":"billing_error","message":"fixture renewal needed","fixture_field":1}}'
CHATGPT_BILLING = b'{"error":{"type":"billing_error","code":"payment_required","message":"fixture renewal needed","fixture_field":1}}'
CHATGPT_OVERFLOW = b'{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}'


def sse(events):
    return "".join("event: %s\ndata: %s\n\n" % (name, json.dumps(data, separators=(",", ":"))) for name, data in events).encode()


def anthropic_stream(model):
    usage = {"input_tokens": 12, "output_tokens": 1, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}
    return sse([
        ("message_start", {"type": "message_start", "message": {"id": "msg_fixture", "type": "message", "role": "assistant", "model": model, "content": [], "stop_reason": None, "usage": usage}}),
        ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "ok"}}),
        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
        ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 1}}),
        ("message_stop", {"type": "message_stop"}),
    ])


def chatgpt_stream(model, fail):
    events = [
        ("response.created", {"type": "response.created", "sequence_number": 0, "response": {"id": "resp_fixture", "model": model, "status": "in_progress"}}),
        ("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": 1, "item_id": "msg_1", "output_index": 0, "content_index": 0, "delta": "ok"}),
    ]
    if fail:
        events.append(("response.failed", {"type": "response.failed", "sequence_number": 2, "response": {"id": "resp_fixture", "status": "failed", "error": json.loads(CHATGPT_BILLING)["error"]}}))
    else:
        events.append(("response.completed", {"type": "response.completed", "sequence_number": 2, "response": {"id": "resp_fixture", "model": model, "status": "completed", "output": [{"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]}], "usage": {"input_tokens": int(os.environ.get("FIXTURE_GPT_INPUT_TOKENS", "12")), "output_tokens": 1, "total_tokens": int(os.environ.get("FIXTURE_GPT_INPUT_TOKENS", "12")) + 1}}}))
    return sse(events)


def chatgpt_tool_stream(model, name, n):
    call = {"type": "function_call", "id": "fc_fixture_%d" % n, "call_id": "call_fixture_%d" % n, "name": name, "arguments": "{}", "status": "completed"}
    tokens = int(os.environ.get("FIXTURE_GPT_INPUT_TOKENS", "12"))
    return sse([
        ("response.created", {"type": "response.created", "sequence_number": 0, "response": {"id": "resp_fixture_tool_%d" % n, "model": model, "status": "in_progress"}}),
        ("response.output_item.added", {"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": dict(call, arguments="", status="in_progress")}),
        ("response.output_item.done", {"type": "response.output_item.done", "sequence_number": 2, "output_index": 0, "item": call}),
        ("response.completed", {"type": "response.completed", "sequence_number": 3, "response": {"id": "resp_fixture_tool_%d" % n, "model": model, "status": "completed", "output": [call], "usage": {"input_tokens": tokens, "output_tokens": 1, "total_tokens": tokens + 1}}}),
    ])


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    agent_sent = False
    gpt_tools_sent = 0

    def log_message(self, *args):
        pass

    def reply(self, status, body, content_type="application/json", headers=None):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        for key, value in (headers or {}).items():
            self.send_header(key, value)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.record(b"")
        self.reply(404, b'{"error":"fixture"}')

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length)
        self.record(body)
        try:
            payload = json.loads(body or b"{}")
        except ValueError:
            payload = {}
        model = payload.get("model", "fixture")
        chatgpt = self.path.rstrip("/").endswith("/responses")
        if MODE in ("billing402", "billing403"):
            status = 402 if MODE == "billing402" else 403
            return self.reply(status, CHATGPT_BILLING if chatgpt else ANTHROPIC_BILLING, headers={"X-Fixture-Upstream": "kept"})
        if MODE == "throttle429":
            return self.reply(429, b'{"type":"error","error":{"type":"rate_limit_error","message":"fixture throttle"}}', headers={"Retry-After": "30"})
        if chatgpt and MODE == "overflow" and any(
            item.get("role") == "assistant" for item in payload.get("input", []) if isinstance(item, dict)
        ):
            return self.reply(400, CHATGPT_OVERFLOW)
        if self.path.endswith("/count_tokens"):
            return self.reply(200, b'{"input_tokens":12}')
        if chatgpt and Handler.gpt_tools_sent < int(os.environ.get("FIXTURE_GPT_TOOL_FIRST", "0")):
            name = next((t.get("name") for t in payload.get("tools", []) if t.get("type") == "function" and t.get("name")), None)
            if name:
                Handler.gpt_tools_sent += 1
                return self.reply(200, chatgpt_tool_stream(model, name, Handler.gpt_tools_sent), "text/event-stream")
        if chatgpt:
            return self.reply(200, chatgpt_stream(model, MODE == "codex_instream"), "text/event-stream")
        if MODE == "agent" and not chatgpt and not Handler.agent_sent:
            tool = next((t.get("name") for t in payload.get("tools", []) if t.get("name") in ("Agent", "Task")), None)
            if tool:
                Handler.agent_sent = True
                use = {"type": "tool_use", "id": "toolu_fixture", "name": tool, "input": {"description": "fixture", "prompt": "Reply with ok.", "subagent_type": os.environ.get("FIXTURE_SUBAGENT_TYPE", "general-purpose")}}
                return self.tool_use_reply(model, payload, use)
        if MODE == "permprobe" and not chatgpt and any(t.get("name") == "Bash" for t in payload.get("tools", [])):
            commands = [c for c in os.environ.get("FIXTURE_PERM_COMMANDS", "").split("|") if c]
            results = [c for m in payload.get("messages", []) if isinstance(m.get("content"), list) for c in m["content"] if isinstance(c, dict) and c.get("type") == "tool_result"]
            if len(results) < len(commands):
                use = {"type": "tool_use", "id": "toolu_perm_%d" % len(results), "name": "Bash", "input": {"command": commands[len(results)], "description": "fixture permission probe"}}
                return self.tool_use_reply(model, payload, use)
        if payload.get("stream"):
            return self.reply(200, anthropic_stream(model), "text/event-stream")
        message = {"id": "msg_fixture", "type": "message", "role": "assistant", "model": model, "content": [{"type": "text", "text": "ok"}], "stop_reason": "end_turn", "usage": {"input_tokens": 12, "output_tokens": 1}}
        return self.reply(200, json.dumps(message).encode())

    def tool_use_reply(self, model, payload, use):
        message = {"id": "msg_fixture_tool", "type": "message", "role": "assistant", "model": model, "content": [use], "stop_reason": "tool_use", "usage": {"input_tokens": 12, "output_tokens": 1}}
        if payload.get("stream"):
            return self.reply(200, sse([
                ("message_start", {"type": "message_start", "message": dict(message, content=[], stop_reason=None)}),
                ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": dict(use, input={})}),
                ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "input_json_delta", "partial_json": json.dumps(use["input"])}}),
                ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                ("message_delta", {"type": "message_delta", "delta": {"stop_reason": "tool_use"}, "usage": {"output_tokens": 1}}),
                ("message_stop", {"type": "message_stop"}),
            ]), "text/event-stream")
        return self.reply(200, json.dumps(message).encode())

    def record(self, body):
        entry = {"method": self.command, "path": self.path, "headers": dict(self.headers.items()), "body": body.decode("utf-8", "replace")}
        for name in list(entry["headers"]):
            if name.lower() in ("authorization", "x-api-key"):
                entry["headers"][name] = "<redacted>"
        with open(RECORD, "a", encoding="utf-8") as handle:
            handle.write(json.dumps(entry) + "\n")


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
