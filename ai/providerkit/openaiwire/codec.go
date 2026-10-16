// Package openaiwire implements the reusable OpenAI-compatible chat
// completions wire contract. It deliberately does not own endpoint routing,
// credentials, retries, provider identity, or telemetry.
package openaiwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/ai/internal/openaimodel"
	"github.com/truvaagents/truva-g3/ai/internal/wirevalue"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const (
	// DefaultSurfaceVersion is the versioned identity of the stock chat
	// completions adapter.
	DefaultSurfaceVersion = "openai-chat-completions-v2"
	// DefaultReasoningTokenMultiplier leaves room for reasoning as well as an
	// answer on scaling profiles. Explicit 1 keeps the configured budget exact.
	DefaultReasoningTokenMultiplier = 3
	// DefaultMaxSSEEventBytes bounds one decoded server-sent event to 1 MiB.
	DefaultMaxSSEEventBytes = 1 << 20
	maxWireErrorTypeBytes   = 64
)

// Codec builds and decodes OpenAI-compatible chat completion requests without
// coupling reusable wire behavior to one endpoint or authentication scheme.
type Codec interface {
	BuildDraft(request *core.AIRequest, resolvedModel string, stream bool) (*Draft, error)
	Encode(draft *Draft) ([]byte, error)
	Decode(response io.Reader) (*core.AIResult, error)
	DecodeStream(response io.Reader, callback core.StreamCallback) (*core.AIResult, error)
	SurfaceVersion() string
}

// ProfiledCodec adds explicit semantic and wire-profile draft construction.
type ProfiledCodec interface {
	Codec
	BuildDraftWithProfile(request *core.AIRequest, profile RequestProfile, stream bool) (*Draft, error)
}

// Config controls wire-level behavior that differs among OpenAI-compatible
// deployments. Its zero value retains the stock OpenAI defaults.
type Config struct {
	SurfaceVersion           string
	ReasoningTokenMultiplier int
	DefaultReasoningEffort   string
	ForceReasoningObject     bool
	// MaxSSEEventBytes bounds one complete server-sent event. Non-positive
	// values use DefaultMaxSSEEventBytes.
	MaxSSEEventBytes int
	// ReasoningContentFallback explicitly restores the legacy substitution of
	// reasoning for an empty answer (and mixed streaming content). The default
	// keeps the channels separate. Both modes retain provenance and reasoning.
	ReasoningContentFallback bool
}

type codec struct {
	surfaceVersion           string
	reasoningTokenMultiplier int
	defaultReasoningEffort   string
	forceReasoningObject     bool
	maxSSEEventBytes         int
	reasoningContentFallback bool
}

// NewCodec constructs a reusable codec with stock OpenAI reasoning defaults.
func NewCodec(surfaceVersion string) (Codec, error) {
	return NewConfiguredCodec(Config{SurfaceVersion: surfaceVersion})
}

// NewConfiguredCodec constructs a codec with explicit wire-level behavior.
func NewConfiguredCodec(config Config) (Codec, error) {
	return newConfiguredCodec(config)
}

// NewProfiledCodec constructs a codec that exposes explicit wire profiles.
func NewProfiledCodec(config Config) (ProfiledCodec, error) {
	return newConfiguredCodec(config)
}

func newConfiguredCodec(config Config) (*codec, error) {
	version := strings.TrimSpace(config.SurfaceVersion)
	if version == "" {
		return nil, errors.New("OpenAI wire surface version is empty")
	}
	multiplier := config.ReasoningTokenMultiplier
	if multiplier < 0 {
		return nil, errors.New("reasoning token multiplier must be positive or zero (unset)")
	}
	if multiplier == 0 {
		multiplier = DefaultReasoningTokenMultiplier
	}
	maxSSEEventBytes := config.MaxSSEEventBytes
	if maxSSEEventBytes <= 0 {
		maxSSEEventBytes = DefaultMaxSSEEventBytes
	}
	return &codec{
		surfaceVersion:           version,
		reasoningTokenMultiplier: multiplier,
		defaultReasoningEffort:   config.DefaultReasoningEffort,
		forceReasoningObject:     config.ForceReasoningObject,
		maxSSEEventBytes:         maxSSEEventBytes,
		reasoningContentFallback: config.ReasoningContentFallback,
	}, nil
}

func (c *codec) SurfaceVersion() string { return c.surfaceVersion }

// Draft is an isolated, policy-editable OpenAI chat completions document.
// BindIdentity should be called before policy evaluation when a provider needs
// selectors and reports to carry a non-default alias.
type Draft struct {
	*requestpolicy.Document
	surfaceVersion  string
	profileIdentity string
	semanticModel   string
	wireModel       string
	modelField      ModelFieldMode
	tokenLimit      TokenLimitField
	reasoningStyle  ReasoningEffortStyle
	sampling        SamplingPolicy
	stream          bool
	explicit        map[string]struct{}
	adjustments     []core.AIRequestAdjustment
	headerConflicts []string
}

// BindIdentity supplies the provider-owned selector and report identity while
// preserving the codec-owned surface, operation, purpose, and model facts.
func (d *Draft) BindIdentity(provider, providerAlias string) error {
	if d == nil || d.Document == nil {
		return errors.New("OpenAI wire draft is nil")
	}
	if strings.TrimSpace(provider) == "" {
		return errors.New("OpenAI wire provider identity is empty")
	}
	info := d.Info()
	info.Provider = provider
	info.ProviderAlias = providerAlias
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info:                 info,
		Body:                 d.Body(),
		Headers:              d.Headers(),
		ProtectedPaths:       protectedPaths(),
		ProtectedHeaders:     protectedHeaders(d.stream),
		CaseInsensitivePaths: caseInsensitivePaths(),
	})
	if err != nil {
		return fmt.Errorf("bind OpenAI wire draft identity: %w", err)
	}
	d.Document = document
	return nil
}

// Adjustments returns portable preparation changes made before request-policy
// evaluation. The returned slice is isolated from the draft.
func (d *Draft) Adjustments() []core.AIRequestAdjustment {
	if d == nil {
		return nil
	}
	return append([]core.AIRequestAdjustment(nil), d.adjustments...)
}

// ProtectedHeaderConflicts returns legacy header names ignored because the
// provider transport owns them.
func (d *Draft) ProtectedHeaderConflicts() []string {
	if d == nil {
		return nil
	}
	return append([]string(nil), d.headerConflicts...)
}

// HasExplicitIntent lets strict compatibility mode identify portable values.
func (d *Draft) HasExplicitIntent(path string) bool {
	_, ok := d.explicit[path]
	return ok
}

// PolicyFingerprintIdentity supplies the versioned codec identity.
func (d *Draft) PolicyFingerprintIdentity() string { return d.profileIdentity }

// EffectiveGenerationPaths maps provider-local wire fields to the sanitized
// provider-neutral request report.
func (d *Draft) EffectiveGenerationPaths() (string, string) {
	return "/temperature", tokenLimitPath(d.tokenLimit)
}

// Validate checks invariants after every policy layer has run.
func (d *Draft) Validate() error {
	if d == nil || d.Document == nil {
		return errors.New("OpenAI wire draft is nil")
	}
	model, modelPresent := d.Get("/model")
	switch d.modelField {
	case ModelFieldRequired:
		if !modelPresent || model != d.wireModel {
			return errors.New("wire model invariant was not preserved")
		}
	case ModelFieldOmitted:
		if modelPresent {
			return errors.New("wire model omission invariant was not preserved")
		}
	default:
		return errors.New("wire model-field invariant is invalid")
	}
	messages, ok := d.Get("/messages")
	if !ok || !hasMessages(messages) {
		return errors.New("messages input is required")
	}
	stream, exists := d.Get("/stream")
	if d.stream {
		if !exists || stream != true {
			return errors.New("streaming invariant was not preserved")
		}
		options, ok := d.Get("/stream_options")
		if !ok || !includesUsage(options) {
			return errors.New("stream usage invariant was not preserved")
		}
	} else if exists {
		return errors.New("non-streaming request cannot enable streaming")
	}
	_, maxTokens := d.Get("/max_tokens")
	_, maxCompletionTokens := d.Get("/max_completion_tokens")
	if maxTokens && maxCompletionTokens {
		return errors.New("max_tokens and max_completion_tokens cannot both be set")
	}
	if d.tokenLimit == TokenLimitMaxTokens && maxCompletionTokens {
		return errors.New("max_completion_tokens is incompatible with the wire profile")
	}
	if d.tokenLimit == TokenLimitMaxCompletionTokens && maxTokens {
		return errors.New("max_tokens is incompatible with the wire profile")
	}
	for _, path := range []string{"/max_tokens", "/max_completion_tokens"} {
		if value, present := d.Get(path); present {
			if err := validatePositiveInteger(path, value); err != nil {
				return err
			}
		}
	}
	_, nestedReasoning := d.Get("/reasoning")
	_, topLevelReasoning := d.Get("/reasoning_effort")
	switch d.reasoningStyle {
	case ReasoningEffortOmitted:
		if nestedReasoning || topLevelReasoning {
			return errors.New("reasoning effort is incompatible with the wire profile")
		}
	case ReasoningEffortTopLevel:
		if nestedReasoning {
			return errors.New("nested reasoning effort is incompatible with the wire profile")
		}
	case ReasoningEffortNestedObject:
		if topLevelReasoning {
			return errors.New("top-level reasoning effort is incompatible with the wire profile")
		}
	default:
		return errors.New("wire reasoning-effort invariant is invalid")
	}
	if d.sampling == SamplingReasoningRestricted && profileReasoningEffort(d.Body(), d.reasoningStyle) != "none" {
		if _, present := d.Get("/temperature"); present {
			return &core.AIRequestFeatureError{
				ClientType: "openaiwire.Codec",
				Feature:    "generation.temperature",
			}
		}
	}
	return nil
}

func (c *codec) BuildDraft(request *core.AIRequest, resolvedModel string, stream bool) (*Draft, error) {
	profile := RequestProfile{
		SemanticModel:   resolvedModel,
		WireModel:       resolvedModel,
		ModelField:      ModelFieldRequired,
		TokenLimit:      TokenLimitMaxTokens,
		TokenBudget:     TokenBudgetExact,
		ReasoningEffort: ReasoningEffortOmitted,
		Sampling:        SamplingOrdinary,
	}
	if IsReasoningModel(resolvedModel) {
		profile.TokenLimit = TokenLimitMaxCompletionTokens
		profile.TokenBudget = TokenBudgetScaleForReasoning
		profile.ReasoningEffort = ReasoningEffortNestedObject
		profile.Sampling = SamplingReasoningRestricted
	} else if c.forceReasoningObject || request != nil && request.Generation.ReasoningEffort.Mode == core.AIParameterSet {
		profile.ReasoningEffort = ReasoningEffortNestedObject
	}
	return c.BuildDraftWithProfile(request, profile, stream)
}

// BuildDraftWithProfile constructs one isolated policy-editable request draft.
func (c *codec) BuildDraftWithProfile(request *core.AIRequest, profile RequestProfile, stream bool) (*Draft, error) {
	if request == nil {
		return nil, errors.New("OpenAI wire AI request is nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	request, err := core.CloneAIRequest(request)
	if err != nil {
		return nil, fmt.Errorf("clone OpenAI wire AI request: %w", err)
	}
	options := request.LegacyOptions()
	if options == nil {
		options = &core.AIOptions{}
	}

	temperature := options.Temperature
	maxTokens := options.MaxTokens
	systemPrompt := options.SystemPrompt
	reasoningEffort := options.ReasoningEffort
	responseFormat := options.ResponseFormat
	explicit := make(map[string]struct{})

	if value, set, err := resolveParameter("temperature", request.Generation.Temperature); err != nil {
		return nil, err
	} else if set {
		temperature = value
		explicit["/temperature"] = struct{}{}
	}
	if value, set, err := resolveParameter("max_tokens", request.Generation.MaxTokens); err != nil {
		return nil, err
	} else if set {
		if value <= 0 {
			return nil, errors.New("generation.max_tokens must be positive")
		}
		maxTokens = value
		explicit[tokenLimitPath(profile.TokenLimit)] = struct{}{}
	}
	if value, set, err := resolveParameter("system_prompt", request.Generation.SystemPrompt); err != nil {
		return nil, err
	} else if set {
		systemPrompt = value
		explicit["/messages"] = struct{}{}
	}
	if value, set, err := resolveParameter("reasoning_effort", request.Generation.ReasoningEffort); err != nil {
		return nil, err
	} else if set {
		reasoningEffort = value
		if path := reasoningEffortPath(profile.ReasoningEffort); path != "" {
			explicit[path] = struct{}{}
		}
	}
	if value, set, err := resolveParameter("response_format", request.Generation.ResponseFormat); err != nil {
		return nil, err
	} else if set {
		responseFormat = value
		explicit["/response_format"] = struct{}{}
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterOmit {
		responseFormat = ""
	} else {
		responseFormat, err = portableResponseFormatType(responseFormat)
		if err != nil {
			return nil, err
		}
	}
	if request.Generation.TopK.Mode == core.AIParameterSet {
		return nil, &core.AIRequestFeatureError{ClientType: "openaiwire.Codec", Feature: "generation.top_k"}
	}
	if request.Generation.TopK.Mode != core.AIParameterInherit && request.Generation.TopK.Mode != core.AIParameterOmit {
		return nil, fmt.Errorf("invalid generation.top_k mode %d", request.Generation.TopK.Mode)
	}
	if value, set, err := resolveParameter("top_p", request.Generation.TopP); err != nil {
		return nil, err
	} else if set {
		explicit["/top_p"] = struct{}{}
		options.Extra = cloneExtraTopLevel(options.Extra)
		options.Extra["top_p"] = value
	}

	// Omission controls budget and sampling preparation too, not just the final
	// removal of the wire field below.
	if request.Generation.ReasoningEffort.Mode == core.AIParameterOmit {
		reasoningEffort = ""
	} else if reasoningEffort == "" {
		reasoningEffort = c.defaultReasoningEffort
	}
	modelDefaultReasoning := profile.ReasoningActiveByDefault && reasoningEffort == "" &&
		request.Generation.ReasoningEffort.Mode != core.AIParameterOmit &&
		!hasKeyFold(options.Extra, "reasoning") && !hasKeyFold(options.Extra, "reasoning_effort")
	reasoningActive := (reasoningEffort != "" && reasoningEffort != "none") || modelDefaultReasoning
	boundedHeadroom := headroomExceedsLimit(maxTokens, c.reasoningTokenMultiplier, profile.ReasoningHeadroomLimit)
	if request.Generation.MaxTokens.Mode != core.AIParameterOmit &&
		profile.TokenBudget == TokenBudgetScaleForReasoning &&
		reasoningActive && !boundedHeadroom &&
		maxTokens > math.MaxInt/c.reasoningTokenMultiplier {
		return nil, errors.New("reasoning output budget overflows; reduce max tokens or multiplier")
	}
	legacySystemPrompt := options.SystemPrompt
	if request.Generation.SystemPrompt.Mode == core.AIParameterOmit {
		systemPrompt = ""
	}
	messages := make([]map[string]string, 0, 2)
	if request.Generation.SystemPrompt.Mode != core.AIParameterOmit &&
		(systemPrompt != "" || request.Generation.SystemPrompt.Mode == core.AIParameterSet) {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	}
	messages = append(messages, map[string]string{"role": "user", "content": request.Prompt})

	body := buildProfiledBody(
		profile,
		messages,
		maxTokens,
		temperature,
		stream,
		c.reasoningTokenMultiplier,
		reasoningEffort,
		reasoningActive,
	)
	nativeReasoningOverridden := false
	if profile.ReasoningEffort == ReasoningEffortNestedObject &&
		reasoningEffort != "" && request.Generation.ReasoningEffort.Mode != core.AIParameterOmit {
		nativeReasoningOverridden = mergeNativeReasoningSiblings(body, options.Extra, reasoningEffort)
	}
	if responseFormat != "" {
		body["response_format"] = map[string]interface{}{"type": responseFormat}
	}
	for key, value := range options.Extra {
		if hasKeyFold(body, key) || protectedStructuralKey(key) {
			continue
		}
		body[key] = value
	}
	if request.Generation.Temperature.Mode == core.AIParameterSet {
		removeKeyFold(body, "temperature")
		body["temperature"] = temperature
	}
	if request.Generation.TopP.Mode == core.AIParameterSet {
		removeKeyFold(body, "top_p")
		body["top_p"] = options.Extra["top_p"]
	}
	if request.Generation.ReasoningEffort.Mode == core.AIParameterSet {
		applyReasoningEffort(body, profile.ReasoningEffort, reasoningEffort)
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterSet {
		removeKeyFold(body, "response_format")
		if responseFormat != "" {
			body["response_format"] = map[string]interface{}{"type": responseFormat}
		}
	}

	adjustments := make([]core.AIRequestAdjustment, 0, 8)
	if request.Generation.MaxTokens.Mode != core.AIParameterOmit && maxTokens > 0 &&
		profile.TokenBudget == TokenBudgetScaleForReasoning && c.reasoningTokenMultiplier > 1 &&
		reasoningActive {
		rule, reason := "reasoning-headroom-v1", "shared reasoning and answer allowance"
		if modelDefaultReasoning {
			rule, reason = "model-default-reasoning-headroom-v1", "reviewed model default uses shared reasoning and answer allowance"
		}
		adjustments = append(adjustments, core.AIRequestAdjustment{
			Source: "wire-profile", Rule: rule, Path: tokenLimitPath(profile.TokenLimit),
			Action: "scale", Reason: reason,
		})
		if boundedHeadroom {
			adjustments = append(adjustments, core.AIRequestAdjustment{
				Source: "wire-profile", Rule: "reasoning-headroom-ceiling-v1", Path: tokenLimitPath(profile.TokenLimit),
				Action: "cap", Reason: "derived reasoning headroom bounded by reviewed model capacity",
			})
		}
	}
	if nativeReasoningOverridden {
		adjustments = append(adjustments, core.AIRequestAdjustment{
			Source: "portable", Rule: "generation-precedence", Path: "/reasoning/effort",
			Action: "set", Reason: "portable reasoning effort overrides native effort",
		})
	}
	omits := []struct {
		mode core.AIParameterMode
		path string
	}{
		{request.Generation.Temperature.Mode, "/temperature"},
		{request.Generation.TopP.Mode, "/top_p"},
		{request.Generation.TopK.Mode, "/top_k"},
		{request.Generation.MaxTokens.Mode, tokenLimitPath(profile.TokenLimit)},
		{request.Generation.ReasoningEffort.Mode, reasoningEffortPath(profile.ReasoningEffort)},
		{request.Generation.ResponseFormat.Mode, "/response_format"},
	}
	if request.Generation.SystemPrompt.Mode == core.AIParameterOmit && legacySystemPrompt != "" {
		adjustments = append(adjustments, portableOmitAdjustment("/messages"))
	}
	for _, omit := range omits {
		if omit.mode != core.AIParameterOmit {
			continue
		}
		if omit.path == "" {
			continue
		}
		if removeKeyFold(body, strings.TrimPrefix(omit.path, "/")) {
			adjustments = append(adjustments, portableOmitAdjustment(omit.path))
		}
	}

	operation := "generate"
	if stream {
		operation = "stream"
	}
	eligible, headerConflicts := eligibleHeaders(options.Headers, stream)
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Surface:        "chat-completions",
			Operation:      operation,
			Purpose:        request.Purpose,
			RequestedModel: requestedModel(request, options),
			ResolvedModel:  profile.SemanticModel,
		},
		Body:                 body,
		Headers:              eligible,
		ProtectedPaths:       protectedPaths(),
		ProtectedHeaders:     protectedHeaders(stream),
		CaseInsensitivePaths: caseInsensitivePaths(),
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI wire draft: %w", err)
	}
	draft := &Draft{
		Document:        document,
		surfaceVersion:  c.surfaceVersion,
		profileIdentity: profileFingerprintIdentity(c.surfaceVersion, profile) + fmt.Sprintf(":completion-v2:fallback=%t", c.reasoningContentFallback),
		semanticModel:   profile.SemanticModel,
		wireModel:       profile.WireModel,
		modelField:      profile.ModelField,
		tokenLimit:      profile.TokenLimit,
		reasoningStyle:  profile.ReasoningEffort,
		sampling:        profile.Sampling,
		stream:          stream,
		explicit:        explicit,
		adjustments:     adjustments,
		headerConflicts: headerConflicts,
	}
	if profile.TokenBudget == TokenBudgetScaleForReasoning && reasoningActive {
		// This is a policy identity, not a hash of the prompt or token count.
		// Include it even when MaxTokens is absent so cache preflight and the
		// later invocation agree. Explicit reasoning omission stays exact.
		draft.profileIdentity += fmt.Sprintf(":reasoning-budget-v1:multiplier=%d", c.reasoningTokenMultiplier)
		if profile.ReasoningHeadroomLimit > 0 {
			draft.profileIdentity += fmt.Sprintf(":reasoning-headroom-ceiling-v1=%d", profile.ReasoningHeadroomLimit)
		}
		if modelDefaultReasoning {
			draft.profileIdentity += ":model-default-reasoning-v1"
		}
	}
	if request.Generation.ReasoningEffort.Mode == core.AIParameterOmit {
		// Invalidate cached output prepared under the old omission semantics
		// with an additional marker specific to explicit omission.
		draft.profileIdentity += ":reasoning-omission-v1"
	}
	if err := draft.Validate(); err != nil {
		return nil, fmt.Errorf("validate OpenAI wire draft: %w", err)
	}
	return draft, nil
}

func portableResponseFormatType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "json":
		return "json_object", nil
	default:
		return "", errors.New("unsupported portable response format")
	}
}

func (c *codec) Encode(draft *Draft) ([]byte, error) {
	if draft == nil {
		return nil, errors.New("OpenAI wire draft is nil")
	}
	if draft.surfaceVersion != c.surfaceVersion {
		return nil, errors.New("OpenAI wire draft belongs to a different codec surface")
	}
	if err := draft.Validate(); err != nil {
		return nil, fmt.Errorf("validate OpenAI wire draft: %w", err)
	}
	encoded, err := json.Marshal(draft.Body())
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI wire request: %w", err)
	}
	return encoded, nil
}

// Keep the diagnostic stable while exposing reader and JSON causes to callers.
type responseDecodeError struct {
	cause     error
	readError error
}

func (*responseDecodeError) Error() string   { return "decode OpenAI wire response" }
func (e *responseDecodeError) Unwrap() error { return e.cause }

// ResponseBodyReadError exposes an observed non-EOF reader failure. Nil means
// the decoder failed without such evidence, including incomplete JSON at clean EOF.
func (e *responseDecodeError) ResponseBodyReadError() error { return e.readError }

type responseDecodeReader struct {
	reader    io.Reader
	readError error
}

func (r *responseDecodeReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if r.readError == nil && err != nil && err != io.EOF {
		r.readError = err
	}
	return n, err
}

func (c *codec) Decode(response io.Reader) (*core.AIResult, error) {
	if response == nil {
		return nil, errors.New("OpenAI wire response reader is nil")
	}
	var decoded responseEnvelope
	reader := &responseDecodeReader{reader: response}
	if err := json.NewDecoder(reader).Decode(&decoded); err != nil {
		if reader.readError != nil && !errors.Is(err, reader.readError) {
			err = errors.Join(err, reader.readError)
		}
		return nil, &responseDecodeError{cause: err, readError: reader.readError}
	}
	if decoded.Error != nil {
		return nil, endpointError(decoded.Error)
	}
	if len(decoded.Choices) == 0 {
		return nil, errors.New("no response from OpenAI-compatible endpoint")
	}
	choice := decoded.Choices[0]
	content := choice.Message.Content
	source := core.AIContentSourceFinal
	if content == "" && c.reasoningContentFallback && choice.Message.ReasoningContent != "" {
		content = choice.Message.ReasoningContent
		source = core.AIContentSourceReasoningFallback
	}
	result := resultFor(content, decoded.Model, decoded.Usage)
	result.Response.ReasoningContent = choice.Message.ReasoningContent
	result.Response.FinishReason = choice.FinishReason
	result.Response.ContentSource = source
	if choice.Error != nil || strings.EqualFold(strings.TrimSpace(choice.FinishReason), "error") {
		return result, endpointErrorOrUnmapped(choice.Error)
	}
	return result, nil
}

func (c *codec) DecodeStream(response io.Reader, callback core.StreamCallback) (*core.AIResult, error) {
	if response == nil {
		return nil, errors.New("OpenAI wire stream reader is nil")
	}
	if callback == nil {
		return nil, errors.New("OpenAI wire stream callback is nil")
	}
	parser := newSSEParser(response, c.maxSSEEventBytes)
	var content strings.Builder
	var reasoning strings.Builder
	var finalSeen, fallbackSeen bool
	var model string
	var usage usage
	var finishReason string
	chunkIndex := 0
	terminal := false
	// Any delivered channel prevents replay after a stream failure, including
	// reasoning-only output. Completion metadata alone does not invent content.
	emitted := false
	completion := func() *core.AIResult {
		result := resultFor(content.String(), model, usage)
		result.Response.ReasoningContent = reasoning.String()
		result.Response.FinishReason = finishReason
		result.Response.ContentSource = core.AIContentSourceFinal
		if fallbackSeen {
			result.Response.ContentSource = core.AIContentSourceReasoningFallback
			if finalSeen {
				result.Response.ContentSource = core.AIContentSourceMixed
			}
		}
		return result
	}
	for {
		event, err := parser.Next()
		if errors.Is(err, io.EOF) {
			if terminal {
				break
			}
			return streamDecodeFailure(completion(), emitted,
				errors.New("OpenAI wire stream ended before a terminal event"))
		}
		if err != nil {
			return streamDecodeFailure(completion(), emitted, err)
		}
		if event.Done {
			break
		}
		var chunk streamEnvelope
		if err := json.Unmarshal(event.Data, &chunk); err != nil {
			return streamDecodeFailure(completion(), emitted,
				errors.New("decode OpenAI wire stream event"))
		}
		if chunk.Error != nil {
			return streamDecodeFailure(completion(), emitted,
				endpointError(chunk.Error))
		}
		if model == "" && chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			delta := choice.Delta.Content
			reasoningDelta := choice.Delta.ReasoningContent
			reasoning.WriteString(reasoningDelta)
			source := core.AIContentSourceFinal
			if delta != "" {
				finalSeen = true
			} else if c.reasoningContentFallback && reasoningDelta != "" {
				delta = choice.Delta.ReasoningContent
				source = core.AIContentSourceReasoningFallback
				fallbackSeen = true
			}
			if delta != "" || reasoningDelta != "" {
				content.WriteString(delta)
				emitted = true
				if callback(core.StreamChunk{Content: delta, ReasoningContent: reasoningDelta, ContentSource: source, Delta: true, Index: chunkIndex, Model: model}) != nil {
					return completion(), nil
				}
				chunkIndex++
			}
			if strings.EqualFold(strings.TrimSpace(choice.FinishReason), "error") || choice.Error != nil {
				finishReason = choice.FinishReason
				return streamDecodeFailure(completion(), emitted,
					endpointErrorOrUnmapped(choice.Error))
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
				terminal = true
			}
		}
	}
	if finishReason != "" {
		normalized := normalizeUsage(usage)
		_ = callback(core.StreamChunk{
			Delta:         false,
			Index:         chunkIndex,
			FinishReason:  finishReason,
			ContentSource: completion().Response.ContentSource,
			Model:         model,
			Usage:         &normalized,
		})
	}
	return completion(), nil
}

func streamDecodeFailure(result *core.AIResult, emitted bool, err error) (*core.AIResult, error) {
	if !emitted {
		return nil, err
	}
	return result, errors.Join(core.ErrStreamPartiallyCompleted, err)
}

// IsReasoningModel reports whether model uses the OpenAI reasoning parameter
// contract.
func IsReasoningModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if openaimodel.IsGPT6(normalized) {
		return true
	}
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// BuildRequestBody is retained as a small public bridge for providers that
// adopted the earlier OpenAI reasoning helper before using Codec directly.
// Its map-only API saturates overflowing multiplication at math.MaxInt; that
// is not a model capacity guarantee. Use Codec for error-returning validation.
func BuildRequestBody(
	model string,
	messages []map[string]string,
	maxTokens int,
	temperature float32,
	stream bool,
	reasoningTokenMultiplier int,
	reasoningEffort string,
) map[string]interface{} {
	if reasoningTokenMultiplier <= 0 {
		reasoningTokenMultiplier = DefaultReasoningTokenMultiplier
	}
	return buildBody(model, messages, maxTokens, temperature, stream, reasoningTokenMultiplier, reasoningEffort, false)
}

func buildBody(
	model string,
	messages []map[string]string,
	maxTokens int,
	temperature float32,
	stream bool,
	reasoningTokenMultiplier int,
	reasoningEffort string,
	forceReasoningObject bool,
) map[string]interface{} {
	body := map[string]interface{}{"model": model, "messages": messages}
	if IsReasoningModel(model) {
		if reasoningEffort == "none" {
			if maxTokens > 0 {
				body["max_completion_tokens"] = maxTokens
			}
			body["temperature"] = temperature
		} else {
			if maxTokens > 0 {
				budget := maxTokens
				if reasoningEffort != "" {
					budget = saturatedTokenProduct(budget, reasoningTokenMultiplier)
				}
				body["max_completion_tokens"] = budget
			}
		}
		if reasoningEffort != "" {
			body["reasoning"] = map[string]interface{}{"effort": reasoningEffort}
		}
	} else {
		if maxTokens > 0 {
			body["max_tokens"] = maxTokens
		}
		body["temperature"] = temperature
		if forceReasoningObject && reasoningEffort != "" {
			body["reasoning"] = map[string]interface{}{"effort": reasoningEffort}
		}
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	return body
}

func buildProfiledBody(
	profile RequestProfile,
	messages []map[string]string,
	maxTokens int,
	temperature float32,
	stream bool,
	reasoningTokenMultiplier int,
	reasoningEffort string,
	reasoningActive bool,
) map[string]interface{} {
	body := map[string]interface{}{"messages": messages}
	if profile.ModelField == ModelFieldRequired {
		body["model"] = profile.WireModel
	}
	if maxTokens > 0 {
		switch profile.TokenLimit {
		case TokenLimitMaxTokens:
			body["max_tokens"] = maxTokens
		case TokenLimitMaxCompletionTokens:
			budget := maxTokens
			if profile.TokenBudget == TokenBudgetScaleForReasoning &&
				reasoningActive {
				if headroomExceedsLimit(budget, reasoningTokenMultiplier, profile.ReasoningHeadroomLimit) {
					budget = profile.ReasoningHeadroomLimit
				} else {
					// The draft path rejects overflow when no reviewed limit bounds it.
					budget = saturatedTokenProduct(budget, reasoningTokenMultiplier)
				}
			}
			body["max_completion_tokens"] = budget
		}
	}
	if profile.Sampling == SamplingOrdinary || reasoningEffort == "none" {
		body["temperature"] = temperature
	}
	if reasoningEffort != "" {
		applyReasoningEffort(body, profile.ReasoningEffort, reasoningEffort)
	}
	if stream {
		body["stream"] = true
		body["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	return body
}

// Only added headroom is bounded here. Over-limit caller budgets remain visible
// to the adapter's capacity policy, which owns Compatible/Strict behavior.
// Division avoids overflow even for a very large configured multiplier.
func headroomExceedsLimit(budget, multiplier, limit int) bool {
	return budget > 0 && limit > 0 && budget <= limit && multiplier > limit/budget
}

// Both map-building paths supply positive budgets and resolved multipliers.
func saturatedTokenProduct(budget, multiplier int) int {
	if budget > math.MaxInt/multiplier {
		return math.MaxInt
	}
	return budget * multiplier
}

func applyReasoningEffort(body map[string]interface{}, style ReasoningEffortStyle, effort string) {
	removeKeyFold(body, "reasoning_effort")
	switch style {
	case ReasoningEffortTopLevel:
		removeKeyFold(body, "reasoning")
		body["reasoning_effort"] = effort
	case ReasoningEffortNestedObject:
		reasoning := foldedStringMap(body, "reasoning")
		removeKeyFold(reasoning, "effort")
		reasoning["effort"] = effort
		removeKeyFold(body, "reasoning")
		body["reasoning"] = reasoning
	}
}

// mergeNativeReasoningSiblings preserves provider-native controls such as
// OpenRouter's reasoning.exclude while giving portable reasoning_effort
// deterministic precedence over a duplicate native effort value.
func mergeNativeReasoningSiblings(
	body map[string]interface{},
	extra map[string]interface{},
	effort string,
) bool {
	reasoning := foldedStringMap(body, "reasoning")
	overridden := false
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if strings.EqualFold(key, "reasoning") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		native, ok := stringMap(extra[key])
		if !ok {
			overridden = true
			continue
		}
		nativeKeys := make([]string, 0, len(native))
		for nativeKey := range native {
			nativeKeys = append(nativeKeys, nativeKey)
		}
		sort.Strings(nativeKeys)
		for _, nativeKey := range nativeKeys {
			if strings.EqualFold(nativeKey, "effort") {
				nativeEffort, sameType := native[nativeKey].(string)
				if !sameType || nativeEffort != effort {
					overridden = true
				}
				continue
			}
			if !hasKeyFold(reasoning, nativeKey) {
				reasoning[nativeKey] = native[nativeKey]
			}
		}
	}
	removeKeyFold(reasoning, "effort")
	reasoning["effort"] = effort
	removeKeyFold(body, "reasoning")
	body["reasoning"] = reasoning
	return overridden
}

func foldedStringMap(body map[string]interface{}, key string) map[string]interface{} {
	for existing, value := range body {
		if strings.EqualFold(existing, key) {
			if mapped, ok := stringMap(value); ok {
				return mapped
			}
		}
	}
	return make(map[string]interface{})
}

func stringMap(value interface{}) (map[string]interface{}, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Map || reflected.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	clone := make(map[string]interface{}, reflected.Len())
	iterator := reflected.MapRange()
	for iterator.Next() {
		clone[fmt.Sprint(iterator.Key().Interface())] = iterator.Value().Interface()
	}
	return clone, true
}

func tokenLimitPath(field TokenLimitField) string {
	if field == TokenLimitMaxCompletionTokens {
		return "/max_completion_tokens"
	}
	return "/max_tokens"
}

func reasoningEffortPath(style ReasoningEffortStyle) string {
	switch style {
	case ReasoningEffortTopLevel:
		return "/reasoning_effort"
	case ReasoningEffortNestedObject:
		return "/reasoning"
	default:
		return ""
	}
}

func profileReasoningEffort(body map[string]interface{}, style ReasoningEffortStyle) string {
	switch style {
	case ReasoningEffortTopLevel:
		effort, _ := body["reasoning_effort"].(string)
		return effort
	case ReasoningEffortNestedObject:
		return reasoningEffort(body)
	default:
		return ""
	}
}

func resolveParameter[T any](name string, parameter core.AIParameter[T]) (T, bool, error) {
	switch parameter.Mode {
	case core.AIParameterInherit, core.AIParameterOmit:
		var zero T
		return zero, false, nil
	case core.AIParameterSet:
		return parameter.Value, true, nil
	default:
		var zero T
		return zero, false, fmt.Errorf("invalid generation.%s mode %d", name, parameter.Mode)
	}
}

func requestedModel(request *core.AIRequest, options *core.AIOptions) string {
	if request.Generation.Model != "" {
		return request.Generation.Model
	}
	return options.Model
}

func protectedPaths() []string {
	return []string{"/model", "/messages", "/stream", "/stream_options"}
}

func protectedHeaders(stream bool) []string {
	headers := []string{"Authorization", "Content-Type"}
	if stream {
		headers = append(headers, "Accept")
	}
	return headers
}

func caseInsensitivePaths() []string {
	return []string{
		"/temperature", "/top_p", "/top_k", "/max_tokens",
		"/max_completion_tokens", "/reasoning", "/response_format",
	}
}

func eligibleHeaders(source map[string]string, stream bool) (map[string]string, []string) {
	protected := make(map[string]struct{})
	for _, name := range protectedHeaders(stream) {
		protected[strings.ToLower(name)] = struct{}{}
	}
	headers := make(map[string]string, len(source))
	conflictSet := make(map[string]struct{})
	keys := make([]string, 0, len(source))
	for name := range source {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if _, blocked := protected[strings.ToLower(name)]; blocked {
			conflictSet[http.CanonicalHeaderKey(name)] = struct{}{}
			continue
		}
		headers[name] = source[name]
	}
	conflicts := make([]string, 0, len(conflictSet))
	for name := range conflictSet {
		conflicts = append(conflicts, name)
	}
	sort.Strings(conflicts)
	return headers, conflicts
}

func portableOmitAdjustment(path string) core.AIRequestAdjustment {
	return core.AIRequestAdjustment{
		Source: "portable", Rule: "generation-omit", Path: path,
		Action: "remove", Reason: "explicit portable omit",
	}
}

func removeKeyFold(body map[string]interface{}, key string) bool {
	removed := false
	for existing := range body {
		if strings.EqualFold(existing, key) {
			delete(body, existing)
			removed = true
		}
	}
	return removed
}

func hasKeyFold(body map[string]interface{}, key string) bool {
	for existing := range body {
		if strings.EqualFold(existing, key) {
			return true
		}
	}
	return false
}

func protectedStructuralKey(key string) bool {
	switch strings.ToLower(key) {
	case "model", "messages", "stream", "stream_options":
		return true
	default:
		return false
	}
}

func reasoningEffort(body map[string]interface{}) string {
	value, exists := body["reasoning"]
	if !exists {
		return ""
	}
	switch reasoning := value.(type) {
	case map[string]interface{}:
		effort, _ := reasoning["effort"].(string)
		return effort
	case map[string]string:
		return reasoning["effort"]
	default:
		return ""
	}
}

func cloneExtraTopLevel(source map[string]interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func hasMessages(value interface{}) bool {
	switch messages := value.(type) {
	case []map[string]string:
		return len(messages) > 0
	case []interface{}:
		return len(messages) > 0
	default:
		return false
	}
}

func includesUsage(value interface{}) bool {
	switch options := value.(type) {
	case map[string]interface{}:
		return options["include_usage"] == true
	case map[string]bool:
		return options["include_usage"]
	default:
		return false
	}
}

func validatePositiveInteger(path string, value interface{}) error {
	number, valid := wirevalue.Int(value)
	if !valid || number <= 0 {
		return fmt.Errorf("%s must be a positive integer", strings.TrimPrefix(path, "/"))
	}
	return nil
}

type responseEnvelope struct {
	Model   string             `json:"model"`
	Choices []responseChoice   `json:"choices"`
	Usage   usage              `json:"usage"`
	Error   *wireErrorEnvelope `json:"error,omitempty"`
}

type responseChoice struct {
	Message struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content,omitempty"`
	} `json:"message"`
	FinishReason string             `json:"finish_reason"`
	Error        *wireErrorEnvelope `json:"error,omitempty"`
}

type streamEnvelope struct {
	Model   string             `json:"model"`
	Choices []streamChoice     `json:"choices"`
	Usage   *usage             `json:"usage,omitempty"`
	Error   *wireErrorEnvelope `json:"error,omitempty"`
}

type streamChoice struct {
	Delta struct {
		Content          string `json:"content,omitempty"`
		ReasoningContent string `json:"reasoning_content,omitempty"`
	} `json:"delta"`
	FinishReason string             `json:"finish_reason,omitempty"`
	Error        *wireErrorEnvelope `json:"error,omitempty"`
}

type wireErrorMetadata struct {
	ErrorType string `json:"error_type"`
}

type wireErrorEnvelope struct {
	Code     int                `json:"code"`
	Metadata *wireErrorMetadata `json:"metadata,omitempty"`
}

// EndpointError is the provider-neutral, bounded reduction of an in-band
// OpenAI-compatible endpoint error.
type EndpointError struct {
	Code int
	Type string
}

// Error deliberately excludes all network-controlled error text.
func (*EndpointError) Error() string {
	return "OpenAI-compatible endpoint returned an in-band error"
}

func endpointError(wire *wireErrorEnvelope) *EndpointError {
	if wire == nil {
		return &EndpointError{Type: "unmapped"}
	}
	errorType := ""
	if wire.Metadata != nil {
		errorType = wire.Metadata.ErrorType
	}
	return &EndpointError{Code: wire.Code, Type: normalizeWireErrorType(errorType)}
}

func endpointErrorOrUnmapped(wire *wireErrorEnvelope) *EndpointError {
	return endpointError(wire)
}

func normalizeWireErrorType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > maxWireErrorTypeBytes {
		return "unmapped"
	}
	for _, char := range value {
		if char != '_' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return "unmapped"
		}
	}
	return value
}

type usage struct {
	PromptTokens            int                    `json:"prompt_tokens"`
	CompletionTokens        int                    `json:"completion_tokens"`
	TotalTokens             int                    `json:"total_tokens"`
	PromptTokensDetails     promptTokenDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails completionTokenDetails `json:"completion_tokens_details"`
}

type promptTokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
	AudioTokens  int `json:"audio_tokens"`
}

type completionTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	AudioTokens     int `json:"audio_tokens"`
}

func resultFor(content, model string, wireUsage usage) *core.AIResult {
	responseUsage := normalizeUsage(wireUsage)
	details := &core.AIUsageDetails{
		CachedInputTokens: int64(wireUsage.PromptTokensDetails.CachedTokens),
		ReasoningTokens:   int64(wireUsage.CompletionTokensDetails.ReasoningTokens),
		AudioInputTokens:  int64(wireUsage.PromptTokensDetails.AudioTokens),
		AudioOutputTokens: int64(wireUsage.CompletionTokensDetails.AudioTokens),
	}
	if details.CachedInputTokens == 0 && details.ReasoningTokens == 0 &&
		details.AudioInputTokens == 0 && details.AudioOutputTokens == 0 {
		details = nil
	}
	return &core.AIResult{
		Response:     &core.AIResponse{Content: content, Model: model, Usage: responseUsage},
		UsageDetails: details,
	}
}

func normalizeUsage(wireUsage usage) core.TokenUsage {
	return core.TokenUsage{
		PromptTokens:     wireUsage.PromptTokens,
		CompletionTokens: wireUsage.CompletionTokens,
		TotalTokens:      wireUsage.TotalTokens,
	}
}
