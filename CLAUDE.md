# megaclawd — Claude Code Guidelines

## Project Overview

Go CLI tool that launches Claude Code inside Docker containers with full autonomy, persistent tooling, and tmux multi-worker support.

## Development

- **Build:** `CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=dev" -o bin/megaclawd ./cmd/megaclawd`
- **Test:** `go test -v ./...`
- **Vet:** `go vet ./...`
- **Format:** `go fmt ./...`
- **Run:** `./bin/megaclawd start`

## Architecture

- `cmd/megaclawd/main.go` — Cobra CLI with subcommands (start, list, attach, destroy, etc.)
- `internal/config/` — Project config (YAML) + system prompt + `--init` scaffolding
- `internal/docker/` — Docker image lifecycle + session (container) management
  - `Dockerfile` — Ubuntu base image with dev tools (embedded via `//go:embed`)
  - `entrypoint.sh` — User setup, claude/mise bootstrap, tmux, cost summary (embedded)
  - `userinfo.go` — Cross-platform UID/GID/username detection
- `internal/ui/` — Terminal output formatting (TTY-aware colors, progress bars)
- `internal/taglines/` — Fun one-liners for CLI output
- `internal/updater/` — Self-update via GitHub Releases

## Rules

- CGO_ENABLED=0 always
- Run tests after every change
- Update README.md with code changes
- All exported functions need doc comments
- All Docker operations use `exec.Command("docker", ...)` — no Docker SDK dependency
- Cross-platform: code must work on Linux, macOS, and Windows
