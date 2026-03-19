package registry

import "testing"

func TestGetGitHubCopilotModelsIncludesGpt4oMini(t *testing.T) {
	models := GetGitHubCopilotModels()
	for _, model := range models {
		if model != nil && model.ID == "gpt-4o-mini" {
			return
		}
	}
	t.Fatalf("expected gpt-4o-mini in GitHub Copilot model definitions")
}

func TestKiroClaudeModelsUseSharedCanonicalIDs(t *testing.T) {
	models := GetKiroModels()
	got := map[string]string{}
	for _, model := range models {
		if model != nil {
			got[model.ID] = model.CanonicalID
		}
	}

	tests := map[string]string{
		"claude-haiku-4.5":  "claude-haiku-4-5",
		"claude-sonnet-4.5": "claude-sonnet-4-5",
		"claude-sonnet-4.6": "claude-sonnet-4-6",
		"claude-opus-4.5":   "claude-opus-4-5",
		"claude-opus-4.6":   "claude-opus-4-6",
	}

	for modelID, want := range tests {
		if got[modelID] != want {
			t.Fatalf("expected %s canonical id %s, got %s", modelID, want, got[modelID])
		}
	}
}
