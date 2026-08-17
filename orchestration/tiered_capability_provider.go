package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// errNoToolsSelected is the sentinel error returned by parseToolSelection when
// the LLM response parses as a well-formed but empty JSON array. Using a
// sentinel (rather than a string comparison on fmt.Errorf) makes the ORCH-018
// Layer 3 defensive recovery robust to error wrapping in future refactors:
// callers use errors.Is(err, errNoToolsSelected) to detect this specific case.
var errNoToolsSelected = errors.New("no tools selected")

// TieredCapabilityProvider implements a two-phase capability resolution strategy
// that significantly reduces LLM token usage for large tool deployments.
//
// Research basis:
// - RAG-MCP (May 2025): 74.8% token reduction, 62.1% faster, 3.2x accuracy
// - Less is More (Nov 2024): Accuracy degrades beyond ~20 tools
// - Guided-Structured Templates (Sept 2025): 3-12% improvement with structured prompts
//
// Phase 1 (Tier 1): Send lightweight summaries to LLM for tool selection
// Phase 2 (Tier 2): Retrieve full schemas only for selected tools
//
// This approach reduces token usage by 50-75% for deployments with 20+ tools.
type TieredCapabilityProvider struct {
	catalog           *AgentCatalog
	aiClient          core.AIClient
	aiOptionsOverride *AIOptionsOverride

	// MinToolsForTiering is the minimum tool count to trigger tiered resolution.
	// Below this threshold, sends all tools directly (simpler, one LLM call).
	// Research shows degradation starts at ~20 tools (Less is More, Nov 2024)
	// Default: 20
	MinToolsForTiering int

	// SelectionMaxTokens is the maximum output tokens for tiered selection LLM calls.
	// Default: 2000
	SelectionMaxTokens int

	// Retry configuration for empty responses and parse failures.
	// Mirrors PlanParseRetryEnabled pattern for consistency.
	retryEnabled bool // Whether to retry on empty response or parse failure
	maxRetries   int  // Max additional attempts (2 means up to 3 total)

	// customInstructions are domain-specific workflow rules from PromptConfig.
	// Injected into selection prompts so the LLM selector knows about
	// tools that are always required regardless of user query.
	// Example: "Always check for reusable test scripts" → includes lookup_scripts
	customInstructions []string // ORCH-014

	// Logger for observability
	logger core.Logger

	// Telemetry for metrics
	telemetry core.Telemetry

	// LLM Debug Store integration (per LLM_DEBUG_PAYLOAD_DESIGN.md)
	debugStore LLMDebugStore  // For recording LLM interactions
	debugWg    sync.WaitGroup // Tracks in-flight debug recordings for graceful shutdown
	debugSeqID atomic.Uint64  // For generating unique fallback IDs when TraceID is empty

	// Circuit breaker for sophisticated resilience (optional)
	circuitBreaker core.CircuitBreaker
}

// Environment variable constants for tiered resolution
const (
	// EnvTieredMinTools overrides the minimum tool count to trigger tiering.
	// Example: TRUVAG3_TIERED_MIN_TOOLS=15
	EnvTieredMinTools = "TRUVAG3_TIERED_MIN_TOOLS"

	// EnvTieredSelectionMaxTokens overrides the maximum output tokens for tiered selection LLM calls.
	// Example: TRUVAG3_TIERED_SELECTION_MAX_TOKENS=3000
	EnvTieredSelectionMaxTokens = "TRUVAG3_TIERED_SELECTION_MAX_TOKENS" // #nosec G101 -- env var name, not a credential

	// errTieredSelectionEmptyResponse is the sentinel error string recorded in
	// LLMInteraction.Error when the LLM returns an empty/whitespace-only response.
	errTieredSelectionEmptyResponse = "empty_response"
)

// NewTieredCapabilityProvider creates a provider with intelligent tiered resolution.
// Configuration precedence: Explicit config → TRUVAG3_TIERED_* env vars → defaults
// Both tiers use the AI client's default model for simplicity.
func NewTieredCapabilityProvider(
	catalog *AgentCatalog,
	aiClient core.AIClient,
	config *TieredCapabilityConfig,
) *TieredCapabilityProvider {
	if config == nil {
		config = &TieredCapabilityConfig{}
	}

	// Resolve MinToolsForTiering with environment variable fallback
	// Precedence: Explicit config → TRUVAG3_TIERED_MIN_TOOLS → 20
	minTools := config.MinToolsForTiering
	if minTools == 0 {
		if envVal := os.Getenv(EnvTieredMinTools); envVal != "" {
			if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 {
				minTools = parsed
			}
		}
	}
	if minTools == 0 {
		minTools = 20 // Research-backed default
	}

	// Resolve SelectionMaxTokens with environment variable fallback
	// Precedence: Explicit config → TRUVAG3_TIERED_SELECTION_MAX_TOKENS → 2000
	selMaxTokens := config.SelectionMaxTokens
	if selMaxTokens == 0 {
		if envVal := os.Getenv(EnvTieredSelectionMaxTokens); envVal != "" {
			if parsed, err := strconv.Atoi(envVal); err == nil && parsed > 0 {
				selMaxTokens = parsed
			}
		}
	}
	if selMaxTokens == 0 {
		selMaxTokens = 2000 // Allow room for complex multi-tool selections
	}

	// Retry config — defaults (true, 2) are set in DefaultConfig().
	// Direct callers must set RetryEnabled explicitly if desired.
	retryEnabled := config.RetryEnabled
	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	return &TieredCapabilityProvider{
		catalog:            catalog,
		aiClient:           aiClient,
		MinToolsForTiering: minTools,
		SelectionMaxTokens: selMaxTokens,
		retryEnabled:       retryEnabled,
		maxRetries:         maxRetries,
	}
}

// SetLogger sets the logger for observability
func (t *TieredCapabilityProvider) SetLogger(logger core.Logger) {
	if logger != nil {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			t.logger = cal.WithComponent("framework/orchestration/tiered")
		} else {
			t.logger = logger
		}
	}
}

// SetTelemetry sets the telemetry provider for metrics
func (t *TieredCapabilityProvider) SetTelemetry(telemetry core.Telemetry) {
	t.telemetry = telemetry
}

// SetAIOptionsOverride sets the per-phase AI options override for tiered selection calls.
func (t *TieredCapabilityProvider) SetAIOptionsOverride(opts *AIOptionsOverride) {
	t.aiOptionsOverride = opts
}

// SetLLMDebugStore sets the debug store for recording LLM interactions.
// Per LLM_DEBUG_PAYLOAD_DESIGN.md, this enables recording of tiered_selection calls.
func (t *TieredCapabilityProvider) SetLLMDebugStore(store LLMDebugStore) {
	t.debugStore = store
}

// GetLLMDebugStore returns the debug store (for testing/inspection)
func (t *TieredCapabilityProvider) GetLLMDebugStore() LLMDebugStore {
	return t.debugStore
}

// SetCircuitBreaker sets the circuit breaker for sophisticated resilience.
// When set, LLM calls are wrapped with circuit breaker protection.
func (t *TieredCapabilityProvider) SetCircuitBreaker(cb core.CircuitBreaker) {
	t.circuitBreaker = cb
}

// logDebugWithContext logs debug messages with context for trace correlation.
// Follows Pattern 1 (nil check), Pattern 2 (operation), Pattern 3 (request_id).
func (t *TieredCapabilityProvider) logDebugWithContext(ctx context.Context, msg string, extraFields map[string]interface{}) {
	if t.logger != nil {
		fields := map[string]interface{}{
			"operation": "tiered_selection",
		}
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			if reqID := baggage["request_id"]; reqID != "" {
				fields["request_id"] = reqID
			}
		}
		for k, v := range extraFields {
			fields[k] = v
		}
		t.logger.DebugWithContext(ctx, msg, fields)
	}
}

// logInfoWithContext logs info messages with context for trace correlation.
func (t *TieredCapabilityProvider) logInfoWithContext(ctx context.Context, msg string, extraFields map[string]interface{}) {
	if t.logger != nil {
		fields := map[string]interface{}{
			"operation": "tiered_selection",
		}
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			if reqID := baggage["request_id"]; reqID != "" {
				fields["request_id"] = reqID
			}
		}
		for k, v := range extraFields {
			fields[k] = v
		}
		t.logger.InfoWithContext(ctx, msg, fields)
	}
}

// logWarnWithContext logs warning messages with context for trace correlation.
func (t *TieredCapabilityProvider) logWarnWithContext(ctx context.Context, msg string, extraFields map[string]interface{}) {
	if t.logger != nil {
		fields := map[string]interface{}{
			"operation": "tiered_selection",
		}
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			if reqID := baggage["request_id"]; reqID != "" {
				fields["request_id"] = reqID
			}
		}
		for k, v := range extraFields {
			fields[k] = v
		}
		t.logger.WarnWithContext(ctx, msg, fields)
	}
}

// SetCustomInstructions configures domain-specific workflow rules for tiered selection.
// These are injected into selection prompts so the LLM selector knows about tools
// that are always required regardless of user query. ORCH-014 fix.
func (t *TieredCapabilityProvider) SetCustomInstructions(instructions []string) {
	t.customInstructions = instructions
}

// writeCustomInstructions appends the <custom_instructions> XML section to sb
// if instructions are non-empty. Shared by TieredCapabilityProvider (selection prompts)
// and AIOrchestrator (continuation planning prompts). ORCH-012/ORCH-014.
func writeCustomInstructions(sb *strings.Builder, instructions []string) {
	if len(instructions) == 0 {
		return
	}
	sb.WriteString("<custom_instructions>\n")
	for i, inst := range instructions {
		fmt.Fprintf(sb, "%d. %s\n", i+1, inst)
	}
	sb.WriteString("</custom_instructions>\n\n")
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Uses WaitGroup to ensure graceful shutdown waits for pending recordings.
// Per LLM_DEBUG_PAYLOAD_DESIGN.md section 4.6 Lifecycle Management.
func (t *TieredCapabilityProvider) recordDebugInteraction(ctx context.Context, interaction LLMInteraction) {
	if t.debugStore == nil {
		return
	}

	// Priority for requestID:
	// 1. Orchestrator's requestID from context value (for grouped debug records)
	// 2. Request ID from telemetry baggage (set by orchestrator in ProcessRequest)
	// 3. Trace ID from telemetry context
	// 4. Generated fallback ID
	requestID := GetRequestID(ctx)
	if requestID == "" {
		// Check telemetry baggage (orchestrator sets this in ProcessRequest)
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			requestID = baggage["request_id"]
		}
	}
	if requestID == "" {
		// GetTraceContext is nil-safe, returns empty TraceID if no span
		tc := telemetry.GetTraceContext(ctx)
		requestID = tc.TraceID
	}
	if requestID == "" {
		// Generate unique fallback ID using atomic counter (collision-safe)
		seq := t.debugSeqID.Add(1)
		requestID = fmt.Sprintf("tiered-no-trace-%d-%d", time.Now().Unix(), seq)
	}

	t.debugWg.Add(1)
	go func() {
		defer t.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := t.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			if t.logger != nil {
				t.logger.Warn("Failed to record tiered_selection debug interaction", map[string]interface{}{
					"operation":  "llm_debug_record",
					"request_id": requestID,
					"type":       interaction.Type,
					"error":      err.Error(),
				})
			}
		}
	}()
}

// Shutdown waits for pending debug recordings with a timeout.
// Should be called during graceful shutdown to ensure no data loss.
func (t *TieredCapabilityProvider) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		t.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		if t.logger != nil {
			t.logger.Warn("TieredCapabilityProvider shutdown timeout: some debug recordings may be lost", map[string]interface{}{
				"operation": "tiered_provider_shutdown",
			})
		}
		return fmt.Errorf("tiered provider shutdown timeout: %w", ctx.Err())
	}
}

// GetCapabilities implements CapabilityProvider with tiered resolution.
// It automatically chooses between direct (all tools) and tiered based on tool count.
func (t *TieredCapabilityProvider) GetCapabilities(
	ctx context.Context,
	request string,
	metadata map[string]interface{},
) (*CapabilityResult, error) {
	// Get all capability summaries
	summaries := t.catalog.GetCapabilitySummaries()

	// Check if tiering is beneficial
	if len(summaries) < t.MinToolsForTiering {
		// Below threshold - use direct approach (simpler, one LLM call)
		t.logDebugWithContext(ctx, "Below tiering threshold, using direct approach", map[string]interface{}{
			"tool_count": len(summaries),
			"threshold":  t.MinToolsForTiering,
		})
		// Get all agent names from catalog
		return t.buildResultFromAllAgents(), nil
	}

	// Tier 1: Select relevant tools using lightweight summaries
	tier1Start := time.Now()
	selectedTools, err := t.selectRelevantTools(ctx, request, summaries, metadata)
	tier1Duration := time.Since(tier1Start)

	if err != nil {
		// Fallback to direct approach on selection failure
		t.logWarnWithContext(ctx, "Tool selection failed, falling back to direct approach", map[string]interface{}{
			"status":        "fallback",
			"error":         err.Error(),
			"duration_ms":   tier1Duration.Milliseconds(),
			"context_aware": metadata != nil && metadata[PhaseContextKeyPhaseNumber] != nil,
		})
		return t.buildResultFromAllAgents(), nil
	}

	contextAware := metadata != nil && metadata[PhaseContextKeyPhaseNumber] != nil
	t.logInfoWithContext(ctx, "Tier 1 tool selection complete", map[string]interface{}{
		"status":                    "success",
		"total_tools":               len(summaries),
		"selected_tools":            selectedTools,
		"reduction":                 fmt.Sprintf("%.1f%%", (1-float64(len(selectedTools))/float64(len(summaries)))*100),
		"duration_ms":               tier1Duration.Milliseconds(),
		"context_aware":             contextAware,
		"custom_instructions_count": len(t.customInstructions),
	})

	// Record metrics for observability (Phase 5)
	if t.telemetry != nil {
		t.telemetry.RecordMetric("orchestrator.tiered.tool_selection", 1, map[string]string{
			"total_tools":    strconv.Itoa(len(summaries)),
			"selected_tools": strconv.Itoa(len(selectedTools)),
		})

		// Record token savings estimate (~200 tokens per full schema)
		savedTokens := (len(summaries) - len(selectedTools)) * 200
		t.telemetry.RecordMetric("orchestrator.tiered.tokens_saved", float64(savedTokens), nil)
	}

	// Extract unique agent names from selected tools (format: "agent/capability")
	agentNames := extractAgentNamesFromToolIDs(selectedTools)

	// Tier 2: Get full schemas for selected tools only
	return &CapabilityResult{
		FormattedInfo: t.catalog.FormatToolsForLLM(selectedTools),
		AgentNames:    agentNames,
	}, nil
}

// buildResultFromAllAgents creates a CapabilityResult with all public agents from the catalog.
// Used when tiering is not beneficial or as a fallback.
// Uses GetPublicAgentNames() to ensure AgentNames matches the agents in FormattedInfo.
func (t *TieredCapabilityProvider) buildResultFromAllAgents() *CapabilityResult {
	// Use GetPublicAgentNames to match FormatForLLM filtering (excludes internal-only agents)
	agentNames := t.catalog.GetPublicAgentNames()

	return &CapabilityResult{
		FormattedInfo: t.catalog.FormatForLLM(),
		AgentNames:    agentNames,
	}
}

// extractAgentNamesFromToolIDs extracts unique agent names from tool IDs.
// Tool IDs are in format "agent-name/capability-name".
// Names are normalized to lowercase for consistent case-insensitive matching during
// hallucination validation. This ensures consistency across the system.
func extractAgentNamesFromToolIDs(toolIDs []string) []string {
	agentSet := make(map[string]bool)
	for _, toolID := range toolIDs {
		// Split "agent/capability" and take the agent part
		// Normalize to lowercase for consistent case-insensitive matching
		if idx := strings.Index(toolID, "/"); idx > 0 {
			agentSet[strings.ToLower(toolID[:idx])] = true
		} else {
			// If no slash, treat the whole ID as agent name
			agentSet[strings.ToLower(toolID)] = true
		}
	}

	agents := make([]string, 0, len(agentSet))
	for agent := range agentSet {
		agents = append(agents, agent)
	}
	return agents
}

// selectRelevantTools uses an LLM call to identify which tools are needed.
// Uses structured prompting (Guided-Structured Templates, Sept 2025) and validates
// results to filter hallucinated tools (RAG-MCP, May 2025).
// Records LLM interaction to debug store per LLM_DEBUG_PAYLOAD_DESIGN.md.
// Uses AI client's default model - cost savings come from reduced token counts.
func (t *TieredCapabilityProvider) selectRelevantTools(
	ctx context.Context,
	request string,
	summaries []CapabilitySummary,
	phaseContext map[string]interface{}, // nil for Phase 1, populated for Phase 2+
) ([]string, error) {
	// Check context before expensive LLM call (per Component Lifecycle Rules)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Build the selection prompt — context-aware for Phase 2+
	var prompt string
	var promptErr error
	if phaseContext != nil && phaseContext[PhaseContextKeyPhaseNumber] != nil {
		telemetry.Counter("tiered_selection.context_aware.total",
			"module", telemetry.ModuleOrchestration,
		)
		prompt, promptErr = t.buildPreparedContinuationSelectionPrompt(ctx, summaries, request, phaseContext)
	} else {
		prompt, promptErr = t.buildPreparedSelectionPrompt(ctx, summaries, request)
	}
	if promptErr != nil {
		return nil, fmt.Errorf("prepare tiered selection prompt input: %w", promptErr)
	}

	// Use deterministic settings for tool selection
	options := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0,                  // Deterministic selection
		MaxTokens:   t.SelectionMaxTokens, // Configurable via TRUVAG3_TIERED_SELECTION_MAX_TOKENS
	}, t.aiOptionsOverride)
	// Uses AI client's default model - no override needed

	// Capture timing for LLM debug recording
	llmStartTime := time.Now()

	// Get requestID for span events (same logic as recordDebugInteraction)
	requestID := GetRequestID(ctx)
	if requestID == "" {
		// Check telemetry baggage (orchestrator sets this in ProcessRequest)
		if baggage := telemetry.GetBaggage(ctx); baggage != nil {
			requestID = baggage["request_id"]
		}
	}
	if requestID == "" {
		tc := telemetry.GetTraceContext(ctx)
		requestID = tc.TraceID
	}

	// Telemetry: Record phase context for visibility when debugging Phase 2+ selection
	if phaseContext != nil && phaseContext[PhaseContextKeyPhaseNumber] != nil {
		phaseNum, _ := phaseContext[PhaseContextKeyPhaseNumber].(int)
		note, _ := phaseContext[PhaseContextKeyContinuationNote].(string)
		priorTools, _ := phaseContext[PhaseContextKeyPriorToolsUsed].([]string)
		summary, _ := phaseContext[PhaseContextKeyCompletedSummary].(string)

		telemetry.AddSpanEvent(ctx, "llm.tiered_selection.phase_context",
			attribute.String("request_id", requestID),
			attribute.Int("phase_number", phaseNum),
			attribute.String("continuation_note", truncateString(note, 200)),
			attribute.Int("prior_tools_count", len(priorTools)),
			attribute.String("prior_tools", strings.Join(priorTools, ",")),
			attribute.Int("summary_length", len(summary)),
		)
	}

	maxAttempts := 1
	if t.retryEnabled {
		maxAttempts = t.maxRetries + 1 // maxRetries is additional attempts beyond the first
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Telemetry: Record LLM request for visibility in distributed traces
		telemetry.AddSpanEvent(ctx, "llm.tiered_selection.request",
			attribute.String("request_id", requestID),
			attribute.String("user_request", truncateRequest(request, 200)),
			attribute.Int("prompt_length", len(prompt)),
			attribute.Int("tool_count", len(summaries)),
			attribute.Float64("temperature", 0.0),
			attribute.Int("max_tokens", options.MaxTokens),
			attribute.Int("attempt", attempt),
			attribute.Int("custom_instructions_count", len(t.customInstructions)),
		)

		// Make the LLM call, optionally wrapped with circuit breaker
		var response *core.AIResponse
		var invocationResult *aiInvocationResult
		var err error

		ctx := telemetry.WithBaggage(ctx, "ai.purpose", "tiered_selection")
		invocation := aiInvocation{
			Purpose:        "tiered-selection",
			Prompt:         prompt,
			Options:        options,
			DeferRecording: t.debugStore != nil,
		}
		if t.circuitBreaker != nil {
			err = t.circuitBreaker.Execute(ctx, func() error {
				var cbErr error
				invocationResult, cbErr = invokeAI(ctx, t.aiClient, invocation)
				if invocationResult != nil {
					response = invocationResult.Response
				}
				return cbErr
			})
		} else {
			invocationResult, err = invokeAI(ctx, t.aiClient, invocation)
			if invocationResult != nil {
				response = invocationResult.Response
			}
		}
		effective := effectiveAIRequestForDebug(invocationResult, invocation)
		model, provider := effectiveAIIdentity(invocationResult, response, err)
		effectiveTemperature := effectiveAITemperature(effective, options.Temperature)
		effectiveMaxTokens := effectiveAIMaxTokens(effective, options.MaxTokens)
		if err == nil && response != nil {
			core.RecordTokenUsage(ctx, "tiered_selection", response.Usage)
		}
		llmDuration := time.Since(llmStartTime)

		// LLM Debug: Record interaction (success or failure)
		// Per LLM_DEBUG_PAYLOAD_DESIGN.md - this is the 7th recording site: "tiered_selection"
		if err != nil {
			// LLM API error — not retryable at this level (circuit breaker handles transport retries)
			telemetry.AddSpanEvent(ctx, "llm.tiered_selection.error",
				attribute.String("request_id", requestID),
				attribute.String("error", err.Error()),
				attribute.Int64("duration_ms", llmDuration.Milliseconds()),
				attribute.Int("attempt", attempt),
			)

			t.recordDebugInteraction(ctx, LLMInteraction{
				Type:         "tiered_selection",
				Timestamp:    llmStartTime,
				DurationMs:   llmDuration.Milliseconds(),
				Prompt:       effective.Prompt,
				SystemPrompt: effective.SystemPrompt,
				Temperature:  effectiveTemperature,
				MaxTokens:    effectiveMaxTokens,
				Model:        model,
				Provider:     provider,
				Success:      false,
				Error:        err.Error(),
				Attempt:      attempt,
			})
			return nil, fmt.Errorf("tiered tool selection failed for request %q: %w",
				truncateRequest(request, 50), err)
		}

		if strings.TrimSpace(response.Content) == "" {
			t.logWarnWithContext(ctx, "Tiered selection LLM returned empty response", map[string]interface{}{
				"request_id":   requestID,
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"will_retry":   attempt < maxAttempts,
			})

			telemetry.AddSpanEvent(ctx, "llm.tiered_selection.empty_response",
				attribute.String("request_id", requestID),
				attribute.Int("attempt", attempt),
				attribute.Bool("will_retry", attempt < maxAttempts),
			)
			telemetry.Counter("orchestrator.tiered.empty_response",
				"module", telemetry.ModuleOrchestration,
			)

			t.recordDebugInteraction(ctx, LLMInteraction{
				Type:             "tiered_selection",
				Timestamp:        llmStartTime,
				DurationMs:       llmDuration.Milliseconds(),
				Prompt:           effective.Prompt,
				SystemPrompt:     effective.SystemPrompt,
				Temperature:      effectiveTemperature,
				MaxTokens:        effectiveMaxTokens,
				Model:            model,
				Provider:         provider,
				Response:         response.Content,
				PromptTokens:     response.Usage.PromptTokens,
				CompletionTokens: response.Usage.CompletionTokens,
				TotalTokens:      response.Usage.TotalTokens,
				Success:          false,
				Error:            errTieredSelectionEmptyResponse,
				Attempt:          attempt,
			})

			if attempt < maxAttempts {
				telemetry.Counter("orchestrator.tiered.retry",
					"reason", errTieredSelectionEmptyResponse,
					"module", telemetry.ModuleOrchestration,
				)
				llmStartTime = time.Now() // Reset timer for next attempt
				continue
			}
			return nil, fmt.Errorf("tiered selection returned empty response after %d attempt(s)", attempt)
		}

		// Telemetry: Record LLM response for visibility in distributed traces
		telemetry.AddSpanEvent(ctx, "llm.tiered_selection.response",
			attribute.String("request_id", requestID),
			attribute.String("response", truncateRequest(response.Content, 500)),
			attribute.Int("response_length", len(response.Content)),
			attribute.Int("prompt_tokens", response.Usage.PromptTokens),
			attribute.Int("completion_tokens", response.Usage.CompletionTokens),
			attribute.Int("total_tokens", response.Usage.TotalTokens),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
			attribute.Int("attempt", attempt),
		)

		// Parse BEFORE recording success — Success field must reflect actual usability
		selectedTools, parseErr := t.parseToolSelection(response.Content)

		// ORCH-018 Layer 3: Defensive empty-response handling.
		// The selector returned a well-formed empty array []. parseErr will be
		// errNoToolsSelected (the sentinel from parseToolSelection). We use
		// errors.Is so the check remains robust if parseToolSelection is ever
		// refactored to wrap the error.
		//
		// Two cases — both short-circuit retries because re-running the same
		// prompt at temperature=0 will produce the same answer:
		//
		//   (a) Continuation phase WITH prior_tool_ids → recover by reusing
		//       the prior tools as the selection. This preserves tiered
		//       selection's filter purpose: the planner sees a small relevant
		//       subset, not the full catalog. Layer 2's prompt instructs the
		//       LLM to do this directly; this branch is the defensive net for
		//       LLM disobedience.
		//
		//   (b) Phase 1 OR continuation without prior_tool_ids → there is no
		//       sensible filtered subset to fall back to. Return the sentinel
		//       error immediately (no retries) so the caller in selectTools
		//       hits its existing all-agents fallback path after exactly ONE
		//       LLM call instead of maxAttempts. This is correct because
		//       Phase 1 with semantic-empty genuinely means "no tool in the
		//       catalog matches this request" — retrying cannot change that
		//       at temperature=0, and the existing fallback already handles
		//       it (the planner sees the full catalog and answers from
		//       conversation context, e.g. "Do you remember our previous
		//       conversation?" → no tool needed).
		if errors.Is(parseErr, errNoToolsSelected) {
			if priorIDs, ok := phaseContext[PhaseContextKeyPriorToolIDs].([]string); ok && len(priorIDs) > 0 {
				// Case (a): continuation phase with prior tools — recover.
				selectedTools = priorIDs
				parseErr = nil

				// Counter — Pattern 5: module label
				telemetry.Counter("orchestrator.tiered.empty_fallback_to_prior",
					"module", telemetry.ModuleOrchestration,
				)
				// Span event — Pattern 6: request_id FIRST
				telemetry.AddSpanEvent(ctx, "llm.tiered_selection.empty_recovered",
					attribute.String("request_id", requestID),
					attribute.Int("prior_count", len(priorIDs)),
					attribute.Int("attempt", attempt),
				)
				// Structured log — Patterns 1+2+3 via the framework helper
				// (t.logInfoWithContext encapsulates the nil check).
				t.logInfoWithContext(ctx, "Tiered selection returned empty; recovered using prior_tool_ids", map[string]interface{}{
					"operation":   "empty_fallback_to_prior",
					"request_id":  requestID,
					"attempt":     attempt,
					"prior_count": len(priorIDs),
				})
				// Fall through to the success path below.
			} else {
				// Case (b): Phase 1 (or continuation without prior tools).
				// Record one debug interaction marking this as semantic-empty
				// and surface the sentinel error WITHOUT retrying. The caller
				// will fall back to all-agents in selectTools.
				telemetry.Counter("orchestrator.tiered.semantic_empty_phase1",
					"module", telemetry.ModuleOrchestration,
				)
				telemetry.AddSpanEvent(ctx, "llm.tiered_selection.semantic_empty_phase1",
					attribute.String("request_id", requestID),
					attribute.Int("attempt", attempt),
				)
				t.logInfoWithContext(ctx, "Tiered selection returned semantic empty on Phase 1 — surfacing immediately (no retries)", map[string]interface{}{
					"operation":  "semantic_empty_phase1",
					"request_id": requestID,
					"attempt":    attempt,
				})
				t.recordDebugInteraction(ctx, LLMInteraction{
					Type:             "tiered_selection",
					Timestamp:        llmStartTime,
					DurationMs:       llmDuration.Milliseconds(),
					Prompt:           effective.Prompt,
					SystemPrompt:     effective.SystemPrompt,
					Temperature:      effectiveTemperature,
					MaxTokens:        effectiveMaxTokens,
					Model:            model,
					Provider:         provider,
					Response:         response.Content,
					PromptTokens:     response.Usage.PromptTokens,
					CompletionTokens: response.Usage.CompletionTokens,
					TotalTokens:      response.Usage.TotalTokens,
					Success:          true, // semantic-empty is a valid signal, not a failure
					Error:            "semantic_empty: no tools in catalog match this request",
					Attempt:          attempt,
				})
				// Return the sentinel error directly. selectTools has an
				// existing graceful-degradation path that converts this into
				// an all-agents fallback after a warn-level log entry.
				return nil, errNoToolsSelected
			}
		}

		if parseErr != nil {
			t.logWarnWithContext(ctx, "Tiered selection parse failed", map[string]interface{}{
				"request_id":   requestID,
				"attempt":      attempt,
				"max_attempts": maxAttempts,
				"error":        parseErr.Error(),
				"will_retry":   attempt < maxAttempts,
			})

			telemetry.AddSpanEvent(ctx, "llm.tiered_selection.parse_failed",
				attribute.String("request_id", requestID),
				attribute.Int("attempt", attempt),
				attribute.String("error", parseErr.Error()),
				attribute.Bool("will_retry", attempt < maxAttempts),
			)

			t.recordDebugInteraction(ctx, LLMInteraction{
				Type:             "tiered_selection",
				Timestamp:        llmStartTime,
				DurationMs:       llmDuration.Milliseconds(),
				Prompt:           effective.Prompt,
				SystemPrompt:     effective.SystemPrompt,
				Temperature:      effectiveTemperature,
				MaxTokens:        effectiveMaxTokens,
				Model:            model,
				Provider:         provider,
				Response:         response.Content,
				PromptTokens:     response.Usage.PromptTokens,
				CompletionTokens: response.Usage.CompletionTokens,
				TotalTokens:      response.Usage.TotalTokens,
				Success:          false,
				Error:            parseErr.Error(),
				Attempt:          attempt,
			})

			if attempt < maxAttempts {
				telemetry.Counter("orchestrator.tiered.retry",
					"reason", "parse_failed",
					"module", telemetry.ModuleOrchestration,
				)
				llmStartTime = time.Now() // Reset timer for next attempt
				continue
			}
			return nil, parseErr
		}

		// Record successful selection (AFTER parse succeeds)
		t.recordDebugInteraction(ctx, LLMInteraction{
			Type:             "tiered_selection",
			Timestamp:        llmStartTime,
			DurationMs:       llmDuration.Milliseconds(),
			Prompt:           effective.Prompt,
			SystemPrompt:     effective.SystemPrompt,
			Temperature:      effectiveTemperature,
			MaxTokens:        effectiveMaxTokens,
			Model:            model,
			Provider:         provider,
			Response:         response.Content,
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      response.Usage.TotalTokens,
			Success:          true,
			Attempt:          attempt,
		})

		if attempt > 1 {
			telemetry.Counter("orchestrator.tiered.retry_success",
				"attempt", strconv.Itoa(attempt),
				"module", telemetry.ModuleOrchestration,
			)
		}

		// Validate and filter to prevent hallucinated tool names
		// RAG-MCP research: "model often picks the wrong one or makes up fake tools"
		validatedTools := t.validateAndFilterTools(ctx, selectedTools, summaries)

		// Telemetry: Compare Phase 2+ selection with prior phase tools
		if phaseContext != nil && phaseContext[PhaseContextKeyPhaseNumber] != nil {
			if priorTools, ok := phaseContext[PhaseContextKeyPriorToolsUsed].([]string); ok {
				phaseNum, _ := phaseContext[PhaseContextKeyPhaseNumber].(int)

				priorSet := make(map[string]bool, len(priorTools))
				for _, tool := range priorTools {
					priorSet[tool] = true
				}
				var newTools []string
				for _, toolID := range validatedTools {
					agent := strings.Split(toolID, "/")[0]
					if !priorSet[agent] {
						newTools = append(newTools, toolID)
					}
				}

				telemetry.AddSpanEvent(ctx, "llm.tiered_selection.phase_comparison",
					attribute.String("request_id", requestID),
					attribute.Int("phase_number", phaseNum),
					attribute.Int("prior_tool_count", len(priorTools)),
					attribute.Int("selected_tool_count", len(validatedTools)),
					attribute.Int("new_tool_count", len(newTools)),
					attribute.String("new_tools", strings.Join(newTools, ",")),
				)
			}
		}

		if len(validatedTools) == 0 {
			hallucinationErr := fmt.Errorf("no valid tools after filtering (all selections were hallucinated)")

			t.recordDebugInteraction(ctx, LLMInteraction{
				Type:             "tiered_selection",
				Timestamp:        llmStartTime,
				DurationMs:       llmDuration.Milliseconds(),
				Prompt:           effective.Prompt,
				SystemPrompt:     effective.SystemPrompt,
				Temperature:      effectiveTemperature,
				MaxTokens:        effectiveMaxTokens,
				Model:            model,
				Provider:         provider,
				Response:         response.Content,
				PromptTokens:     response.Usage.PromptTokens,
				CompletionTokens: response.Usage.CompletionTokens,
				TotalTokens:      response.Usage.TotalTokens,
				Success:          false,
				Error:            hallucinationErr.Error(),
				Attempt:          attempt,
			})

			if attempt < maxAttempts {
				telemetry.Counter("orchestrator.tiered.retry",
					"reason", "all_hallucinated",
					"module", telemetry.ModuleOrchestration,
				)
				llmStartTime = time.Now() // Reset timer for next attempt
				continue
			}
			return nil, hallucinationErr
		}

		return validatedTools, nil
	}

	// Should not reach here (loop always returns), but safety fallback
	return nil, fmt.Errorf("tiered selection exhausted all %d attempts", maxAttempts)
}

// buildSelectionPrompt creates the Tier 1 prompt with tool summaries.
// Uses structured template approach based on "Guided-Structured Templates" research (Sept 2025)
// which shows 3-12% accuracy improvement over free-form prompts.
func (t *TieredCapabilityProvider) buildSelectionPrompt(
	ctx context.Context,
	summaries []CapabilitySummary,
	request string,
) string {
	return t.buildSelectionPromptFromCatalog(ctx, formatCapabilitySummaryCatalog(summaries), request)
}

func (t *TieredCapabilityProvider) buildPreparedSelectionPrompt(
	ctx context.Context,
	summaries []CapabilitySummary,
	request string,
) (string, error) {
	preparedRequest, err := preparePromptValue(
		ctx, promptCapabilitySelect, promptValueRequest, promptFieldRequest, request,
	)
	if err != nil {
		return "", err
	}
	preparedCatalog, err := preparePromptValue(
		ctx, promptCapabilitySelect, promptValueCapabilityCatalog,
		promptFieldCapabilityCatalog, formatCapabilitySummaryCatalog(summaries),
	)
	if err != nil {
		return "", err
	}
	preparedEnrichments, err := prepareKnownPromptEnrichments(
		ctx, promptCapabilitySelect, core.GetPipelineEnrichments(ctx),
	)
	if err != nil {
		return "", err
	}
	ctx = core.WithPipelineEnrichments(ctx, preparedEnrichments)
	return t.buildSelectionPromptFromCatalog(ctx, preparedCatalog, preparedRequest), nil
}

func (t *TieredCapabilityProvider) buildSelectionPromptFromCatalog(
	ctx context.Context,
	catalog string,
	request string,
) string {
	var sb strings.Builder

	sb.WriteString(`<identity>
You are a tool selector for a multi-agent system.
Reason silently. Output raw JSON only — no reasoning text, no markdown, no code blocks.
</identity>

<selection_guide>
A. PRIMARY TOOLS: Which tools directly address the user's explicit requests?
   - What information is the user explicitly asking for?
   - Which tools provide that information?

B. DATA DEPENDENCY TOOLS: What intermediate data is needed?
   - Do any selected tools require input from other tools?
   - Example: Weather tools often need coordinates -> need geocoding tool
   - Example: Currency conversion needs currency codes -> need country-info tool

C. COMPLETENESS CHECK: Review each part of the request
   - Is every aspect of the user's request covered?
   - Are all data dependencies satisfied?
</selection_guide>

`)

	// ORCH-014 fix: Inject CustomInstructions so selection LLM knows about
	// domain-specific tool requirements not implied by the user query.
	writeCustomInstructions(&sb, t.customInstructions)

	sb.WriteString("<available_tools>\n")
	sb.WriteString(catalog)
	sb.WriteString("</available_tools>\n\n")

	// Inject conversation history from pipeline enrichments so the selection LLM
	// can interpret follow-up queries like "Can you check from the official website?"
	// that are meaningless without prior conversation context.
	if enrichments := core.GetPipelineEnrichments(ctx); len(enrichments) > 0 {
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				sb.WriteString("<conversation_history>\n")
				sb.WriteString(convStr)
				sb.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	fmt.Fprintf(&sb, `<user_request>
%s
</user_request>

<output_format>
JSON array of tool identifiers using "agent_name/capability_name" format.
Example: ["stock-service/stock_quote", "country-info-tool/get_country_info", "currency-tool/convert_currency"]
</output_format>

JSON array:
`, request)

	return sb.String()
}

// buildContinuationSelectionPrompt builds a phase-aware tool selection prompt
// for Phase 2+ of iterative DAG execution. Unlike buildSelectionPrompt (Phase 1),
// this includes prior phase execution context: tools already used, the LLM's
// continuation_note explaining why more phases are needed, and a compact summary
// of what Phase 1 discovered.
//
// This enables the selection LLM to choose tools that weren't predictable from
// the original request alone — e.g., discovering a visa requirement during
// research triggers selection of a visa-check tool for Phase 2.
//
// Prompt design applies Issue 5/6 research principles:
//   - P3: All positive instructions (zero negatives)
//   - P1: Concrete output example in <output_format>
//   - P9: XML section tags (consistent with restructured BuildPlanningPrompt)
//   - P8: Key behavioral directive at top (U-curve), format anchor at bottom (Gemini dual-anchor)
//   - P4: "newly relevant tools" concept stated once (in <selection_guide> C)
func (t *TieredCapabilityProvider) buildContinuationSelectionPrompt(
	ctx context.Context,
	summaries []CapabilitySummary,
	request string,
	phaseContext map[string]interface{},
) string {
	return t.buildContinuationSelectionPromptFromCatalog(
		ctx, formatCapabilitySummaryCatalog(summaries), request, phaseContext,
	)
}

func (t *TieredCapabilityProvider) buildPreparedContinuationSelectionPrompt(
	ctx context.Context,
	summaries []CapabilitySummary,
	request string,
	phaseContext map[string]interface{},
) (string, error) {
	preparedRequest, err := preparePromptValue(
		ctx, promptCapabilitySelect, promptValueRequest, promptFieldRequest, request,
	)
	if err != nil {
		return "", err
	}
	preparedCatalog, err := preparePromptValue(
		ctx, promptCapabilitySelect, promptValueCapabilityCatalog,
		promptFieldCapabilityCatalog, formatCapabilitySummaryCatalog(summaries),
	)
	if err != nil {
		return "", err
	}
	preparedPhaseContext := clonePromptMetadata(phaseContext)
	if priorIDs, ok := phaseContext[PhaseContextKeyPriorToolIDs].([]string); ok {
		preparedIDs := make([]string, len(priorIDs))
		for index, value := range priorIDs {
			preparedIDs[index], err = preparePromptValue(
				ctx, promptCapabilitySelect, promptValuePhaseContext,
				promptFieldPhaseContextPriorToolID, value,
			)
			if err != nil {
				return "", err
			}
		}
		preparedPhaseContext[PhaseContextKeyPriorToolIDs] = preparedIDs
	}
	preparedEnrichments, err := prepareKnownPromptEnrichments(
		ctx, promptCapabilitySelect, core.GetPipelineEnrichments(ctx),
	)
	if err != nil {
		return "", err
	}
	ctx = core.WithPipelineEnrichments(ctx, preparedEnrichments)
	return t.buildContinuationSelectionPromptFromCatalog(
		ctx, preparedCatalog, preparedRequest, preparedPhaseContext,
	), nil
}

func (t *TieredCapabilityProvider) buildContinuationSelectionPromptFromCatalog(
	ctx context.Context,
	catalog string,
	request string,
	phaseContext map[string]interface{},
) string {
	var sb strings.Builder

	// Identity + key behavioral directive at top (U-curve high-attention zone).
	// "Reason internally... output a JSON array only" is the most important
	// instruction — placed at line 2 for maximum salience.
	sb.WriteString(`<identity>
You are a tool selector for a continuation phase of multi-step execution.
Reason silently. Output raw JSON only — no reasoning text, no markdown, no code blocks.
</identity>

`)

	// Phase context — the key differentiator from buildSelectionPrompt.
	// Dynamic content placed after identity per P2 (Identity → Instructions → Context).
	//
	// ORCH-018 Layer 2: continuation_note DROPPED — it was mixed-purpose narrative
	// that pushed the selector toward reasoning about WHETHER to proceed (a planner
	// concern) instead of filtering tools. discoveries_so_far is also dropped
	// because the bug-causing pattern was "continuation_note says X + discoveries
	// show X done → []". Prior tools now carried as agent/capability IDs via
	// PhaseContextKeyPriorToolIDs so the LLM can copy them verbatim into its
	// selection when step D of the selection guide applies.
	sb.WriteString("<phase_context>\n")
	if priorIDs, ok := phaseContext[PhaseContextKeyPriorToolIDs].([]string); ok && len(priorIDs) > 0 {
		sb.WriteString("Tools used in previous phases (in agent/capability format):\n")
		for _, id := range priorIDs {
			fmt.Fprintf(&sb, "- %s\n", id)
		}
	}
	sb.WriteString("</phase_context>\n\n")

	// ORCH-014 fix: Same pattern as buildSelectionPrompt — inject domain rules before tool list.
	writeCustomInstructions(&sb, t.customInstructions)

	// Available tools — same compact format as buildSelectionPrompt
	sb.WriteString("<available_tools>\n")
	sb.WriteString(catalog)
	sb.WriteString("</available_tools>\n\n")

	// Inject conversation history for follow-up queries (same as buildSelectionPrompt).
	if enrichments := core.GetPipelineEnrichments(ctx); len(enrichments) > 0 {
		if convHistory, ok := enrichments[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				sb.WriteString("<conversation_history>\n")
				sb.WriteString(convStr)
				sb.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	// User request + selection guide + output format.
	// <selection_guide> is near the bottom (second U-curve high-attention zone)
	// and references context above it. A/B/C reasoning structure matches
	// buildSelectionPrompt's structured process, reframed for continuation.
	// <output_format> with concrete example at the very bottom serves as
	// the Gemini dual-anchor (Issue 6 Caveat 1).
	fmt.Fprintf(&sb, `<user_request>
%s
</user_request>

<selection_guide>
A. What parts of the original request still need to be addressed?
B. Did previous phase discoveries reveal a need for tool types absent from earlier phases?
   Example: Research discovered a visa requirement → select a visa-check tool.
C. Select ALL tools for remaining work — both tools reused from prior phases
   (e.g., same geocoding tool for new locations) and newly relevant tools.
D. If you cannot identify any new tools and the next phase appears to need only
   the tools already used, return the items from <phase_context> verbatim.
   Always return at least one tool — the planner will decide whether to actually
   invoke them or signal a different next step.
</selection_guide>

<output_format>
JSON array of tool identifiers using "agent_name/capability_name" format.
Example (new tools needed): ["weather-tool-v2/get_current_weather", "geocoding-tool/geocode_location"]
Example (prior-tools fallback): ["country-info-tool/get_country_info", "travel-advisory-tool/get_travel_advisory"]
</output_format>

JSON array:
`, request)

	return sb.String()
}

func formatCapabilitySummaryCatalog(summaries []CapabilitySummary) string {
	var catalog strings.Builder
	for _, summary := range summaries {
		fmt.Fprintf(&catalog, "- %s/%s: %s\n", summary.AgentName, summary.CapabilityName, summary.Summary)
	}
	return catalog.String()
}

// parseToolSelection extracts tool names from the LLM response.
func (t *TieredCapabilityProvider) parseToolSelection(response string) ([]string, error) {
	// Clean up response (handle markdown wrapping)
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	// Parse JSON array
	var tools []string
	if err := json.Unmarshal([]byte(response), &tools); err != nil {
		return nil, fmt.Errorf("failed to parse tool selection: %w (response: %s)", err, response)
	}

	if len(tools) == 0 {
		// Return the sentinel directly so callers can detect this specific
		// case via errors.Is(err, errNoToolsSelected) — used by the ORCH-018
		// Layer 3 defensive recovery in selectRelevantTools.
		return nil, errNoToolsSelected
	}

	return tools, nil
}

// validateAndFilterTools verifies selected tools exist in the catalog.
// Returns only valid tools and logs warnings for invalid selections.
// This prevents hallucinated tool names (a known issue per RAG-MCP research).
func (t *TieredCapabilityProvider) validateAndFilterTools(
	ctx context.Context,
	selectedTools []string,
	summaries []CapabilitySummary,
) []string {
	// Build lookup set of valid tool IDs
	validTools := make(map[string]bool)
	for _, s := range summaries {
		toolID := fmt.Sprintf("%s/%s", s.AgentName, s.CapabilityName)
		validTools[toolID] = true
	}

	// Filter to only valid tools
	var filtered []string
	var invalid []string
	for _, tool := range selectedTools {
		if validTools[tool] {
			filtered = append(filtered, tool)
		} else {
			invalid = append(invalid, tool)
		}
	}

	// Log any hallucinated tools (research shows this is common with many tools)
	if len(invalid) > 0 {
		t.logWarnWithContext(ctx, "LLM selected non-existent tools (hallucination)", map[string]interface{}{
			"invalid_tools": invalid,
			"valid_count":   len(filtered),
		})
	}

	return filtered
}

// truncateRequest truncates a request string for error messages.
// Helps keep error messages readable while providing context.
func truncateRequest(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
