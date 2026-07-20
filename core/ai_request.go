package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
)

// noUnkeyedLiterals prevents positional literals for public structs that are
// intended to evolve without breaking callers.
type noUnkeyedLiterals struct{}

// AIParameterMode describes how a portable generation parameter should be
// resolved.
type AIParameterMode uint8

const (
	// AIParameterInherit leaves the parameter to lower-precedence defaults.
	AIParameterInherit AIParameterMode = iota
	// AIParameterSet explicitly supplies the parameter, including its zero value.
	AIParameterSet
	// AIParameterOmit requires the provider request not to contain the parameter.
	AIParameterOmit
)

// AIParameter carries presence-aware intent for an optional provider
// parameter. Its zero value means inherit.
type AIParameter[T any] struct {
	_     noUnkeyedLiterals
	Mode  AIParameterMode
	Value T
}

// InheritAIParameter returns a parameter that inherits lower-precedence intent.
func InheritAIParameter[T any]() AIParameter[T] {
	return AIParameter[T]{Mode: AIParameterInherit}
}

// SetAIParameter returns a parameter that explicitly supplies value.
func SetAIParameter[T any](value T) AIParameter[T] {
	return AIParameter[T]{Mode: AIParameterSet, Value: value}
}

// OmitAIParameter returns a parameter that must be absent from the provider
// request.
func OmitAIParameter[T any]() AIParameter[T] {
	return AIParameter[T]{Mode: AIParameterOmit}
}

// AIGenerationOptions contains portable, presence-aware generation intent.
type AIGenerationOptions struct {
	_ noUnkeyedLiterals
	// Model selects the provider model. An empty value inherits model selection;
	// model is structural and cannot be explicitly omitted.
	Model           string
	Temperature     AIParameter[float32]
	TopP            AIParameter[float32]
	TopK            AIParameter[int]
	MaxTokens       AIParameter[int]
	SystemPrompt    AIParameter[string]
	ReasoningEffort AIParameter[string]
	ResponseFormat  AIParameter[string]
}

// AIProviderSelector scopes a provider patch to matching request identity.
type AIProviderSelector struct {
	_             noUnkeyedLiterals
	Provider      string
	ProviderAlias string
	Surface       string
	Model         string
	Operation     string
	Purpose       string
	AllProviders  bool
}

// AIProviderPatch describes ordered provider-native body and header edits.
// Body paths use RFC 6901 JSON Pointer syntax.
type AIProviderPatch struct {
	_        noUnkeyedLiterals
	Name     string
	Version  string
	Selector AIProviderSelector
	// Set maps body paths to literal values. Values must be JSON-native:
	// string-keyed maps, slices, arrays, finite scalars, or nil. Pointers,
	// structs, non-finite floats, non-string map keys, and cycles are rejected.
	// A nil value means JSON null, not removal.
	Set           map[string]interface{}
	Remove        []string
	SetHeaders    map[string]string
	RemoveHeaders []string
}

// AIRequest is the provider-neutral request envelope for request-capable AI
// clients.
type AIRequest struct {
	_      noUnkeyedLiterals
	Prompt string
	// Purpose is a stable, provider-neutral, non-secret operation label. It
	// may appear in sanitized request reports, policy selectors, and traces.
	Purpose    string
	Generation AIGenerationOptions
	Patches    []AIProviderPatch

	legacyOptions *AIOptions
}

// AIResult contains the legacy normalized response and optional request and
// usage details.
type AIResult struct {
	_             noUnkeyedLiterals
	Response      *AIResponse
	RequestReport *AIRequestReport
	UsageDetails  *AIUsageDetails
}

// AIRequestClient adds the provider-neutral request/result capability to an
// AIClient without changing the legacy interface.
type AIRequestClient interface {
	AIClient
	Generate(context.Context, *AIRequest) (*AIResult, error)
}

// StreamingAIRequestClient adds request-aware streaming to AIRequestClient.
type StreamingAIRequestClient interface {
	AIRequestClient
	Stream(context.Context, *AIRequest, StreamCallback) (*AIResult, error)
}

// AIRequestReport contains sanitized, reproducible request preparation facts.
// It must not contain prompts, credentials, raw bodies, or secret field values.
type AIRequestReport struct {
	_              noUnkeyedLiterals
	Provider       string
	ProviderAlias  string
	Surface        string
	Operation      string
	Purpose        string
	RequestedModel string
	ResolvedModel  string
	Adjustments    []AIRequestAdjustment
	Fingerprint    string
	Stable         bool
}

// AIRequestAdjustment records a sanitized change to effective request intent.
type AIRequestAdjustment struct {
	_      noUnkeyedLiterals
	Source string
	Rule   string
	Path   string
	Action string
	Reason string
}

// AIUsageDetails contains normalized, optional provider usage counters.
type AIUsageDetails struct {
	_                 noUnkeyedLiterals
	CachedInputTokens int64
	ReasoningTokens   int64
	AudioInputTokens  int64
	AudioOutputTokens int64
	Counters          map[string]int64
}

// NewAIRequest constructs a provider-neutral AI request.
func NewAIRequest(prompt, purpose string) *AIRequest {
	return &AIRequest{Prompt: prompt, Purpose: purpose}
}

// NewAIRequestFromLegacy constructs a request with an isolated snapshot of
// legacy options. Map, slice, and array containers in Extra are copied
// recursively; opaque leaves are retained by reference for backward
// compatibility and are never mutated by the framework. The snapshot is
// available only to provider and fallback adapters.
func NewAIRequestFromLegacy(prompt, purpose string, options *AIOptions) *AIRequest {
	return &AIRequest{
		Prompt:        prompt,
		Purpose:       purpose,
		legacyOptions: cloneLegacyAIOptions(options),
	}
}

// LegacyOptions returns an isolated copy of the request's legacy option
// snapshot. Container values are recursively copied, while opaque leaves retain
// the backward-compatible shared-reference behavior described by
// NewAIRequestFromLegacy. It is not an application mutation surface.
func (r *AIRequest) LegacyOptions() *AIOptions {
	if r == nil {
		return nil
	}
	return cloneLegacyAIOptions(r.legacyOptions)
}

// CloneAIRequest returns a request-local copy. Provider patch values must be
// composed exclusively of JSON-compatible maps, slices, and scalar values.
func CloneAIRequest(request *AIRequest) (*AIRequest, error) {
	if request == nil {
		return nil, errors.New("AI request is nil")
	}

	patches, err := cloneProviderPatches(request.Patches)
	if err != nil {
		return nil, fmt.Errorf("clone AI request patches: %w", err)
	}
	clone := *request
	clone.Patches = patches
	clone.legacyOptions = cloneLegacyAIOptions(request.legacyOptions)
	return &clone, nil
}

// GenerateAI dispatches to the request capability when present. A legacy-only
// client is used only when it can represent the request without discarding new
// semantics.
func GenerateAI(ctx context.Context, client AIClient, request *AIRequest) (*AIResult, error) {
	if client == nil {
		return nil, errors.New("AI client is nil")
	}
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	if advanced, ok := client.(AIRequestClient); ok {
		return advanced.Generate(ctx, request)
	}

	if feature := request.firstUnsupportedLegacyFeature(); feature != "" {
		return nil, &AIRequestFeatureError{
			ClientType: fmt.Sprintf("%T", client),
			Feature:    feature,
		}
	}

	response, err := client.GenerateResponse(ctx, request.Prompt, request.toLegacyOptions())
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("AI client returned a nil response without error")
	}
	return &AIResult{
		Response: response,
		RequestReport: &AIRequestReport{
			Provider:      response.Provider,
			ResolvedModel: response.Model,
			Purpose:       request.Purpose,
			Stable:        false,
		},
	}, nil
}

// StreamAI dispatches to the request-aware streaming capability when present.
// A legacy streaming client is used only when it can represent the request
// without discarding provider-neutral semantics.
func StreamAI(
	ctx context.Context,
	client AIClient,
	request *AIRequest,
	callback StreamCallback,
) (*AIResult, error) {
	if client == nil {
		return nil, errors.New("AI client is nil")
	}
	if request == nil {
		return nil, errors.New("AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("AI stream callback is nil")
	}
	if advanced, ok := client.(StreamingAIRequestClient); ok {
		return advanced.Stream(ctx, request, callback)
	}

	legacy, ok := client.(StreamingAIClient)
	if !ok || !legacy.SupportsStreaming() {
		return nil, &AIRequestFeatureError{
			ClientType: fmt.Sprintf("%T", client),
			Feature:    "streaming",
		}
	}
	if feature := request.firstUnsupportedLegacyFeature(); feature != "" {
		return nil, &AIRequestFeatureError{
			ClientType: fmt.Sprintf("%T", client),
			Feature:    feature,
		}
	}

	response, err := legacy.StreamResponse(ctx, request.Prompt, request.toLegacyOptions(), callback)
	if response == nil {
		if err == nil {
			return nil, errors.New("AI client returned a nil streaming response without error")
		}
		return nil, err
	}
	return &AIResult{
		Response: response,
		RequestReport: &AIRequestReport{
			Provider:      response.Provider,
			Operation:     "stream",
			Purpose:       request.Purpose,
			ResolvedModel: response.Model,
			Stable:        false,
		},
	}, err
}

func (r *AIRequest) firstUnsupportedLegacyFeature() string {
	if len(r.Patches) > 0 {
		return "provider_patches"
	}

	if feature := unsupportedLegacyParameter("temperature", r.Generation.Temperature); feature != "" {
		return feature
	}
	if r.Generation.TopP.Mode != AIParameterInherit {
		return "generation.top_p"
	}
	if r.Generation.TopK.Mode != AIParameterInherit {
		return "generation.top_k"
	}
	if feature := unsupportedLegacyParameter("max_tokens", r.Generation.MaxTokens); feature != "" {
		return feature
	}
	if feature := unsupportedLegacyParameter("system_prompt", r.Generation.SystemPrompt); feature != "" {
		return feature
	}
	if feature := unsupportedLegacyParameter("reasoning_effort", r.Generation.ReasoningEffort); feature != "" {
		return feature
	}
	return unsupportedLegacyParameter("response_format", r.Generation.ResponseFormat)
}

func unsupportedLegacyParameter[T comparable](name string, parameter AIParameter[T]) string {
	feature := "generation." + name
	switch parameter.Mode {
	case AIParameterInherit:
		return ""
	case AIParameterSet:
		var zero T
		if parameter.Value == zero {
			return feature
		}
		return ""
	case AIParameterOmit:
		return feature
	default:
		return feature + ".mode"
	}
}

func (r *AIRequest) toLegacyOptions() *AIOptions {
	options := cloneLegacyAIOptions(r.legacyOptions)
	hasOptions := options != nil
	ensureOptions := func() {
		if options == nil {
			options = &AIOptions{}
		}
		hasOptions = true
	}

	if r.Generation.Model != "" {
		ensureOptions()
		options.Model = r.Generation.Model
	}
	if r.Generation.Temperature.Mode == AIParameterSet {
		ensureOptions()
		options.Temperature = r.Generation.Temperature.Value
	}
	if r.Generation.MaxTokens.Mode == AIParameterSet {
		ensureOptions()
		options.MaxTokens = r.Generation.MaxTokens.Value
	}
	if r.Generation.SystemPrompt.Mode == AIParameterSet {
		ensureOptions()
		options.SystemPrompt = r.Generation.SystemPrompt.Value
	}
	if r.Generation.ReasoningEffort.Mode == AIParameterSet {
		ensureOptions()
		options.ReasoningEffort = r.Generation.ReasoningEffort.Value
	}
	if r.Generation.ResponseFormat.Mode == AIParameterSet {
		ensureOptions()
		options.ResponseFormat = r.Generation.ResponseFormat.Value
	}

	if !hasOptions {
		return nil
	}
	return options
}

type cloneVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

func cloneLegacyAIOptions(options *AIOptions) *AIOptions {
	if options == nil {
		return nil
	}

	clone := *options
	clone.Headers = cloneStringMap(options.Headers)
	if options.Extra != nil {
		clone.Extra = cloneLegacyReflect(
			reflect.ValueOf(options.Extra),
			make(map[cloneVisit]reflect.Value),
		).Interface().(map[string]interface{})
	}
	return &clone
}

func cloneLegacyReflect(value reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(cloneLegacyReflect(value.Elem(), seen))
		return clone
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{kind: reflect.Map, ptr: value.Pointer()}
		if existing, ok := seen[visit]; ok {
			return existing
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[visit] = clone
		iterator := value.MapRange()
		for iterator.Next() {
			clone.SetMapIndex(iterator.Key(), cloneLegacyReflect(iterator.Value(), seen))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{kind: reflect.Slice, ptr: value.Pointer()}
		if existing, ok := seen[visit]; ok {
			return existing
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		seen[visit] = clone
		for index := range value.Len() {
			clone.Index(index).Set(cloneLegacyReflect(value.Index(index), seen))
		}
		return clone
	case reflect.Array:
		clone := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			clone.Index(index).Set(cloneLegacyReflect(value.Index(index), seen))
		}
		return clone
	default:
		return value
	}
}

func cloneProviderPatches(patches []AIProviderPatch) ([]AIProviderPatch, error) {
	if patches == nil {
		return nil, nil
	}

	clone := make([]AIProviderPatch, len(patches))
	for index, patch := range patches {
		clone[index] = patch
		clone[index].Remove = append([]string(nil), patch.Remove...)
		clone[index].SetHeaders = cloneStringMap(patch.SetHeaders)
		clone[index].RemoveHeaders = append([]string(nil), patch.RemoveHeaders...)
		if patch.Set == nil {
			continue
		}

		clone[index].Set = make(map[string]interface{}, len(patch.Set))
		for path, value := range patch.Set {
			clonedValue, err := clonePatchValue(value, make(map[cloneVisit]struct{}))
			if err != nil {
				return nil, fmt.Errorf("patch %q path %q: %w", patch.Name, path, err)
			}
			clone[index].Set[path] = clonedValue
		}
	}
	return clone, nil
}

func clonePatchValue(value interface{}, active map[cloneVisit]struct{}) (interface{}, error) {
	if value == nil {
		return nil, nil
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.String:
		return value, nil
	case reflect.Float32, reflect.Float64:
		if number := reflected.Float(); math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("non-finite floating-point value %v is not JSON-compatible", number)
		}
		return value, nil
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("unsupported map key type %s", reflected.Type().Key())
		}
		if reflected.IsNil() {
			return value, nil
		}
		visit := cloneVisit{kind: reflect.Map, ptr: reflected.Pointer()}
		if _, exists := active[visit]; exists {
			return nil, errors.New("cyclic map value is not JSON-compatible")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		clone := reflect.MakeMapWithSize(reflected.Type(), reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			item, err := clonePatchValue(iterator.Value().Interface(), active)
			if err != nil {
				return nil, err
			}
			clone.SetMapIndex(iterator.Key(), clonedReflectValue(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	case reflect.Slice:
		if reflected.IsNil() {
			return value, nil
		}
		visit := cloneVisit{kind: reflect.Slice, ptr: reflected.Pointer()}
		if _, exists := active[visit]; exists {
			return nil, errors.New("cyclic slice value is not JSON-compatible")
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		clone := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
		for index := range reflected.Len() {
			item, err := clonePatchValue(reflected.Index(index).Interface(), active)
			if err != nil {
				return nil, err
			}
			clone.Index(index).Set(clonedReflectValue(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	case reflect.Array:
		clone := reflect.New(reflected.Type()).Elem()
		for index := range reflected.Len() {
			item, err := clonePatchValue(reflected.Index(index).Interface(), active)
			if err != nil {
				return nil, err
			}
			clone.Index(index).Set(clonedReflectValue(item, reflected.Type().Elem()))
		}
		return clone.Interface(), nil
	default:
		return nil, fmt.Errorf("unsupported JSON-compatible value type %T", value)
	}
}

func clonedReflectValue(value interface{}, target reflect.Type) reflect.Value {
	if value == nil {
		return reflect.Zero(target)
	}
	return reflect.ValueOf(value)
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
