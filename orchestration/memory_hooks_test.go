package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- MemoryEnrichmentHook Tests ---

func TestMemoryEnrichmentHook_InjectsEpisodicContext(t *testing.T) {
	episodic := &core.MockEpisodicMemory{
		QueryEntityHistFn: func(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
			return []core.AgentEvent{
				{
					AgentName:   "event-driven-agent",
					AgentDomain: "infrastructure",
					ActionType:  "pod_restart",
					EntityType:  "pod",
					EntityID:    "pod-7x9k2",
					Summary:     "Restarted pod due to OOMKilled",
					Outcome:     "success",
					Timestamp:   time.Now().Add(-5 * time.Minute),
				},
			}, nil
		},
	}

	hook, _ := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "infrastructure")

	pctx := &core.PipelineContext{
		Request:     "Pod pod-7x9k2 is slow",
		Metadata:    map[string]interface{}{"entity_type": "pod", "entity_id": "pod-7x9k2"},
		Enrichments: make(map[string]interface{}),
	}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit, "memory hook should never short-circuit")

	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok, "enrichments should contain rag_context")
	assert.Contains(t, ragCtx, "event-driven-agent")
	assert.Contains(t, ragCtx, "Restarted pod due to OOMKilled") // Summary takes precedence over action_type in formatted output
	assert.Contains(t, ragCtx, "success")
	assert.Equal(t, 1, episodic.QueryEntityHistCt)
}

func TestMemoryEnrichmentHook_InjectsInvestigationContext(t *testing.T) {
	episodic := &core.MockEpisodicMemory{} // No events
	coordinator := &core.MockInvestigationCoordinator{
		GetActiveFn: func(ctx context.Context) (map[string]string, error) {
			return map[string]string{
				"pod-7x9k2": "other-agent",
			}, nil
		},
	}

	hook, _ := NewMemoryEnrichmentHook(episodic, coordinator, "test-agent", "infrastructure")

	pctx := &core.PipelineContext{
		Request:     "Check pod-7x9k2",
		Metadata:    map[string]interface{}{"entity_type": "pod", "entity_id": "pod-7x9k2"},
		Enrichments: make(map[string]interface{}),
	}

	hook.BeforePlanning(context.Background(), pctx)

	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "other-agent")
	assert.Contains(t, ragCtx, "investigated by")
}

func TestMemoryEnrichmentHook_GracefulDegradation(t *testing.T) {
	// Episodic memory returns error — should not block pipeline
	episodic := &core.MockEpisodicMemory{
		QueryEntityHistFn: func(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
			return nil, assert.AnError
		},
	}

	hook, _ := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "infrastructure")

	pctx := &core.PipelineContext{
		Request:     "Check pod-7x9k2",
		Metadata:    map[string]interface{}{"entity_type": "pod", "entity_id": "pod-7x9k2"},
		Enrichments: make(map[string]interface{}),
	}

	shortCircuit, err := hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)
	assert.Nil(t, shortCircuit)
	// No enrichment injected — that's fine, fail-open
}

func TestMemoryEnrichmentHook_NoEntities(t *testing.T) {
	episodic := &core.MockEpisodicMemory{}
	hook, _ := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "infrastructure")

	pctx := &core.PipelineContext{
		Request:     "What is the meaning of life?", // No entities
		Enrichments: make(map[string]interface{}),
	}

	hook.BeforePlanning(context.Background(), pctx)

	_, hasRAG := pctx.Enrichments[core.EnrichmentRAGContext]
	assert.False(t, hasRAG, "no entities = no memory context injected")
	assert.Equal(t, 0, episodic.QueryEntityHistCt, "should not query if no entities found")
}

func TestMemoryEnrichmentHook_AppendsToExistingRAGContext(t *testing.T) {
	episodic := &core.MockEpisodicMemory{
		QueryEntityHistFn: func(ctx context.Context, callerDomain string, entityType, entityID string, since time.Time) ([]core.AgentEvent, error) {
			return []core.AgentEvent{{
				AgentName: "agent-a", AgentDomain: "infra", ActionType: "test",
				EntityType: "pod", EntityID: "pod-1", Outcome: "success",
				Timestamp: time.Now(),
			}}, nil
		},
	}

	hook, _ := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "infra")

	pctx := &core.PipelineContext{
		Request:  "Check pod-1",
		Metadata: map[string]interface{}{"entity_type": "pod", "entity_id": "pod-1"},
		Enrichments: map[string]interface{}{
			core.EnrichmentRAGContext: "existing RAG context from another hook",
		},
	}

	hook.BeforePlanning(context.Background(), pctx)

	ragCtx := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.Contains(t, ragCtx, "existing RAG context from another hook")
	assert.Contains(t, ragCtx, "agent-a")
}

// --- MemoryRecordHook Tests ---

func TestMemoryRecordHook_RecordsEvents(t *testing.T) {
	var recorded []core.AgentEvent
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recorded = append(recorded, event)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "prometheus-query-tool",
				Capability:  "query_metrics",
				Instruction: "Query metrics for pod pod-abc12",
				Success:     true,
				Parameters:  map[string]interface{}{"entity_type": "pod", "entity_id": "pod-abc12"},
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	require.GreaterOrEqual(t, len(recorded), 1)
	assert.Equal(t, "query_metrics", recorded[0].ActionType)
	assert.Equal(t, "infrastructure", recorded[0].AgentDomain)
	assert.Equal(t, "test-agent", recorded[0].AgentName)
}

func TestMemoryRecordHook_ReleasesInvestigationClaims(t *testing.T) {
	episodic := &core.MockEpisodicMemory{}
	var released []string
	coordinator := &core.MockInvestigationCoordinator{
		ReleaseFn: func(ctx context.Context, agentName, entityID string) error {
			released = append(released, entityID)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, coordinator, "test-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "devops-tool",
				Capability:  "pod_restart",
				Instruction: "Restart pod pod-xyz99",
				Success:     true,
				Parameters:  map[string]interface{}{"entity_type": "pod", "entity_id": "pod-xyz99"},
			},
		},
	}

	hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.Contains(t, released, "pod-xyz99")
}

func TestMemoryRecordHook_SkipsOnlySkippedSteps(t *testing.T) {
	episodic := &core.MockEpisodicMemory{}
	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-1", Success: false, AgentName: "tool-a", Capability: "action_a",
				Instruction: "Do something on pod pod-fail1",
				Parameters:  map[string]interface{}{"entity_type": "pod", "entity_id": "pod-fail1"}},
			{StepID: "step-2", Success: true, Skipped: true, AgentName: "tool-b", Capability: "action_b"},
		},
	}

	hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	// Failed steps ARE recorded (other agents need to know about failures).
	// Skipped steps are NOT recorded (no execution occurred).
	assert.GreaterOrEqual(t, episodic.RecordEventCt, 1, "should record failed steps")
}

func TestMemoryRecordHook_DelegationDetection(t *testing.T) {
	var recorded []core.AgentEvent
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recorded = append(recorded, event)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, nil, "parent-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "child-orchestrator",
				Capability:  "devops_query",
				Instruction: "Investigate pod pod-orch1",
				Success:     true,
				Parameters:  map[string]interface{}{"entity_type": "pod", "entity_id": "pod-orch1"},
				Metadata: map[string]interface{}{
					"capability_type": string(core.CapabilityOrchestrator),
				},
			},
		},
	}

	hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	require.GreaterOrEqual(t, len(recorded), 1)
	assert.Equal(t, "delegated", recorded[0].ActionType, "orchestrator steps should record as 'delegated'")
}

func TestMemoryRecordHook_GracefulOnNilResult(t *testing.T) {
	episodic := &core.MockEpisodicMemory{}
	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, nil)
	assert.NoError(t, err)
	assert.Equal(t, 0, episodic.RecordEventCt)
}

// --- EventSummarizer Integration Tests ---

func TestMemoryRecordHook_UsesLLMSummaryWhenAvailable(t *testing.T) {
	var recordedSummary string
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recordedSummary = event.Summary
			return nil
		},
	}
	summarizer := &core.MockEventSummarizer{
		SummarizeStepsFn: func(ctx context.Context, steps []core.StepSummaryInput) (map[string]core.StepSummary, error) {
			return map[string]core.StepSummary{
				"step-1": {Summary: "Created JIRA ticket DEVOPS-43 via jira-tool"},
			}, nil
		},
	}
	hook, err := NewMemoryRecordHook(episodic, nil, "test-agent", "test-domain",
		WithEventSummarizer(summarizer),
	)
	require.NoError(t, err)

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "jira-tool",
				Capability:  "create_issue",
				Instruction: "Create a ticket for pod/payment-service-pod-7x failure",
				Parameters:  map[string]interface{}{"namespace": "default", "pod_name": "payment-service-pod-7x"},
				Response:    `{"key":"DEVOPS-43"}`,
				Success:     true,
			},
		},
	}

	err = hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	assert.Equal(t, 1, summarizer.SummarizeStepsCt, "should call summarizer")
	assert.GreaterOrEqual(t, episodic.RecordEventCt, 1, "should record at least one event")
	assert.Equal(t, "Created JIRA ticket DEVOPS-43 via jira-tool", recordedSummary)
}

func TestMemoryRecordHook_FallsBackOnSummarizerError(t *testing.T) {
	var recordedSummary string
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recordedSummary = event.Summary
			return nil
		},
	}
	summarizer := &core.MockEventSummarizer{
		SummarizeStepsFn: func(ctx context.Context, steps []core.StepSummaryInput) (map[string]core.StepSummary, error) {
			return nil, fmt.Errorf("LLM unavailable")
		},
	}
	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "test-domain",
		WithEventSummarizer(summarizer),
	)

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "jira-tool",
				Capability:  "create_issue",
				Instruction: "Create a ticket for pod/payment-service-pod-7x failure",
				Parameters:  map[string]interface{}{"namespace": "default", "pod_name": "payment-service-pod-7x"},
				Response:    `{"key":"DEVOPS-43"}`,
				Success:     true,
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	assert.Equal(t, 1, summarizer.SummarizeStepsCt)
	assert.NotEmpty(t, recordedSummary, "should use heuristic fallback summary")
}

func TestMemoryRecordHook_NilSummarizer_UsesHeuristic(t *testing.T) {
	var recordedSummary string
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recordedSummary = event.Summary
			return nil
		},
	}
	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "test-domain")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "jira-tool",
				Capability:  "create_issue",
				Instruction: "Create ticket for pod/payment-service-pod-7x",
				Parameters:  map[string]interface{}{"namespace": "default", "pod_name": "payment-service-pod-7x"},
				Response:    `{"key":"DEVOPS-43"}`,
				Success:     true,
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	assert.NotEmpty(t, recordedSummary, "should use heuristic summary")
}

func TestWithEventSummarizer_AcceptsNil(t *testing.T) {
	hook, err := NewMemoryRecordHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithEventSummarizer(nil),
	)
	require.NoError(t, err)
	assert.Nil(t, hook.summarizer)
}

// --- DefaultImportanceScorer Tests ---

func TestDefaultImportanceScorer(t *testing.T) {
	// Alerts/incidents — high importance (8.0)
	assert.Equal(t, 8.0, DefaultImportanceScorer("alert_fired", "success"))
	assert.Equal(t, 10.0, DefaultImportanceScorer("alert_fired", "failure")) // 8 + 2, capped at 10
	assert.Equal(t, 8.0, DefaultImportanceScorer("incident_created", "success"))

	// Mutations — high importance (7.0)
	assert.Equal(t, 7.0, DefaultImportanceScorer("create_issue", "success"))
	assert.Equal(t, 7.0, DefaultImportanceScorer("restart_pod", "success"))
	assert.Equal(t, 7.0, DefaultImportanceScorer("delete_record", "success"))
	assert.Equal(t, 7.0, DefaultImportanceScorer("scale_deployment", "success"))
	assert.Equal(t, 9.0, DefaultImportanceScorer("create_issue", "failure")) // 7 + 2

	// External communication — medium (5.0)
	assert.Equal(t, 5.0, DefaultImportanceScorer("send_message", "success"))
	assert.Equal(t, 5.0, DefaultImportanceScorer("notify_channel", "success"))

	// Read-only — low importance (3.0)
	assert.Equal(t, 3.0, DefaultImportanceScorer("query_metrics", "success"))
	assert.Equal(t, 3.0, DefaultImportanceScorer("get_pod_logs", "success"))
	assert.Equal(t, 3.0, DefaultImportanceScorer("describe_resource", "success"))
	assert.Equal(t, 3.0, DefaultImportanceScorer("list_pods", "success"))
	assert.Equal(t, 5.0, DefaultImportanceScorer("query_metrics", "failure")) // 3 + 2

	// Delegation — medium (5.0)
	assert.Equal(t, 5.0, DefaultImportanceScorer("delegated", "success"))

	// Unknown — medium default (5.0)
	assert.Equal(t, 5.0, DefaultImportanceScorer("unknown_action", "success"))
	assert.Equal(t, 7.0, DefaultImportanceScorer("unknown_action", "failure")) // 5 + 2

	// Case insensitive
	assert.Equal(t, 7.0, DefaultImportanceScorer("Create_Ticket", "success"))
	assert.Equal(t, 3.0, DefaultImportanceScorer("QUERY_data", "success"))
}

// --- Multi-Entity Event Tests (Phase 7) ---

func TestMemoryRecordHook_SingleEventPerStep_MultiEntity(t *testing.T) {
	var recorded []core.AgentEvent
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recorded = append(recorded, event)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	// Step with instruction that matches multiple entities
	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "devops-tool",
				Capability:  "rollout_restart",
				Instruction: "Restart deployment for pod/my-pod in service/my-service",
				Parameters:  map[string]interface{}{"namespace": "default", "deployment_name": "my-service", "pod_name": "my-pod"},
				Success:     true,
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)

	// Should record exactly 1 event, not N (one per entity)
	assert.Equal(t, 1, len(recorded), "should record exactly 1 event per step, not 1 per entity")
}

func TestMemoryRecordHook_EntitiesFieldPopulated(t *testing.T) {
	var recorded []core.AgentEvent
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recorded = append(recorded, event)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "devops-tool",
				Capability:  "describe_resource",
				Instruction: "Describe pod/payment-pod-7x9k2 in namespace truvag3-examples",
				Parameters:  map[string]interface{}{"entity_type": "pod", "entity_id": "payment-pod-7x9k2", "namespace": "truvag3-examples"},
				Success:     true,
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	require.Equal(t, 1, len(recorded))

	event := recorded[0]
	// Primary entity should be set
	assert.NotEmpty(t, event.EntityType)
	assert.NotEmpty(t, event.EntityID)
	// Entities slice should be populated
	assert.NotEmpty(t, event.Entities, "Entities should be populated")
	// Primary entity should match first entity in Entities
	assert.Equal(t, event.EntityType, event.Entities[0].Type)
	assert.Equal(t, event.EntityID, event.Entities[0].ID)
}

func TestMemoryRecordHook_NoEntities_NoEvent(t *testing.T) {
	var recorded []core.AgentEvent
	episodic := &core.MockEpisodicMemory{
		RecordEventFn: func(ctx context.Context, event core.AgentEvent) error {
			recorded = append(recorded, event)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, nil, "test-agent", "infrastructure")

	// Step with no recognizable entities
	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "some-tool",
				Capability:  "do_something",
				Instruction: "Perform a generic action",
				Success:     true,
			},
		},
	}

	err := hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)
	assert.NoError(t, err)
	// Entity-less steps now record a domain-level event (no entity index).
	// See LLM_NATIVE_ENTITY_EXTRACTION_PROPOSAL.md §9.8.6 — the else branch
	// ensures events are never silently dropped when the extractor returns nothing.
	assert.Equal(t, 1, len(recorded), "entity-less step should still record a domain-level event")
	if len(recorded) > 0 {
		assert.Empty(t, recorded[0].EntityType, "entity-less event should have empty EntityType")
		assert.Empty(t, recorded[0].EntityID, "entity-less event should have empty EntityID")
	}
}

func TestMemoryRecordHook_MultiEntity_AllEntitiesInInvestigationRelease(t *testing.T) {
	episodic := &core.MockEpisodicMemory{}
	var released []string
	coordinator := &core.MockInvestigationCoordinator{
		ReleaseFn: func(ctx context.Context, agentName, entityID string) error {
			released = append(released, entityID)
			return nil
		},
	}

	hook, _ := NewMemoryRecordHook(episodic, coordinator, "test-agent", "infrastructure")

	result := &ExecutionResult{
		Steps: []StepResult{
			{
				StepID:      "step-1",
				AgentName:   "devops-tool",
				Capability:  "rollout_restart",
				Instruction: "Restart pod/my-pod in service/my-svc namespace/default",
				Parameters: map[string]interface{}{
					"pod_name":    "my-pod",
					"namespace":   "default",
					"entity_type": "pod",
					"entity_id":   "my-pod",
				},
				Success: true,
			},
		},
	}

	hook.AfterExecution(context.Background(), &core.PipelineContext{}, result)

	// Entity came from explicit metadata; investigation should be released
	assert.GreaterOrEqual(t, len(released), 1, "should release investigation for entities from metadata")
}

func TestToEntityRefs(t *testing.T) {
	entities := []Entity{
		{Type: "pod", ID: "my-pod"},
		{Type: "service", ID: "my-svc"},
		{Type: "deployment", ID: "my-deploy"},
	}

	refs := toEntityRefs(entities)
	require.Len(t, refs, 3)
	assert.Equal(t, "pod", refs[0].Type)
	assert.Equal(t, "my-pod", refs[0].ID)
	assert.Equal(t, "service", refs[1].Type)
	assert.Equal(t, "my-svc", refs[1].ID)
	assert.Equal(t, "deployment", refs[2].Type)
	assert.Equal(t, "my-deploy", refs[2].ID)
}

func TestToEntityRefs_Empty(t *testing.T) {
	refs := toEntityRefs(nil)
	assert.Len(t, refs, 0)

	refs2 := toEntityRefs([]Entity{})
	assert.Len(t, refs2, 0)
}

// --- Digest Caching Tests (§6.7) ---

// makeEvents creates n test events with sequential timestamps starting from base.
func makeEvents(n int, base time.Time) []core.AgentEvent {
	events := make([]core.AgentEvent, n)
	for i := 0; i < n; i++ {
		events[i] = core.AgentEvent{
			EventID:     fmt.Sprintf("evt-%d", i),
			AgentName:   "test-agent",
			AgentDomain: "test-domain",
			ActionType:  "check",
			EntityType:  "pod",
			EntityID:    fmt.Sprintf("pod-%d", i),
			Outcome:     "success",
			Summary:     fmt.Sprintf("Event %d", i),
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
		}
	}
	return events
}

func TestDigestCache_CacheMiss_FullCompact(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	events := makeEvents(30, baseTime)

	var compactedWith []core.AgentEvent
	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, evts []core.AgentEvent, maxTokens int) (string, error) {
			compactedWith = evts
			return "compacted-digest-v1", nil
		},
	}

	digestCache := &core.MockDigestCache{} // Returns nil on Get (cache miss)

	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return events, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithCompactionRawLimit(200),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check pod status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// CompactEvents should be called (not UpdateDigest)
	assert.Equal(t, 1, compactor.CompactEventsCt, "should call CompactEvents on cache miss")
	assert.Equal(t, len(events), len(compactedWith), "should compact all events")

	// Cache should be populated (SetDigest called)
	assert.Equal(t, 1, digestCache.SetCt, "should store digest in cache")

	// RAG context should contain the digest
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "compacted-digest-v1")
}

func TestDigestCache_CacheHit_NoNewEvents(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	lastEventTS := baseTime.Add(29 * time.Minute) // Timestamp of newest cached event

	cached := cachedDigestData{
		Content:     "cached-digest-content",
		LastEventTS: lastEventTS,
		GeneratedAt: time.Now().Add(-2 * time.Minute),
	}
	cachedJSON, _ := json.Marshal(cached)

	callCount := 0
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			callCount++
			if callCount == 1 {
				// First call: initial query (returns events for the baseline)
				return makeEvents(30, baseTime), nil
			}
			// Second call: query for events after lastEventTS — none
			return nil, nil
		},
	}

	compactor := &core.MockActivityCompactor{}

	digestCache := &core.MockDigestCache{
		GetFn: func(ctx context.Context, domain string) ([]byte, error) {
			return cachedJSON, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// No compaction calls — cached digest reused
	assert.Equal(t, 0, compactor.CompactEventsCt, "should skip compaction when cache hit + no new events")

	// RAG context should contain cached digest
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "cached-digest-content")
}

func TestDigestCache_CacheHit_FewNewEvents_IncrementalUpdate(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	lastEventTS := baseTime.Add(29 * time.Minute)
	newEvents := makeEvents(5, lastEventTS.Add(time.Minute)) // 5 new events after cache

	cached := cachedDigestData{
		Content:     "old-digest",
		LastEventTS: lastEventTS,
		GeneratedAt: time.Now().Add(-2 * time.Minute),
	}
	cachedJSON, _ := json.Marshal(cached)

	callCount := 0
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			callCount++
			if callCount == 1 {
				return makeEvents(30, baseTime), nil
			}
			// Second call: query for events after lastEventTS — returns 5 new
			return newEvents, nil
		},
	}

	var receivedPrevDigest string
	var receivedNewEvts []core.AgentEvent
	compactor := &core.MockActivityCompactor{
		UpdateDigestFn: func(ctx context.Context, previousDigest string, evts []core.AgentEvent, maxTokens int) (string, error) {
			receivedPrevDigest = previousDigest
			receivedNewEvts = evts
			return "incrementally-updated-digest", nil
		},
	}

	digestCache := &core.MockDigestCache{
		GetFn: func(ctx context.Context, domain string) ([]byte, error) {
			return cachedJSON, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithIncrementalThreshold(20),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// UpdateDigest should have been called with the cached digest and new events
	assert.Equal(t, 1, compactor.UpdateDigestCt, "should call UpdateDigest for incremental update")
	assert.Equal(t, "old-digest", receivedPrevDigest, "should pass cached digest as previousDigest")
	assert.Equal(t, len(newEvents), len(receivedNewEvts), "should pass new events to UpdateDigest")

	// CompactEvents should NOT have been called (incremental path, not full)
	assert.Equal(t, 0, compactor.CompactEventsCt, "should use UpdateDigest, not CompactEvents")

	// Cache should be updated
	assert.GreaterOrEqual(t, digestCache.SetCt, 1, "should store updated digest in cache")

	// RAG context should contain the updated digest
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "incrementally-updated-digest")
}

func TestDigestCache_CacheHit_ManyNewEvents_FullRecompact(t *testing.T) {
	baseTime := time.Now().Add(-2 * time.Hour)
	lastEventTS := baseTime.Add(29 * time.Minute)
	// 50 new events (above threshold of 20)
	manyNewEvents := makeEvents(50, lastEventTS.Add(time.Minute))

	cached := cachedDigestData{
		Content:     "stale-digest",
		LastEventTS: lastEventTS,
		GeneratedAt: time.Now().Add(-10 * time.Minute),
	}
	cachedJSON, _ := json.Marshal(cached)

	callCount := 0
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			callCount++
			if callCount == 1 {
				return makeEvents(30, baseTime), nil
			}
			if callCount == 2 {
				// Second call: query for events after lastEventTS — 50 new (above threshold)
				return manyNewEvents, nil
			}
			// Third call: full re-fetch for burst recompaction
			allEvents := append(makeEvents(30, baseTime), manyNewEvents...)
			return allEvents, nil
		},
	}

	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, evts []core.AgentEvent, maxTokens int) (string, error) {
			return "fully-recompacted-digest", nil
		},
	}

	digestCache := &core.MockDigestCache{
		GetFn: func(ctx context.Context, domain string) ([]byte, error) {
			return cachedJSON, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithIncrementalThreshold(20), // 50 > 20 → burst
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// CompactEvents should be called (full recompact, not UpdateDigest)
	assert.GreaterOrEqual(t, compactor.CompactEventsCt, 1, "should call CompactEvents for burst recompaction")

	// RAG context should contain the recompacted digest
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "fully-recompacted-digest")
}

func TestDigestCache_CompactError_CacheMiss_FallsOpenToRawEvents(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	events := makeEvents(10, baseTime)

	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, evts []core.AgentEvent, maxTokens int) (string, error) {
			return "", fmt.Errorf("LLM unavailable")
		},
	}

	digestCache := &core.MockDigestCache{} // cache miss

	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return events, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithEnrichmentRecentEventsLimit(10),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// Should fall open — RAG context with raw events
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok, "should inject raw event context on compaction failure")
	assert.Contains(t, ragCtx, "Recent activity in this domain:")
}

func TestDigestCache_IncrementalError_FallsBackToStaleCache(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	lastEventTS := baseTime.Add(29 * time.Minute)
	newEvents := makeEvents(5, lastEventTS.Add(time.Minute))

	cached := cachedDigestData{
		Content:     "stale-but-usable-digest",
		LastEventTS: lastEventTS,
		GeneratedAt: time.Now().Add(-3 * time.Minute),
	}
	cachedJSON, _ := json.Marshal(cached)

	callCount := 0
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			callCount++
			if callCount == 1 {
				return makeEvents(30, baseTime), nil
			}
			return newEvents, nil
		},
	}

	compactor := &core.MockActivityCompactor{
		UpdateDigestFn: func(ctx context.Context, previousDigest string, evts []core.AgentEvent, maxTokens int) (string, error) {
			return "", fmt.Errorf("LLM timeout")
		},
	}

	digestCache := &core.MockDigestCache{
		GetFn: func(ctx context.Context, domain string) ([]byte, error) {
			return cachedJSON, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithIncrementalThreshold(20),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	// Should fall back to stale cache
	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "stale-but-usable-digest", "should use stale cache on incremental failure")
}

func TestDigestCache_NilCache_FullCompactEveryTime(t *testing.T) {
	baseTime := time.Now().Add(-1 * time.Hour)
	events := makeEvents(20, baseTime)

	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, evts []core.AgentEvent, maxTokens int) (string, error) {
			return "no-cache-digest", nil
		},
	}

	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			return events, nil
		},
	}

	// No digestCache configured
	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	assert.Equal(t, 1, compactor.CompactEventsCt, "should compact every time without cache")

	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "no-cache-digest")
}

func TestDigestCache_BurstRecompactError_FallsBackToStaleCache(t *testing.T) {
	baseTime := time.Now().Add(-2 * time.Hour)
	lastEventTS := baseTime.Add(29 * time.Minute)
	manyNewEvents := makeEvents(50, lastEventTS.Add(time.Minute))

	cached := cachedDigestData{
		Content:     "stale-burst-digest",
		LastEventTS: lastEventTS,
		GeneratedAt: time.Now().Add(-10 * time.Minute),
	}
	cachedJSON, _ := json.Marshal(cached)

	callCount := 0
	episodic := &core.MockEpisodicMemory{
		QueryRecentEventsFn: func(ctx context.Context, domain string, since time.Time, limit int) ([]core.AgentEvent, error) {
			callCount++
			if callCount == 1 {
				return makeEvents(30, baseTime), nil
			}
			if callCount == 2 {
				return manyNewEvents, nil
			}
			return makeEvents(80, baseTime), nil
		},
	}

	compactor := &core.MockActivityCompactor{
		CompactEventsFn: func(ctx context.Context, evts []core.AgentEvent, maxTokens int) (string, error) {
			return "", fmt.Errorf("LLM capacity exceeded")
		},
	}

	digestCache := &core.MockDigestCache{
		GetFn: func(ctx context.Context, domain string) ([]byte, error) {
			return cachedJSON, nil
		},
	}

	hook, err := NewMemoryEnrichmentHook(episodic, nil, "test-agent", "test-domain",
		WithActivityCompactor(compactor),
		WithDigestCache(digestCache),
		WithIncrementalThreshold(20),
		WithCompactionRecentDetail(5),
	)
	require.NoError(t, err)

	pctx := &core.PipelineContext{
		Request:     "Check status",
		Enrichments: make(map[string]interface{}),
	}

	_, err = hook.BeforePlanning(context.Background(), pctx)
	assert.NoError(t, err)

	ragCtx, ok := pctx.Enrichments[core.EnrichmentRAGContext].(string)
	assert.True(t, ok)
	assert.Contains(t, ragCtx, "stale-burst-digest", "burst recompact failure should fall back to stale cache")
}

// --- Helper function tests ---

func TestNewestEventTS(t *testing.T) {
	t1 := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 20, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

	events := []core.AgentEvent{
		{Timestamp: t1},
		{Timestamp: t2},
		{Timestamp: t3},
	}

	assert.Equal(t, t2, newestEventTS(events))
}

func TestNewestEventTS_Empty(t *testing.T) {
	assert.True(t, newestEventTS(nil).IsZero())
	assert.True(t, newestEventTS([]core.AgentEvent{}).IsZero())
}

func TestNewestEventTS_SingleEvent(t *testing.T) {
	ts := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	events := []core.AgentEvent{{Timestamp: ts}}
	assert.Equal(t, ts, newestEventTS(events))
}

// --- WithDigestCache Option Tests ---

func TestWithDigestCacheTTL_RejectsNonPositive(t *testing.T) {
	_, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithDigestCacheTTL(0),
	)
	assert.Error(t, err)

	_, err = NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithDigestCacheTTL(-1*time.Minute),
	)
	assert.Error(t, err)
}

func TestWithIncrementalThreshold_RejectsNonPositive(t *testing.T) {
	_, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithIncrementalThreshold(0),
	)
	assert.Error(t, err)
}

func TestWithDigestCache_AcceptsNil(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithDigestCache(nil),
	)
	require.NoError(t, err)
	assert.Nil(t, hook.digestCache)
}

func TestWithDigestCacheTTL_SetsValue(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithDigestCacheTTL(10*time.Minute),
	)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Minute, hook.digestCacheTTL)
}

func TestWithIncrementalThreshold_SetsValue(t *testing.T) {
	hook, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithIncrementalThreshold(50),
	)
	require.NoError(t, err)
	assert.Equal(t, 50, hook.incrementalThreshold)
}

// --- ComponentAwareLogger Wrapping Tests (R.7 / R.11) ---

// Reuses mockComponentAwareLogger from plan_refinement_test.go (same package).

func TestWithEnrichmentLogger_WrapsComponentAwareLogger(t *testing.T) {
	logger := &mockComponentAwareLogger{}
	hook, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithEnrichmentLogger(logger),
	)
	require.NoError(t, err)

	// WithComponent was called — the existing mock stores it in .component
	assert.Equal(t, "framework/orchestration", logger.component)
	// The hook's logger should NOT be the original (it was replaced by WithComponent's return)
	assert.NotEqual(t, logger, hook.logger)
}

func TestWithRecordLogger_WrapsComponentAwareLogger(t *testing.T) {
	logger := &mockComponentAwareLogger{}
	hook, err := NewMemoryRecordHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithRecordLogger(logger),
	)
	require.NoError(t, err)

	assert.Equal(t, "framework/orchestration", logger.component)
	assert.NotEqual(t, logger, hook.logger)
}

func TestWithEnrichmentLogger_PlainLoggerPassedThrough(t *testing.T) {
	logger := &core.NoOpLogger{} // does NOT implement ComponentAwareLogger
	hook, err := NewMemoryEnrichmentHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithEnrichmentLogger(logger),
	)
	require.NoError(t, err)
	assert.Equal(t, logger, hook.logger, "plain logger should be used as-is")
}

func TestWithRecordLogger_PlainLoggerPassedThrough(t *testing.T) {
	logger := &core.NoOpLogger{}
	hook, err := NewMemoryRecordHook(
		&core.MockEpisodicMemory{}, nil, "test", "domain",
		WithRecordLogger(logger),
	)
	require.NoError(t, err)
	assert.Equal(t, logger, hook.logger, "plain logger should be used as-is")
}

// --- NoOpEntityExtractor Tests ---

func TestNoOpEntityExtractor_NilMetadata(t *testing.T) {
	e := NoOpEntityExtractor{}
	assert.Nil(t, e.ExtractEntities("any text", nil))
}

func TestNoOpEntityExtractor_MissingFields(t *testing.T) {
	e := NoOpEntityExtractor{}
	// Only entity_type, no entity_id
	assert.Nil(t, e.ExtractEntities("", map[string]interface{}{"entity_type": "pod"}))
	// Only entity_id, no entity_type
	assert.Nil(t, e.ExtractEntities("", map[string]interface{}{"entity_id": "foo"}))
	// Empty strings
	assert.Nil(t, e.ExtractEntities("", map[string]interface{}{"entity_type": "", "entity_id": ""}))
}

func TestNoOpEntityExtractor_FullMetadata(t *testing.T) {
	e := NoOpEntityExtractor{}
	entities := e.ExtractEntities("text is ignored", map[string]interface{}{
		"entity_type": "pod",
		"entity_id":   "foo-abc12",
	})
	require.Len(t, entities, 1)
	assert.Equal(t, "pod", entities[0].Type)
	assert.Equal(t, "foo-abc12", entities[0].ID)
}

func TestNoOpEntityExtractor_MultiEntityMetadata(t *testing.T) {
	e := NoOpEntityExtractor{}
	entities := e.ExtractEntities("", map[string]interface{}{
		"entities": []core.EntityRef{
			{Type: "pod", ID: "foo-abc12"},
			{Type: "service", ID: "payment-api"},
		},
	})
	require.Len(t, entities, 2)
	assert.Equal(t, "pod", entities[0].Type)
	assert.Equal(t, "service", entities[1].Type)
}

// --- LLMEntityExtractor Tests ---

func TestLLMEntityExtractor_ReadsLLMEntities(t *testing.T) {
	e := LLMEntityExtractor{}
	entities := e.ExtractEntities("text is ignored", map[string]interface{}{
		"llm_entities": []core.EntityRef{
			{Type: "pod", ID: "foo-abc12"},
			{Type: "service", ID: "payment-api"},
		},
	})
	require.Len(t, entities, 2)
	assert.Equal(t, "pod", entities[0].Type)
	assert.Equal(t, "foo-abc12", entities[0].ID)
	assert.Equal(t, "service", entities[1].Type)
	assert.Equal(t, "payment-api", entities[1].ID)
}

func TestLLMEntityExtractor_FallsThroughToExplicitMetadata(t *testing.T) {
	e := LLMEntityExtractor{}
	// No llm_entities, but explicit fields present
	entities := e.ExtractEntities("", map[string]interface{}{
		"entity_type": "pod",
		"entity_id":   "foo-abc12",
	})
	require.Len(t, entities, 1)
	assert.Equal(t, "pod", entities[0].Type)
}

func TestLLMEntityExtractor_NilMetadata(t *testing.T) {
	e := LLMEntityExtractor{}
	assert.Nil(t, e.ExtractEntities("any text", nil))
}

func TestLLMEntityExtractor_FiltersEmptyTypeOrID(t *testing.T) {
	e := LLMEntityExtractor{}
	entities := e.ExtractEntities("", map[string]interface{}{
		"llm_entities": []core.EntityRef{
			{Type: "", ID: "foo"},
			{Type: "pod", ID: ""},
			{Type: "pod", ID: "real-1"},
		},
	})
	require.Len(t, entities, 1)
	assert.Equal(t, "real-1", entities[0].ID)
}

func TestLLMEntityExtractor_DedupsDuplicates(t *testing.T) {
	e := LLMEntityExtractor{}
	entities := e.ExtractEntities("", map[string]interface{}{
		"llm_entities": []core.EntityRef{
			{Type: "pod", ID: "foo"},
			{Type: "pod", ID: "foo"},
			{Type: "pod", ID: "bar"},
		},
	})
	require.Len(t, entities, 2)
}

func TestLLMEntityExtractor_FallsThroughToMultiEntityMetadata(t *testing.T) {
	e := LLMEntityExtractor{}
	// No llm_entities, but multi-entity structured path present
	entities := e.ExtractEntities("", map[string]interface{}{
		"entities": []core.EntityRef{
			{Type: "pod", ID: "foo"},
			{Type: "service", ID: "bar"},
		},
	})
	require.Len(t, entities, 2)
	assert.Equal(t, "pod", entities[0].Type)
	assert.Equal(t, "service", entities[1].Type)
}

// --- extractorTypeLabel Tests ---

func TestExtractorTypeLabel(t *testing.T) {
	cases := []struct {
		name     string
		in       EntityExtractor
		expected string
	}{
		{"nil", nil, "none"},
		{"NoOp value", NoOpEntityExtractor{}, "noop"},
		{"NoOp pointer", &NoOpEntityExtractor{}, "noop"},
		{"LLM value", LLMEntityExtractor{}, "llm"},
		{"LLM pointer", &LLMEntityExtractor{}, "llm"},
		{"custom", testEntityExtractor{}, "custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, extractorTypeLabel(tc.in))
		})
	}
}

// testEntityExtractor is a minimal local test impl for the "custom" case.
type testEntityExtractor struct{}

func (testEntityExtractor) ExtractEntities(_ string, _ map[string]interface{}) []Entity {
	return nil
}
