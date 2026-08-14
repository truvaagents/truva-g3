package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type responseDelivery uint8

const (
	deliverBuffered responseDelivery = iota
	deliverNativeStream
	deliverSimulatedStream
)

type requestRunInput struct {
	Request                string
	Metadata               map[string]interface{}
	Delivery               responseDelivery
	DeliveryFallbackReason string
	Callback               core.StreamCallback
}

type responseDeliveryState struct {
	ChunksDelivered int
	StreamCompleted bool
	PartialContent  bool
	StepResults     []StepResult
	FinishReason    string
}

type requestRunResult struct {
	Response OrchestratorResponse
	Delivery responseDeliveryState
}

type requestCorrelation struct {
	RequestID         string
	OriginalRequestID string
	ConversationID    string
}

type phaseRunState struct {
	Result             *phaseLoopResult
	LastPreparation    boundaryPreparation
	PreparationHistory []orchestrationBoundary
}

type orchestrationBoundary uint8

const (
	boundaryInitialPlanning orchestrationBoundary = iota
	boundaryContinuationPlanning
	boundaryRegeneration
	boundarySynthesis
	boundaryResume
)

// boundaryPreparation is a framework-owned, immutable view of the lifecycle
// facts available at a named orchestration boundary. Feature-specific state
// must remain outside this value and attach through a future narrow
// contributor seam.
type boundaryPreparation struct {
	Boundary orchestrationBoundary
	Snapshot executionRunSnapshot
}

type boundaryPreparer func(context.Context, orchestrationBoundary) (context.Context, error)
type boundaryPreparerContextKey struct{}
type executionRunSnapshotContextKey struct{}

// boundaryPreparationError distinguishes a lifecycle-boundary failure from an
// ordinary planner failure. Callers may preserve planner retries without
// retrying work that failed before the model boundary was reached.
type boundaryPreparationError struct {
	boundary orchestrationBoundary
	cause    error
}

func (err *boundaryPreparationError) Error() string {
	if err == nil || err.cause == nil {
		return "orchestration boundary preparation failed"
	}
	return err.cause.Error()
}

func (err *boundaryPreparationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func withBoundaryPreparer(ctx context.Context, prepare boundaryPreparer) context.Context {
	return context.WithValue(ctx, boundaryPreparerContextKey{}, prepare)
}

func withExecutionRunSnapshot(ctx context.Context, snapshot executionRunSnapshot) context.Context {
	return context.WithValue(ctx, executionRunSnapshotContextKey{}, snapshot)
}

func prepareOrchestrationBoundary(ctx context.Context, boundary orchestrationBoundary) (context.Context, error) {
	prepare, _ := ctx.Value(boundaryPreparerContextKey{}).(boundaryPreparer)
	if prepare == nil {
		return ctx, nil
	}
	prepared, err := prepare(ctx, boundary)
	if err != nil {
		return prepared, &boundaryPreparationError{boundary: boundary, cause: err}
	}
	return prepared, nil
}

func executionRunSnapshotFromContext(ctx context.Context) executionRunSnapshot {
	snapshot, _ := ctx.Value(executionRunSnapshotContextKey{}).(executionRunSnapshot)
	return snapshot
}

// phaseCoordinator owns the named phase lifecycle. The implementation remains
// delegated to the characterized phase loop while extraction proceeds along
// lifecycle facts rather than feature-specific callbacks.
type phaseCoordinator struct {
	orchestrator *AIOrchestrator
}

func (c phaseCoordinator) Run(
	ctx context.Context,
	state *executionRunState,
	progress phaseProgressFn,
) (*phaseLoopResult, error) {
	phaseProjections := make(map[int]*skillPromptProjection)
	ctx = withBoundaryPreparer(ctx, func(boundaryCtx context.Context, boundary orchestrationBoundary) (context.Context, error) {
		preparation, err := c.prepareBoundary(boundaryCtx, boundary, state)
		if err != nil {
			return boundaryCtx, err
		}
		if c.orchestrator.skillRuntime == nil || isSkillFreeCheckpointResume(boundaryCtx) {
			return boundaryCtx, nil
		}
		skillState, found := state.skillState()
		if !found {
			return boundaryCtx, newSkillDomainError(ErrSkillIntegrity, "read execution skill state", SkillRef{})
		}
		switch boundary {
		case boundaryInitialPlanning:
			boundaryCtx, skillState, err = c.orchestrator.skillRuntime.prepareInitialBoundary(
				boundaryCtx,
				skillState,
				state.Input.Request,
				state.Pipeline.Enrichments,
				max(preparation.Snapshot.PhaseNumber, 1),
			)
			if projection, found := skillPromptProjectionFromContext(boundaryCtx); found {
				phaseProjections[max(preparation.Snapshot.PhaseNumber, 1)] = projection
			}
		case boundaryContinuationPlanning:
			var projection *skillPromptProjection
			boundaryCtx, skillState, projection, err = c.orchestrator.skillRuntime.prepareContinuationBoundary(
				boundaryCtx, skillState, state.Input.Request, state.Pipeline.Enrichments,
				preparation.Snapshot, SkillBoundaryContinuation,
			)
			if projection != nil {
				phaseProjections[max(preparation.Snapshot.PhaseNumber, 1)] = projection
			}
		case boundaryRegeneration:
			projection := phaseProjections[max(preparation.Snapshot.PhaseNumber, 1)]
			if projection == nil {
				return boundaryCtx, newSkillDomainError(ErrSkillIntegrity, "reuse regeneration projection", SkillRef{})
			}
			boundaryCtx, err = c.orchestrator.skillRuntime.reuseSkillProjection(
				boundaryCtx, &skillState, projection, SkillBoundaryRegeneration,
			)
		case boundaryResume:
			var projection *skillPromptProjection
			boundaryCtx, skillState, projection, err = c.orchestrator.skillRuntime.prepareContinuationBoundary(
				boundaryCtx, skillState, state.Input.Request, state.Pipeline.Enrichments,
				preparation.Snapshot, SkillBoundaryResume,
			)
			if projection != nil {
				phaseProjections[max(preparation.Snapshot.PhaseNumber, 1)] = projection
			}
		case boundarySynthesis:
			boundaryCtx, skillState, _, err = c.orchestrator.skillRuntime.prepareSynthesisBoundary(
				boundaryCtx, skillState, state.Input.Request, preparation.Snapshot,
			)
		}
		state.setSkillState(skillState)
		if holder, ok := skillExecutionHolderFromContext(boundaryCtx); ok {
			holder.Store(skillState)
		}
		debug := skillState.Debug
		state.Debug.Skills = &debug
		boundaryCtx = withSkillExecutionState(boundaryCtx, skillState)
		if err != nil {
			return boundaryCtx, err
		}
		return boundaryCtx, nil
	})
	state.Context = ctx
	result, err := c.orchestrator.executePhaseLoop(
		ctx,
		state.Input.Request,
		state.Correlation.RequestID,
		state.StartedAt,
		state.Span,
		state.Pipeline,
		progress,
	)
	var preparationErr *boundaryPreparationError
	if err != nil && errors.As(err, &preparationErr) {
		// A boundary failure occurs before the planner/executor can produce a
		// result. Persist the request-local debug snapshot that the boundary
		// contributor captured so the failure is still diagnosable.
		c.orchestrator.storeExecutionAsync(
			state.Context,
			state.Input.Request,
			state.Correlation.RequestID,
			nil,
			nil,
			nil,
		)
	}
	return result, err
}

func (c phaseCoordinator) prepareBoundary(
	ctx context.Context,
	boundary orchestrationBoundary,
	state *executionRunState,
) (boundaryPreparation, error) {
	if err := ctx.Err(); err != nil {
		return boundaryPreparation{}, err
	}
	preparation := boundaryPreparation{
		Boundary: boundary,
		Snapshot: executionRunSnapshotFromContext(ctx),
	}
	state.Phase.LastPreparation = preparation
	state.Phase.PreparationHistory = append(state.Phase.PreparationHistory, boundary)
	return preparation, nil
}

// synthesisCoordinator owns the synthesis lifecycle boundary and delegates
// only the response-delivery choice. It is deliberately provider- and
// feature-neutral.
type synthesisCoordinator struct {
	orchestrator *AIOrchestrator
}

func (c synthesisCoordinator) Run(state *executionRunState) (*requestRunResult, error) {
	state.Context = withExecutionRunSnapshot(state.Context, state.Snapshot())
	var err error
	state.Context, err = prepareOrchestrationBoundary(state.Context, boundarySynthesis)
	if err != nil {
		return nil, err
	}
	if c.orchestrator.skillRuntime != nil && state.Phase.Result != nil {
		c.orchestrator.storeExecutionAsync(
			state.Context,
			state.Input.Request,
			state.Correlation.RequestID,
			state.Phase.Result.LastPlan,
			state.Phase.Result.CombinedResult,
			nil,
		)
	}

	switch state.Input.Delivery {
	case deliverBuffered:
		response, err := c.orchestrator.synthesizeBuffered(state)
		if response == nil {
			return nil, err
		}
		return &requestRunResult{Response: *response}, err
	case deliverNativeStream:
		response, err := c.orchestrator.synthesizeNativeStreaming(state)
		if response == nil {
			return nil, err
		}
		return &requestRunResult{
			Response: response.OrchestratorResponse,
			Delivery: responseDeliveryState{
				ChunksDelivered: response.ChunksDelivered,
				StreamCompleted: response.StreamCompleted,
				PartialContent:  response.PartialContent,
				StepResults:     response.StepResults,
				FinishReason:    response.FinishReason,
			},
		}, err
	case deliverSimulatedStream:
		response, err := c.orchestrator.synthesizeBuffered(state)
		if response == nil {
			return nil, err
		}
		deliveryState, deliveryErr := deliverUTF8Chunks(response.Response, state.Input.Callback)
		if deliveryErr != nil {
			response.Errors = append(response.Errors, "stream callback stopped delivery")
		}
		return &requestRunResult{Response: *response, Delivery: deliveryState}, err
	default:
		return nil, ErrInvalidOrchestratorConfig
	}
}

type usageState struct {
	Accumulator *core.AggregatedTokenUsage
}

type executionDebugState struct {
	Skills *SkillExecutionDebug
}

// executionRunState is the request-local lifecycle owner. Feature-specific
// state is typed and request-scoped; provider clients and content bodies never
// attach to this aggregate.
type executionRunState struct {
	Input            requestRunInput
	Context          context.Context
	StartedAt        time.Time
	Correlation      requestCorrelation
	Pipeline         *core.PipelineContext
	Phase            phaseRunState
	Usage            usageState
	Debug            executionDebugState
	SkillState       *SkillExecutionState
	Recorder         *executionRecorder
	Span             core.Span
	completionLogged bool
	completionReason string
	spanFailed       bool
}

func (s *executionRunState) setSkillState(state SkillExecutionState) {
	if s == nil {
		return
	}
	copy := cloneSkillExecutionState(state)
	s.SkillState = &copy
}

func (s *executionRunState) skillState() (SkillExecutionState, bool) {
	if s == nil || s.SkillState == nil {
		return SkillExecutionState{}, false
	}
	return cloneSkillExecutionState(*s.SkillState), true
}

func (s *executionRunState) Snapshot() executionRunSnapshot {
	if s == nil || s.Phase.Result == nil || s.Phase.Result.CombinedResult == nil {
		return executionRunSnapshot{}
	}
	results := make(map[string]*StepResult, len(s.Phase.Result.CombinedResult.Steps))
	ids := make([]string, 0, len(s.Phase.Result.CombinedResult.Steps))
	for index := range s.Phase.Result.CombinedResult.Steps {
		step := s.Phase.Result.CombinedResult.Steps[index]
		stepCopy := step
		results[step.StepID] = &stepCopy
		ids = append(ids, step.StepID)
	}
	return newExecutionRunSnapshot(s.Phase.Result.CombinedResult.PhaseCount, results, ids, "")
}

// supportsNativeStreaming preserves dynamic capability checks exposed by
// wrappers while also accepting request-aware streaming-only clients.
func supportsNativeStreaming(client core.AIClient) bool {
	if legacy, ok := client.(core.StreamingAIClient); ok {
		return legacy.SupportsStreaming()
	}
	_, requestAware := client.(core.StreamingAIRequestClient)
	return requestAware
}

// ProcessRequest is the buffered adapter over the shared request runner.
func (o *AIOrchestrator) ProcessRequest(
	ctx context.Context,
	request string,
	metadata map[string]interface{},
) (*OrchestratorResponse, error) {
	result, err := o.runRequest(ctx, requestRunInput{
		Request: request, Metadata: metadata, Delivery: deliverBuffered,
	})
	if result == nil {
		return nil, err
	}
	response := result.Response
	return &response, err
}

// ProcessRequestStreaming selects delivery capability before creating request
// correlation. Simulated delivery therefore does not recursively create a
// second request lifecycle.
func (o *AIOrchestrator) ProcessRequestStreaming(
	ctx context.Context,
	request string,
	metadata map[string]interface{},
	callback core.StreamCallback,
) (*StreamingOrchestratorResponse, error) {
	delivery := deliverSimulatedStream
	fallbackReason := "strategy_non_llm"
	if o.config.SynthesisStrategy == StrategyLLM && supportsNativeStreaming(o.aiClient) {
		delivery = deliverNativeStream
		fallbackReason = ""
	} else if o.config.SynthesisStrategy == StrategyLLM {
		fallbackReason = "client_streaming_unsupported"
	}
	result, err := o.runRequest(ctx, requestRunInput{
		Request: request, Metadata: metadata, Delivery: delivery,
		DeliveryFallbackReason: fallbackReason, Callback: callback,
	})
	if result == nil {
		return nil, err
	}
	return &StreamingOrchestratorResponse{
		OrchestratorResponse: result.Response,
		ChunksDelivered:      result.Delivery.ChunksDelivered,
		StreamCompleted:      result.Delivery.StreamCompleted,
		PartialContent:       result.Delivery.PartialContent,
		StepResults:          result.Delivery.StepResults,
		FinishReason:         result.Delivery.FinishReason,
	}, err
}

func (o *AIOrchestrator) runRequest(ctx context.Context, input requestRunInput) (runResult *requestRunResult, runErr error) {
	state, err := o.beginRequestRun(ctx, input)
	if err != nil {
		return nil, err
	}
	defer state.Span.End()
	defer o.releaseExecutionRecorder(state.Correlation.RequestID)
	defer func() {
		if runErr != nil && !IsInterrupted(runErr) {
			recordRunSpanFailure(state)
		}
		if !state.completionLogged {
			o.completeFailedRun(state, runErr)
		}
	}()

	gate := newPipelineGate(nil, false)
	if o.skillRuntime != nil {
		var skillState SkillExecutionState
		var cacheContext SkillCacheContext
		var pinErr error
		skillRuntimeActive := !isSkillFreeCheckpointResume(state.Context)
		if checkpointState, priorCacheContext, ok := checkpointSkillStateFromContext(state.Context); ok &&
			!checkpointHasEffectiveSkillState(checkpointState, priorCacheContext) {
			// Explicitly empty compatibility snapshots are causal evidence that
			// this suspended execution was skill-free. Do not attach bindings that
			// were added after the checkpoint was created.
			skillRuntimeActive = false
			state.Context = withSkillFreeCheckpointResume(state.Context)
		} else if ok {
			skillState, cacheContext, pinErr = o.skillRuntime.ResumeCandidates(
				state.Context, checkpointState, priorCacheContext,
			)
		} else if skillRuntimeActive {
			skillState, cacheContext, pinErr = o.skillRuntime.PinCandidates(state.Context)
		}
		if skillRuntimeActive {
			if pinErr != nil {
				state.completionReason = "skill_candidate_pinning_failed"
				if skillState.Pinned != nil {
					holder := newSkillExecutionStateHolder(skillState, cacheContext)
					state.Context = withSkillExecutionHolder(state.Context, holder)
					state.Context = withSkillExecutionState(state.Context, skillState)
					state.setSkillState(skillState)
					debug := skillState.Debug
					state.Debug.Skills = &debug
					o.storeExecutionAsync(
						state.Context, state.Input.Request, state.Correlation.RequestID,
						nil, nil, nil,
					)
				}
				return nil, pinErr
			}
			holder := newSkillExecutionStateHolder(skillState, cacheContext)
			state.Context = withSkillExecutionHolder(state.Context, holder)
			state.Context = withSkillExecutionState(state.Context, skillState)
			state.setSkillState(skillState)
			debug := skillState.Debug
			state.Debug.Skills = &debug
			gate = newPipelineGate(
				map[string]string{reservedSkillCacheDimension: cacheContext.Fingerprint},
				!cacheContext.ResponseCacheEligible,
			)
			// Persist request-start evidence before hooks can short-circuit the run.
			o.storeExecutionAsync(
				state.Context, state.Input.Request, state.Correlation.RequestID,
				nil, nil, nil,
			)
		}
	} else if checkpointState, priorCacheContext, ok := checkpointSkillStateFromContext(state.Context); ok &&
		checkpointHasEffectiveSkillState(checkpointState, priorCacheContext) {
		state.completionReason = "skill_binding_revoked"
		diagnostic := SkillDiagnostic{
			Code: "skill_binding_revoked", Boundary: SkillBoundaryResume,
			Action: "resume_failed",
		}
		checkpointState.Diagnostics = append(checkpointState.Diagnostics, diagnostic)
		checkpointState.Debug.Diagnostics = append(checkpointState.Debug.Diagnostics, diagnostic)
		cacheContext := SkillCacheContext{}
		if priorCacheContext != nil {
			cacheContext = *priorCacheContext
		}
		holder := newSkillExecutionStateHolder(checkpointState, cacheContext)
		state.Context = withSkillExecutionHolder(state.Context, holder)
		state.Context = withSkillExecutionState(state.Context, checkpointState)
		state.setSkillState(checkpointState)
		debug := checkpointState.Debug
		state.Debug.Skills = &debug
		o.storeExecutionAsync(
			state.Context, state.Input.Request, state.Correlation.RequestID,
			nil, nil, nil,
		)
		return nil, newSkillDomainError(ErrSkillUnavailable, "resume with skills disabled", SkillRef{})
	}
	decision, err := o.runBeforePlanningHooks(
		state.Context,
		state.Pipeline,
		gate,
		reservedSkillCacheDimension,
	)
	if err != nil {
		state.completionReason = "before_planning_failed"
		return nil, fmt.Errorf("before-planning pipeline contract: %w", err)
	}
	if decision != nil {
		totalUsage, usageByPhase := state.Usage.Accumulator.Snapshot()
		response := o.buildShortCircuitResponse(
			state.Correlation.RequestID,
			input.Request,
			state.Pipeline.Metadata,
			state.Pipeline,
			decision.shortCircuit,
			state.StartedAt,
			&totalUsage,
			usageByPhase,
		)
		if input.Delivery == deliverBuffered {
			result := &requestRunResult{Response: *response}
			o.completeRun(state, result)
			return result, nil
		}
		deliveryState, callbackErr := deliverUTF8Chunks(response.Response, input.Callback)
		if callbackErr != nil {
			response.Errors = append(response.Errors, "stream callback stopped delivery")
		}
		result := &requestRunResult{Response: *response, Delivery: deliveryState}
		o.completeRun(state, result)
		return result, nil
	}
	state.Context = core.WithPipelineEnrichments(state.Context, state.Pipeline.Enrichments)

	var progress phaseProgressFn
	if input.Delivery == deliverNativeStream {
		progress = func(phaseNumber, stepsInPhase int) {
			_ = input.Callback(core.StreamChunk{
				Content: fmt.Sprintf("Phase %d complete (%d steps). Planning next phase...", phaseNumber, stepsInPhase),
				Metadata: map[string]interface{}{
					"type": "phase_complete", "phase": phaseNumber, "terminal": false,
				},
			})
		}
	}
	coordinator := phaseCoordinator{orchestrator: o}
	state.Phase.Result, err = coordinator.Run(state.Context, state, progress)
	if err != nil {
		state.completionReason = "execution_failed"
		if IsInterrupted(err) {
			state.completionReason = "interrupted"
		}
		return nil, err
	}

	o.runAfterExecutionHooks(state.Context, state.Pipeline, state.Phase.Result.CombinedResult)
	o.markRunSynthesizing(state)

	result, err := (synthesisCoordinator{orchestrator: o}).Run(state)
	if result == nil {
		state.completionReason = "synthesis_failed"
		return nil, err
	}
	o.completeRun(state, result)
	return result, err
}

func checkpointHasEffectiveSkillState(
	state SkillExecutionState,
	cacheContext *SkillCacheContext,
) bool {
	if cacheContext != nil &&
		(cacheContext.Fingerprint != "" || cacheContext.ResponseCacheEligible) {
		return true
	}
	if len(state.ActiveSkills) != 0 || len(state.UnavailableContent) != 0 ||
		len(state.ResourceSelections) != 0 || len(state.Diagnostics) != 0 {
		return true
	}
	if state.Debug.BindingSource != "" || state.Debug.BindingFingerprint != "" ||
		state.Debug.BudgetFingerprint != "" || state.Debug.CacheFingerprint != "" ||
		state.Debug.RuntimePolicy != (SkillRuntimePolicyDebug{}) ||
		len(state.Debug.Candidates) != 0 || len(state.Debug.Activations) != 0 ||
		len(state.Debug.ResourceSelections) != 0 || len(state.Debug.ContentLoads) != 0 ||
		len(state.Debug.Projections) != 0 || len(state.Debug.Diagnostics) != 0 {
		return true
	}
	if state.Pinned == nil {
		return false
	}
	pinned := state.Pinned
	return len(pinned.EffectiveBindings) != 0 || len(pinned.Candidates) != 0 ||
		len(pinned.TrustedExplicitActivations) != 0 || len(pinned.TrustedResourceRequests) != 0 ||
		len(pinned.ExpectedCapabilities) != 0 || len(pinned.DomainOutcomes) != 0 ||
		pinned.CacheFingerprint != "" || pinned.DebugProvenance != (SkillDebugProvenance{})
}

func (o *AIOrchestrator) beginRequestRun(ctx context.Context, input requestRunInput) (*executionRunState, error) {
	if err := o.rejectIfConstructionFailed(ctx, "process_request"); err != nil {
		return nil, err
	}
	startedAt := time.Now()
	requestID := o.newRequestID()
	ctx = telemetry.WithBaggage(ctx, "request_id", requestID)

	ctx, conversationID, metadata := o.resolveConversationContext(ctx, input.Metadata)
	input.Metadata = metadata
	if o.config.Name != "" {
		ctx = telemetry.WithBaggage(ctx, "agent_name", o.config.Name)
	}
	if bag := telemetry.GetBaggage(ctx); bag == nil || bag["original_request_id"] == "" {
		ctx = telemetry.WithBaggage(ctx, "original_request_id", requestID)
	}
	ctx = WithRequestID(ctx, requestID)
	ctx = withCheckpointMetadata(ctx, metadata, conversationID)

	parentAttrs := []attribute.KeyValue{attribute.String("request_id", requestID)}
	if conversationID != "" {
		parentAttrs = append(parentAttrs, attribute.String(MetadataConversationID, conversationID))
	}
	telemetry.SetSpanAttributes(ctx, parentAttrs...)
	originalRequestID := requestID
	if bag := telemetry.GetBaggage(ctx); bag != nil && bag["original_request_id"] != "" {
		originalRequestID = bag["original_request_id"]
		telemetry.SetSpanAttributes(ctx, attribute.String("original_request_id", originalRequestID))
	}

	spanName := "orchestrator.process_request"
	if input.Delivery != deliverBuffered {
		spanName = "orchestrator.process_request_streaming"
	}
	var span core.Span = &core.NoOpSpan{}
	if o.telemetry != nil {
		ctx, span = o.telemetry.StartSpan(ctx, spanName)
	}
	span.SetAttribute("request_id", requestID)
	span.SetAttribute("request_length", len(input.Request))
	span.SetAttribute("streaming", input.Delivery != deliverBuffered)
	if conversationID != "" {
		span.SetAttribute(MetadataConversationID, conversationID)
	}
	span.SetAttribute("original_request_id", originalRequestID)

	if o.logger != nil {
		o.logger.InfoWithContext(ctx, "Starting request processing", map[string]interface{}{
			"operation":      "process_request",
			"request_id":     requestID,
			"request_length": len(input.Request),
			"metadata_keys":  getMapKeys(metadata),
			"delivery":       input.Delivery.String(),
		})
	}
	if o.telemetry != nil {
		o.telemetry.RecordMetric("orchestrator.requests.total", 1, map[string]string{
			"module": telemetry.ModuleOrchestration,
			"mode":   string(o.config.RoutingMode),
		})
	}
	if input.DeliveryFallbackReason == "client_streaming_unsupported" {
		if o.logger != nil {
			o.logger.WarnWithContext(ctx, "AI client does not support streaming, using simulated streaming", map[string]interface{}{
				"operation":  "streaming_fallback",
				"request_id": requestID,
				"status":     "fallback",
				"reason":     input.DeliveryFallbackReason,
			})
		}
		telemetry.AddSpanEvent(ctx, "orchestrator.streaming.fallback",
			attribute.String("request_id", requestID),
			attribute.String("reason", input.DeliveryFallbackReason),
		)
	}
	ctx, accumulator := core.WithTokenUsageAccumulator(ctx)
	pctx := &core.PipelineContext{Request: input.Request, Metadata: metadata, Enrichments: make(map[string]interface{})}
	prepareKnownEnrichments(ctx, metadata, pctx.Enrichments, o.conversationHistoryPreparer)

	return &executionRunState{
		Input:     input,
		Context:   ctx,
		StartedAt: startedAt,
		Correlation: requestCorrelation{
			RequestID: requestID, OriginalRequestID: originalRequestID, ConversationID: conversationID,
		},
		Pipeline: pctx,
		Usage:    usageState{Accumulator: accumulator},
		Recorder: o.executionRecorderFor(requestID),
		Span:     span,
	}, nil
}

func (d responseDelivery) String() string {
	switch d {
	case deliverBuffered:
		return "buffered"
	case deliverNativeStream:
		return "native_stream"
	case deliverSimulatedStream:
		return "simulated_stream"
	default:
		return "unknown"
	}
}

func (o *AIOrchestrator) markRunSynthesizing(state *executionRunState) {
	if o.activityCoordinator == nil {
		return
	}
	if err := o.activityCoordinator.UpdateStatus(state.Context, state.Correlation.RequestID, "synthesizing"); err != nil && o.logger != nil {
		o.logger.WarnWithContext(state.Context, "Failed to update activity status", map[string]interface{}{
			"operation": "activity_status_update", "request_id": state.Correlation.RequestID,
			"status": "synthesizing", "error": err.Error(),
		})
	}
}

func (o *AIOrchestrator) completeRun(state *executionRunState, result *requestRunResult) {
	if state == nil || result == nil {
		return
	}
	state.completionLogged = true
	result.Response.ExecutionTime = time.Since(state.StartedAt)
	success := !result.Delivery.PartialContent && len(result.Response.Errors) == 0
	o.updateMetrics(result.Response.ExecutionTime, success)
	o.addToHistory(&result.Response)

	terminationReason := "completed"
	if result.Response.Clarification != nil {
		terminationReason = "clarification"
		telemetry.Histogram("orchestrator.clarification_turn.latency_ms",
			float64(result.Response.ExecutionTime.Milliseconds()),
			"module", telemetry.ModuleOrchestration,
		)
	} else if state.Phase.Result != nil && state.Phase.Result.ForcedTerminal {
		terminationReason = "forced_terminal"
	} else if result.Delivery.PartialContent {
		terminationReason = "partial"
	}

	operation := "process_request_complete"
	message := "Request processing completed"
	latencyOperation := "process_request"
	if state.Input.Delivery != deliverBuffered {
		operation = "streaming_complete"
		message = "Streaming request completed"
		latencyOperation = "process_request_streaming"
	}
	phaseCount := 0
	if state.Phase.Result != nil && state.Phase.Result.CombinedResult != nil {
		phaseCount = state.Phase.Result.CombinedResult.PhaseCount
	}
	status := "success"
	if result.Delivery.PartialContent {
		status = "partial"
	} else if !success {
		status = "error"
	}
	durationMs := result.Response.ExecutionTime.Milliseconds()
	state.Span.SetAttribute("status", status)
	state.Span.SetAttribute("termination_reason", terminationReason)
	state.Span.SetAttribute("duration_ms", durationMs)
	if status == "error" {
		recordRunSpanFailure(state)
	}
	if o.logger != nil {
		fields := map[string]interface{}{
			"operation": operation, "request_id": state.Correlation.RequestID,
			"success": success, "status": status,
			"duration_ms": durationMs, "total_duration_ms": durationMs,
			"termination_reason": terminationReason, "phase_count": phaseCount,
			"chunks_delivered": result.Delivery.ChunksDelivered,
		}
		if status == "error" {
			fields["error"] = "orchestration response completed with errors"
			fields["error_type"] = "request_failed"
			o.logger.ErrorWithContext(state.Context, message, fields)
		} else {
			o.logger.InfoWithContext(state.Context, message, fields)
		}
	}
	if o.telemetry != nil {
		status := "failure"
		if success {
			status = "success"
			o.telemetry.RecordMetric("orchestrator.requests.success", 1, map[string]string{
				"module": telemetry.ModuleOrchestration,
				"mode":   string(o.config.RoutingMode),
			})
		}
		o.telemetry.RecordMetric("orchestrator.latency_ms", float64(result.Response.ExecutionTime.Milliseconds()), map[string]string{
			"module": telemetry.ModuleOrchestration, "operation": latencyOperation, "status": status,
		})
	}
}

func (o *AIOrchestrator) completeFailedRun(state *executionRunState, runErr error) {
	if state == nil || state.completionLogged {
		return
	}
	state.completionLogged = true
	duration := time.Since(state.StartedAt)
	o.updateMetrics(duration, false)
	operation := "process_request_complete"
	message := "Request processing completed"
	latencyOperation := "process_request"
	if state.Input.Delivery != deliverBuffered {
		operation = "streaming_complete"
		message = "Streaming request completed"
		latencyOperation = "process_request_streaming"
	}
	reason := state.completionReason
	if reason == "" {
		reason = "failed"
	}
	phaseCount := 0
	if state.Phase.Result != nil && state.Phase.Result.CombinedResult != nil {
		phaseCount = state.Phase.Result.CombinedResult.PhaseCount
	}
	status := "error"
	if IsInterrupted(runErr) {
		status = "interrupted"
	}
	durationMs := duration.Milliseconds()
	state.Span.SetAttribute("status", status)
	state.Span.SetAttribute("termination_reason", reason)
	state.Span.SetAttribute("duration_ms", durationMs)
	if o.logger != nil {
		fields := map[string]interface{}{
			"operation": operation, "request_id": state.Correlation.RequestID,
			"success": false, "status": status,
			"duration_ms": durationMs, "total_duration_ms": durationMs,
			"termination_reason": reason, "phase_count": phaseCount,
			"chunks_delivered": 0,
		}
		if status == "error" {
			fields["error"] = "orchestration request failed"
			fields["error_type"] = "request_failed"
			o.logger.ErrorWithContext(state.Context, message, fields)
		} else {
			o.logger.InfoWithContext(state.Context, message, fields)
		}
	}
	if o.telemetry != nil {
		o.telemetry.RecordMetric("orchestrator.latency_ms", float64(duration.Milliseconds()), map[string]string{
			"module": telemetry.ModuleOrchestration, "operation": latencyOperation, "status": "failure",
		})
	}
}

func requestFailureReason(state *executionRunState) string {
	if state == nil || state.completionReason == "" {
		return "failed"
	}
	return state.completionReason
}

func recordRunSpanFailure(state *executionRunState) {
	if state == nil || state.Span == nil || state.spanFailed {
		return
	}
	state.spanFailed = true
	state.Span.RecordError(fmt.Errorf("orchestration request failed: %s", requestFailureReason(state)))
	state.Span.SetAttribute("error_type", "request_failed")
}

const simulatedStreamChunkBytes = 50

func deliverUTF8Chunks(content string, callback core.StreamCallback) (responseDeliveryState, error) {
	state := responseDeliveryState{FinishReason: "stop"}
	deliveredBytes := 0
	for deliveredBytes < len(content) {
		end := utf8SafeChunkEnd(content, deliveredBytes, simulatedStreamChunkBytes)
		chunk := core.StreamChunk{
			Content: content[deliveredBytes:end],
			Delta:   true,
			Index:   state.ChunksDelivered,
		}
		state.ChunksDelivered++
		deliveredBytes = end
		if err := callback(chunk); err != nil {
			state.PartialContent = true
			state.FinishReason = "cancelled"
			return state, err
		}
	}

	final := core.StreamChunk{Delta: false, Index: state.ChunksDelivered, FinishReason: "stop"}
	if err := callback(final); err != nil {
		state.PartialContent = true
		state.FinishReason = "cancelled"
		return state, err
	}
	state.StreamCompleted = true
	return state, nil
}

func utf8SafeChunkEnd(content string, start, targetBytes int) int {
	end := start + targetBytes
	if end >= len(content) {
		return len(content)
	}
	for end > start && !utf8.RuneStart(content[end]) {
		end--
	}
	if end == start {
		_, size := utf8.DecodeRuneInString(content[start:])
		return start + size
	}
	return end
}
