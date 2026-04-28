package orchestration

import "github.com/truvaagents/truva-g3/telemetry"

func emitConversationHistoryMetrics(
	agentName string,
	result HistoryPreparationResult,
	outcome string,
	compactionOutcome string,
	cacheLen int,
) {
	telemetry.Counter("conversation_history.requests.total",
		"module", telemetry.ModuleOrchestration,
		"agent", agentName,
		"path", result.Path,
		"outcome", outcome,
	)

	for i := 0; i < result.TurnsDropped; i++ {
		telemetry.Counter("conversation_history.turns_dropped.total",
			"module", telemetry.ModuleOrchestration,
			"agent", agentName,
		)
	}

	if compactionOutcome != "" {
		telemetry.Counter("conversation_history.compactions.total",
			"module", telemetry.ModuleOrchestration,
			"agent", agentName,
			"outcome", compactionOutcome,
		)
	}

	if compactionOutcome != "" && result.CompactionDurationMs > 0 {
		telemetry.Histogram("conversation_history.compaction.duration_ms", float64(result.CompactionDurationMs),
			"module", telemetry.ModuleOrchestration,
			"agent", agentName,
			"outcome", compactionOutcome,
		)
	}

	telemetry.Histogram("conversation_history.estimated_tokens", float64(result.EstimatedTokensPre),
		"module", telemetry.ModuleOrchestration,
		"agent", agentName,
		"stage", "pre",
	)
	telemetry.Histogram("conversation_history.estimated_tokens", float64(result.EstimatedTokensPost),
		"module", telemetry.ModuleOrchestration,
		"agent", agentName,
		"stage", "post",
	)

	telemetry.Gauge("conversation_history.summary_cache.size", float64(cacheLen),
		"module", telemetry.ModuleOrchestration,
		"agent", agentName,
	)
}
