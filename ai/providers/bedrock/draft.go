//go:build bedrock
// +build bedrock

package bedrock

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithydocument "github.com/aws/smithy-go/document"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const (
	bedrockConverseAdapterVersion = "bedrock-converse-v5"
	maxStopSequences              = 2500
)

// Draft is a logical, policy-editable Bedrock Converse request. It translates
// directly into AWS SDK inputs and never pretends the SDK operation is an HTTP
// JSON surface.
type Draft struct {
	*requestpolicy.Document
	semanticModel string
	wireModel     string
	routeIdentity string
	stream        bool
	explicit      map[string]struct{}
	adjustments   []core.AIRequestAdjustment
}

// NewDraft builds an isolated logical Bedrock Converse request. The caller
// should apply request defaults and resolve the model before invoking it.
func NewDraft(resolvedModel string, request *core.AIRequest) (*Draft, error) {
	return newDraft(requestProfile{
		semanticModel: resolvedModel,
		wireModel:     resolvedModel,
		routeIdentity: defaultRouteIdentity,
	}, request, false)
}

// NewStreamDraft builds the streaming form of the same logical Converse
// request so policy semantics remain identical across both SDK operations.
func NewStreamDraft(resolvedModel string, request *core.AIRequest) (*Draft, error) {
	return newDraft(requestProfile{
		semanticModel: resolvedModel,
		wireModel:     resolvedModel,
		routeIdentity: defaultRouteIdentity,
	}, request, true)
}

func newRoutedDraft(profile requestProfile, request *core.AIRequest, stream bool) (*Draft, error) {
	return newDraft(profile, request, stream)
}

func newDraft(profile requestProfile, request *core.AIRequest, stream bool) (*Draft, error) {
	if request == nil {
		return nil, errors.New("bedrock AI request is nil")
	}
	if strings.TrimSpace(profile.semanticModel) == "" {
		return nil, errors.New("bedrock semantic model is empty")
	}
	if err := validateWireModel(profile.wireModel); err != nil {
		return nil, err
	}
	if err := validateBedrockRouteIdentity(profile.routeIdentity); err != nil {
		return nil, err
	}
	request, err := core.CloneAIRequest(request)
	if err != nil {
		return nil, fmt.Errorf("clone Bedrock AI request: %w", err)
	}
	options := request.LegacyOptions()
	if options == nil {
		options = &core.AIOptions{}
	}

	samplingPolicy := bedrockSamplingPolicyForModel(profile.semanticModel)
	preparationAdjustments := make([]core.AIRequestAdjustment, 0, 1)
	inference := make(map[string]interface{})
	if options.MaxTokens > 0 {
		inference["max_tokens"] = options.MaxTokens
	}
	if options.Temperature > 0 {
		if samplingPolicy == bedrockSamplingFable5 &&
			request.Generation.Temperature.Mode == core.AIParameterInherit &&
			options.Temperature != bedrockFableTemperature {
			preparationAdjustments = append(preparationAdjustments, core.AIRequestAdjustment{
				Source: "built-in-rule",
				Rule: bedrockFableTemperaturePreparationRule + "@" +
					bedrockFableTemperatureRuleVersion,
				Path:   "/inference_config/temperature",
				Action: "remove",
				Reason: "Bedrock Claude Fable 5 accepts only temperature 1 or omission",
			})
		} else {
			inference["temperature"] = options.Temperature
		}
	}
	additional, err := prepareBedrockAdditionalFields(
		options.Extra,
		samplingPolicy,
	)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"model": profile.wireModel,
		"messages": []map[string]string{{
			"role": "user", "content": request.Prompt,
		}},
	}
	if options.SystemPrompt != "" {
		body["system"] = options.SystemPrompt
	}
	if len(inference) > 0 {
		body["inference_config"] = inference
	}
	if len(additional) > 0 {
		body["additional_model_request_fields"] = additional
	}

	explicit := make(map[string]struct{})
	if err := applyPortableSet(body, "/inference_config/temperature", "temperature", request.Generation.Temperature, explicit); err != nil {
		return nil, err
	}
	if err := applyPortableSet(body, "/inference_config/top_p", "top_p", request.Generation.TopP, explicit); err != nil {
		return nil, err
	}
	if request.Generation.TopK.Mode == core.AIParameterSet {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Draft", Feature: "generation.top_k"}
	}
	if request.Generation.TopK.Mode != core.AIParameterInherit && request.Generation.TopK.Mode != core.AIParameterOmit {
		return nil, fmt.Errorf("invalid generation.top_k mode %d", request.Generation.TopK.Mode)
	}
	if err := applyPortableSet(body, "/inference_config/max_tokens", "max_tokens", request.Generation.MaxTokens, explicit); err != nil {
		return nil, err
	}
	if err := applyTopLevelPortableSet(body, "/system", "system", request.Generation.SystemPrompt, explicit); err != nil {
		return nil, err
	}
	if request.Generation.ReasoningEffort.Mode == core.AIParameterSet {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Draft", Feature: "generation.reasoning_effort"}
	}
	if request.Generation.ReasoningEffort.Mode != core.AIParameterInherit && request.Generation.ReasoningEffort.Mode != core.AIParameterOmit {
		return nil, fmt.Errorf("invalid generation.reasoning_effort mode %d", request.Generation.ReasoningEffort.Mode)
	}
	if request.Generation.ResponseFormat.Mode == core.AIParameterSet {
		return nil, &core.AIRequestFeatureError{ClientType: "*bedrock.Draft", Feature: "generation.response_format"}
	}
	if request.Generation.ResponseFormat.Mode != core.AIParameterInherit && request.Generation.ResponseFormat.Mode != core.AIParameterOmit {
		return nil, fmt.Errorf("invalid generation.response_format mode %d", request.Generation.ResponseFormat.Mode)
	}

	operation := "generate"
	if stream {
		operation = "stream"
	}
	document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
		Info: requestpolicy.RequestInfo{
			Provider:       "bedrock",
			ProviderAlias:  "bedrock",
			Surface:        "converse",
			Operation:      operation,
			Purpose:        request.Purpose,
			RequestedModel: requestedModel(request, options),
			ResolvedModel:  profile.semanticModel,
		},
		Body:             body,
		ProtectedPaths:   []string{"/model", "/messages"},
		ProtectedHeaders: []string{"*"},
	})
	if err != nil {
		return nil, fmt.Errorf("create Bedrock request draft: %w", err)
	}
	draft := &Draft{
		Document:      document,
		semanticModel: profile.semanticModel,
		wireModel:     profile.wireModel,
		routeIdentity: profile.routeIdentity,
		stream:        stream,
		explicit:      explicit,
		adjustments:   preparationAdjustments,
	}
	if err := draft.applyPortableOmits(request.Generation); err != nil {
		return nil, err
	}
	if err := draft.validate(false); err != nil {
		return nil, fmt.Errorf("validate Bedrock request draft: %w", err)
	}
	return draft, nil
}

// SetHeader rejects HTTP header policy on the SDK-native surface.
func (d *Draft) SetHeader(name, _ string) error {
	return fmt.Errorf("header %q is unsupported by the SDK-native Bedrock Converse surface", name)
}

// RemoveHeader rejects HTTP header policy on the SDK-native surface.
func (d *Draft) RemoveHeader(name string) error {
	return fmt.Errorf("header %q is unsupported by the SDK-native Bedrock Converse surface", name)
}

// Header reports no eligible transport headers.
func (d *Draft) Header(string) (string, bool) { return "", false }

// HasExplicitIntent supports strict request-policy compatibility checks.
func (d *Draft) HasExplicitIntent(path string) bool {
	_, ok := d.explicit[path]
	return ok
}

// PolicyFingerprintIdentity versions the logical-to-SDK translation.
func (d *Draft) PolicyFingerprintIdentity() string {
	if d == nil {
		return ""
	}
	return bedrockConverseAdapterVersion + "|route=" + d.routeIdentity
}

// Adjustments returns portable preparation adjustments made before policy.
func (d *Draft) Adjustments() []core.AIRequestAdjustment {
	if d == nil {
		return nil
	}
	return append([]core.AIRequestAdjustment(nil), d.adjustments...)
}

// Validate checks logical and SDK translation invariants after policy.
func (d *Draft) Validate() error {
	return d.validate(true)
}

func (d *Draft) validate(validateModelSampling bool) error {
	if d == nil || d.Document == nil {
		return errors.New("bedrock request draft is nil")
	}
	model, ok := d.Get("/model")
	if !ok || model != d.wireModel {
		return errors.New("wire model invariant was not preserved")
	}
	if messages, ok := d.Get("/messages"); !ok || !hasLogicalMessages(messages) {
		return errors.New("messages input is required")
	}
	allowedTopLevel := map[string]struct{}{
		"model": {}, "messages": {}, "system": {}, "inference_config": {},
		"additional_model_request_fields": {},
	}
	for key := range d.Body() {
		if _, allowed := allowedTopLevel[key]; !allowed {
			return fmt.Errorf("unsupported Bedrock Converse logical field %q", key)
		}
	}
	if system, exists := d.Get("/system"); exists {
		text, ok := system.(string)
		if !ok {
			return fmt.Errorf("system has unsupported type %T", system)
		}
		if text == "" {
			return errors.New("system must be a non-empty string")
		}
	}
	if value, exists := d.Get("/inference_config"); exists {
		config, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("inference_config has unsupported type %T", value)
		}
		for key, parameter := range config {
			switch key {
			case "max_tokens":
				if _, err := positiveInt32(parameter); err != nil {
					return fmt.Errorf("max_tokens: %w", err)
				}
			case "temperature", "top_p":
				if _, err := unitFloat32(parameter); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			case "stop_sequences":
				if err := validateStopSequences(parameter); err != nil {
					return fmt.Errorf("stop_sequences: %w", err)
				}
			default:
				return fmt.Errorf("unsupported Bedrock inference_config field %q", key)
			}
		}
	}
	if value, exists := d.Get("/additional_model_request_fields"); exists {
		if _, ok := value.(map[string]interface{}); !ok {
			return fmt.Errorf("additional_model_request_fields has unsupported type %T", value)
		}
		if err := validateBedrockDocumentValue(value); err != nil {
			return fmt.Errorf("additional_model_request_fields: %w", err)
		}
	}
	if validateModelSampling {
		if err := d.validateModelSampling(); err != nil {
			return err
		}
	}
	return nil
}

// SDKInput translates the logical draft directly to a ConverseInput.
func (d *Draft) SDKInput() (*bedrockruntime.ConverseInput, error) {
	if d.stream {
		return nil, errors.New("streaming Bedrock draft cannot create a non-streaming SDK input")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d.sdkInput()
}

func (d *Draft) sdkInput() (*bedrockruntime.ConverseInput, error) {
	common, err := d.sdkFields()
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseInput{
		ModelId:                      aws.String(d.wireModel),
		Messages:                     common.messages,
		System:                       common.system,
		InferenceConfig:              common.inference,
		AdditionalModelRequestFields: common.additional,
	}, nil
}

// SDKStreamInput translates the same logical draft directly to a
// ConverseStreamInput.
func (d *Draft) SDKStreamInput() (*bedrockruntime.ConverseStreamInput, error) {
	if !d.stream {
		return nil, errors.New("non-streaming Bedrock draft cannot create a streaming SDK input")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d.sdkStreamInput()
}

func (d *Draft) sdkStreamInput() (*bedrockruntime.ConverseStreamInput, error) {
	common, err := d.sdkFields()
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseStreamInput{
		ModelId:                      aws.String(d.wireModel),
		Messages:                     common.messages,
		System:                       common.system,
		InferenceConfig:              common.inference,
		AdditionalModelRequestFields: common.additional,
	}, nil
}

type sdkFields struct {
	messages   []types.Message
	system     []types.SystemContentBlock
	inference  *types.InferenceConfiguration
	additional bedrockdocument.Interface
}

func (d *Draft) sdkFields() (sdkFields, error) {
	fields := sdkFields{}
	messages, _ := d.Get("/messages")
	// /messages is protected by the draft, so this guard can become reachable
	// only if a future Document implementation changes protected-path typing.
	logicalMessages, ok := messages.([]map[string]string)
	if !ok {
		return fields, fmt.Errorf("messages has unsupported type %T", messages)
	}
	fields.messages = make([]types.Message, 0, len(logicalMessages))
	for _, message := range logicalMessages {
		role := types.ConversationRoleUser
		if message["role"] == "assistant" {
			role = types.ConversationRoleAssistant
		}
		fields.messages = append(fields.messages, types.Message{
			Role: role,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: message["content"]},
			},
		})
	}
	if system, exists := d.Get("/system"); exists {
		fields.system = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: system.(string)},
		}
	}
	if value, exists := d.Get("/inference_config"); exists {
		config := value.(map[string]interface{})
		fields.inference = &types.InferenceConfiguration{}
		if parameter, ok := config["max_tokens"]; ok {
			converted, _ := positiveInt32(parameter)
			fields.inference.MaxTokens = aws.Int32(converted)
		}
		if parameter, ok := config["temperature"]; ok {
			converted, _ := unitFloat32(parameter)
			fields.inference.Temperature = aws.Float32(converted)
		}
		if parameter, ok := config["top_p"]; ok {
			converted, _ := unitFloat32(parameter)
			fields.inference.TopP = aws.Float32(converted)
		}
		if parameter, ok := config["stop_sequences"]; ok {
			fields.inference.StopSequences, _ = stringSlice(parameter)
		}
	}
	if value, exists := d.Get("/additional_model_request_fields"); exists {
		normalized, err := normalizeBedrockDocumentValue(value)
		if err != nil {
			return fields, fmt.Errorf("normalize additional_model_request_fields: %w", err)
		}
		fields.additional = bedrockdocument.NewLazyDocument(normalized)
	}
	return fields, nil
}

func (d *Draft) validateModelSampling() error {
	inference, _ := d.Get("/inference_config")
	config, _ := inference.(map[string]interface{})
	sampling := bedrockDraftSamplingFields{}
	if value, exists := config["temperature"]; exists {
		sampling.temperatures = append(sampling.temperatures, value)
	}
	if value, exists := config["top_p"]; exists {
		sampling.topPs = append(sampling.topPs, value)
	}
	if additional, exists := d.Get("/additional_model_request_fields"); exists {
		fields, _ := additional.(map[string]interface{})
		for key, value := range fields {
			field, samplingField := canonicalBedrockSamplingField(key)
			if !samplingField {
				continue
			}
			switch field {
			case "temperature":
				sampling.additionalTemperatures = append(sampling.additionalTemperatures, value)
			case "top_p":
				sampling.additionalTopPs = append(sampling.additionalTopPs, value)
			case "top_k":
				sampling.hasTopK = true
			}
		}
	}

	switch bedrockSamplingPolicyForModel(d.semanticModel) {
	case bedrockSamplingOmitAll:
		if len(sampling.temperatures) > 0 ||
			len(sampling.topPs) > 0 ||
			len(sampling.additionalTemperatures) > 0 ||
			len(sampling.additionalTopPs) > 0 ||
			sampling.hasTopK {
			return errors.New("selected Bedrock Claude model does not accept modified temperature, top_p, or top_k")
		}
	case bedrockSamplingFable5:
		if sampling.hasTopK {
			return errors.New("bedrock Claude Fable 5 does not accept top_k")
		}
		if len(sampling.additionalTemperatures) > 0 || len(sampling.additionalTopPs) > 0 {
			return errors.New(
				"bedrock Claude Fable 5 temperature and top_p must use inference_config, " +
					"not additional_model_request_fields",
			)
		}
		for _, value := range sampling.temperatures {
			temperature, err := unitFloat32(value)
			if err != nil {
				return fmt.Errorf("bedrock Claude Fable 5 temperature: %w", err)
			}
			if temperature != bedrockFableTemperature {
				return errors.New("bedrock Claude Fable 5 temperature must be 1 or omitted")
			}
		}
		for _, value := range sampling.topPs {
			topP, err := unitFloat32(value)
			if err != nil {
				return fmt.Errorf("bedrock Claude Fable 5 top_p: %w", err)
			}
			if topP < bedrockFableTopPMinimum || topP >= bedrockFableTopPMaximum {
				return errors.New("bedrock Claude Fable 5 top_p must be at least 0.99 and less than 1, or omitted")
			}
		}
	}
	return nil
}

type bedrockDraftSamplingFields struct {
	temperatures           []interface{}
	topPs                  []interface{}
	additionalTemperatures []interface{}
	additionalTopPs        []interface{}
	hasTopK                bool
}

func prepareBedrockAdditionalFields(
	fields map[string]interface{},
	policy bedrockSamplingPolicy,
) (map[string]interface{}, error) {
	if len(fields) == 0 || policy == bedrockSamplingUnrestricted {
		return fields, nil
	}
	counts := make(map[string]int, 3)
	for key := range fields {
		if canonical, ok := canonicalBedrockSamplingField(key); ok {
			counts[canonical]++
		}
	}
	for field, count := range counts {
		if count > 1 {
			return nil, fmt.Errorf(
				"additional_model_request_fields contains %d case-insensitive %s fields",
				count,
				field,
			)
		}
	}
	canonical := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		field, ok := canonicalBedrockSamplingField(key)
		if !ok {
			canonical[key] = value
			continue
		}
		canonical[field] = value
	}
	return canonical, nil
}

func canonicalBedrockSamplingField(field string) (string, bool) {
	switch {
	case strings.EqualFold(field, "temperature"):
		return "temperature", true
	case strings.EqualFold(field, "top_p"):
		return "top_p", true
	case strings.EqualFold(field, "top_k"):
		return "top_k", true
	default:
		return "", false
	}
}

type bedrockDocumentVisit struct {
	kind reflect.Kind
	typ  reflect.Type
	ptr  uintptr
	len  int
	cap  int
}

// validateBedrockDocumentValue rejects values that cannot be translated to a
// Bedrock Smithy document. It deliberately runs during final draft validation,
// before a stable policy fingerprint is produced.
func validateBedrockDocumentValue(value interface{}) error {
	_, err := walkBedrockDocumentValue(
		reflect.ValueOf(value),
		"$",
		make(map[bedrockDocumentVisit]struct{}),
		false,
	)
	return err
}

// normalizeBedrockDocumentValue converts JSON decoder numbers to Smithy's
// arbitrary-precision number type before the AWS document encoder sees them.
// It validates and preserves the original decimal representation instead of
// round-tripping integers through a precision-losing float64. The Smithy
// encoder otherwise observes encoding/json.Number's string kind and emits a
// quoted JSON string. Maps and sequences are rebuilt into wire-local JSON
// containers so typed decoder shapes are handled recursively.
func normalizeBedrockDocumentValue(value interface{}) (interface{}, error) {
	return walkBedrockDocumentValue(
		reflect.ValueOf(value),
		"$",
		make(map[bedrockDocumentVisit]struct{}),
		true,
	)
}

func walkBedrockDocumentValue(
	value reflect.Value,
	path string,
	active map[bedrockDocumentVisit]struct{},
	normalize bool,
) (interface{}, error) {
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, nil
		}
		return walkBedrockDocumentValue(value.Elem(), path, active, normalize)
	}
	if value.CanInterface() {
		if number, ok := value.Interface().(json.Number); ok {
			if number == "" {
				return nil, fmt.Errorf("%s: invalid empty JSON number", path)
			}
			encoded, err := json.Marshal(number)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid JSON number %q: %w", path, number, err)
			}
			if !normalize {
				return nil, nil
			}
			return smithydocument.Number(encoded), nil
		}
	}

	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil, nil
		}
		visit := bedrockDocumentVisit{
			kind: reflect.Pointer,
			typ:  value.Type(),
			ptr:  value.Pointer(),
		}
		if _, exists := active[visit]; exists {
			return nil, fmt.Errorf("%s: cyclic pointer value is not document-compatible", path)
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
		return walkBedrockDocumentValue(value.Elem(), path, active, normalize)
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: unsupported document map key type %s", path, value.Type().Key())
		}
		if value.IsNil() {
			return nil, nil
		}
		visit := bedrockDocumentVisit{
			kind: reflect.Map,
			typ:  value.Type(),
			ptr:  value.Pointer(),
		}
		if _, exists := active[visit]; exists {
			return nil, fmt.Errorf("%s: cyclic map value is not document-compatible", path)
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		var normalized map[string]interface{}
		if normalize {
			normalized = make(map[string]interface{}, value.Len())
		}
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			item, err := walkBedrockDocumentValue(
				iterator.Value(),
				path+"/"+key,
				active,
				normalize,
			)
			if err != nil {
				return nil, err
			}
			if normalize {
				normalized[key] = item
			}
		}
		return normalized, nil
	case reflect.Slice:
		if value.IsNil() {
			return nil, nil
		}
		visit := bedrockDocumentVisit{
			kind: reflect.Slice,
			typ:  value.Type(),
			ptr:  value.Pointer(),
			len:  value.Len(),
			cap:  value.Cap(),
		}
		if _, exists := active[visit]; exists {
			return nil, fmt.Errorf("%s: cyclic slice value is not document-compatible", path)
		}
		active[visit] = struct{}{}
		defer delete(active, visit)

		var normalized []interface{}
		if normalize {
			normalized = make([]interface{}, value.Len())
		}
		for index := range value.Len() {
			item, err := walkBedrockDocumentValue(
				value.Index(index),
				fmt.Sprintf("%s/%d", path, index),
				active,
				normalize,
			)
			if err != nil {
				return nil, err
			}
			if normalize {
				normalized[index] = item
			}
		}
		return normalized, nil
	case reflect.Array:
		var normalized []interface{}
		if normalize {
			normalized = make([]interface{}, value.Len())
		}
		for index := range value.Len() {
			item, err := walkBedrockDocumentValue(
				value.Index(index),
				fmt.Sprintf("%s/%d", path, index),
				active,
				normalize,
			)
			if err != nil {
				return nil, err
			}
			if normalize {
				normalized[index] = item
			}
		}
		return normalized, nil
	case reflect.Struct:
		return nil, fmt.Errorf(
			"%s: unsupported document struct type %s; use maps and JSON-compatible values",
			path,
			value.Type(),
		)
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !normalize {
			return nil, nil
		}
		return value.Interface(), nil
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("%s: non-finite floating-point value is not document-compatible", path)
		}
		if !normalize {
			return nil, nil
		}
		return value.Interface(), nil
	default:
		return nil, fmt.Errorf("%s: unsupported document value type %s", path, value.Type())
	}
}

func (d *Draft) applyPortableOmits(generation core.AIGenerationOptions) error {
	omits := []struct {
		mode core.AIParameterMode
		path string
	}{
		{generation.Temperature.Mode, "/inference_config/temperature"},
		{generation.TopP.Mode, "/inference_config/top_p"},
		{generation.MaxTokens.Mode, "/inference_config/max_tokens"},
		{generation.SystemPrompt.Mode, "/system"},
	}
	for _, omit := range omits {
		if omit.mode != core.AIParameterOmit {
			continue
		}
		_, existed := d.Get(omit.path)
		if err := d.Remove(omit.path); err != nil {
			return fmt.Errorf("apply portable omit %s: %w", omit.path, err)
		}
		if existed {
			d.adjustments = append(d.adjustments, portableOmitAdjustment(omit.path))
		}
	}
	if value, exists := d.Get("/inference_config"); exists {
		if config, ok := value.(map[string]interface{}); ok && len(config) == 0 {
			if err := d.Remove("/inference_config"); err != nil {
				return err
			}
		}
	}
	for _, omit := range []struct {
		mode core.AIParameterMode
		key  string
		path string
	}{
		{generation.TopK.Mode, "top_k", "/additional_model_request_fields/top_k"},
		{generation.ReasoningEffort.Mode, "reasoning_effort", "/additional_model_request_fields/reasoning_effort"},
		{generation.ResponseFormat.Mode, "response_format", "/additional_model_request_fields/response_format"},
	} {
		if omit.mode != core.AIParameterOmit {
			continue
		}
		additional, exists := d.Get("/additional_model_request_fields")
		if !exists {
			continue
		}
		fields := additional.(map[string]interface{})
		removed := false
		for key := range fields {
			if strings.EqualFold(key, omit.key) {
				delete(fields, key)
				removed = true
			}
		}
		if removed {
			d.adjustments = append(d.adjustments, portableOmitAdjustment(omit.path))
		}
		if len(fields) == 0 {
			if err := d.Remove("/additional_model_request_fields"); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyPortableSet[T any](
	body map[string]interface{},
	path string,
	key string,
	parameter core.AIParameter[T],
	explicit map[string]struct{},
) error {
	switch parameter.Mode {
	case core.AIParameterInherit, core.AIParameterOmit:
		return nil
	case core.AIParameterSet:
		config, _ := body["inference_config"].(map[string]interface{})
		if config == nil {
			config = make(map[string]interface{})
			body["inference_config"] = config
		}
		config[key] = parameter.Value
		explicit[path] = struct{}{}
		return nil
	default:
		return fmt.Errorf("invalid generation mode %d for %s", parameter.Mode, path)
	}
}

func applyTopLevelPortableSet[T any](
	body map[string]interface{},
	path string,
	key string,
	parameter core.AIParameter[T],
	explicit map[string]struct{},
) error {
	switch parameter.Mode {
	case core.AIParameterInherit, core.AIParameterOmit:
		return nil
	case core.AIParameterSet:
		body[key] = parameter.Value
		explicit[path] = struct{}{}
		return nil
	default:
		return fmt.Errorf("invalid generation mode %d for %s", parameter.Mode, path)
	}
}

func portableOmitAdjustment(path string) core.AIRequestAdjustment {
	return core.AIRequestAdjustment{
		Source: "portable", Rule: "generation-omit", Path: path,
		Action: "remove", Reason: "explicit portable omit",
	}
}

func requestedModel(request *core.AIRequest, options *core.AIOptions) string {
	if request.Generation.Model != "" {
		return request.Generation.Model
	}
	return options.Model
}

func hasLogicalMessages(value interface{}) bool {
	switch messages := value.(type) {
	case []map[string]string:
		return len(messages) > 0
	case []interface{}:
		return len(messages) > 0
	default:
		return false
	}
}

func positiveInt32(value interface{}) (int32, error) {
	var converted int64
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("has invalid JSON number %q: %w", number, err)
		}
		if parsed != math.Trunc(parsed) {
			return 0, errors.New("must be an integer")
		}
		if parsed > math.MaxInt64 || parsed < math.MinInt64 {
			return 0, errors.New("must be a 64-bit integer")
		}
		converted = int64(parsed)
	} else {
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() {
			return 0, errors.New("has unsupported type <nil>")
		}
		switch reflected.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			converted = reflected.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			number := reflected.Uint()
			if number > math.MaxInt64 {
				return 0, errors.New("must be a 64-bit integer")
			}
			converted = int64(number)
		case reflect.Float32, reflect.Float64:
			number := reflected.Float()
			if number != math.Trunc(number) {
				return 0, errors.New("must be an integer")
			}
			if number > math.MaxInt64 || number < math.MinInt64 {
				return 0, errors.New("must be a 64-bit integer")
			}
			converted = int64(number)
		default:
			return 0, fmt.Errorf("has unsupported type %T", value)
		}
	}
	if converted <= 0 || converted > math.MaxInt32 {
		return 0, errors.New("must be a positive 32-bit integer")
	}
	return int32(converted), nil
}

func finiteFloat32(value interface{}) (float32, error) {
	var converted float64
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Float64()
		if err != nil {
			return 0, fmt.Errorf("has invalid JSON number %q: %w", number, err)
		}
		converted = parsed
	} else {
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() {
			return 0, errors.New("has unsupported type <nil>")
		}
		switch reflected.Kind() {
		case reflect.Float32, reflect.Float64:
			converted = reflected.Float()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			converted = float64(reflected.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			converted = float64(reflected.Uint())
		default:
			return 0, fmt.Errorf("has unsupported type %T", value)
		}
	}
	if math.IsNaN(converted) || math.IsInf(converted, 0) || converted > math.MaxFloat32 || converted < -math.MaxFloat32 {
		return 0, errors.New("must be a finite 32-bit float")
	}
	return float32(converted), nil
}

func unitFloat32(value interface{}) (float32, error) {
	converted, err := finiteFloat32(value)
	if err != nil {
		return 0, err
	}
	if converted < 0 || converted > 1 {
		return 0, errors.New("must be between 0 and 1")
	}
	return converted, nil
}

func stringSlice(value interface{}) ([]string, error) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []interface{}:
		result := make([]string, len(values))
		for index, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("item %d has unsupported type %T", index, item)
			}
			result[index] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("has unsupported type %T", value)
	}
}

func validateStopSequences(value interface{}) error {
	sequences, err := stringSlice(value)
	if err != nil {
		return err
	}
	if len(sequences) > maxStopSequences {
		return fmt.Errorf("must contain at most %d items", maxStopSequences)
	}
	for index, sequence := range sequences {
		if sequence == "" {
			return fmt.Errorf("item %d must be non-empty", index)
		}
	}
	return nil
}
