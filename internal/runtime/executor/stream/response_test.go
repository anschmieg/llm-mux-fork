package stream

import (
	"strings"
	"testing"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/tidwall/gjson"
)

func TestTranslateToCodexDropsUnsupportedTokenLimitFields(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.1-codex","messages":[{"role":"user","content":"hello"}],"max_tokens":128}`)

	out, err := TranslateToCodex(&config.Config{}, provider.FromString("openai"), "gpt-5.1-codex", raw, false, nil)
	if err != nil {
		t.Fatalf("TranslateToCodex returned error: %v", err)
	}
	if gjson.GetBytes(out, "max_output_tokens").Exists() {
		t.Fatalf("did not expect max_output_tokens in codex payload: %s", string(out))
	}
	if gjson.GetBytes(out, "max_completion_tokens").Exists() {
		t.Fatalf("did not expect max_completion_tokens in codex payload: %s", string(out))
	}
}

func TestTranslateResponseNonStreamKiroToResponses(t *testing.T) {
	raw := []byte(`{
		"assistantResponseMessage": {
			"content": "Kiro response text"
		},
		"usage": {
			"inputTokens": 10,
			"outputTokens": 5
		}
	}`)

	out, err := TranslateResponseNonStream(&config.Config{}, provider.FromString("kiro"), provider.FromString("openai-response"), raw, "minimax-m2.1")
	if err != nil {
		t.Fatalf("TranslateResponseNonStream returned error: %v", err)
	}
	if !strings.Contains(string(out), `"object":"response"`) || !strings.Contains(string(out), `Kiro response text`) {
		t.Fatalf("expected Responses API payload with translated Kiro text, got: %s", string(out))
	}
}

func TestTranslateResponseNonStreamKiroToClaude(t *testing.T) {
	raw := []byte(`{
		"assistantResponseMessage": {
			"content": "Kiro Claude text"
		}
	}`)

	out, err := TranslateResponseNonStream(&config.Config{}, provider.FromString("kiro"), provider.FromString("claude"), raw, "minimax-m2.1")
	if err != nil {
		t.Fatalf("TranslateResponseNonStream returned error: %v", err)
	}
	if !strings.Contains(string(out), `"type":"message"`) || !strings.Contains(string(out), `Kiro Claude text`) {
		t.Fatalf("expected Claude message payload with translated Kiro text, got: %s", string(out))
	}
}

func TestTranslateResponseNonStreamCodexToResponsesUnwrapsCompletedWrapper(t *testing.T) {
	raw := []byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_test",
			"output":[
				{
					"type":"message",
					"role":"assistant",
					"content":[{"type":"output_text","text":"Codex wrapped response"}]
				}
			],
			"usage":{"input_tokens":12,"output_tokens":4}
		}
	}`)

	out, err := TranslateResponseNonStream(&config.Config{}, provider.FromString("codex"), provider.FromString("openai-response"), raw, "gpt-5.1-codex")
	if err != nil {
		t.Fatalf("TranslateResponseNonStream returned error: %v", err)
	}
	if !strings.Contains(string(out), `"object":"response"`) || !strings.Contains(string(out), `Codex wrapped response`) {
		t.Fatalf("expected Responses API payload with translated Codex text, got: %s", string(out))
	}
}
