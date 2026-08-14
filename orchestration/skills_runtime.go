package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	reservedSkillCacheDimension = "skills"

	skillBindingProjectionVersion        = "truvag3.skill-bindings/v1"
	skillBudgetProjectionVersion         = "truvag3.skill-runtime-budgets/v1"
	skillCandidateCacheProjectionVersion = "truvag3.skill-candidate-cache/v1"
	skillActivationSelectorPolicyVersion = "truvag3.skill-activation-selector/v1"
	skillResourceSelectorPolicyVersion   = "truvag3.skill-resource-selector/v1"
	skillEstimationPolicyVersion         = "truvag3.skill-runtime-token-counter/v1"
	skillCapabilityHintPolicyVersion     = "truvag3.skill-capability-hints/v1"
	skillBudgetPolicyVersion             = "truvag3.skill-admission/v1"
	skillProjectionCompilerVersion       = "truvag3.skill-projection/v1"
	skillInputEncoderPolicyVersion       = "skill_input_json_v1"
	maxTrustedSkillActivations           = 32
	maxTrustedSkillResourceRequests      = 32
	maxSkillExpectedCapabilities         = 32
)

type skillRuntime struct {
	config             SkillConfig
	registry           SkillRegistry
	agentDomain        string
	activationOptions  *AIOptionsOverride
	resourceOptions    *AIOptionsOverride
	guidance           SkillPromptGuidance
	aiClient           core.AIClient
	activationPolicy   SkillActivationPolicy
	customResolver     SkillResolver
	resourceResolver   SkillResourceResolver
	tokenCounter       core.TokenCounter
	telemetry          core.Telemetry
	logger             core.Logger
	debugRecorder      func(context.Context, LLMInteraction)
	bindingFingerprint string
	budgetFingerprint  string
	policyDebug        SkillRuntimePolicyDebug
}

type trustedSkillActivationsContextKey struct{}
type trustedSkillResourcesContextKey struct{}
type skillExpectedCapabilitiesContextKey struct{}
type skillExecutionStateContextKey struct{}
type skillExecutionHolderContextKey struct{}
type checkpointSkillStateContextKey struct{}

// skillFreeCheckpointResumeContextKey is an internal provenance marker set
// only by BuildResumeContext. It distinguishes a checkpoint that genuinely
// predates skills (or carries an explicitly empty compatibility snapshot) from
// a new request whose caller merely set the public resume-mode flag.
type skillFreeCheckpointResumeContextKey struct{}

type checkpointSkillState struct {
	state        SkillExecutionState
	cacheContext *SkillCacheContext
}

type skillExecutionStateHolder struct {
	mu           sync.RWMutex
	state        SkillExecutionState
	cacheContext SkillCacheContext
}

func newSkillExecutionStateHolder(
	state SkillExecutionState,
	cacheContext SkillCacheContext,
) *skillExecutionStateHolder {
	return &skillExecutionStateHolder{
		state: cloneSkillExecutionState(state), cacheContext: cacheContext,
	}
}

func (holder *skillExecutionStateHolder) Store(state SkillExecutionState) {
	if holder == nil {
		return
	}
	holder.mu.Lock()
	holder.state = cloneSkillExecutionState(state)
	holder.mu.Unlock()
}

func (holder *skillExecutionStateHolder) Snapshot() (SkillExecutionState, SkillCacheContext) {
	if holder == nil {
		return SkillExecutionState{}, SkillCacheContext{}
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return cloneSkillExecutionState(holder.state), holder.cacheContext
}

func withSkillExecutionHolder(
	ctx context.Context,
	holder *skillExecutionStateHolder,
) context.Context {
	return context.WithValue(ctx, skillExecutionHolderContextKey{}, holder)
}

func skillExecutionHolderFromContext(ctx context.Context) (*skillExecutionStateHolder, bool) {
	if ctx == nil {
		return nil, false
	}
	holder, ok := ctx.Value(skillExecutionHolderContextKey{}).(*skillExecutionStateHolder)
	return holder, ok && holder != nil
}

func withCheckpointSkillState(
	ctx context.Context,
	state SkillExecutionState,
	cacheContext *SkillCacheContext,
) context.Context {
	var cacheCopy *SkillCacheContext
	if cacheContext != nil {
		copy := *cacheContext
		cacheCopy = &copy
	}
	return context.WithValue(ctx, checkpointSkillStateContextKey{}, checkpointSkillState{
		state: cloneSkillExecutionState(state), cacheContext: cacheCopy,
	})
}

func checkpointSkillStateFromContext(
	ctx context.Context,
) (SkillExecutionState, *SkillCacheContext, bool) {
	if ctx == nil {
		return SkillExecutionState{}, nil, false
	}
	value, ok := ctx.Value(checkpointSkillStateContextKey{}).(checkpointSkillState)
	if !ok {
		return SkillExecutionState{}, nil, false
	}
	var cacheCopy *SkillCacheContext
	if value.cacheContext != nil {
		copy := *value.cacheContext
		cacheCopy = &copy
	}
	return cloneSkillExecutionState(value.state), cacheCopy, true
}

func withSkillFreeCheckpointResume(ctx context.Context) context.Context {
	return context.WithValue(ctx, skillFreeCheckpointResumeContextKey{}, true)
}

func isSkillFreeCheckpointResume(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(skillFreeCheckpointResumeContextKey{}).(bool)
	return value
}

// WithTrustedSkillActivations attaches a bounded, canonical, body-free host
// request. Ordinary request metadata and user text never populate this value.
func WithTrustedSkillActivations(ctx context.Context, refs ...SkillRef) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: trusted skill activation context is nil", ErrInvalidSkillPackage)
	}
	if len(refs) > maxTrustedSkillActivations {
		return nil, fmt.Errorf("%w: too many trusted skill activations", ErrSkillLimitExceeded)
	}
	normalized, err := normalizeTrustedSkillRefs(refs)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, trustedSkillActivationsContextKey{}, normalized), nil
}

// WithTrustedSkillResourceRequests attaches bounded body-free host requests.
// Runtime code still revalidates every request against a bound active manifest.
func WithTrustedSkillResourceRequests(
	ctx context.Context,
	requests ...SkillResourceRequest,
) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: trusted skill resource context is nil", ErrInvalidSkillPackage)
	}
	if len(requests) > maxTrustedSkillResourceRequests {
		return nil, fmt.Errorf("%w: too many trusted skill resource requests", ErrSkillLimitExceeded)
	}
	normalized := append([]SkillResourceRequest(nil), requests...)
	seen := make(map[string]struct{}, len(normalized))
	for _, request := range normalized {
		if !validSkillSlug(request.Skill.Namespace, 64) || !validSkillSlug(request.Skill.Name, 64) ||
			!validSkillSlug(request.Name, 64) {
			return nil, fmt.Errorf("%w: trusted skill resource identity is invalid", ErrInvalidSkillPackage)
		}
		key := request.Skill.String() + "\x00" + request.Name
		if _, found := seen[key]; found {
			return nil, fmt.Errorf("%w: duplicate trusted skill resource request", ErrInvalidSkillPackage)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Skill != normalized[j].Skill {
			return normalized[i].Skill.String() < normalized[j].Skill.String()
		}
		return normalized[i].Name < normalized[j].Name
	})
	return context.WithValue(ctx, trustedSkillResourcesContextKey{}, normalized), nil
}

// WithSkillExpectedCapabilities adds bounded request-local capability hints
// for continuation/resource selection. Hints are selector data only and never
// widen the agent's discovered capability set or tool permissions.
func WithSkillExpectedCapabilities(ctx context.Context, capabilities ...string) (context.Context, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: expected capability context is nil", ErrInvalidSkillPackage)
	}
	if len(capabilities) > maxSkillExpectedCapabilities {
		return nil, fmt.Errorf("%w: too many expected capabilities", ErrSkillLimitExceeded)
	}
	seen := make(map[string]struct{}, len(capabilities))
	normalized := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if !validBoundedSkillText(capability, 128) {
			return nil, fmt.Errorf("%w: invalid expected capability", ErrInvalidSkillPackage)
		}
		if _, found := seen[capability]; found {
			continue
		}
		seen[capability] = struct{}{}
		normalized = append(normalized, capability)
	}
	sort.Strings(normalized)
	return context.WithValue(ctx, skillExpectedCapabilitiesContextKey{}, normalized), nil
}

func skillExpectedCapabilitiesFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	values, _ := ctx.Value(skillExpectedCapabilitiesContextKey{}).([]string)
	return append([]string(nil), values...)
}

func newSkillRuntime(
	config *OrchestratorConfig,
	registry SkillRegistry,
	aiClient core.AIClient,
) (*skillRuntime, error) {
	if config == nil || isNilBackendValue(registry) {
		return nil, fmt.Errorf("%w: skill runtime dependencies are incomplete", ErrInvalidOrchestratorConfig)
	}
	bindingFingerprint, err := ComputeSkillBindingFingerprint(config.Skills.Bindings)
	if err != nil {
		return nil, err
	}
	budgetFingerprint, err := ComputeSkillRuntimeBudgetFingerprint(config.Skills)
	if err != nil {
		return nil, err
	}
	activationGuidanceFingerprint, err := computeSkillGuidanceFingerprint("activation", config.SkillPromptGuidance.Activation)
	if err != nil {
		return nil, err
	}
	resourceGuidanceFingerprint, err := computeSkillGuidanceFingerprint("resource", config.SkillPromptGuidance.Resource)
	if err != nil {
		return nil, err
	}
	hasCustomRuntime := !isNilBackendValue(config.SkillActivationPolicy) ||
		!isNilBackendValue(config.SkillResolver) || !isNilBackendValue(config.SkillResourceResolver) ||
		!isNilBackendValue(config.SkillTokenCounter)
	responseCacheEligible := config.Skills.RuntimePolicyID != "" ||
		!hasCustomRuntime && explicitSkillModel(config.SkillActivationAIOptions) &&
			explicitSkillModel(config.SkillResourceAIOptions)
	tokenCounter := config.SkillTokenCounter
	if isNilBackendValue(tokenCounter) {
		tokenCounter = HeuristicTokenCounter{}
	}
	return &skillRuntime{
		config:            cloneSkillConfig(config.Skills),
		registry:          registry,
		agentDomain:       strings.ToLower(strings.TrimSpace(config.PromptConfig.Domain)),
		activationOptions: cloneAIOptionsOverride(config.SkillActivationAIOptions),
		resourceOptions:   cloneAIOptionsOverride(config.SkillResourceAIOptions),
		guidance: SkillPromptGuidance{
			Activation: normalizeSkillMetadataText(config.SkillPromptGuidance.Activation),
			Resource:   normalizeSkillMetadataText(config.SkillPromptGuidance.Resource),
		},
		aiClient:           aiClient,
		activationPolicy:   config.SkillActivationPolicy,
		customResolver:     config.SkillResolver,
		resourceResolver:   config.SkillResourceResolver,
		tokenCounter:       tokenCounter,
		bindingFingerprint: bindingFingerprint,
		budgetFingerprint:  budgetFingerprint,
		policyDebug: SkillRuntimePolicyDebug{
			ActivationSelectorPolicyVersion: skillActivationSelectorPolicyVersion,
			ResourceSelectorPolicyVersion:   skillResourceSelectorPolicyVersion,
			TokenCounterPolicyVersion:       skillEstimationPolicyVersion,
			CapabilityHintPolicyVersion:     skillCapabilityHintPolicyVersion,
			BudgetPolicyVersion:             skillBudgetPolicyVersion,
			ProjectionCompilerVersion:       skillProjectionCompilerVersion,
			InputEncoderVersion:             skillInputEncoderPolicyVersion,
			RuntimePolicyID:                 config.Skills.RuntimePolicyID,
			ActivationGuidanceFingerprint:   activationGuidanceFingerprint,
			ResourceGuidanceFingerprint:     resourceGuidanceFingerprint,
			DomainCompatibilityMode:         string(config.Skills.DomainCompatibilityMode),
			ResponseCacheEligible:           responseCacheEligible,
		},
	}, nil
}

func resolveSkillContentCache(config *OrchestratorConfig) (SkillContentCache, error) {
	if !isNilBackendValue(config.SkillContentCache) {
		return config.SkillContentCache, nil
	}
	switch config.Skills.Cache.Mode {
	case SkillContentCacheLocal:
		return NewByteLRUSkillContentCache(config.Skills.Cache.MaxBytes)
	case SkillContentCacheDisabled:
		return NoOpSkillContentCache{}, nil
	default:
		return nil, fmt.Errorf("%w: skills cache mode is invalid", ErrInvalidOrchestratorConfig)
	}
}

func (runtime *skillRuntime) PinCandidates(
	ctx context.Context,
) (state SkillExecutionState, cacheContext SkillCacheContext, err error) {
	ctx, observation := runtime.startSkillOperation(ctx, "pin_candidates", "", 0)
	defer func() { observation.Finish("resolved", err) }()
	if runtime == nil {
		return SkillExecutionState{}, SkillCacheContext{}, nil
	}
	if err := ctx.Err(); err != nil {
		return SkillExecutionState{}, SkillCacheContext{}, err
	}
	bindings := canonicalSkillBindingOrder(runtime.config.Bindings)
	requests := make([]SkillCandidateRequest, len(bindings))
	for index, binding := range bindings {
		requests[index] = SkillCandidateRequest{Ref: binding.Ref(), RequestedVersion: binding.Version}
	}
	trustedActivations := trustedSkillActivationsFromContext(ctx)
	trustedResources := trustedSkillResourcesFromContext(ctx)
	expectedCapabilities := skillExpectedCapabilitiesFromContext(ctx)

	readCtx, cancel := context.WithTimeout(ctx, runtime.config.Limits.RegistryReadTimeout)
	readCtx, registryObservation := runtime.startSkillRegistryOperation(
		readCtx, "resolve_candidates", "", 0,
	)
	candidates, readErr := runtime.registry.ResolveCandidates(readCtx, append([]SkillCandidateRequest(nil), requests...))
	registryObservation.Finish("resolved", readErr)
	if runtime.telemetry != nil {
		outcome := "success"
		if readErr != nil {
			outcome = "error"
		}
		runtime.telemetry.RecordMetric(skillCandidateBatchMetric, float64(len(requests)), map[string]string{
			"module": telemetry.ModuleOrchestration, "boundary": "request_start", "outcome": outcome,
		})
	}
	cancel()
	providerUnavailable := readErr != nil
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) && ctx.Err() != nil {
			return SkillExecutionState{}, SkillCacheContext{}, ctx.Err()
		}
		if hasRequiredSkillBinding(bindings) {
			candidates = unavailableSkillCandidates(requests)
			state, cacheContext = runtime.partialSkillPinState(
				bindings, candidates, trustedActivations, trustedResources, expectedCapabilities, nil,
				[]SkillDiagnostic{{Code: "skill_registry_unavailable", Action: "request_failed"}},
			)
			return state, cacheContext, newSkillDomainError(ErrSkillUnavailable, "resolve candidates", SkillRef{})
		}
		candidates = unavailableSkillCandidates(requests)
	}

	normalizedCandidates, diagnostics, err := normalizeResolvedSkillCandidates(bindings, requests, candidates)
	if err != nil {
		state, cacheContext = runtime.partialSkillPinState(
			bindings, unavailableSkillCandidates(requests), trustedActivations, trustedResources, expectedCapabilities,
			nil, []SkillDiagnostic{{Code: "skill_candidate_result_invalid", Action: "request_failed"}},
		)
		return state, cacheContext, err
	}
	if providerUnavailable {
		diagnostics = append(diagnostics, SkillDiagnostic{Code: "skill_registry_unavailable", Action: "optional_candidates_omitted"})
	}
	domainOutcomes, domainDiagnostics, err := runtime.applyDomainCompatibility(bindings, normalizedCandidates)
	diagnostics = append(diagnostics, domainDiagnostics...)
	if err != nil {
		state, cacheContext = runtime.partialSkillPinState(
			bindings, normalizedCandidates, trustedActivations, trustedResources, expectedCapabilities,
			domainOutcomes, append(diagnostics, SkillDiagnostic{
				Code: "skill_domain_mismatch", Action: "request_failed",
			}),
		)
		return state, cacheContext, err
	}
	for index, candidate := range normalizedCandidates {
		if candidate.Status != SkillCandidateResolved && bindings[index].Required {
			ref := bindingCandidateRef(bindings[index])
			state, cacheContext = runtime.partialSkillPinState(
				bindings, normalizedCandidates, trustedActivations, trustedResources, expectedCapabilities,
				domainOutcomes, append(diagnostics, SkillDiagnostic{
					Code: "skill_required_candidate_unavailable", Skill: &ref, Action: "request_failed",
				}),
			)
			return state, cacheContext, newSkillDomainError(ErrSkillUnavailable, "resolve required candidate", ref)
		}
	}

	fingerprint, err := runtime.computeCandidateCacheFingerprint(
		normalizedCandidates, trustedActivations, trustedResources, expectedCapabilities, domainOutcomes,
	)
	if err != nil {
		return SkillExecutionState{}, SkillCacheContext{}, err
	}
	cacheContext = SkillCacheContext{
		Fingerprint: fingerprint, ResponseCacheEligible: runtime.policyDebug.ResponseCacheEligible,
	}
	snapshot := &SkillSnapshot{
		EffectiveBindings:          append([]SkillBinding(nil), bindings...),
		Candidates:                 cloneSkillCandidates(normalizedCandidates),
		TrustedExplicitActivations: append([]SkillRef(nil), trustedActivations...),
		TrustedResourceRequests:    append([]SkillResourceRequest(nil), trustedResources...),
		ExpectedCapabilities:       append([]string(nil), expectedCapabilities...),
		DomainOutcomes:             append([]SkillDomainCompatibilityOutcome(nil), domainOutcomes...),
		CacheFingerprint:           fingerprint,
		DebugProvenance: SkillDebugProvenance{
			BindingSource:      runtime.config.bindingSource,
			BindingFingerprint: runtime.bindingFingerprint,
			BudgetFingerprint:  runtime.budgetFingerprint,
			RuntimePolicy:      runtime.policyDebug,
		},
	}
	debug := SkillExecutionDebug{
		BindingSource:      runtime.config.bindingSource,
		BindingFingerprint: runtime.bindingFingerprint,
		BudgetFingerprint:  runtime.budgetFingerprint,
		CacheFingerprint:   fingerprint,
		RuntimePolicy:      runtime.policyDebug,
		Candidates:         skillCandidateDebugRecords(bindings, normalizedCandidates),
		Diagnostics:        append([]SkillDiagnostic(nil), diagnostics...),
	}
	return SkillExecutionState{Pinned: snapshot, Diagnostics: diagnostics, Debug: debug}, cacheContext, nil
}

func (runtime *skillRuntime) partialSkillPinState(
	bindings []SkillBinding,
	candidates []SkillCandidate,
	trustedActivations []SkillRef,
	trustedResources []SkillResourceRequest,
	expectedCapabilities []string,
	domainOutcomes []SkillDomainCompatibilityOutcome,
	diagnostics []SkillDiagnostic,
) (SkillExecutionState, SkillCacheContext) {
	fingerprint, _ := runtime.computeCandidateCacheFingerprint(
		candidates, trustedActivations, trustedResources, expectedCapabilities, domainOutcomes,
	)
	cacheContext := SkillCacheContext{
		Fingerprint:           fingerprint,
		ResponseCacheEligible: runtime.policyDebug.ResponseCacheEligible,
	}
	snapshot := &SkillSnapshot{
		EffectiveBindings:          append([]SkillBinding(nil), bindings...),
		Candidates:                 cloneSkillCandidates(candidates),
		TrustedExplicitActivations: append([]SkillRef(nil), trustedActivations...),
		TrustedResourceRequests:    append([]SkillResourceRequest(nil), trustedResources...),
		ExpectedCapabilities:       append([]string(nil), expectedCapabilities...),
		DomainOutcomes:             append([]SkillDomainCompatibilityOutcome(nil), domainOutcomes...),
		CacheFingerprint:           fingerprint,
		DebugProvenance: SkillDebugProvenance{
			BindingSource:      runtime.config.bindingSource,
			BindingFingerprint: runtime.bindingFingerprint,
			BudgetFingerprint:  runtime.budgetFingerprint,
			RuntimePolicy:      runtime.policyDebug,
		},
	}
	debug := SkillExecutionDebug{
		BindingSource:      runtime.config.bindingSource,
		BindingFingerprint: runtime.bindingFingerprint,
		BudgetFingerprint:  runtime.budgetFingerprint,
		CacheFingerprint:   fingerprint, RuntimePolicy: runtime.policyDebug,
		Candidates:  skillCandidateDebugRecords(bindings, candidates),
		Diagnostics: append([]SkillDiagnostic(nil), diagnostics...),
	}
	return SkillExecutionState{
		Pinned: snapshot, Diagnostics: append([]SkillDiagnostic(nil), diagnostics...), Debug: debug,
	}, cacheContext
}

// ResumeCandidates revalidates the exact immutable tuples captured by a HITL
// checkpoint. Current bindings provide revocation membership only; they never
// repin a suspended execution to a newly published revision.
func (runtime *skillRuntime) ResumeCandidates(
	ctx context.Context,
	checkpointState SkillExecutionState,
	priorCacheContext *SkillCacheContext,
) (state SkillExecutionState, cacheContext SkillCacheContext, err error) {
	ctx, observation := runtime.startSkillOperation(ctx, "pin_candidates", SkillBoundaryResume, 0)
	defer func() { observation.Finish("resumed", err) }()
	state = cloneSkillExecutionState(checkpointState)
	if state.Pinned == nil {
		if len(state.ActiveSkills) != 0 || len(state.UnavailableContent) != 0 ||
			len(state.ResourceSelections) != 0 {
			return state, SkillCacheContext{}, newSkillDomainError(
				ErrSkillIntegrity, "resume orphaned skill state", SkillRef{},
			)
		}
		return state, SkillCacheContext{}, nil
	}
	if err := runtime.validateResumedSkillState(state); err != nil {
		return state, SkillCacheContext{}, err
	}
	if len(state.Pinned.EffectiveBindings) == 0 {
		return state, SkillCacheContext{}, nil
	}
	currentBindings := make(map[SkillRef]struct{}, len(runtime.config.Bindings))
	for _, binding := range runtime.config.Bindings {
		currentBindings[binding.Ref()] = struct{}{}
	}
	for _, binding := range state.Pinned.EffectiveBindings {
		if _, found := currentBindings[binding.Ref()]; !found {
			ref := binding.Ref()
			diagnostic := SkillDiagnostic{
				Code: "skill_binding_revoked", Boundary: SkillBoundaryResume,
				Skill: &ref, Action: "resume_failed",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
			return state, SkillCacheContext{}, newSkillDomainError(
				ErrSkillUnavailable, "resume revoked binding", ref,
			)
		}
	}
	requests := make([]SkillCandidateRequest, 0, len(state.Pinned.Candidates))
	requestIndexes := make([]int, 0, len(state.Pinned.Candidates))
	for index, candidate := range state.Pinned.Candidates {
		if candidate.Status != SkillCandidateResolved {
			continue
		}
		requests = append(requests, SkillCandidateRequest{
			Ref:              candidate.Resolved.Ref,
			RequestedVersion: strconv.FormatUint(candidate.Resolved.Version, 10),
		})
		requestIndexes = append(requestIndexes, index)
	}
	if len(requests) > 0 {
		readCtx, cancel := context.WithTimeout(ctx, runtime.config.Limits.RegistryReadTimeout)
		readCtx, registryObservation := runtime.startSkillRegistryOperation(
			readCtx, "resolve_candidates", SkillBoundaryResume, 0,
		)
		returned, err := runtime.registry.ResolveCandidates(readCtx, append([]SkillCandidateRequest(nil), requests...))
		registryObservation.Finish("resolved", err)
		if runtime.telemetry != nil {
			outcome := "success"
			if err != nil {
				outcome = "error"
			}
			runtime.telemetry.RecordMetric(skillCandidateBatchMetric, float64(len(requests)), map[string]string{
				"module": telemetry.ModuleOrchestration, "boundary": "resume", "outcome": outcome,
			})
		}
		cancel()
		if err != nil {
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return state, SkillCacheContext{}, ctx.Err()
			}
			returned = unavailableSkillCandidates(requests)
		}
		resolvedByKey := make(map[string]SkillCandidate, len(returned))
		for _, candidate := range returned {
			key := skillCandidateKey(candidate.Ref, candidate.RequestedVersion)
			if _, duplicate := resolvedByKey[key]; duplicate {
				return state, SkillCacheContext{}, newSkillDomainError(
					ErrSkillIntegrity, "resume duplicate candidate", candidate.Ref,
				)
			}
			resolvedByKey[key] = candidate
		}
		for requestIndex, request := range requests {
			stateIndex := requestIndexes[requestIndex]
			prior := state.Pinned.Candidates[stateIndex]
			candidate, found := resolvedByKey[skillCandidateKey(request.Ref, request.RequestedVersion)]
			if found {
				delete(resolvedByKey, skillCandidateKey(request.Ref, request.RequestedVersion))
			}
			valid := found && validateResolvedSkillCandidate(request, candidate) == nil &&
				candidate.Status == SkillCandidateResolved && candidate.Resolved == prior.Resolved
			if valid {
				candidate.RequestedVersion = prior.RequestedVersion
				state.Pinned.Candidates[stateIndex] = candidate
				continue
			}
			binding := state.Pinned.EffectiveBindings[stateIndex]
			ref := binding.Ref()
			diagnostic := SkillDiagnostic{
				Code: "skill_version_unavailable", Boundary: SkillBoundaryResume,
				Skill: &ref, Action: "content_omitted",
			}
			state.Diagnostics = append(state.Diagnostics, diagnostic)
			state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
			if binding.Required {
				return state, SkillCacheContext{}, newSkillDomainError(
					ErrSkillUnavailable, "resume required version", ref,
				)
			}
			if !skillVersionRefInList(state.UnavailableContent, prior.Resolved) {
				state.UnavailableContent = append(state.UnavailableContent, prior.Resolved)
			}
		}
		if len(resolvedByKey) != 0 {
			return state, SkillCacheContext{}, newSkillDomainError(
				ErrSkillIntegrity, "resume unexpected candidate", SkillRef{},
			)
		}
	}
	if err := runtime.validateResumedSkillState(state); err != nil {
		return state, SkillCacheContext{}, err
	}
	originalBindingFingerprint, err := ComputeSkillBindingFingerprint(state.Pinned.EffectiveBindings)
	if err != nil {
		return state, SkillCacheContext{}, err
	}
	fingerprint, err := runtime.computeCandidateCacheFingerprintWithBinding(
		originalBindingFingerprint,
		state.Pinned.Candidates,
		state.Pinned.TrustedExplicitActivations,
		state.Pinned.TrustedResourceRequests,
		state.Pinned.ExpectedCapabilities,
		state.Pinned.DomainOutcomes,
		state.UnavailableContent,
	)
	if err != nil {
		return state, SkillCacheContext{}, err
	}
	cacheContext = SkillCacheContext{
		Fingerprint:           fingerprint,
		ResponseCacheEligible: runtime.policyDebug.ResponseCacheEligible,
	}
	priorFingerprint := state.Pinned.CacheFingerprint
	if priorCacheContext != nil && priorCacheContext.Fingerprint != "" {
		priorFingerprint = priorCacheContext.Fingerprint
	}
	if priorFingerprint != "" && priorFingerprint != fingerprint {
		diagnostic := SkillDiagnostic{
			Code: "skill_resume_cache_context_changed", Boundary: SkillBoundaryResume,
			Action: "continued_with_current_context",
		}
		state.Diagnostics = append(state.Diagnostics, diagnostic)
		state.Debug.Diagnostics = append(state.Debug.Diagnostics, diagnostic)
	}
	state.Pinned.CacheFingerprint = fingerprint
	state.Pinned.DebugProvenance.BindingSource = SkillBindingsFromCheckpoint
	state.Pinned.DebugProvenance.BindingFingerprint = originalBindingFingerprint
	state.Pinned.DebugProvenance.BudgetFingerprint = runtime.budgetFingerprint
	state.Pinned.DebugProvenance.RuntimePolicy = runtime.policyDebug
	state.Debug.BindingSource = SkillBindingsFromCheckpoint
	state.Debug.BindingFingerprint = originalBindingFingerprint
	state.Debug.BudgetFingerprint = runtime.budgetFingerprint
	state.Debug.CacheFingerprint = fingerprint
	state.Debug.RuntimePolicy = runtime.policyDebug
	state.Debug.Candidates = skillCandidateDebugRecords(
		state.Pinned.EffectiveBindings, state.Pinned.Candidates,
	)
	return state, cacheContext, nil
}

func (runtime *skillRuntime) validateResumedSkillState(state SkillExecutionState) error {
	if state.Pinned == nil {
		return newSkillDomainError(ErrSkillIntegrity, "resume missing snapshot", SkillRef{})
	}
	bindings := state.Pinned.EffectiveBindings
	candidates := state.Pinned.Candidates
	if len(bindings) > runtime.config.Limits.MaxBindings || len(candidates) != len(bindings) {
		return newSkillDomainError(ErrSkillIntegrity, "resume candidate cardinality", SkillRef{})
	}
	bindingFingerprint, err := ComputeSkillBindingFingerprint(bindings)
	if err != nil {
		return newSkillDomainError(ErrSkillIntegrity, "resume binding fingerprint", SkillRef{})
	}
	if checkpointFingerprint := state.Pinned.DebugProvenance.BindingFingerprint; checkpointFingerprint != "" && checkpointFingerprint != bindingFingerprint {
		return newSkillDomainError(ErrSkillIntegrity, "resume binding fingerprint", SkillRef{})
	}
	seenBindings := make(map[SkillRef]struct{}, len(bindings))
	for index, binding := range bindings {
		ref := binding.Ref()
		if !validSkillSlug(ref.Namespace, 64) || !validSkillSlug(ref.Name, 64) ||
			!validCheckpointSkillVersion(binding.Version) || !validSkillActivation(binding.Activation) {
			return newSkillDomainError(ErrSkillIntegrity, "resume binding", ref)
		}
		if _, duplicate := seenBindings[ref]; duplicate {
			return newSkillDomainError(ErrSkillIntegrity, "resume duplicate binding", ref)
		}
		seenBindings[ref] = struct{}{}
		if index > 0 && bindings[index-1].Ref().String() >= ref.String() {
			return newSkillDomainError(ErrSkillIntegrity, "resume binding order", ref)
		}
		request := SkillCandidateRequest{Ref: ref, RequestedVersion: binding.Version}
		if err := validateResolvedSkillCandidate(request, candidates[index]); err != nil {
			return newSkillDomainError(ErrSkillIntegrity, "resume candidate", ref)
		}
	}
	if len(state.Pinned.TrustedExplicitActivations) > maxTrustedSkillActivations ||
		!validCanonicalTrustedSkillRefs(state.Pinned.TrustedExplicitActivations) ||
		len(state.Pinned.TrustedResourceRequests) > maxTrustedSkillResourceRequests ||
		!validCanonicalTrustedSkillResourceRequests(state.Pinned.TrustedResourceRequests) {
		return newSkillDomainError(ErrSkillIntegrity, "resume trusted requests", SkillRef{})
	}
	if len(state.Pinned.DomainOutcomes) != len(bindings) {
		return newSkillDomainError(ErrSkillIntegrity, "resume domain outcomes", SkillRef{})
	}
	for index, outcome := range state.Pinned.DomainOutcomes {
		if outcome.Ref != bindings[index].Ref() || !validSkillDomainOutcome(outcome.Outcome) {
			return newSkillDomainError(ErrSkillIntegrity, "resume domain outcome", outcome.Ref)
		}
	}
	if len(state.Pinned.ExpectedCapabilities) > maxSkillExpectedCapabilities {
		return newSkillDomainError(ErrSkillIntegrity, "resume expected capabilities", SkillRef{})
	}
	for index, capability := range state.Pinned.ExpectedCapabilities {
		if !validBoundedSkillText(capability, 128) ||
			index > 0 && state.Pinned.ExpectedCapabilities[index-1] >= capability {
			return newSkillDomainError(ErrSkillIntegrity, "resume expected capabilities", SkillRef{})
		}
	}
	pinned := make(map[SkillVersionRef]SkillBinding, len(candidates))
	for index, candidate := range candidates {
		if candidate.Status == SkillCandidateResolved {
			pinned[candidate.Resolved] = bindings[index]
		}
	}
	unavailable := make(map[SkillVersionRef]struct{}, len(state.UnavailableContent))
	for _, ref := range state.UnavailableContent {
		if _, found := pinned[ref]; !found {
			return newSkillDomainError(ErrSkillIntegrity, "resume unavailable content", ref.Ref)
		}
		if _, duplicate := unavailable[ref]; duplicate {
			return newSkillDomainError(ErrSkillIntegrity, "resume duplicate unavailable content", ref.Ref)
		}
		unavailable[ref] = struct{}{}
	}
	if len(state.ActiveSkills) > runtime.config.Limits.MaxActiveSkills {
		return newSkillDomainError(ErrSkillIntegrity, "resume active skill limit", SkillRef{})
	}
	active := make(map[SkillVersionRef]struct{}, len(state.ActiveSkills))
	for _, skill := range state.ActiveSkills {
		binding, found := pinned[skill.Skill]
		if !found || binding != skill.Binding || !validSkillDecisionSource(skill.Selector) ||
			!validBoundedSkillText(string(skill.Reason), skillActivationReasonMaxBytes) {
			return newSkillDomainError(ErrSkillIntegrity, "resume active skill state", skill.Skill.Ref)
		}
		if _, duplicate := active[skill.Skill]; duplicate {
			return newSkillDomainError(ErrSkillIntegrity, "resume duplicate active skill", skill.Skill.Ref)
		}
		active[skill.Skill] = struct{}{}
	}
	for _, selection := range state.ResourceSelections {
		if _, found := active[selection.Resource.Skill]; !found ||
			!validSkillSlug(selection.Resource.Name, 64) ||
			!validSkillSHA256(selection.Resource.ExpectedHash) ||
			!validSkillSelectionBoundary(selection.Boundary) || selection.PhaseNumber <= 0 ||
			!validSkillDecisionSource(selection.Selector) ||
			!validBoundedSkillText(string(selection.Reason), skillActivationReasonMaxBytes) {
			return newSkillDomainError(
				ErrSkillIntegrity, "resume resource selection", selection.Resource.Skill.Ref,
			)
		}
	}
	return nil
}

func validCheckpointSkillVersion(value string) bool {
	if value == "published" {
		return true
	}
	version, err := strconv.ParseUint(value, 10, 64)
	return err == nil && version > 0
}

func validSkillActivation(value SkillActivation) bool {
	switch value {
	case SkillActivationAlways, SkillActivationAuto, SkillActivationExplicit:
		return true
	default:
		return false
	}
}

func validSkillDecisionSource(value SkillDecisionSource) bool {
	switch value {
	case SkillDecisionAlways, SkillDecisionTrusted, SkillDecisionCustomPolicy, SkillDecisionDefaultAI:
		return true
	default:
		return false
	}
}

func validSkillSelectionBoundary(value SkillPromptBoundary) bool {
	switch value {
	case SkillBoundaryInitialPlanning, SkillBoundaryContinuation, SkillBoundarySynthesis, SkillBoundaryResume:
		return true
	default:
		return false
	}
}

func validSkillDomainOutcome(value string) bool {
	switch value {
	case "not_applicable", "compatible", "mismatch_warned", "mismatch_omitted":
		return true
	default:
		return false
	}
}

func validCanonicalTrustedSkillRefs(values []SkillRef) bool {
	for index, ref := range values {
		if !validSkillSlug(ref.Namespace, 64) || !validSkillSlug(ref.Name, 64) ||
			index > 0 && values[index-1].String() >= ref.String() {
			return false
		}
	}
	return true
}

func validCanonicalTrustedSkillResourceRequests(values []SkillResourceRequest) bool {
	previous := ""
	for index, request := range values {
		if !validSkillSlug(request.Skill.Namespace, 64) ||
			!validSkillSlug(request.Skill.Name, 64) || !validSkillSlug(request.Name, 64) {
			return false
		}
		current := skillResourceRequestKey(request)
		if index > 0 && previous >= current {
			return false
		}
		previous = current
	}
	return true
}

func (runtime *skillRuntime) applyDomainCompatibility(
	bindings []SkillBinding,
	candidates []SkillCandidate,
) ([]SkillDomainCompatibilityOutcome, []SkillDiagnostic, error) {
	outcomes := make([]SkillDomainCompatibilityOutcome, 0, len(candidates))
	diagnostics := make([]SkillDiagnostic, 0)
	for index, candidate := range candidates {
		outcome := SkillDomainCompatibilityOutcome{Ref: candidate.Ref, Outcome: "not_applicable"}
		if candidate.Status == SkillCandidateResolved && runtime.config.DomainCompatibilityMode != SkillDomainCompatibilityOff &&
			runtime.agentDomain != "" && len(candidate.Metadata.Domains) > 0 {
			outcome.Outcome = "compatible"
			if !containsNormalizedSkillDomain(candidate.Metadata.Domains, runtime.agentDomain) {
				outcome.Outcome = "mismatch_warned"
				diagnostic := SkillDiagnostic{Code: "skill_domain_mismatch", Skill: &candidate.Ref, Action: "honored"}
				if runtime.config.DomainCompatibilityMode == SkillDomainCompatibilityEnforce {
					outcome.Outcome = "mismatch_omitted"
					diagnostic.Action = "omitted"
					if bindings[index].Required {
						return nil, nil, newSkillDomainError(ErrSkillUnavailable, "enforce domain compatibility", candidate.Ref)
					}
					// Enforce mode removes an optional incompatible candidate from
					// the pinned set. Retain only its requested identity and terminal
					// status so no body-bearing reference can become active later.
					candidates[index] = SkillCandidate{
						Ref:              candidate.Ref,
						RequestedVersion: candidate.RequestedVersion,
						Status:           SkillCandidateUnavailable,
					}
				}
				diagnostics = append(diagnostics, diagnostic)
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, diagnostics, nil
}

func normalizeResolvedSkillCandidates(
	bindings []SkillBinding,
	requests []SkillCandidateRequest,
	returned []SkillCandidate,
) ([]SkillCandidate, []SkillDiagnostic, error) {
	keyed := make(map[string]SkillCandidate, len(returned))
	for _, candidate := range returned {
		key := skillCandidateKey(candidate.Ref, candidate.RequestedVersion)
		if _, found := keyed[key]; found {
			return nil, nil, newSkillDomainError(ErrSkillIntegrity, "resolve duplicate candidate", candidate.Ref)
		}
		keyed[key] = candidate
	}
	result := make([]SkillCandidate, len(requests))
	diagnostics := make([]SkillDiagnostic, 0)
	for index, request := range requests {
		key := skillCandidateKey(request.Ref, request.RequestedVersion)
		candidate, found := keyed[key]
		delete(keyed, key)
		if !found {
			candidate = SkillCandidate{Ref: request.Ref, RequestedVersion: request.RequestedVersion, Status: SkillCandidateUnavailable}
			diagnostics = append(diagnostics, SkillDiagnostic{Code: "skill_candidate_result_missing", Skill: &request.Ref, Action: "omitted"})
		}
		if err := validateResolvedSkillCandidate(request, candidate); err != nil {
			if bindings[index].Required {
				return nil, nil, err
			}
			candidate = SkillCandidate{Ref: request.Ref, RequestedVersion: request.RequestedVersion, Status: SkillCandidateUnavailable}
			diagnostics = append(diagnostics, SkillDiagnostic{Code: "skill_candidate_result_invalid", Skill: &request.Ref, Action: "omitted"})
		}
		result[index] = candidate
	}
	if len(keyed) > 0 {
		return nil, nil, newSkillDomainError(ErrSkillIntegrity, "resolve unexpected candidate", SkillRef{})
	}
	return result, diagnostics, nil
}

func validateResolvedSkillCandidate(request SkillCandidateRequest, candidate SkillCandidate) error {
	if candidate.Ref != request.Ref || candidate.RequestedVersion != request.RequestedVersion {
		return newSkillDomainError(ErrSkillIntegrity, "verify candidate identity", request.Ref)
	}
	switch candidate.Status {
	case SkillCandidateResolved:
		if candidate.Resolved.Ref != request.Ref || candidate.Resolved.Version == 0 ||
			!validSkillSHA256(candidate.Resolved.ManifestHash) || candidate.Metadata.Ref != request.Ref ||
			candidate.Metadata.PublishedVersion != candidate.Resolved.Version ||
			candidate.Metadata.Status != SkillPublicationPublished {
			return newSkillDomainError(ErrSkillIntegrity, "verify resolved candidate", request.Ref)
		}
		if request.RequestedVersion != "published" {
			version, err := strconv.ParseUint(request.RequestedVersion, 10, 64)
			if err != nil || version != candidate.Resolved.Version {
				return newSkillDomainError(ErrSkillIntegrity, "verify candidate revision", request.Ref)
			}
		}
	case SkillCandidateNotFound, SkillCandidateDeleted, SkillCandidateInvalidVersion, SkillCandidateUnavailable:
		if candidate.Resolved != (SkillVersionRef{}) || !skillMetadataIsEmpty(candidate.Metadata) {
			return newSkillDomainError(ErrSkillIntegrity, "verify unavailable candidate", request.Ref)
		}
	default:
		return newSkillDomainError(ErrSkillIntegrity, "verify candidate status", request.Ref)
	}
	return nil
}

func skillMetadataIsEmpty(metadata SkillMetadata) bool {
	return metadata.Ref == (SkillRef{}) && metadata.DisplayName == "" && metadata.Description == "" &&
		len(metadata.Domains) == 0 && len(metadata.Tags) == 0 && metadata.PublishedVersion == 0 &&
		metadata.Status == ""
}

// CanonicalSkillBindingsV1 is the normative deterministic binding projection.
type CanonicalSkillBindingsV1 struct {
	Schema   string         `json:"schema"`
	Bindings []SkillBinding `json:"bindings"`
}

// CanonicalSkillRuntimeBudgetsV1 is the answer-affecting runtime limit and
// compatibility projection. Cache capacity is intentionally excluded.
type CanonicalSkillRuntimeBudgetsV1 struct {
	Schema                  string                       `json:"schema"`
	DomainCompatibilityMode SkillDomainCompatibilityMode `json:"domain_compatibility_mode"`
	Limits                  SkillRuntimeLimits           `json:"limits"`
}

type canonicalSkillCandidateCacheProjectionV1 struct {
	Schema                string                            `json:"schema"`
	BindingFingerprint    string                            `json:"binding_fingerprint"`
	BudgetFingerprint     string                            `json:"budget_fingerprint"`
	Candidates            []SkillCandidate                  `json:"candidates"`
	TrustedActivations    []SkillRef                        `json:"trusted_activations,omitempty"`
	TrustedResources      []SkillResourceRequest            `json:"trusted_resources,omitempty"`
	ExpectedCapabilities  []string                          `json:"expected_capabilities,omitempty"`
	DomainOutcomes        []SkillDomainCompatibilityOutcome `json:"domain_outcomes"`
	UnavailableContent    []SkillVersionRef                 `json:"unavailable_content,omitempty"`
	RuntimePolicy         SkillRuntimePolicyDebug           `json:"runtime_policy"`
	ActivationModelIntent skillSelectorModelIntent          `json:"activation_model_intent"`
	ResourceModelIntent   skillSelectorModelIntent          `json:"resource_model_intent"`
}

type skillSelectorModelIntent struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// ComputeSkillBindingFingerprint hashes the canonical normalized binding set.
func ComputeSkillBindingFingerprint(bindings []SkillBinding) (string, error) {
	canonical := CanonicalSkillBindingsV1{
		Schema: skillBindingProjectionVersion, Bindings: canonicalSkillBindingOrder(bindings),
	}
	return hashCanonicalSkillValue(canonical)
}

// ComputeSkillRuntimeBudgetFingerprint hashes answer-affecting runtime limits
// and compatibility behavior while excluding cache/storage capacity.
func ComputeSkillRuntimeBudgetFingerprint(config SkillConfig) (string, error) {
	canonical := CanonicalSkillRuntimeBudgetsV1{
		Schema:                  skillBudgetProjectionVersion,
		DomainCompatibilityMode: config.DomainCompatibilityMode,
		Limits:                  config.Limits,
	}
	return hashCanonicalSkillValue(canonical)
}

func (runtime *skillRuntime) computeCandidateCacheFingerprint(
	candidates []SkillCandidate,
	trustedActivations []SkillRef,
	trustedResources []SkillResourceRequest,
	expectedCapabilities []string,
	domainOutcomes []SkillDomainCompatibilityOutcome,
) (string, error) {
	return runtime.computeCandidateCacheFingerprintWithBinding(
		runtime.bindingFingerprint, candidates, trustedActivations, trustedResources, expectedCapabilities, domainOutcomes, nil,
	)
}

func (runtime *skillRuntime) computeCandidateCacheFingerprintWithBinding(
	bindingFingerprint string,
	candidates []SkillCandidate,
	trustedActivations []SkillRef,
	trustedResources []SkillResourceRequest,
	expectedCapabilities []string,
	domainOutcomes []SkillDomainCompatibilityOutcome,
	unavailableContent []SkillVersionRef,
) (string, error) {
	projection := canonicalSkillCandidateCacheProjectionV1{
		Schema:                skillCandidateCacheProjectionVersion,
		BindingFingerprint:    bindingFingerprint,
		BudgetFingerprint:     runtime.budgetFingerprint,
		Candidates:            cloneSkillCandidates(candidates),
		TrustedActivations:    append([]SkillRef(nil), trustedActivations...),
		TrustedResources:      append([]SkillResourceRequest(nil), trustedResources...),
		ExpectedCapabilities:  append([]string(nil), expectedCapabilities...),
		DomainOutcomes:        append([]SkillDomainCompatibilityOutcome(nil), domainOutcomes...),
		UnavailableContent:    canonicalSkillVersionRefOrder(unavailableContent),
		RuntimePolicy:         runtime.policyDebug,
		ActivationModelIntent: normalizedSkillModelIntent(runtime.activationOptions),
		ResourceModelIntent:   normalizedSkillModelIntent(runtime.resourceOptions),
	}
	return hashCanonicalSkillValue(projection)
}

func canonicalSkillBindingOrder(bindings []SkillBinding) []SkillBinding {
	canonical := append([]SkillBinding(nil), bindings...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Namespace != canonical[j].Namespace {
			return canonical[i].Namespace < canonical[j].Namespace
		}
		return canonical[i].Name < canonical[j].Name
	})
	return canonical
}

func canonicalSkillVersionRefOrder(values []SkillVersionRef) []SkillVersionRef {
	canonical := append([]SkillVersionRef(nil), values...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Ref != canonical[j].Ref {
			return canonical[i].Ref.String() < canonical[j].Ref.String()
		}
		if canonical[i].Version != canonical[j].Version {
			return canonical[i].Version < canonical[j].Version
		}
		return canonical[i].ManifestHash < canonical[j].ManifestHash
	})
	return canonical
}

func normalizedSkillModelIntent(options *AIOptionsOverride) skillSelectorModelIntent {
	intent := skillSelectorModelIntent{Model: "inherit", ReasoningEffort: "inherit"}
	if options != nil && options.Model != nil {
		intent.Model = *options.Model
	}
	if options != nil && options.ReasoningEffort != nil {
		intent.ReasoningEffort = *options.ReasoningEffort
	}
	return intent
}

func explicitSkillModel(options *AIOptionsOverride) bool {
	return options != nil && options.Model != nil && *options.Model != ""
}

func computeSkillGuidanceFingerprint(kind, value string) (string, error) {
	return hashCanonicalSkillValue(struct {
		Schema string `json:"schema"`
		Kind   string `json:"kind"`
		Value  string `json:"value"`
	}{Schema: "truvag3.skill-guidance/v1", Kind: kind, Value: normalizeSkillMetadataText(value)})
}

func normalizeTrustedSkillRefs(refs []SkillRef) ([]SkillRef, error) {
	normalized := append([]SkillRef(nil), refs...)
	seen := make(map[SkillRef]struct{}, len(normalized))
	for _, ref := range normalized {
		if !validSkillSlug(ref.Namespace, 64) || !validSkillSlug(ref.Name, 64) {
			return nil, fmt.Errorf("%w: trusted skill activation identity is invalid", ErrInvalidSkillPackage)
		}
		if _, found := seen[ref]; found {
			return nil, fmt.Errorf("%w: duplicate trusted skill activation", ErrInvalidSkillPackage)
		}
		seen[ref] = struct{}{}
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].String() < normalized[j].String() })
	return normalized, nil
}

func trustedSkillActivationsFromContext(ctx context.Context) []SkillRef {
	if ctx == nil {
		return nil
	}
	refs, _ := ctx.Value(trustedSkillActivationsContextKey{}).([]SkillRef)
	return append([]SkillRef(nil), refs...)
}

func trustedSkillResourcesFromContext(ctx context.Context) []SkillResourceRequest {
	if ctx == nil {
		return nil
	}
	requests, _ := ctx.Value(trustedSkillResourcesContextKey{}).([]SkillResourceRequest)
	return append([]SkillResourceRequest(nil), requests...)
}

func withSkillExecutionState(ctx context.Context, state SkillExecutionState) context.Context {
	return context.WithValue(ctx, skillExecutionStateContextKey{}, cloneSkillExecutionState(state))
}

func skillExecutionStateFromContext(ctx context.Context) (SkillExecutionState, bool) {
	if ctx == nil {
		return SkillExecutionState{}, false
	}
	state, ok := ctx.Value(skillExecutionStateContextKey{}).(SkillExecutionState)
	return cloneSkillExecutionState(state), ok
}

func cloneSkillExecutionState(state SkillExecutionState) SkillExecutionState {
	encoded, err := json.Marshal(state)
	if err != nil {
		return SkillExecutionState{}
	}
	var cloned SkillExecutionState
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return SkillExecutionState{}
	}
	return cloned
}

func unavailableSkillCandidates(requests []SkillCandidateRequest) []SkillCandidate {
	candidates := make([]SkillCandidate, len(requests))
	for index, request := range requests {
		candidates[index] = SkillCandidate{Ref: request.Ref, RequestedVersion: request.RequestedVersion, Status: SkillCandidateUnavailable}
	}
	return candidates
}

func skillCandidateKey(ref SkillRef, version string) string {
	return ref.String() + "\x00" + version
}

func hasRequiredSkillBinding(bindings []SkillBinding) bool {
	for _, binding := range bindings {
		if binding.Required {
			return true
		}
	}
	return false
}

func bindingCandidateRef(binding SkillBinding) SkillRef {
	return SkillRef{Namespace: binding.Namespace, Name: binding.Name}
}

func containsNormalizedSkillDomain(domains []string, domain string) bool {
	for _, candidate := range domains {
		if strings.ToLower(strings.TrimSpace(candidate)) == domain {
			return true
		}
	}
	return false
}

func skillCandidateDebugRecords(bindings []SkillBinding, candidates []SkillCandidate) []SkillCandidateDebug {
	debug := make([]SkillCandidateDebug, len(bindings))
	for index, binding := range bindings {
		candidate := candidates[index]
		debug[index] = SkillCandidateDebug{
			Sequence: index + 1, Ref: binding.Ref(), RequestedVersion: binding.Version,
			Activation: binding.Activation, Required: binding.Required, Status: candidate.Status,
		}
		if candidate.Status == SkillCandidateResolved {
			debug[index].DisplayName = candidate.Metadata.DisplayName
			debug[index].Description = candidate.Metadata.Description
			resolved := candidate.Resolved
			debug[index].Resolved = &resolved
		}
	}
	return debug
}
