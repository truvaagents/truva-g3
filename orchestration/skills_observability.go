package orchestration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	skillOperationTotalMetric         = "orchestration.skills.operation.total"
	skillOperationDurationMetric      = "orchestration.skills.operation.duration_ms"
	skillCandidateBatchMetric         = "orchestration.skills.candidate.batch_size"
	skillSelectorTokensMetric         = "orchestration.skills.selector.tokens"
	skillContentCacheMetric           = "orchestration.skills.content_cache.total"
	skillPromptTokensMetric           = "orchestration.skills.prompt.tokens"
	skillIntegrityMetric              = "orchestration.skills.integrity.total"
	skillAuthoringDiagnosticMetric    = "orchestration.skills.authoring.diagnostic.total"
	skillAdminOperationTotalMetric    = "orchestration.skills.admin.operation.total"
	skillAdminOperationDurationMetric = "orchestration.skills.admin.operation.duration_ms"
)

type skillOperationObservation struct {
	runtime     *skillRuntime
	ctx         context.Context
	span        core.Span
	operation   string
	boundary    SkillPromptBoundary
	phaseNumber int
	startedAt   time.Time
}

func (runtime *skillRuntime) startSkillOperation(
	ctx context.Context,
	operation string,
	boundary SkillPromptBoundary,
	phaseNumber int,
) (context.Context, *skillOperationObservation) {
	return runtime.startNamedSkillOperation(
		ctx, "orchestrator.skills."+operation, operation, boundary, phaseNumber,
	)
}

func (runtime *skillRuntime) startSkillRegistryOperation(
	ctx context.Context,
	operation string,
	boundary SkillPromptBoundary,
	phaseNumber int,
) (context.Context, *skillOperationObservation) {
	return runtime.startNamedSkillOperation(
		ctx, "skills.registry."+operation, "registry_"+operation, boundary, phaseNumber,
	)
}

func (runtime *skillRuntime) startNamedSkillOperation(
	ctx context.Context,
	spanName string,
	operation string,
	boundary SkillPromptBoundary,
	phaseNumber int,
) (context.Context, *skillOperationObservation) {
	observation := &skillOperationObservation{
		runtime: runtime, ctx: ctx, span: &core.NoOpSpan{}, operation: operation,
		boundary: boundary, phaseNumber: phaseNumber, startedAt: time.Now(),
	}
	if runtime != nil && runtime.telemetry != nil {
		ctx, observation.span = runtime.telemetry.StartSpan(ctx, spanName)
		observation.ctx = ctx
		telemetry.SetCommonAttrsOn(ctx, observation.span)
	}
	observation.span.SetAttribute("skills.operation", operation)
	observation.span.SetAttribute("skills.boundary", skillMetricBoundary(boundary))
	if phaseNumber > 0 {
		observation.span.SetAttribute("skills.phase_number", phaseNumber)
	}
	return ctx, observation
}

func (runtime *skillRuntime) loadSkillManifest(
	ctx context.Context,
	ref SkillVersionRef,
	boundary SkillPromptBoundary,
	phaseNumber int,
	required bool,
) (SkillManifest, skillContentReadEvidence, error) {
	startedAt := time.Now()
	readCtx, cancel := context.WithTimeout(ctx, runtime.config.Limits.RegistryReadTimeout)
	defer cancel()
	var evidence skillContentReadEvidence
	observedCtx := withSkillContentReadObserver(readCtx, func(observed skillContentReadEvidence) {
		evidence = observed
	})
	var manifest SkillManifest
	var err error
	if _, cached := runtime.registry.(*ImmutableCachedSkillRegistry); cached {
		observedCtx = withSkillUpstreamReadInterceptors(observedCtx, skillUpstreamReadInterceptors{
			manifest: func(
				ctx context.Context,
				ref SkillVersionRef,
				read skillManifestUpstreamReader,
			) (SkillManifest, error) {
				return runtime.observeSkillManifestRegistryRead(ctx, ref, boundary, phaseNumber, read)
			},
		})
		manifest, err = runtime.registry.GetManifest(observedCtx, ref)
	} else {
		manifest, err = runtime.observeSkillManifestRegistryRead(
			observedCtx, ref, boundary, phaseNumber, runtime.registry.GetManifest,
		)
	}
	evidence = normalizeSkillContentReadEvidence(evidence)
	evidence.DurationMs = time.Since(startedAt).Milliseconds()
	if err == nil {
		content := skillManifestRuntimeContent(manifest)
		evidence.ByteEstimate = len(content)
		evidence.TokenEstimate = canonicalSkillTokenEstimate(content)
	}
	runtime.recordSkillContentMetrics("manifest", evidence, skillContentFinalAction(err, required))
	return manifest, evidence, err
}

func (runtime *skillRuntime) observeSkillManifestRegistryRead(
	ctx context.Context,
	ref SkillVersionRef,
	boundary SkillPromptBoundary,
	phaseNumber int,
	read skillManifestUpstreamReader,
) (SkillManifest, error) {
	ctx, observation := runtime.startSkillRegistryOperation(
		ctx, "load_manifest", boundary, phaseNumber,
	)
	observation.span.SetAttribute("skill.namespace", ref.Ref.Namespace)
	observation.span.SetAttribute("skill.name", ref.Ref.Name)
	observation.span.SetAttribute("skill.version", ref.Version)
	manifest, err := read(ctx, ref)
	observation.Finish("resolved", err)
	return manifest, err
}

func (runtime *skillRuntime) loadSkillResource(
	ctx context.Context,
	ref SkillResourceRef,
	boundary SkillPromptBoundary,
	phaseNumber int,
	required bool,
) (SkillResource, skillContentReadEvidence, error) {
	startedAt := time.Now()
	readCtx, cancel := context.WithTimeout(ctx, runtime.config.Limits.RegistryReadTimeout)
	defer cancel()
	var evidence skillContentReadEvidence
	observedCtx := withSkillContentReadObserver(readCtx, func(observed skillContentReadEvidence) {
		evidence = observed
	})
	var resource SkillResource
	var err error
	if _, cached := runtime.registry.(*ImmutableCachedSkillRegistry); cached {
		observedCtx = withSkillUpstreamReadInterceptors(observedCtx, skillUpstreamReadInterceptors{
			resource: func(
				ctx context.Context,
				ref SkillResourceRef,
				read skillResourceUpstreamReader,
			) (SkillResource, error) {
				return runtime.observeSkillResourceRegistryRead(ctx, ref, boundary, phaseNumber, read)
			},
		})
		resource, err = runtime.registry.GetResource(observedCtx, ref)
	} else {
		resource, err = runtime.observeSkillResourceRegistryRead(
			observedCtx, ref, boundary, phaseNumber, runtime.registry.GetResource,
		)
	}
	evidence = normalizeSkillContentReadEvidence(evidence)
	evidence.DurationMs = time.Since(startedAt).Milliseconds()
	if err == nil {
		evidence.ByteEstimate = len(resource.Content)
		evidence.TokenEstimate = canonicalSkillTokenEstimate(resource.Content)
	}
	runtime.recordSkillContentMetrics("resource", evidence, skillContentFinalAction(err, required))
	return resource, evidence, err
}

func (runtime *skillRuntime) observeSkillResourceRegistryRead(
	ctx context.Context,
	ref SkillResourceRef,
	boundary SkillPromptBoundary,
	phaseNumber int,
	read skillResourceUpstreamReader,
) (SkillResource, error) {
	ctx, observation := runtime.startSkillRegistryOperation(
		ctx, "load_resource", boundary, phaseNumber,
	)
	observation.span.SetAttribute("skill.namespace", ref.Skill.Ref.Namespace)
	observation.span.SetAttribute("skill.name", ref.Skill.Ref.Name)
	observation.span.SetAttribute("skill.version", ref.Skill.Version)
	observation.span.SetAttribute("skill.resource", ref.Name)
	resource, err := read(ctx, ref)
	observation.Finish("resolved", err)
	return resource, err
}

func skillManifestRuntimeContent(manifest SkillManifest) string {
	parts := make([]string, 0, len(manifest.PlanningInstructions)+len(manifest.ResponseInstructions)+len(manifest.ToolHints))
	parts = append(parts, manifest.PlanningInstructions...)
	parts = append(parts, manifest.ResponseInstructions...)
	parts = append(parts, manifest.ToolHints...)
	return strings.Join(parts, "\n")
}

func normalizeSkillContentReadEvidence(evidence skillContentReadEvidence) skillContentReadEvidence {
	if evidence.Source == "" {
		evidence.Source = "verified_registry"
	}
	if evidence.CacheOutcome == "" {
		evidence.CacheOutcome = "bypass"
	}
	if evidence.Source == "verified_registry" && evidence.Attempt == 0 {
		evidence.Attempt = 1
	}
	return evidence
}

func (observation *skillOperationObservation) Finish(outcome string, err error) {
	if observation == nil {
		return
	}
	duration := time.Since(observation.startedAt)
	status := "success"
	reason := boundedSkillObservationReason(outcome, err)
	if err != nil {
		status = "error"
		observed := errors.New(safeSkillObservationError(err))
		observation.span.RecordError(observed)
	}
	observation.span.SetAttribute("skills.status", status)
	observation.span.SetAttribute("skills.outcome", reason)
	observation.span.SetAttribute("skills.duration_ms", duration.Milliseconds())
	observation.span.End()
	if observation.runtime != nil && observation.runtime.telemetry != nil {
		labels := map[string]string{
			"module":   telemetry.ModuleOrchestration,
			"stage":    skillMetricStage(observation.operation),
			"boundary": skillMetricBoundary(observation.boundary),
			"outcome":  skillMetricOutcome(outcome, err),
		}
		observation.runtime.telemetry.RecordMetric(skillOperationTotalMetric, 1, labels)
		observation.runtime.telemetry.RecordMetric(
			skillOperationDurationMetric, float64(duration.Milliseconds()), labels,
		)
	}
	if observation.runtime != nil && observation.runtime.logger != nil {
		fields := map[string]interface{}{
			"operation":    "skills_" + observation.operation,
			"request_id":   GetRequestID(observation.ctx),
			"boundary":     skillMetricBoundary(observation.boundary),
			"phase_number": observation.phaseNumber,
			"status":       status, "reason": reason,
			"duration_ms": duration.Milliseconds(),
		}
		if err != nil {
			fields["error_type"] = skillObservationErrorType(err)
			fields["error"] = safeSkillObservationError(err)
			observation.runtime.logger.WarnWithContext(
				observation.ctx, "Skill operation completed with a bounded failure", fields,
			)
		} else {
			observation.runtime.logger.DebugWithContext(
				observation.ctx, "Skill operation completed", fields,
			)
		}
	}
}

func (runtime *skillRuntime) recordSkillProjectionObservation(
	ctx context.Context,
	projection SkillProjectionDebug,
) {
	telemetry.AddSpanEvent(ctx, "orchestrator.skills.projection",
		attribute.String("request_id", GetRequestID(ctx)),
		attribute.String("boundary", skillMetricBoundary(projection.Boundary)),
		attribute.Int("phase_number", projection.PhaseNumber),
		attribute.Int("skill_count", len(projection.SkillRefs)),
		attribute.Int("resource_count", len(projection.ResourceRefs)),
		attribute.Int("total_tokens", projection.TotalTokens),
		attribute.String("outcome", projection.Outcome),
	)
	if runtime != nil && runtime.telemetry != nil {
		runtime.telemetry.RecordMetric(
			skillPromptTokensMetric, float64(projection.TotalTokens),
			map[string]string{
				"module":      telemetry.ModuleOrchestration,
				"boundary":    skillMetricBoundary(projection.Boundary),
				"prompt_kind": skillPromptKindForBoundary(projection.Boundary),
			},
		)
	}
}

func (o *AIOrchestrator) recordSkillResponseCacheDecision(
	currentDimensions map[string]string,
	cachedDimensions map[string]string,
	kind core.PipelineShortCircuitKind,
	accepted bool,
	decisionErr error,
	duration time.Duration,
) {
	if o == nil || o.telemetry == nil ||
		!hasSkillCacheDimension(currentDimensions, cachedDimensions) {
		return
	}
	outcome := "rejected"
	switch {
	case decisionErr != nil:
		outcome = "error"
	case kind == core.PipelineShortCircuitAuthoritative:
		outcome = "authoritative"
	case accepted:
		outcome = "accepted"
	}
	labels := map[string]string{
		"module": telemetry.ModuleOrchestration, "stage": "response_cache",
		"boundary": "request_start", "outcome": outcome,
	}
	o.telemetry.RecordMetric(skillOperationTotalMetric, 1, labels)
	o.telemetry.RecordMetric(skillOperationDurationMetric, float64(duration.Milliseconds()), labels)
}

func hasSkillCacheDimension(dimensions ...map[string]string) bool {
	for _, values := range dimensions {
		if _, present := values[reservedSkillCacheDimension]; present {
			return true
		}
	}
	return false
}

func recordSkillConfigValidation(
	provider core.Telemetry,
	config *OrchestratorConfig,
	boundary string,
	duration time.Duration,
	err error,
) {
	if provider == nil || config == nil || (!config.Skills.Enabled && len(config.Skills.Bindings) == 0) {
		return
	}
	recordSkillValidationMetrics(provider, boundary, duration, err)
}

func recordSkillConfigResolutionFailure(
	provider core.Telemetry,
	err error,
	duration time.Duration,
) {
	if provider == nil || err == nil {
		return
	}
	var environmentErr *ConfigEnvironmentError
	if errors.As(err, &environmentErr) && strings.HasPrefix(environmentErr.Variable, "TRUVAG3_SKILL") {
		recordSkillValidationMetrics(provider, "startup", duration, err)
		return
	}
	if errors.Is(err, errInvalidSkillRuntimeConfig) {
		recordSkillValidationMetrics(provider, "startup", duration, err)
	}
}

func recordSkillValidationMetrics(
	provider core.Telemetry,
	boundary string,
	duration time.Duration,
	err error,
) {
	if provider == nil {
		return
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	labels := map[string]string{
		"module": telemetry.ModuleOrchestration, "stage": "config_validation",
		"boundary": boundary, "outcome": outcome,
	}
	provider.RecordMetric(skillOperationTotalMetric, 1, labels)
	provider.RecordMetric(skillOperationDurationMetric, float64(duration.Milliseconds()), labels)
}

func skillMetricStage(operation string) string {
	switch operation {
	case "pin_candidates", "registry_resolve_candidates":
		return "pin_candidates"
	case "activate":
		return "activation"
	case "registry_load_manifest":
		return "manifest_load"
	case "resolve_resources":
		return "resource_selection"
	case "registry_load_resource":
		return "resource_load"
	default:
		return "projection"
	}
}

func skillMetricOutcome(outcome string, err error) string {
	if err != nil {
		return "error"
	}
	switch outcome {
	case "omitted":
		return "omitted"
	case "empty":
		return "noop"
	default:
		return "success"
	}
}

func skillPromptKindForBoundary(boundary SkillPromptBoundary) string {
	switch boundary {
	case SkillBoundaryInitialPlanning:
		return "planning"
	case SkillBoundaryContinuation:
		return "continuation"
	case SkillBoundaryRegeneration:
		return "regeneration"
	case SkillBoundarySynthesis:
		return "synthesis"
	case SkillBoundaryResume:
		return "planning"
	default:
		return "planning"
	}
}

func skillMetricBoundary(boundary SkillPromptBoundary) string {
	switch boundary {
	case SkillBoundaryInitialPlanning, SkillBoundaryContinuation, SkillBoundaryRegeneration,
		SkillBoundarySynthesis, SkillBoundaryResume:
		return string(boundary)
	default:
		return "request_start"
	}
}

func boundedSkillObservationReason(outcome string, err error) string {
	if err == nil {
		switch outcome {
		case "resolved", "compiled", "selected", "empty", "resumed", "omitted":
			return outcome
		default:
			return "completed"
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrSkillIntegrity):
		return "integrity"
	case errors.Is(err, ErrSkillLimitExceeded):
		return "limit"
	case errors.Is(err, ErrInvalidSkillPackage):
		return "invalid"
	case errors.Is(err, ErrSkillUnavailable), errors.Is(err, ErrSkillNotFound),
		errors.Is(err, ErrSkillRevisionNotFound):
		return "unavailable"
	default:
		return "framework"
	}
}

func skillObservationErrorType(err error) string {
	var featureErr *core.AIRequestFeatureError
	switch {
	case errors.As(err, &featureErr):
		return "llm_unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, ErrSkillIntegrity):
		return "integrity"
	case errors.Is(err, ErrInvalidSkillPackage), errors.Is(err, ErrSkillLimitExceeded):
		return "validation"
	case errors.Is(err, ErrSkillUnavailable), errors.Is(err, ErrSkillNotFound),
		errors.Is(err, ErrSkillRevisionNotFound):
		return "backend_read"
	default:
		return "framework"
	}
}

func safeSkillObservationError(err error) string {
	if err == nil {
		return ""
	}
	return "skill operation failed: " + boundedSkillObservationReason("", err)
}

func (runtime *skillRuntime) invokeSkillAI(
	ctx context.Context,
	invocation aiInvocation,
	boundary SkillPromptBoundary,
	phaseNumber int,
	attempt int,
) (*aiInvocationResult, error) {
	purpose := invocation.Purpose
	llmCtx := telemetry.WithBaggage(ctx, "ai.purpose", purpose)
	startedAt := time.Now()
	invocation.DeferRecording = runtime.debugRecorder != nil
	result, err := invokeAI(llmCtx, runtime.aiClient, invocation)
	if result != nil && result.Response != nil {
		runtime.recordSkillSelectorTokenMetrics(purpose, boundary, result.Response.Usage, err)
	}
	if runtime.debugRecorder == nil {
		return result, err
	}
	var response *core.AIResponse
	if result != nil {
		response = result.Response
	}
	interaction := withEffectiveAIRequest(LLMInteraction{
		Type: purpose, Category: "llm", Timestamp: startedAt,
		DurationMs: time.Since(startedAt).Milliseconds(),
		Success:    err == nil,
		Attempt:    attempt, PhaseNumber: phaseNumber,
		CallDescription: "Agent skill selection",
	}, result, invocation, response, err)
	if response != nil {
		interaction.Response = response.Content
		interaction.PromptTokens = response.Usage.PromptTokens
		interaction.CompletionTokens = response.Usage.CompletionTokens
		interaction.TotalTokens = response.Usage.TotalTokens
	}
	if err != nil {
		interaction.Error = core.RedactSensitiveText(err.Error())
	}
	runtime.debugRecorder(llmCtx, interaction)
	return result, err
}

func (runtime *skillRuntime) recordSkillSelectorTokenMetrics(
	purpose string,
	boundary SkillPromptBoundary,
	usage core.TokenUsage,
	err error,
) {
	if runtime == nil || runtime.telemetry == nil {
		return
	}
	selector := "activation"
	if purpose == skillResourceAIPurpose {
		selector = "resource"
	}
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	for kind, value := range map[string]int{
		"prompt": usage.PromptTokens, "completion": usage.CompletionTokens, "total": usage.TotalTokens,
	} {
		runtime.telemetry.RecordMetric(skillSelectorTokensMetric, float64(value), map[string]string{
			"module": telemetry.ModuleOrchestration, "selector": selector,
			"token_kind": kind, "outcome": outcome,
		})
	}
	runtime.telemetry.RecordMetric(skillPromptTokensMetric, float64(usage.PromptTokens), map[string]string{
		"module": telemetry.ModuleOrchestration, "prompt_kind": purpose,
		"boundary": skillMetricBoundary(boundary),
	})
}

func (runtime *skillRuntime) recordSkillContentMetrics(kind string, evidence skillContentReadEvidence, action string) {
	if runtime == nil || runtime.telemetry == nil {
		return
	}
	if cacheOutcome := skillCacheMetricOutcome(evidence.CacheOutcome); cacheOutcome != "" {
		runtime.telemetry.RecordMetric(skillContentCacheMetric, 1, map[string]string{
			"module": telemetry.ModuleOrchestration, "content_kind": skillMetricContentKind(kind), "outcome": cacheOutcome,
		})
	}
	source := "registry"
	if evidence.Source == "immutable_cache" {
		source = "cache"
	}
	retry := evidence.RetryOutcome
	if retry == "" {
		retry = "not_attempted"
	}
	runtime.telemetry.RecordMetric(skillIntegrityMetric, 1, map[string]string{
		"module": telemetry.ModuleOrchestration, "content_kind": skillMetricContentKind(kind), "source": source,
		"retry_outcome": retry, "action": action,
	})
}

func skillContentFinalAction(err error, required bool) string {
	if err == nil {
		return "used"
	}
	if required {
		return "failed"
	}
	return "omitted"
}

func skillMetricContentKind(kind string) string {
	if kind == "resource" {
		return "resource"
	}
	return "manifest"
}

func skillCacheMetricOutcome(outcome string) string {
	switch outcome {
	case "hit", "miss":
		return outcome
	case "integrity_mismatch":
		return "evicted"
	case "cache_error":
		return "error"
	default:
		return ""
	}
}
