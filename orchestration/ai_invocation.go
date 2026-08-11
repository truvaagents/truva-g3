package orchestration

import (
	"context"
	"fmt"
	"sync"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type aiFingerprintCaptureKey struct{}

type aiFingerprintCapture struct {
	mu           sync.Mutex
	observed     bool
	stable       bool
	fingerprints map[string]struct{}
}

func withAIFingerprintCapture(ctx context.Context) (context.Context, *aiFingerprintCapture) {
	capture := &aiFingerprintCapture{stable: true, fingerprints: make(map[string]struct{})}
	return context.WithValue(ctx, aiFingerprintCaptureKey{}, capture), capture
}

func (capture *aiFingerprintCapture) matches(expected string) bool {
	if capture == nil {
		return true
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if !capture.observed || !capture.stable || len(capture.fingerprints) != 1 {
		return false
	}
	_, ok := capture.fingerprints[expected]
	return ok
}

// aiInvocation is the provider-neutral description of one orchestration LLM
// call. Options remains the compatibility input while call sites migrate to
// presence-aware generation parameters and declarative provider patches.
type aiInvocation struct {
	Purpose        string
	Prompt         string
	Options        *core.AIOptions
	Generation     core.AIGenerationOptions
	Patches        []core.AIProviderPatch
	SystemSource   promptSystemSource
	DeferRecording bool
}

type effectiveAIRequest struct {
	Purpose           string
	Prompt            string
	SystemPrompt      string
	RequestedModel    string
	ResolvedModel     string
	Generation        core.AIGenerationOptions
	PolicyFingerprint string
	Adjustments       []core.AIRequestAdjustment
}

type aiInvocationResult struct {
	Response  *core.AIResponse
	Report    *core.AIRequestReport
	Effective effectiveAIRequest
}

type aiInvocationEvidenceCaptureKey struct{}

type aiInvocationEvidenceCapture struct {
	mu     sync.Mutex
	result *aiInvocationResult
}

func withAIInvocationEvidenceCapture(ctx context.Context) (context.Context, *aiInvocationEvidenceCapture) {
	capture := &aiInvocationEvidenceCapture{}
	return context.WithValue(ctx, aiInvocationEvidenceCaptureKey{}, capture), capture
}

func (capture *aiInvocationEvidenceCapture) Result() *aiInvocationResult {
	if capture == nil {
		return nil
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.result
}

func captureAIInvocationEvidence(ctx context.Context, result *aiInvocationResult) {
	capture, _ := ctx.Value(aiInvocationEvidenceCaptureKey{}).(*aiInvocationEvidenceCapture)
	if capture == nil {
		return
	}
	capture.mu.Lock()
	capture.result = result
	capture.mu.Unlock()
}

type preparedAIInvocation struct {
	Request   *core.AIRequest
	Effective effectiveAIRequest
}

func newAIRequest(invocation aiInvocation) *core.AIRequest {
	request := core.NewAIRequestFromLegacy(invocation.Prompt, invocation.Purpose, invocation.Options)
	request.Generation = invocation.Generation
	request.Patches = append([]core.AIProviderPatch(nil), invocation.Patches...)
	return request
}

func prepareAIInvocation(ctx context.Context, invocation aiInvocation) (*preparedAIInvocation, error) {
	options := cloneCoreAIOptions(invocation.Options)
	kind := promptKindForPurpose(invocation.Purpose)
	systemPrompt := ""
	source := invocation.SystemSource
	if options != nil {
		systemPrompt = options.SystemPrompt
	}
	switch invocation.Generation.SystemPrompt.Mode {
	case core.AIParameterSet:
		systemPrompt = invocation.Generation.SystemPrompt.Value
		source = promptSystemAIOptionsOverride
	case core.AIParameterOmit:
		systemPrompt = ""
		source = promptSystemAIOptionsOverride
	}
	assembly, err := finalizePromptAssembly(ctx, promptAssembly{
		Kind:            kind,
		SystemBase:      systemPrompt,
		SystemSource:    source,
		UserSections:    []promptSection{{Name: "prompt", Body: invocation.Prompt, Role: promptRoleUser}},
		Generation:      invocation.Generation,
		ProviderPatches: append([]core.AIProviderPatch(nil), invocation.Patches...),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPromptAssembly, err)
	}

	prepared := invocation
	prepared.Prompt = renderUserPrompt(assembly)
	prepared.Options = options
	prepared.Patches = append([]core.AIProviderPatch(nil), assembly.ProviderPatches...)
	if len(promptFinalizers(kind)) > 0 {
		// Planning finalizers are immutable framework contracts. An explicit
		// provider-level omission may remove developer content, but it cannot
		// remove the finalized runtime context from the actual request.
		prepared.Generation.SystemPrompt = core.SetAIParameter(assembly.SystemBase)
	} else if prepared.Generation.SystemPrompt.Mode == core.AIParameterSet {
		prepared.Generation.SystemPrompt = core.SetAIParameter(assembly.SystemBase)
	} else if prepared.Generation.SystemPrompt.Mode == core.AIParameterInherit {
		if prepared.Options == nil {
			prepared.Options = &core.AIOptions{}
		}
		prepared.Options.SystemPrompt = assembly.SystemBase
	}
	request := newAIRequest(prepared)
	generation := effectiveGeneration(request.Generation, prepared.Options)
	effectiveSystemPrompt := assembly.SystemBase
	if generation.SystemPrompt.Mode == core.AIParameterOmit {
		effectiveSystemPrompt = ""
	}
	return &preparedAIInvocation{
		Request: request,
		Effective: effectiveAIRequest{
			Purpose:        invocation.Purpose,
			Prompt:         string([]byte(prepared.Prompt)),
			SystemPrompt:   string([]byte(effectiveSystemPrompt)),
			RequestedModel: generation.Model,
			Generation:     generation,
		},
	}, nil
}

func effectiveGeneration(request core.AIGenerationOptions, options *core.AIOptions) core.AIGenerationOptions {
	effective := request
	if options == nil {
		return effective
	}
	if effective.Model == "" {
		effective.Model = options.Model
	}
	if effective.Temperature.Mode == core.AIParameterInherit {
		effective.Temperature = core.SetAIParameter(options.Temperature)
	}
	if effective.MaxTokens.Mode == core.AIParameterInherit {
		effective.MaxTokens = core.SetAIParameter(options.MaxTokens)
	}
	if effective.SystemPrompt.Mode == core.AIParameterInherit {
		effective.SystemPrompt = core.SetAIParameter(options.SystemPrompt)
	}
	if effective.ReasoningEffort.Mode == core.AIParameterInherit && options.ReasoningEffort != "" {
		effective.ReasoningEffort = core.SetAIParameter(options.ReasoningEffort)
	}
	if effective.ResponseFormat.Mode == core.AIParameterInherit && options.ResponseFormat != "" {
		effective.ResponseFormat = core.SetAIParameter(options.ResponseFormat)
	}
	return effective
}

func invokeAI(
	ctx context.Context,
	client core.AIClient,
	invocation aiInvocation,
) (*aiInvocationResult, error) {
	prepared, err := prepareAIInvocation(ctx, invocation)
	if err != nil {
		recordAIRequestRejection(ctx, invocation.Purpose, "prompt_assembly")
		return nil, err
	}
	if invocation.DeferRecording {
		ctx = telemetry.WithLLMCallRecordingDeferred(ctx)
	}
	result, err := core.GenerateAI(ctx, client, prepared.Request)
	if result == nil {
		if err == nil {
			err = fmt.Errorf("orchestration: AI client returned a nil result without error")
		}
		captureAIRequestFingerprint(ctx, nil)
		completed := &aiInvocationResult{Effective: prepared.Effective}
		recordAIRequestEvidence(ctx, completed)
		return completed, err
	}
	if result.Response == nil && err == nil {
		err = fmt.Errorf("orchestration: AI client returned a nil response without error")
	}
	captureAIRequestFingerprint(ctx, result.RequestReport)
	completed := completedAIInvocationResult(prepared.Effective, result)
	recordAIRequestEvidence(ctx, completed)
	return completed, err
}

func streamAI(
	ctx context.Context,
	client core.AIClient,
	invocation aiInvocation,
	callback core.StreamCallback,
) (*aiInvocationResult, error) {
	prepared, err := prepareAIInvocation(ctx, invocation)
	if err != nil {
		recordAIRequestRejection(ctx, invocation.Purpose, "prompt_assembly")
		return nil, err
	}
	if invocation.DeferRecording {
		ctx = telemetry.WithLLMCallRecordingDeferred(ctx)
	}
	result, err := core.StreamAI(ctx, client, prepared.Request, callback)
	if result == nil {
		if err == nil {
			err = fmt.Errorf("orchestration: AI client returned a nil streaming result without error")
		}
		captureAIRequestFingerprint(ctx, nil)
		completed := &aiInvocationResult{Effective: prepared.Effective}
		recordAIRequestEvidence(ctx, completed)
		return completed, err
	}
	if result.Response == nil && err == nil {
		err = fmt.Errorf("orchestration: AI client returned a nil streaming response without error")
	}
	captureAIRequestFingerprint(ctx, result.RequestReport)
	completed := completedAIInvocationResult(prepared.Effective, result)
	recordAIRequestEvidence(ctx, completed)
	return completed, err
}

func completedAIInvocationResult(effective effectiveAIRequest, result *core.AIResult) *aiInvocationResult {
	completed := effective
	if result != nil && result.RequestReport != nil {
		report := result.RequestReport
		if report.RequestedModel != "" {
			completed.RequestedModel = report.RequestedModel
		}
		completed.ResolvedModel = report.ResolvedModel
		completed.PolicyFingerprint = report.Fingerprint
		completed.Adjustments = append([]core.AIRequestAdjustment(nil), report.Adjustments...)
		if report.EffectiveTemperature.Mode != core.AIParameterInherit {
			completed.Generation.Temperature = report.EffectiveTemperature
		}
		if report.EffectiveMaxTokens.Mode != core.AIParameterInherit {
			completed.Generation.MaxTokens = report.EffectiveMaxTokens
		}
	}
	return &aiInvocationResult{Response: result.Response, Report: result.RequestReport, Effective: completed}
}

// effectiveAIRequestForDebug returns the immutable request evidence prepared by
// the canonical dispatch boundary. The fallback is used only when preparation
// itself failed and therefore no provider request was made.
func effectiveAIRequestForDebug(result *aiInvocationResult, invocation aiInvocation) effectiveAIRequest {
	if result != nil {
		return result.Effective
	}
	generation := effectiveGeneration(invocation.Generation, invocation.Options)
	systemPrompt := ""
	if generation.SystemPrompt.Mode == core.AIParameterSet {
		systemPrompt = generation.SystemPrompt.Value
	}
	return effectiveAIRequest{
		Purpose:        invocation.Purpose,
		Prompt:         string([]byte(invocation.Prompt)),
		SystemPrompt:   string([]byte(systemPrompt)),
		RequestedModel: generation.Model,
		Generation:     generation,
	}
}

func effectiveAITemperature(effective effectiveAIRequest, fallback float32) float64 {
	switch effective.Generation.Temperature.Mode {
	case core.AIParameterSet:
		return roundLegacyFloat(float64(effective.Generation.Temperature.Value))
	case core.AIParameterOmit:
		return 0
	default:
		return roundLegacyFloat(float64(fallback))
	}
}

func effectiveAIMaxTokens(effective effectiveAIRequest, fallback int) int {
	switch effective.Generation.MaxTokens.Mode {
	case core.AIParameterSet:
		return effective.Generation.MaxTokens.Value
	case core.AIParameterOmit:
		return 0
	default:
		return fallback
	}
}

func effectiveAIIdentity(result *aiInvocationResult, response *core.AIResponse, callErr error) (string, string) {
	model, provider := "", ""
	if result != nil {
		if result.Effective.RequestedModel != "" {
			model = result.Effective.RequestedModel
		}
	}
	if errorModel, errorProvider := extractErrorProviderInfo(callErr); errorModel != "" || errorProvider != "" {
		if errorModel != "" {
			model = errorModel
		}
		if errorProvider != "" {
			provider = errorProvider
		}
	}
	if result != nil {
		if result.Effective.ResolvedModel != "" {
			model = result.Effective.ResolvedModel
		}
		if result.Report != nil && result.Report.Provider != "" {
			provider = result.Report.Provider
		}
	}
	if response != nil {
		if response.Model != "" {
			model = response.Model
		}
		if response.Provider != "" {
			provider = response.Provider
		}
	}
	return model, provider
}

// withEffectiveAIRequest projects canonical request evidence onto a restricted
// debug interaction. Call sites supply outcome-specific fields (type, timing,
// response, usage, and error); this helper owns the request fields so they
// cannot drift back to pre-merge options or omit a developer system prompt.
func withEffectiveAIRequest(
	interaction LLMInteraction,
	result *aiInvocationResult,
	invocation aiInvocation,
	response *core.AIResponse,
	callErr error,
) LLMInteraction {
	effective := effectiveAIRequestForDebug(result, invocation)
	var fallbackTemperature float32
	var fallbackMaxTokens int
	if invocation.Options != nil {
		fallbackTemperature = invocation.Options.Temperature
		fallbackMaxTokens = invocation.Options.MaxTokens
	}
	interaction.Prompt = effective.Prompt
	interaction.SystemPrompt = effective.SystemPrompt
	interaction.Temperature = effectiveAITemperature(effective, fallbackTemperature)
	interaction.MaxTokens = effectiveAIMaxTokens(effective, fallbackMaxTokens)
	interaction.Model, interaction.Provider = effectiveAIIdentity(result, response, callErr)
	return interaction
}

func captureAIRequestFingerprint(ctx context.Context, report *core.AIRequestReport) {
	capture, ok := ctx.Value(aiFingerprintCaptureKey{}).(*aiFingerprintCapture)
	if !ok || capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.observed = true
	if report == nil || !report.Stable || report.Fingerprint == "" {
		capture.stable = false
		return
	}
	capture.fingerprints[report.Fingerprint] = struct{}{}
}

// fingerprintAI returns a cache namespace fragment and whether it is safe to
// use the affected AI-output cache. Legacy-only clients retain their existing
// namespace when the invocation uses only legacy-representable semantics.
func fingerprintAI(ctx context.Context, client core.AIClient, invocation aiInvocation) (string, bool) {
	prepared, err := prepareAIInvocation(ctx, invocation)
	if err != nil {
		return "", false
	}
	request := prepared.Request
	if fingerprinter, ok := client.(core.AIRequestFingerprinter); ok {
		fingerprint, stable := fingerprinter.RequestFingerprint(ctx, request)
		return fingerprint, stable && fingerprint != ""
	}
	if _, requestAware := client.(core.AIRequestClient); requestAware || !request.LegacyRepresentable() {
		return "", false
	}
	return "", true
}

// recordAIRequestEvidence emits exactly one metadata-only event for the
// request preparation result, including provider failures that return no
// report. It excludes prompt bodies, provider patches, credentials, and
// adjustment details.
func recordAIRequestEvidence(ctx context.Context, result *aiInvocationResult) {
	if result == nil {
		return
	}
	captureAIInvocationEvidence(ctx, result)
	effective := result.Effective
	policyStable := effective.PolicyFingerprint != ""
	if result.Report != nil {
		policyStable = result.Report.Stable
	}
	attrs := []attribute.KeyValue{
		attribute.String("request_id", requestIDFromBaggage(ctx)),
		attribute.String("ai.purpose", effective.Purpose),
		attribute.String("ai.requested_model", effective.RequestedModel),
		attribute.String("ai.model", effective.ResolvedModel),
		attribute.Bool("ai.request.policy_stable", policyStable),
		attribute.Int("ai.request.adjustment_count", len(effective.Adjustments)),
		attribute.Bool("ai.request.reported", result.Report != nil),
	}
	if result.Report != nil {
		attrs = append(attrs,
			attribute.String("ai.provider", result.Report.Provider),
			attribute.String("ai.provider_alias", result.Report.ProviderAlias),
			attribute.String("ai.surface", result.Report.Surface),
			attribute.String("ai.request.operation", result.Report.Operation),
		)
	}
	if policyStable && effective.PolicyFingerprint != "" {
		attrs = append(attrs, attribute.String("ai.request.policy_fingerprint", effective.PolicyFingerprint))
	}
	telemetry.AddSpanEvent(ctx, "ai.request.prepared", attrs...)
}

func recordAIRequestRejection(ctx context.Context, purpose, reason string) {
	telemetry.AddSpanEvent(ctx, "ai.request.rejected",
		attribute.String("request_id", requestIDFromBaggage(ctx)),
		attribute.String("ai.purpose", purpose),
		attribute.String("reason", reason),
	)
}
