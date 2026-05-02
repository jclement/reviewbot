#!/usr/bin/env bash
# reviewbot container entrypoint.
#
# Responsibilities:
#   1. Create a non-root user matching the host UID/GID so writes to the
#      output dir don't end up root-owned on the host.
#   2. Bootstrap Claude Code CLI (cached in a Docker volume).
#   3. Hand off to the orchestrator, which fans out the agents.
#   4. After agents finish, drop into a tmux session attached to a Claude
#      instance pre-loaded with the full review (so the user can ask
#      follow-up questions in their terminal).
set -euo pipefail

DEV_UID="${DEV_UID:-1000}"
DEV_GID="${DEV_GID:-1000}"
DEV_USER="${DEV_USER:-dev}"
HOME_DIR="/home/$DEV_USER"

# ── User setup (matches host UID so output dir perms are correct) ────────
EXISTING_USER=$(getent passwd "$DEV_UID" | cut -d: -f1 || true)
if [[ -n "$EXISTING_USER" && "$EXISTING_USER" != "$DEV_USER" ]]; then
    userdel "$EXISTING_USER" 2>/dev/null || true
fi
EXISTING_GROUP=$(getent group "$DEV_GID" | cut -d: -f1 || true)
if [[ -n "$EXISTING_GROUP" && "$EXISTING_GROUP" != "$DEV_USER" ]]; then
    groupdel "$EXISTING_GROUP" 2>/dev/null || true
fi
sed -i 's/^UID_MIN.*/UID_MIN 500/' /etc/login.defs 2>/dev/null || true
sed -i 's/^GID_MIN.*/GID_MIN 500/' /etc/login.defs 2>/dev/null || true
if ! getent group "$DEV_GID" >/dev/null 2>&1; then
    groupadd -g "$DEV_GID" "$DEV_USER"
fi
if ! id "$DEV_USER" >/dev/null 2>&1; then
    useradd -M -u "$DEV_UID" -g "$DEV_GID" -s /bin/bash -d "$HOME_DIR" "$DEV_USER"
fi
echo "$DEV_USER ALL=(ALL) NOPASSWD:ALL" > "/etc/sudoers.d/$DEV_USER"
chmod 440 "/etc/sudoers.d/$DEV_USER"

mkdir -p "$HOME_DIR"
chown "$DEV_UID:$DEV_GID" "$HOME_DIR"

# /review is the working tree for the orchestrator (it writes diff.patch,
# findings/, raw/, out/ here). The image was built with /review owned by
# root, so chown the whole tree to the dev user before we drop privs.
# /review/out is the host-mounted bind; chown follows into it.
chown -R "$DEV_UID:$DEV_GID" /review
# Workspace stays owned by whoever the host owns it as; we read it read-only.

# ── Bootstrap Claude Code (cached in /home volume on subsequent runs) ────
CLAUDE_BIN="$HOME_DIR/.local/bin/claude"
run_as_user() { su - "$DEV_USER" -c "$*"; }

if [[ ! -x "$CLAUDE_BIN" ]]; then
    echo ""
    echo -e "\033[1;35m⚡\033[0m \033[1mFirst run — installing Claude Code\033[0m"
    echo -e "\033[0;37m   This is cached in a Docker volume for future runs.\033[0m"
    echo ""
    run_as_user 'curl -fsSL https://claude.ai/install.sh | bash' 2>&1 | tail -5
    echo ""
fi

# Make sure PATH has claude for the user.
BASHRC="$HOME_DIR/.bashrc"
if ! grep -qF '# --- reviewbot-env ---' "$BASHRC" 2>/dev/null; then
    {
        echo '# --- reviewbot-env ---'
        echo 'export PATH="$HOME/.local/bin:$PATH"'
    } >> "$BASHRC"
    chown "$DEV_UID:$DEV_GID" "$BASHRC"
fi

# ── Drop privileges and run the orchestrator ─────────────────────────────
export HOME="$HOME_DIR" USER="$DEV_USER" LOGNAME="$DEV_USER"
export PATH="$HOME_DIR/.local/bin:$PATH"

exec setpriv --reuid="$DEV_UID" --regid="$DEV_GID" --init-groups \
    /review/orchestrator.sh "$@"
