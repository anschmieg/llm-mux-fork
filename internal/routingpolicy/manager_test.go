package routingpolicy

import (
	"context"
	"testing"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/usage"
)

func TestShouldDowngradeOnExpensiveCallLimit(t *testing.T) {
	m := &Manager{
		counters: dayCounters{
			day:            utcDay(time.Now().UTC()),
			expensiveCalls: 3,
		},
	}
	routing := &config.RoutingConfig{
		Policy: config.RoutingPolicyConfig{
			Enabled:                 true,
			DowngradeModel:          "chat-fast",
			ExpensiveModelPatterns:  []string{"gpt-5*"},
			MaxExpensiveCallsPerDay: 3,
		},
	}

	target, reason, downgraded := m.ShouldDowngrade("gpt-5", routing)
	if !downgraded {
		t.Fatalf("expected downgrade to trigger")
	}
	if target != "chat-fast" {
		t.Fatalf("unexpected downgrade target: %q", target)
	}
	if reason == "" {
		t.Fatalf("expected downgrade reason")
	}
}

func TestHandleUsageTracksCostAndSnapshot(t *testing.T) {
	m := &Manager{
		counters: dayCounters{day: utcDay(time.Now().UTC())},
	}
	routing := &config.RoutingConfig{
		Policy: config.RoutingPolicyConfig{
			Enabled:                 true,
			DowngradeModel:          "chat-fast",
			ExpensiveModelPatterns:  []string{"gpt-5*"},
			MaxExpensiveCallsPerDay: 100,
			ModelPricingUSDPer1K: map[string]config.ModelPriceUSDPer1K{
				"gpt-5*": {Input: 0.01, Output: 0.03},
			},
		},
	}
	m.UpdateRouting(routing)

	m.HandleUsage(context.Background(), usage.Record{
		Model:       "gpt-5",
		RequestedAt: time.Now().UTC(),
		Usage: &ir.Usage{
			PromptTokens:     1000,
			CompletionTokens: 1000,
		},
	})

	s := m.Snapshot(routing)
	if s.EstimatedCostUSDToday <= 0 {
		t.Fatalf("expected estimated cost > 0, got %.6f", s.EstimatedCostUSDToday)
	}
	if s.ExpensiveCallsToday != 1 {
		t.Fatalf("expected expensive calls to be 1, got %d", s.ExpensiveCallsToday)
	}
}
