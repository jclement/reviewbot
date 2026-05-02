#!/usr/bin/env python3
"""Parse the wrapper JSON that `claude --output-format json` writes,
extract the inner agent JSON, and emit two files:
  - <out_file>:  the agent's normalized findings JSON
  - <meta_file>: tokens + cost + duration

Usage: parse_agent_output.py <raw_file> <out_file> <meta_file> <agent_id>
"""
import json, os, re, sys

raw_path, out_path, meta_path, agent_id = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]

with open(raw_path) as f:
    text = f.read().strip()

# Step 1: parse claude's wrapper.
result_text = text
meta = {
    "agent": agent_id,
    "cost_usd": 0.0,
    "input_tokens": 0,
    "output_tokens": 0,
    "cache_read_tokens": 0,
    "cache_write_tokens": 0,
    "duration_ms": 0,
}
try:
    wrapper = json.loads(text)
    if isinstance(wrapper, dict) and ("result" in wrapper or "type" in wrapper):
        result_text = wrapper.get("result") or wrapper.get("content") or ""
        meta["cost_usd"] = float(wrapper.get("total_cost_usd") or wrapper.get("cost_usd") or 0.0)
        meta["duration_ms"] = int(wrapper.get("duration_ms") or 0)
        usage = wrapper.get("usage") or {}
        meta["input_tokens"]       = int(usage.get("input_tokens") or 0)
        meta["output_tokens"]      = int(usage.get("output_tokens") or 0)
        meta["cache_read_tokens"]  = int(usage.get("cache_read_input_tokens") or 0)
        meta["cache_write_tokens"] = int(usage.get("cache_creation_input_tokens") or 0)
except Exception:
    pass

# Step 2: extract the agent's JSON object out of result_text.
inner = result_text or ""
inner = re.sub(r"^```(?:json)?\s*", "", inner.strip(), flags=re.MULTILINE)
inner = re.sub(r"```\s*$", "", inner.strip(), flags=re.MULTILINE)


def find_json(s):
    candidates = []
    for i, c in enumerate(s):
        if c != "{":
            continue
        depth, in_str, esc = 0, False, False
        for j in range(i, len(s)):
            d = s[j]
            if in_str:
                if esc:
                    esc = False
                elif d == "\\":
                    esc = True
                elif d == '"':
                    in_str = False
                continue
            if d == '"':
                in_str = True
            elif d == "{":
                depth += 1
            elif d == "}":
                depth -= 1
                if depth == 0:
                    candidates.append(s[i:j + 1])
                    break
    candidates.sort(key=len, reverse=True)
    for c in candidates:
        try:
            return json.loads(c)
        except Exception:
            pass
    return None


obj = find_json(inner)
if obj is None:
    obj = {
        "agent": agent_id,
        "summary": (inner[:600] + "...") if inner else "No output produced by claude.",
        "findings": [],
        "_no_json_parse": True,
    }
obj.setdefault("agent", agent_id)
obj.setdefault("summary", "")
obj.setdefault("findings", [])
obj["_meta"] = meta

with open(out_path, "w") as f:
    json.dump(obj, f, indent=2)
with open(meta_path, "w") as f:
    json.dump(meta, f, indent=2)
