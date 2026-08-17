package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

const skillResourceSystemPrompt = `<identity>
You select detailed resources for active skills at one orchestration boundary.
</identity>

<rules>
1. Select only resources listed in <resource_candidates>.
2. Use each resource description, load_when guidance, and boundary applicability as data.
3. Select a resource only when its detailed content is needed for the current request and boundary.
4. Prefer the smallest sufficient set.
</rules>

<output_contract>
Return one JSON object with exactly one selected_resources array. Each item has namespace, name, resource, and a concise reason.
Example: {"selected_resources":[{"namespace":"travel","name":"weather-assessment","resource":"flood-guidance","reason":"The request identifies a flood warning."}]}
</output_contract>`

type skillResourceSelectionOutput struct {
	SelectedResources []skillResourceSelectionItem `json:"selected_resources"`
}

type skillResourceSelectionItem struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Resource  string `json:"resource"`
	Reason    string `json:"reason"`
}

type skillResourceChoice struct {
	entry    SkillResourceCatalogEntry
	selector SkillDecisionSource
	reason   SkillSelectionReason
}

func (runtime *skillRuntime) selectAndAdmitResources(
	ctx context.Context,
	request string,
	boundary SkillPromptBoundary,
	phaseNumber int,
	state *SkillExecutionState,
	projected []projectedSkill,
	selectorContext SkillSelectorContext,
) ([]projectedSkill, error) {
	scope, found := skillResourceScopeForBoundary(boundary)
	if !found || len(eligibleSkillResourceEntries(projected, scope)) == 0 {
		return projected, nil
	}
	operationCtx, observation := runtime.startSkillOperation(
		ctx, "resolve_resources", boundary, phaseNumber,
	)
	result, err := runtime.selectAndAdmitResourcesCore(
		operationCtx, request, boundary, phaseNumber, state, projected, selectorContext,
	)
	observation.Finish("selected", err)
	return result, err
}

func (runtime *skillRuntime) selectAndAdmitResourcesCore(
	ctx context.Context,
	request string,
	boundary SkillPromptBoundary,
	phaseNumber int,
	state *SkillExecutionState,
	projected []projectedSkill,
	selectorContext SkillSelectorContext,
) ([]projectedSkill, error) {
	if state == nil || len(projected) == 0 {
		return projected, nil
	}
	scope, found := skillResourceScopeForBoundary(boundary)
	if !found {
		return projected, nil
	}
	entries := eligibleSkillResourceEntries(projected, scope)
	if len(entries) == 0 {
		return projected, nil
	}
	entryByKey := make(map[string]SkillResourceCatalogEntry, len(entries))
	for _, entry := range entries {
		entryByKey[skillResourceRequestKey(SkillResourceRequest{
			Skill: entry.Skill.Ref, Name: entry.Resource.Name,
		})] = entry
	}

	choices := make([]skillResourceChoice, 0)
	selected := make(map[string]struct{})
	for _, trustedRequest := range state.Pinned.TrustedResourceRequests {
		key := skillResourceRequestKey(trustedRequest)
		entry, found := entryByKey[key]
		if !found {
			ref := trustedRequest.Skill
			diagnostic := SkillDiagnostic{
				Code: "skill_trusted_resource_ineligible", Boundary: boundary,
				PhaseNumber: phaseNumber, Skill: &ref, Resource: trustedRequest.Name, Action: "ignored",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
			continue
		}
		choices = append(choices, skillResourceChoice{
			entry: entry, selector: SkillDecisionTrusted, reason: "trusted host resource request",
		})
		selected[key] = struct{}{}
	}

	remaining := make([]SkillResourceCatalogEntry, 0, len(entries))
	for _, entry := range entries {
		key := skillResourceRequestKey(SkillResourceRequest{Skill: entry.Skill.Ref, Name: entry.Resource.Name})
		if _, found := selected[key]; !found {
			remaining = append(remaining, entry)
		}
	}
	if len(remaining) > 0 {
		catalogTokens, catalogFallback := runtime.runtimeSkillTokenEstimate(ctx, mustMarshalSkillValue(remaining))
		if catalogFallback {
			diagnostic := skillTokenCounterFallbackDiagnostic(boundary, phaseNumber)
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
		}
		input := SkillResourceResolutionInput{
			Request: request, Boundary: boundary, PhaseNumber: phaseNumber,
			ActiveSkills: append([]ActiveSkill(nil), state.ActiveSkills...),
			Resources:    cloneSkillResourceCatalogEntries(remaining),
			Context:      selectorContext,
		}
		var decision SkillResourceDecision
		var resolverErr error
		decisionSource := SkillDecisionDefaultAI
		switch {
		case !isNilBackendValue(runtime.resourceResolver):
			decisionSource = SkillDecisionCustomPolicy
			decision, resolverErr = runtime.resourceResolver.Resolve(ctx, input)
		case len(remaining) > runtime.config.Limits.MaxResourceCandidates ||
			catalogTokens > runtime.config.Limits.ResourceCatalogTokenBudget:
			diagnostic := SkillDiagnostic{
				Code: "skill_resource_catalog_budget_exceeded", Boundary: boundary,
				PhaseNumber: phaseNumber, Action: "optional_resource_selection_skipped",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
		default:
			decision, resolverErr = runtime.resolveResourcesWithAI(ctx, input)
		}
		if resolverErr != nil {
			diagnostic := SkillDiagnostic{
				Code: classifySkillResourceSelectorFailure(resolverErr), Boundary: boundary,
				PhaseNumber: phaseNumber, Action: "optional_resource_selection_skipped",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
		} else if len(decision.Select) > 0 {
			validated, validationErr := validateSkillResourceDecision(remaining, decision)
			if validationErr != nil {
				diagnostic := SkillDiagnostic{
					Code: "skill_resource_selection_invalid", Boundary: boundary,
					PhaseNumber: phaseNumber, Action: "optional_resource_selection_skipped",
				}
				state.Diagnostics = append(state.Diagnostics, diagnostic)
				state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
			} else {
				for _, request := range validated {
					key := skillResourceRequestKey(request)
					reason := decision.Reasons[key]
					if reason == "" {
						reason = "selected as relevant"
					}
					choices = append(choices, skillResourceChoice{
						entry: entryByKey[key], selector: decisionSource,
						reason: boundedSkillSelectionReason(reason),
					})
				}
			}
		}
	}

	return runtime.admitResources(ctx, boundary, phaseNumber, state, projected, choices)
}

func (runtime *skillRuntime) resolveResourcesWithAI(
	ctx context.Context,
	input SkillResourceResolutionInput,
) (SkillResourceDecision, error) {
	if runtime.aiClient == nil {
		return SkillResourceDecision{}, ErrSkillUnavailable
	}
	payload, err := json.Marshal(struct {
		Request   string                      `json:"request"`
		Boundary  SkillPromptBoundary         `json:"boundary"`
		Resources []SkillResourceCatalogEntry `json:"resource_candidates"`
		Context   SkillSelectorContext        `json:"context,omitempty"`
	}{Request: input.Request, Boundary: input.Boundary, Resources: input.Resources, Context: input.Context})
	if err != nil {
		return SkillResourceDecision{}, newSkillDomainError(ErrSkillIntegrity, "encode resource selector input", SkillRef{})
	}
	systemPrompt := composeSkillTaskSystemPrompt(
		skillResourceSystemPrompt,
		runtime.guidance.Resource,
	)
	// Leave provider-native response format unset. The prompt contract, strict
	// parser, and bounded retry provide the portable structured-output contract.
	options := mergeAIOptions(&core.AIOptions{
		Temperature: 0.01, MaxTokens: runtime.config.Limits.ResolutionMaxTokens,
		SystemPrompt: systemPrompt,
	}, runtime.resourceOptions)
	prompt := "<resource_selector_input>\n" + string(payload) + "\n</resource_selector_input>\n\nReturn the JSON object now."
	var lastErr error
	for attempt := 1; attempt <= skillSelectorMaxAttempts; attempt++ {
		result, callErr := runtime.invokeSkillAI(
			ctx,
			aiInvocation{Purpose: "skill_resource_selection", Prompt: prompt, Options: options},
			input.Boundary,
			input.PhaseNumber,
			attempt,
		)
		if callErr != nil {
			return SkillResourceDecision{}, callErr
		}
		if result == nil || result.Response == nil {
			return SkillResourceDecision{}, ErrSkillUnavailable
		}
		core.RecordTokenUsage(ctx, skillResourceAIPurpose, result.Response.Usage)
		decision, parseErr := parseSkillResourceSelection(result.Response.Content, input.Resources)
		if parseErr == nil {
			return decision, nil
		}
		lastErr = parseErr
		prompt = "<resource_selector_input>\n" + string(payload) + "\n</resource_selector_input>\n\n" +
			"Your previous response did not match the output contract. Return only the required JSON object."
	}
	return SkillResourceDecision{}, lastErr
}

func parseSkillResourceSelection(
	value string,
	entries []SkillResourceCatalogEntry,
) (SkillResourceDecision, error) {
	data := []byte(strings.TrimSpace(value))
	if err := rejectDuplicateSkillJSONFields(data); err != nil {
		return SkillResourceDecision{}, ErrInvalidSkillPackage
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var output skillResourceSelectionOutput
	if err := decoder.Decode(&output); err != nil {
		return SkillResourceDecision{}, ErrInvalidSkillPackage
	}
	if err := ensureSkillJSONEOF(decoder); err != nil {
		return SkillResourceDecision{}, err
	}
	allowed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		allowed[skillResourceRequestKey(SkillResourceRequest{
			Skill: entry.Skill.Ref, Name: entry.Resource.Name,
		})] = struct{}{}
	}
	decision := SkillResourceDecision{Reasons: make(map[string]SkillSelectionReason)}
	seen := make(map[string]struct{}, len(output.SelectedResources))
	for _, item := range output.SelectedResources {
		request := SkillResourceRequest{
			Skill: SkillRef{Namespace: item.Namespace, Name: item.Name}, Name: item.Resource,
		}
		key := skillResourceRequestKey(request)
		if _, found := allowed[key]; !found || !validSkillSlug(item.Namespace, 64) ||
			!validSkillSlug(item.Name, 64) || !validSkillSlug(item.Resource, 64) {
			return SkillResourceDecision{}, ErrInvalidSkillPackage
		}
		if _, found := seen[key]; found {
			return SkillResourceDecision{}, ErrInvalidSkillPackage
		}
		seen[key] = struct{}{}
		decision.Select = append(decision.Select, request)
		decision.Reasons[key] = boundedSkillSelectionReason(SkillSelectionReason(item.Reason))
	}
	return decision, nil
}

func (runtime *skillRuntime) admitResources(
	ctx context.Context,
	boundary SkillPromptBoundary,
	phaseNumber int,
	state *SkillExecutionState,
	projected []projectedSkill,
	choices []skillResourceChoice,
) ([]projectedSkill, error) {
	sort.Slice(choices, func(i, j int) bool {
		left, right := choices[i].entry, choices[j].entry
		if left.Resource.RequiredWhenSelected != right.Resource.RequiredWhenSelected {
			return left.Resource.RequiredWhenSelected
		}
		if left.Skill.Ref != right.Skill.Ref {
			return left.Skill.Ref.String() < right.Skill.Ref.String()
		}
		return left.Resource.Name < right.Resource.Name
	})
	projectedIndex := make(map[SkillRef]int, len(projected))
	for index, skill := range projected {
		projectedIndex[skill.active.Skill.Ref] = index
	}
	distinctExecutionResources := make(map[SkillResourceRef]struct{}, len(state.ResourceSelections))
	for _, selection := range state.ResourceSelections {
		distinctExecutionResources[selection.Resource] = struct{}{}
	}
	selectedThisBoundary := 0
	for _, choice := range choices {
		entry := choice.entry
		ref := SkillResourceRef{
			Skill: entry.Skill, Name: entry.Resource.Name, ExpectedHash: entry.Resource.ResourceHash,
		}
		_, selectedEarlier := distinctExecutionResources[ref]
		debug := SkillResourceSelectionDebug{
			Sequence: len(state.Debug.ResourceSelections) + 1,
			Boundary: boundary, PhaseNumber: phaseNumber,
			Resource: ref, Eligibility: "eligible", Selector: choice.selector,
			Decision: "selected", RequiredWhenSelected: entry.Resource.RequiredWhenSelected,
			Reason: choice.reason,
		}
		if selectedThisBoundary >= runtime.config.Limits.MaxResourcesPerPhase ||
			!selectedEarlier && len(distinctExecutionResources) >= runtime.config.Limits.MaxResourcesPerExecution {
			debug.Admission = "limit_exceeded"
			state.Debug.ResourceSelections = append(state.Debug.ResourceSelections, debug)
			if entry.Resource.RequiredWhenSelected {
				return projected, newSkillDomainError(ErrSkillLimitExceeded, "admit required resource", entry.Skill.Ref)
			}
			state.addSkillResourceDiagnostic("skill_resource_limit_exceeded", boundary, entry, phaseNumber, "omitted")
			continue
		}
		resource, loadEvidence, loadErr := runtime.loadSkillResource(
			ctx, ref, boundary, phaseNumber, entry.Resource.RequiredWhenSelected,
		)
		load := SkillContentLoadDebug{
			Sequence: len(state.Debug.ContentLoads) + 1,
			Boundary: boundary, PhaseNumber: phaseNumber,
			ContentKind: "resource", Skill: entry.Skill, ResourceName: entry.Resource.Name,
			ExpectedHash: entry.Resource.ResourceHash,
			CacheOutcome: loadEvidence.CacheOutcome, Source: loadEvidence.Source,
			Attempt: loadEvidence.Attempt, RetryOutcome: loadEvidence.RetryOutcome,
			ObservedHash: loadEvidence.ObservedHash,
			ByteEstimate: loadEvidence.ByteEstimate, TokenEstimate: loadEvidence.TokenEstimate,
			DurationMs: loadEvidence.DurationMs,
		}
		if loadErr != nil {
			load.Outcome = "omitted"
			load.DiagnosticCode = "skill_resource_load_failed"
			if errors.Is(loadErr, ErrSkillIntegrity) {
				load.DiagnosticCode = "skill_resource_hash_mismatch"
			}
			if entry.Resource.RequiredWhenSelected {
				load.Outcome = "failed"
			}
			state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
			debug.Admission = "load_failed"
			state.Debug.ResourceSelections = append(state.Debug.ResourceSelections, debug)
			if entry.Resource.RequiredWhenSelected {
				return projected, newRequiredSkillContentError(loadErr, "load required resource", entry.Skill.Ref)
			}
			state.addSkillResourceDiagnostic("skill_resource_load_failed", boundary, entry, phaseNumber, "omitted")
			continue
		}
		if load.ObservedHash == "" {
			observedHash, hashErr := ComputeSkillResourceHash(resource)
			if hashErr == nil {
				load.ObservedHash = observedHash
			}
		}
		load.Outcome = "verified"
		state.Debug.ContentLoads = append(state.Debug.ContentLoads, load)
		index := projectedIndex[entry.Skill.Ref]
		candidateProjection := cloneProjectedSkills(projected)
		candidateProjection[index].resources = append(candidateProjection[index].resources, resource)
		mainTokens, resourceTokens, totalTokens, diagnostics := runtime.runtimeProjectedSkillTokenCosts(
			ctx, candidateProjection, boundary == SkillBoundarySynthesis, boundary, phaseNumber,
		)
		state.Diagnostics = append(state.Diagnostics, diagnostics...)
		state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostics...)
		if resourceTokens > runtime.config.Limits.ResourceTokenBudget ||
			totalTokens > runtime.effectiveTotalTokenBudget() ||
			(boundary == SkillBoundarySynthesis && totalTokens > runtime.config.Limits.SynthesisTokenBudget) ||
			(boundary != SkillBoundarySynthesis && mainTokens > runtime.config.Limits.MainTokenBudget) {
			debug.Admission = "budget_exceeded"
			state.Debug.ResourceSelections = append(state.Debug.ResourceSelections, debug)
			if entry.Resource.RequiredWhenSelected {
				return projected, newSkillDomainError(ErrSkillLimitExceeded, "admit required resource content", entry.Skill.Ref)
			}
			state.addSkillResourceDiagnostic("skill_resource_budget_exceeded", boundary, entry, phaseNumber, "omitted")
			continue
		}
		projected[index].resources = append(projected[index].resources, resource)
		selection := SkillResourceSelection{
			Resource: ref, Boundary: boundary, PhaseNumber: phaseNumber,
			Selector: choice.selector, Reason: choice.reason,
			RequiredWhenSelected: entry.Resource.RequiredWhenSelected,
		}
		state.ResourceSelections = append(state.ResourceSelections, selection)
		distinctExecutionResources[ref] = struct{}{}
		selectedThisBoundary++
		debug.Admission = "admitted"
		state.Debug.ResourceSelections = append(state.Debug.ResourceSelections, debug)
	}
	return projected, nil
}

func eligibleSkillResourceEntries(
	projected []projectedSkill,
	scope SkillResourceScope,
) []SkillResourceCatalogEntry {
	entries := make([]SkillResourceCatalogEntry, 0)
	for _, skill := range projected {
		for _, resource := range skill.manifest.Resources {
			if len(resource.AppliesTo) > 0 && !containsSkillResourceScope(resource.AppliesTo, scope) {
				continue
			}
			entries = append(entries, SkillResourceCatalogEntry{Skill: skill.active.Skill, Resource: resource})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Skill.Ref != entries[j].Skill.Ref {
			return entries[i].Skill.Ref.String() < entries[j].Skill.Ref.String()
		}
		return entries[i].Resource.Name < entries[j].Resource.Name
	})
	return entries
}

func containsSkillResourceScope(scopes []SkillResourceScope, wanted SkillResourceScope) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func skillResourceScopeForBoundary(boundary SkillPromptBoundary) (SkillResourceScope, bool) {
	switch boundary {
	case SkillBoundaryInitialPlanning:
		return SkillResourcePlanning, true
	case SkillBoundaryContinuation, SkillBoundaryResume:
		return SkillResourceContinuation, true
	case SkillBoundarySynthesis:
		return SkillResourceSynthesis, true
	default:
		return "", false
	}
}

func cloneSkillResourceCatalogEntries(values []SkillResourceCatalogEntry) []SkillResourceCatalogEntry {
	cloned := append([]SkillResourceCatalogEntry(nil), values...)
	for index := range cloned {
		cloned[index].Resource.AppliesTo = append([]SkillResourceScope(nil), values[index].Resource.AppliesTo...)
	}
	return cloned
}

func validateSkillResourceDecision(
	entries []SkillResourceCatalogEntry,
	decision SkillResourceDecision,
) ([]SkillResourceRequest, error) {
	allowed := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		allowed[skillResourceRequestKey(SkillResourceRequest{Skill: entry.Skill.Ref, Name: entry.Resource.Name})] = struct{}{}
	}
	seen := make(map[string]struct{}, len(decision.Select))
	validated := make([]SkillResourceRequest, 0, len(decision.Select))
	for _, request := range decision.Select {
		key := skillResourceRequestKey(request)
		if _, found := allowed[key]; !found {
			return nil, ErrInvalidSkillPackage
		}
		if _, found := seen[key]; found {
			return nil, ErrInvalidSkillPackage
		}
		seen[key] = struct{}{}
		validated = append(validated, request)
	}
	return validated, nil
}

func skillResourceRequestKey(request SkillResourceRequest) string {
	return request.Skill.String() + "/" + request.Name
}

func (state *SkillExecutionState) addSkillResourceDiagnostic(
	code string,
	boundary SkillPromptBoundary,
	entry SkillResourceCatalogEntry,
	phaseNumber int,
	action string,
) {
	ref := entry.Skill.Ref
	diagnostic := SkillDiagnostic{
		Code: code, Boundary: boundary, PhaseNumber: phaseNumber,
		Skill: &ref, Resource: entry.Resource.Name, Action: action,
	}
	state.Diagnostics = append(state.Diagnostics, diagnostic)
	state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
}

func classifySkillResourceSelectorFailure(err error) string {
	if err == nil {
		return ""
	}
	var featureErr *core.AIRequestFeatureError
	if errors.As(err, &featureErr) {
		return "skill_ai_request_feature_unsupported"
	}
	if errors.Is(err, ErrInvalidSkillPackage) {
		return "skill_resource_selection_invalid"
	}
	return "skill_resource_selection_failed"
}
