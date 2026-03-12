package openai

import (
	"slices"
	"testing"

	"github.com/nghyane/llm-mux/internal/api/handlers/format"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
)

func registerTestClient(t *testing.T, clientID, provider string, models ...*registry.ModelInfo) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, models)
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func TestCanonicalizeModelList(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-sonnet-4.6":         "claude-sonnet-4-6",
			"kiro-claude-sonnet-4.6":    "claude-sonnet-4-6",
			"claude-sonnet-4.6-preview": "claude-sonnet-4-6",
			"glm-5":                     "modal-ai/glm-5",
			"modal-ai/glm-5":            "modal-ai/glm-5",
		},
		Fallbacks: map[string][]string{
			"claude-sonnet-4-6": {"kiro-claude-sonnet-4-6"},
		},
		CanonicalModelsOnly: true,
	}
	routing.Init()

	registerTestClient(t, "test-openai-models-kiro-canonicalize", "kiro",
		registry.Kiro("kiro-claude-sonnet-4-6").Canonical("claude-sonnet-4-6").Build(),
	)
	registerTestClient(t, "test-openai-models-modal-canonicalize", "modal-ai",
		registry.OpenAI("modal-ai/glm-5").Build(),
	)

	handler := &OpenAIAPIHandler{
		BaseAPIHandler: &format.BaseAPIHandler{
			Routing: routing,
		},
	}

	models := []map[string]any{
		{"id": "kiro-claude-sonnet-4-6", "object": "model", "created": int64(1), "owned_by": "kiro"},
		{"id": "modal-ai/glm-5", "object": "model", "created": int64(2), "owned_by": "modal"},
	}

	got := handler.canonicalizeModelList(models)
	gotIDs := modelIDs(got)
	wantIDs := []string{"claude-sonnet-4.6", "glm-5"}

	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unexpected canonical model IDs: got=%v want=%v", gotIDs, wantIDs)
	}
}

func TestCanonicalizeModelListRespectsAllowlist(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-sonnet-4.6": "claude-sonnet-4-6",
			"glm-5":             "modal-ai/glm-5",
		},
		Fallbacks: map[string][]string{
			"claude-sonnet-4-6": {"kiro-claude-sonnet-4-6"},
		},
		CanonicalModelsOnly:    true,
		CanonicalModelsInclude: []string{"glm-5"},
	}
	routing.Init()

	registerTestClient(t, "test-openai-models-kiro-allow", "kiro",
		registry.Kiro("kiro-claude-sonnet-4-6").Canonical("claude-sonnet-4-6").Build(),
	)
	registerTestClient(t, "test-openai-models-modal-allow", "modal-ai",
		registry.OpenAI("modal-ai/glm-5").Build(),
	)

	handler := &OpenAIAPIHandler{
		BaseAPIHandler: &format.BaseAPIHandler{
			Routing: routing,
		},
	}

	models := []map[string]any{
		{"id": "kiro-claude-sonnet-4-6", "object": "model", "created": int64(1), "owned_by": "kiro"},
		{"id": "modal-ai/glm-5", "object": "model", "created": int64(2), "owned_by": "modal"},
	}

	got := handler.canonicalizeModelList(models)
	gotIDs := modelIDs(got)
	wantIDs := []string{"glm-5"}

	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unexpected canonical model IDs with allowlist: got=%v want=%v", gotIDs, wantIDs)
	}
}

func TestHideProviderVariantModels(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-sonnet-4.6":      "claude-sonnet-4-6",
			"kiro-claude-sonnet-4.6": "claude-sonnet-4-6",
		},
		HideProviderModels: true,
	}
	routing.Init()

	handler := &OpenAIAPIHandler{
		BaseAPIHandler: &format.BaseAPIHandler{
			Routing: routing,
		},
	}

	models := []map[string]any{
		{"id": "kiro-claude-sonnet-4-6", "object": "model"},
		{"id": "claude-sonnet-4-6", "object": "model"},
	}

	got := handler.hideProviderVariantModels(models)
	gotIDs := modelIDs(got)
	wantIDs := []string{"claude-sonnet-4-6"}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unexpected hidden-provider result: got=%v want=%v", gotIDs, wantIDs)
	}
}

func TestCanonicalizeModelListFromFallbacksIncludesCanonicalModelIDs(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"gpt4":                    "gpt-4o",
			"codex/gpt-4o-mini":       "gpt-4o-mini",
			"github-copilot/gpt-4o":   "gpt-4o",
			"github-copilot:gpt-4.1":  "gpt-4.1",
			"openrouter/claude-sonnet-4": "claude-sonnet-4",
		},
		Fallbacks: map[string][]string{
			"gpt-4o-mini": {"gpt-4o"},
			"gpt-5.2":     {"gpt-5.1"},
		},
		CanonicalModelsOnly: true,
		CanonicalModelSource: "fallbacks",
	}
	routing.Init()

	registerTestClient(t, "test-openai-models-gh-fallbacks", "github-copilot",
		registry.Copilot("gpt-4o").Build(),
		registry.Copilot("gpt-4o-mini").Build(),
		registry.Copilot("gpt-4.1").Build(),
	)
	registerTestClient(t, "test-openai-models-codex-fallbacks", "codex",
		registry.OpenAI("gpt-5.2").Build(),
		registry.OpenAI("codex/gpt-4o-mini").Build(),
	)

	handler := &OpenAIAPIHandler{
		BaseAPIHandler: &format.BaseAPIHandler{
			Routing: routing,
		},
	}

	models := []map[string]any{
		{"id": "gpt-4o", "object": "model", "created": int64(1), "owned_by": "github-copilot"},
		{"id": "gpt-4o-mini", "object": "model", "created": int64(1), "owned_by": "github-copilot"},
		{"id": "gpt-4.1", "object": "model", "created": int64(1), "owned_by": "github-copilot"},
		{"id": "gpt-5.2", "object": "model", "created": int64(1), "owned_by": "codex"},
		{"id": "codex/gpt-4o-mini", "object": "model", "created": int64(1), "owned_by": "codex"},
		{"id": "openrouter/claude-sonnet-4", "object": "model", "created": int64(1), "owned_by": "openrouter"},
	}

	got := handler.canonicalizeModelList(models)
	gotIDs := modelIDs(got)
	wantIDs := []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini", "gpt-5.2"}

	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unexpected canonical fallback-source model IDs: got=%v want=%v", gotIDs, wantIDs)
	}
}

func TestCanonicalizeModelListSkipsUnroutableCanonicalModels(t *testing.T) {
	routing := &config.RoutingConfig{
		Aliases: map[string]string{
			"claude-sonnet-4.6": "claude-sonnet-4-6",
			"glm-5":             "modal-ai/glm-5",
		},
		Fallbacks: map[string][]string{
			"claude-sonnet-4-6": {"kiro-claude-sonnet-4-6"},
		},
		CanonicalModelsOnly: true,
	}
	routing.Init()

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-openai-models-kiro", "kiro", []*registry.ModelInfo{
		registry.Kiro("kiro-claude-sonnet-4-6").Canonical("claude-sonnet-4-6").Build(),
	})
	t.Cleanup(func() {
		reg.UnregisterClient("test-openai-models-kiro")
	})

	handler := &OpenAIAPIHandler{
		BaseAPIHandler: &format.BaseAPIHandler{
			Routing: routing,
		},
	}

	models := []map[string]any{
		{"id": "kiro-claude-sonnet-4-6", "object": "model", "created": int64(1), "owned_by": "kiro"},
	}

	got := handler.canonicalizeModelList(models)
	gotIDs := modelIDs(got)
	wantIDs := []string{"claude-sonnet-4.6"}

	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("unexpected canonical model IDs for routable-only listing: got=%v want=%v", gotIDs, wantIDs)
	}
}

func modelIDs(models []map[string]any) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
