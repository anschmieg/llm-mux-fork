package format

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/interfaces"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/routingpolicy"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

type BaseAPIHandler struct {
	AuthManager           *provider.Manager
	Cfg                   *config.SDKConfig
	Routing               *config.RoutingConfig
	OpenAICompatProviders []string
}

func NewBaseAPIHandlers(cfg *config.SDKConfig, routing *config.RoutingConfig, authManager *provider.Manager, openAICompatProviders []string) *BaseAPIHandler {
	return &BaseAPIHandler{
		Cfg:                   cfg,
		Routing:               routing,
		AuthManager:           authManager,
		OpenAICompatProviders: openAICompatProviders,
	}
}

func (h *BaseAPIHandler) UpdateClients(cfg *config.SDKConfig) { h.Cfg = cfg }

func (h *BaseAPIHandler) UpdateRouting(routing *config.RoutingConfig) { h.Routing = routing }

func (h *BaseAPIHandler) getFallbackChain(model string) []string {
	if h.Routing == nil {
		return nil
	}
	return h.Routing.GetFallbackChain(model)
}

// Models returns all available models as maps from the global registry.
func (h *BaseAPIHandler) Models() []map[string]any {
	return registry.GetGlobalRegistry().GetAvailableModels("openai")
}

func (h *BaseAPIHandler) GetAlt(c *gin.Context) string {
	alt, hasAlt := c.GetQuery("alt")
	if !hasAlt {
		alt, _ = c.GetQuery("$alt")
	}
	if alt == "sse" {
		return ""
	}
	return alt
}

func (h *BaseAPIHandler) GetContextWithCancel(ctx context.Context, handler interfaces.APIHandler, c *gin.Context) (context.Context, APIHandlerCancelFunc) {
	newCtx, cancel := context.WithCancel(ctx)
	newCtx = context.WithValue(newCtx, ctxKeyGin, c)
	newCtx = context.WithValue(newCtx, ctxKeyHandler, handler)
	return newCtx, func(params ...any) {
		if h.Cfg.RequestLog && len(params) == 1 {
			switch data := params[0].(type) {
			case []byte:
				appendAPIResponse(c, data)
			case error:
				appendAPIResponse(c, []byte(data.Error()))
			case string:
				appendAPIResponse(c, []byte(data))
			}
		}
		cancel()
	}
}

// Context keys to avoid string allocation on each request
type ctxKey int

const (
	ctxKeyGin ctxKey = iota
	ctxKeyHandler
)

const routingFallbackChainMetadataKey = "routing_fallback_chain"
const routingDowngradeReasonMetadataKey = "routing_downgrade_reason"

func appendAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}
	c.Set("API_RESPONSE", bytes.Clone(data))
}

// buildRequestOpts creates request and options, cloning payload/metadata only once (shared reference)
func buildRequestOpts(normalizedModel string, rawJSON []byte, metadata map[string]any, handlerType string, alt string, stream bool) (provider.Request, provider.Options) {
	payload := cloneBytes(rawJSON)
	payload = syncPayloadModel(payload, normalizedModel)
	meta := cloneMetadata(metadata)

	sourceFormat := provider.Format(handlerType)

	req := provider.Request{
		Model:    normalizedModel,
		Payload:  payload,
		Metadata: meta,
	}
	opts := provider.Options{
		Stream:          stream,
		Alt:             alt,
		OriginalRequest: payload, // Same slice, no second clone
		SourceFormat:    sourceFormat,
		Metadata:        meta, // Same map, no second clone
	}
	return req, opts
}

func syncPayloadModel(payload []byte, normalizedModel string) []byte {
	if len(payload) == 0 || strings.TrimSpace(normalizedModel) == "" {
		return payload
	}
	if !gjson.ValidBytes(payload) {
		return payload
	}
	if !gjson.GetBytes(payload, "model").Exists() {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "model", normalizedModel)
	if err != nil {
		return payload
	}
	return updated
}

// extractErrorDetails extracts status code and headers from error interface
func extractErrorDetails(err error) (int, http.Header) {
	status := http.StatusInternalServerError
	if se, ok := err.(interface{ StatusCode() int }); ok {
		if code := se.StatusCode(); code > 0 {
			status = code
		}
	}
	var addon http.Header
	if he, ok := err.(interface{ Headers() http.Header }); ok {
		if hdr := he.Headers(); hdr != nil {
			addon = hdr.Clone()
		}
	}
	return status, addon
}

func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, false)
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err == nil {
		return resp.Payload, nil
	}

	fallbacks := h.effectiveFallbackChain(normalizedModel, metadata)
	attemptedModels := map[string]struct{}{normalizedModel: {}}
	for _, fallbackModel := range fallbacks {
		fbProviders, fbNormalizedModel, fbMetadata, _ := h.getRequestDetails(fallbackModel)
		if len(fbProviders) == 0 {
			continue
		}
		if _, seen := attemptedModels[fbNormalizedModel]; seen {
			continue
		}
		attemptedModels[fbNormalizedModel] = struct{}{}
		fbReq, fbOpts := buildRequestOpts(fbNormalizedModel, rawJSON, fbMetadata, handlerType, alt, false)
		fbResp, fbErr := h.AuthManager.Execute(ctx, fbProviders, fbReq, fbOpts)
		if fbErr == nil {
			return fbResp.Payload, nil
		}
	}

	status, addon := extractErrorDetails(err)
	return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
}

func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		return nil, errMsg
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, false)
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		status, addon := extractErrorDetails(err)
		return nil, &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	}
	return resp.Payload, nil
}

func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	providers, normalizedModel, metadata, errMsg := h.getRequestDetails(modelName)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, errChan
	}
	req, opts := buildRequestOpts(normalizedModel, rawJSON, metadata, handlerType, alt, true)
	chunks, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err == nil {
		return h.wrapStreamChannel(ctx, chunks)
	}

	fallbacks := h.effectiveFallbackChain(normalizedModel, metadata)
	attemptedModels := map[string]struct{}{normalizedModel: {}}
	for _, fallbackModel := range fallbacks {
		fbProviders, fbNormalizedModel, fbMetadata, _ := h.getRequestDetails(fallbackModel)
		if len(fbProviders) == 0 {
			continue
		}
		if _, seen := attemptedModels[fbNormalizedModel]; seen {
			continue
		}
		attemptedModels[fbNormalizedModel] = struct{}{}
		fbReq, fbOpts := buildRequestOpts(fbNormalizedModel, rawJSON, fbMetadata, handlerType, alt, true)
		fbChunks, fbErr := h.AuthManager.ExecuteStream(ctx, fbProviders, fbReq, fbOpts)
		if fbErr == nil {
			return h.wrapStreamChannel(ctx, fbChunks)
		}
	}

	errChan := make(chan *interfaces.ErrorMessage, 1)
	status, addon := extractErrorDetails(err)
	errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
	close(errChan)
	return nil, errChan
}

func (h *BaseAPIHandler) wrapStreamChannel(ctx context.Context, chunks <-chan provider.StreamChunk) (<-chan []byte, <-chan *interfaces.ErrorMessage) {
	dataChan := make(chan []byte, 128)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		defer close(dataChan)
		defer close(errChan)
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-chunks:
				if !ok {
					return
				}
				if chunk.Err != nil {
					status, addon := extractErrorDetails(chunk.Err)
					select {
					case errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: chunk.Err, Addon: addon}:
					case <-ctx.Done():
					}
					return
				}
				if len(chunk.Payload) > 0 {
					select {
					case dataChan <- chunk.Payload:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return dataChan, errChan
}

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, metadata map[string]any, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithSeen(modelName, make(map[string]struct{}))
}

func (h *BaseAPIHandler) getRequestDetailsWithSeen(modelName string, seen map[string]struct{}) (providers []string, normalizedModel string, metadata map[string]any, err *interfaces.ErrorMessage) {
	if _, exists := seen[modelName]; exists {
		return nil, "", nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}
	seen[modelName] = struct{}{}

	resolvedModelName := util.ResolveAutoModel(modelName)
	specifiedProvider := util.ExtractProviderFromPrefixedModelID(resolvedModelName)
	cleanModelName := util.NormalizeIncomingModelID(resolvedModelName)
	var profileFallbackChain []string

	if h.Routing != nil {
		requestedProfileID := cleanModelName
		if profilePrimary, ok := h.Routing.ResolveProfilePrimary(requestedProfileID); ok {
			cleanModelName = util.NormalizeIncomingModelID(profilePrimary)
			profileFallbackChain = h.Routing.GetProfileFallbackChain(requestedProfileID)
		}
		cleanModelName = h.Routing.ResolveModelAlias(cleanModelName)
		if specifiedProvider == "" {
			if downgradeModel, downgradeReason, downgraded := routingpolicy.Global().ShouldDowngrade(cleanModelName, h.Routing); downgraded {
				downgradeTarget := util.NormalizeIncomingModelID(downgradeModel)
				if profilePrimary, ok := h.Routing.ResolveProfilePrimary(downgradeTarget); ok {
					profileFallbackChain = h.Routing.GetProfileFallbackChain(downgradeTarget)
					downgradeTarget = util.NormalizeIncomingModelID(profilePrimary)
				} else {
					profileFallbackChain = nil
				}
				cleanModelName = h.Routing.ResolveModelAlias(downgradeTarget)
				if metadata == nil {
					metadata = make(map[string]any, 1)
				}
				metadata[routingDowngradeReasonMetadataKey] = downgradeReason
			}
		}
	}

	if len(profileFallbackChain) > 0 {
		if metadata == nil {
			metadata = make(map[string]any, 1)
		}
		metadata[routingFallbackChainMetadataKey] = append([]string(nil), profileFallbackChain...)
	}

	providerName, extractedModelName, isDynamic := h.parseDynamicModel(cleanModelName)
	normalizedModel, geminiMetadata := util.NormalizeGeminiThinkingModel(cleanModelName)
	metadata = mergeRoutingMetadata(metadata, geminiMetadata)

	if isDynamic {
		providers = []string{providerName}
		normalizedModel = extractedModelName
	} else if specifiedProvider != "" {
		providers = []string{specifiedProvider}
	} else {
		// GetProviderName uses canonical index for cross-provider routing
		// Translation happens in executeWithProvider via GetModelIDForProvider
		providers = util.GetProviderName(normalizedModel)
	}

	if len(providers) == 0 {
		if specifiedProvider == "" {
			if h.Routing != nil {
				if aliasTarget := h.Routing.ResolveModelAlias(modelName); aliasTarget != "" && aliasTarget != modelName {
					return h.getRequestDetailsWithSeen(aliasTarget, seen)
				}
			}
			if len(profileFallbackChain) > 0 {
				resolvedProviders, resolvedModel, resolvedMetadata, resolved := h.resolveFallbackChain(profileFallbackChain)
				if resolved {
					return resolvedProviders, resolvedModel, mergeRoutingMetadata(metadata, resolvedMetadata), nil
				}
			}
			resolvedProviders, resolvedModel, resolvedMetadata, resolved := h.resolveFallbackSeed(normalizedModel)
			if resolved {
				return resolvedProviders, resolvedModel, mergeRoutingMetadata(metadata, resolvedMetadata), nil
			}
		}
		return nil, "", nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: fmt.Errorf("unknown provider for model %s", modelName)}
	}
	return providers, normalizedModel, metadata, nil
}

func (h *BaseAPIHandler) resolveFallbackSeed(seedModel string) ([]string, string, map[string]any, bool) {
	return h.resolveFallbackSeedWithSeen(seedModel, make(map[string]struct{}))
}

func (h *BaseAPIHandler) resolveFallbackSeedWithSeen(seedModel string, seen map[string]struct{}) ([]string, string, map[string]any, bool) {
	if _, exists := seen[seedModel]; exists {
		return nil, "", nil, false
	}
	seen[seedModel] = struct{}{}

	fallbacks := h.getFallbackChain(seedModel)
	if len(fallbacks) == 0 {
		return nil, "", nil, false
	}
	return h.resolveFallbackChainWithSeen(fallbacks, seen)
}

func (h *BaseAPIHandler) resolveFallbackChain(fallbacks []string) ([]string, string, map[string]any, bool) {
	return h.resolveFallbackChainWithSeen(fallbacks, make(map[string]struct{}))
}

func (h *BaseAPIHandler) resolveFallbackChainWithSeen(fallbacks []string, seen map[string]struct{}) ([]string, string, map[string]any, bool) {
	if len(fallbacks) == 0 {
		return nil, "", nil, false
	}

	for _, fallbackModel := range fallbacks {
		normalizedFallback := util.NormalizeIncomingModelID(fallbackModel)
		if h.Routing != nil {
			normalizedFallback = h.Routing.ResolveModelAlias(normalizedFallback)
		}

		providers := util.GetProviderName(normalizedFallback)
		if len(providers) == 0 {
			resolvedProviders, resolvedModel, resolvedMetadata, resolved := h.resolveFallbackSeedWithSeen(normalizedFallback, seen)
			if resolved {
				return resolvedProviders, resolvedModel, resolvedMetadata, true
			}
			continue
		}

		metadata := map[string]any{
			routingFallbackChainMetadataKey: append([]string(nil), fallbacks...),
		}
		return providers, normalizedFallback, metadata, true
	}
	return nil, "", nil, false
}

func mergeRoutingMetadata(base, extra map[string]any) map[string]any {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}
	if len(base) == 0 {
		return cloneMetadata(extra)
	}
	if len(extra) == 0 {
		return base
	}
	out := cloneMetadata(base)
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func (h *BaseAPIHandler) effectiveFallbackChain(normalizedModel string, metadata map[string]any) []string {
	if metadata != nil {
		if chainRaw, exists := metadata[routingFallbackChainMetadataKey]; exists {
			switch typed := chainRaw.(type) {
			case []string:
				return typed
			case []any:
				chain := make([]string, 0, len(typed))
				for _, entry := range typed {
					asString, ok := entry.(string)
					if !ok || asString == "" {
						continue
					}
					chain = append(chain, asString)
				}
				if len(chain) > 0 {
					return chain
				}
			}
		}
	}
	return h.getFallbackChain(normalizedModel)
}

func (h *BaseAPIHandler) parseDynamicModel(modelName string) (providerName, model string, isDynamic bool) {
	if parts := strings.SplitN(modelName, "://", 2); len(parts) == 2 {
		for _, pName := range h.OpenAICompatProviders {
			if pName == parts[0] {
				return parts[0], parts[1], true
			}
		}
	}
	return "", modelName, false
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	return bytes.Clone(src)
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (h *BaseAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && msg.Addon != nil {
		for key, values := range msg.Addon {
			if len(values) == 0 {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}
	c.Status(status)
	if msg != nil && msg.Error != nil {
		errResp := ErrorResponse{
			Error: ErrorDetail{
				Message: msg.Error.Error(),
				Type:    "server_error",
			},
		}
		c.JSON(status, errResp)
	} else {
		c.JSON(status, ErrorResponse{
			Error: ErrorDetail{
				Message: http.StatusText(status),
				Type:    "server_error",
			},
		})
	}
}

func (h *BaseAPIHandler) LoggingAPIResponseError(ctx context.Context, err *interfaces.ErrorMessage) {
	if !h.Cfg.RequestLog {
		return
	}
	ginContext, ok := ctx.Value(ctxKeyGin).(*gin.Context)
	if !ok {
		return
	}
	if apiResponseErrors, isExist := ginContext.Get("API_RESPONSE_ERROR"); isExist {
		if slices, isOk := apiResponseErrors.([]*interfaces.ErrorMessage); isOk {
			ginContext.Set("API_RESPONSE_ERROR", append(slices, err))
			return
		}
	}
	ginContext.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{err})
}

type APIHandlerCancelFunc func(params ...any)

// SSEWriter wraps gin ResponseWriter with error-checked writes for SSE streaming.
// Following Ollama's pattern: write errors terminate stream immediately to free upstream resources.
type SSEWriter struct {
	w   gin.ResponseWriter
	err error
}

// NewSSEWriter creates a new SSE writer wrapper.
func NewSSEWriter(w gin.ResponseWriter) *SSEWriter {
	return &SSEWriter{w: w}
}

// Write writes data and tracks first error. Subsequent writes are no-ops after error.
func (s *SSEWriter) Write(data []byte) bool {
	if s.err != nil {
		return false
	}
	_, s.err = s.w.Write(data)
	return s.err == nil
}

// WriteString writes a string and tracks first error.
func (s *SSEWriter) WriteString(data string) bool {
	if s.err != nil {
		return false
	}
	_, s.err = s.w.WriteString(data)
	return s.err == nil
}

// Err returns the first write error encountered.
func (s *SSEWriter) Err() error {
	return s.err
}

// Ok returns true if no write errors have occurred.
func (s *SSEWriter) Ok() bool {
	return s.err == nil
}
