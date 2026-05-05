package orchestration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// buildConcreteExample returns a concrete JSON plan example that demonstrates
// correct template usage, depends_on chains, parallelism, and quoted templates.
// This replaces the verbose stepReferenceInstructions const with a learning-by-example
// approach that works better across LLM providers (Issue 5 P4, Issue 6).
func buildConcreteExample() string {
	return `{
  "plan_id": "example-plan",
  "original_request": "Get weather in Tokyo and convert 100 USD to JPY",
  "mode": "autonomous",
  "terminal": true,
  "steps": [
    {
      "step_id": "step-1",
      "agent_name": "geocoding-tool",
      "instruction": "Get coordinates for Tokyo",
      "depends_on": [],
      "metadata": {
        "capability": "geocode",
        "parameters": { "city": "Tokyo", "country": "Japan" }
      }
    },
    {
      "step_id": "step-2",
      "agent_name": "currency-tool",
      "instruction": "Convert 100 USD to JPY",
      "depends_on": [],
      "metadata": {
        "capability": "convert_currency",
        "parameters": { "from": "USD", "to": "JPY", "amount": 100 }
      }
    },
    {
      "step_id": "step-3",
      "agent_name": "weather-tool",
      "instruction": "Get weather at Tokyo coordinates",
      "depends_on": ["step-1"],
      "metadata": {
        "capability": "get_weather",
        "parameters": {
          "lat": "{{step-1.response.data.lat}}",
          "lon": "{{step-1.response.data.lon}}"
        }
      }
    }
  ]
}`
}

// DefaultPromptBuilder provides comprehensive type rules out of the box.
// It handles all common JSON types and can be extended with additional rules.
//
// This is the default builder used when no custom PromptBuilder is configured.
// It follows the "works with zero configuration" principle.
type DefaultPromptBuilder struct {
	config    *PromptConfig
	typeRules []TypeRule
	logger    core.Logger
	telemetry core.Telemetry
}

// NewDefaultPromptBuilder creates a builder with sensible defaults.
//
// Default type rules cover:
//   - string: JSON strings
//   - number/float64/float32/float: JSON numbers
//   - integer/int/int64/int32: JSON integers
//   - boolean/bool: JSON booleans
//   - array/[]string/[]int/list: JSON arrays
//   - object/map/struct: JSON objects
//
// Additional rules from config.AdditionalTypeRules are appended.
func NewDefaultPromptBuilder(config *PromptConfig) (*DefaultPromptBuilder, error) {
	if config == nil {
		config = &PromptConfig{}
	}

	// Core type rules (always included)
	// These cover the most common parameter types
	defaultRules := []TypeRule{
		{
			TypeNames:   []string{"string"},
			JsonType:    "JSON strings",
			Example:     `"text value"`,
			Description: "Text values should be quoted strings",
		},
		{
			TypeNames:   []string{"number", "float64", "float32", "float"},
			JsonType:    "JSON numbers",
			Example:     `35.6897`,
			AntiPattern: `"35.6897"`,
			Description: "Numeric values with decimals (coordinates, amounts, rates)",
		},
		{
			TypeNames:   []string{"integer", "int", "int64", "int32"},
			JsonType:    "JSON integers",
			Example:     `10`,
			AntiPattern: `"10"`,
			Description: "Whole numbers without decimals (counts, IDs)",
		},
		{
			TypeNames:   []string{"boolean", "bool"},
			JsonType:    "JSON booleans",
			Example:     `true`,
			AntiPattern: `"true"`,
			Description: "Boolean true/false values (flags, toggles)",
		},
		{
			TypeNames:   []string{"array", "[]string", "[]int", "[]float64", "list"},
			JsonType:    "JSON arrays",
			Example:     `["item1", "item2"]`,
			AntiPattern: `"[\"item1\"]"`,
			Description: "Arrays/lists of values (tags, currencies, IDs)",
		},
		{
			TypeNames:   []string{"object", "map", "struct", "map[string]interface{}"},
			JsonType:    "JSON objects",
			Example:     `{"key": "value", "count": 5}`,
			AntiPattern: `"{\"key\": \"value\"}"`,
			Description: "Nested objects with key-value pairs (options, filters)",
		},
	}

	// Validate additional rules from config
	for i, rule := range config.AdditionalTypeRules {
		if err := ValidateTypeRule(rule); err != nil {
			return nil, fmt.Errorf("invalid additional type rule at index %d: %w", i, err)
		}
	}

	// Merge additional rules from config
	allRules := append(defaultRules, config.AdditionalTypeRules...)

	return &DefaultPromptBuilder{
		config:    config,
		typeRules: allRules,
	}, nil
}

// SetLogger sets the logger for debug output (follows framework design principles)
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (d *DefaultPromptBuilder) SetLogger(logger core.Logger) {
	if logger == nil {
		d.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			d.logger = cal.WithComponent("framework/orchestration")
		} else {
			d.logger = logger
		}
	}
}

// SetTelemetry allows dependency injection of telemetry
func (d *DefaultPromptBuilder) SetTelemetry(t core.Telemetry) {
	d.telemetry = t
}

// BuildPlanningPrompt implements PromptBuilder interface.
// Restructured per BUG_PHASE3_SKIPPED_EXECUTION.md Issues 4-6:
//   - XML section tags for clear boundaries (Issue 5 P9)
//   - Instructions at top (U-curve primacy, Issue 5 P1)
//   - Concrete example instead of verbose reference docs (Issue 5 P4)
//   - Budget-aware iterative instructions (Issue 3, 4)
//   - Positive instructions only (Issue 5 P3)
//   - Persona moved to system message (Issue 5 P10, P5)
//   - Format constraint at bottom (Gemini dual-anchor, Issue 6)
func (d *DefaultPromptBuilder) BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error) {
	start := time.Now()
	status := "success"

	// Distributed tracing: Create span if telemetry is available
	var span core.Span
	if d.telemetry != nil {
		_, span = d.telemetry.StartSpan(ctx, SpanPromptBuilderBuild)
		defer span.End()

		span.SetAttribute("builder_type", "default")
		span.SetAttribute("domain", d.config.Domain)
		span.SetAttribute("type_rules_count", len(d.typeRules))
		span.SetAttribute("request_length", len(input.Request))
		span.SetAttribute("has_custom_persona", d.config.SystemInstructions != "")
	}

	// Build sections
	typeRulesSection := d.buildTypeRulesSection()
	customInstructionsSection := d.buildCustomInstructionsSection()
	domainSection := d.buildDomainSection()
	iterativeInstructions := BuildIterativePlanningInstructions(d.config.IterativePlanConfig)

	// Construct the prompt with XML-tagged sections
	var sb strings.Builder

	// Instructions at top (U-curve: critical info at start)
	sb.WriteString("<instructions>\n")
	sb.WriteString("Create a JSON execution plan to fulfill the user's request.\n")
	sb.WriteString("1. Use only agent_name values from the available agents list\n")
	sb.WriteString("2. Match parameter names and types exactly to each capability's schema\n")
	sb.WriteString("3. Order steps by dependencies — a step can only depend on earlier steps\n")
	sb.WriteString("4. Use \"{{step-N.response.data.field}}\" template syntax for cross-step references (always quoted). Only reference fields listed in the capability's Return Fields. Do not guess field names.\n")
	sb.WriteString("5. Declare dependencies in depends_on before referencing a step's data\n")
	sb.WriteString("6. Parallelize independent steps (empty depends_on) for efficiency\n")
	if iterativeInstructions != "" {
		sb.WriteString("\n")
		sb.WriteString(iterativeInstructions)
		sb.WriteString("\n")
	}
	sb.WriteString("</instructions>\n\n")

	// Concrete example (replaces verbose stepReferenceInstructions)
	sb.WriteString("<example>\n")
	sb.WriteString(buildConcreteExample())
	sb.WriteString("\n\nKey rules shown above:\n")
	sb.WriteString("- step-1 and step-2 run in parallel (both have empty depends_on)\n")
	sb.WriteString("- step-3 depends on step-1 and uses template references for its parameters\n")
	sb.WriteString("- Template references are always quoted strings: \"{{step-1.response.data.lat}}\"\n")
	sb.WriteString("- Access specific fields with dot notation: data.lat, data.currency.code\n")
	sb.WriteString("- Template references must use field names from the capability's Return Fields section\n")
	sb.WriteString("</example>\n\n")

	// Type rules
	if typeRulesSection != "" {
		sb.WriteString("<type_rules>\n")
		sb.WriteString(typeRulesSection)
		sb.WriteString("\n</type_rules>\n\n")
	}

	// Domain rules (only if configured)
	if domainSection != "" {
		sb.WriteString("<domain_rules>")
		sb.WriteString(domainSection)
		sb.WriteString("\n</domain_rules>\n\n")
	}

	// Custom instructions (only if configured)
	if customInstructionsSection != "" {
		sb.WriteString("<custom_instructions>")
		sb.WriteString(customInstructionsSection)
		sb.WriteString("\n</custom_instructions>\n\n")
	}

	// Agent identity — tells the LLM which agent it belongs to.
	// Used by schedule_task to default target_agent to self.
	if input.AgentName != "" {
		sb.WriteString("<agent_identity>\n")
		sb.WriteString("You are the orchestrator for agent: " + input.AgentName + "\n")
		sb.WriteString("When scheduling tasks (schedule_task capability), set target_agent to \"" + input.AgentName + "\" unless the user explicitly names a different agent.\n")
		sb.WriteString("</agent_identity>\n\n")
	}

	// Available agents
	sb.WriteString("<available_agents>\n")
	sb.WriteString(input.CapabilityInfo)
	sb.WriteString("\n</available_agents>\n\n")

	// Agent coordination from pipeline enrichments (real-time activity signals)
	if input.Metadata != nil {
		if coordCtx, ok := input.Metadata[core.EnrichmentActivityCoordination]; ok {
			if coordStr, isStr := coordCtx.(string); isStr && coordStr != "" {
				sb.WriteString("<agent_coordination>\n")
				sb.WriteString(coordStr)
				sb.WriteString("\n</agent_coordination>\n\n")
			}
		}
	}

	// Pipeline enrichments: user profile, agent memory, conversation history.
	// Order: user_profile (most stable) → agent_memory (domain context) → conversation_history (session context).
	if input.Metadata != nil {
		// User profile from UserMemoryEnrichmentHook (per-user private facts)
		if userProfile, ok := input.Metadata[core.EnrichmentUserProfile]; ok {
			if profileStr, isStr := userProfile.(string); isStr && profileStr != "" {
				sb.WriteString(profileStr)
				sb.WriteString("\n\n")
			}
		}
		if ragCtx, ok := input.Metadata[core.EnrichmentRAGContext]; ok {
			if ragStr, isStr := ragCtx.(string); isStr && ragStr != "" {
				sb.WriteString("<agent_memory>\n")
				sb.WriteString(ragStr)
				sb.WriteString("\n</agent_memory>\n\n")
			}
		}
		if convHistory, ok := input.Metadata[core.EnrichmentConversationHistory]; ok {
			if convStr, isStr := convHistory.(string); isStr && convStr != "" {
				sb.WriteString("<conversation_history>\n")
				sb.WriteString(convStr)
				sb.WriteString("\n</conversation_history>\n\n")
			}
		}
	}

	// Precedence rule: placed immediately before <user_request> so it lands
	// in the high-attention tail of the prompt (docs/building/EFFECTIVE_PROMPTS_GUIDE.md
	// §2.1). Emitted only when at least one enrichment block that can conflict
	// with the live request is present — otherwise the rule is noise.
	writeContextPrecedence(ctx, &sb, input.Metadata, PromptKindPlanning)

	// User request
	sb.WriteString("<user_request>\n")
	sb.WriteString(input.Request)
	sb.WriteString("\n</user_request>\n\n")

	// Format constraint at bottom (Gemini dual-anchor: critical constraint at end)
	sb.WriteString("Return a JSON execution plan. Output raw JSON — no markdown, no code blocks. Start with { and end with }.")

	prompt := sb.String()

	// Calculate duration
	durationMs := float64(time.Since(start).Milliseconds())

	// Emit metrics only if telemetry is available (fail-safe pattern)
	if d.telemetry != nil {
		d.telemetry.RecordMetric("orchestrator.prompt.build_duration_ms", durationMs,
			map[string]string{
				"builder_type": "default",
				"domain":       d.config.Domain,
				"status":       status,
			})

		d.telemetry.RecordMetric("orchestrator.prompt.built", 1,
			map[string]string{
				"builder_type": "default",
				"domain":       d.config.Domain,
				"status":       status,
			})

		d.telemetry.RecordMetric("orchestrator.prompt.size_bytes", float64(len(prompt)),
			map[string]string{
				"builder_type": "default",
			})

		if span != nil {
			span.SetAttribute("prompt_length", len(prompt))
			span.SetAttribute("duration_ms", durationMs)
		}
	}

	// Structured logging (logger may also be nil - fail-safe)
	if d.logger != nil {
		d.logger.DebugWithContext(ctx, "Built planning prompt", map[string]interface{}{
			"builder_type":        "default",
			"type_rules_count":    len(d.typeRules),
			"custom_instructions": len(d.config.CustomInstructions),
			"domain":              d.config.Domain,
			"prompt_length":       len(prompt),
			"duration_ms":         durationMs,
		})
	}

	return prompt, nil
}

// buildTypeRulesSection generates the type rules text for the prompt.
// Simplified per BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P3: positive instructions only,
// anti-patterns removed to avoid Pink Elephant effect (~80 tokens vs ~200).
func (d *DefaultPromptBuilder) buildTypeRulesSection() string {
	var rules []string

	for _, rule := range d.typeRules {
		typeNames := strings.Join(rule.TypeNames, `" or "`)
		rules = append(rules, fmt.Sprintf(`- "%s" → %s (e.g., %s)`,
			typeNames, rule.JsonType, rule.Example))
	}

	return strings.Join(rules, "\n")
}

// buildCustomInstructionsSection generates custom instructions text.
// Numbering continues from the 6 default rules in the <instructions> section.
func (d *DefaultPromptBuilder) buildCustomInstructionsSection() string {
	if len(d.config.CustomInstructions) == 0 {
		return ""
	}

	var instructions []string
	for i, inst := range d.config.CustomInstructions {
		instructions = append(instructions, fmt.Sprintf("%d. %s", i+7, inst))
	}

	return "\n" + strings.Join(instructions, "\n")
}

// buildDomainSection generates domain-specific instructions
func (d *DefaultPromptBuilder) buildDomainSection() string {
	switch d.config.Domain {
	case "healthcare":
		return `
HEALTHCARE DOMAIN REQUIREMENTS:
- Never include PHI (Protected Health Information) in logs
- Prefer HIPAA-compliant tools when available
- Include audit trail metadata in all steps`

	case "finance":
		return `
FINANCE DOMAIN REQUIREMENTS:
- All monetary calculations must preserve decimal precision
- Include transaction IDs for audit compliance
- Prefer SOX-compliant tools when available`

	case "legal":
		return `
LEGAL DOMAIN REQUIREMENTS:
- Maintain chain of custody for all data transformations
- Include timestamp and source attribution in all steps
- Flag any steps that modify original documents`

	default:
		return ""
	}
}

// defaultOrchestratorPersona is the framework's fallback persona text. Lifted to
// a package-level constant so every system-prompt fallback path renders the same
// string, and so tests can assert against a stable token.
const defaultOrchestratorPersona = "You are an intelligent orchestrator that creates execution plans for multi-agent systems."

// appendRuntimeContext wraps a persona string with a <runtime_context> block
// carrying the current UTC date.
//
// ORCH-020 RC7: Every framework-built system prompt must carry runtime context
// so the planner can resolve relative date language ("today", "tomorrow", "next
// week") without inventing {{today_plus_1}}-style macros. Centralised so the
// four fallback paths (DefaultPromptBuilder, TemplatePromptBuilder nil-fallback,
// AIOrchestrator.buildSystemPrompt SystemInstructions branch, AIOrchestrator
// final-default branch) all emit identical content. Custom PromptBuilders that
// implement SystemPromptBuilder may opt in by calling this helper too.
//
// The tag is registered in docs/building/EFFECTIVE_PROMPTS_GUIDE.md §9.1.
func appendRuntimeContext(persona string) string {
	var sb strings.Builder
	sb.WriteString(persona)
	sb.WriteString("\n\n<runtime_context>\n")
	fmt.Fprintf(&sb, "Current date (UTC): %s. Resolve relative dates (today, tomorrow, next week, etc.) against this value.\n",
		time.Now().UTC().Format("2006-01-02"))
	sb.WriteString("</runtime_context>")
	return sb.String()
}

// BuildSystemPrompt implements SystemPromptBuilder interface.
// Per BUG_PHASE3_SKIPPED_EXECUTION.md Issue 5 P10, P5: persona belongs in the
// system-level message (higher priority across all providers), not in the user prompt.
// This replaces the deleted buildPersonaSection method.
//
// ORCH-020 RC7: delegates the runtime-context tail to appendRuntimeContext so
// the same block is emitted by every fallback path in the framework.
func (d *DefaultPromptBuilder) BuildSystemPrompt(ctx context.Context, input PromptInput) string {
	source := "default"
	persona := defaultOrchestratorPersona

	if d.config.SystemInstructions != "" {
		source = "system_instructions"
		persona = d.config.SystemInstructions +
			"\n\nAs an AI orchestrator, you manage a multi-agent system to fulfill user requests."
	}

	prompt := appendRuntimeContext(persona)

	// Track system prompt source (relocates has_custom_persona tracking)
	if d.telemetry != nil {
		d.telemetry.RecordMetric("orchestrator.prompt.system_prompt_built", 1,
			map[string]string{"source": source})
	}

	return prompt
}

// GetTypeRules returns the current type rules (useful for debugging)
func (d *DefaultPromptBuilder) GetTypeRules() []TypeRule {
	return d.typeRules
}

// GetConfig returns the current configuration (useful for debugging)
func (d *DefaultPromptBuilder) GetConfig() *PromptConfig {
	return d.config
}

// contextPrecedenceDirective is the conflict-resolution rule shared by every
// framework prompt that injects <user_profile> or <conversation_history>
// alongside the live user request. Centralized so the wording stays
// consistent across initial planning, continuation planning, the legacy
// inline fallback, and synthesis.
const contextPrecedenceDirective = `When <user_profile>, <conversation_history>, and <user_request> disagree about a named entity — a place, person, date, quantity, or identifier — trust the live turn: the current <user_request> first, then the most recent <conversation_history> turn. Treat <user_profile> "Context" entries as hints that may be stale; recency labels reflect when they were last recorded.`

// Canonical promptKind labels emitted on orchestrator.context_precedence.*
// telemetry. Kept short so dashboards stay readable; stable so downstream
// queries don't break when new call sites are added.
const (
	PromptKindPlanning              = "planning"               // DefaultPromptBuilder initial plan
	PromptKindPlanningFallback      = "planning_fallback"      // inline hardcoded prompt (PromptBuilder nil/errored)
	PromptKindContinuation          = "continuation"           // buildContinuationPrompt (phase 2+)
	PromptKindSynthesis             = "synthesis"              // AISynthesizer.buildSynthesisPrompt
	PromptKindSynthesisOrchestrator = "synthesis_orchestrator" // AIOrchestrator.buildSynthesisPrompt (legacy path)
)

// writeContextPrecedence emits the <context_precedence> block when at least
// one enrichment that can conflict with the live request is present. It is
// idempotent and emits at most once per call. Callers must invoke this from
// every planning- or synthesis-style prompt that injects <user_profile> or
// <conversation_history> — otherwise the planner/synthesizer can anchor on
// stale stored context.
//
// Placement guidance per docs/building/EFFECTIVE_PROMPTS_GUIDE.md §2.1: emit the
// block close to either the live <user_request> or the conflicting
// enrichment(s) so it lands in the high-attention region of the prompt.
//
// Observability: every call emits
//
//	telemetry.Counter("orchestrator.context_precedence.evaluated",
//	    "prompt_kind", promptKind, "emitted", "true"|"false")
//
// so the denominator (how often the code path ran) AND emission rate are
// dashboardable. A span event "orchestrator.context_precedence.emitted" is
// attached to the current span only on the emit path, keeping Jaeger clean.
func writeContextPrecedence(ctx context.Context, sb *strings.Builder, enrichments map[string]interface{}, promptKind string) {
	emitted := false
	defer func() {
		emittedLabel := "false"
		if emitted {
			emittedLabel = "true"
		}
		telemetry.Counter("orchestrator.context_precedence.evaluated",
			"prompt_kind", promptKind,
			"emitted", emittedLabel,
		)
	}()

	if len(enrichments) == 0 {
		return
	}
	_, hasProfile := enrichments[core.EnrichmentUserProfile]
	_, hasHistory := enrichments[core.EnrichmentConversationHistory]
	if !hasProfile && !hasHistory {
		return
	}

	sb.WriteString("<context_precedence>\n")
	sb.WriteString(contextPrecedenceDirective)
	sb.WriteString("\n</context_precedence>\n\n")
	emitted = true

	telemetry.AddSpanEvent(ctx, "orchestrator.context_precedence.emitted",
		attribute.String("prompt_kind", promptKind),
		attribute.Bool("has_profile", hasProfile),
		attribute.Bool("has_history", hasHistory),
	)
}
