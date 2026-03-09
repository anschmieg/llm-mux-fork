package stream

import (
	"strings"
	"testing"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/tidwall/gjson"
)

func collectChunks(chunks [][]byte) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.Write(chunk)
	}
	return b.String()
}

func TestTranslateToOpenAISetsStreamForClaudeSource(t *testing.T) {
	raw := []byte(`{"model":"chatgpt/gpt-5.4","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	out, err := TranslateToOpenAI(&config.Config{}, provider.FromString("claude"), "chatgpt/gpt-5.4", raw, true, nil)
	if err != nil {
		t.Fatalf("TranslateToOpenAI returned error: %v", err)
	}

	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("expected translated request to set stream=true: %s", string(out))
	}
	if !gjson.GetBytes(out, "stream_options.include_usage").Bool() {
		t.Fatalf("expected translated request to include usage in stream options: %s", string(out))
	}
}

func TestOpenAIStreamProcessorTranslatesClaudeMessagesStream(t *testing.T) {
	processor := NewOpenAIStreamProcessor(&config.Config{}, provider.FromString("claude"), "chatgpt/gpt-5.4", "msg-chatgpt/gpt-5.4")

	lines := []string{
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"content":"MSG_STREAM"},"logprobs":null,"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"content":"_OK"},"logprobs":null,"finish_reason":"stop"}]}`,
	}

	var got strings.Builder
	for _, line := range lines {
		chunks, _, err := processor.ProcessLine([]byte(line))
		if err != nil {
			t.Fatalf("ProcessLine returned error: %v", err)
		}
		got.WriteString(collectChunks(chunks))
	}

	doneChunks, err := processor.ProcessDone()
	if err != nil {
		t.Fatalf("ProcessDone returned error: %v", err)
	}
	got.WriteString(collectChunks(doneChunks))

	output := got.String()
	if !strings.Contains(output, `"text":"MSG_STREAM"`) || !strings.Contains(output, `"text":"_OK"`) {
		t.Fatalf("expected Claude SSE text deltas to include streamed content, got: %s", output)
	}
	if !strings.Contains(output, "event: message_stop") {
		t.Fatalf("expected Claude SSE to terminate correctly, got: %s", output)
	}
}

func TestOpenAIStreamProcessorTranslatesResponsesStream(t *testing.T) {
	processor := NewOpenAIStreamProcessor(&config.Config{}, provider.FromString("openai-response"), "chatgpt/gpt-5.4", "resp-chatgpt/gpt-5.4")

	lines := []string{
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"content":"RESP_STREAM"},"logprobs":null,"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1773067815,"model":"gpt-5-4-thinking","choices":[{"index":0,"delta":{"content":"_OK"},"logprobs":null,"finish_reason":"stop"}]}`,
	}

	var got strings.Builder
	for _, line := range lines {
		chunks, _, err := processor.ProcessLine([]byte(line))
		if err != nil {
			t.Fatalf("ProcessLine returned error: %v", err)
		}
		got.WriteString(collectChunks(chunks))
	}

	doneChunks, err := processor.ProcessDone()
	if err != nil {
		t.Fatalf("ProcessDone returned error: %v", err)
	}
	got.WriteString(collectChunks(doneChunks))

	output := got.String()
	if !strings.Contains(output, "event: response.output_text.delta") {
		t.Fatalf("expected Responses SSE text delta events, got: %s", output)
	}
	if !strings.Contains(output, `"delta":"RESP_STREAM"`) || !strings.Contains(output, `"delta":"_OK"`) {
		t.Fatalf("expected Responses SSE to include streamed content, got: %s", output)
	}
	if !strings.Contains(output, "event: response.done") {
		t.Fatalf("expected Responses SSE to terminate correctly, got: %s", output)
	}
}
