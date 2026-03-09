package stream

import (
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

// EventBufferStrategy defines the interface for event buffering strategies (merged from event_buffer.go)
type EventBufferStrategy interface {
	Process(event *ir.UnifiedEvent) []*ir.UnifiedEvent
	Flush() []*ir.UnifiedEvent
}

// PassthroughEventBuffer is a no-op event buffer that passes events through unchanged (merged from event_buffer.go)
type PassthroughEventBuffer struct{}

func NewPassthroughEventBuffer() *PassthroughEventBuffer {
	return &PassthroughEventBuffer{}
}

func (b *PassthroughEventBuffer) Process(event *ir.UnifiedEvent) []*ir.UnifiedEvent {
	return []*ir.UnifiedEvent{event}
}

func (b *PassthroughEventBuffer) Flush() []*ir.UnifiedEvent {
	return nil
}

// StreamContext holds state for stream processing (merged from stream_state.go)
type StreamContext struct {
	ClaudeState          *from_ir.ClaudeStreamState
	GeminiState          *ir.GeminiStreamParserState
	ResponsesState       *from_ir.ResponsesStreamState
	ToolCallIndex        int
	HasToolCalls         bool
	FinishSent           bool
	ReasoningCharsAccum  int
	ToolSchemaCtx        *ir.ToolSchemaContext
	EstimatedInputTokens int64
}

func NewStreamContext() *StreamContext {
	return &StreamContext{
		ClaudeState:    from_ir.NewClaudeStreamState(),
		GeminiState:    ir.NewGeminiStreamParserState(),
		ResponsesState: from_ir.NewResponsesStreamState(),
	}
}

func NewStreamContextWithTools(originalRequest []byte) *StreamContext {
	Ctx := NewStreamContext()
	if len(originalRequest) > 0 {
		tools := gjson.GetBytes(originalRequest, "tools").Array()
		if len(tools) > 0 {
			Ctx.ToolSchemaCtx = ir.NewToolSchemaContextFromGJSON(tools)
		}
	}
	return Ctx
}

func (s *StreamContext) MarkFinishSent() bool {
	if s.FinishSent {
		return false
	}
	s.FinishSent = true
	return true
}

func (s *StreamContext) AccumulateReasoning(text string) {
	s.ReasoningCharsAccum += len(text)
}

func (s *StreamContext) EstimateReasoningTokens() int32 {
	return int32(s.ReasoningCharsAccum / 3)
}

// StreamTranslator handles format conversion with integrated buffering
type StreamTranslator struct {
	cfg             *config.Config
	from            provider.Format
	to              string
	model           string
	messageID       string
	Ctx             *StreamContext
	eventBuffer     EventBufferStrategy
	chunkBuffer     ChunkBufferStrategy
	streamMetaSent  bool
	pendingThinking *ir.UnifiedEvent // Claude: buffered thinking waiting for signature
}

func NewStreamTranslator(cfg *config.Config, from provider.Format, to, model, messageID string, Ctx *StreamContext) *StreamTranslator {
	if Ctx == nil {
		Ctx = NewStreamContext()
	}
	st := &StreamTranslator{
		cfg:       cfg,
		from:      from,
		to:        to,
		model:     model,
		messageID: messageID,
		Ctx:       Ctx,
	}

	if provider.IsClaudeFormat(to) {
		st.eventBuffer = NewPassthroughEventBuffer()
		st.chunkBuffer = NewPassthroughBuffer() // Claude: no delay - thinking has signature from parser
	} else {
		// Gemini and other formats: no delay buffer - thinking streams immediately
		st.eventBuffer = NewPassthroughEventBuffer()
		st.chunkBuffer = NewPassthroughBuffer()
	}

	return st
}

// Translate converts IR events to target format with buffering
func (t *StreamTranslator) Translate(events []*ir.UnifiedEvent) (*StreamTranslationResult, error) {
	var allChunks [][]byte

	if !t.streamMetaSent && len(events) > 0 {
		t.streamMetaSent = true

		var inputTokens, cacheTokens int64
		if t.Ctx.GeminiState != nil && t.Ctx.GeminiState.ActualInputTokens > 0 {
			inputTokens = t.Ctx.GeminiState.ActualInputTokens
			cacheTokens = t.Ctx.GeminiState.ActualCacheTokens
		}

		metaEvent := ir.UnifiedEvent{
			Type: ir.EventTypeStreamMeta,
			StreamMeta: &ir.StreamMeta{
				MessageID:            t.messageID,
				Model:                t.model,
				EstimatedInputTokens: inputTokens,
				CacheReadInputTokens: cacheTokens,
			},
		}
		if chunks, err := t.convertEventChunks(&metaEvent); err != nil {
			return nil, err
		} else if len(chunks) > 0 {
			allChunks = append(allChunks, chunks...)
		}
	}

	for _, event := range events {
		if t.preprocess(event) {
			continue
		}

		bufferedEvents := t.eventBuffer.Process(event)
		for _, ev := range bufferedEvents {
			chunks, err := t.convertAndBuffer(ev)
			if err != nil {
				return nil, err
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	usage := ExtractUsageFromEvents(events)

	return &StreamTranslationResult{
		Chunks: allChunks,
		Usage:  usage,
	}, nil
}

func (t *StreamTranslator) convertAndBuffer(event *ir.UnifiedEvent) ([][]byte, error) {
	chunks, err := t.convertEventChunks(event)
	if err != nil {
		return nil, err
	}

	if isResponsesTarget(t.to) {
		return chunks, nil
	}

	var chunk []byte
	if len(chunks) > 0 {
		chunk = chunks[0]
	}

	if chunk != nil || event.Type == ir.EventTypeFinish {
		var finishEvent *ir.UnifiedEvent
		if event.Type == ir.EventTypeFinish {
			finishEvent = event
		}
		return t.chunkBuffer.Process(chunk, finishEvent), nil
	}

	return nil, nil
}

func (t *StreamTranslator) Flush() ([][]byte, error) {
	var allChunks [][]byte

	// Finalize Claude parser state (embedded in ClaudeState)
	if t.Ctx != nil && t.Ctx.ClaudeState != nil && t.Ctx.ClaudeState.ParserState != nil {
		if finalEvent := t.Ctx.ClaudeState.ParserState.Finalize(); finalEvent != nil {
			chunks, err := t.convertAndBuffer(finalEvent)
			if err != nil {
				return nil, err
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	// Gemini state events are already emitted during parsing
	// Finalize just clears the state reference
	if t.Ctx != nil && t.Ctx.GeminiState != nil {
		t.Ctx.GeminiState.Finalize()
	}

	flushedEvents := t.eventBuffer.Flush()
	for _, ev := range flushedEvents {
		chunks, err := t.convertAndBuffer(ev)
		if err != nil {
			return nil, err
		}
		allChunks = append(allChunks, chunks...)
	}

	allChunks = append(allChunks, t.chunkBuffer.Flush()...)
	return allChunks, nil
}

// preprocess handles state tracking (tool calls, reasoning, finish dedup)
func (t *StreamTranslator) preprocess(event *ir.UnifiedEvent) bool {
	// Track tool calls - mark HasToolCalls but don't increment index yet
	// Index increment happens in convertEvent to maintain correct 0-based indexing
	if event.Type == ir.EventTypeToolCall {
		t.Ctx.HasToolCalls = true
	}

	// Track reasoning content for token estimation
	if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
		t.Ctx.AccumulateReasoning(event.Reasoning)
	}
	if event.Type == ir.EventTypeReasoningSummary && event.ReasoningSummary != "" {
		t.Ctx.AccumulateReasoning(event.ReasoningSummary)
	}

	// Handle finish event with deduplication and token estimation
	if event.Type == ir.EventTypeFinish {
		if !t.Ctx.MarkFinishSent() {
			return true // skip duplicate finish
		}

		// Override finish_reason if tool calls were seen
		if t.Ctx.HasToolCalls {
			event.FinishReason = ir.FinishReasonToolCalls
		}

		// Estimate reasoning tokens if provider didn't provide them
		if t.Ctx.ReasoningCharsAccum > 0 {
			if event.Usage == nil {
				event.Usage = &ir.Usage{}
			}
			if event.Usage.ThoughtsTokenCount == 0 {
				event.Usage.ThoughtsTokenCount = t.Ctx.EstimateReasoningTokens()
			}
		}
	}

	return false // don't skip
}

func isResponsesTarget(target string) bool {
	return target == "codex" || target == "openai-response"
}

// convertEventChunks converts a single event to one or more target chunks.
func (t *StreamTranslator) convertEventChunks(event *ir.UnifiedEvent) ([][]byte, error) {
	switch {
	case t.to == "openai" || t.to == "cline":
		idx := 0
		if event.Type == ir.EventTypeToolCall {
			idx = t.Ctx.ToolCallIndex
			t.Ctx.ToolCallIndex++ // Increment AFTER getting current index
		} else if event.Type == ir.EventTypeToolCallDelta {
			// For deltas, use PREVIOUS index (the tool call we're continuing)
			if t.Ctx.ToolCallIndex > 0 {
				idx = t.Ctx.ToolCallIndex - 1
			}
		}
		chunk, err := from_ir.ToOpenAIChunk(*event, t.model, t.messageID, idx)
		if chunk == nil || err != nil {
			return nil, err
		}
		return [][]byte{chunk}, nil
	case isResponsesTarget(t.to):
		if t.Ctx.ResponsesState == nil {
			t.Ctx.ResponsesState = from_ir.NewResponsesStreamState()
		}
		return from_ir.ToResponsesAPIChunk(*event, t.model, t.Ctx.ResponsesState)
	case t.to == "claude":
		chunk, err := from_ir.ToClaudeSSE(*event, t.Ctx.ClaudeState)
		if chunk == nil || err != nil {
			return nil, err
		}
		return [][]byte{chunk}, nil
	case provider.IsGeminiFormat(t.to):
		chunk, err := from_ir.ToGeminiChunk(*event, t.model)
		if chunk == nil || err != nil {
			return nil, err
		}
		return [][]byte{chunk}, nil
	case t.to == "ollama":
		chunk, err := from_ir.ToOllamaChatChunk(*event, t.model)
		if chunk == nil || err != nil {
			return nil, err
		}
		return [][]byte{chunk}, nil
	default:
		return nil, nil // unsupported format
	}
}
