package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type preparedRequest struct {
	Model              string
	Body               []byte
	Headers            http.Header
	Report             *core.AIRequestReport
	Codec              openaiwire.ProfiledCodec
	ProtectedConflicts []string
}

type requestSemantics struct {
	Request                *core.AIRequest
	Options                *core.AIOptions
	RequestedModel         string
	SemanticModel          string
	ProviderAlias          string
	Surface                string
	Operation              string
	Purpose                string
	Capabilities           providers.ModelCapabilities
	ReasoningModel         bool
	PreparationAdjustments []core.AIRequestAdjustment
	RequireParameters      bool
}

const openRouterProviderAlias = "openai.openrouter"

var openRouterEfforts = map[string]struct{}{
	"max": {}, "xhigh": {}, "high": {}, "medium": {},
	"low": {}, "minimal": {}, "none": {},
}

func (s *requestSemantics) endpointRequest() ai.EndpointRequest {
	return ai.EndpointRequest{
		Provider:      "openai",
		ProviderAlias: s.ProviderAlias,
		Surface:       s.Surface,
		ResolvedModel: s.SemanticModel,
		Operation:     s.Operation,
		Purpose:       s.Purpose,
	}
}

type preparedInvocation struct {
	Request *preparedRequest
	Route   resolvedRoute
}

func newRequestPolicyEngine() *requestpolicy.Engine {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in OpenAI request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*requestpolicy.Engine, error) {
	return requestpolicy.NewEngine(requestpolicy.Config{
		AppRules:   appRules,
		Middleware: middleware,
		Mode:       mode,
	})
}

func (c *Client) prepareInvocation(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*preparedInvocation, error) {
	semantics, err := c.prepareSemantics(ctx, supplied, stream)
	if err != nil {
		return nil, err
	}
	route, err := c.resolveEndpoint(ctx, semantics.endpointRequest())
	if err != nil {
		return nil, err
	}
	profile, err := c.requestProfile(semantics, route)
	if err != nil {
		return nil, err
	}
	prepared, err := c.buildPolicyRequest(ctx, semantics, profile, stream)
	invocation := &preparedInvocation{Request: prepared, Route: route}
	if err != nil {
		return invocation, err
	}
	c.bindRoute(prepared, route)
	return invocation, nil
}

func (c *Client) prepareSemantics(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*requestSemantics, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI legacy request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	if err := validatePortableIntent(request.Generation); err != nil {
		return nil, err
	}
	requestedModel := options.Model
	options.Model = ResolveModel(c.providerAlias, options.Model)

	providerAlias := c.getProviderName()
	caps := providers.LookupModelCapabilities(providerAlias, options.Model)
	if request.Generation.ReasoningEffort.Mode == core.AIParameterSet && caps.ReasoningStyle != "openai" {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*openai.Client",
			Feature:    "generation.reasoning_effort",
		}
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterSet && !caps.SupportsJSONMode {
		return nil, &core.AIRequestFeatureError{
			ClientType: "*openai.Client",
			Feature:    "generation.response_format",
		}
	}

	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = c.ReasoningEffort
	}
	switch request.Generation.ReasoningEffort.Mode {
	case core.AIParameterSet:
		reasoningEffort = request.Generation.ReasoningEffort.Value
	case core.AIParameterOmit:
		reasoningEffort = ""
	}
	responseFormat := options.ResponseFormat
	switch request.Generation.ResponseFormat.Mode {
	case core.AIParameterSet:
		responseFormat = request.Generation.ResponseFormat.Value
	case core.AIParameterOmit:
		responseFormat = ""
	}
	preparationAdjustments := make([]core.AIRequestAdjustment, 0, 2)
	if reasoningEffort != "" && caps.ReasoningStyle != "openai" {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "reasoning_effort_stripped", "reasoning_effort")
		reasoningEffort = ""
		if request.Generation.ReasoningEffort.Mode == core.AIParameterInherit {
			preparationAdjustments = append(preparationAdjustments,
				inheritedOmit("/reasoning_effort", "unsupported_model_capability"))
		}
	}
	options.ReasoningEffort = reasoningEffort
	if responseFormat != "" && !caps.SupportsJSONMode {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "response_format_stripped", "response_format")
		responseFormat = ""
		if request.Generation.ResponseFormat.Mode == core.AIParameterInherit {
			preparationAdjustments = append(preparationAdjustments,
				inheritedOmit("/response_format", "unsupported_model_capability"))
		}
	}
	options.ResponseFormat = responseFormat
	if providerAlias == openRouterProviderAlias {
		options.ReasoningEffort, err = normalizeOpenRouterEffort(options.ReasoningEffort)
		if err != nil {
			return nil, err
		}
		if request.Generation.ReasoningEffort.Mode == core.AIParameterSet {
			request.Generation.ReasoningEffort.Value = options.ReasoningEffort
		}
		if strings.EqualFold(strings.TrimSpace(requestedModel), "free") &&
			!isOpenRouterFreeModel(options.Model) {
			return nil, errors.New("OpenRouter free alias must resolve to a free model")
		}
	}

	clientDefaults, err := providers.CloneAIOptions(&core.AIOptions{
		Extra:   c.defaultExtra,
		Headers: c.defaultHeaders,
	})
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI client request defaults: %w", err)
	}
	options.Extra = filterOpenAIExtraFields(
		ctx,
		c.Logger,
		providerAlias,
		options.Model,
		caps,
		providers.MergeAnyMaps(clientDefaults.Extra, options.Extra),
	)
	requireParameters := options.ReasoningEffort != "" || options.ResponseFormat != ""
	if providerAlias == openRouterProviderAlias {
		protectedProviderValues := map[string]interface{}{
			"data_collection": "deny",
			"zdr":             true,
		}
		if requireParameters {
			protectedProviderValues["require_parameters"] = true
		}
		options.Extra, err = mergeProtectedProviderValues(options.Extra, protectedProviderValues)
		if err != nil {
			return nil, err
		}
		if isOpenRouterFreeModel(options.Model) {
			if models, present := options.Extra["models"]; present {
				if err := validateFreeModelFallbacks(models); err != nil {
					return nil, err
				}
			}
		}
	}
	options.Headers = mergeOpenAIHeaders(clientDefaults.Headers, options.Headers)
	operation := "generate"
	if stream {
		operation = "stream"
	}
	return &requestSemantics{
		Request:                request,
		Options:                options,
		RequestedModel:         requestedModel,
		SemanticModel:          options.Model,
		ProviderAlias:          providerAlias,
		Surface:                "chat-completions",
		Operation:              operation,
		Purpose:                request.Purpose,
		Capabilities:           caps,
		ReasoningModel:         openaiwire.IsReasoningModel(options.Model),
		PreparationAdjustments: preparationAdjustments,
		RequireParameters:      requireParameters,
	}, nil
}

func inheritedOmit(path, reason string) core.AIRequestAdjustment {
	return core.AIRequestAdjustment{
		Source: "portable-default", Rule: "capability-guard", Path: path,
		Action: "remove", Reason: reason,
	}
}

func normalizeOpenRouterEffort(effort string) (string, error) {
	if effort == "" {
		return "", nil
	}
	normalized := strings.ToLower(strings.TrimSpace(effort))
	if _, ok := openRouterEfforts[normalized]; !ok {
		return "", errors.New("OpenRouter reasoning effort is invalid")
	}
	return normalized, nil
}

func mergeProtectedProviderValues(
	extra map[string]interface{},
	protected map[string]interface{},
) (map[string]interface{}, error) {
	clonedOptions, err := providers.CloneAIOptions(&core.AIOptions{Extra: extra})
	if err != nil {
		return nil, errors.New("clone OpenRouter request extras")
	}
	cloned := clonedOptions.Extra
	if cloned == nil {
		cloned = make(map[string]interface{})
	}

	providerValues := make(map[string]interface{}, len(protected))
	var existingProvider interface{}
	providerKeys := 0
	for key, value := range cloned {
		if strings.EqualFold(key, "provider") {
			providerKeys++
			existingProvider = value
			delete(cloned, key)
		}
	}
	if providerKeys > 1 {
		return nil, openRouterInvariantError("protected-default", "/provider")
	}
	if providerKeys == 1 {
		existing := existingProvider
		reflected := reflect.ValueOf(existing)
		if !reflected.IsValid() || reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
			return nil, openRouterInvariantError("protected-default", "/provider")
		}
		iterator := reflected.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			for existingKey := range providerValues {
				if strings.EqualFold(existingKey, key) {
					return nil, openRouterInvariantError("protected-default", "/provider")
				}
			}
			providerValues[key] = iterator.Value().Interface()
		}
	}
	for key, value := range protected {
		for existingKey, existing := range providerValues {
			if !strings.EqualFold(existingKey, key) {
				continue
			}
			if !reflect.DeepEqual(existing, value) {
				return nil, openRouterInvariantError("protected-default", "/provider/"+key)
			}
			delete(providerValues, existingKey)
		}
		providerValues[key] = value
	}
	cloned["provider"] = providerValues
	return cloned, nil
}

func openRouterInvariantError(stage, path string) error {
	return &requestpolicy.PolicyError{
		Stage: stage,
		Path:  path,
		Err:   errors.New("protected OpenRouter request invariant was not preserved"),
	}
}

func isOpenRouterFreeModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return normalized == "openrouter/free" || strings.HasSuffix(normalized, ":free")
}

func validateFreeModelFallbacks(models interface{}) error {
	reflected := reflect.ValueOf(models)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return openRouterInvariantError("free-route", "/models")
	}
	for index := range reflected.Len() {
		model, ok := reflected.Index(index).Interface().(string)
		if !ok || !isOpenRouterFreeModel(model) {
			return openRouterInvariantError("free-route", "/models")
		}
	}
	return nil
}

func validatePortableIntent(generation core.AIGenerationOptions) error {
	parameters := []struct {
		name string
		mode core.AIParameterMode
	}{
		{name: "temperature", mode: generation.Temperature.Mode},
		{name: "top_p", mode: generation.TopP.Mode},
		{name: "top_k", mode: generation.TopK.Mode},
		{name: "max_tokens", mode: generation.MaxTokens.Mode},
		{name: "system_prompt", mode: generation.SystemPrompt.Mode},
		{name: "reasoning_effort", mode: generation.ReasoningEffort.Mode},
		{name: "response_format", mode: generation.ResponseFormat.Mode},
	}
	for _, parameter := range parameters {
		if parameter.mode != core.AIParameterInherit &&
			parameter.mode != core.AIParameterSet &&
			parameter.mode != core.AIParameterOmit {
			return fmt.Errorf("invalid generation.%s mode %d", parameter.name, parameter.mode)
		}
	}
	if generation.TopK.Mode == core.AIParameterSet {
		return &core.AIRequestFeatureError{ClientType: "*openai.Client", Feature: "generation.top_k"}
	}
	if generation.MaxTokens.Mode == core.AIParameterSet && generation.MaxTokens.Value <= 0 {
		return errors.New("generation.max_tokens must be positive")
	}
	return nil
}

func (c *Client) requestProfile(
	semantics *requestSemantics,
	_ resolvedRoute,
) (openaiwire.RequestProfile, error) {
	profile := ordinaryProfile(semantics.SemanticModel)
	if semantics.ProviderAlias == openRouterProviderAlias {
		profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
		profile.TokenBudget = openaiwire.TokenBudgetExact
		profile.ReasoningEffort = openaiwire.ReasoningEffortNestedObject
		if semantics.Options.ReasoningEffort != "" && semantics.Capabilities.ReasoningStyle == "openai" {
			profile.Sampling = openaiwire.SamplingReasoningRestricted
		}
		return profile, profile.Validate()
	}
	profile = existingProfileDecision(profile, semantics)
	return profile, profile.Validate()
}

func ordinaryProfile(model string) openaiwire.RequestProfile {
	return openaiwire.RequestProfile{
		SemanticModel:   model,
		WireModel:       model,
		ModelField:      openaiwire.ModelFieldRequired,
		TokenLimit:      openaiwire.TokenLimitMaxTokens,
		TokenBudget:     openaiwire.TokenBudgetExact,
		ReasoningEffort: openaiwire.ReasoningEffortOmitted,
		Sampling:        openaiwire.SamplingOrdinary,
	}
}

func existingProfileDecision(
	profile openaiwire.RequestProfile,
	semantics *requestSemantics,
) openaiwire.RequestProfile {
	if semantics.ReasoningModel {
		profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
		profile.TokenBudget = openaiwire.TokenBudgetScaleForReasoning
		profile.Sampling = openaiwire.SamplingReasoningRestricted
	}
	// The stock OpenAI Chat Completions surface supports the top-level field
	// spelling even when the framework does not claim reasoning capability for
	// an application-supplied model. This lets a scoped native policy make that
	// provider-model assertion without classifying the model as a reasoning
	// family or changing its token and sampling profile.
	if semantics.ProviderAlias == "openai" || semantics.Capabilities.ReasoningStyle == "openai" {
		profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
		if semantics.ProviderAlias == "openai.ollama" {
			profile.ReasoningEffort = openaiwire.ReasoningEffortNestedObject
		}
	}
	return profile
}

func (c *Client) buildPolicyRequest(
	ctx context.Context,
	semantics *requestSemantics,
	profile openaiwire.RequestProfile,
	stream bool,
) (*preparedRequest, error) {
	request := semantics.Request
	options := semantics.Options

	wireOptions := *options
	wireOptions.Model = semantics.RequestedModel
	wireRequest := core.NewAIRequestFromLegacy(request.Prompt, request.Purpose, &wireOptions)
	wireRequest.Generation = request.Generation
	wireRequest.Patches = request.Patches
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
		SurfaceVersion:           openaiwire.DefaultSurfaceVersion,
		ReasoningTokenMultiplier: c.ReasoningTokenMultiplier,
		DefaultReasoningEffort:   options.ReasoningEffort,
		MaxSSEEventBytes:         c.sseEventMaxBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI wire codec: %w", err)
	}
	draft, err := codec.BuildDraftWithProfile(wireRequest, profile, stream)
	if err != nil {
		return nil, err
	}
	if err := draft.BindIdentity("openai", semantics.ProviderAlias); err != nil {
		return nil, err
	}
	// Preserve the unresolved caller-facing model in selectors and reports.
	if draft.Info().RequestedModel != semantics.RequestedModel {
		return nil, errors.New("OpenAI wire requested-model invariant was not preserved")
	}
	prepared := &preparedRequest{
		Model:              options.Model,
		Codec:              codec,
		ProtectedConflicts: draft.ProtectedHeaderConflicts(),
	}
	if c.requestPolicy == nil {
		return prepared, errors.New("OpenAI request policy engine is not configured")
	}
	report, err := c.requestPolicy.Apply(ctx, draft, request.Patches)
	if report != nil {
		adjustments := append([]core.AIRequestAdjustment(nil), semantics.PreparationAdjustments...)
		adjustments = append(adjustments, draft.Adjustments()...)
		report.Adjustments = append(adjustments, report.Adjustments...)
		prepared.Report = report
	}
	if err != nil {
		return prepared, err
	}
	if semantics.ProviderAlias == openRouterProviderAlias {
		if err := validateOpenRouterPostPolicy(semantics, draft); err != nil {
			if prepared.Report != nil {
				prepared.Report.Fingerprint = ""
				prepared.Report.Stable = false
			}
			return prepared, err
		}
		if openRouterSelectionIsDynamic(semantics.SemanticModel, draft) && prepared.Report != nil {
			prepared.Report.Fingerprint = ""
			prepared.Report.Stable = false
		}
	}
	prepared.Body, err = codec.Encode(draft)
	if err != nil {
		return prepared, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if c.credentialSource == nil && c.apiKey != "" {
		headers.Set("Authorization", "Bearer "+c.apiKey)
	}
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	providers.ApplyLegacyHeaders(headers, openAIProtectedHeaders(stream), draft.Headers(), nil)
	prepared.Headers = headers
	return prepared, nil
}

func validateOpenRouterPostPolicy(semantics *requestSemantics, draft requestpolicy.Draft) error {
	if err := validateOpenRouterCanonicalPostPolicyKeys(draft); err != nil {
		return err
	}
	protected := map[string]interface{}{
		"/provider/data_collection": "deny",
		"/provider/zdr":             true,
	}
	if semantics.RequireParameters {
		protected["/provider/require_parameters"] = true
	}
	for path, expected := range protected {
		value, present := draft.Get(path)
		if !present || !reflect.DeepEqual(value, expected) {
			return openRouterInvariantError("post-policy", path)
		}
	}
	if sessionID, present := draft.Get("/session_id"); present {
		value, ok := sessionID.(string)
		if !ok || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 256 {
			return openRouterInvariantError("post-policy", "/session_id")
		}
	}
	if models, present := draft.Get("/models"); present {
		if isOpenRouterFreeModel(semantics.SemanticModel) {
			if err := validateFreeModelFallbacks(models); err != nil {
				return err
			}
		} else if !isModelList(models) {
			return openRouterInvariantError("post-policy", "/models")
		}
	}
	return nil
}

func validateOpenRouterCanonicalPostPolicyKeys(draft requestpolicy.Draft) error {
	bodyReader, ok := draft.(interface {
		Body() map[string]interface{}
	})
	if !ok {
		return openRouterInvariantError("post-policy", "/")
	}
	body := bodyReader.Body()
	for _, key := range []string{
		"model", "messages", "stream", "stream_options", "provider", "models", "session_id",
	} {
		if containsNonCanonicalKey(body, key) {
			return openRouterInvariantError("post-policy", "/"+key)
		}
	}
	provider, present := body["provider"]
	if !present {
		return nil
	}
	for _, key := range []string{"data_collection", "zdr", "require_parameters"} {
		if containsNonCanonicalMapKey(provider, key) {
			return openRouterInvariantError("post-policy", "/provider/"+key)
		}
	}
	return nil
}

func containsNonCanonicalKey(values map[string]interface{}, canonical string) bool {
	for key := range values {
		if key != canonical && strings.EqualFold(key, canonical) {
			return true
		}
	}
	return false
}

func containsNonCanonicalMapKey(value interface{}, canonical string) bool {
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() || reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return false
	}
	iterator := reflected.MapRange()
	for iterator.Next() {
		key := iterator.Key().String()
		if key != canonical && strings.EqualFold(key, canonical) {
			return true
		}
	}
	return false
}

func openRouterSelectionIsDynamic(model string, draft requestpolicy.Draft) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(normalized, "openrouter/") || strings.HasPrefix(normalized, "~") {
		return true
	}
	models, present := draft.Get("/models")
	return present && nonEmptyModelList(models)
}

func nonEmptyModelList(models interface{}) bool {
	length, valid := modelListLength(models)
	return valid && length > 0
}

func isModelList(models interface{}) bool {
	_, valid := modelListLength(models)
	return valid
}

func modelListLength(models interface{}) (int, bool) {
	reflected := reflect.ValueOf(models)
	if !reflected.IsValid() || reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return 0, false
	}
	return reflected.Len(), true
}

func mergeOpenAIHeaders(defaultHeaders, requestHeaders map[string]string) map[string]string {
	if len(defaultHeaders) == 0 && len(requestHeaders) == 0 {
		return nil
	}

	headers := make(http.Header)
	for _, source := range []map[string]string{defaultHeaders, requestHeaders} {
		keys := make([]string, 0, len(source))
		for name := range source {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			headers.Set(name, source[name])
		}
	}

	merged := make(map[string]string, len(headers))
	for name, values := range headers {
		if len(values) > 0 {
			merged[name] = values[len(values)-1]
		}
	}
	return merged
}

func openAIProtectedHeaders(stream bool) map[string]struct{} {
	protected := map[string]struct{}{
		"authorization": {},
		"content-type":  {},
	}
	if stream {
		protected["accept"] = struct{}{}
	}
	return protected
}

func (c *Client) recordRequestPreparation(ctx context.Context, span core.Span, prepared *preparedRequest) {
	if prepared == nil {
		return
	}
	if prepared.Report != nil {
		span.SetAttribute("ai.request.provider_alias", prepared.Report.ProviderAlias)
		span.SetAttribute("ai.request.surface", prepared.Report.Surface)
		span.SetAttribute("ai.request.operation", prepared.Report.Operation)
		span.SetAttribute("ai.request.policy_stable", prepared.Report.Stable)
		span.SetAttribute("ai.request.adjustment_count", len(prepared.Report.Adjustments))
		if prepared.Report.Fingerprint != "" {
			span.SetAttribute("ai.request.policy_fingerprint", prepared.Report.Fingerprint)
		}
	}
	if len(prepared.ProtectedConflicts) > 0 && c.Logger != nil {
		fields := map[string]interface{}{
			"operation":       "ai_request_policy",
			"provider":        c.getProviderName(),
			"model":           prepared.Model,
			"ignored_headers": strings.Join(prepared.ProtectedConflicts, ","),
			"migration":       "remove provider-managed names from WithHeaders and AIOptions.Headers",
		}
		providers.AddObservationRequestID(ctx, fields)
		c.Logger.WarnWithContext(ctx, "OpenAI legacy protected headers ignored", fields)
	}
}

func resultWithReport(prepared *preparedRequest, result *core.AIResult) *core.AIResult {
	if result == nil {
		if prepared == nil || prepared.Report == nil {
			return nil
		}
		result = &core.AIResult{}
	}
	if prepared != nil {
		result.RequestReport = prepared.Report
	}
	return result
}
