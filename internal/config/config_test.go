package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.Image == "" || c.Tag == "" || c.HomeVolume == "" {
		t.Fatalf("default config missing fields: %+v", c)
	}
	if c.Parallel <= 0 {
		t.Fatalf("expected positive default parallelism, got %d", c.Parallel)
	}
}

func TestLoad_NoFile_FallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	got := Load(dir)
	want := DefaultConfig()
	if got.Image != want.Image || got.Tag != want.Tag {
		t.Fatalf("expected defaults, got %+v", got)
	}
}

func TestLoad_OverridesMerge(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ConfigDir)
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	yaml := `image: custom
tag: v1
parallel: 16
base_branch: develop
skip_agents: [frontend]
`
	if err := os.WriteFile(filepath.Join(cfgDir, ConfigFile), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	got := Load(dir)
	if got.Image != "custom" || got.Tag != "v1" || got.Parallel != 16 || got.BaseBranch != "develop" {
		t.Fatalf("merge failed: %+v", got)
	}
	if len(got.SkipAgents) != 1 || got.SkipAgents[0] != "frontend" {
		t.Fatalf("skip_agents not loaded: %+v", got.SkipAgents)
	}
	// Default fields not touched by yaml should remain at default.
	if got.HomeVolume != DefaultConfig().HomeVolume {
		t.Fatalf("home_volume should fall back to default, got %q", got.HomeVolume)
	}
}

func TestInit_CreatesScaffolding(t *testing.T) {
	dir := t.TempDir()
	cfgDir, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{ConfigFile, ProjectRulesFile} {
		if _, err := os.Stat(filepath.Join(cfgDir, f)); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}
}
