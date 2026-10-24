#!/usr/bin/env python3
"""Claude Code upgrade gate comparison.

Usage:
  compare.py OLD_DIR NEW_DIR ALLOWED.txt
  compare.py --self-test CLIENT.jsonl
  compare.py --names OLD_BIN NEW_BIN

OLD_DIR and NEW_DIR each hold the release 1 client fixtures run on one Claude
Code version: one r1-client-<fixture>.XXXXXX directory per fixture_init call,
with the relay's client.jsonl and the dummy's upstream.jsonl. Requests are
grouped by fixture, side (client or upstream), and class; for each compared
field, the set of values in a class is compared between the two versions.
Output lines:
  BLOCK refused <old|new> <fixture> <path>   the proxy refused a client request
                                             as unconfirmed; never allowed
  BLOCK <field> <fixture> <side> <class> removed|added <value>
  BLOCK class-missing|class-new <fixture> <side> <class>
  ALLOWED ...   a BLOCK line, other than refused, listed verbatim in ALLOWED.txt
  RECORD ...    reported; never blocks
Exit 0 only when every BLOCK line is allowed. A missing ALLOWED.txt allows
nothing. A run directory without records exits 2.
"""
import hashlib
import json
import os
import re
import sys

REFUSAL = "client is not confirmed as native claude code"
BILLING = "x-anthropic-billing-header:"
FIELDS = ("user-agent", "header-names", "betas", "cache", "metadata", "billing")
OBSERVED = ("thread", "markers", "prompt-text", "tool-text")
RUN_DIR = re.compile(r"r1-client-([A-Za-z0-9_]+)\.[A-Za-z0-9]{6}")
MASKS = [
    (re.compile(r"[^\s\"']*/r1-client-[A-Za-z0-9_]+\.[A-Za-z0-9]{6}"), "<root>"),
    (re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"), "<uuid>"),
    (re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}"), "<date>"),
]
ENV_NAME = re.compile(rb"[.\"'`]((?:CLAUDE|ANTHROPIC|DISABLE|ENABLE)_[A-Z0-9_]+)(?=[^A-Za-z0-9_])")
SETTING_NAME = re.compile(rb"[{,]([a-z][A-Za-z0-9]{2,}):(?:\(\)=>)?[^;{}\"]{0,100}?\.describe\(\"")
KEEPALIVE = (b"cache_touch", b"cache-keepalive-")


def load(run_dir):
    runs = {}
    for entry in sorted(os.listdir(run_dir)):
        match = RUN_DIR.fullmatch(entry)
        if not match:
            continue
        sides = runs.setdefault(match.group(1), {"client": [], "upstream": []})
        for side in sides:
            path = os.path.join(run_dir, entry, side + ".jsonl")
            if os.path.exists(path):
                with open(path, encoding="utf-8") as handle:
                    sides[side].extend(json.loads(line) for line in handle)
    if not any(sides["client"] or sides["upstream"] for sides in runs.values()):
        print("BLOCK no-records %s" % run_dir)
        sys.exit(2)
    return runs


def header(record, name):
    for key, value in record["headers"].items():
        if key.lower() == name:
            return value
    return ""


def body_of(record):
    try:
        body = json.loads(record["body"] or "{}")
    except ValueError:
        return {"<unparsable>": True}
    return body if isinstance(body, dict) else {"<not-object>": True}


def listed(body, key):
    value = body.get(key)
    return value if isinstance(value, list) else []


def class_key(record):
    model = body_of(record).get("model")
    model = model if isinstance(model, str) else ""
    family = "gpt" if model.startswith("gpt-") else "claude" if model.startswith("claude-") else "-"
    compaction = "compaction" if header(record, "x-claude-code-compaction") else "-"
    return ":".join([record["path"].split("?")[0], family, header(record, "x-claude-code-request-class") or "-",
                     header(record, "x-claude-code-agent-type") or "-", compaction])


def classes(records):
    grouped = {}
    for record in records:
        if record["method"] == "POST":
            grouped.setdefault(class_key(record), []).append(record)
    return grouped


def cache_positions(body):
    positions = []
    if isinstance(body.get("cache_control"), dict):
        positions.append("top=" + json.dumps(body["cache_control"], sort_keys=True))
    groups = [("system", listed(body, "system")), ("tools", listed(body, "tools"))]
    messages = listed(body, "messages")
    for i, message in enumerate(messages):
        content = message.get("content") if isinstance(message, dict) else None
        if isinstance(content, list):
            groups.append(("messages[-%d]" % (len(messages) - i), content))
    for name, blocks in groups:
        for i, block in enumerate(blocks):
            if isinstance(block, dict) and "cache_control" in block:
                positions.append("%s[-%d]=%s" % (name, len(blocks) - i, json.dumps(block["cache_control"], sort_keys=True)))
    return positions


def billing_fields(text):
    if not isinstance(text, str) or not text.startswith(BILLING):
        return []
    fields = []
    for part in text[len(BILLING):].split(";"):
        name, _, value = part.strip().partition("=")
        if name == "cc_entrypoint":
            fields.append("cc_entrypoint=" + value)
        elif name == "cch":
            fields.append("cch=" + ("<5hex>" if re.fullmatch(r"[0-9a-f]{5}", value) else "<other>"))
        elif name:
            fields.append(name)
    return fields


def facets(record):
    body = body_of(record)
    system = listed(body, "system")
    first = system[0].get("text") if system and isinstance(system[0], dict) else ""
    metadata = body.get("metadata") if isinstance(body.get("metadata"), dict) else {}
    raw = metadata.get("user_id")
    try:
        user_id = json.loads(raw) if isinstance(raw, str) and raw else {}
    except ValueError:
        user_id = "<not-json>"
    return {
        "user-agent": re.sub(r"claude-cli/[0-9]+\.[0-9]+\.[0-9]+", "claude-cli/<V>", header(record, "user-agent")),
        "header-names": list(record["headers"]),
        "betas": [b.strip() for b in header(record, "anthropic-beta").split(",") if b.strip()],
        "cache": cache_positions(body),
        "metadata": list(user_id) if isinstance(user_id, dict) else ["<not-object>"],
        "billing": billing_fields(first),
    }


def checksum(value):
    text = json.dumps(value, sort_keys=True)
    for pattern, replacement in MASKS:
        text = pattern.sub(replacement, text)
    return hashlib.sha256(text.encode()).hexdigest()[:12]


def observations(record):
    body = body_of(record)
    return {
        "thread": "present" if "thread" in body else "absent",
        "markers": str(len(cache_positions(body))),
        "prompt-text": checksum([body.get("system"), body.get("instructions")]),
        "tool-text": checksum(body.get("tools")),
    }


def values(records, get):
    return sorted({json.dumps(get(record)) for record in records})


def compare(old_runs, new_runs):
    lines = []
    for label, runs in (("old", old_runs), ("new", new_runs)):
        for fixture in sorted(runs):
            for record in runs[fixture]["client"]:
                line = "BLOCK refused %s %s %s" % (label, fixture, record["path"])
                if record.get("status") == 400 and REFUSAL in str(record.get("error_body", "")).lower() and line not in lines:
                    lines.append(line)
    for fixture in sorted(set(old_runs) | set(new_runs)):
        for side in ("client", "upstream"):
            old_classes = classes(old_runs.get(fixture, {}).get(side, []))
            new_classes = classes(new_runs.get(fixture, {}).get(side, []))
            for key in sorted(set(old_classes) | set(new_classes)):
                where = "%s %s %s" % (fixture, side, key)
                if key not in new_classes or key not in old_classes:
                    lines.append("BLOCK %s %s" % ("class-missing" if key not in new_classes else "class-new", where))
                    continue
                old, new = old_classes[key], new_classes[key]
                for field in FIELDS:
                    before = set(values(old, lambda record: facets(record)[field]))
                    after = set(values(new, lambda record: facets(record)[field]))
                    lines += ["BLOCK %s %s removed %s" % (field, where, value) for value in sorted(before - after)]
                    lines += ["BLOCK %s %s added %s" % (field, where, value) for value in sorted(after - before)]
                for name in OBSERVED:
                    before = values(old, lambda record: observations(record)[name])
                    after = values(new, lambda record: observations(record)[name])
                    if before != after:
                        lines.append("RECORD %s %s old=%s new=%s" % (name, where, ",".join(before), ",".join(after)))
                if len(old) != len(new):
                    lines.append("RECORD count %s old=%d new=%d" % (where, len(old), len(new)))
    return lines


def target(records):
    return next(r for r in records if r["method"] == "POST" and r["path"].split("?")[0] == "/v1/messages" and "metadata" in body_of(r))


def set_header(record, name, value):
    key = next(k for k in record["headers"] if k.lower() == name)
    record["headers"][key] = value


def edit_body(record, change):
    body = json.loads(record["body"])
    change(body)
    record["body"] = json.dumps(body)


def swap_first_header_case(record):
    items = list(record["headers"].items())
    record["headers"] = dict([(items[0][0].swapcase(), items[0][1])] + items[1:])


def swap_first_two_headers(record):
    items = list(record["headers"].items())
    record["headers"] = dict([items[1], items[0]] + items[2:])


def plant_cache_ttl(body):
    blocks = listed(body, "system") + listed(body, "tools") + [c for m in listed(body, "messages") if isinstance(m, dict) and isinstance(m.get("content"), list) for c in m["content"]]
    block = next(b for b in blocks if isinstance(b, dict) and "cache_control" in b)
    block["cache_control"] = dict(block["cache_control"], ttl="planted")


def drop_user_id_key(body):
    user_id = json.loads(body["metadata"]["user_id"])
    user_id.pop(next(iter(user_id)))
    body["metadata"]["user_id"] = json.dumps(user_id)


def drop_target_class(records):
    key = class_key(target(records))
    records[:] = [r for r in records if class_key(r) != key]


# (name, the one BLOCK field expected or None, a RECORD name expected or None, plant)
FAULTS = [
    ("identical", None, None, lambda rs: None),
    ("user-agent", "user-agent", None, lambda rs: set_header(target(rs), "user-agent", header(target(rs), "user-agent").replace(")", ", workload/planted)"))),
    ("header-case", "header-names", None, lambda rs: swap_first_header_case(target(rs))),
    ("header-order", "header-names", None, lambda rs: swap_first_two_headers(target(rs))),
    ("betas", "betas", None, lambda rs: set_header(target(rs), "anthropic-beta", ",".join(header(target(rs), "anthropic-beta").split(",")[:-1]))),
    ("cache", "cache", None, lambda rs: edit_body(target(rs), plant_cache_ttl)),
    ("metadata", "metadata", None, lambda rs: edit_body(target(rs), drop_user_id_key)),
    ("billing", "billing", None, lambda rs: edit_body(target(rs), lambda b: b.__setitem__("system", [{"type": "text", "text": BILLING + " cc_version=0.0.0.000; cc_entrypoint=planted; cch=00000;"}] + b["system"]))),
    ("refused", "refused", None, lambda rs: target(rs).update(status=400, error_body=REFUSAL)),
    ("class-missing", "class-missing", None, drop_target_class),
    ("thread", None, "thread", lambda rs: edit_body(target(rs), lambda b: b.__setitem__("thread", {"type": "planted"}))),
    ("prompt-text", None, "prompt-text", lambda rs: edit_body(target(rs), lambda b: b["system"][-1].__setitem__("text", b["system"][-1]["text"] + " planted"))),
]


def self_test(path):
    with open(path, encoding="utf-8") as handle:
        records = [json.loads(line) for line in handle]
    failures = 0
    for name, block, record, plant in FAULTS:
        planted = json.loads(json.dumps(records))
        try:
            plant(planted)
            lines = compare({"f8": {"client": records}}, {"f8": {"client": planted}})
        except (KeyError, IndexError, TypeError, ValueError, StopIteration) as error:
            print("SELFTEST FAIL %s: %r" % (name, error))
            failures += 1
            continue
        blocks = sorted({line.split(" ")[1] for line in lines if line.startswith("BLOCK ")})
        seen = {line.split(" ")[1] for line in lines if line.startswith("RECORD ")}
        if blocks == ([block] if block else []) and (record is None or record in seen):
            print("SELFTEST ok %s" % name)
        else:
            print("SELFTEST FAIL %s: BLOCK fields %s, RECORD names %s" % (name, blocks, sorted(seen)))
            failures += 1
    return 1 if failures else 0


def names(old_bin, new_bin):
    with open(old_bin, "rb") as handle:
        old = handle.read()
    with open(new_bin, "rb") as handle:
        new = handle.read()
    for kind, pattern in (("env-name", ENV_NAME), ("setting-name", SETTING_NAME)):
        before, after = set(pattern.findall(old)), set(pattern.findall(new))
        for name in sorted(before - after):
            print("RECORD %s removed %s" % (kind, name.decode()))
        for name in sorted(after - before):
            print("RECORD %s added %s" % (kind, name.decode()))
    for token in KEEPALIVE:
        print("RECORD keep-alive %s old=%s new=%s" % (token.decode(), "present" if token in old else "absent", "present" if token in new else "absent"))
    return 0


def main(argv):
    if len(argv) == 3 and argv[1] == "--self-test":
        return self_test(argv[2])
    if len(argv) == 4 and argv[1] == "--names":
        return names(argv[2], argv[3])
    if len(argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    allowed = set()
    if os.path.exists(argv[3]):
        with open(argv[3], encoding="utf-8") as handle:
            allowed = {line.rstrip("\n") for line in handle if line.startswith("BLOCK ") and not line.startswith("BLOCK refused ")}
    unallowed = 0
    for line in compare(load(argv[1]), load(argv[2])):
        if line in allowed:
            line = "ALLOWED" + line[len("BLOCK"):]
        elif line.startswith("BLOCK "):
            unallowed += 1
        print(line)
    print("RESULT %s: %d BLOCK lines not allowed" % ("PASS" if unallowed == 0 else "FAIL", unallowed))
    return 0 if unallowed == 0 else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
