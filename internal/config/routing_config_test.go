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

func TestRoutingConfigProfilesResolveNatively(t *testing.T) {
	r := &RoutingConfig{
		Profiles: map[string]RoutingProfile{
			"chat-fast": {
				Primary:   "gemini-2.5-flash-lite",
				Fallbacks: []string{"gpt-4o-mini", "claude-haiku-4.5"},
			},
		},
	}

	r.Init()

	if got := r.ResolveModelAlias("chat-fast"); got != "chat-fast" {
		t.Fatalf("expected profiles to not materialize into aliases, got %q", got)
	}

	primary, ok := r.ResolveProfilePrimary("chat-fast")
	if !ok {
		t.Fatalf("expected profile primary to resolve")
	}
	if primary != "gemini-2.5-flash-lite" {
		t.Fatalf("unexpected profile primary: %q", primary)
	}

	chain := r.GetProfileFallbackChain("chat-fast")
	if len(chain) != 2 {
		t.Fatalf("expected 2 fallback entries, got %d (%v)", len(chain), chain)
	}
	if chain[0] != "gpt-4o-mini" || chain[1] != "claude-haiku-4.5" {
		t.Fatalf("unexpected fallback chain: %v", chain)
	}

	if mappedChain := r.GetFallbackChain("chat-fast"); len(mappedChain) != 0 {
		t.Fatalf("expected no materialized fallback chain on routing.fallbacks, got %v", mappedChain)
	}
}
