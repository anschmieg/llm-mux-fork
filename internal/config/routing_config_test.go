package config

import "testing"

func TestRoutingConfigCanonicalDefaults(t *testing.T) {
	r := &RoutingConfig{}
	r.Init()

	if r.CanonicalModelSource != "aliases" {
		t.Fatalf("expected canonical-model-source default aliases, got %q", r.CanonicalModelSource)
	}
	if !r.IsCanonicalModelAllowed("any-model") {
		t.Fatalf("expected unrestricted canonical allowlist by default")
	}
}

func TestRoutingConfigCanonicalAllowlist(t *testing.T) {
	r := &RoutingConfig{
		CanonicalModelsInclude: []string{"claude-sonnet-4.6", "glm-5"},
	}
	r.Init()

	if !r.IsCanonicalModelAllowed("claude-sonnet-4.6") {
		t.Fatalf("expected allowlisted model to be accepted")
	}
	if r.IsCanonicalModelAllowed("gpt-4o") {
		t.Fatalf("expected non-allowlisted model to be rejected")
	}
}
