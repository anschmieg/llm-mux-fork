package management

import (
	"slices"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/util"
)

func (h *Handler) GetUsageStatistics(c *gin.Context) {
	if h == nil || h.usagePlugin == nil {
		respondOK(c, UsageStatsResponse{})
		return
	}

	retentionDays := 30
	if cfg := h.getConfig(); cfg != nil && cfg.Usage.RetentionDays > 0 {
		retentionDays = cfg.Usage.RetentionDays
	}

	from, to := h.parseTimeRange(c, retentionDays)

	counters := h.usagePlugin.GetCounters()

	response := UsageStatsResponse{
		Summary: UsageSummary{
			TotalRequests: counters.TotalRequests,
			SuccessCount:  counters.SuccessCount,
			FailureCount:  counters.FailureCount,
			Tokens: TokenSummary{
				Total: counters.TotalTokens,
			},
		},
		Period: UsagePeriod{
			From:          from,
			To:            to,
			RetentionDays: retentionDays,
		},
	}

	backend := h.usagePlugin.GetBackend()
	if backend == nil {
		respondOK(c, response)
		return
	}

	ctx := c.Request.Context()

	if providerStats, err := backend.QueryProviderStats(ctx, from); err != nil {
		log.Warnf("usage: failed to query provider stats: %v", err)
	} else if len(providerStats) > 0 {
		byProvider := make(map[string]UsageProviderStats, len(providerStats))
		var totalInput, totalOutput, totalReasoning int64
		for _, ps := range providerStats {
			byProvider[ps.Provider] = UsageProviderStats{
				Requests: ps.Requests,
				Success:  ps.SuccessCount,
				Failure:  ps.FailureCount,
				Tokens: TokenSummary{
					Total:     ps.TotalTokens,
					Input:     ps.InputTokens,
					Output:    ps.OutputTokens,
					Reasoning: ps.ReasoningTokens,
				},
				AccountCount: ps.AccountCount,
				Models:       ps.Models,
			}
			totalInput += ps.InputTokens
			totalOutput += ps.OutputTokens
			totalReasoning += ps.ReasoningTokens
		}
		response.ByProvider = byProvider
		response.Summary.Tokens.Input = totalInput
		response.Summary.Tokens.Output = totalOutput
		response.Summary.Tokens.Reasoning = totalReasoning
	}

	if authStats, err := backend.QueryAuthStats(ctx, from); err != nil {
		log.Warnf("usage: failed to query auth stats: %v", err)
	} else if len(authStats) > 0 {
		byAccount := make(map[string]UsageAccountStats, len(authStats))
		for _, as := range authStats {
			key := as.Provider + ":" + as.AuthID
			byAccount[key] = UsageAccountStats{
				Provider: as.Provider,
				AuthID:   as.AuthID,
				Requests: as.Requests,
				Success:  as.SuccessCount,
				Failure:  as.FailureCount,
				Tokens: TokenSummary{
					Total:     as.TotalTokens,
					Input:     as.InputTokens,
					Output:    as.OutputTokens,
					Reasoning: as.ReasoningTokens,
				},
			}
		}
		response.ByAccount = byAccount
	}

	if modelStats, err := backend.QueryModelStats(ctx, from); err != nil {
		log.Warnf("usage: failed to query model stats: %v", err)
	} else if len(modelStats) > 0 {
		byModel := make(map[string]UsageModelStats, len(modelStats))
		for _, ms := range modelStats {
			byModel[ms.Model] = UsageModelStats{
				Provider: ms.Provider,
				Requests: ms.Requests,
				Success:  ms.SuccessCount,
				Failure:  ms.FailureCount,
				Tokens: TokenSummary{
					Total:     ms.TotalTokens,
					Input:     ms.InputTokens,
					Output:    ms.OutputTokens,
					Reasoning: ms.ReasoningTokens,
				},
			}
		}
		response.ByModel = byModel
		response.ByPool = h.computePoolStats(byModel)
	}

	timeline := &UsageTimeline{}
	hasTimeline := false

	if dailyStats, err := backend.QueryDailyStats(ctx, from); err != nil {
		log.Warnf("usage: failed to query daily stats: %v", err)
	} else if len(dailyStats) > 0 {
		byDay := make([]UsageDayStats, 0, len(dailyStats))
		for _, d := range dailyStats {
			byDay = append(byDay, UsageDayStats{
				Day:      d.Day,
				Requests: d.Requests,
				Tokens:   d.Tokens,
			})
		}
		timeline.ByDay = byDay
		hasTimeline = true
	}

	if hourlyStats, err := backend.QueryHourlyStats(ctx, from); err != nil {
		log.Warnf("usage: failed to query hourly stats: %v", err)
	} else if len(hourlyStats) > 0 {
		byHour := make([]UsageHourStats, 0, len(hourlyStats))
		for _, h := range hourlyStats {
			byHour = append(byHour, UsageHourStats{
				Hour:     h.Hour,
				Requests: h.Requests,
				Tokens:   h.Tokens,
			})
		}
		timeline.ByHour = byHour
		hasTimeline = true
	}

	if hasTimeline {
		response.Timeline = timeline
	}

	respondOK(c, response)
}

func (h *Handler) parseTimeRange(c *gin.Context, retentionDays int) (from, to time.Time) {
	to = time.Now()
	from = to.AddDate(0, 0, -retentionDays)

	if daysStr := c.Query("days"); daysStr != "" {
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			from = to.AddDate(0, 0, -days)
		}
	}

	if fromStr := c.Query("from"); fromStr != "" {
		if parsed, err := time.Parse("2006-01-02", fromStr); err == nil {
			from = parsed
		} else if parsed, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = parsed
		}
	}

	if toStr := c.Query("to"); toStr != "" {
		if parsed, err := time.Parse("2006-01-02", toStr); err == nil {
			to = parsed.Add(24*time.Hour - time.Second)
		} else if parsed, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = parsed
		}
	}

	return from, to
}

func (h *Handler) computePoolStats(byModel map[string]UsageModelStats) map[string]UsagePoolStats {
	if len(byModel) == 0 {
		return nil
	}
	cfg := h.getConfig()
	if cfg == nil || len(cfg.Routing.Fallbacks) == 0 {
		return nil
	}

	byPool := make(map[string]UsagePoolStats, len(cfg.Routing.Fallbacks))
	for poolName, chain := range cfg.Routing.Fallbacks {
		normalizedPool := normalizeUsageModelID(poolName, &cfg.Routing)
		if normalizedPool == "" {
			continue
		}
		models := make([]string, 0, len(chain)+1)
		models = append(models, normalizedPool)
		for _, model := range chain {
			normalizedModel := normalizeUsageModelID(model, &cfg.Routing)
			if normalizedModel != "" && !slices.Contains(models, normalizedModel) {
				models = append(models, normalizedModel)
			}
		}

		var aggregate UsagePoolStats
		aggregate.Models = models
		for _, model := range models {
			stats, ok := byModel[model]
			if !ok {
				continue
			}
			aggregate.Requests += stats.Requests
			aggregate.Success += stats.Success
			aggregate.Failure += stats.Failure
			aggregate.Tokens.Total += stats.Tokens.Total
			aggregate.Tokens.Input += stats.Tokens.Input
			aggregate.Tokens.Output += stats.Tokens.Output
			aggregate.Tokens.Reasoning += stats.Tokens.Reasoning
		}
		if aggregate.Requests > 0 {
			byPool[normalizedPool] = aggregate
		}
	}
	if len(byPool) == 0 {
		return nil
	}
	return byPool
}

func normalizeUsageModelID(model string, routing *config.RoutingConfig) string {
	normalized := util.NormalizeIncomingModelID(model)
	if routing != nil {
		normalized = routing.ResolveModelAlias(normalized)
	}
	return normalized
}
