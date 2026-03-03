package format

import (
	"slices"
	"testing"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
)

func TestGetRequestDetailsResolvesFallbackSeed(t *testing.T) {
	clientID := "test-fallback-seed-client"
	modelID := "provider-model-x"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:          modelID,
			Object:      "model",
			OwnedBy:     "github-copilot",
			CanonicalID: modelID,
		},
	})
	t.Cleanup(func() {
		modelRegistry.RegisterClient(clientID, "github-copilot", nil)
	})

	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"canonical-model": "canonical-seed",
		},
		Fallbacks: map[string][]string{
			"canonical-seed": {modelID},
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	providers, normalizedModel, metadata, errMsg := handler.getRequestDetails("canonical-model")
	if errMsg != nil {
		t.Fatalf("expected fallback seed resolution to succeed, got error: %v", errMsg.Error)
	}
	if normalizedModel != modelID {
		t.Fatalf("unexpected normalized model: got=%q want=%q", normalizedModel, modelID)
	}
	if len(providers) == 0 {
		t.Fatalf("expected providers from fallback model")
	}
	if !slices.Contains(providers, "github-copilot") {
		t.Fatalf("expected github-copilot provider in %v", providers)
	}
	chain := handler.effectiveFallbackChain(normalizedModel, metadata)
	if !slices.Equal(chain, []string{modelID}) {
		t.Fatalf("unexpected fallback chain metadata: got=%v", chain)
	}
}

func TestGetRequestDetailsReturnsErrorWhenUnresolved(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"canonical-model": "canonical-seed",
		},
		Fallbacks: map[string][]string{
			"canonical-seed": {"still-missing"},
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	_, _, _, errMsg := handler.getRequestDetails("canonical-model")
	if errMsg == nil {
		t.Fatalf("expected unknown provider error for unresolved fallback seed")
	}
}
