package orchestration

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var errInvalidSkillRuntimeConfig = errors.New("invalid skill runtime configuration")

const (
	maxSkillRuntimePolicyIDBytes = 128
	maxSkillGuidanceBytes        = 4096
	maxSkillGuidanceTokens       = 512
)

var skillRuntimePolicyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

func defaultSkillRuntimeLimits() SkillRuntimeLimits {
	return SkillRuntimeLimits{
		MaxBindings:                32,
		MaxAutoCandidates:          16,
		CatalogTokenBudget:         2000,
		MaxResourceCandidates:      32,
		ResourceCatalogTokenBudget: 2000,
		MaxActiveSkills:            6,
		TotalTokenBudget:           8192,
		MainTokenBudget:            6144,
		ResourceTokenBudget:        4096,
		MaxResourcesPerPhase:       2,
		MaxResourcesPerExecution:   8,
		ResolutionMaxTokens:        512,
		RegistryReadTimeout:        5 * time.Second,
		SynthesisTokenBudget:       2048,
		EffectiveInputTokenBudget:  0,
	}
}

func normalizeSkillConfig(config *SkillConfig) {
	if config == nil {
		return
	}
	defaults := defaultSkillRuntimeLimits()
	if config.DomainCompatibilityMode == "" {
		config.DomainCompatibilityMode = SkillDomainCompatibilityWarn
	}
	if config.bindingSource == "" {
		config.bindingSource = SkillBindingsFromCode
	}
	if config.Cache.Mode == "" {
		config.Cache.Mode = SkillContentCacheLocal
	}
	if config.Cache.MaxBytes == 0 {
		config.Cache.MaxBytes = DefaultSkillContentCacheCapacityBytes
	}
	fillSkillRuntimeLimitDefaults(&config.Limits, defaults)
	for index := range config.Bindings {
		if strings.TrimSpace(config.Bindings[index].Version) == "" {
			config.Bindings[index].Version = "published"
		}
	}
}

func fillSkillRuntimeLimitDefaults(target *SkillRuntimeLimits, defaults SkillRuntimeLimits) {
	if target.MaxBindings == 0 {
		target.MaxBindings = defaults.MaxBindings
	}
	if target.MaxAutoCandidates == 0 {
		target.MaxAutoCandidates = defaults.MaxAutoCandidates
	}
	if target.CatalogTokenBudget == 0 {
		target.CatalogTokenBudget = defaults.CatalogTokenBudget
	}
	if target.MaxResourceCandidates == 0 {
		target.MaxResourceCandidates = defaults.MaxResourceCandidates
	}
	if target.ResourceCatalogTokenBudget == 0 {
		target.ResourceCatalogTokenBudget = defaults.ResourceCatalogTokenBudget
	}
	if target.MaxActiveSkills == 0 {
		target.MaxActiveSkills = defaults.MaxActiveSkills
	}
	if target.TotalTokenBudget == 0 {
		target.TotalTokenBudget = defaults.TotalTokenBudget
	}
	if target.MainTokenBudget == 0 {
		target.MainTokenBudget = defaults.MainTokenBudget
	}
	if target.ResourceTokenBudget == 0 {
		target.ResourceTokenBudget = defaults.ResourceTokenBudget
	}
	if target.MaxResourcesPerPhase == 0 {
		target.MaxResourcesPerPhase = defaults.MaxResourcesPerPhase
	}
	if target.MaxResourcesPerExecution == 0 {
		target.MaxResourcesPerExecution = defaults.MaxResourcesPerExecution
	}
	if target.ResolutionMaxTokens == 0 {
		target.ResolutionMaxTokens = defaults.ResolutionMaxTokens
	}
	if target.RegistryReadTimeout == 0 {
		target.RegistryReadTimeout = defaults.RegistryReadTimeout
	}
	if target.SynthesisTokenBudget == 0 {
		target.SynthesisTokenBudget = defaults.SynthesisTokenBudget
	}
}

func validateSkillRuntimeConfig(config *OrchestratorConfig) error {
	skills := &config.Skills
	if err := validateSkillRuntimeLimits(skills.Limits); err != nil {
		return err
	}
	switch skills.DomainCompatibilityMode {
	case SkillDomainCompatibilityOff, SkillDomainCompatibilityWarn, SkillDomainCompatibilityEnforce:
	default:
		return fmt.Errorf("%w: skills domain compatibility mode is invalid", ErrInvalidOrchestratorConfig)
	}
	switch skills.Cache.Mode {
	case SkillContentCacheLocal, SkillContentCacheDisabled:
	default:
		return fmt.Errorf("%w: skills cache mode is invalid", ErrInvalidOrchestratorConfig)
	}
	if skills.Cache.MaxBytes <= 0 {
		return fmt.Errorf("%w: skills cache max bytes must be positive", ErrInvalidOrchestratorConfig)
	}
	if skills.Cache.Mode == SkillContentCacheDisabled && !isNilBackendValue(config.SkillContentCache) {
		return fmt.Errorf("%w: injected skill content cache is incompatible with disabled cache mode", ErrInvalidOrchestratorConfig)
	}
	if err := validateSkillRuntimePolicyID(skills.RuntimePolicyID); err != nil {
		return err
	}
	if err := validateSkillBindings(skills); err != nil {
		return err
	}
	active := skills.Enabled && len(skills.Bindings) > 0
	if active && isNilBackendValue(config.SkillRegistry) {
		return fmt.Errorf("%w: skill registry is required when skills have effective bindings", ErrInvalidOrchestratorConfig)
	}
	if active {
		customComponents := []struct {
			name  string
			value interface{}
		}{
			{"activation policy", config.SkillActivationPolicy},
			{"resolver", config.SkillResolver},
			{"resource resolver", config.SkillResourceResolver},
			{"token counter", config.SkillTokenCounter},
		}
		for _, component := range customComponents {
			if component.value != nil && isNilBackendValue(component.value) {
				return fmt.Errorf("%w: typed-nil skill %s", ErrInvalidOrchestratorConfig, component.name)
			}
		}
	}
	if !skills.Enabled {
		for _, binding := range skills.Bindings {
			if binding.Required {
				return fmt.Errorf("%w: required skill binding cannot be disabled", ErrInvalidOrchestratorConfig)
			}
		}
	}
	if active {
		if err := validateSkillSelectorOptions("activation", config.SkillActivationAIOptions); err != nil {
			return err
		}
		if err := validateSkillSelectorOptions("resource", config.SkillResourceAIOptions); err != nil {
			return err
		}
		if err := validateSkillPromptGuidance(config.SkillPromptGuidance); err != nil {
			return err
		}
		if config.PlanAIOptions != nil && config.PlanAIOptions.SystemPrompt != nil &&
			containsFrameworkSkillPromptTag(*config.PlanAIOptions.SystemPrompt) {
			return fmt.Errorf("%w: plan AI system prompt contains a reserved skill section", ErrInvalidOrchestratorConfig)
		}
		if config.SynthesisAIOptions != nil && config.SynthesisAIOptions.SystemPrompt != nil &&
			containsFrameworkSkillPromptTag(*config.SynthesisAIOptions.SystemPrompt) {
			return fmt.Errorf("%w: synthesis AI system prompt contains a reserved skill section", ErrInvalidOrchestratorConfig)
		}
		if containsFrameworkSkillPromptTag(config.PromptConfig.SystemInstructions) {
			return fmt.Errorf("%w: prompt system instructions contain a reserved skill section", ErrInvalidOrchestratorConfig)
		}
		for _, instruction := range config.PromptConfig.CustomInstructions {
			if containsFrameworkSkillPromptTag(instruction) {
				return fmt.Errorf("%w: custom instructions contain a reserved skill section", ErrInvalidOrchestratorConfig)
			}
		}
	}
	return nil
}

func validateSkillRuntimeLimits(limits SkillRuntimeLimits) error {
	positive := []struct {
		name  string
		value int
	}{
		{"max bindings", limits.MaxBindings},
		{"max auto candidates", limits.MaxAutoCandidates},
		{"catalog token budget", limits.CatalogTokenBudget},
		{"max resource candidates", limits.MaxResourceCandidates},
		{"resource catalog token budget", limits.ResourceCatalogTokenBudget},
		{"max active skills", limits.MaxActiveSkills},
		{"total token budget", limits.TotalTokenBudget},
		{"main token budget", limits.MainTokenBudget},
		{"resource token budget", limits.ResourceTokenBudget},
		{"max resources per phase", limits.MaxResourcesPerPhase},
		{"max resources per execution", limits.MaxResourcesPerExecution},
		{"resolution max tokens", limits.ResolutionMaxTokens},
		{"synthesis token budget", limits.SynthesisTokenBudget},
	}
	for _, limit := range positive {
		if limit.value <= 0 {
			return fmt.Errorf("%w: skills %s must be positive", ErrInvalidOrchestratorConfig, limit.name)
		}
	}
	if limits.RegistryReadTimeout <= 0 {
		return fmt.Errorf("%w: skills registry read timeout must be positive", ErrInvalidOrchestratorConfig)
	}
	if limits.EffectiveInputTokenBudget < 0 {
		return fmt.Errorf("%w: skills effective input token budget must be non-negative", ErrInvalidOrchestratorConfig)
	}
	if limits.MaxAutoCandidates > limits.MaxBindings || limits.MaxActiveSkills > limits.MaxBindings {
		return fmt.Errorf("%w: skills candidate and active limits cannot exceed max bindings", ErrInvalidOrchestratorConfig)
	}
	if limits.MaxResourcesPerPhase > limits.MaxResourcesPerExecution {
		return fmt.Errorf("%w: skills per-phase resources cannot exceed per-execution resources", ErrInvalidOrchestratorConfig)
	}
	if limits.MainTokenBudget > limits.TotalTokenBudget ||
		limits.ResourceTokenBudget > limits.TotalTokenBudget ||
		limits.SynthesisTokenBudget > limits.TotalTokenBudget {
		return fmt.Errorf("%w: skills token sub-budgets cannot exceed the total budget", ErrInvalidOrchestratorConfig)
	}
	return nil
}

func validateSkillRuntimePolicyID(value string) error {
	if value == "" {
		return nil
	}
	if strings.TrimSpace(value) != value || len(value) > maxSkillRuntimePolicyIDBytes ||
		!skillRuntimePolicyIDPattern.MatchString(value) {
		return fmt.Errorf("%w: skills runtime policy ID is invalid", ErrInvalidOrchestratorConfig)
	}
	return nil
}

func validateSkillBindings(skills *SkillConfig) error {
	if len(skills.Bindings) > skills.Limits.MaxBindings {
		return fmt.Errorf("%w: skill bindings exceed the configured maximum", ErrInvalidOrchestratorConfig)
	}
	seen := make(map[SkillRef]struct{}, len(skills.Bindings))
	for index, binding := range skills.Bindings {
		ref := binding.Ref()
		if !validSkillSlug(ref.Namespace, 64) || !validSkillSlug(ref.Name, 64) {
			return fmt.Errorf("%w: skill binding %d has invalid namespace or name", ErrInvalidOrchestratorConfig, index)
		}
		if _, found := seen[ref]; found {
			return fmt.Errorf("%w: skill binding %d duplicates %s", ErrInvalidOrchestratorConfig, index, ref.String())
		}
		seen[ref] = struct{}{}
		if binding.Version != "published" {
			version, err := strconv.ParseUint(binding.Version, 10, 64)
			if err != nil || version == 0 {
				return fmt.Errorf("%w: skill binding %d has invalid version", ErrInvalidOrchestratorConfig, index)
			}
		}
		switch binding.Activation {
		case SkillActivationAlways, SkillActivationAuto, SkillActivationExplicit:
		default:
			return fmt.Errorf("%w: skill binding %d has invalid activation", ErrInvalidOrchestratorConfig, index)
		}
	}
	return nil
}

func validateSkillSelectorOptions(kind string, options *AIOptionsOverride) error {
	if options == nil {
		return nil
	}
	if options.Model != nil && strings.TrimSpace(*options.Model) == "" {
		return fmt.Errorf("%w: skills %s model must not be blank", ErrInvalidOrchestratorConfig, kind)
	}
	if options.ReasoningEffort != nil && strings.TrimSpace(*options.ReasoningEffort) == "" {
		return fmt.Errorf("%w: skills %s reasoning effort must not be blank", ErrInvalidOrchestratorConfig, kind)
	}
	if options.SystemPrompt != nil || options.Temperature != nil || options.MaxTokens != nil ||
		options.ResponseFormat != nil || options.Extra != nil || options.Headers != nil {
		return fmt.Errorf("%w: skills %s options permit only model and reasoning effort", ErrInvalidOrchestratorConfig, kind)
	}
	return nil
}

func validateSkillPromptGuidance(guidance SkillPromptGuidance) error {
	for name, value := range map[string]string{
		"activation": guidance.Activation,
		"resource":   guidance.Resource,
	} {
		if value == "" {
			continue
		}
		if !utf8.ValidString(value) || containsIllegalSkillControl(value) ||
			containsReservedSkillPromptTag(value) {
			return fmt.Errorf("%w: skills %s guidance contains invalid text or a reserved tag", ErrInvalidOrchestratorConfig, name)
		}
		if len(value) > maxSkillGuidanceBytes || canonicalSkillTokenEstimate(value) > maxSkillGuidanceTokens {
			return fmt.Errorf("%w: skills %s guidance exceeds fixed limits", ErrInvalidOrchestratorConfig, name)
		}
	}
	return nil
}

func cloneSkillConfig(value SkillConfig) SkillConfig {
	cloned := value
	if value.Bindings != nil {
		cloned.Bindings = append(make([]SkillBinding, 0, len(value.Bindings)), value.Bindings...)
	}
	return cloned
}
