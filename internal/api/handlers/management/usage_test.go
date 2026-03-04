package management

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/usage"
)

func TestComputePoolStats(t *testing.T) {
	handler := &Handler{
		cfg: &config.Config{
			Routing: config.RoutingConfig{
				Fallbacks: map[string][]string{
					"tool-use": {"claude-sonnet-4", "glm-5", "gpt-4.1"},
				},
			},
		},
	}

	byModel := map[string]UsageModelStats{
		"claude-sonnet-4": {
			Requests: 10,
			Success:  8,
			Failure:  2,
			Tokens:   TokenSummary{Total: 1000, Input: 600, Output: 350, Reasoning: 50},
		},
		"glm-5": {
			Requests: 5,
			Success:  5,
			Failure:  0,
			Tokens:   TokenSummary{Total: 500, Input: 300, Output: 180, Reasoning: 20},
		},
	}

	got := handler.computePoolStats(byModel)
	pool, ok := got["tool-use"]
	if !ok {
		t.Fatalf("expected tool-use pool stats")
	}
	if pool.Requests != 15 || pool.Success != 13 || pool.Failure != 2 {
		t.Fatalf("unexpected pool request counters: %+v", pool)
	}
	if pool.Tokens.Total != 1500 || pool.Tokens.Input != 900 || pool.Tokens.Output != 530 || pool.Tokens.Reasoning != 70 {
		t.Fatalf("unexpected pool token counters: %+v", pool.Tokens)
	}
}

func TestComputePoolStatsNormalizesAliases(t *testing.T) {
	handler := &Handler{
		cfg: &config.Config{
			Routing: config.RoutingConfig{
				Aliases: map[string]string{
					"chat-fast": "gpt-4o",
				},
				Fallbacks: map[string][]string{
					"chat-fast": {"gpt-4.1"},
				},
			},
		},
	}
	handler.cfg.Routing.Init()

	byModel := map[string]UsageModelStats{
		"gpt-4o":  {Requests: 4, Success: 4, Tokens: TokenSummary{Total: 400}},
		"gpt-4.1": {Requests: 2, Success: 2, Tokens: TokenSummary{Total: 200}},
	}

	got := handler.computePoolStats(byModel)
	pool, ok := got["gpt-4o"]
	if !ok {
		t.Fatalf("expected normalized pool key gpt-4o")
	}
	if pool.Requests != 6 || pool.Tokens.Total != 600 {
		t.Fatalf("unexpected normalized pool aggregate: %+v", pool)
	}
}

func TestGetUsageStatisticsIncludesByPoolAndOmitsWhenEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()

	withPool := &Handler{
		cfg: &config.Config{
			Usage: config.UsageConfig{RetentionDays: 30},
			Routing: config.RoutingConfig{
				Fallbacks: map[string][]string{
					"tool-use": {"gpt-4o", "gpt-4.1"},
				},
			},
		},
		usagePlugin: usage.NewLoggerPlugin(&fakeUsageBackend{
			modelStats: []usage.ModelStats{
				{Model: "gpt-4o", Provider: "dummy", Requests: 3, SuccessCount: 3, TotalTokens: 300},
				{Model: "gpt-4.1", Provider: "dummy", Requests: 2, SuccessCount: 2, TotalTokens: 200},
			},
		}),
	}
	withPool.cfg.Routing.Init()

	w1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(w1)
	c1.Request = httptest.NewRequest("GET", "/v1/management/usage", nil)
	withPool.GetUsageStatistics(c1)

	if w1.Code != 200 {
		t.Fatalf("expected status 200, got %d", w1.Code)
	}
	var resp1 map[string]any
	if err := json.Unmarshal(w1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	data1, _ := resp1["data"].(map[string]any)
	if _, exists := data1["by_pool"]; !exists {
		t.Fatalf("expected by_pool in response when model stats and pools exist")
	}

	withoutPool := &Handler{
		cfg: &config.Config{
			Usage:   config.UsageConfig{RetentionDays: 30},
			Routing: config.RoutingConfig{Fallbacks: map[string][]string{"tool-use": {"gpt-4o"}}},
		},
		usagePlugin: usage.NewLoggerPlugin(&fakeUsageBackend{
			modelStats: nil,
			now:        now,
		}),
	}
	withoutPool.cfg.Routing.Init()

	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = httptest.NewRequest("GET", "/v1/management/usage", nil)
	withoutPool.GetUsageStatistics(c2)

	if w2.Code != 200 {
		t.Fatalf("expected status 200, got %d", w2.Code)
	}
	var resp2 map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	data2, _ := resp2["data"].(map[string]any)
	if _, exists := data2["by_pool"]; exists {
		t.Fatalf("expected by_pool omitted when empty")
	}
}

type fakeUsageBackend struct {
	modelStats []usage.ModelStats
	now        time.Time
}

func (f *fakeUsageBackend) Enqueue(record usage.UsageRecord) {}
func (f *fakeUsageBackend) Flush(ctx context.Context) error  { return nil }
func (f *fakeUsageBackend) QueryGlobalStats(ctx context.Context, since time.Time) (*usage.AggregatedStats, error) {
	return &usage.AggregatedStats{}, nil
}
func (f *fakeUsageBackend) QueryDailyStats(ctx context.Context, since time.Time) ([]usage.DailyStats, error) {
	return nil, nil
}
func (f *fakeUsageBackend) QueryHourlyStats(ctx context.Context, since time.Time) ([]usage.HourlyStats, error) {
	return nil, nil
}
func (f *fakeUsageBackend) QueryProviderStats(ctx context.Context, since time.Time) ([]usage.ProviderStats, error) {
	return nil, nil
}
func (f *fakeUsageBackend) QueryAuthStats(ctx context.Context, since time.Time) ([]usage.AuthStats, error) {
	return nil, nil
}
func (f *fakeUsageBackend) QueryModelStats(ctx context.Context, since time.Time) ([]usage.ModelStats, error) {
	return f.modelStats, nil
}
func (f *fakeUsageBackend) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeUsageBackend) Start() error { return nil }
func (f *fakeUsageBackend) Stop() error  { return nil }
