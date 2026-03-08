package format

import (
	"slices"
	"testing"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/tidwall/gjson"
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

func TestGetRequestDetailsResolvesRoutingProfile(t *testing.T) {
	clientID := "test-profile-client"
	primaryModelID := "gpt-4o"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:          primaryModelID,
			Object:      "model",
			OwnedBy:     "github-copilot",
			CanonicalID: primaryModelID,
		},
	})
	t.Cleanup(func() {
		modelRegistry.RegisterClient(clientID, "github-copilot", nil)
	})

	routing := &config.RoutingConfig{
		Profiles: map[string]config.RoutingProfile{
			"chat-fast": {
				Primary:   primaryModelID,
				Fallbacks: []string{"gpt-4.1"},
			},
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	providers, normalizedModel, metadata, errMsg := handler.getRequestDetails("chat-fast")
	if errMsg != nil {
		t.Fatalf("expected profile resolution to succeed, got error: %v", errMsg.Error)
	}
	if normalizedModel != primaryModelID {
		t.Fatalf("unexpected normalized model: got=%q want=%q", normalizedModel, primaryModelID)
	}
	if !slices.Contains(providers, "github-copilot") {
		t.Fatalf("expected github-copilot provider in %v", providers)
	}
	chain := handler.effectiveFallbackChain(normalizedModel, metadata)
	if !slices.Equal(chain, []string{"gpt-4.1"}) {
		t.Fatalf("unexpected profile fallback chain metadata: got=%v", chain)
	}
}

func TestGetRequestDetailsResolvesProfileFallbackWhenPrimaryUnavailable(t *testing.T) {
	clientID := "test-profile-fallback-client"
	fallbackModelID := "gpt-4.1"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:          fallbackModelID,
			Object:      "model",
			OwnedBy:     "github-copilot",
			CanonicalID: fallbackModelID,
		},
	})
	t.Cleanup(func() {
		modelRegistry.RegisterClient(clientID, "github-copilot", nil)
	})

	routing := &config.RoutingConfig{
		Profiles: map[string]config.RoutingProfile{
			"tool-use": {
				Primary:   "missing-primary-model",
				Fallbacks: []string{fallbackModelID},
			},
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	providers, normalizedModel, metadata, errMsg := handler.getRequestDetails("tool-use")
	if errMsg != nil {
		t.Fatalf("expected profile fallback to resolve, got error: %v", errMsg.Error)
	}
	if normalizedModel != fallbackModelID {
		t.Fatalf("expected fallback model, got=%q want=%q", normalizedModel, fallbackModelID)
	}
	if !slices.Contains(providers, "github-copilot") {
		t.Fatalf("expected github-copilot provider in %v", providers)
	}
	chain := handler.effectiveFallbackChain(normalizedModel, metadata)
	if !slices.Equal(chain, []string{fallbackModelID}) {
		t.Fatalf("unexpected resolved chain metadata: got=%v", chain)
	}
}

func TestGetRequestDetailsResolvesNestedFallbackSeed(t *testing.T) {
	clientID := "test-nested-fallback-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{
			ID:          "antigravity/claude-opus-4-6-thinking",
			Object:      "model",
			OwnedBy:     "antigravity",
			CanonicalID: "antigravity/claude-opus-4-6-thinking",
		},
	})
	t.Cleanup(func() {
		modelRegistry.RegisterClient(clientID, "antigravity", nil)
	})

	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-opus-4.6": "claude-opus-4-6-thinking",
		},
		Fallbacks: map[string][]string{
			"claude-opus-4-6-thinking": {"antigravity/claude-opus-4-6-thinking"},
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	providers, normalizedModel, _, errMsg := handler.getRequestDetails("claude-opus-4.6")
	if errMsg != nil {
		t.Fatalf("expected nested fallback resolution to succeed, got error: %v", errMsg.Error)
	}
	if normalizedModel != "antigravity/claude-opus-4-6-thinking" {
		t.Fatalf("unexpected normalized model: got=%q", normalizedModel)
	}
	if !slices.Contains(providers, "antigravity") {
		t.Fatalf("expected antigravity provider in %v", providers)
	}
}

func TestGetRequestDetailsRecursesThroughOriginalAliasKey(t *testing.T) {
	clientID := "test-alias-recurse-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{
			ID:          "claude-opus-4-6-thinking",
			Object:      "model",
			OwnedBy:     "antigravity",
			CanonicalID: "claude-opus-4-6-thinking",
		},
	})
	t.Cleanup(func() {
		modelRegistry.RegisterClient(clientID, "antigravity", nil)
	})

	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-opus-4.6": "claude-opus-4-6-thinking",
		},
	}
	routing.Init()

	handler := &BaseAPIHandler{Routing: routing}
	providers, normalizedModel, _, errMsg := handler.getRequestDetails("claude-opus-4.6")
	if errMsg != nil {
		t.Fatalf("expected alias recursion to succeed, got error: %v", errMsg.Error)
	}
	if normalizedModel != "claude-opus-4-6-thinking" {
		t.Fatalf("unexpected normalized model: got=%q", normalizedModel)
	}
	if !slices.Contains(providers, "antigravity") {
		t.Fatalf("expected antigravity provider in %v", providers)
	}
}

func TestBuildRequestOptsSyncsPayloadModelToNormalizedAlias(t *testing.T) {
	raw := []byte(`{"model":"raptor-mini","messages":[{"role":"user","content":"hi"}]}`)

	req, opts := buildRequestOpts("oswe-vscode-prime", raw, nil, "openai", "", false)

	if req.Model != "oswe-vscode-prime" {
		t.Fatalf("unexpected request model: %q", req.Model)
	}
	if got := gjson.GetBytes(req.Payload, "model").String(); got != "oswe-vscode-prime" {
		t.Fatalf("request payload model mismatch: got=%q", got)
	}
	if got := gjson.GetBytes(opts.OriginalRequest, "model").String(); got != "oswe-vscode-prime" {
		t.Fatalf("original request model mismatch: got=%q", got)
	}
}
