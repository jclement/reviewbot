#!/usr/bin/env bash
# Renders /review/out/index.html from whatever findings exist so far.
# Called repeatedly during a review (live updates) and once at the end
# (final render). The host's HTTP server watches the file and pushes an
# SSE "reload" to the open browser tab when it changes.
#
# Errors here are surfaced — the orchestrator's rerender_loop captures
# stdout+stderr to /review/out/render.log so the host can inspect them.
set -uo pipefail
# Note: NOT -e. We want partial / corrupt findings files to be skipped,
# not abort the whole render.

PHASE="${1:-in-progress}"     # starting | in-progress | complete
BRANCH="${2:-?}"
PARENT="${3:-?}"
MERGE_BASE="${4:-?}"

OUT=/review/out
FINDINGS=/review/findings
TMP="$OUT/.index.html.tmp"

mkdir -p "$OUT"

# Pull all findings JSON into one combined data structure that the
# template embeds. Renders are cheap; do this every time.
python3 - "$PHASE" "$BRANCH" "$PARENT" "$MERGE_BASE" <<'PY' > "$TMP"
import json, os, sys, html, glob, time, datetime

phase, branch, parent, merge_base = sys.argv[1:5]
rendered_at = datetime.datetime.now().strftime("%H:%M:%S")
rendered_epoch = int(time.time())
personality = (os.environ.get("REVIEWBOT_PERSONALITY") or "").strip().lower()
if personality not in ("sexy", "angry", "sarcastic", "butler"):
    personality = ""

FINDINGS = "/review/findings"
RAW = "/review/raw"

# ── Load every agent's findings ─────────────────────────────────────────
agents = []
for fp in sorted(glob.glob(os.path.join(FINDINGS, "*.json"))):
    name = os.path.basename(fp)
    if name.endswith(".meta.json"): continue
    try:
        with open(fp) as f: data = json.load(f)
    except Exception as e:
        data = {"agent": name.replace(".json",""), "summary": f"parse error: {e}", "findings": []}
    agents.append(data)

# ── Pull the consolidator if present ────────────────────────────────────
consolidated = None
spot_test_plan = None
for a in agents:
    if a.get("agent") == "consolidator":
        consolidated = a
    elif a.get("agent") == "spot-test-plan":
        spot_test_plan = a

# ── Aggregate cost / tokens ─────────────────────────────────────────────
total_cost = 0.0
total_in = total_out = total_cr = total_cw = 0
total_duration_ms = 0
agent_costs = []
for a in agents:
    meta = a.get("_meta") or {}
    cost = float(meta.get("cost_usd") or 0)
    inp  = int(meta.get("input_tokens") or 0)
    out  = int(meta.get("output_tokens") or 0)
    cr   = int(meta.get("cache_read_tokens") or 0)
    cw   = int(meta.get("cache_write_tokens") or 0)
    dur  = int(meta.get("duration_ms") or 0)
    total_cost += cost
    total_in += inp; total_out += out; total_cr += cr; total_cw += cw
    total_duration_ms += dur
    agent_costs.append({
        "agent": a.get("agent"), "cost_usd": cost,
        "input_tokens": inp, "output_tokens": out,
        "cache_read_tokens": cr, "cache_write_tokens": cw,
        "duration_ms": dur,
    })

# ── Status of every expected agent (for live progress) ──────────────────
EXPECTED = [
    "supply-chain","security","subtle-bugs","performance","concurrency",
    "error-handling","tests","api-contract","code-quality","architecture",
    "observability","db-migrations","frontend","ui-ux","docs","configuration",
    "repo-hygiene","spot-test-plan","consolidator",
]
def status_of(agent_id):
    sf = os.path.join("/review/out/.status", agent_id)
    try:
        with open(sf) as f: return f.read().strip()
    except Exception: return "pending"

statuses = {a: status_of(a) for a in EXPECTED}
done_count = sum(1 for v in statuses.values() if v in ("done", "failed"))
total_count = len(EXPECTED)

# ── Severity counts (from consolidator if present, else summed) ─────────
sev_order = ["critical","high","medium","low","info"]
sev_counts = {s: 0 for s in sev_order}
if consolidated:
    sc = consolidated.get("stats_by_severity") or {}
    for s in sev_order: sev_counts[s] = int(sc.get(s) or 0)
else:
    for a in agents:
        for f in (a.get("findings") or []):
            s = (f.get("severity") or "info").lower()
            if s in sev_counts: sev_counts[s] += 1

# Risk
risk = (consolidated or {}).get("risk") or (
    "critical" if sev_counts["critical"] else
    "high"     if sev_counts["high"] else
    "medium"   if sev_counts["medium"] else
    "low"
)

# ── Diff stats ──────────────────────────────────────────────────────────
diff_stats = {"files_changed": 0, "insertions": 0, "deletions": 0}
try:
    with open("/review/diff-shortstat.txt") as f:
        line = f.read().strip()
    import re as _re
    m = _re.search(r"(\d+) files? changed", line); diff_stats["files_changed"] = int(m.group(1)) if m else 0
    m = _re.search(r"(\d+) insertions?", line);    diff_stats["insertions"]    = int(m.group(1)) if m else 0
    m = _re.search(r"(\d+) deletions?", line);     diff_stats["deletions"]     = int(m.group(1)) if m else 0
except Exception: pass

# ── Build the JS payload (single source of truth for the page) ──────────
payload = {
    "phase": phase,
    "branch": branch,
    "parent": parent,
    "merge_base": merge_base,
    "rendered_at": rendered_at,
    "rendered_epoch": rendered_epoch,
    "personality": personality,
    "diff_stats": diff_stats,
    "agents": agents,
    "agent_status": statuses,
    "agents_done": done_count,
    "agents_total": total_count,
    "consolidated": consolidated,
    "spot_test_plan": spot_test_plan,
    "severity_counts": sev_counts,
    "risk": risk,
    "cost": {
        "total_usd": round(total_cost, 4),
        "input_tokens": total_in,
        "output_tokens": total_out,
        "cache_read_tokens": total_cr,
        "cache_write_tokens": total_cw,
        "duration_ms": total_duration_ms,
        "by_agent": agent_costs,
    },
}

# ── Read the HTML template and inject the payload ───────────────────────
TEMPLATE_PATH = "/review/template.html"
with open(TEMPLATE_PATH) as f:
    template = f.read()

# We embed the JSON directly into index.html (so first-load is fully
# self-contained) AND write it to a sibling payload.json that the live
# poller can fetch. Escape the closing-script tag so an injected payload
# can't break out of the script element when embedded.
data_json = json.dumps(payload, ensure_ascii=False)
embedded  = data_json.replace("</", "<\\/")
with open("/review/out/.payload.json.tmp", "w") as f:
    f.write(data_json)
print(template.replace("/*__REVIEWBOT_PAYLOAD__*/", embedded))
PY

mv "$TMP" "$OUT/index.html"
mv "$OUT/.payload.json.tmp" "$OUT/payload.json"
