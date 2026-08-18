package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

type geminiDraft struct {
	*requestpolicy.Document
	explicit     map[string]struct{}
	profile      wireProfile
	capabilities modelCapabilities
	stream       bool
}

func (draft *geminiDraft) HasExplicitIntent(path string) bool {
	_, ok := draft.explicit[path]
	return ok
}

func (draft *geminiDraft) PolicyFingerprintIdentity() string {
	return draft.profile.fingerprintIdentity + "\ncapabilities=" + capabilitySnapshotVersion
}

func (draft *geminiDraft) EffectiveGenerationPaths() (string, string) {
	paths := draft.profile.generationPaths()
	return paths.Temperature, paths.MaxTokens
}

// Set rejects known fields from another Gemini surface or a noncanonical
// GenerateContent spelling before the shared policy engine mutates the draft.
// Final validation still permits provider-native escape-hatch fields that are
// not part of the portable contract.
func (draft *geminiDraft) Set(path string, value interface{}) error {
	if err := validateGeminiMutationPath(path); err != nil {
		return err
	}
	return draft.Document.Set(path, value)
}

// Remove performs the same path validation as Set. This matters because the
// shared document deliberately treats removal of a missing path as a no-op;
// without this check, a misspelled or wrong-surface removal could appear to
// have been accepted.
func (draft *geminiDraft) Remove(path string) error {
	if err := validateGeminiMutationPath(path); err != nil {
		return err
	}
	return draft.Document.Remove(path)
}

func validateGeminiMutationPath(path string) error {
	segments := strings.Split(path, "/")
	if len(segments) < 2 || segments[0] != "" {
		// The shared JSON Pointer validator returns the detailed syntax error.
		return nil
	}
	for index := 1; index < len(segments); index++ {
		segments[index] = strings.ReplaceAll(strings.ReplaceAll(segments[index], "~1", "/"), "~0", "~")
	}
	top := segments[1]
	topNormalized := normalizedFieldName(top)
	switch topNormalized {
	case "generationconfig":
		if top != "generationConfig" {
			return fmt.Errorf("gemini request path %q must use canonical GenerateContent casing", path)
		}
	case "systeminstruction":
		if top != "systemInstruction" {
			return fmt.Errorf("gemini request path %q must use canonical GenerateContent casing", path)
		}
	case "temperature", "topp", "topk", "maxoutputtokens", "candidatecount",
		"responsemimetype", "responseformat", "thinkingconfig", "thinkinglevel", "thinkingbudget":
		return fmt.Errorf("gemini request path %q places a generation field outside generationConfig", path)
	}
	if top != "generationConfig" || len(segments) < 3 {
		return nil
	}
	canonicalGenerationFields := map[string]string{
		"temperature":      "temperature",
		"topp":             "topP",
		"topk":             "topK",
		"maxoutputtokens":  "maxOutputTokens",
		"candidatecount":   "candidateCount",
		"responsemimetype": "responseMimeType",
		"responseschema":   "responseSchema",
		"thinkingconfig":   "thinkingConfig",
	}
	field := segments[2]
	if canonical, known := canonicalGenerationFields[normalizedFieldName(field)]; known && field != canonical {
		return fmt.Errorf("gemini request path %q must use canonical GenerateContent casing", path)
	}
	if field != "thinkingConfig" || len(segments) < 4 {
		return nil
	}
	thinkingField := segments[3]
	switch normalizedFieldName(thinkingField) {
	case "thinkinglevel":
		if thinkingField != "thinkingLevel" {
			return fmt.Errorf("gemini request path %q must use canonical GenerateContent casing", path)
		}
	case "thinkingbudget":
		return errors.New("gemini thinkingBudget is outside the approved reasoning-effort contract")
	}
	return nil
}

func (draft *geminiDraft) Validate() error {
	if draft == nil || draft.Document == nil {
		return errors.New("gemini request draft is nil")
	}
	if err := draft.profile.validateDraft(draft.Document, draft.stream); err != nil {
		return err
	}
	capabilities := draft.capabilities
	if capabilities.Surfaces&surfaceGenerateContent == 0 {
		return fmt.Errorf("gemini model %q is not covered on GenerateContent", capabilities.ModelID)
	}
	requiredMethod := methodGenerate
	if draft.stream {
		requiredMethod = methodStream
	}
	if capabilities.Methods&requiredMethod == 0 {
		return fmt.Errorf("gemini model %q does not support the selected generation method", capabilities.ModelID)
	}

	paths := draft.profile.generationPaths()
	if err := draft.validateFloatCapability(paths.Temperature, "temperature", capabilities.Temperature, capabilities.ForbidTemperature); err != nil {
		return err
	}
	if err := draft.validateFloatCapability(paths.TopP, "topP", capabilities.TopP, capabilities.ForbidTopP); err != nil {
		return err
	}
	if err := draft.validateIntCapability(paths.TopK, "topK", capabilities.TopK, capabilities.ForbidTopK); err != nil {
		return err
	}
	if capabilities.ForbidCandidateCount && containsNormalizedKey(draft.Body(), "candidatecount") {
		return fmt.Errorf("gemini model %q does not accept candidateCount", capabilities.ModelID)
	}
	if value, exists := draft.Get(paths.CandidateCount); exists {
		candidateCount, ok := integerValue(value)
		if !ok || candidateCount < 1 || candidateCount > 8 {
			return errors.New("gemini candidateCount must be an integer between 1 and 8")
		}
	}
	if value, exists := draft.Get(paths.MaxTokens); exists {
		maxTokens, ok := integerValue(value)
		if !ok || maxTokens <= 0 {
			return errors.New("gemini maxOutputTokens must be a positive integer")
		}
		if capabilities.OutputTokenLimit > 0 && maxTokens > capabilities.OutputTokenLimit {
			return fmt.Errorf("gemini maxOutputTokens exceeds model limit %d", capabilities.OutputTokenLimit)
		}
	}
	if value, exists := draft.Get(paths.ReasoningLevel); exists {
		level, ok := value.(string)
		if !ok || !capabilities.ThinkingLevels.supports(level) {
			return fmt.Errorf("gemini model %q does not accept thinking level", capabilities.ModelID)
		}
	}
	if capabilities.RejectPrefilledModelTurn {
		if err := validateNoPrefilledModelTurn(draft); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonicalGenerationShape(body map[string]interface{}) error {
	var config map[string]interface{}
	for _, key := range sortedAnyMapKeys(body) {
		value := body[key]
		normalized := normalizedFieldName(key)
		switch normalized {
		case "generationconfig":
			if key != "generationConfig" {
				return fmt.Errorf("gemini generation field %q must use canonical GenerateContent casing", key)
			}
			var ok bool
			config, ok = value.(map[string]interface{})
			if !ok {
				return errors.New("gemini generationConfig must be an object")
			}
		case "systeminstruction":
			if key != "systemInstruction" {
				return fmt.Errorf("gemini generation field %q must use canonical GenerateContent casing", key)
			}
		case "temperature", "topp", "topk", "maxoutputtokens", "candidatecount",
			"responsemimetype", "responseformat", "thinkingconfig", "thinkinglevel", "thinkingbudget":
			return fmt.Errorf("gemini generation field %q is outside generationConfig", key)
		}
	}
	if config == nil {
		return nil
	}
	canonicalFields := map[string]string{
		"temperature":        "temperature",
		"topp":               "topP",
		"topk":               "topK",
		"maxoutputtokens":    "maxOutputTokens",
		"candidatecount":     "candidateCount",
		"responsemimetype":   "responseMimeType",
		"responseschema":     "responseSchema",
		"thinkingconfig":     "thinkingConfig",
		"stopsequences":      "stopSequences",
		"presencepenalty":    "presencePenalty",
		"frequencypenalty":   "frequencyPenalty",
		"responsemodalities": "responseModalities",
	}
	for _, key := range sortedAnyMapKeys(config) {
		value := config[key]
		canonical, known := canonicalFields[normalizedFieldName(key)]
		if known && key != canonical {
			return fmt.Errorf("gemini generationConfig field %q must use canonical GenerateContent casing", key)
		}
		if canonical != "thinkingConfig" {
			continue
		}
		thinking, ok := value.(map[string]interface{})
		if !ok {
			return errors.New("gemini thinkingConfig must be an object")
		}
		for _, thinkingKey := range sortedAnyMapKeys(thinking) {
			switch normalizedFieldName(thinkingKey) {
			case "thinkinglevel":
				if thinkingKey != "thinkingLevel" {
					return fmt.Errorf("gemini thinkingConfig field %q must use canonical GenerateContent casing", thinkingKey)
				}
			case "thinkingbudget":
				return errors.New("gemini thinkingBudget is outside the approved reasoning-effort contract")
			}
		}
	}
	return nil
}

func (draft *geminiDraft) validateFloatCapability(
	path string,
	name string,
	capability floatCapability,
	forbidden bool,
) error {
	if forbidden && generationConfigContains(draft.Body(), name) {
		return fmt.Errorf("gemini model %q does not accept %s", draft.capabilities.ModelID, name)
	}
	value, exists := draft.Get(path)
	if !exists {
		return nil
	}
	if !capability.Supported {
		return fmt.Errorf("gemini model %q has no documented %s capability", draft.capabilities.ModelID, name)
	}
	number, ok := finiteNumber(value)
	if !ok || number < capability.Min || number > capability.Max {
		return fmt.Errorf("gemini %s must be between %g and %g", name, capability.Min, capability.Max)
	}
	return nil
}

func (draft *geminiDraft) validateIntCapability(
	path string,
	name string,
	capability intCapability,
	forbidden bool,
) error {
	if forbidden && generationConfigContains(draft.Body(), name) {
		return fmt.Errorf("gemini model %q does not accept %s", draft.capabilities.ModelID, name)
	}
	value, exists := draft.Get(path)
	if !exists {
		return nil
	}
	if !capability.Supported {
		return fmt.Errorf("gemini model %q has no documented %s capability", draft.capabilities.ModelID, name)
	}
	number, ok := integerValue(value)
	if !ok || number < capability.Min {
		return fmt.Errorf("gemini %s must be at least %d", name, capability.Min)
	}
	return nil
}

func generationConfigContains(body map[string]interface{}, target string) bool {
	for key, value := range body {
		if normalizedFieldName(key) != "generationconfig" {
			continue
		}
		config, ok := value.(map[string]interface{})
		if !ok {
			return false
		}
		for configKey := range config {
			if normalizedFieldName(configKey) == normalizedFieldName(target) {
				return true
			}
		}
	}
	return false
}

func containsNormalizedKey(value interface{}, target string) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if normalizedFieldName(key) == target || containsNormalizedKey(child, target) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if containsNormalizedKey(child, target) {
				return true
			}
		}
	}
	return false
}

func normalizedFieldName(value string) string {
	return strings.ToLower(strings.ReplaceAll(value, "_", ""))
}

func validateNoPrefilledModelTurn(draft *geminiDraft) error {
	contents, exists := draft.Get("/contents")
	if !exists {
		return errors.New("gemini contents are required")
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		return errors.New("gemini contents are invalid")
	}
	var normalized []Content
	if err := json.Unmarshal(encoded, &normalized); err != nil || len(normalized) == 0 {
		return errors.New("gemini contents are invalid")
	}
	last := normalized[len(normalized)-1]
	if strings.EqualFold(last.Role, "model") && len(last.Parts) > 0 {
		return errors.New("gemini latest models do not accept a prefilled model turn")
	}
	return nil
}

func integerValue(value interface{}) (int, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	var number int64
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number = reflected.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := reflected.Uint()
		if unsigned > math.MaxInt64 {
			return 0, false
		}
		number = int64(unsigned)
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		converted := int(floating)
		return converted, !math.IsNaN(floating) && !math.IsInf(floating, 0) && float64(converted) == floating
	default:
		return 0, false
	}
	converted := int(number)
	return converted, int64(converted) == number
}

func finiteNumber(value interface{}) (float64, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return 0, false
	}
	var number float64
	switch reflected.Kind() {
	case reflect.Float32, reflect.Float64:
		number = reflected.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		number = float64(reflected.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		number = float64(reflected.Uint())
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}
