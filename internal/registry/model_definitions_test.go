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

