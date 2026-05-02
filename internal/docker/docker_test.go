package docker

import (
	"strings"
	"testing"
)

func TestEmbeddedAssetsPresent(t *testing.T) {
	if len(dockerfile) < 100 {
		t.Errorf("dockerfile embed too short (%d bytes)", len(dockerfile))
	}
	if len(entrypoint) < 100 {
		t.Errorf("entrypoint embed too short")
	}
	if !strings.Contains(runAgent, "claude --dangerously-skip-permissions") {
		t.Errorf("run_agent.sh embed missing expected claude invocation")
	}
	if !strings.Contains(parseAgentOutput, "find_json") {
		t.Errorf("parse_agent_output.py embed missing find_json helper")
	}
	if !strings.Contains(template, "/*__REVIEWBOT_PAYLOAD__*/") {
		t.Errorf("template embed missing payload placeholder")
	}
	if !strings.Contains(agentShared, "Output contract") {
		t.Errorf("_shared.md embed seems wrong")
	}
}

func TestEmbeddedAgents_ContainsCoreSpecialists(t *testing.T) {
	want := []string{"security", "subtle-bugs", "supply-chain", "performance",
		"concurrency", "consolidator", "spot-test-plan", "code-quality",
		"architecture", "tests", "api-contract"}
	entries, err := agentsFS.ReadDir("agents")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	for _, a := range want {
		if !got[a] {
			t.Errorf("missing embedded agent: %s.md", a)
		}
	}
}

func TestContentHash_Stable(t *testing.T) {
	a := ContentHash()
	b := ContentHash()
	if a != b {
		t.Errorf("ContentHash should be deterministic, got %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected 64-char sha256 hex, got %d chars", len(a))
	}
}
