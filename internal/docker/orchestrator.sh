#!/usr/bin/env bash
# reviewbot orchestrator. Runs INSIDE the container as the dev user.
#
#   1. Detects parent branch + computes diff into /review.
#   2. Generates a skeleton index.html in /review/out so the host's HTTP
#      server can immediately open the browser.
#   3. Fans out specialist agents in parallel via `claude -p`.
#   4. After each agent finishes, regenerates index.html so the live
#      page picks up the new findings.
#   5. Runs the spot-test-plan agent (fed the in-flight findings).
#   6. Runs the consolidator agent.
#   7. Renders the final index.html.
#   8. Hands off to a tmux session attached to a Claude instance loaded
#      with the full review for follow-up Q&A.
set -euo pipefail

OUT=/review/out
FINDINGS=/review/findings
RAW=/review/raw
AGENTS=/review/agents
WS=/workspace

mkdir -p "$OUT" "$FINDINGS" "$RAW"

# ── Parse args ───────────────────────────────────────────────────────────
BASE_BRANCH="${REVIEWBOT_BASE:-}"
CHAT_ONLY="false"
for arg in "$@"; do
    case "$arg" in
        --chat-only) CHAT_ONLY="true" ;;
        --base) :;;
        *)
            if [[ "${PREV:-}" == "--base" ]]; then BASE_BRANCH="$arg"; fi
            ;;
    esac
    PREV="$arg"
done

# ── Helpers ──────────────────────────────────────────────────────────────
log() { printf '\033[1;35m▸\033[0m %s\n' "$*" >&2; }
ok()  { printf '\033[1;32m✔\033[0m %s\n' "$*" >&2; }
warn(){ printf '\033[1;33m⚠\033[0m %s\n' "$*" >&2; }
err() { printf '\033[1;31m✖\033[0m %s\n' "$*" >&2; }

# Treat the workspace as a safe directory regardless of host owner.
git config --global --add safe.directory "$WS" 2>/dev/null || true

# Try a sequence of likely parent branches if the user didn't pick one.
detect_parent_branch() {
    local current
    current=$(cd "$WS" && git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "HEAD")

    # 1. Explicit override.
    if [[ -n "$BASE_BRANCH" ]]; then
        echo "$BASE_BRANCH"
        return
    fi

    # 2. The remote default branch (origin/HEAD).
    local remote_default
    remote_default=$(cd "$WS" && git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null \
        | sed 's@^refs/remotes/origin/@@' || true)
    if [[ -n "$remote_default" && "$remote_default" != "$current" ]]; then
        echo "$remote_default"; return
    fi

    # 3. First of: main, master, develop, trunk that exists locally and isn't us.
    for cand in main master develop trunk; do
        if [[ "$cand" == "$current" ]]; then continue; fi
        if (cd "$WS" && git rev-parse --verify --quiet "$cand" >/dev/null); then
            echo "$cand"; return
        fi
        if (cd "$WS" && git rev-parse --verify --quiet "origin/$cand" >/dev/null); then
            echo "origin/$cand"; return
        fi
    done

    # 4. Fall back to HEAD~1 so something still works.
    echo "HEAD~1"
}

# ── Discover diff source ────────────────────────────────────────────────
# Three modes:
#   1. branch (default): diff between HEAD and detected/--base parent
#   2. staged           : diff = `git diff --cached` (uncommitted-but-staged)
#   3. since N          : diff between HEAD and HEAD~N
# REVIEWBOT_STAGED=1 / REVIEWBOT_SINCE=N selects the alt modes.
CURRENT_BRANCH=$(cd "$WS" && git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "HEAD")

cd "$WS"
DIFF_MODE="branch"
if [[ -n "${REVIEWBOT_STAGED:-}" ]]; then
    DIFF_MODE="staged"
elif [[ -n "${REVIEWBOT_SINCE:-}" ]]; then
    DIFF_MODE="since"
fi

case "$DIFF_MODE" in
    staged)
        PARENT_BRANCH="(index)"
        MERGE_BASE=$(git rev-parse HEAD 2>/dev/null || echo "?")
        log "Mode:      reviewing staged (uncommitted) changes"
        git diff --no-color --cached  > /review/diff.patch
        git diff --name-only --cached > /review/files-changed.txt
        git diff --shortstat --cached > /review/diff-shortstat.txt
        echo "(uncommitted, staged)" > /review/commits.txt
        ;;
    since)
        N="${REVIEWBOT_SINCE}"
        if ! [[ "$N" =~ ^[0-9]+$ ]] || [[ "$N" -lt 1 ]]; then
            err "Invalid REVIEWBOT_SINCE=$N (need a positive integer)"
            exit 2
        fi
        PARENT_BRANCH="HEAD~$N"
        MERGE_BASE=$(git rev-parse "HEAD~$N" 2>/dev/null || echo "")
        if [[ -z "$MERGE_BASE" ]]; then
            err "HEAD~$N does not exist (only $(git rev-list --count HEAD) commits in history)"
            exit 2
        fi
        log "Mode:      reviewing last $N commits"
        git diff --no-color "$MERGE_BASE"..HEAD > /review/diff.patch
        git diff --name-only "$MERGE_BASE"..HEAD > /review/files-changed.txt
        git diff --shortstat "$MERGE_BASE"..HEAD > /review/diff-shortstat.txt
        git log --pretty=format:'%H %s' "$MERGE_BASE"..HEAD > /review/commits.txt
        ;;
    branch)
        PARENT_BRANCH=$(detect_parent_branch)
        MERGE_BASE=$(git merge-base "$PARENT_BRANCH" HEAD 2>/dev/null || echo "")
        if [[ -z "$MERGE_BASE" ]]; then
            err "Could not find a merge-base between HEAD and $PARENT_BRANCH"
            err "Use --base <branch> to pick the parent explicitly."
            exit 2
        fi
        git diff --no-color "$MERGE_BASE"..HEAD > /review/diff.patch
        git diff --name-only "$MERGE_BASE"..HEAD > /review/files-changed.txt
        git diff --shortstat "$MERGE_BASE"..HEAD > /review/diff-shortstat.txt
        git log --pretty=format:'%H %s' "$MERGE_BASE"..HEAD > /review/commits.txt
        ;;
esac

log "Branch:    $CURRENT_BRANCH"
log "Parent:    $PARENT_BRANCH"
log "Base SHA:  ${MERGE_BASE:0:12}"

cat > /review/branch.txt <<EOF
branch=$CURRENT_BRANCH
parent=$PARENT_BRANCH
merge_base=$MERGE_BASE
mode=$DIFF_MODE
EOF

# repo-meta.json: a tiny detection of language/build system for the agents.
python3 - <<'PY' > /review/repo-meta.json
import json, os
ws = "/workspace"
sig = {
    "Go": ["go.mod"],
    "Node/JS/TS": ["package.json"],
    "Python": ["pyproject.toml", "requirements.txt", "setup.py", "Pipfile"],
    "Rust": ["Cargo.toml"],
    "Ruby": ["Gemfile"],
    "Java/Maven": ["pom.xml"],
    "Java/Gradle": ["build.gradle", "build.gradle.kts"],
    "Swift": ["Package.swift"],
    ".NET": ["*.csproj", "*.fsproj", "*.sln"],
    "PHP/Composer": ["composer.json"],
    "Docker": ["Dockerfile", "compose.yaml", "compose.yml", "docker-compose.yml"],
    "Kubernetes": ["k8s/", "kubernetes/", "deploy/k8s/"],
    "Terraform": [".tf"],
    "GitHub Actions": [".github/workflows/"],
}
detected = []
for label, hints in sig.items():
    for h in hints:
        if h.startswith("."):
            for root, _, files in os.walk(ws):
                if any(f.endswith(h) for f in files):
                    detected.append(label); break
            else:
                continue
            break
        if h.endswith("/"):
            if os.path.isdir(os.path.join(ws, h.rstrip("/"))):
                detected.append(label); break
        elif "*" in h:
            import glob
            if glob.glob(os.path.join(ws, h)):
                detected.append(label); break
        else:
            if os.path.exists(os.path.join(ws, h)):
                detected.append(label); break
hints = []
for f in ("README.md", "CONTRIBUTING.md", "CLAUDE.md", "AGENTS.md", ".cursorrules"):
    if os.path.exists(os.path.join(ws, f)): hints.append(f)
print(json.dumps({"languages": sorted(set(detected)), "context_files": hints}, indent=2))
PY

NUM_FILES=$(wc -l < /review/files-changed.txt | tr -d ' ')
DIFF_BYTES=$(wc -c < /review/diff.patch | tr -d ' ')

if [[ "$NUM_FILES" -eq 0 ]]; then
    warn "No file changes detected between $PARENT_BRANCH and $CURRENT_BRANCH."
    warn "Are you on the right branch? Use --base to override the parent."
    # Still write a report so the user sees something.
fi

ok "Review bundle ready: $NUM_FILES files, $DIFF_BYTES bytes of diff"

# ── Personality (cosmetic; --personality flag) ──────────────────────────
# Personality colors the executive summary and per-agent summary fields
# only. Findings (titles, severity, file/line, snippets) stay neutral so
# engineers can act on them without a tone in the way.
PERSONALITY="${REVIEWBOT_PERSONALITY:-}"
PERSONALITY_PROMPT=""
case "$PERSONALITY" in
    sexy)
        PERSONALITY_PROMPT='## Tone (cosmetic, summary fields only)
The user invoked --personality sexy. In your `summary` field (and only
there), be warm, enthusiastic, and use a few heart/sparkle emojis (💕✨💋
🌸💖). Address the reviewer fondly ("darling", "love", "hon"). Findings
themselves — `title`, `severity`, `file`, `line`, `snippet`, `explanation`,
`fix`, `verification` — stay strictly professional. Do not let the tone
soften assessments: a critical bug is still critical.'
        ;;
    angry)
        PERSONALITY_PROMPT='## Tone (cosmetic, summary fields only)
The user invoked --personality angry. In your `summary` field (and only
there), channel BOFH energy: exasperated, weary, rude but competent. Mild
profanity allowed (damn, hell, garbage). Complain about the code on the way
to your conclusion. Findings themselves — `title`, `severity`, `file`,
`line`, `snippet`, `explanation`, `fix`, `verification` — stay strictly
professional. Do not let the tone inflate severity; rate honestly.'
        ;;
    sarcastic)
        PERSONALITY_PROMPT='## Tone (cosmetic, summary fields only)
The user invoked --personality sarcastic. In your `summary` field (and only
there), be dry, witty, and pointed. Use understatement. Findings themselves
— `title`, `severity`, `file`, `line`, `snippet`, `explanation`, `fix`,
`verification` — stay strictly professional and direct.'
        ;;
    butler)
        PERSONALITY_PROMPT='## Tone (cosmetic, summary fields only)
The user invoked --personality butler. In your `summary` field (and only
there), be formal, distinguished, and impeccably polite — a senior butler
addressing the master of the house. "If I may humbly suggest", "permit me
to observe", "with the utmost respect". Findings themselves — `title`,
`severity`, `file`, `line`, `snippet`, `explanation`, `fix`, `verification`
— stay strictly professional and direct (the butler does not mince words
when reporting an actual problem).'
        ;;
esac
[[ -n "$PERSONALITY" ]] && log "Personality: $PERSONALITY"

# ── Pick agents to run based on what's in the diff ──────────────────────
# Always-run agents inspect general code; file-typed agents only run when
# their kind of file is in the diff. This saves cost on small PRs without
# losing coverage (the always-run ones still cover the same territory at
# a different angle).
ALL_AGENTS=(
    supply-chain   security        subtle-bugs    performance    concurrency
    error-handling tests           api-contract   code-quality   architecture
    observability  db-migrations   frontend       ui-ux          docs
    configuration  repo-hygiene
)

# Always run regardless of diff content.
ALWAYS_RUN=(security subtle-bugs performance concurrency error-handling
            tests api-contract code-quality architecture observability
            repo-hygiene)

# Conditional agents — only run when their patterns match files-changed.txt.
# Patterns are extended-regex against full paths.
declare -A AGENT_TRIGGER=(
    [supply-chain]='(^|/)(package(-lock)?\.json|pnpm-lock\.yaml|yarn\.lock|go\.(mod|sum)|Cargo\.(toml|lock)|requirements.*\.txt|pyproject\.toml|Pipfile(\.lock)?|poetry\.lock|uv\.lock|Gemfile(\.lock)?|composer\.(json|lock)|pom\.xml|build\.gradle.*|Podfile.*|Package\.swift|.*\.csproj|flake\.nix|Dockerfile.*|.*\.dockerfile)$|^\.github/workflows/'
    [db-migrations]='\.sql$|/migrations?/|/schema\.(prisma|rb)$|/db/migrate/'
    [frontend]='\.(tsx|jsx|vue|svelte|html|htm|css|scss|sass|less)$|/components?/.*\.(ts|js)$'
    [ui-ux]='\.(tsx|jsx|vue|svelte|html|htm|css|scss|sass|less)$|/components?/.*\.(ts|js)$'
    [docs]='\.md$|^README|^CHANGELOG|^docs?/|/CONTRIBUTING'
    [configuration]='Dockerfile|\.dockerfile$|\.ya?ml$|\.tf$|\.tfvars$|\.toml$|\.ini$|\.conf$|/k8s/|/kubernetes/|/helm/|^\.github/workflows/|^\.env'
)

# Compute the run-set.
files_in_diff=$(cat /review/files-changed.txt 2>/dev/null)
AGENTS_TO_RUN=()
SKIPPED_AGENTS=()
for agent in "${ALL_AGENTS[@]}"; do
    keep=false
    # Always-run? in.
    for a in "${ALWAYS_RUN[@]}"; do
        if [[ "$a" == "$agent" ]]; then keep=true; break; fi
    done
    # Triggered by diff content? in.
    if [[ "$keep" == "false" ]]; then
        pat="${AGENT_TRIGGER[$agent]:-}"
        if [[ -n "$pat" ]] && echo "$files_in_diff" | grep -Eq "$pat"; then
            keep=true
        fi
    fi
    if [[ "$keep" == "true" ]]; then
        AGENTS_TO_RUN+=("$agent")
    else
        SKIPPED_AGENTS+=("$agent")
    fi
done

# Honor the user's per-project skip list (.reviewbot/config.yaml -> skip_agents).
# Passed in as REVIEWBOT_SKIP_AGENTS=foo,bar,baz
if [[ -n "${REVIEWBOT_SKIP_AGENTS:-}" ]]; then
    IFS=',' read -ra USER_SKIPS <<< "$REVIEWBOT_SKIP_AGENTS"
    new_run=()
    for a in "${AGENTS_TO_RUN[@]}"; do
        skip=false
        for s in "${USER_SKIPS[@]}"; do
            [[ "$a" == "$s" ]] && skip=true && break
        done
        if [[ "$skip" == "true" ]]; then
            SKIPPED_AGENTS+=("$a")
        else
            new_run+=("$a")
        fi
    done
    AGENTS_TO_RUN=("${new_run[@]}")
fi

# Surface skipped agents to the report and write a sentinel for each so
# the renderer can show them as "skipped" in the agent grid.
mkdir -p /review/out/.status
for s in "${SKIPPED_AGENTS[@]}"; do
    echo "skipped" > "/review/out/.status/$s"
done

if (( ${#SKIPPED_AGENTS[@]} > 0 )); then
    log "Running ${#AGENTS_TO_RUN[@]}/${#ALL_AGENTS[@]} agents (skipped: ${SKIPPED_AGENTS[*]})"
else
    log "Running all ${#AGENTS_TO_RUN[@]} agents"
fi

# ── Skeleton report so the browser opens immediately ─────────────────────
/review/render.sh "starting" "$CURRENT_BRANCH" "$PARENT_BRANCH" "$MERGE_BASE"

# ── Fan out specialist agents in parallel ───────────────────────────────
log "Spawning ${#AGENTS_TO_RUN[@]} specialist agents in parallel..."

# run_agent lives in /review/run_agent.sh — invoked directly via xargs.
# Exporting it as a bash function caused issues: the embedded heredoc /
# python source confused bash's exported-function re-parser in the child
# subshells xargs spawns ("syntax error near unexpected token `fi'").
# A standalone script avoids the export round-trip entirely.

# All vars run_agent.sh needs are exported below.
export FINDINGS RAW AGENTS OUT PERSONALITY_PROMPT

# Trigger a re-render whenever a new agent finishes.
# Errors from render.sh are appended to /review/out/render.log so they're
# host-visible. Bash `set -e` is dialed back inside the loop so a single
# bad render doesn't kill the whole loop.
rerender_loop() {
    set +e
    local render_errs=/review/out/render.log
    : > "$render_errs"
    local n=0
    while true; do
        sleep 2
        if [[ -f /review/out/.stop_rerender ]]; then return; fi
        # Mirror raw + findings to host-visible dir so they're inspectable
        # mid-run if the report ever looks off.
        mkdir -p /review/out/raw /review/out/findings 2>/dev/null
        cp -f /review/raw/*           /review/out/raw/      2>/dev/null
        cp -f /review/findings/*.json /review/out/findings/ 2>/dev/null
        n=$((n+1))
        if ! /review/render.sh "in-progress" "$CURRENT_BRANCH" "$PARENT_BRANCH" "$MERGE_BASE" \
                >> "$render_errs" 2>&1; then
            echo "[$(date +%H:%M:%S)] render #$n FAILED — see above" >> "$render_errs"
            # Surface the failure to the user the first three times only
            # (so we don't spam if every render is broken).
            if [[ $n -le 3 ]]; then
                err "render #$n failed — check ~/.cache/reviewbot/runs/<id>/render.log"
            fi
        fi
    done
}
rerender_loop &
RERENDER_PID=$!

# Periodic heartbeat so the user sees something while agents are thinking.
# The list of "in flight" agents is derived from the .status sentinel files
# the renderer already maintains.
heartbeat_loop() {
    local hb_started=$(date +%s)
    while true; do
        sleep 15
        if [[ -f /review/out/.stop_heartbeat ]]; then return; fi
        local total=${1:-?}
        local done_n=0 in_flight=()
        for sf in /review/out/.status/*; do
            [[ -f "$sf" ]] || continue
            local state=$(cat "$sf" 2>/dev/null)
            local id=$(basename "$sf")
            if [[ "$state" == "done" || "$state" == "failed" ]]; then
                done_n=$((done_n+1))
            elif [[ "$state" == "running" ]]; then
                in_flight+=("$id")
            fi
        done
        local elapsed=$(( $(date +%s) - hb_started ))
        local list="${in_flight[*]:-—}"
        # Truncate the list so a long line doesn't spam.
        if [[ ${#list} -gt 90 ]]; then list="${list:0:87}..."; fi
        printf '\033[0;90m   ⋯ %ds elapsed · %d/%d done · in flight: %s\033[0m\n' \
            "$elapsed" "$done_n" "$total" "$list" >&2
    done
}
heartbeat_loop "${#AGENTS_TO_RUN[@]}" &
HEARTBEAT_PID=$!
export CURRENT_BRANCH PARENT_BRANCH MERGE_BASE

# We want a controlled concurrency. Default to 8 simultaneous agents; let
# the user override via REVIEWBOT_PARALLEL.
PARALLEL="${REVIEWBOT_PARALLEL:-8}"

printf '%s\n' "${AGENTS_TO_RUN[@]}" | xargs -P"$PARALLEL" -I{} /review/run_agent.sh {}

# Stop the heartbeat now that the parallel phase is over; the next two
# agents (spot-test-plan, consolidator) run sequentially with their own log lines.
touch /review/out/.stop_heartbeat
wait "$HEARTBEAT_PID" 2>/dev/null || true

# ── Scoreboard: per-agent results so the user can see what happened ─────
echo "" >&2
printf '\033[1;37m🏁 Specialist results\033[0m\n' >&2
total_findings=0
for agent_id in "${AGENTS_TO_RUN[@]}"; do
    out="$FINDINGS/$agent_id.json"
    if [[ ! -f "$out" ]]; then
        printf '   \033[1;31m✖\033[0m %-18s \033[1;31mno output file\033[0m\n' "$agent_id" >&2
        continue
    fi
    n=$(jq '.findings | length' "$out" 2>/dev/null || echo 0)
    total_findings=$((total_findings + n))
    summary=$(jq -r '.summary // "(no summary)"' "$out" 2>/dev/null | head -c 120 | tr '\n' ' ')
    parse_err=$(jq -r 'if (._parser_error or ._no_json_parse or ._raw_truncated) then "yes" else "no" end' "$out" 2>/dev/null)
    badge="\033[0;90m—\033[0m"
    if [[ "$parse_err" == "yes" ]]; then
        badge="\033[1;33m⚠ parse\033[0m"
    fi
    printf '   \033[1;36m%2d\033[0m %-18s %b  \033[2m%s\033[0m\n' "$n" "$agent_id" "$badge" "$summary" >&2
done
printf '\033[1;37m   total findings across specialists: %d\033[0m\n' "$total_findings" >&2
echo "" >&2

# Mirror raw + parse logs to the host-visible output dir so the user can
# debug via `cat ~/.cache/reviewbot/runs/<id>/raw/<agent>.log` if anything
# looks off in the report.
mkdir -p /review/out/raw
cp -f "$RAW"/* /review/out/raw/ 2>/dev/null || true
mkdir -p /review/out/findings
cp -f "$FINDINGS"/* /review/out/findings/ 2>/dev/null || true

ok "All specialist agents complete."

# ── Spot-test-plan (sees other agents' findings) ────────────────────────
log "Generating spot-test plan..."
/review/run_agent.sh "spot-test-plan"
ok "Spot-test plan ready."

# ── Consolidator ────────────────────────────────────────────────────────
log "Consolidating findings..."
/review/run_agent.sh "consolidator"
ok "Consolidation complete."

# Stop the periodic re-render and do one final render.
touch /review/out/.stop_rerender
wait "$RERENDER_PID" 2>/dev/null || true

# Mirror the final findings (spot-test-plan + consolidator ran *after* the
# rerender_loop stopped, so they weren't picked up by it). Without this,
# ~/.cache/reviewbot/runs/<id>/findings/ would be missing the two most
# important files for follow-up debugging.
mkdir -p /review/out/raw /review/out/findings
cp -f "$RAW"/*           /review/out/raw/      2>/dev/null
cp -f "$FINDINGS"/*.json /review/out/findings/ 2>/dev/null

/review/render.sh "complete" "$CURRENT_BRANCH" "$PARENT_BRANCH" "$MERGE_BASE"
touch /review/out/.done

ok "Report ready: /review/out/index.html"

# ── Drop into follow-up Claude session inside tmux ──────────────────────
if [[ "$CHAT_ONLY" == "true" ]]; then
    log "Chat-only mode."
fi

cat > /review/followup-prompt.md <<EOF
You are the post-review follow-up assistant. The user just received a code-review
report on branch '$CURRENT_BRANCH' (parent '$PARENT_BRANCH').

You have access to:
- /review/diff.patch              — the reviewed diff
- /review/findings/*.json         — every agent's findings
- /review/findings/consolidator.json — the consolidated report
- /review/findings/spot-test-plan.json — the spot-test plan
- /workspace/                     — the project tree

Help the user explore findings, dig deeper into specific issues, draft fixes,
or run additional ad-hoc analyses. Be concise. When citing a finding, cite by
title and severity. The user has already seen the HTML report.
EOF

# tmux config so ctrl+b is the prefix and the user has a sane env.
cat > "$HOME/.tmux.conf" <<'TMUX_EOF'
set -g default-terminal "screen-256color"
set -ga terminal-overrides ",*256col*:Tc"
set -g mouse on
set -g base-index 1
setw -g pane-base-index 1
set -sg escape-time 0
set -g status-style bg=colour235,fg=colour255
set -g status-left-length 50
set -g status-left "#[fg=colour232,bg=colour39,bold] reviewbot #[fg=colour39,bg=colour235,nobold]"
set -g status-right "#[fg=colour245] follow-up chat — ctrl+b d to detach "
bind | split-window -h -c "#{pane_current_path}"
bind - split-window -v -c "#{pane_current_path}"
TMUX_EOF

CLAUDE_CMD="$HOME/.local/bin/claude --dangerously-skip-permissions --append-system-prompt \"\$(cat /review/followup-prompt.md)\""

log "Starting follow-up chat in tmux (detached) — host will attach via docker exec..."
# -d = detached. The orchestrator runs without a controlling TTY (we were
# launched via `docker run -d`), so creating an *attached* tmux session
# fails with "open terminal failed: not a terminal". The host attaches to
# this detached session via `docker exec -it ... tmux attach-session -t reviewbot`.
tmux new-session -d -s reviewbot -n chat \
    "cd /workspace && bash -c '$CLAUDE_CMD'" || {
        warn "tmux failed to start follow-up chat — keeping container alive 5min so the report stays reachable"
        sleep 300
        exit 0
    }

# Keep container alive while tmux still has sessions.
while tmux has-session -t reviewbot 2>/dev/null; do
    sleep 1
done

ok "Follow-up chat session ended."
