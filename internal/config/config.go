// Package config holds reviewbot's project-level configuration.
//
// Most installs need zero config — reviewbot detects the parent branch
// automatically. The .reviewbot/ directory exists for two opt-in things:
//
//   - .reviewbot/config.yaml — pin the docker image tag, set a default base
//     branch, define exclusions, configure parallelism.
//   - .reviewbot/CLAUDE.md   — additional rules merged into every agent's
//     system prompt (e.g. "this is a regulated codebase, treat any logging
//     of customer data as critical").
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigDir is the project-level config directory name.
	ConfigDir = ".reviewbot"
	// ConfigFile is the config file name within ConfigDir.
	ConfigFile = "config.yaml"
	// ProjectRulesFile is the project-level rules file merged into every
	// agent's system prompt.
	ProjectRulesFile = "CLAUDE.md"
)

// Config holds reviewbot project configuration.
type Config struct {
	// Image is the docker image name.
	Image string `yaml:"image"`
	// Tag is the docker image tag.
	Tag string `yaml:"tag"`
	// HomeVolume is the docker volume name for caching the claude install.
	HomeVolume string `yaml:"home_volume"`
	// BaseBranch overrides the auto-detected parent branch.
	BaseBranch string `yaml:"base_branch,omitempty"`
	// Parallel sets how many agents run simultaneously inside the container.
	Parallel int `yaml:"parallel,omitempty"`
	// SkipAgents lists agent IDs to skip.
	SkipAgents []string `yaml:"skip_agents,omitempty"`
	// ExtraEnv passes additional environment variables to the container.
	ExtraEnv map[string]string `yaml:"env,omitempty"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		Image:      "reviewbot",
		Tag:        "latest",
		HomeVolume: "reviewbot-home",
		Parallel:   8,
	}
}

// Load reads configuration from the given project directory. Falls back to
// defaults for any unset values.
func Load(projectDir string) *Config {
	cfg := DefaultConfig()

	data, err := os.ReadFile(filepath.Join(projectDir, ConfigDir, ConfigFile))
	if err != nil {
		return cfg
	}
	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return cfg
	}

	if fileCfg.Image != "" {
		cfg.Image = fileCfg.Image
	}
	if fileCfg.Tag != "" {
		cfg.Tag = fileCfg.Tag
	}
	if fileCfg.HomeVolume != "" {
		cfg.HomeVolume = fileCfg.HomeVolume
	}
	if fileCfg.BaseBranch != "" {
		cfg.BaseBranch = fileCfg.BaseBranch
	}
	if fileCfg.Parallel > 0 {
		cfg.Parallel = fileCfg.Parallel
	}
	if len(fileCfg.SkipAgents) > 0 {
		cfg.SkipAgents = fileCfg.SkipAgents
	}
	if len(fileCfg.ExtraEnv) > 0 {
		cfg.ExtraEnv = fileCfg.ExtraEnv
	}
	return cfg
}

// LoadProjectRules reads .reviewbot/CLAUDE.md if present. Returns "" if missing.
func LoadProjectRules(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, ConfigDir, ProjectRulesFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// DefaultConfigYAML returns a commented YAML config template.
func DefaultConfigYAML() string {
	return `# reviewbot project configuration
# Place this file at .reviewbot/config.yaml in your project root.
# All keys are optional — reviewbot works with zero config.

# Docker image / tag (don't change unless you know why)
# image: reviewbot
# tag:   latest

# Override parent-branch detection (default: detect via origin/HEAD, then main/master/develop)
# base_branch: main

# Number of agents to run in parallel inside the container
# parallel: 8

# Agents to skip (rare — most agents are no-ops if their file types aren't in the diff)
# skip_agents:
#   - frontend
#   - db-migrations

# Extra env vars to forward into the container (e.g. private registry tokens
# the supply-chain agent needs to inspect packages)
# env:
#   NPM_TOKEN: "${NPM_TOKEN}"
`
}

// DefaultProjectRules returns a commented CLAUDE.md template.
func DefaultProjectRules() string {
	return `# reviewbot — project-specific review rules

These rules are merged into **every** agent's system prompt. Use this file to
encode things only your team knows: domain rules, regulatory constraints,
historical incidents, "we tried that and it broke."

Examples:

- Treat any change that logs raw email addresses or phone numbers as
  ` + "`critical`" + ` — we're under a consent decree from 2024-Q3.

- The ` + "`/internal/billing/`" + ` package is touched by audit; any change there
  must include a CHANGELOG entry naming the ticket.

- We use ` + "`pgx`" + ` directly, not ` + "`database/sql`" + `. Any new code that imports
  ` + "`database/sql`" + ` is a finding, even if the diff compiles.

Keep this file short. Long rule sets dilute the agents' attention.
`
}

// Init creates the .reviewbot/ config directory with defaults.
func Init(projectDir string) (string, error) {
	dir := filepath.Join(projectDir, ConfigDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if path := filepath.Join(dir, ConfigFile); !exists(path) {
		if err := os.WriteFile(path, []byte(DefaultConfigYAML()), 0644); err != nil {
			return "", err
		}
	}
	if path := filepath.Join(dir, ProjectRulesFile); !exists(path) {
		if err := os.WriteFile(path, []byte(DefaultProjectRules()), 0644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
