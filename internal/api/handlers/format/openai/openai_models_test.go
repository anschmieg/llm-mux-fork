package openai

import (
	"slices"
	"testing"

	"github.com/nghyane/llm-mux/internal/api/handlers/format"
	"github.com/nghyane/llm-mux/internal/config"
)

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
