package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

const portableTargetService = "portable-target-agent"

// deterministicTargetOrchestrator gives the standard scheduled endpoint a
// stable, LLM-free implementation suitable for local portability tests.
type deterministicTargetOrchestrator struct {
	startedAt time.Time
	requests  atomic.Int64
	successes atomic.Int64
	failFirst int
	attempts  map[string]int
	mu        sync.Mutex
}

func newDeterministicTargetOrchestrator(failFirst ...int) *deterministicTargetOrchestrator {
	failures := 0
	if len(failFirst) > 0 {
		failures = failFirst[0]
	}
	return &deterministicTargetOrchestrator{
		startedAt: time.Now().UTC(),
		failFirst: failures,
		attempts:  make(map[string]int),
	}
}

func (target *deterministicTargetOrchestrator) ProcessRequest(
	_ context.Context,
	request string,
	metadata map[string]interface{},
) (*orchestration.OrchestratorResponse, error) {
	target.requests.Add(1)
	scheduleID := metadataString(metadata, "schedule_id")
	taskID := metadataString(metadata, "task_id")
	request = strings.TrimSpace(request)
	target.mu.Lock()
	target.attempts[taskID]++
	attempt := target.attempts[taskID]
	target.mu.Unlock()
	if attempt <= target.failFirst {
		return nil, fmt.Errorf("portable target: injected transient failure %d of %d", attempt, target.failFirst)
	}
	target.successes.Add(1)
	return &orchestration.OrchestratorResponse{
		RequestID:       taskID,
		OriginalRequest: request,
		Response: fmt.Sprintf(
			"portable target completed schedule %s task %s: %s",
			scheduleID,
			taskID,
			request,
		),
		RoutingMode:    orchestration.ModeWorkflow,
		AgentsInvolved: []string{portableTargetService},
		Metadata: map[string]interface{}{
			"schedule_id": scheduleID,
			"task_id":     taskID,
			"instruction": request,
			"attempt":     attempt,
		},
		Confidence: 1,
	}, nil
}

func (*deterministicTargetOrchestrator) ExecutePlan(
	context.Context,
	*orchestration.RoutingPlan,
) (*orchestration.ExecutionResult, error) {
	return nil, fmt.Errorf("portable target: ExecutePlan is not supported")
}

func (*deterministicTargetOrchestrator) ExecutePlanWithSynthesis(
	context.Context,
	*orchestration.RoutingPlan,
	string,
) (*orchestration.OrchestratorResponse, error) {
	return nil, fmt.Errorf("portable target: ExecutePlanWithSynthesis is not supported")
}

func (*deterministicTargetOrchestrator) GetExecutionHistory() []orchestration.ExecutionRecord {
	return nil
}

func (target *deterministicTargetOrchestrator) GetMetrics() orchestration.OrchestratorMetrics {
	total := target.requests.Load()
	return orchestration.OrchestratorMetrics{
		TotalRequests:      total,
		SuccessfulRequests: target.successes.Load(),
		LastRequestTime:    target.startedAt,
		UptimeSeconds:      int64(time.Since(target.startedAt).Seconds()),
	}
}

func runTargetAgent(ctx context.Context, config Config, port int) error {
	if strings.TrimSpace(config.RedisURL) == "" {
		return fmt.Errorf("target agent: REDIS_URL is required")
	}
	agent, framework, err := newAgentFramework(portableTargetService, config, port, true)
	if err != nil {
		return err
	}
	failFirst, err := strconv.Atoi(envOrDefault("PORTABILITY_TARGET_FAIL_FIRST", "0"))
	if err != nil || failFirst < 0 || failFirst > 2 {
		return fmt.Errorf("target agent: PORTABILITY_TARGET_FAIL_FIRST must be 0, 1, or 2")
	}
	target := newDeterministicTargetOrchestrator(failFirst)
	if err := orchestration.RegisterScheduledEndpoint(agent, func() orchestration.Orchestrator { return target }); err != nil {
		return fmt.Errorf("register deterministic scheduled endpoint: %w", err)
	}
	agent.Logger.Info("portable target agent starting", map[string]interface{}{"port": port})
	return framework.Run(ctx)
}

func metadataString(metadata map[string]interface{}, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

var _ orchestration.Orchestrator = (*deterministicTargetOrchestrator)(nil)
