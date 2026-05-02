FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive

# ── System packages ──────────────────────────────────────────────────────
# Reviewer-focused: git for diffs, jq for JSON manipulation, ripgrep for
# fast greps, parallel + xargs for fan-out, common toolchains so agents
# can run quick experiments inline.
RUN apt-get update && apt-get install -y --no-install-recommends \
    bc \
    build-essential \
    ca-certificates \
    curl \
    file \
    git \
    gnupg \
    jq \
    less \
    libffi-dev \
    libsqlite3-dev \
    libssl-dev \
    locales \
    moreutils \
    parallel \
    pkg-config \
    python3 \
    python3-pip \
    python3-venv \
    ripgrep \
    sudo \
    tmux \
    tree \
    unzip \
    vim \
    wget \
    zip \
    zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*

# ── Locale ───────────────────────────────────────────────────────────────
RUN locale-gen en_US.UTF-8
ENV LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8

# ── Layout ───────────────────────────────────────────────────────────────
# /workspace      = project under review (mounted read-only via overlay; the
#                   container only writes findings to /review/out)
# /review         = the review-bundle dir (diff, agent prompts, findings)
# /review/out     = the output dir (mounted from host) — index.html,
#                   findings/, raw/ get written here. Host serves it live.
RUN mkdir -p /workspace /review/out /review/findings /review/raw /review/agents

WORKDIR /review
