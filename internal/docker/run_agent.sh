#!/usr/bin/env bash
# Run a single review agent. Called by the orchestrator (typically via xargs
# for parallelism). Reads required env vars from the orchestrator:
#
#   FINDINGS, RAW, AGENTS, OUT, PERSONALITY_PROMPT
#
# Argument: agent ID (matches /review/agents/<id>.md).
set -uo pipefail

id="${1:?usage: run_agent.sh <agent-id>}"

prompt_file="$AGENTS/$id.md"
out_file="$FINDINGS/$id.json"
raw_file="$RAW/$id.log"
err_file="$RAW/$id.err.log"
parse_err_file="$RAW/$id.parse-err.log"
meta_file="$FINDINGS/$id.meta.json"
status_file="$OUT/.status/$id"

mkdir -p "$OUT/.status" "$RAW" "$FINDINGS"
echo "running" > "$status_file"
started_at=$(date +%s)
printf '\033[1;36m▸\033[0m \033[1m%-18s\033[0m \033[2mstarting\033[0m\n' "$id" >&2

if [[ ! -f "$prompt_file" ]]; then
    echo "missing-prompt" > "$status_file"
    printf '\033[1;31m✖\033[0m \033[1m%-18s\033[0m missing prompt file\n' "$id" >&2
    exit 0
fi

# Build the system prompt: shared rules + agent role + (optional)
# personality + path map.
sys_prompt="$(cat "$AGENTS/_shared.md")

$(cat "$prompt_file")

${PERSONALITY_PROMPT:-}

## Path map (you have access to all of these)

- /review/diff.patch          — the diff to review (this is your input)
- /review/files-changed.txt   — list of changed files
- /review/branch.txt          — branch + parent + merge-base SHA
- /review/repo-meta.json      — detected languages / context files
- /review/agents/             — all agent prompts (for cross-reference)
- /workspace/                 — full project tree at HEAD (read-only intent)

## Your agent ID is: $id

Begin. Output ONLY the JSON object (no markdown fences, no preamble). Use up
to 60 minutes; the orchestrator will wait."

user_prompt="Review the diff at /review/diff.patch as the '$id' specialist.
Use the project tree at /workspace for context. Output JSON per the contract."

# Run claude. We capture its stdout to RAW for debugging + post-processing.
claude --dangerously-skip-permissions \
    --append-system-prompt "$sys_prompt" \
    --output-format json \
    -p "$user_prompt" \
    > "$raw_file" 2>"$err_file"
rc=$?

if [[ $rc -ne 0 ]]; then
    jq -n --arg id "$id" --arg log "$(tail -50 "$err_file" 2>/dev/null)" \
        '{agent:$id, summary:"Agent failed to run.", findings:[], _error:$log}' \
        > "$out_file"
    printf '{"agent":"%s","cost_usd":0,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"duration_ms":0,"failed":true}\n' "$id" > "$meta_file"
    echo "failed" > "$status_file"
    dur=$(( $(date +%s) - started_at ))
    printf '\033[1;31m✖\033[0m \033[1m%-18s\033[0m failed after %ds (see raw/%s.err.log)\n' "$id" "$dur" "$id" >&2
    exit 0
fi

# Parse claude's wrapper JSON → findings + meta.
if ! python3 /review/parse_agent_output.py \
        "$raw_file" "$out_file" "$meta_file" "$id" 2>"$parse_err_file"; then
    # Parser itself crashed — wrap raw output as a fallback finding so
    # the user can see what claude returned and where the parser stumbled.
    jq -n --arg id "$id" \
          --arg raw "$(head -c 4000 "$raw_file" 2>/dev/null)" \
          --arg perr "$(cat "$parse_err_file" 2>/dev/null)" \
        '{agent:$id,
          summary:"Output parser failed — see raw log.",
          findings:[],
          _parser_error:$perr,
          _raw_truncated:$raw}' > "$out_file"
fi

echo "done" > "$status_file"

# ── Summary line: duration, finding count + worst severity, cost ──────────
dur=$(( $(date +%s) - started_at ))
n_findings=$(jq '.findings | length' "$out_file" 2>/dev/null)
[[ -z "$n_findings" || "$n_findings" == "null" ]] && n_findings=0

worst=$(jq -r '[.findings[]?.severity] | (
    if any(. == "critical") then "critical"
    elif any(. == "high")   then "high"
    elif any(. == "medium") then "medium"
    elif any(. == "low")    then "low"
    elif any(. == "info")   then "info"
    else "—" end)' "$out_file" 2>/dev/null)
[[ -z "$worst" || "$worst" == "null" ]] && worst="—"

cost=$(jq -r '.cost_usd // 0' "$meta_file" 2>/dev/null)
[[ -z "$cost" || "$cost" == "null" ]] && cost="0"
cost=$(printf '%.4f' "$cost" 2>/dev/null || echo "0.0000")

sev_color="\033[0;90m"
case "$worst" in
    critical) sev_color="\033[1;31m" ;;
    high)     sev_color="\033[1;33m" ;;
    medium)   sev_color="\033[1;33m" ;;
    low)      sev_color="\033[1;34m" ;;
    info)     sev_color="\033[0;37m" ;;
esac

printf '\033[1;32m✔\033[0m \033[1m%-18s\033[0m \033[2m%4ds\033[0m  %s%-8s\033[0m  %s findings   \033[2m$%s\033[0m\n' \
    "$id" "$dur" "$sev_color" "$worst" "$n_findings" "$cost" >&2
