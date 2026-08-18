package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type generationPaths struct {
	Temperature    string
	TopP           string
	TopK           string
	MaxTokens      string
	ReasoningLevel string
	ResponseFormat string
	CandidateCount string
	Store          string
}

type wireProfile struct {
	surface             string
	apiVersion          string
	schemaVersion       string
	fingerprintIdentity string
}

var selectedWireProfile = wireProfile{
	surface:             "generate-content",
	apiVersion:          "v1beta",
	schemaVersion:       "2026-08-17",
	fingerprintIdentity: "gemini/generate-content/v1beta/2026-08-17",
}

func (profile wireProfile) generationPaths() generationPaths {
	return generationPaths{
		Temperature:    "/generationConfig/temperature",
		TopP:           "/generationConfig/topP",
		TopK:           "/generationConfig/topK",
		MaxTokens:      "/generationConfig/maxOutputTokens",
		ReasoningLevel: "/generationConfig/thinkingConfig/thinkingLevel",
		ResponseFormat: "/generationConfig/responseMimeType",
		CandidateCount: "/generationConfig/candidateCount",
		Store:          "/store",
	}
}

func (profile wireProfile) validateDraft(document *requestpolicy.Document, stream bool) error {
	if profile != selectedWireProfile {
		return errors.New("unsupported Gemini wire profile")
	}
	body := document.Body()
	if err := validateProtectedTopLevelShape(body); err != nil {
		return err
	}
	if err := validateCanonicalGenerationShape(body); err != nil {
		return err
	}
	contents, exists := document.Get("/contents")
	if !exists {
		return errors.New("gemini contents are required")
	}
	contentsValue := reflect.ValueOf(contents)
	if !contentsValue.IsValid() ||
		(contentsValue.Kind() != reflect.Slice && contentsValue.Kind() != reflect.Array) ||
		contentsValue.Len() == 0 {
		return errors.New("gemini contents must be a nonempty array")
	}
	store, exists := document.Get("/store")
	if !exists || store != false {
		return errors.New("gemini store=false invariant was not preserved")
	}
	if stream {
		// GenerateContent carries streaming in the endpoint/query, never the body.
		if _, exists := document.Get("/stream"); exists {
			return errors.New("gemini GenerateContent stream must not appear in the body")
		}
	}
	return nil
}

func validateProtectedTopLevelShape(body map[string]interface{}) error {
	counts := make(map[string]int)
	for key := range body {
		normalized := normalizedFieldName(key)
		switch normalized {
		case "contents", "store", "stream", "background", "previousinteractionid":
			counts[normalized]++
		}
		if normalized == "contents" && key != "contents" || normalized == "store" && key != "store" {
			return fmt.Errorf("gemini protected field %q must use its canonical shape", key)
		}
	}
	if counts["contents"] != 1 || counts["store"] != 1 {
		return errors.New("gemini contents/store invariants were not preserved")
	}
	if counts["stream"] != 0 || counts["background"] != 0 || counts["previousinteractionid"] != 0 {
		return errors.New("gemini request contains a forbidden stateful or transport field")
	}
	return nil
}

type requestSemantics struct {
	Request        *core.AIRequest
	Options        *core.AIOptions
	RequestedModel string
	SemanticModel  string
	ProviderAlias  string
	Surface        string
	Operation      string
	Purpose        string
	MergedExtras   map[string]interface{}
	Capabilities   modelCapabilities
}

func (semantics *requestSemantics) endpointRequest() ai.EndpointRequest {
	return ai.EndpointRequest{
		Provider:      "gemini",
		ProviderAlias: semantics.ProviderAlias,
		Surface:       semantics.Surface,
		ResolvedModel: semantics.SemanticModel,
		Operation:     semantics.Operation,
		Purpose:       semantics.Purpose,
	}
}

type preparedRequest struct {
	Model   string
	Body    []byte
	Headers http.Header
	Report  *core.AIRequestReport
	Profile wireProfile
}

type preparedInvocation struct {
	Request *preparedRequest
	Route   resolvedRoute
}

func (client *Client) prepareInvocation(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*preparedInvocation, error) {
	semantics, err := client.prepareSemantics(supplied, stream)
	if err != nil {
		return nil, err
	}
	route, err := client.resolveEndpoint(ctx, semantics.endpointRequest())
	if err != nil {
		return nil, err
	}
	profile, err := client.requestProfile(semantics, route)
	if err != nil {
		return nil, err
	}
	prepared, err := client.buildPolicyRequest(ctx, semantics, profile, stream)
	invocation := &preparedInvocation{Request: prepared, Route: route}
	if err != nil {
		return invocation, err
	}
	client.bindRoute(prepared, route)
	return invocation, nil
}

func (client *Client) prepareSemantics(supplied *core.AIRequest, stream bool) (*requestSemantics, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone Gemini AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone Gemini legacy request options: %w", err)
	}
	options = client.ApplyDefaults(options)
	if options.ReasoningEffort == "" {
		options.ReasoningEffort = client.defaultReasoning
	}
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	if err := validatePortableIntent(request.Generation); err != nil {
		return nil, err
	}
	requestedModel := options.Model
	options.Model = resolveModel(options.Model)
	capabilities, known := capabilitiesForModel(options.Model)
	if !known {
		capabilities = conservativeUnknownCapabilities(options.Model)
	}
	defaults, err := providers.CloneAIOptions(&core.AIOptions{Extra: client.defaultExtra})
	if err != nil {
		return nil, fmt.Errorf("clone Gemini client request extras: %w", err)
	}
	operation := "generate"
	if stream {
		operation = "stream"
	}
	return &requestSemantics{
		Request:        request,
		Options:        options,
		RequestedModel: requestedModel,
		SemanticModel:  options.Model,
		ProviderAlias:  "gemini",
		Surface:        selectedWireProfile.surface,
		Operation:      operation,
		Purpose:        request.Purpose,
		MergedExtras:   mergeGeminiExtras(defaults.Extra, options.Extra),
		Capabilities:   capabilities,
	}, nil
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
		if parameter.mode != core.AIParameterInherit && parameter.mode != core.AIParameterSet && parameter.mode != core.AIParameterOmit {
			return fmt.Errorf("invalid generation.%s mode %d", parameter.name, parameter.mode)
		}
	}
	if generation.MaxTokens.Mode == core.AIParameterSet && generation.MaxTokens.Value <= 0 {
		return errors.New("generation.max_tokens must be positive")
	}
	return nil
}

func (client *Client) requestProfile(*requestSemantics, resolvedRoute) (wireProfile, error) {
	return selectedWireProfile, nil
}

func (client *Client) buildPolicyRequest(
	ctx context.Context,
	semantics *requestSemantics,
	profile wireProfile,
	stream bool,
) (*preparedRequest, error) {
	request := semantics.Request
	options := semantics.Options
	generationConfig := map[string]interface{}{
		"maxOutputTokens": options.MaxTokens,
	}
	if !semantics.Capabilities.ForbidTemperature {
		generationConfig["temperature"] = options.Temperature
	}
	body := map[string]interface{}{
		"contents":         geminiContents(request.Prompt),
		"generationConfig": generationConfig,
		"store":            false,
	}
	if options.SystemPrompt != "" {
		body["systemInstruction"] = geminiSystemInstruction(options.SystemPrompt)
	}
	if options.ResponseFormat != "" {
		generationConfig["responseMimeType"] = normalizeResponseFormat(options.ResponseFormat)
	}
	if options.ReasoningEffort != "" {
		level, err := reasoningLevel(options.ReasoningEffort, semantics.Capabilities)
		if err != nil {
			return nil, err
		}
		generationConfig["thinkingConfig"] = map[string]interface{}{"thinkingLevel": level}
	}
	explicit := make(map[string]struct{})
	for _, key := range sortedAnyMapKeys(semantics.MergedExtras) {
		value := semantics.MergedExtras[key]
		if isProtectedBodyKey(key) {
			continue
		}
		if applyLegacyGenerationExtra(generationConfig, key, value, explicit) {
			continue
		}
		if _, exists := body[key]; !exists {
			body[key] = value
		}
	}

	if err := applyGenerationSets(body, request.Generation, explicit, semantics.Capabilities); err != nil {
		return nil, err
	}
	protectedHeaders := geminiProtectedHeaders(stream)
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Provider:       "gemini",
			ProviderAlias:  semantics.ProviderAlias,
			Surface:        semantics.Surface,
			Operation:      semantics.Operation,
			Purpose:        semantics.Purpose,
			RequestedModel: semantics.RequestedModel,
			ResolvedModel:  semantics.SemanticModel,
		},
		Body:                 body,
		Headers:              eligibleLegacyHeaders(protectedHeaders, client.defaultHeaders, options.Headers),
		ProtectedPaths:       []string{"/contents", "/store", "/stream", "/background", "/previous_interaction_id"},
		ProtectedHeaders:     sortedSetKeys(protectedHeaders),
		CaseInsensitivePaths: []string{"/contents", "/store", "/stream", "/background", "/previous_interaction_id"},
	})
	if err != nil {
		return nil, fmt.Errorf("create Gemini request draft: %w", err)
	}
	draft := &geminiDraft{Document: document, explicit: explicit, profile: profile, capabilities: semantics.Capabilities, stream: stream}
	portableAdjustments, err := applyGenerationOmits(draft, request.Generation)
	if err != nil {
		return nil, err
	}
	prepared := &preparedRequest{Model: semantics.SemanticModel, Profile: profile}
	if client.requestPolicy == nil {
		return prepared, errors.New("gemini request policy engine is not configured")
	}
	report, err := client.requestPolicy.Apply(ctx, draft, request.Patches)
	if report != nil {
		report.Adjustments = append(portableAdjustments, report.Adjustments...)
		prepared.Report = report
	}
	if err != nil {
		return prepared, err
	}
	encoded, err := json.Marshal(draft.Body())
	if err != nil {
		return prepared, fmt.Errorf("marshal Gemini request: %w", err)
	}
	prepared.Body = encoded
	prepared.Headers = finalizeHeaders(draft, stream)
	return prepared, nil
}

func mergeGeminiExtras(sources ...map[string]interface{}) map[string]interface{} {
	var merged map[string]interface{}
	for _, source := range sources {
		for _, key := range sortedAnyMapKeys(source) {
			if merged == nil {
				merged = make(map[string]interface{})
			}
			merged[canonicalGeminiExtraKey(key)] = source[key]
		}
	}
	return merged
}

func canonicalGeminiExtraKey(key string) string {
	switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
	case "temperature":
		return "temperature"
	case "topp":
		return "topP"
	case "topk":
		return "topK"
	case "candidatecount":
		return "candidateCount"
	default:
		return key
	}
}

func sortedAnyMapKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func applyLegacyGenerationExtra(
	config map[string]interface{},
	key string,
	value interface{},
	explicit map[string]struct{},
) bool {
	paths := selectedWireProfile.generationPaths()
	var canonicalKey, path string
	switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
	case "temperature":
		canonicalKey, path = "temperature", paths.Temperature
	case "topp":
		canonicalKey, path = "topP", paths.TopP
	case "topk":
		canonicalKey, path = "topK", paths.TopK
	case "candidatecount":
		canonicalKey, path = "candidateCount", paths.CandidateCount
	default:
		return false
	}
	config[canonicalKey] = value
	explicit[path] = struct{}{}
	return true
}

func applyGenerationSets(
	body map[string]interface{},
	generation core.AIGenerationOptions,
	explicit map[string]struct{},
	capabilities modelCapabilities,
) error {
	paths := selectedWireProfile.generationPaths()
	config := body["generationConfig"].(map[string]interface{})
	set := func(path, key string, mode core.AIParameterMode, value interface{}) error {
		if mode != core.AIParameterSet {
			return nil
		}
		config[key] = value
		explicit[path] = struct{}{}
		return nil
	}
	if err := set(paths.Temperature, "temperature", generation.Temperature.Mode, generation.Temperature.Value); err != nil {
		return err
	}
	if err := set(paths.TopP, "topP", generation.TopP.Mode, generation.TopP.Value); err != nil {
		return err
	}
	if err := set(paths.TopK, "topK", generation.TopK.Mode, generation.TopK.Value); err != nil {
		return err
	}
	if err := set(paths.MaxTokens, "maxOutputTokens", generation.MaxTokens.Mode, generation.MaxTokens.Value); err != nil {
		return err
	}
	if generation.SystemPrompt.Mode == core.AIParameterSet {
		body["systemInstruction"] = geminiSystemInstruction(generation.SystemPrompt.Value)
		explicit["/systemInstruction"] = struct{}{}
	}
	if generation.ResponseFormat.Mode == core.AIParameterSet {
		config["responseMimeType"] = normalizeResponseFormat(generation.ResponseFormat.Value)
		explicit[paths.ResponseFormat] = struct{}{}
	}
	if generation.ReasoningEffort.Mode == core.AIParameterSet {
		level, err := reasoningLevel(generation.ReasoningEffort.Value, capabilities)
		if err != nil {
			return err
		}
		config["thinkingConfig"] = map[string]interface{}{"thinkingLevel": level}
		explicit[paths.ReasoningLevel] = struct{}{}
	}
	return nil
}

func geminiContents(prompt string) []interface{} {
	return []interface{}{map[string]interface{}{
		"role":  "user",
		"parts": []interface{}{map[string]interface{}{"text": prompt}},
	}}
}

func geminiSystemInstruction(prompt string) map[string]interface{} {
	return map[string]interface{}{
		"parts": []interface{}{map[string]interface{}{"text": prompt}},
	}
}

func applyGenerationOmits(draft *geminiDraft, generation core.AIGenerationOptions) ([]core.AIRequestAdjustment, error) {
	paths := selectedWireProfile.generationPaths()
	parameters := []struct {
		path string
		mode core.AIParameterMode
	}{
		{paths.Temperature, generation.Temperature.Mode},
		{paths.TopP, generation.TopP.Mode},
		{paths.TopK, generation.TopK.Mode},
		{paths.MaxTokens, generation.MaxTokens.Mode},
		{"/systemInstruction", generation.SystemPrompt.Mode},
		{paths.ReasoningLevel, generation.ReasoningEffort.Mode},
		{paths.ResponseFormat, generation.ResponseFormat.Mode},
	}
	adjustments := make([]core.AIRequestAdjustment, 0)
	for _, parameter := range parameters {
		if parameter.mode != core.AIParameterOmit {
			continue
		}
		_, existed := draft.Get(parameter.path)
		if err := draft.Remove(parameter.path); err != nil {
			return nil, fmt.Errorf("apply portable omit %s: %w", parameter.path, err)
		}
		if existed {
			adjustments = append(adjustments, core.AIRequestAdjustment{Source: "portable", Rule: "generation-omit", Path: parameter.path, Action: "remove", Reason: "explicit portable omit"})
		}
	}
	return adjustments, nil
}

func reasoningLevel(effort string, capabilities modelCapabilities) (string, error) {
	level := strings.ToLower(strings.TrimSpace(effort))
	if level == "none" || level == "xhigh" || !capabilities.ThinkingLevels.supports(level) {
		return "", &core.AIRequestFeatureError{ClientType: "*gemini.Client", Feature: "generation.reasoning_effort." + level}
	}
	return level, nil
}

func normalizeResponseFormat(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "json") {
		return "application/json"
	}
	return strings.TrimSpace(value)
}

func isProtectedBodyKey(key string) bool {
	switch strings.ToLower(key) {
	case "contents", "generationconfig", "systeminstruction", "store", "stream", "background", "previous_interaction_id":
		return true
	default:
		return false
	}
}

func geminiProtectedHeaders(stream bool) map[string]struct{} {
	protected := map[string]struct{}{"content-type": {}, "x-goog-api-key": {}, "authorization": {}}
	if stream {
		protected["accept"] = struct{}{}
	}
	return protected
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func eligibleLegacyHeaders(protected map[string]struct{}, sources ...map[string]string) map[string]string {
	headers := make(http.Header)
	for _, source := range sources {
		keys := make([]string, 0, len(source))
		for key := range source {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, blocked := protected[strings.ToLower(key)]; !blocked {
				headers.Set(key, source[key])
			}
		}
	}
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			result[key] = values[len(values)-1]
		}
	}
	return result
}

func finalizeHeaders(draft *geminiDraft, stream bool) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if stream {
		headers.Set("Accept", "text/event-stream")
	}
	providers.ApplyLegacyHeaders(headers, geminiProtectedHeaders(stream), draft.Headers(), nil)
	return headers
}

func (client *Client) recordRequestPreparation(ctx context.Context, span core.Span, prepared *preparedRequest) {
	if prepared == nil || prepared.Report == nil {
		return
	}
	report := prepared.Report
	span.SetAttribute("ai.request.provider_alias", report.ProviderAlias)
	span.SetAttribute("ai.request.surface", report.Surface)
	span.SetAttribute("ai.request.operation", report.Operation)
	span.SetAttribute("ai.request.policy_stable", report.Stable)
	span.SetAttribute("ai.request.adjustment_count", len(report.Adjustments))
	if report.Fingerprint != "" {
		span.SetAttribute("ai.request.policy_fingerprint", report.Fingerprint)
	}
	if client.Logger == nil {
		return
	}
	fields := map[string]interface{}{
		"operation":        "ai_request_policy",
		"provider":         "gemini",
		"provider_alias":   "gemini",
		"model":            prepared.Model,
		"surface":          report.Surface,
		"policy_stable":    report.Stable,
		"adjustment_count": len(report.Adjustments),
	}
	providers.AddObservationRequestID(ctx, fields)
	client.Logger.DebugWithContext(ctx, "Gemini request policy evaluated", fields)
}
