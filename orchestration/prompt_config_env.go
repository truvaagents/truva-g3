package orchestration

import (
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
	resolver := configEnvResolver{
		mode:   EnvironmentCompatible,
		lookup: os.LookupEnv,
	}
	return applyPromptEnvironment(c, &resolver, true)
}

// MustLoadFromEnv loads configuration from environment variables and panics on error.
// Use this only in main() or init() when startup failure is acceptable.
func (c *PromptConfig) MustLoadFromEnv() {
	if err := c.LoadFromEnv(); err != nil {
		panic(fmt.Sprintf("failed to load prompt config from environment: %v", err))
	}
}
