package orchestration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SkillCanonicalEstimatorName    = "truvag3-canonical"
	SkillCanonicalEstimatorVersion = "v1"

	SkillAuthoringDescriptionWarningChars = 320
	SkillAuthoringManifestWarningTokens   = 2500
	SkillAuthoringResourceWarningTokens   = 4000
	SkillAuthoringResourceCountWarning    = 12
	SkillAuthoringPackageWarningBytes     = 256 * 1024
	SkillAuthoringCombinedWarningTokens   = 3000

	maxSkillValidationRuleDiagnostics = 64
	maxSkillDiagnosticCodeBytes       = 64
	maxSkillDiagnosticPathBytes       = 256
	maxSkillDiagnosticMessageBytes    = 1024
)

var (
	skillSlugPattern           = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	skillDiagnosticCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	longBase64RunPattern       = regexp.MustCompile(`[A-Za-z0-9+/]{512,}={0,2}`)
	credentialAssignment       = regexp.MustCompile(`(?i)(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|client[_-]?secret|secret[_-]?(?:key|token)|password|passwd)\s*[=:]\s*["']?([^\s,"';&}]{8,})`)
	bearerCredential           = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{12,})`)
	providerCredential         = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|gh[opusr]_[A-Za-z0-9]{20,}|AKIA[A-Z0-9]{16})\b`)
	negativeInstructionPattern = regexp.MustCompile(`(?i)\b(?:do not|don't|never|wrong|must not|should not|cannot|can't)\b`)
)

// SkillAuthoringLimits are deterministic provider-neutral admission ceilings.
// Every value is required to be positive; zero never disables a safety limit.
type SkillAuthoringLimits struct {
	MaxNameChars        int `json:"max_name_chars"`
	MaxDescriptionChars int `json:"max_description_chars"`
	MaxManifestTokens   int `json:"max_manifest_tokens"`
	MaxManifestBytes    int `json:"max_manifest_bytes"`
	MaxResourceTokens   int `json:"max_resource_tokens"`
	MaxResourceBytes    int `json:"max_resource_bytes"`
	MaxResources        int `json:"max_resources"`
	MaxPackageBytes     int `json:"max_package_bytes"`
}

// SkillPackageValidator is the provider-neutral deterministic authoring
// boundary used by management hosts and future authoring clients.
type SkillPackageValidator interface {
	Validate(
		context.Context,
		SkillRef,
		SkillPackageInput,
	) (ValidatedSkillPackage, SkillValidationResult, error)
}

// DefaultSkillAuthoringLimits returns the V1 defaults from the skills
// architecture. Callers may replace them with explicit positive values.
func DefaultSkillAuthoringLimits() SkillAuthoringLimits {
	return SkillAuthoringLimits{
		MaxNameChars:        64,
		MaxDescriptionChars: 1024,
		MaxManifestTokens:   5000,
		MaxManifestBytes:    24 * 1024,
		MaxResourceTokens:   8000,
		MaxResourceBytes:    32 * 1024,
		MaxResources:        32,
		MaxPackageBytes:     1024 * 1024,
	}
}

// Validate rejects disabled or ambiguous authoring ceilings.
func (limits SkillAuthoringLimits) Validate() error {
	values := []struct {
		name  string
		value int
	}{
		{"max name chars", limits.MaxNameChars},
		{"max description chars", limits.MaxDescriptionChars},
		{"max manifest tokens", limits.MaxManifestTokens},
		{"max manifest bytes", limits.MaxManifestBytes},
		{"max resource tokens", limits.MaxResourceTokens},
		{"max resource bytes", limits.MaxResourceBytes},
		{"max resources", limits.MaxResources},
		{"max package bytes", limits.MaxPackageBytes},
	}
	for _, candidate := range values {
		if candidate.value <= 0 {
			return fmt.Errorf("%w: %s must be positive", ErrInvalidSkillPackage, candidate.name)
		}
	}
	return nil
}

// SkillValidationRule adds deterministic domain diagnostics after mandatory
// framework validation. It receives a defensive copy and cannot rewrite the
// normalized package or remove framework findings.
type SkillValidationRule interface {
	Evaluate(context.Context, SkillRef, SkillPackageInput) []SkillValidationDiagnostic
}

// DefaultSkillPackageValidator implements the mandatory V1 normalization and
// validation contract. Optional rules are additive only.
type DefaultSkillPackageValidator struct {
	limits SkillAuthoringLimits
	rules  []SkillValidationRule
}

// NewDefaultSkillPackageValidator constructs the framework validator. Rules
// are copied and nil rules are rejected at construction.
func NewDefaultSkillPackageValidator(
	limits SkillAuthoringLimits,
	rules ...SkillValidationRule,
) (*DefaultSkillPackageValidator, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	copiedRules := append([]SkillValidationRule(nil), rules...)
	for _, rule := range copiedRules {
		if isNilSkillValidationRule(rule) {
			return nil, fmt.Errorf("%w: validation rule is nil", ErrInvalidSkillPackage)
		}
	}
	return &DefaultSkillPackageValidator{limits: limits, rules: copiedRules}, nil
}

// DecodeSkillPackageInputJSON decodes exactly one authoring payload and
// rejects malformed JSON, duplicate object fields, and unknown schema fields.
// HTTP hosts remain responsible for applying their configured request-body
// byte limit before calling this helper.
func DecodeSkillPackageInputJSON(data []byte) (SkillPackageInput, error) {
	if err := rejectDuplicateSkillJSONFields(data); err != nil {
		return SkillPackageInput{}, fmt.Errorf("%w: malformed or duplicate JSON field", ErrInvalidSkillPackage)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input SkillPackageInput
	if err := decoder.Decode(&input); err != nil {
		return SkillPackageInput{}, fmt.Errorf("%w: authoring payload does not match the skill schema", ErrInvalidSkillPackage)
	}
	if err := ensureSkillJSONEOF(decoder); err != nil {
		return SkillPackageInput{}, err
	}
	return input, nil
}

func rejectDuplicateSkillJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object field is not a string")
				}
				if _, found := seen[key]; found {
					return errors.New("duplicate object field")
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := visit(); err != nil {
		return err
	}
	return ensureSkillJSONEOF(decoder)
}

func ensureSkillJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: authoring payload must contain exactly one JSON value", ErrInvalidSkillPackage)
	}
	return nil
}

// Validate normalizes one complete authoring payload, performs framework
// checks, then evaluates additive deterministic rules. Invalid authoring
// returns a populated result together with ErrInvalidSkillPackage.
func (validator *DefaultSkillPackageValidator) Validate(
	ctx context.Context,
	ref SkillRef,
	input SkillPackageInput,
) (ValidatedSkillPackage, SkillValidationResult, error) {
	if validator == nil {
		return ValidatedSkillPackage{}, SkillValidationResult{},
			fmt.Errorf("%w: validator is nil", ErrInvalidSkillPackage)
	}
	if err := ctx.Err(); err != nil {
		return ValidatedSkillPackage{}, SkillValidationResult{}, err
	}

	normalized := normalizeSkillPackageInput(input)
	result := SkillValidationResult{
		Errors:   make([]SkillValidationDiagnostic, 0),
		Warnings: make([]SkillValidationDiagnostic, 0),
		Metrics: SkillValidationMetrics{
			TokenEstimator: SkillTokenEstimator{
				Name:    SkillCanonicalEstimatorName,
				Version: SkillCanonicalEstimatorVersion,
			},
		},
	}

	// Validate raw text before normalization so invalid UTF-8 cannot be replaced
	// or obscured by case folding or whitespace processing.
	validateSkillTextFields(input, &result)
	validator.validateFramework(ref, normalized, &result)
	validator.evaluateRules(ctx, ref, normalized, &result)
	result.Valid = len(result.Errors) == 0
	if err := ctx.Err(); err != nil {
		return ValidatedSkillPackage{}, result, err
	}
	if !result.Valid {
		return ValidatedSkillPackage{}, result,
			newSkillDomainError(ErrInvalidSkillPackage, "validate", ref)
	}
	return ValidatedSkillPackage{Package: cloneSkillPackageInput(normalized)}, result, nil
}

func (validator *DefaultSkillPackageValidator) validateFramework(
	ref SkillRef,
	input SkillPackageInput,
	result *SkillValidationResult,
) {
	if !validSkillSlug(ref.Namespace, validator.limits.MaxNameChars) {
		addSkillValidationError(result, "invalid_namespace", "/namespace", "Namespace must be a canonical lowercase slug within the configured character limit.")
	}
	if !validSkillSlug(ref.Name, validator.limits.MaxNameChars) {
		addSkillValidationError(result, "invalid_name", "/name", "Name must be a canonical lowercase slug within the configured character limit.")
	}

	validator.validateRequiredAndStructuralFields(input, result)
	validator.calculateAndValidateMetrics(input, result)
	validateSkillProhibitedContent(input, result)
	addSkillQualityWarnings(input, result)
}

func (validator *DefaultSkillPackageValidator) validateRequiredAndStructuralFields(
	input SkillPackageInput,
	result *SkillValidationResult,
) {
	if input.DisplayName == "" {
		addSkillValidationError(result, "display_name_required", "/display_name", "Display name is required.")
	}
	if input.Description == "" {
		addSkillValidationError(result, "description_required", "/description", "Description is required.")
	} else if utf8.RuneCountInString(input.Description) > validator.limits.MaxDescriptionChars {
		addSkillValidationError(result, "description_limit_exceeded", "/description", "Description exceeds the configured character limit.")
	}
	if len(input.PlanningInstructions) == 0 {
		addSkillValidationError(result, "planning_instructions_required", "/planning_instructions", "At least one non-empty planning instruction is required.")
	}
	if input.ChangeReason == "" {
		addSkillValidationError(result, "change_reason_required", "/change_reason", "Change reason is required.")
	} else if len(input.ChangeReason) > maxSkillAuditReasonBytes {
		addSkillValidationError(result, "change_reason_limit_exceeded", "/change_reason", "Change reason exceeds the fixed audit-reason byte limit.")
	}
	if len(input.Resources) > validator.limits.MaxResources {
		addSkillValidationError(result, "resource_count_limit_exceeded", "/resources", "Resource count exceeds the configured limit.")
	}
	for index, domain := range input.Domains {
		if !validSkillSlug(domain, validator.limits.MaxNameChars) {
			addSkillValidationError(result, "invalid_domain", fmt.Sprintf("/domains/%d", index), "Domain must be a canonical lowercase slug within the configured character limit.")
		}
	}
	for index, tag := range input.Tags {
		if !validSkillSlug(tag, validator.limits.MaxNameChars) {
			addSkillValidationError(result, "invalid_tag", fmt.Sprintf("/tags/%d", index), "Tag must be a canonical lowercase slug within the configured character limit.")
		}
	}

	seenResources := make(map[string]struct{}, len(input.Resources))
	for index, resource := range input.Resources {
		path := fmt.Sprintf("/resources/%d", index)
		if !validSkillSlug(resource.Name, validator.limits.MaxNameChars) {
			addSkillValidationError(result, "invalid_resource_name", path+"/name", "Resource name must be a canonical lowercase slug within the configured character limit.")
		}
		if _, found := seenResources[resource.Name]; found {
			addSkillValidationError(result, "duplicate_resource_name", path+"/name", "Resource names must be unique within a skill package.")
		} else if resource.Name != "" {
			seenResources[resource.Name] = struct{}{}
		}
		if resource.Description == "" {
			addSkillValidationError(result, "resource_description_required", path+"/description", "Resource description is required.")
		}
		if resource.LoadWhen == "" {
			addSkillValidationError(result, "resource_load_when_required", path+"/load_when", "Resource load_when guidance is required.")
		}
		if resource.Content == "" {
			addSkillValidationError(result, "resource_content_required", path+"/content", "Resource content is required.")
		}
		if resource.ContentType != "text/plain" && resource.ContentType != "text/markdown" {
			addSkillValidationError(result, "unsupported_resource_content_type", path+"/content_type", "V1 resources support only text/plain and text/markdown.")
		}

		seenScopes := make(map[SkillResourceScope]struct{}, len(resource.AppliesTo))
		for scopeIndex, scope := range resource.AppliesTo {
			scopePath := fmt.Sprintf("%s/applies_to/%d", path, scopeIndex)
			if !validSkillResourceScope(scope) {
				addSkillValidationError(result, "invalid_resource_scope", scopePath, "Resource scope must be planning, continuation, or synthesis.")
			}
			if _, found := seenScopes[scope]; found {
				addSkillValidationError(result, "duplicate_resource_scope", scopePath, "Resource scopes must be unique.")
			}
			seenScopes[scope] = struct{}{}
		}
	}
}

func (validator *DefaultSkillPackageValidator) calculateAndValidateMetrics(
	input SkillPackageInput,
	result *SkillValidationResult,
) {
	manifestText := skillManifestNormativeText(input)
	result.Metrics.ManifestBytes = len(manifestText)
	result.Metrics.ManifestTokens = canonicalSkillTokenEstimate(manifestText)
	result.Metrics.ResourceCount = len(input.Resources)

	for index, resource := range input.Resources {
		bytes := len(resource.Content)
		tokens := canonicalSkillTokenEstimate(resource.Content)
		result.Metrics.ResourceBytes += bytes
		result.Metrics.ResourceTokens += tokens
		if bytes > validator.limits.MaxResourceBytes {
			addSkillValidationError(result, "resource_byte_limit_exceeded", fmt.Sprintf("/resources/%d/content", index), "Resource content exceeds the configured byte limit.")
		}
		if tokens > validator.limits.MaxResourceTokens {
			addSkillValidationError(result, "resource_token_limit_exceeded", fmt.Sprintf("/resources/%d/content", index), "Resource content exceeds the configured estimated-token limit.")
		}
	}
	if result.Metrics.ManifestBytes > validator.limits.MaxManifestBytes {
		addSkillValidationError(result, "manifest_byte_limit_exceeded", "/planning_instructions", "Manifest normative content exceeds the configured byte limit.")
	}
	if result.Metrics.ManifestTokens > validator.limits.MaxManifestTokens {
		addSkillValidationError(result, "manifest_token_limit_exceeded", "/planning_instructions", "Manifest normative content exceeds the configured estimated-token limit.")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		addSkillValidationError(result, "package_encoding_failed", "", "Normalized package could not be encoded.")
		return
	}
	result.Metrics.PackageBytes = len(encoded)
	if result.Metrics.PackageBytes > validator.limits.MaxPackageBytes {
		addSkillValidationError(result, "package_byte_limit_exceeded", "", "Complete package exceeds the configured byte limit.")
	}
}

func (validator *DefaultSkillPackageValidator) evaluateRules(
	ctx context.Context,
	ref SkillRef,
	input SkillPackageInput,
	result *SkillValidationResult,
) {
	remaining := maxSkillValidationRuleDiagnostics
	for ruleIndex, rule := range validator.rules {
		if ctx.Err() != nil {
			return
		}
		diagnostics, panicked := evaluateSkillValidationRule(ctx, rule, ref, cloneSkillPackageInput(input))
		if panicked {
			addSkillValidationError(result, "custom_rule_failed", "", "A custom validation rule failed while evaluating the package.")
			continue
		}
		if len(diagnostics) > remaining {
			addSkillValidationError(result, "custom_rule_limit_exceeded", "", "Custom validation rules returned too many diagnostics.")
			return
		}
		for diagnosticIndex, diagnostic := range diagnostics {
			if !validCustomSkillDiagnostic(diagnostic) {
				path := fmt.Sprintf("/validation_rules/%d/diagnostics/%d", ruleIndex, diagnosticIndex)
				addSkillValidationError(result, "invalid_custom_rule_diagnostic", path, "A custom validation rule returned an invalid or unbounded diagnostic.")
				continue
			}
			if diagnostic.Severity == SkillValidationError {
				result.Errors = append(result.Errors, diagnostic)
			} else {
				result.Warnings = append(result.Warnings, diagnostic)
			}
		}
		remaining -= len(diagnostics)
	}
}

func evaluateSkillValidationRule(
	ctx context.Context,
	rule SkillValidationRule,
	ref SkillRef,
	input SkillPackageInput,
) (diagnostics []SkillValidationDiagnostic, panicked bool) {
	defer func() {
		if recover() != nil {
			diagnostics = nil
			panicked = true
		}
	}()
	return rule.Evaluate(ctx, ref, input), false
}

func normalizeSkillPackageInput(input SkillPackageInput) SkillPackageInput {
	normalized := cloneSkillPackageInput(input)
	normalized.DisplayName = normalizeSkillMetadataText(normalized.DisplayName)
	normalized.Description = normalizeSkillMetadataText(normalized.Description)
	normalized.Domains = normalizeSkillIdentifierList(normalized.Domains)
	normalized.Tags = normalizeSkillIdentifierList(normalized.Tags)
	normalized.PlanningInstructions = normalizeSkillTextList(normalized.PlanningInstructions)
	normalized.ResponseInstructions = normalizeSkillTextList(normalized.ResponseInstructions)
	normalized.ToolHints = normalizeSkillTextList(normalized.ToolHints)
	normalized.ActivationExamples.ShouldActivate = normalizeSkillTextList(normalized.ActivationExamples.ShouldActivate)
	normalized.ActivationExamples.ShouldNotActivate = normalizeSkillTextList(normalized.ActivationExamples.ShouldNotActivate)
	normalized.ChangeReason = normalizeSkillMetadataText(normalized.ChangeReason)
	for index := range normalized.Resources {
		resource := &normalized.Resources[index]
		resource.Description = normalizeSkillMetadataText(resource.Description)
		resource.LoadWhen = normalizeSkillMetadataText(resource.LoadWhen)
		resource.ContentType = strings.ToLower(strings.TrimSpace(resource.ContentType))
		resource.Content = normalizeSkillLineEndings(resource.Content)
		// Preserve duplicates until structural validation so an ambiguous authored
		// scope list is rejected rather than silently normalized away.
		resource.AppliesTo = sortedSkillResourceScopes(resource.AppliesTo)
	}
	return normalized
}

func normalizeSkillMetadataText(value string) string {
	return strings.TrimSpace(normalizeSkillLineEndings(value))
}

func normalizeSkillLineEndings(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func normalizeSkillTextList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeSkillMetadataText(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeSkillIdentifierList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizeSkillResourceScopes(values []SkillResourceScope) []SkillResourceScope {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[SkillResourceScope]struct{}, len(values))
	normalized := make([]SkillResourceScope, 0, len(values))
	for _, value := range values {
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func sortedSkillResourceScopes(values []SkillResourceScope) []SkillResourceScope {
	if len(values) == 0 {
		return nil
	}
	normalized := append([]SkillResourceScope(nil), values...)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func cloneSkillPackageInput(input SkillPackageInput) SkillPackageInput {
	cloned := input
	cloned.Domains = append([]string(nil), input.Domains...)
	cloned.Tags = append([]string(nil), input.Tags...)
	cloned.PlanningInstructions = append([]string(nil), input.PlanningInstructions...)
	cloned.ResponseInstructions = append([]string(nil), input.ResponseInstructions...)
	cloned.ToolHints = append([]string(nil), input.ToolHints...)
	cloned.ActivationExamples.ShouldActivate = append([]string(nil), input.ActivationExamples.ShouldActivate...)
	cloned.ActivationExamples.ShouldNotActivate = append([]string(nil), input.ActivationExamples.ShouldNotActivate...)
	cloned.Resources = make([]SkillResourceInput, len(input.Resources))
	for index, resource := range input.Resources {
		cloned.Resources[index] = resource
		cloned.Resources[index].AppliesTo = append([]SkillResourceScope(nil), resource.AppliesTo...)
	}
	return cloned
}

func validateSkillTextFields(input SkillPackageInput, result *SkillValidationResult) {
	visitSkillPackageText(input, func(path, value string) {
		if !utf8.ValidString(value) {
			addSkillValidationError(result, "invalid_utf8", path, "Text must contain valid UTF-8.")
			return
		}
		if containsIllegalSkillControl(value) {
			addSkillValidationError(result, "illegal_control_character", path, "Text contains an illegal control character.")
		}
	})
}

func validateSkillProhibitedContent(input SkillPackageInput, result *SkillValidationResult) {
	visitSkillPackageText(input, func(path, value string) {
		if containsHighConfidenceSkillCredential(value) {
			addSkillValidationError(result, "prohibited_secret", path, "Skill packages must not contain credentials or private keys.")
		}
		if containsExecutableSkillAsset(value) {
			addSkillValidationError(result, "prohibited_executable_payload", path, "V1 skill packages accept inert text guidance, not executable or binary assets.")
		}
		if containsSkillAuthorityBypass(value) {
			addSkillValidationError(result, "prohibited_authority_override", path, "Skill guidance cannot grant capabilities or bypass framework, platform, or HITL policy.")
		}
		if containsReservedSkillPromptTag(value) {
			addSkillValidationWarning(result, "reserved_prompt_tag", path, "Use skill-specific markup instead of framework-reserved prompt tags.")
		}
	})
}

func addSkillQualityWarnings(input SkillPackageInput, result *SkillValidationResult) {
	negativeInstructionWarningAdded := false
	visitSkillNormativePromptText(input, func(path, value string) {
		if !negativeInstructionWarningAdded && containsNegativeInstructionPhrasing(value) {
			addSkillValidationWarning(result, "negative_instruction_phrasing", path, "Express the desired behavior as a positive directive.")
			negativeInstructionWarningAdded = true
		}
	})
	if utf8.RuneCountInString(input.Description) > SkillAuthoringDescriptionWarningChars {
		addSkillValidationWarning(result, "description_too_detailed", "/description", "Keep the catalog description concise and move procedural detail into instructions or resources.")
	}
	if input.Description != "" && !descriptionHasActivationLanguage(input.Description) {
		addSkillValidationWarning(result, "description_activation_unclear", "/description", "Describe both what the skill does and when it should activate using concrete request vocabulary.")
	}
	if result.Metrics.ManifestTokens > SkillAuthoringManifestWarningTokens {
		addSkillValidationWarning(result, "manifest_too_detailed", "/planning_instructions", "Consider moving conditional detail into independently loadable resources.")
	}
	for index, resource := range input.Resources {
		if canonicalSkillTokenEstimate(resource.Content) > SkillAuthoringResourceWarningTokens {
			addSkillValidationWarning(result, "resource_too_detailed", fmt.Sprintf("/resources/%d/content", index), "Consider splitting this resource around distinct selection conditions.")
		}
		if resource.LoadWhen != "" && loadWhenIsAmbiguous(resource.LoadWhen) {
			addSkillValidationWarning(result, "resource_load_when_ambiguous", fmt.Sprintf("/resources/%d/load_when", index), "Use concrete entities, tasks, or conditions that distinguish when this resource is relevant.")
		}
	}
	if len(input.Resources) > SkillAuthoringResourceCountWarning {
		addSkillValidationWarning(result, "resource_count_high", "/resources", "A smaller resource catalog is usually easier for selectors to distinguish reliably.")
	}
	if result.Metrics.PackageBytes > SkillAuthoringPackageWarningBytes {
		addSkillValidationWarning(result, "package_too_large", "", "The package is above the recommended authoring size even though it remains within the hard limit.")
	}
	if result.Metrics.ManifestTokens+result.Metrics.ResourceTokens > SkillAuthoringCombinedWarningTokens {
		addSkillValidationWarning(result, "combined_instructions_too_detailed", "", "Combined normative guidance is above the prompt-quality target; verify decomposition and activation precision.")
	}
}

func visitSkillNormativePromptText(input SkillPackageInput, visit func(path, value string)) {
	for index, value := range input.PlanningInstructions {
		visit(fmt.Sprintf("/planning_instructions/%d", index), value)
	}
	for index, value := range input.ResponseInstructions {
		visit(fmt.Sprintf("/response_instructions/%d", index), value)
	}
	for index, value := range input.ToolHints {
		visit(fmt.Sprintf("/tool_hints/%d", index), value)
	}
	for index, resource := range input.Resources {
		visit(fmt.Sprintf("/resources/%d/content", index), resource.Content)
	}
}

func containsNegativeInstructionPhrasing(value string) bool {
	return negativeInstructionPattern.MatchString(value)
}

func visitSkillPackageText(input SkillPackageInput, visit func(path, value string)) {
	visit("/display_name", input.DisplayName)
	visit("/description", input.Description)
	for index, value := range input.Domains {
		visit(fmt.Sprintf("/domains/%d", index), value)
	}
	for index, value := range input.Tags {
		visit(fmt.Sprintf("/tags/%d", index), value)
	}
	for index, value := range input.PlanningInstructions {
		visit(fmt.Sprintf("/planning_instructions/%d", index), value)
	}
	for index, value := range input.ResponseInstructions {
		visit(fmt.Sprintf("/response_instructions/%d", index), value)
	}
	for index, value := range input.ToolHints {
		visit(fmt.Sprintf("/tool_hints/%d", index), value)
	}
	for index, resource := range input.Resources {
		base := fmt.Sprintf("/resources/%d", index)
		visit(base+"/name", resource.Name)
		visit(base+"/description", resource.Description)
		visit(base+"/load_when", resource.LoadWhen)
		visit(base+"/content_type", resource.ContentType)
		visit(base+"/content", resource.Content)
	}
	for index, value := range input.ActivationExamples.ShouldActivate {
		visit(fmt.Sprintf("/activation_examples/should_activate/%d", index), value)
	}
	for index, value := range input.ActivationExamples.ShouldNotActivate {
		visit(fmt.Sprintf("/activation_examples/should_not_activate/%d", index), value)
	}
	visit("/change_reason", input.ChangeReason)
}

func containsIllegalSkillControl(value string) bool {
	for _, r := range value {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return true
		}
		if r >= 0x7f && r <= 0x9f {
			return true
		}
	}
	return false
}

func containsHighConfidenceSkillCredential(value string) bool {
	lower := strings.ToLower(value)
	// Keep PEM sentinels split in source so repository secret scanners do not
	// mistake the validation rule itself for embedded private-key material.
	if strings.Contains(lower, "-----begin "+"private key-----") ||
		strings.Contains(lower, "-----begin rsa "+"private key-----") ||
		strings.Contains(lower, "-----begin ec "+"private key-----") ||
		providerCredential.MatchString(value) {
		return true
	}
	for _, pattern := range []*regexp.Regexp{credentialAssignment, bearerCredential} {
		matches := pattern.FindAllStringSubmatch(value, -1)
		for _, match := range matches {
			if len(match) > 1 && !isSkillCredentialPlaceholder(match[1]) {
				return true
			}
		}
	}
	return false
}

func isSkillCredentialPlaceholder(value string) bool {
	lower := strings.ToLower(strings.Trim(value, `"'`))
	return lower == "" || strings.Contains(lower, "redacted") ||
		strings.Contains(lower, "example") || strings.Contains(lower, "placeholder") ||
		strings.Contains(lower, "your_") || strings.Contains(lower, "your-") ||
		strings.HasPrefix(lower, "${") || strings.HasPrefix(lower, "{{") ||
		strings.HasPrefix(lower, "<") || strings.HasPrefix(lower, "[")
}

func containsExecutableSkillAsset(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "data:application/octet-stream;base64,") ||
		strings.Contains(lower, "data:application/x-executable;base64,") ||
		longBase64RunPattern.MatchString(value)
}

func containsSkillAuthorityBypass(value string) bool {
	lower := strings.ToLower(value)
	for _, phrase := range []string{
		"bypass human approval", "skip human approval", "ignore human approval",
		"bypass hitl", "skip hitl", "disable hitl", "ignore hitl",
		"bypass platform policy", "ignore platform policy", "override platform policy",
		"bypass framework policy", "ignore framework policy", "override framework policy",
		"override the system prompt", "ignore the system prompt",
		"grant yourself permission", "grant yourself capability",
	} {
		if containsUnsafeSkillAuthorityPhrase(lower, phrase) {
			return true
		}
	}
	return false
}

func containsUnsafeSkillAuthorityPhrase(value, phrase string) bool {
	for offset := 0; ; {
		index := strings.Index(value[offset:], phrase)
		if index < 0 {
			return false
		}
		index += offset
		prefixStart := max(0, index-24)
		prefix := strings.TrimSpace(value[prefixStart:index])
		safeNegation := false
		for _, negation := range []string{"do not", "never", "must not", "should not", "cannot", "can't"} {
			if strings.HasSuffix(prefix, negation) {
				safeNegation = true
				break
			}
		}
		if !safeNegation {
			return true
		}
		offset = index + len(phrase)
	}
}

func containsReservedSkillPromptTag(value string) bool {
	lower := strings.ToLower(value)
	for _, tag := range []string{
		"runtime_context", "context_precedence", "skill_precedence", "active_skills",
		"user_request", "agent_responses", "user_profile", "conversation_history",
		"skill", "planning_guidance", "response_guidance", "instruction",
		"selected_resources", "resource", "active_skill_tool_hints",
		"identity", "rules", "output_contract", "developer_guidance",
		"selector_input", "skill_candidates", "resource_selector_input",
		"resource_candidates", "skill_authoring_input",
	} {
		if containsExactPromptTag(lower, tag) {
			return true
		}
	}
	return false
}

func descriptionHasActivationLanguage(value string) bool {
	lower := strings.ToLower(value)
	for _, phrase := range []string{"use when", "activate when", "when a ", "when the ", "for requests", "for tasks", "whenever"} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func loadWhenIsAmbiguous(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return len(strings.Fields(lower)) < 4 || lower == "when relevant" ||
		lower == "when needed" || lower == "as needed" || lower == "if relevant"
}

func skillManifestNormativeText(input SkillPackageInput) string {
	parts := make([]string, 0, len(input.PlanningInstructions)+len(input.ResponseInstructions)+len(input.ToolHints))
	parts = append(parts, input.PlanningInstructions...)
	parts = append(parts, input.ResponseInstructions...)
	parts = append(parts, input.ToolHints...)
	return strings.Join(parts, "\n")
}

// canonicalSkillTokenEstimate is the immutable ingestion estimator for V1.
// It intentionally matches the framework's documented 3.5 UTF-8 bytes/token
// heuristic while remaining independent of an injected provider tokenizer.
func canonicalSkillTokenEstimate(value string) int {
	if value == "" {
		return 0
	}
	return int(math.Ceil(float64(len(value)) / 3.5))
}

func validSkillSlug(value string, maxChars int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxChars && skillSlugPattern.MatchString(value)
}

func validSkillResourceScope(value SkillResourceScope) bool {
	switch value {
	case SkillResourcePlanning, SkillResourceContinuation, SkillResourceSynthesis:
		return true
	default:
		return false
	}
}

func validCustomSkillDiagnostic(diagnostic SkillValidationDiagnostic) bool {
	if diagnostic.Severity != SkillValidationError && diagnostic.Severity != SkillValidationWarning {
		return false
	}
	if len(diagnostic.Code) == 0 || len(diagnostic.Code) > maxSkillDiagnosticCodeBytes ||
		!skillDiagnosticCodePattern.MatchString(diagnostic.Code) {
		return false
	}
	if len(diagnostic.Path) > maxSkillDiagnosticPathBytes ||
		(diagnostic.Path != "" && !strings.HasPrefix(diagnostic.Path, "/")) {
		return false
	}
	return diagnostic.Message != "" && len(diagnostic.Message) <= maxSkillDiagnosticMessageBytes &&
		utf8.ValidString(diagnostic.Message) && !containsIllegalSkillControl(diagnostic.Message) &&
		!containsHighConfidenceSkillCredential(diagnostic.Message)
}

func addSkillValidationError(result *SkillValidationResult, code, path, message string) {
	result.Errors = append(result.Errors, SkillValidationDiagnostic{
		Code: code, Path: path, Message: message, Severity: SkillValidationError,
	})
}

func addSkillValidationWarning(result *SkillValidationResult, code, path, message string) {
	result.Warnings = append(result.Warnings, SkillValidationDiagnostic{
		Code: code, Path: path, Message: message, Severity: SkillValidationWarning,
	})
}

func isNilSkillValidationRule(rule SkillValidationRule) bool {
	if rule == nil {
		return true
	}
	// Interfaces containing typed nil pointers are not equal to nil. A JSON
	// round trip is inappropriate for code values, so use the standard error
	// path through a tiny type switch-free reflection helper in one place.
	reflected := reflect.ValueOf(rule)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// ComputeSkillManifestHash returns the public V1 integrity hash for one exact
// immutable logical manifest. It excludes the manifest hash field itself.
func ComputeSkillManifestHash(manifest SkillManifest) (string, error) {
	if manifest.Ref.Version == 0 || !validSkillSlug(manifest.Ref.Ref.Namespace, 64) ||
		!validSkillSlug(manifest.Ref.Ref.Name, 64) {
		return "", fmt.Errorf("%w: manifest identity or revision is invalid", ErrInvalidSkillPackage)
	}
	if len(manifest.PlanningInstructions) == 0 ||
		!canonicalSkillManifestTextIsValid(manifest) {
		return "", fmt.Errorf("%w: manifest text is not canonical", ErrInvalidSkillPackage)
	}
	seenResources := make(map[string]struct{}, len(manifest.Resources))
	for _, resource := range manifest.Resources {
		if !validSkillSlug(resource.Name, 64) ||
			(resource.ContentType != "text/plain" && resource.ContentType != "text/markdown") ||
			!validSkillSHA256(resource.ResourceHash) {
			return "", fmt.Errorf("%w: manifest resource index is invalid", ErrInvalidSkillPackage)
		}
		if _, found := seenResources[resource.Name]; found {
			return "", fmt.Errorf("%w: manifest resource index contains duplicate names", ErrInvalidSkillPackage)
		}
		seenResources[resource.Name] = struct{}{}
	}
	canonical := canonicalSkillManifestV1{
		Schema:               "truvag3.skill-manifest/v1",
		Namespace:            manifest.Ref.Ref.Namespace,
		Name:                 manifest.Ref.Ref.Name,
		Version:              manifest.Ref.Version,
		DisplayName:          normalizeSkillMetadataText(manifest.DisplayName),
		Description:          normalizeSkillMetadataText(manifest.Description),
		Domains:              normalizeSkillIdentifierList(manifest.Domains),
		Tags:                 normalizeSkillIdentifierList(manifest.Tags),
		PlanningInstructions: normalizeSkillTextList(manifest.PlanningInstructions),
		ResponseInstructions: normalizeSkillTextList(manifest.ResponseInstructions),
		ToolHints:            normalizeSkillTextList(manifest.ToolHints),
		Resources:            canonicalSkillResourceIndex(manifest.Resources),
	}
	return hashCanonicalSkillValue(canonical)
}

// ComputeSkillResourceHash returns the public V1 integrity hash for one exact
// immutable logical resource. It excludes ExpectedHash and manifest hash.
func ComputeSkillResourceHash(resource SkillResource) (string, error) {
	ref := resource.Ref.Skill
	if ref.Version == 0 || !validSkillSlug(ref.Ref.Namespace, 64) ||
		!validSkillSlug(ref.Ref.Name, 64) || !validSkillSlug(resource.Ref.Name, 64) {
		return "", fmt.Errorf("%w: resource identity or revision is invalid", ErrInvalidSkillPackage)
	}
	if !utf8.ValidString(resource.Content) || containsIllegalSkillControl(resource.Content) {
		return "", fmt.Errorf("%w: resource content is not canonical text", ErrInvalidSkillPackage)
	}
	contentType := strings.ToLower(strings.TrimSpace(resource.ContentType))
	if contentType != "text/plain" && contentType != "text/markdown" {
		return "", fmt.Errorf("%w: resource content type is invalid", ErrInvalidSkillPackage)
	}
	canonical := canonicalSkillResourceV1{
		Schema:      "truvag3.skill-resource/v1",
		Namespace:   ref.Ref.Namespace,
		Name:        ref.Ref.Name,
		Version:     ref.Version,
		Resource:    resource.Ref.Name,
		ContentType: contentType,
		Content:     normalizeSkillLineEndings(resource.Content),
	}
	return hashCanonicalSkillValue(canonical)
}

// SkillVersionedAuthoringContentEqual compares the normalized content that
// determines whether publication creates a new immutable revision. It includes
// activation examples and excludes change_reason. No package hash is exposed.
func SkillVersionedAuthoringContentEqual(left, right ValidatedSkillPackage) bool {
	leftCanonical, leftErr := canonicalSkillVersionedAuthoringContent(left.Package)
	rightCanonical, rightErr := canonicalSkillVersionedAuthoringContent(right.Package)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func canonicalSkillVersionedAuthoringContent(input SkillPackageInput) ([]byte, error) {
	normalized := normalizeSkillPackageInput(input)
	normalized.ChangeReason = ""
	sort.Strings(normalized.ActivationExamples.ShouldActivate)
	sort.Strings(normalized.ActivationExamples.ShouldNotActivate)
	sort.Slice(normalized.Resources, func(i, j int) bool {
		return normalized.Resources[i].Name < normalized.Resources[j].Name
	})
	return json.Marshal(struct {
		Schema  string            `json:"schema"`
		Package SkillPackageInput `json:"package"`
	}{
		Schema:  "truvag3.skill-authoring-content/v1",
		Package: normalized,
	})
}

func canonicalSkillManifestTextIsValid(manifest SkillManifest) bool {
	values := []string{manifest.DisplayName, manifest.Description}
	values = append(values, manifest.Domains...)
	values = append(values, manifest.Tags...)
	values = append(values, manifest.PlanningInstructions...)
	values = append(values, manifest.ResponseInstructions...)
	values = append(values, manifest.ToolHints...)
	for _, resource := range manifest.Resources {
		values = append(values, resource.Name, resource.Description, resource.LoadWhen, resource.ContentType)
	}
	for _, value := range values {
		if !utf8.ValidString(value) || containsIllegalSkillControl(value) {
			return false
		}
	}
	return true
}

func validSkillSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

type canonicalSkillManifestV1 struct {
	Schema               string                           `json:"schema"`
	Namespace            string                           `json:"namespace"`
	Name                 string                           `json:"name"`
	Version              uint64                           `json:"version"`
	DisplayName          string                           `json:"display_name"`
	Description          string                           `json:"description"`
	Domains              []string                         `json:"domains,omitempty"`
	Tags                 []string                         `json:"tags,omitempty"`
	PlanningInstructions []string                         `json:"planning_instructions"`
	ResponseInstructions []string                         `json:"response_instructions,omitempty"`
	ToolHints            []string                         `json:"tool_hints,omitempty"`
	Resources            []canonicalSkillResourceMetadata `json:"resources,omitempty"`
}

type canonicalSkillResourceMetadata struct {
	Name                 string               `json:"name"`
	Description          string               `json:"description"`
	LoadWhen             string               `json:"load_when"`
	AppliesTo            []SkillResourceScope `json:"applies_to,omitempty"`
	RequiredWhenSelected bool                 `json:"required_when_selected,omitempty"`
	ContentType          string               `json:"content_type"`
	ResourceHash         string               `json:"resource_hash"`
}

type canonicalSkillResourceV1 struct {
	Schema      string `json:"schema"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Version     uint64 `json:"version"`
	Resource    string `json:"resource"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

func canonicalSkillResourceIndex(resources []SkillResourceMetadata) []canonicalSkillResourceMetadata {
	canonical := make([]canonicalSkillResourceMetadata, len(resources))
	for index, resource := range resources {
		canonical[index] = canonicalSkillResourceMetadata{
			Name:                 resource.Name,
			Description:          normalizeSkillMetadataText(resource.Description),
			LoadWhen:             normalizeSkillMetadataText(resource.LoadWhen),
			AppliesTo:            normalizeSkillResourceScopes(resource.AppliesTo),
			RequiredWhenSelected: resource.RequiredWhenSelected,
			ContentType:          strings.ToLower(strings.TrimSpace(resource.ContentType)),
			ResourceHash:         resource.ResourceHash,
		}
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Name < canonical[j].Name })
	return canonical
}

func hashCanonicalSkillValue(value interface{}) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode canonical skill content: %v", ErrInvalidSkillPackage, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// isNilInterfaceValue is deliberately isolated so code-bearing extension
// validation does not spread reflection through the authoring path.
var _ SkillPackageValidator = (*DefaultSkillPackageValidator)(nil)
