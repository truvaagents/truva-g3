package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	viewerOverviewTextBytes = 1000
	viewerDetailReadTimeout = 5 * time.Second
	viewerHITLResponseBytes = 16 * 1024 * 1024
)

func validExecutionDetailSection(section string) bool {
	switch section {
	case "overview", "steps", "llm", "pre", "post", "hitl", "skills", "raw", "flow":
		return true
	default:
		return false
	}
}

// loadExecutionDetailRecord reads only the explicitly selected execution. The
// existing framework record and its storage representation remain unchanged.
func loadExecutionDetailRecord(parent context.Context, requestID string) (*StoredExecution, error) {
	execution, _, err := loadExecutionDetailRecordWithRaw(parent, requestID, false)
	return execution, err
}

func loadExecutionDetailRecordWithRaw(parent context.Context, requestID string, includeRaw bool) (*StoredExecution, json.RawMessage, error) {
	client, err := getExecutionDebugClient()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, viewerDetailReadTimeout)
	defer cancel()
	data, err := client.Get(ctx, viewerExecutionKeys.Record(requestID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil, fmt.Errorf("execution not found: %s: %w", requestID, orchestration.ErrExecutionRecordNotFound)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("execution read failed: %w", err)
	}
	execution, err := deserializeExecution(data)
	if err != nil || !includeRaw {
		return execution, nil, err
	}
	raw, err := executionDetailRawJSON(data)
	return execution, raw, err
}

// Preserve fields unknown to the Viewer's DTO and exact JSON number literals in
// the explicit raw view. This does not write or normalize the stored record.
func executionDetailRawJSON(data []byte) (json.RawMessage, error) {
	if len(data) == 0 {
		return nil, errors.New("empty execution data")
	}
	switch data[0] {
	case 0:
		data = data[1:]
	case 1:
		reader, err := gzip.NewReader(bytes.NewReader(data[1:]))
		if err != nil {
			return nil, err
		}
		decoded, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		data = decoded
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid execution JSON")
	}
	return json.RawMessage(data), nil
}

// buildExecutionDetailSection is a read-side projection, never a replacement
// for the authoritative execution or debug record. Each section is fetched by
// an explicit UI action. In particular, overview never touches optional stores.
func buildExecutionDetailSection(parent context.Context, execution *StoredExecution, section string, storedJSON ...json.RawMessage) map[string]interface{} {
	ctx, cancel := context.WithTimeout(parent, viewerDetailReadTimeout)
	defer cancel()
	projected := execution
	if section == "overview" {
		projected = projectExecutionOverview(execution)
	}
	// Timing depends on authoritative step metadata (including resume ownership),
	// which the lightweight graph intentionally omits from its payload projection.
	response := executionDetailHeader(execution)
	response["detail_level"] = section
	response["loaded_sections"] = []string{section}
	response["available_sections"] = []string{"overview", "steps", "llm", "pre", "post", "hitl", "skills", "raw", "flow"}
	response["payloads_deferred"] = section == "overview"
	if execution == nil {
		return response
	}

	switch section {
	case "overview":
		response["original_request"] = projected.OriginalRequest
		addExecutionGraphFields(response, projected)
	case "steps":
		addExecutionGraphFields(response, execution)
		response["metadata"] = execution.Metadata
	case "skills":
		response["skills"] = execution.Skills
	case "raw":
		if len(storedJSON) > 0 && len(storedJSON[0]) > 0 {
			// A JSON string transports the original text through browser parsing
			// without rounding large numeric literals or dropping unknown fields.
			response["raw_execution_json"] = string(storedJSON[0])
		} else if encoded, err := json.Marshal(execution); err == nil {
			response["raw_execution_json"] = string(encoded)
		}
	case "hitl":
		checkpoints, current, warnings := loadExecutionHITLSection(ctx, execution)
		response["hitl_checkpoints"] = nonNilSlice(checkpoints)
		response["checkpoint"] = execution.Checkpoint
		response["hitl_lifecycle"] = buildHITLLifecycle(execution, current, nil)
		if numbering := executionResumeStepNumbering(execution, current); numbering != nil {
			response["resume_step_numbering"] = numbering
		}
		response["has_hitl_data"] = len(checkpoints) > 0 || execution.Checkpoint != nil || execution.Interrupted || execution.Metadata["resume_checkpoint_id"] != ""
		if len(warnings) > 0 {
			response["section_warnings"] = warnings
		}
	case "llm", "pre", "post", "flow":
		interactions, warnings := loadExecutionLLMSection(ctx, execution.RequestID)
		allInteractions := interactions
		if section == "pre" || section == "post" {
			interactions = filterExecutionHookInteractions(interactions, section)
		}
		response["llm_interactions"] = nonNilSlice(interactions)
		response["has_llm_data"] = len(allInteractions) > 0
		response["llm_debug_summary"] = executionLLMSummary(allInteractions)
		extendExecutionDetailDuration(response, execution, allInteractions)
		if len(warnings) > 0 {
			response["section_warnings"] = warnings
		}
		if section == "pre" || section == "post" || section == "flow" {
			response["pipeline_hooks"] = filterExecutionPipelineHooks(execution.PipelineHooks, section)
		}
		if section == "post" || section == "flow" {
			response["final_response"] = execution.FinalResponse
			response["final_response_source"] = execution.FinalResponseSource
		}
		if section == "flow" {
			addExecutionGraphFields(response, execution)
			response["skills"] = execution.Skills
			response["metadata"] = execution.Metadata
			// A flow shows the checkpoint observed by this execution. Current
			// checkpoint state requires the explicit HITL section/owning-agent read.
			checkpoints := []HITLCheckpoint{}
			if execution.Checkpoint != nil {
				checkpoints = append(checkpoints, *execution.Checkpoint)
			}
			response["hitl_checkpoints"] = checkpoints
			response["has_hitl_data"] = len(checkpoints) > 0 || execution.Interrupted || execution.Metadata["resume_checkpoint_id"] != ""
			response["hitl_lifecycle"] = buildHITLLifecycle(execution, nil, nil)
		}
	}
	return response
}

func executionDetailHeader(execution *StoredExecution) map[string]interface{} {
	if execution == nil {
		return map[string]interface{}{}
	}
	summary := summarizeExecution(execution)
	header := map[string]interface{}{
		"request_id": execution.RequestID, "original_request_id": execution.OriginalRequestID,
		"parent_request_id": execution.ParentRequestID, "parent_step_id": execution.ParentStepID,
		"conversation_id": execution.Metadata[orchestration.MetadataConversationID],
		"trace_id":        execution.TraceID, "agent_name": execution.AgentName,
		"original_request": execution.OriginalRequest, "created_at": execution.CreatedAt,
		"lifecycle": execution.Lifecycle, "record_revision": execution.RecordRevision,
		"success": summary.Success, "interrupted": execution.Interrupted,
		"total_duration_ms": summary.TotalDurationMs, "wall_clock_duration_ms": summary.WallClockDurationMs,
		"duration_source": summary.DurationSource, "phase_count": execution.PhaseCount,
		"forced_terminal": execution.ForcedTerminal,
	}
	if numbering := executionResumeStepNumbering(execution, nil); numbering != nil {
		header["resume_step_numbering"] = numbering
	}
	return header
}

// This small view fact can survive section changes without retaining a live
// checkpoint payload or fetching one eagerly. A nil count means unobserved,
// not zero prior work. Only the checkpoint that started this resume is relevant;
// a later interruption snapshot can include work from this invocation itself.
type executionResumeNumbering struct {
	CheckpointID       string `json:"checkpoint_id"`
	CompletedStepCount *int   `json:"completed_step_count"`
	DisplayStepOffset  *int   `json:"display_step_offset"`
	Source             string `json:"source,omitempty"`
}

func executionResumeStepNumbering(execution *StoredExecution, current []HITLCheckpoint) *executionResumeNumbering {
	if execution == nil || execution.Metadata["resume_checkpoint_id"] == "" {
		return nil
	}
	numbering := &executionResumeNumbering{CheckpointID: execution.Metadata["resume_checkpoint_id"]}
	checkpoint := checkpointByID(current, numbering.CheckpointID)
	source := "agent_checkpoint_api"
	if checkpoint == nil || checkpoint.CompletedSteps == nil {
		checkpoint = execution.Checkpoint
		source = "stored_checkpoint"
	}
	if checkpoint == nil || checkpoint.CheckpointID != numbering.CheckpointID || checkpoint.CompletedSteps == nil {
		return numbering
	}
	stepIDs := make(map[string]struct{}, len(checkpoint.CompletedSteps))
	for _, step := range checkpoint.CompletedSteps {
		if step.StepID != "" {
			stepIDs[step.StepID] = struct{}{}
		}
	}
	count := len(stepIDs)
	// A resumed checkpoint retains its entire interrupted plan, including the
	// completed steps the executor restores. Those visible steps already occupy
	// an index in the UI and must not also be added as an offset. Only completed
	// steps outside the displayed plans precede its first visible step.
	plans := execution.PhasePlans
	if len(plans) <= 1 {
		plans = []*RoutingPlan{execution.Plan}
	}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		for _, step := range plan.Steps {
			delete(stepIDs, step.StepID)
		}
	}
	offset := len(stepIDs)
	numbering.CompletedStepCount = &count
	numbering.DisplayStepOffset = &offset
	numbering.Source = source
	return numbering
}

// Rich sections already loaded these request-local interactions. Reuse their
// timestamps without adding an optional-store read to the overview or list.
func extendExecutionDetailDuration(response map[string]interface{}, execution *StoredExecution, interactions []orchestration.LLMInteraction) {
	if len(interactions) == 0 {
		return
	}
	start, end := executionObservationWindow(execution)
	start, end = extendWindowWithLLMInteractions(start, end, interactions)
	current, _ := response["wall_clock_duration_ms"].(int64)
	source, _ := response["duration_source"].(string)
	response["wall_clock_duration_ms"], response["duration_source"] = preferDurationCandidate(
		current, source, observedDurationMilliseconds(start, end), "llm_and_step_time_envelope",
	)
}

func addExecutionGraphFields(response map[string]interface{}, execution *StoredExecution) {
	response["plan"] = execution.Plan
	response["result"] = execution.Result
	response["checkpoint"] = execution.Checkpoint
	response["phase_plans"] = execution.PhasePlans
	response["dag"] = computeDAG(execution)
}

func projectExecutionOverview(execution *StoredExecution) *StoredExecution {
	if execution == nil {
		return nil
	}
	projected := *execution
	projected.OriginalRequest = executionOverviewPreview(execution.OriginalRequest)
	projected.Plan = projectOverviewPlan(execution.Plan)
	projected.PhasePlans = make([]*RoutingPlan, len(execution.PhasePlans))
	for index, plan := range execution.PhasePlans {
		projected.PhasePlans[index] = projectOverviewPlan(plan)
	}
	if execution.Result != nil {
		result := *execution.Result
		result.Steps = make([]StepResult, len(execution.Result.Steps))
		for index, step := range execution.Result.Steps {
			result.Steps[index] = projectOverviewResult(step)
		}
		result.Metadata = nil
		if reason, ok := execution.Result.Metadata["termination_reason"].(string); ok {
			result.Metadata = map[string]interface{}{"termination_reason": executionOverviewPreview(reason)}
		}
		projected.Result = &result
	}
	projected.Checkpoint = projectOverviewCheckpoint(execution.Checkpoint)
	projected.Skills = nil
	projected.PipelineHooks = nil
	projected.FinalResponse = nil
	return &projected
}

func projectOverviewPlan(plan *RoutingPlan) *RoutingPlan {
	if plan == nil {
		return nil
	}
	projected := *plan
	projected.OriginalRequest = executionOverviewPreview(plan.OriginalRequest)
	projected.Rationale = ""
	projected.ContinuationNote = executionOverviewPreview(plan.ContinuationNote)
	projected.Steps = make([]RoutingStep, len(plan.Steps))
	for index, step := range plan.Steps {
		projected.Steps[index] = projectOverviewStep(step)
	}
	return &projected
}

func projectOverviewStep(step RoutingStep) RoutingStep {
	projected := step
	projected.Parameters = nil
	projected.Instruction = executionOverviewPreview(step.Instruction)
	projected.Description = executionOverviewPreview(step.Description)
	projected.ExpectedOutput = ""
	projected.Metadata = map[string]interface{}{"viewer_payloads_deferred": true}
	if capability, ok := step.Metadata["capability"].(string); ok {
		projected.Metadata["capability"] = capability
	}
	return projected
}

func projectOverviewResult(step StepResult) StepResult {
	projected := step
	projected.Parameters = nil
	projected.Instruction = executionOverviewPreview(step.Instruction)
	projected.Response = nil
	projected.ResponseText = ""
	projected.Error = executionOverviewPreview(step.Error)
	projected.SkipReason = executionOverviewPreview(step.SkipReason)
	projected.Dispatches = nil
	projected.ToolError = nil
	projected.Metadata = map[string]interface{}{"viewer_payloads_deferred": true}
	return projected
}

func projectOverviewCheckpoint(checkpoint *HITLCheckpoint) *HITLCheckpoint {
	if checkpoint == nil {
		return nil
	}
	projected := *checkpoint
	projected.Plan = projectOverviewPlan(checkpoint.Plan)
	projected.CompletedSteps = make([]StepResult, len(checkpoint.CompletedSteps))
	for index, step := range checkpoint.CompletedSteps {
		projected.CompletedSteps[index] = projectOverviewResult(step)
	}
	projected.StepResults = make(map[string]*StepResult, len(checkpoint.StepResults))
	for id, step := range checkpoint.StepResults {
		if step != nil {
			value := projectOverviewResult(*step)
			projected.StepResults[id] = &value
		}
	}
	if checkpoint.CurrentStep != nil {
		step := projectOverviewStep(*checkpoint.CurrentStep)
		projected.CurrentStep = &step
	}
	if checkpoint.CurrentStepResult != nil {
		step := projectOverviewResult(*checkpoint.CurrentStepResult)
		projected.CurrentStepResult = &step
	}
	projected.Decision = nil
	projected.ResolvedParameters = nil
	projected.UserContext = nil
	projected.OriginalRequest = executionOverviewPreview(checkpoint.OriginalRequest)
	return &projected
}

func executionOverviewPreview(value string) string {
	if len(value) <= viewerOverviewTextBytes {
		return value
	}
	end := viewerOverviewTextBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}

func loadExecutionLLMSection(ctx context.Context, requestID string) ([]orchestration.LLMInteraction, []string) {
	if useMock {
		return nil, nil
	}
	store, err := getLLMDebugStore()
	if err != nil {
		logExecutionSectionFailure(ctx, "llm", "store_unavailable")
		return nil, []string{"LLM debug data is currently unavailable. Retry this section to check again."}
	}
	record, err := store.GetRecord(ctx, requestID)
	if err != nil {
		if errors.Is(err, orchestration.ErrLLMDebugRecordNotFound) {
			return nil, nil
		}
		logExecutionSectionFailure(ctx, "llm", "read_failed")
		return nil, []string{"LLM debug data could not be loaded. Retry this section to check again."}
	}
	if record == nil {
		return nil, nil
	}
	return orchestration.DedupeLLMInteractions(record.Interactions), nil
}

func executionLLMSummary(interactions []orchestration.LLMInteraction) *LLMDebugSummary {
	if len(interactions) == 0 {
		return nil
	}
	summary := &LLMDebugSummary{TotalCalls: len(interactions), ProviderBreakdown: make(map[string]int)}
	for _, interaction := range interactions {
		summary.TotalTokensIn += interaction.PromptTokens
		summary.TotalTokensOut += interaction.CompletionTokens
		summary.TotalDurationMs += interaction.DurationMs
		provider := interaction.Provider
		if provider == "" {
			provider = "unknown"
		}
		summary.ProviderBreakdown[provider]++
	}
	return summary
}

func filterExecutionHookInteractions(interactions []orchestration.LLMInteraction, phase string) []orchestration.LLMInteraction {
	anyPhaseSet := false
	latestSynthesis := -1
	for index, interaction := range interactions {
		anyPhaseSet = anyPhaseSet || interaction.HookPhase != ""
		if interaction.Type == "synthesis" || interaction.Type == "synthesis_streaming" {
			latestSynthesis = index
		}
	}
	filtered := make([]orchestration.LLMInteraction, 0)
	for index, interaction := range interactions {
		matches := interaction.HookPhase == phase
		if !anyPhaseSet {
			pre := strings.HasPrefix(interaction.Type, "user_memory_recall_") || interaction.Type == "user_memory_enrichment_injected" || interaction.Type == "conversation_history_prepare" || interaction.Type == "conversation_history_compaction" || interaction.Type == "activity_compaction" || interaction.Type == "activity_compaction_incremental"
			post := strings.HasPrefix(interaction.Type, "user_memory_") && !pre || interaction.Type == "event_summarization"
			matches = phase == "pre" && pre || phase == "post" && post
		}
		// The post-hook response view compares against the last synthesis.
		// Include this one dependency so omitted evidence cannot look like an
		// empty pre-hook answer and falsely suggest that the application changed it.
		if phase == "post" && index == latestSynthesis {
			matches = true
		}
		if matches {
			filtered = append(filtered, interaction)
		}
	}
	return filtered
}

func filterExecutionPipelineHooks(hooks []orchestration.PipelineHookExecution, section string) []orchestration.PipelineHookExecution {
	filtered := make([]orchestration.PipelineHookExecution, 0)
	for _, hook := range hooks {
		pre := hook.Phase == orchestration.PipelineHookPhaseBeforePlanning || hook.Phase == orchestration.PipelineHookPhaseAfterPlanning
		post := hook.Phase == orchestration.PipelineHookPhaseAfterExecution || hook.Phase == orchestration.PipelineHookPhaseAfterSynthesis
		if section == "flow" || section == "pre" && pre || section == "post" && post {
			filtered = append(filtered, hook)
		}
	}
	return filtered
}

func loadExecutionHITLSection(ctx context.Context, execution *StoredExecution) ([]HITLCheckpoint, []HITLCheckpoint, []string) {
	snapshots := make([]HITLCheckpoint, 0)
	if execution.Checkpoint != nil {
		snapshots = append(snapshots, *execution.Checkpoint)
	}
	if useMock || execution.AgentName == "" {
		return snapshots, nil, nil
	}
	// The record names its owner. An explicit checkpoint view queries that owner
	// only; it does not fan out over every registered agent or scan executions.
	address, err := getAgentAddress(execution.AgentName)
	if err != nil {
		return snapshots, nil, []string{"The owning agent is unavailable; showing the recorded checkpoint snapshot."}
	}
	requestIDs := []string{execution.RequestID}
	if execution.OriginalRequestID != "" && execution.OriginalRequestID != execution.RequestID {
		requestIDs = append(requestIDs, execution.OriginalRequestID)
	}
	var warnings []string
	var current []HITLCheckpoint
	for _, requestID := range requestIDs {
		resp, err := doHITLGet(ctx, address+"/hitl/checkpoints?request_id="+url.QueryEscape(requestID))
		if err != nil {
			warnings = append(warnings, "Current checkpoint state could not be loaded from the owning agent.")
			break
		}
		var body struct {
			Checkpoints []HITLCheckpoint `json:"checkpoints"`
		}
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, viewerHITLResponseBytes)).Decode(&body)
		}
		closeHTTPBody(resp.Body)
		if resp.StatusCode != http.StatusOK || err != nil {
			warnings = append(warnings, "Current checkpoint state could not be loaded from the owning agent.")
			continue
		}
		current = append(current, body.Checkpoints...)
	}
	current = dedupeHITLCheckpoints(current)
	// The list endpoint only returns pending checkpoints. Resolved approvals and
	// resume checkpoints require exact reads, still confined to the recorded owner
	// and the same request deadline. There are at most two recorded checkpoint IDs.
	checkpointIDs := []string{execution.Metadata["resume_checkpoint_id"]}
	if execution.Checkpoint != nil {
		checkpointIDs = append(checkpointIDs, execution.Checkpoint.CheckpointID)
	}
	seen := make(map[string]bool, len(checkpointIDs))
	for _, checkpointID := range checkpointIDs {
		if checkpointID == "" || seen[checkpointID] || checkpointByID(current, checkpointID) != nil {
			continue
		}
		seen[checkpointID] = true
		if ctx.Err() != nil {
			warnings = append(warnings, "Current checkpoint state could not be loaded from the owning agent.")
			break
		}
		resp, err := doHITLGet(ctx, address+"/hitl/checkpoints/"+url.PathEscape(checkpointID))
		if err != nil {
			warnings = append(warnings, "Current checkpoint state could not be loaded from the owning agent.")
			continue
		}
		var checkpoint HITLCheckpoint
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, viewerHITLResponseBytes)).Decode(&checkpoint)
		}
		closeHTTPBody(resp.Body)
		if resp.StatusCode != http.StatusOK || err != nil || checkpoint.CheckpointID != checkpointID {
			warnings = append(warnings, "Current checkpoint state could not be loaded from the owning agent.")
			continue
		}
		current = append(current, checkpoint)
	}
	// API observations take precedence for the display list. Keep the immutable
	// snapshot separate so lifecycle provenance cannot call an old snapshot live.
	checkpoints := dedupeHITLCheckpoints(append(append([]HITLCheckpoint(nil), current...), snapshots...))
	return checkpoints, current, warnings
}

func logExecutionSectionFailure(ctx context.Context, section, reason string) {
	if viewerLogger != nil {
		fields := map[string]interface{}{
			"operation": "viewer_execution_detail",
			"section":   section, "status": "unavailable", "error_type": "backend_read",
			"error": "requested execution detail could not be loaded", "reason": reason,
		}
		if requestID := core.GetRequestID(ctx); requestID != "" {
			fields["request_id"] = requestID
		}
		viewerLogger.WarnWithContext(ctx, "Execution detail section unavailable", fields)
	}
}
