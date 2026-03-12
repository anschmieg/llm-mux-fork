package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/json"

	"github.com/google/uuid"
	"github.com/nghyane/llm-mux/internal/auth/kiro"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/constant"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/translator"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
)

const kiroAPIURL = executor.KiroDefaultBaseURL

var kiroModelMapping = map[string]string{
	"auto":              "auto",
	"claude-opus-4.6":   "claude-opus-4.6",
	"claude-opus-4-6":   "claude-opus-4.6",
	"claude-opus-4.5":   "claude-opus-4.5",
	"claude-opus-4-5":   "claude-opus-4.5",
	"claude-sonnet-4.6": "claude-sonnet-4.6",
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-sonnet-4.5": "claude-sonnet-4.5",
	"claude-sonnet-4-5": "claude-sonnet-4.5",
	"claude-sonnet-4":   "claude-sonnet-4",
	"claude-haiku-4.5":  "claude-haiku-4.5",
	"claude-haiku-4-5":  "claude-haiku-4.5",
	"deepseek-3.2":      "deepseek-3.2",
	"minimax-m2.1":      "minimax-m2.1",
	"qwen3-coder-next":  "qwen3-coder-next",
}

type kiroCapabilityCacheEntry struct {
	MaterialVersion int64
	ExpiresAt       time.Time
	Models          []*registry.ModelInfo
}

var kiroCapabilityCache sync.Map

type KiroExecutor struct {
	executor.BaseExecutor
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{BaseExecutor: executor.BaseExecutor{Cfg: cfg}}
}

func (e *KiroExecutor) Identifier() string { return constant.Kiro }

func FetchKiroModels(ctx context.Context, auth *provider.Auth, cfg *config.Config) []*registry.ModelInfo {
	if auth == nil || auth.ID == "" {
		return nil
	}
	if cached, ok := loadCachedKiroModels(auth); ok {
		return cached
	}

	exec := NewKiroExecutor(cfg)
	baseModels := registry.GetKiroModels()
	supported := make([]*registry.ModelInfo, 0, len(baseModels))
	for _, model := range baseModels {
		if model == nil || model.ID == "" {
			continue
		}
		ok, reason := probeKiroModel(ctx, exec, auth, model.ID)
		if ok {
			supported = append(supported, model)
			continue
		}
		log.Debugf("kiro capability probe rejected model %s for auth %s: %s", model.ID, auth.ID, reason)
	}

	storeCachedKiroModels(auth, supported)
	return cloneKiroModels(supported)
}

func loadCachedKiroModels(auth *provider.Auth) ([]*registry.ModelInfo, bool) {
	value, ok := kiroCapabilityCache.Load(auth.ID)
	if !ok {
		return nil, false
	}
	entry, ok := value.(kiroCapabilityCacheEntry)
	if !ok {
		return nil, false
	}
	if entry.MaterialVersion != auth.MaterialVersion {
		return nil, false
	}
	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}
	return cloneKiroModels(entry.Models), true
}

func storeCachedKiroModels(auth *provider.Auth, models []*registry.ModelInfo) {
	kiroCapabilityCache.Store(auth.ID, kiroCapabilityCacheEntry{
		MaterialVersion: auth.MaterialVersion,
		ExpiresAt:       time.Now().Add(12 * time.Hour),
		Models:          cloneKiroModels(models),
	})
}

func cloneKiroModels(models []*registry.ModelInfo) []*registry.ModelInfo {
	if len(models) == 0 {
		return nil
	}
	clones := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		copy := *model
		clones = append(clones, &copy)
	}
	return clones
}

type kiroProbeClassification int

const (
	kiroProbeUnsupported kiroProbeClassification = iota
	kiroProbeSupported
	kiroProbeRetryable
)

func classifyKiroProbe(statusCode int, body []byte) kiroProbeClassification {
	msg := string(body)
	switch {
	case statusCode == http.StatusOK:
		return kiroProbeSupported
	case statusCode == http.StatusTooManyRequests && strings.Contains(msg, "INSUFFICIENT_MODEL_CAPACITY"):
		return kiroProbeSupported
	case statusCode == http.StatusBadRequest && strings.Contains(msg, "INVALID_MODEL_ID"):
		return kiroProbeUnsupported
	case statusCode >= 500:
		return kiroProbeRetryable
	default:
		return kiroProbeRetryable
	}
}

func probeKiroModel(ctx context.Context, exec *KiroExecutor, auth *provider.Auth, modelID string) (bool, string) {
	attempt := func(timeout time.Duration) (kiroProbeClassification, string) {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		req := provider.Request{
			Model: modelID,
			Payload: []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Write a Python function add(a, b) that returns their sum."}],"max_tokens":48}`,
				modelID)),
		}

		rc, err := exec.prepareRequest(probeCtx, auth, req)
		if err != nil {
			return kiroProbeRetryable, err.Error()
		}
		httpReq, err := exec.buildHTTPRequest(rc)
		if err != nil {
			return kiroProbeRetryable, err.Error()
		}

		client := exec.NewHTTPClient(probeCtx, rc.auth, executor.KiroRequestTimeout)
		resp, err := client.Do(httpReq)
		if err != nil {
			return kiroProbeRetryable, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		classification := classifyKiroProbe(resp.StatusCode, body)
		if classification == kiroProbeSupported {
			return classification, ""
		}
		return classification, classifyKiroProbeFailure(resp.StatusCode, body)
	}

	classification, reason := attempt(20 * time.Second)
	if classification == kiroProbeSupported || classification == kiroProbeUnsupported {
		return classification == kiroProbeSupported, reason
	}

	classification, retryReason := attempt(30 * time.Second)
	if classification == kiroProbeSupported {
		return true, ""
	}
	if retryReason != "" {
		return false, retryReason
	}
	return false, reason
}

func classifyKiroProbeFailure(statusCode int, body []byte) string {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Sprintf("status_%d", statusCode)
	}
	return fmt.Sprintf("status_%d:%s", statusCode, msg)
}

func (e *KiroExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *KiroExecutor) ensureValidToken(ctx context.Context, auth *provider.Auth) (string, *provider.Auth, error) {
	if auth == nil {
		return "", nil, fmt.Errorf("kiro: auth is nil")
	}
	token := getMetaString(auth.Metadata, "access_token", "accessToken")
	expiry := parseTokenExpiry(auth.Metadata)

	if token != "" && expiry.After(time.Now().Add(executor.KiroRefreshSkew)) {
		return token, nil, nil
	}

	updatedAuth, err := e.Refresh(ctx, auth)
	if err != nil {
		return "", nil, fmt.Errorf("kiro: token refresh failed: %w", err)
	}
	return getMetaString(updatedAuth.Metadata, "access_token", "accessToken"), updatedAuth, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	var creds kiro.KiroCredentials
	data, _ := json.Marshal(auth.Metadata)
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	newCreds, err := kiro.RefreshTokens(&creds)
	if err != nil {
		return nil, err
	}
	metaBytes, _ := json.Marshal(newCreds)
	var newMeta map[string]any
	json.Unmarshal(metaBytes, &newMeta)

	updatedAuth := auth.Clone()
	updatedAuth.Metadata = newMeta
	updatedAuth.LastRefreshedAt = time.Now()
	if store, ok := auth.Storage.(*kiro.KiroTokenStorage); ok {
		store.KiroCredentials = newCreds
	}
	return updatedAuth, nil
}

type kiroRequestContext struct {
	ctx         context.Context
	auth        *provider.Auth
	req         provider.Request
	token       string
	kiroModelID string
	requestID   string
	irReq       *ir.UnifiedChatRequest
	kiroBody    []byte
}

func (e *KiroExecutor) prepareRequest(ctx context.Context, auth *provider.Auth, req provider.Request) (*kiroRequestContext, error) {
	rc := &kiroRequestContext{ctx: ctx, auth: auth, req: req, requestID: uuid.New().String()[:8]}
	var err error
	rc.token, rc.auth, err = e.ensureValidToken(ctx, auth)
	if err != nil {
		return nil, err
	}
	if rc.auth == nil {
		rc.auth = auth
	}

	rc.kiroModelID = mapModelID(req.Model)
	rc.irReq, err = to_ir.ParseOpenAIRequest([]byte(ir.SanitizeText(string(req.Payload))))
	if err != nil {
		return nil, err
	}
	rc.irReq.Model = rc.kiroModelID
	if arn := getMetaString(rc.auth.Metadata, "profile_arn", "profileArn"); arn != "" {
		if rc.irReq.Metadata == nil {
			rc.irReq.Metadata = make(map[string]any)
		}
		rc.irReq.Metadata["profileArn"] = arn
	}

	rc.kiroBody, err = translator.ConvertRequest("kiro", rc.irReq)
	return rc, err
}

func (e *KiroExecutor) buildHTTPRequest(rc *kiroRequestContext) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(rc.ctx, "POST", kiroAPIURL, bytes.NewReader(rc.kiroBody))
	if err != nil {
		return nil, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	httpReq.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.7 KiroIDE-0.1.25 llm-mux")
	httpReq.Header.Set("amz-sdk-request", "attempt=1; max=1")
	if rc.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+rc.token)
	}
	return httpReq, nil
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return provider.Response{}, err
	}
	httpReq, err := e.buildHTTPRequest(rc)
	if err != nil {
		return provider.Response{}, err
	}

	client := e.NewHTTPClient(ctx, auth, executor.KiroRequestTimeout)

	resp, err := client.Do(httpReq)
	if err != nil {
		return provider.Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return provider.Response{}, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	rawData, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.Response{}, err
	}

	if hasEventStreamContentType(resp.Header.Get("Content-Type")) || looksLikeAWSEventStream(rawData) {
		return e.handleEventStreamBytes(rawData, req.Model, opts.SourceFormat)
	}
	return e.handleJSONBytes(rawData, req.Model, opts.SourceFormat)
}

func hasEventStreamContentType(contentType string) bool {
	return len(contentType) >= 37 && contentType[:37] == "application/vnd.amazon.eventstream"
}

func looksLikeAWSEventStream(data []byte) bool {
	if len(data) < 16 {
		return false
	}
	_, frame, err := splitAWSEventStream(data, false)
	if err != nil || frame == nil {
		return false
	}
	_, err = parseEventPayload(frame)
	return err == nil
}

func (e *KiroExecutor) handleEventStreamResponse(body io.ReadCloser, model string) (provider.Response, error) {
	defer body.Close()
	rawData, err := io.ReadAll(body)
	if err != nil {
		return provider.Response{}, err
	}
	return e.handleEventStreamBytes(rawData, model, provider.FromString("openai"))
}

func (e *KiroExecutor) handleEventStreamBytes(rawData []byte, model string, target provider.Format) (provider.Response, error) {
	bufPtr := stream.ScannerBufferPool.Get().(*[]byte)
	defer stream.ScannerBufferPool.Put(bufPtr)

	scanner := bufio.NewScanner(bytes.NewReader(rawData))
	scanner.Buffer(*bufPtr, executor.DefaultStreamBufferSize)
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()

	for scanner.Scan() {
		payload, err := parseEventPayload(scanner.Bytes())
		if err == nil {
			state.ProcessChunk(payload)
		}
	}

	msg := &ir.Message{Role: ir.RoleAssistant, ToolCalls: state.ToolCalls}
	if state.AccumulatedContent != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: state.AccumulatedContent})
	}
	translator := stream.NewResponseTranslator(e.Cfg, target.String(), model)
	payload, err := translator.Translate([]ir.CandidateResult{{
		Index:        0,
		Messages:     []ir.Message{*msg},
		FinishReason: state.DetermineFinishReason(),
	}}, nil, nil)
	if err != nil {
		return provider.Response{}, err
	}
	return provider.Response{Payload: payload}, nil
}

func (e *KiroExecutor) handleJSONResponse(body io.ReadCloser, model string) (provider.Response, error) {
	rawData, err := io.ReadAll(body)
	if err != nil {
		return provider.Response{}, err
	}
	return e.handleJSONBytes(rawData, model, provider.FromString("openai"))
}

func (e *KiroExecutor) handleJSONBytes(rawData []byte, model string, target provider.Format) (provider.Response, error) {
	converted, err := stream.TranslateResponseNonStream(e.Cfg, provider.FromString("kiro"), target, rawData, model)
	if err != nil {
		return provider.Response{}, err
	}
	return provider.Response{Payload: converted}, nil
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (<-chan provider.StreamChunk, error) {
	rc, err := e.prepareRequest(ctx, auth, req)
	if err != nil {
		return nil, err
	}
	httpReq, err := e.buildHTTPRequest(rc)
	if err != nil {
		return nil, err
	}

	client := e.NewHTTPClient(ctx, rc.auth, 0)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body))
	}

	out := make(chan provider.StreamChunk, 4096) // Single user: maximize throughput
	go e.processStream(ctx, resp, req.Model, opts.SourceFormat, out)
	return out, nil
}

func (e *KiroExecutor) processStream(ctx context.Context, resp *http.Response, model string, target provider.Format, out chan<- provider.StreamChunk) {
	defer resp.Body.Close()
	defer close(out)
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("kiro executor: panic in stream goroutine: %v", r)
		}
	}()

	bufPtr := stream.ScannerBufferPool.Get().(*[]byte)
	defer stream.ScannerBufferPool.Put(bufPtr)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(*bufPtr, executor.DefaultStreamBufferSize)
	scanner.Split(splitAWSEventStream)
	state := to_ir.NewKiroStreamState()
	messageID := "chatcmpl-" + uuid.New().String()
	streamCtx := stream.NewStreamContext()
	translator := stream.NewStreamTranslator(e.Cfg, provider.FromString("kiro"), target.String(), model, messageID, streamCtx)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, err := parseEventPayload(scanner.Bytes())
		if err != nil {
			continue
		}
		events, _ := state.ProcessChunk(payload)
		unifiedEvents := make([]*ir.UnifiedEvent, 0, len(events))
		for idx := range events {
			event := events[idx]
			unifiedEvents = append(unifiedEvents, &event)
		}
		result, err := translator.Translate(unifiedEvents)
		if err != nil {
			select {
			case out <- provider.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		for _, chunk := range result.Chunks {
			select {
			case out <- provider.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
	}

	chunks, err := translator.Flush()
	if err != nil {
		select {
		case out <- provider.StreamChunk{Err: err}:
		case <-ctx.Done():
		}
		return
	}
	for _, chunk := range chunks {
		select {
		case out <- provider.StreamChunk{Payload: chunk}:
		case <-ctx.Done():
			return
		}
	}
}

func (e *KiroExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	return provider.Response{Payload: []byte(`{"total_tokens": 0}`)}, nil
}

func getMetaString(meta map[string]any, keys ...string) string {
	if meta == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := meta[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func parseTokenExpiry(meta map[string]any) time.Time {
	if meta == nil {
		return time.Time{}
	}
	for _, key := range []string{"expires_at", "expiresAt"} {
		if exp, ok := meta[key].(string); ok && exp != "" {
			if t, err := time.Parse(time.RFC3339, exp); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func mapModelID(model string) string {
	if mapped, ok := kiroModelMapping[model]; ok {
		return mapped
	}
	return model
}

func splitAWSEventStream(data []byte, atEOF bool) (int, []byte, error) {
	if len(data) < 4 {
		if atEOF && len(data) > 0 {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	totalLen := int(binary.BigEndian.Uint32(data[0:4]))
	if totalLen < 16 || totalLen > 16*1024*1024 {
		return 1, nil, nil
	}
	if len(data) < totalLen {
		if atEOF {
			return len(data), nil, nil
		}
		return 0, nil, nil
	}
	return totalLen, data[:totalLen], nil
}

func parseEventPayload(frame []byte) ([]byte, error) {
	if len(frame) < 16 {
		return nil, fmt.Errorf("short frame")
	}
	if binary.BigEndian.Uint32(frame[8:12]) != crc32.ChecksumIEEE(frame[0:8]) {
		return nil, fmt.Errorf("crc mismatch")
	}
	totalLen := int(binary.BigEndian.Uint32(frame[0:4]))
	headersLen := int(binary.BigEndian.Uint32(frame[4:8]))
	start, end := 12+headersLen, totalLen-4
	if start >= end || end > len(frame) {
		return nil, fmt.Errorf("bounds")
	}
	return frame[start:end], nil
}
