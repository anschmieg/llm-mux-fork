package routingpolicy

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/nghyane/llm-mux/internal/usage"
)

const microsPerUSD = 1_000_000

// Snapshot is an immutable view of current routing policy counters.
type Snapshot struct {
	DayUTC                    string  `json:"day_utc"`
	EstimatedCostUSDToday     float64 `json:"estimated_cost_usd_today"`
	MaxEstimatedCostUSDPerDay float64 `json:"max_estimated_cost_usd_per_day,omitempty"`
	ExpensiveCallsToday       int64   `json:"expensive_calls_today"`
	MaxExpensiveCallsPerDay   int64   `json:"max_expensive_calls_per_day,omitempty"`
	DowngradedRequestsToday   int64   `json:"downgraded_requests_today"`
	DowngradeModel            string  `json:"downgrade_model,omitempty"`
	PolicyEnabled             bool    `json:"policy_enabled"`
}

type dayCounters struct {
	day                 string
	estimatedCostMicros int64
	expensiveCalls      int64
	downgradedRequests  int64
}

type pricingRule struct {
	pattern              string
	inputMicrosPerToken  int64
	outputMicrosPerToken int64
}

type policySnapshot struct {
	expensivePatterns []string
	pricingRules      []pricingRule
}

// Manager tracks daily routing policy counters and provides downgrade decisions.
type Manager struct {
	mu       sync.Mutex
	counters dayCounters
	policy   policySnapshot
}

var (
	globalManagerOnce sync.Once
	globalManager     *Manager
	registerOnce      sync.Once
)

// Global returns the singleton routing policy manager.
func Global() *Manager {
	globalManagerOnce.Do(func() {
		globalManager = &Manager{
			counters: dayCounters{day: utcDay(time.Now().UTC())},
			policy:   policySnapshot{},
		}
	})
	return globalManager
}

// RegisterUsagePlugin registers the global routing policy manager as a usage plugin once.
func RegisterUsagePlugin() {
	registerOnce.Do(func() {
		usage.RegisterPlugin(Global())
	})
}

// UpdateRouting refreshes policy matchers and pricing from the active runtime config.
func (m *Manager) UpdateRouting(routing *config.RoutingConfig) {
	if m == nil || routing == nil {
		return
	}
	snapshot := policySnapshot{
		expensivePatterns: normalizePatterns(routing.Policy.ExpensiveModelPatterns),
		pricingRules:      buildPricingRules(routing.Policy.ModelPricingUSDPer1K),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policy = snapshot
}

// HandleUsage implements usage.Plugin.
func (m *Manager) HandleUsage(_ context.Context, record usage.Record) {
	if m == nil {
		return
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		return
	}
	day := utcDay(record.RequestedAt)
	if day == "" {
		day = utcDay(time.Now().UTC())
	}
	estimatedCostMicros := m.estimateCostMicrosLocked(model, record)
	expensive := m.isExpensiveLocked(model)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDay(day)
	if estimatedCostMicros > 0 {
		m.counters.estimatedCostMicros += estimatedCostMicros
	}
	if expensive {
		m.counters.expensiveCalls++
	}
}

// ShouldDowngrade checks whether the given model should be downgraded based on routing policy.
func (m *Manager) ShouldDowngrade(model string, routing *config.RoutingConfig) (downgradeModel string, reason string, downgraded bool) {
	if m == nil || routing == nil {
		return "", "", false
	}
	policy := routing.Policy
	if !policy.Enabled {
		return "", "", false
	}

	originalModel := strings.TrimSpace(model)
	downgradeTarget := strings.TrimSpace(policy.DowngradeModel)
	if originalModel == "" || downgradeTarget == "" || strings.EqualFold(originalModel, downgradeTarget) {
		return "", "", false
	}
	if !isExpensiveModel(originalModel, policy.ExpensiveModelPatterns) {
		return "", "", false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDay(utcDay(time.Now().UTC()))

	if policy.MaxExpensiveCallsPerDay > 0 && m.counters.expensiveCalls >= policy.MaxExpensiveCallsPerDay {
		m.counters.downgradedRequests++
		return downgradeTarget, "max_expensive_calls_per_day_reached", true
	}

	maxCostMicros := usdToMicros(policy.MaxEstimatedCostUSDPerDay)
	if maxCostMicros > 0 && m.counters.estimatedCostMicros >= maxCostMicros {
		m.counters.downgradedRequests++
		return downgradeTarget, "max_estimated_cost_usd_per_day_reached", true
	}

	return "", "", false
}

// Snapshot returns current counters + policy limits for dashboard consumption.
func (m *Manager) Snapshot(routing *config.RoutingConfig) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureDay(utcDay(time.Now().UTC()))

	s := Snapshot{
		DayUTC:                  m.counters.day,
		EstimatedCostUSDToday:   microsToUSD(m.counters.estimatedCostMicros),
		ExpensiveCallsToday:     m.counters.expensiveCalls,
		DowngradedRequestsToday: m.counters.downgradedRequests,
	}
	if routing != nil {
		s.PolicyEnabled = routing.Policy.Enabled
		s.DowngradeModel = strings.TrimSpace(routing.Policy.DowngradeModel)
		s.MaxExpensiveCallsPerDay = routing.Policy.MaxExpensiveCallsPerDay
		s.MaxEstimatedCostUSDPerDay = routing.Policy.MaxEstimatedCostUSDPerDay
	}
	return s
}

func (m *Manager) ensureDay(day string) {
	if day == "" {
		day = utcDay(time.Now().UTC())
	}
	if m.counters.day == day {
		return
	}
	m.counters = dayCounters{day: day}
}

func utcDay(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format("2006-01-02")
}

func (m *Manager) estimateCostMicrosLocked(model string, record usage.Record) int64 {
	// Uses static price map from config template for now; if model has no price entry, cost is 0.
	inputMicrosPerToken, outputMicrosPerToken, ok := m.resolveModelPricingLocked(model)
	if !ok {
		return 0
	}
	if record.Usage == nil {
		return 0
	}
	inputTokens := record.Usage.PromptTokens
	outputTokens := record.Usage.CompletionTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	return inputTokens*inputMicrosPerToken + outputTokens*outputMicrosPerToken
}

func (m *Manager) isExpensiveLocked(model string) bool {
	if len(m.policy.expensivePatterns) > 0 {
		return isExpensiveModel(model, m.policy.expensivePatterns)
	}
	return isExpensiveModel(model, []string{"gpt-5*", "claude-opus-*", "claude-sonnet-*"})
}

func isExpensiveModel(model string, patterns []string) bool {
	m := strings.TrimSpace(model)
	if m == "" || len(patterns) == 0 {
		return false
	}
	for _, pattern := range patterns {
		if sseutil.MatchModelPattern(strings.TrimSpace(pattern), m) {
			return true
		}
	}
	return false
}

func (m *Manager) resolveModelPricingLocked(model string) (int64, int64, bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return 0, 0, false
	}
	for _, rule := range m.policy.pricingRules {
		if sseutil.MatchModelPattern(rule.pattern, trimmed) {
			return rule.inputMicrosPerToken, rule.outputMicrosPerToken, true
		}
	}
	return 0, 0, false
}

func normalizePatterns(patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func buildPricingRules(input map[string]config.ModelPriceUSDPer1K) []pricingRule {
	if len(input) == 0 {
		return nil
	}
	rules := make([]pricingRule, 0, len(input))
	for pattern, price := range input {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		rules = append(rules, pricingRule{
			pattern:              trimmed,
			inputMicrosPerToken:  usdToMicrosPerToken(price.Input),
			outputMicrosPerToken: usdToMicrosPerToken(price.Output),
		})
	}
	sort.SliceStable(rules, func(i, j int) bool {
		// Prefer more specific patterns first.
		iWild := strings.Count(rules[i].pattern, "*")
		jWild := strings.Count(rules[j].pattern, "*")
		if iWild != jWild {
			return iWild < jWild
		}
		if len(rules[i].pattern) != len(rules[j].pattern) {
			return len(rules[i].pattern) > len(rules[j].pattern)
		}
		return rules[i].pattern < rules[j].pattern
	})
	return rules
}

func usdToMicros(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	return int64(math.Round(usd * microsPerUSD))
}

func microsToUSD(micros int64) float64 {
	if micros <= 0 {
		return 0
	}
	return float64(micros) / microsPerUSD
}

func usdToMicrosPerToken(usdPer1k float64) int64 {
	if usdPer1k <= 0 {
		return 0
	}
	// usd per token = usdPer1k / 1000; convert to micros => usdPer1k * 1000
	return int64(math.Round(usdPer1k * 1000))
}
