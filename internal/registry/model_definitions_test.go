package registry

import "testing"

func TestKiroClaudeModelsUseSharedCanonicalIDs(t *testing.T) {
	models := GetKiroModels()
	got := map[string]string{}
	for _, model := range models {
		got[model.ID] = model.CanonicalID
	}

	tests := map[string]string{
		"claude-haiku-4.5":  "claude-haiku-4.5",
		"claude-sonnet-4.5": "claude-sonnet-4.5",
		"claude-sonnet-4.6": "claude-sonnet-4.6",
		"claude-opus-4.5":   "claude-opus-4.5",
		"claude-opus-4.6":   "claude-opus-4.6",
	}

	for modelID, want := range tests {
		if got[modelID] != want {
			t.Fatalf("expected %s canonical id %s, got %s", modelID, want, got[modelID])
		}
	}
}
