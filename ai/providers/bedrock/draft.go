//go:build bedrock
// +build bedrock

package bedrock

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const bedrockConverseAdapterVersion = "bedrock-converse-v1"

// Draft is a logical, policy-editable Bedrock Converse request. It translates
// directly into AWS SDK inputs and never pretends the SDK operation is an HTTP
// JSON surface.
type Draft struct {
	*requestpolicy.Document
	resolvedModel string
	stream        bool
	explicit      map[string]struct{}
	adjustments   []core.AIRequestAdjustment
}

// NewDraft builds an isolated logical Bedrock Converse request. The caller
// should apply request defaults and resolve the model before invoking it.
func NewDraft(resolvedModel string, request *core.AIRequest) (*Draft, error) {
	return newDraft(resolvedModel, request, false)
}

// NewStreamDraft builds the streaming form of the same logical Converse
// request so policy semantics remain identical across both SDK operations.
func NewStreamDraft(resolvedModel string, request *core.AIRequest) (*Draft, error) {
	return newDraft(resolvedModel, request, true)
}

func newDraft(resolvedModel string, request *core.AIRequest, stream bool) (*Draft, error) {
	if request == nil {
		return nil, errors.New("bedrock AI request is nil")
	}
	if strings.TrimSpace(resolvedModel) == "" {
		return nil, errors.New("bedrock resolved model is empty")
	}
	request, err := core.CloneAIRequest(request)
	if err != nil {
		return nil, fmt.Errorf("clone Bedrock AI request: %w", err)
	}
	options := request.LegacyOptions()
	if options == nil {
		options = &core.AIOptions{}
	}

	inference := make(map[string]interface{})
	if options.MaxTokens > 0 {
		inference["max_tokens"] = options.MaxTokens
	}
	if options.Temperature > 0 {
		inference["temperature"] = options.Temperature
	}
	body := map[string]interface{}{
		"model": resolvedModel,
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
	if len(options.Extra) > 0 {
		body["additional_model_request_fields"] = options.Extra
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
			ResolvedModel:  resolvedModel,
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
		resolvedModel: resolvedModel,
		stream:        stream,
		explicit:      explicit,
	}
	if err := draft.applyPortableOmits(request.Generation); err != nil {
		return nil, err
	}
	if err := draft.Validate(); err != nil {
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
func (d *Draft) PolicyFingerprintIdentity() string { return bedrockConverseAdapterVersion }

// Adjustments returns portable preparation adjustments made before policy.
func (d *Draft) Adjustments() []core.AIRequestAdjustment {
	if d == nil {
		return nil
	}
	return append([]core.AIRequestAdjustment(nil), d.adjustments...)
}

// Validate checks logical and SDK translation invariants after policy.
func (d *Draft) Validate() error {
	if d == nil || d.Document == nil {
		return errors.New("bedrock request draft is nil")
	}
	model, ok := d.Get("/model")
	if !ok || model != d.resolvedModel {
		return errors.New("resolved model invariant was not preserved")
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
		if _, ok := system.(string); !ok {
			return fmt.Errorf("system has unsupported type %T", system)
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
				if _, err := stringSlice(parameter); err != nil {
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
	common, err := d.sdkFields()
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseInput{
		ModelId:                      aws.String(d.resolvedModel),
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
	common, err := d.sdkFields()
	if err != nil {
		return nil, err
	}
	return &bedrockruntime.ConverseStreamInput{
		ModelId:                      aws.String(d.resolvedModel),
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
		fields.additional = bedrockdocument.NewLazyDocument(value)
	}
	return fields, nil
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
	switch number := value.(type) {
	case int:
		converted = int64(number)
	case int32:
		converted = int64(number)
	case int64:
		converted = number
	case float64:
		if number != math.Trunc(number) {
			return 0, errors.New("must be an integer")
		}
		converted = int64(number)
	default:
		return 0, fmt.Errorf("has unsupported type %T", value)
	}
	if converted <= 0 || converted > math.MaxInt32 {
		return 0, errors.New("must be a positive 32-bit integer")
	}
	return int32(converted), nil
}

func finiteFloat32(value interface{}) (float32, error) {
	var converted float64
	switch number := value.(type) {
	case float32:
		converted = float64(number)
	case float64:
		converted = number
	case int:
		converted = float64(number)
	case int32:
		converted = float64(number)
	case int64:
		converted = float64(number)
	default:
		return 0, fmt.Errorf("has unsupported type %T", value)
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
