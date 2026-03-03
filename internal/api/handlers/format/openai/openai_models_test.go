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
