package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

func (runtime *skillRuntime) prepareContinuationBoundary(
	ctx context.Context,
	state SkillExecutionState,
	request string,
	enrichments map[string]interface{},
	snapshot executionRunSnapshot,
	boundary SkillPromptBoundary,
) (context.Context, SkillExecutionState, *skillPromptProjection, error) {
	phaseNumber := max(snapshot.PhaseNumber, 1)
	activationCtx := ctx
	var observation *skillOperationObservation
	if boundary == SkillBoundaryContinuation && hasContinuationActivationCandidates(&state) {
		activationCtx, observation = runtime.startSkillOperation(
			ctx, "activate", boundary, phaseNumber,
		)
	}
	projected, err := runtime.loadActiveSkillProjection(
		activationCtx, &state, boundary, phaseNumber,
	)
	if err != nil {
		if observation != nil {
			observation.Finish("selected", err)
		}
		return ctx, state, nil, err
	}
	if boundary == SkillBoundaryContinuation {
		choices, diagnostics := runtime.selectContinuationSkills(
			activationCtx, &state, request, enrichments, snapshot,
		)
		state.Diagnostics = append(state.Diagnostics, diagnostics...)
		state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostics...)
		projected, err = runtime.admitAdditionalSkills(
			activationCtx, &state, projected, choices, boundary, phaseNumber,
		)
		if err != nil {
			if observation != nil {
				observation.Finish("selected", err)
			}
			return ctx, state, nil, err
		}
	}
	if observation != nil {
		observation.Finish("selected", nil)
	}
	// Resource resolution belongs directly to the phase boundary. Keeping the
	// original context makes it a sibling of activation in distributed traces.
	projected, err = runtime.selectAndAdmitResources(
		ctx, request, boundary, phaseNumber, &state, projected,
		buildSkillSelectorContext(enrichments, snapshot, state.ResourceSelections, state.Pinned.ExpectedCapabilities),
	)
	if err != nil {
		return ctx, state, nil, err
	}
	projection := &skillPromptProjection{
		Boundary: boundary, PhaseNumber: phaseNumber, Skills: projected,
	}
	promptKind := "continuation"
	if boundary == SkillBoundaryResume {
		promptKind = "resume"
	}
	runtime.recordSkillProjection(ctx, &state, projection, promptKind, "compiled")
	runtime.recordSkillProjectionObservation(ctx, state.Debug.Projections[len(state.Debug.Projections)-1])
	ctx = runtime.withSkillProjection(ctx, projection)
	return ctx, state, projection, nil
}

func hasContinuationActivationCandidates(state *SkillExecutionState) bool {
	if state == nil || state.Pinned == nil {
		return false
	}
	active := make(map[SkillRef]struct{}, len(state.ActiveSkills))
	for _, skill := range state.ActiveSkills {
		active[skill.Skill.Ref] = struct{}{}
	}
	for index, binding := range state.Pinned.EffectiveBindings {
		if index >= len(state.Pinned.Candidates) ||
			binding.Activation != SkillActivationAuto ||
			state.Pinned.Candidates[index].Status != SkillCandidateResolved {
			continue
		}
		if _, alreadyActive := active[binding.Ref()]; !alreadyActive {
			return true
		}
	}
	return false
}

func (runtime *skillRuntime) prepareSynthesisBoundary(
	ctx context.Context,
	state SkillExecutionState,
	request string,
	snapshot executionRunSnapshot,
) (context.Context, SkillExecutionState, *skillPromptProjection, error) {
	phaseNumber := max(snapshot.PhaseNumber, 1)
	projected, err := runtime.loadActiveSkillProjection(
		ctx, &state, SkillBoundarySynthesis, phaseNumber,
	)
	if err != nil {
		return ctx, state, nil, err
	}
	projected, err = runtime.selectAndAdmitResources(
		ctx, request, SkillBoundarySynthesis, phaseNumber, &state, projected,
		buildSkillSelectorContext(nil, snapshot, state.ResourceSelections, state.Pinned.ExpectedCapabilities),
	)
	if err != nil {
		return ctx, state, nil, err
	}
	projection := &skillPromptProjection{
		Boundary: SkillBoundarySynthesis, PhaseNumber: phaseNumber, Skills: projected,
	}
	runtime.recordSkillProjection(ctx, &state, projection, "synthesis", "compiled")
	runtime.recordSkillProjectionObservation(ctx, state.Debug.Projections[len(state.Debug.Projections)-1])
	ctx = runtime.withSkillProjection(ctx, projection)
	return ctx, state, projection, nil
}

func (runtime *skillRuntime) withSkillProjection(
	ctx context.Context,
	projection *skillPromptProjection,
) context.Context {
	ctx = withSkillPromptProjection(ctx, projection)
	if compileSkillInstructionEnvelope(projection, projection.Boundary == SkillBoundarySynthesis) != "" ||
		compileSkillToolHintsEnvelope(projection) != "" {
		ctx = withPromptInputPreparer(ctx, skillPromptInputPreparer{})
	}
	return ctx
}

func withReusedSkillProjection(
	ctx context.Context,
	projection *skillPromptProjection,
	boundary SkillPromptBoundary,
) context.Context {
	if projection == nil {
		return ctx
	}
	copy := *projection
	copy.Boundary = boundary
	copy.Skills = cloneProjectedSkills(projection.Skills)
	ctx = withSkillPromptProjection(ctx, &copy)
	return withPromptInputPreparer(ctx, skillPromptInputPreparer{})
}

func (runtime *skillRuntime) reuseSkillProjection(
	ctx context.Context,
	state *SkillExecutionState,
	projection *skillPromptProjection,
	boundary SkillPromptBoundary,
) (context.Context, error) {
	ctx = withReusedSkillProjection(ctx, projection, boundary)
	reused, ok := skillPromptProjectionFromContext(ctx)
	if !ok {
		return ctx, newSkillDomainError(ErrSkillIntegrity, "record reused projection", SkillRef{})
	}
	runtime.recordSkillProjection(ctx, state, reused, skillPromptKindForBoundary(boundary), "reused")
	runtime.recordSkillProjectionObservation(ctx, state.Debug.Projections[len(state.Debug.Projections)-1])
	return ctx, nil
}

func (runtime *skillRuntime) loadActiveSkillProjection(
	ctx context.Context,
	state *SkillExecutionState,
	boundary SkillPromptBoundary,
	phaseNumber int,
) ([]projectedSkill, error) {
	projected := make([]projectedSkill, 0, len(state.ActiveSkills))
	for _, active := range state.ActiveSkills {
		if skillVersionRefInList(state.UnavailableContent, active.Skill) {
			continue
		}
		manifest, loadEvidence, err := runtime.loadSkillManifest(
			ctx, active.Skill, boundary, phaseNumber, active.Binding.Required,
		)
		load := SkillContentLoadDebug{
			Sequence: len(state.Debug.ContentLoads) + 1,
			Boundary: boundary, PhaseNumber: phaseNumber, ContentKind: "manifest",
			Skill: active.Skill, ExpectedHash: active.Skill.ManifestHash,
			CacheOutcome: loadEvidence.CacheOutcome, Source: loadEvidence.Source,
			Attempt: loadEvidence.Attempt, RetryOutcome: loadEvidence.RetryOutcome,
			ObservedHash: loadEvidence.ObservedHash,
			ByteEstimate: loadEvidence.ByteEstimate, TokenEstimate: loadEvidence.TokenEstimate,
			DurationMs: loadEvidence.DurationMs,
		}
		if err != nil {
			load.Outcome = "omitted"
			load.DiagnosticCode = "skill_manifest_load_failed"
			if errors.Is(err, ErrSkillIntegrity) {
				load.DiagnosticCode = "skill_manifest_hash_mismatch"
			}
			if active.Binding.Required {
				load.Outcome = "failed"
			}
			state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
			ref := active.Skill.Ref
			diagnostic := SkillDiagnostic{
				Code: "skill_content_omitted", Boundary: boundary, PhaseNumber: phaseNumber,
				Skill: &ref, Action: "omitted",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
			if active.Binding.Required {
				return nil, newRequiredSkillContentError(err, "load required active manifest", active.Skill.Ref)
			}
			continue
		}
		if load.ObservedHash == "" {
			load.ObservedHash = manifest.Ref.ManifestHash
		}
		load.Outcome = "verified"
		state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
		projected = append(projected, projectedSkill{active: active, manifest: manifest})
	}
	return projected, nil
}

func skillVersionRefInList(values []SkillVersionRef, wanted SkillVersionRef) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (runtime *skillRuntime) selectContinuationSkills(
	ctx context.Context,
	state *SkillExecutionState,
	request string,
	enrichments map[string]interface{},
	snapshot executionRunSnapshot,
) ([]skillActivationChoice, []SkillDiagnostic) {
	if state == nil || state.Pinned == nil {
		return nil, nil
	}
	active := make(map[SkillRef]struct{}, len(state.ActiveSkills))
	for _, skill := range state.ActiveSkills {
		active[skill.Skill.Ref] = struct{}{}
	}
	bindingByRef := make(map[SkillRef]SkillBinding, len(state.Pinned.EffectiveBindings))
	candidateByRef := make(map[SkillRef]SkillCandidate, len(state.Pinned.Candidates))
	remaining := make([]SkillCatalogSummary, 0)
	for index, binding := range state.Pinned.EffectiveBindings {
		bindingByRef[binding.Ref()] = binding
		candidate := state.Pinned.Candidates[index]
		candidateByRef[binding.Ref()] = candidate
		if binding.Activation != SkillActivationAuto || candidate.Status != SkillCandidateResolved {
			continue
		}
		if _, alreadyActive := active[binding.Ref()]; alreadyActive {
			continue
		}
		remaining = append(remaining, candidateCatalogSummary(candidate))
	}
	if len(remaining) == 0 {
		return nil, nil
	}
	diagnostics := make([]SkillDiagnostic, 0)
	choices := make([]skillActivationChoice, 0)
	if !isNilBackendValue(runtime.activationPolicy) {
		decision, err := runtime.activationPolicy.Evaluate(ctx, SkillActivationPolicyInput{
			Request: request, Enrichments: clonePromptMetadata(enrichments),
			Candidates: cloneSkillCatalogSummaries(remaining),
		})
		if err != nil {
			return nil, []SkillDiagnostic{{
				Code: "skill_activation_policy_failed", Boundary: SkillBoundaryContinuation,
				PhaseNumber: snapshot.PhaseNumber, Action: "auto_activation_skipped",
			}}
		}
		included, excluded, err := validateSkillActivationPolicyDecision(remaining, decision)
		if err != nil {
			return nil, []SkillDiagnostic{{
				Code: "skill_activation_policy_invalid", Boundary: SkillBoundaryContinuation,
				PhaseNumber: snapshot.PhaseNumber, Action: "auto_activation_skipped",
			}}
		}
		for _, ref := range included {
			choices = append(choices, skillActivationChoice{
				binding: bindingByRef[ref], candidate: candidateByRef[ref],
				selector: SkillDecisionCustomPolicy, reason: "deterministic activation policy",
			})
		}
		remaining = filterSkillCatalogSummaries(remaining, included, excluded)
	}
	if len(remaining) == 0 {
		return choices, diagnostics
	}
	catalogTokens, catalogFallback := runtime.runtimeSkillTokenEstimate(ctx, mustMarshalSkillCatalog(remaining))
	if catalogFallback {
		diagnostics = append(diagnostics, skillTokenCounterFallbackDiagnostic(
			SkillBoundaryContinuation, snapshot.PhaseNumber,
		))
	}
	if len(remaining) > runtime.config.Limits.MaxAutoCandidates ||
		catalogTokens > runtime.config.Limits.CatalogTokenBudget {
		return choices, append(diagnostics, SkillDiagnostic{
			Code: "skill_activation_catalog_budget_exceeded", Boundary: SkillBoundaryContinuation,
			PhaseNumber: snapshot.PhaseNumber, Action: "auto_activation_skipped",
		})
	}
	input := SkillContinuationInput{
		Request: request, Boundary: SkillBoundaryContinuation,
		PhaseNumber: snapshot.PhaseNumber, Candidates: cloneSkillCatalogSummaries(remaining),
		PreviouslyActive: append([]ActiveSkill(nil), state.ActiveSkills...),
		Context:          buildSkillSelectorContext(enrichments, snapshot, state.ResourceSelections, state.Pinned.ExpectedCapabilities),
	}
	decisionSource := SkillDecisionDefaultAI
	var decision SkillActivationDecision
	var err error
	if !isNilBackendValue(runtime.customResolver) {
		decisionSource = SkillDecisionCustomPolicy
		decision, err = runtime.customResolver.ResolveContinuation(ctx, input)
	} else {
		decision, err = runtime.resolveContinuationWithAI(ctx, input)
	}
	if err != nil {
		return choices, append(diagnostics, SkillDiagnostic{
			Code: classifySkillSelectorFailure(err), Boundary: SkillBoundaryContinuation,
			PhaseNumber: snapshot.PhaseNumber, Action: "auto_activation_skipped",
		})
	}
	validated, err := validateSkillActivationDecision(remaining, decision)
	if err != nil {
		return choices, append(diagnostics, SkillDiagnostic{
			Code: "skill_activation_selection_invalid", Boundary: SkillBoundaryContinuation,
			PhaseNumber: snapshot.PhaseNumber, Action: "auto_activation_skipped",
		})
	}
	for _, ref := range validated {
		reason := decision.Reasons[ref.String()]
		if reason == "" {
			reason = "selected as relevant"
		}
		choices = append(choices, skillActivationChoice{
			binding: bindingByRef[ref], candidate: candidateByRef[ref], selector: decisionSource,
			reason: boundedSkillSelectionReason(reason),
		})
	}
	return choices, diagnostics
}

func (runtime *skillRuntime) resolveContinuationWithAI(
	ctx context.Context,
	input SkillContinuationInput,
) (SkillActivationDecision, error) {
	payload, err := json.Marshal(struct {
		Request          string                `json:"request"`
		Boundary         SkillPromptBoundary   `json:"boundary"`
		PhaseNumber      int                   `json:"phase_number"`
		PreviouslyActive []ActiveSkill         `json:"previously_active,omitempty"`
		Candidates       []SkillCatalogSummary `json:"skill_candidates"`
		Context          SkillSelectorContext  `json:"context,omitempty"`
	}{input.Request, input.Boundary, input.PhaseNumber, input.PreviouslyActive, input.Candidates, input.Context})
	if err != nil {
		return SkillActivationDecision{}, newSkillDomainError(ErrSkillIntegrity, "encode continuation selector input", SkillRef{})
	}
	return runtime.resolveActivationPayloadWithAI(ctx, payload, input.Candidates, input.Boundary, input.PhaseNumber)
}

func (runtime *skillRuntime) admitAdditionalSkills(
	ctx context.Context,
	state *SkillExecutionState,
	projected []projectedSkill,
	choices []skillActivationChoice,
	boundary SkillPromptBoundary,
	phaseNumber int,
) ([]projectedSkill, error) {
	sort.SliceStable(choices, func(i, j int) bool {
		left, right := skillActivationAdmissionRank(choices[i]), skillActivationAdmissionRank(choices[j])
		if left != right {
			return left < right
		}
		return choices[i].binding.Ref().String() < choices[j].binding.Ref().String()
	})
	for _, choice := range choices {
		debug := SkillActivationDebug{
			Sequence: len(state.Debug.Activations) + 1, Boundary: boundary,
			PhaseNumber: phaseNumber, Skill: choice.candidate.Resolved,
			Activation: choice.binding.Activation, Required: choice.binding.Required,
			Selector: choice.selector, Decision: "selected", Reason: choice.reason,
		}
		if len(state.ActiveSkills) >= runtime.config.Limits.MaxActiveSkills {
			debug.Admission = "limit_exceeded"
			state.Debug.Activations = append(state.Debug.Activations, debug)
			if choice.binding.Required {
				return projected, newSkillDomainError(ErrSkillLimitExceeded, "admit required activation", choice.binding.Ref())
			}
			continue
		}
		manifest, loadEvidence, err := runtime.loadSkillManifest(
			ctx, choice.candidate.Resolved, boundary, phaseNumber, choice.binding.Required,
		)
		load := SkillContentLoadDebug{
			Sequence: len(state.Debug.ContentLoads) + 1, Boundary: boundary,
			PhaseNumber: phaseNumber, ContentKind: "manifest", Skill: choice.candidate.Resolved,
			ExpectedHash: choice.candidate.Resolved.ManifestHash,
			CacheOutcome: loadEvidence.CacheOutcome, Source: loadEvidence.Source,
			Attempt: loadEvidence.Attempt, RetryOutcome: loadEvidence.RetryOutcome,
			ObservedHash: loadEvidence.ObservedHash,
			ByteEstimate: loadEvidence.ByteEstimate, TokenEstimate: loadEvidence.TokenEstimate,
			DurationMs: loadEvidence.DurationMs,
		}
		if err != nil {
			load.Outcome, load.DiagnosticCode = "omitted", "skill_manifest_load_failed"
			if errors.Is(err, ErrSkillIntegrity) {
				load.DiagnosticCode = "skill_manifest_hash_mismatch"
			}
			if choice.binding.Required {
				load.Outcome = "failed"
			}
			state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
			debug.Admission = "load_failed"
			state.Debug.Activations = append(state.Debug.Activations, debug)
			if choice.binding.Required {
				return projected, newRequiredSkillContentError(err, "load required manifest", choice.binding.Ref())
			}
			continue
		}
		if load.ObservedHash == "" {
			load.ObservedHash = manifest.Ref.ManifestHash
		}
		load.Outcome = "verified"
		state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
		active := ActiveSkill{
			Binding: choice.binding, Skill: choice.candidate.Resolved,
			Selector: choice.selector, Reason: choice.reason,
		}
		candidateProjection := append(cloneProjectedSkills(projected), projectedSkill{
			active: active, manifest: manifest,
		})
		mainTokens, _, totalTokens, planningDiagnostics := runtime.runtimeProjectedSkillTokenCosts(
			ctx, candidateProjection, false, boundary, phaseNumber,
		)
		responseTokens, _, synthesisTokens, responseDiagnostics := runtime.runtimeProjectedSkillTokenCosts(
			ctx, candidateProjection, true, SkillBoundarySynthesis, phaseNumber,
		)
		state.Diagnostics = append(state.Diagnostics, planningDiagnostics...)
		state.Debug.Diagnostics = append(state.Debug.Diagnostics, planningDiagnostics...)
		state.Diagnostics = append(state.Diagnostics, responseDiagnostics...)
		state.Debug.Diagnostics = append(state.Debug.Diagnostics, responseDiagnostics...)
		if mainTokens > runtime.config.Limits.MainTokenBudget ||
			responseTokens > runtime.config.Limits.SynthesisTokenBudget ||
			totalTokens > runtime.effectiveTotalTokenBudget() ||
			synthesisTokens > runtime.effectiveTotalTokenBudget() {
			debug.Admission = "budget_exceeded"
			state.Debug.Activations = append(state.Debug.Activations, debug)
			if choice.binding.Required {
				return projected, newSkillDomainError(ErrSkillLimitExceeded, "admit required skill content", choice.binding.Ref())
			}
			continue
		}
		state.ActiveSkills = append(state.ActiveSkills, active)
		projected = append(projected, projectedSkill{active: active, manifest: manifest})
		debug.Admission = "admitted"
		state.Debug.Activations = append(state.Debug.Activations, debug)
	}
	return projected, nil
}

func buildSkillSelectorContext(
	enrichments map[string]interface{},
	snapshot executionRunSnapshot,
	priorSelections []SkillResourceSelection,
	expectedCapabilities []string,
) SkillSelectorContext {
	keys := make([]string, 0, len(snapshot.AccumulatedResults))
	for key := range snapshot.AccumulatedResults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summaries := make([]SkillPriorResultSummary, 0, len(keys))
	for _, key := range keys {
		result := snapshot.AccumulatedResults[key]
		if result == nil {
			continue
		}
		summary := SkillPriorResultSummary{
			StepID: result.StepID, Agent: result.AgentName, Success: result.Success,
			Result: truncateSkillSelectorText(result.Response, 500), Error: truncateSkillSelectorText(result.Error, 200),
		}
		summaries = append(summaries, summary)
	}
	knownEnrichments := make(map[string]string)
	for _, key := range []string{
		core.EnrichmentActivityCoordination,
		core.EnrichmentUserProfile,
		core.EnrichmentRAGContext,
		core.EnrichmentConversationHistory,
	} {
		if value, ok := enrichments[key].(string); ok && value != "" {
			knownEnrichments[key] = truncateSkillSelectorText(value, 1000)
		}
	}
	if len(knownEnrichments) == 0 {
		knownEnrichments = nil
	}
	return SkillSelectorContext{
		Objective:               truncateSkillSelectorText(snapshot.ContinuationNote, 500),
		ExpectedCapabilities:    append([]string(nil), expectedCapabilities...),
		PriorResults:            summaries,
		ExecutedStepIDs:         append([]string(nil), snapshot.ExecutedStepIDs...),
		Enrichments:             knownEnrichments,
		PriorResourceSelections: append([]SkillResourceSelection(nil), priorSelections...),
	}
}

func truncateSkillSelectorText(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "..."
}

func (runtime *skillRuntime) resolveActivationPayloadWithAI(
	ctx context.Context,
	payload []byte,
	candidates []SkillCatalogSummary,
	boundary SkillPromptBoundary,
	phaseNumber int,
) (SkillActivationDecision, error) {
	if runtime.aiClient == nil {
		return SkillActivationDecision{}, ErrSkillUnavailable
	}
	systemPrompt := composeSkillTaskSystemPrompt(
		skillActivationSystemPrompt,
		runtime.guidance.Activation,
	)
	options := mergeAIOptions(runtime.defaultActivationAIOptions(systemPrompt), runtime.activationOptions)
	prompt := "<selector_input>\n" + string(payload) + "\n</selector_input>\n\nReturn the JSON object now."
	var lastErr error
	for attempt := 1; attempt <= skillSelectorMaxAttempts; attempt++ {
		result, err := runtime.invokeSkillAI(
			ctx,
			aiInvocation{Purpose: "skill_activation_selection", Prompt: prompt, Options: options},
			boundary,
			phaseNumber,
			attempt,
		)
		if err != nil {
			return SkillActivationDecision{}, err
		}
		if result == nil || result.Response == nil {
			return SkillActivationDecision{}, ErrSkillUnavailable
		}
		core.RecordTokenUsage(ctx, skillActivationAIPurpose, result.Response.Usage)
		decision, err := parseSkillActivationSelection(result.Response.Content, candidates)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		prompt = "<selector_input>\n" + string(payload) + "\n</selector_input>\n\n" +
			"Your previous response did not match the output contract. Return only the required JSON object."
	}
	return SkillActivationDecision{}, lastErr
}

func (runtime *skillRuntime) defaultActivationAIOptions(systemPrompt string) *core.AIOptions {
	// Structured output is enforced by the prompt, strict parser, and bounded
	// correction retry. Provider-native response-format values are deliberately
	// left unset because their wire vocabulary is not provider-neutral.
	return &core.AIOptions{
		Temperature: 0.01, MaxTokens: runtime.config.Limits.ResolutionMaxTokens,
		SystemPrompt: systemPrompt,
	}
}
