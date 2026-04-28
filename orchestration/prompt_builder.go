package orchestration

import (
	"context"
	"fmt"
)

// PromptBuilder defines the interface for building LLM orchestration prompts.
// This follows the same pattern as CapabilityProvider for consistency.
//
// Implementations can:
// - Extend the default prompt with additional type rules (Layer 1)
// - Use templates for structural customization (Layer 2)
// - Provide complete custom prompt logic (Layer 3)
//
// The interface is intentionally minimal following the
// Minimal Interface Principle from CORE_DESIGN_PRINCIPLES.md.
type PromptBuilder interface {
	// BuildPlanningPrompt creates the prompt for LLM-based orchestration.
	//
	// Parameters:
	//   - ctx: Context for cancellation, tracing, and request-scoped values
	//   - input: All data needed to build the prompt
	//
	// Returns:
	//   - The complete prompt string ready for LLM consumption
	//   - Error if prompt building fails
	//
	// Implementations should:
	//   - Include capability information for agent/tool discovery
	//   - Specify JSON output format requirements
	//   - Include type rules for accurate parameter serialization
	//   - Add domain-specific instructions if applicable
	BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error)
}

// SystemPromptBuilder is an optional extension to PromptBuilder.
// If a PromptBuilder also implements SystemPromptBuilder, the orchestrator
// uses it to construct the system-level message for LLM providers that support
// separate system/user message roles (e.g., Anthropic, OpenAI).
// If not implemented, the orchestrator falls back to SystemInstructions + default role.
// See BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P10.
type SystemPromptBuilder interface {
	BuildSystemPrompt(ctx context.Context, input PromptInput) string
}

// PromptInput contains all data needed to build an orchestration prompt.
// This is passed to PromptBuilder.BuildPlanningPrompt().
type PromptInput struct {
	// CapabilityInfo is the formatted string of available agents and tools.
	// This comes from AgentCatalog.FormatForLLM() or CapabilityProvider.
	// The LLM uses this to understand what capabilities are available.
	CapabilityInfo string

	// Request is the user's natural language request to be orchestrated.
	// Example: "What's the weather in Tokyo and convert 1000 USD to JPY?"
	Request string

	// AgentName is the name of the agent whose orchestrator is generating
	// this plan. Emitted as <agent_identity> in the planning prompt so the
	// LLM knows which agent it belongs to. Used by capabilities like
	// schedule_task where the target_agent should default to self.
	// If empty, the <agent_identity> section is omitted.
	AgentName string

	// Metadata contains optional context for prompt customization.
	// Examples:
	//   - "domain": "healthcare" for HIPAA-aware prompts
	//   - "user_id": "123" for audit logging
	//   - "priority": "high" for SLA-aware routing
	//   - "language": "ja" for localized prompts
	Metadata map[string]interface{}
}

// TypeRule defines how the LLM should handle a specific parameter type.
// This ensures the LLM generates correct JSON types in the execution plan.
type TypeRule struct {
	// TypeNames are the type strings that trigger this rule.
	// Multiple names allow handling type aliases.
	// Examples: ["number", "float64", "float32", "float"]
	TypeNames []string `json:"type_names"`

	// JsonType is the human-readable JSON type description.
	// This is shown to the LLM in the prompt.
	// Example: "JSON numbers"
	JsonType string `json:"json_type"`

	// Example shows a correct value for this type.
	// Example: "35.6897" for numbers
	Example string `json:"example"`

	// AntiPattern shows what NOT to do (optional).
	// This helps the LLM avoid common mistakes.
	// Example: "\"35.6897\"" (string-quoted number)
	AntiPattern string `json:"anti_pattern,omitempty"`

	// Description provides additional context (optional).
	// Example: "Numeric values with decimals, used for coordinates and amounts"
	Description string `json:"description,omitempty"`
}

// PromptConfig configures prompt building behavior.
// This is part of OrchestratorConfig.
type PromptConfig struct {
	// Layer 1: Additional type rules to extend defaults
	// These are appended to DefaultPromptBuilder's built-in rules.
	// Use this to add support for new types without replacing the entire prompt.
	AdditionalTypeRules []TypeRule `json:"additional_type_rules,omitempty"`

	// Layer 2: Template-based customization
	// TemplateFile takes precedence over Template if both are set.

	// TemplateFile is the path to a Go text/template file.
	// In Kubernetes, this is typically a ConfigMap mount path.
	// Example: "/config/planning-prompt.tmpl"
	TemplateFile string `json:"template_file,omitempty"`

	// Template is an inline Go text/template string.
	// Use this for simpler templates that don't need external files.
	Template string `json:"template,omitempty"`

	// CustomInstructions are additional instructions appended to the prompt.
	// These are added after type rules but before the response instruction.
	// Example: ["Always prefer local tools over remote ones", "Minimize API calls"]
	CustomInstructions []string `json:"custom_instructions,omitempty"`

	// Domain provides context for domain-specific prompt adjustments.
	// The DefaultPromptBuilder uses this to add domain-specific instructions.
	// Examples: "healthcare", "finance", "legal", "retail"
	Domain string `json:"domain,omitempty"`

	// IncludeAntiPatterns controls whether to show "what NOT to do" examples.
	// Default: true (recommended for better LLM guidance)
	IncludeAntiPatterns *bool `json:"include_anti_patterns,omitempty"`

	// SystemInstructions defines the orchestrator's core behavioral context.
	// This is prepended to the planning prompt.
	//
	// When set, the developer's persona becomes the primary identity, and
	// the orchestrator role becomes a functional description.
	//
	// Example: "You are a travel planning assistant. Always check weather
	// before recommending outdoor activities. Prefer real-time data sources."
	SystemInstructions string `json:"system_instructions,omitempty"`

	// IterativePlanConfig provides budget information (MaxPhases, MaxTotalSteps)
	// to prompt builders so they can embed budget-aware iterative planning
	// instructions. Populated automatically by NewAIOrchestrator when
	// iterative planning is enabled.
	// See BUG_PHASE3_SKIPPED_EXECUTION.md Issue 3.
	IterativePlanConfig *IterativePlanConfig `json:"iterative_plan_config,omitempty"`
}

// IterativePlanningInstructions is the instruction text that custom PromptBuilder
// implementations should include in their prompts to enable multi-phase planning.
//
// Deprecated: Use BuildIterativePlanningInstructions(config) instead for budget-aware
// instructions. This constant is retained for backward compatibility with existing
// custom PromptBuilder implementations.
const IterativePlanningInstructions = `ITERATIVE PLANNING:
If your plan requires information that is not yet available (e.g., you need to search
or discover something before you can act on the results), you may generate a PARTIAL plan:

1. Set "terminal": false in the plan JSON
2. Include ONLY the steps you can confidently plan right now
3. Add "continuation_note" explaining what data you need from these steps
4. After these steps execute, you will be called again with their actual results
   to plan the remaining steps

If you have all information needed for the complete plan, set "terminal": true
(or omit the field - true is the default).

When to use "terminal": false:
- User asks about "top/famous/popular [things]" but you need to search first
- A step's output determines how many subsequent steps are needed
- Parameters for later steps depend on semantic content of earlier results
  (not just structured field references like {{step-1.response.data.lat}})

When to use "terminal": true (default):
- All entities are explicitly named (weather in Tokyo, stock price of AAPL)
- All parameters are known or expressible as template references to known fields
- The plan structure does not depend on intermediate result content`

// BuildIterativePlanningInstructions returns budget-aware iterative planning instructions.
// Unlike the IterativePlanningInstructions constant, this function embeds the configured
// budget limits (MaxPhases, MaxTotalSteps) into the instructions so the LLM can make
// informed terminal/non-terminal decisions.
// Returns empty string if config is nil or iterative planning is disabled.
// See BUG_PHASE3_SKIPPED_EXECUTION.md Issue 3 (budget visibility) and Issue 4 (phase splits).
func BuildIterativePlanningInstructions(config *IterativePlanConfig) string {
	if config == nil || !config.Enabled {
		return ""
	}
	return fmt.Sprintf(`<iterative_planning>
If your plan requires information not yet available, generate a PARTIAL plan:
- Set "terminal": false and add "continuation_note" explaining what you need.
- Include ONLY steps you can confidently plan now.
- Budget: up to %d phases, %d total steps. Minimize phases — each costs an LLM call.

PHASE SPLIT RULE:
- If a step's parameters are expressible as "{{step-N.response.data.field}}" templates,
  include it in the SAME phase with depends_on listing every referenced step-N — do not split into a new phase.
- Only set terminal: false when the plan STRUCTURE (which steps, how many)
  depends on the semantic CONTENT of a result.

PLAN OPTIMIZATION:
- If all remaining steps can be connected via depends_on chains and template references,
  include them in a SINGLE phase with terminal: true.
- Only set terminal: false if you need to discover entirely new entities from results
  that cannot be anticipated (e.g., a list of IDs you don't know yet).

When to use "terminal": true (default):
- All entities are explicitly named (weather in Tokyo, stock price of AAPL)
- All parameters are known or expressible as template references

CLARIFICATION ESCAPE VALVE:
Use "needs_user_input" when the next step depends on information that only the
user can provide (e.g., travel dates, preferences, choice between options) and
no available tool can produce it. When tools could yield partial progress —
even discovery — plan those tool steps first and ask the user later.

Setting "needs_user_input" ends the current turn:
- Populate "question" with a natural-language question for the user.
- Set "steps" to an empty array for that plan.
- Set "terminal": true.
- Optionally include "missing_fields" (structured field names) and
  "partial_progress" (one-line description of what was already gathered).

Example clarification plan:
{
  "plan_id": "ask-travel-dates-001",
  "original_request": "I want to visit Japan and Korea together due to their proximity",
  "mode": "autonomous",
  "terminal": true,
  "steps": [],
  "needs_user_input": {
    "question": "What dates are you planning to travel, and how many days would you like to spend in each country?",
    "missing_fields": ["travel_dates", "trip_duration"],
    "partial_progress": "Country information and travel advisories for both Japan and South Korea."
  }
}
</iterative_planning>`, config.MaxPhases, config.MaxTotalSteps)
}

// BuildPhaseBudgetSummary returns a concise budget summary for template use.
// Unlike BuildIterativePlanningInstructions (which includes full guidance),
// this provides just the budget numbers for templates that want a brief summary.
func BuildPhaseBudgetSummary(config *IterativePlanConfig) string {
	if config == nil || !config.Enabled {
		return ""
	}
	return fmt.Sprintf("Budget: up to %d phases, %d total steps.", config.MaxPhases, config.MaxTotalSteps)
}

// ValidateTypeRule validates a TypeRule for correctness.
// Returns an error if the rule is invalid.
func ValidateTypeRule(rule TypeRule) error {
	if len(rule.TypeNames) == 0 {
		return &ValidationError{Field: "TypeNames", Message: "must have at least one type name"}
	}
	if rule.JsonType == "" {
		return &ValidationError{Field: "JsonType", Message: "must not be empty"}
	}
	if rule.Example == "" {
		return &ValidationError{Field: "Example", Message: "must not be empty"}
	}
	return nil
}

// ValidationError represents a validation error for prompt builder configuration
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "validation error for " + e.Field + ": " + e.Message
}
