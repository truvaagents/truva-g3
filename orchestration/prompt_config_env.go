package orchestration

import (
	"encoding/json"
	"fmt"
	"os"
)

// LoadFromEnv loads prompt configuration from environment variables.
// This follows the same pattern as ServiceCapabilityConfig and enables
// Kubernetes ConfigMap/Secret-based configuration.
//
// Supported environment variables:
//   - TRUVAG3_PROMPT_TEMPLATE_FILE: Path to template file (ConfigMap mount)
//   - TRUVAG3_PROMPT_DOMAIN: Domain context (healthcare, finance, legal, retail)
//   - TRUVAG3_PROMPT_TYPE_RULES: JSON array of additional type rules
//   - TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS: JSON array of custom instructions
//
// Returns error if JSON parsing fails for structured fields.
func (c *PromptConfig) LoadFromEnv() error {
	// Template file path (typically from ConfigMap mount)
	if path := os.Getenv("TRUVAG3_PROMPT_TEMPLATE_FILE"); path != "" {
		c.TemplateFile = path
	}

	// Domain context
	if domain := os.Getenv("TRUVAG3_PROMPT_DOMAIN"); domain != "" {
		c.Domain = domain
	}

	// Additional type rules from JSON
	if rulesJSON := os.Getenv("TRUVAG3_PROMPT_TYPE_RULES"); rulesJSON != "" {
		var rules []TypeRule
		if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
			return fmt.Errorf("failed to parse TRUVAG3_PROMPT_TYPE_RULES: %w", err)
		}
		// Validate each rule
		for i, rule := range rules {
			if err := ValidateTypeRule(rule); err != nil {
				return fmt.Errorf("invalid type rule at index %d: %w", i, err)
			}
		}
		c.AdditionalTypeRules = append(c.AdditionalTypeRules, rules...)
	}

	// Custom instructions from JSON
	if instructionsJSON := os.Getenv("TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS"); instructionsJSON != "" {
		var instructions []string
		if err := json.Unmarshal([]byte(instructionsJSON), &instructions); err != nil {
			return fmt.Errorf("failed to parse TRUVAG3_PROMPT_CUSTOM_INSTRUCTIONS: %w", err)
		}
		c.CustomInstructions = append(c.CustomInstructions, instructions...)
	}

	return nil
}

// MustLoadFromEnv loads configuration from environment variables and panics on error.
// Use this only in main() or init() when startup failure is acceptable.
func (c *PromptConfig) MustLoadFromEnv() {
	if err := c.LoadFromEnv(); err != nil {
		panic(fmt.Sprintf("failed to load prompt config from environment: %v", err))
	}
}
