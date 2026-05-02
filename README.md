<h1 align="center">🤖 reviewbot</h1>

<h3 align="center">Agentic code review for the current branch</h3>
<p align="center"><em>16+ specialist Claude agents, in parallel, in a sandbox, with a live HTML report</em></p>

---

## What it does

You're on a feature branch. You want a **real** code review before you open the
PR — not the kind that bikesheds variable names and misses the SQL injection.

`reviewbot` spins up a Docker container, computes the diff between your branch
and its parent (auto-detected), and fans out **16+ specialist Claude reviewer
agents** in parallel:

| Agent             | Hunts for |
| ----------------- | --------- |
| `supply-chain`    | Malicious / typosquatted / unpinned deps, install hooks, lockfile drift |
| `security`        | Injection, authn/authz holes, secrets, crypto misuse, SSRF, deserialization |
| `subtle-bugs`     | Off-by-one, nil deref, error swallowing, time zones, regex, copy-paste artifacts |
| `performance`     | N+1 queries, accidentally O(n²), missing indexes, memory bombs |
| `concurrency`     | Data races, deadlocks, channel leaks, async/await traps |
| `error-handling`  | Silently dropped errors, wrong recovery, leaked resources on failure |
| `tests`           | Coverage gaps that *matter*, fragile snapshots, sleep-based waits |
| `api-contract`    | Breaking changes to HTTP / SDK / CLI / config / DB schema |
| `code-quality`    | Real complexity hotspots (anti-pedantry rules baked in) |
| `architecture`    | Layering violations, god objects, premature abstractions |
| `observability`   | Missing logs/metrics on new code paths, high-cardinality labels |
| `db-migrations`   | Lock-the-table risks, missing indexes, unsafe backfills |
| `frontend`        | a11y, React hook bugs, render perf, missing loading/error states |
| `ui-ux`           | Microcopy, info hierarchy, friction, design-system consistency, undo |
| `docs`            | Stale README, missing CHANGELOG, broken examples |
| `configuration`   | Insecure Dockerfiles, k8s defaults, GHA pinning, weak TLS |
| `repo-hygiene`    | Secrets / credentials / `node_modules` accidentally committed |
| `spot-test-plan`  | A short, prioritized hands-on test plan for QA |
| `consolidator`    | Dedupes, re-ranks, and writes the executive summary |

Then it generates a **self-contained HTML report** with collapsible sections,
syntax-highlighted snippets, severity badges, a clickable filter, the
spot-test plan, and a per-agent token-cost breakdown — and **opens it in your
browser**. The page **live-reloads** as each agent completes (Server-Sent
Events + a polling watcher on the report file).

When all agents finish, you're dropped into a `tmux` session with a Claude
instance pre-loaded with the full review context, so you can ask:

```
> draft a fix for the JWT alg=none finding
> show me everywhere we read user input into a SQL string
> the SSRF finding — am I actually reachable from the public internet?
```

It is explicitly designed to **not be CodeRabbit**: the agents have
anti-pedantry rules baked into every prompt, and the consolidator drops
findings that don't meaningfully clear the bar.

## Quick start

```bash
# 1. Install Docker.
# 2. Install reviewbot:
brew install jclement/tap/reviewbot     # or: go install github.com/jclement/reviewbot/cmd/reviewbot@latest

# 3. Check out your feature branch and run:
cd my-project
reviewbot
```

First run builds the container image (~2 min) and installs Claude Code inside
it (~30s, cached in a Docker volume). After that, runs start in seconds.

## Commands

```text
reviewbot                  Review the current dir vs detected parent branch
reviewbot <path>           Review a specific project
reviewbot --base develop   Override the auto-detected parent branch
reviewbot chat             Re-attach to a finished review's follow-up chat
reviewbot list             List active review containers
reviewbot stop [name]      Stop / clean up review containers
reviewbot doctor           Check Docker + image health
reviewbot init             Scaffold .reviewbot/ in the current project
reviewbot clean            Remove image, volume, and all containers
```

### Flags

| Flag | What it does |
| ---- | ------------ |
| `--base BRANCH` | Override the auto-detected parent branch |
| `--rebuild` | Force rebuild of the docker image |
| `--no-browser` | Don't open the browser automatically |
| `--no-chat` | Skip the post-review tmux follow-up chat |
| `--arch ARCH` | Container CPU arch (`amd64`, `arm64`); default = host |
| `--personality MODE` | Tone for summaries + report theme: `sexy`, `angry`, `sarcastic`, or `butler` (findings stay neutral) |

## Parent-branch detection

In order:

1. `--base <branch>` flag (or `base_branch:` in `.reviewbot/config.yaml`)
2. `origin/HEAD` (the remote's default branch)
3. First of `main`, `master`, `develop`, `trunk` that exists locally or on origin
4. Fallback: `HEAD~1`

The merge-base between your branch and that parent is the line we draw.
Everything you've added since that point gets reviewed.

## How the live report works

```
┌────────────────────────┐         ┌──────────────────────────────┐
│  Your terminal         │         │  Browser (auto-opened)       │
│                        │         │                              │
│  reviewbot             │         │  http://127.0.0.1:RANDOM/    │
│  ├─ HTTP server        │◀── SSE──│  index.html                  │
│  ├─ File watcher       │         │  • severity cards            │
│  └─ docker run -d ─────┼───────▶ │  • live agent grid           │
│                        │         │  • findings (filterable)     │
│                        │         │  • spot-test plan            │
│                        │         │  • cost breakdown            │
└────────────────────────┘         └──────────────────────────────┘
            │
            │ writes index.html to a temp dir
            ▼
┌────────────────────────────────────────┐
│  Container (the orchestrator)          │
│                                        │
│  1. detect parent branch               │
│  2. compute diff bundle                │
│  3. spawn 16+ claude -p agents (xargs) │
│  4. on each agent finish, re-render    │
│     index.html → host watcher fires    │
│     SSE → browser smooth-reloads       │
│  5. spot-test-plan agent runs          │
│  6. consolidator agent runs            │
│  7. drop into tmux with claude loaded  │
│     for follow-up Q&A                  │
└────────────────────────────────────────┘
```

The HTML uses Tailwind v3 + Typography (CDN), highlight.js for
syntax-highlighted code, and `marked` for markdown. The browser tab keeps
working after the container exits — the report is just a file.

## Project-specific rules

`reviewbot init` creates a `.reviewbot/` directory in your project:

```
.reviewbot/
├── config.yaml      # base_branch override, parallelism, skipped agents
└── CLAUDE.md        # rules merged into every agent's system prompt
```

Use `CLAUDE.md` for things only your team knows:

```markdown
- The `/internal/billing/` package is touched by audit; any change there
  must include a CHANGELOG entry naming the ticket.

- We use `pgx` directly, not `database/sql`. Any new code that imports
  `database/sql` is a finding, even if the diff compiles.

- Treat any change that logs raw email addresses as `critical` — we're under
  a consent decree from 2024-Q3.
```

These get prepended to every agent's prompt.

## Cost & performance

Each agent runs as a separate `claude --output-format json -p` invocation,
and reviewbot captures `total_cost_usd` and token usage from the response
wrapper. The HTML report shows total cost, per-agent cost, and a token
breakdown (input / output / cache read / cache write).

A typical small-PR review (50–200 lines of diff) tends to land between
$0.20 and $1.50 depending on how deep the agents need to dig — the project
tree is read on demand, not all up-front.

Default parallelism is 8; override via `parallel: 16` in `.reviewbot/config.yaml`
or `REVIEWBOT_PARALLEL=16 reviewbot`.

## Development

```bash
mise run build        # production build
mise run test         # unit tests
mise run lint         # go vet + staticcheck
mise run fmt          # go fmt
mise run dev          # build + --help
```

The container assets (Dockerfile, entrypoint, orchestrator, render script,
HTML template, all 18 agent prompts) are embedded into the Go binary via
`//go:embed`. Any change to them invalidates the image content hash and
the next `reviewbot` run rebuilds the image automatically.

## License

MIT.
