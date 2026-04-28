package orchestration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TemplatePromptBuilder uses Go text/template for prompt customization.
// This enables structural changes to the prompt without code modification.
//
// SECURITY: Templates must come from trusted sources only (admin-managed ConfigMaps
// or compiled-in config). Do NOT accept templates from user input. While TemplateData
// only exposes safe types (strings, maps), Go templates can cause DoS via infinite
// loops or resource exhaustion if crafted maliciously.
//
// Templates have access to:
//   - .CapabilityInfo: Formatted capabilities from catalog
//   - .Request: User's natural language request
//   - .TypeRules: Formatted type rules string
//   - .CustomInstructions: Additional instructions
//   - .Domain: Domain context
//   - .Metadata: Request metadata map
//   - .ConcreteExample: Concrete few-shot JSON plan example
type TemplatePromptBuilder struct {
	template  *template.Template
	config    *PromptConfig
	fallback  *DefaultPromptBuilder
	logger    core.Logger
	telemetry core.Telemetry
}

// TemplateData contains all data available to the template.
// Updated per BUG_PHASE3_SKIPPED_EXECUTION.md Changes 5A:
//   - ConcreteExample replaces JSONStructure (Issue 5 P4)
//   - PhaseBudget provides budget-aware info (Issue 3)
//   - SystemInstructions for template-level persona access
type TemplateData struct {
	CapabilityInfo                string
	Request                       string
	TypeRules                     string
	CustomInstructions            string
	Domain                        string
	Metadata                      map[string]interface{}
	ConcreteExample               string // replaces JSONStructure
	IterativePlanningInstructions string
	PhaseBudget                   string
	SystemInstructions            string
}

// defaultJSONStructure removed per BUG_PHASE3_SKIPPED_EXECUTION.md Change 5C.
// Replaced by buildConcreteExample() which provides a realistic few-shot example
// instead of a generic template with placeholder values (Issue 5 P4).

// NewTemplatePromptBuilder creates a template-based builder.
// It loads the template from file or inline string.
// Falls back to DefaultPromptBuilder on template errors.
func NewTemplatePromptBuilder(config *PromptConfig) (*TemplatePromptBuilder, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required for TemplatePromptBuilder")
	}

	var tmpl *template.Template
	var err error

	// Load template from file or inline
	if config.TemplateFile != "" {
		// Load from file (Kubernetes ConfigMap mount)
		content, err := os.ReadFile(config.TemplateFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read template file %q: %w", config.TemplateFile, err)
		}
		tmpl, err = template.New("planning").Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("failed to parse template from file %q: %w", config.TemplateFile, err)
		}
	} else if config.Template != "" {
		// Use inline template
		tmpl, err = template.New("planning").Parse(config.Template)
		if err != nil {
			return nil, fmt.Errorf("failed to parse inline template: %w", err)
		}
	} else {
		return nil, fmt.Errorf("either TemplateFile or Template must be set")
	}

	// Create fallback builder
	fallback, err := NewDefaultPromptBuilder(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create fallback builder: %w", err)
	}

	return &TemplatePromptBuilder{
		template: tmpl,
		config:   config,
		fallback: fallback,
	}, nil
}

// SetLogger sets the logger for debug output (follows framework design principles)
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (t *TemplatePromptBuilder) SetLogger(logger core.Logger) {
	if logger == nil {
		t.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			t.logger = cal.WithComponent("framework/orchestration")
		} else {
			t.logger = logger
		}
	}
	// Propagate to fallback (it will apply its own WithComponent)
	if t.fallback != nil {
		t.fallback.SetLogger(logger)
	}
}

// SetTelemetry allows dependency injection of telemetry
func (t *TemplatePromptBuilder) SetTelemetry(tel core.Telemetry) {
	t.telemetry = tel
	if t.fallback != nil {
		t.fallback.SetTelemetry(tel)
	}
}

// BuildPlanningPrompt implements PromptBuilder using the template
func (t *TemplatePromptBuilder) BuildPlanningPrompt(ctx context.Context, input PromptInput) (string, error) {
	start := time.Now()

	// Distributed tracing - only if telemetry is available
	var span core.Span
	if t.telemetry != nil {
		ctx, span = t.telemetry.StartSpan(ctx, SpanPromptBuilderBuild)
		defer span.End()

		span.SetAttribute("builder_type", "template")
		span.SetAttribute("template_file", t.config.TemplateFile)
	}

	// Prepare template data (updated per BUG_PHASE3_SKIPPED_EXECUTION.md Change 5B)
	data := TemplateData{
		CapabilityInfo:                input.CapabilityInfo,
		Request:                       input.Request,
		TypeRules:                     t.fallback.buildTypeRulesSection(),
		CustomInstructions:            t.fallback.buildCustomInstructionsSection(),
		Domain:                        t.config.Domain,
		Metadata:                      input.Metadata,
		ConcreteExample:               buildConcreteExample(),
		IterativePlanningInstructions: BuildIterativePlanningInstructions(t.config.IterativePlanConfig),
		PhaseBudget:                   BuildPhaseBudgetSummary(t.config.IterativePlanConfig),
		SystemInstructions:            t.config.SystemInstructions,
	}

	// Execute template
	var buf bytes.Buffer
	if err := t.template.Execute(&buf, data); err != nil {
		// Track fallback usage if telemetry is available
		if t.telemetry != nil {
			t.telemetry.RecordMetric("orchestrator.prompt.template_fallback", 1,
				map[string]string{"reason": "execution_error"})

			if span != nil {
				span.SetAttribute("fallback_used", true)
				span.SetAttribute("fallback_reason", "execution_error")
				span.RecordError(err)
			}
		}

		if t.logger != nil {
			t.logger.WarnWithContext(ctx, "Template execution failed, using fallback", map[string]interface{}{
				"error":         err.Error(),
				"template_file": t.config.TemplateFile,
			})
		}

		// Graceful degradation: fall back to default builder
		// The fallback will emit its own metrics
		return t.fallback.BuildPlanningPrompt(ctx, input)
	}

	prompt := buf.String()
	durationMs := float64(time.Since(start).Milliseconds())

	// Emit success metrics only if telemetry is available
	if t.telemetry != nil {
		t.telemetry.RecordMetric("orchestrator.prompt.build_duration_ms", durationMs,
			map[string]string{
				"builder_type": "template",
				"domain":       t.config.Domain,
				"status":       "success",
			})

		t.telemetry.RecordMetric("orchestrator.prompt.built", 1,
			map[string]string{
				"builder_type": "template",
				"domain":       t.config.Domain,
				"status":       "success",
			})

		t.telemetry.RecordMetric("orchestrator.prompt.size_bytes", float64(len(prompt)),
			map[string]string{"builder_type": "template"})

		if span != nil {
			span.SetAttribute("prompt_length", len(prompt))
			span.SetAttribute("duration_ms", durationMs)
		}
	}

	if t.logger != nil {
		t.logger.DebugWithContext(ctx, "Built planning prompt from template", map[string]interface{}{
			"builder_type":  "template",
			"template_file": t.config.TemplateFile,
			"domain":        t.config.Domain,
			"prompt_length": len(prompt),
			"duration_ms":   durationMs,
		})
	}

	return prompt, nil
}

// BuildSystemPrompt implements SystemPromptBuilder by delegating to the fallback
// DefaultPromptBuilder. Per BUG_PHASE3_SKIPPED_EXECUTION.md Change 5D.
//
// ORCH-020 RC7: when no fallback is wired, the nil-safe default still emits the
// <runtime_context> block via appendRuntimeContext so the planner receives
// today's date along this path too.
func (t *TemplatePromptBuilder) BuildSystemPrompt(ctx context.Context, input PromptInput) string {
	if t.fallback != nil {
		return t.fallback.BuildSystemPrompt(ctx, input)
	}
	// Nil-safe default (FRAMEWORK_DESIGN_PRINCIPLES.md §Fail-Safe Defaults)
	return appendRuntimeContext(defaultOrchestratorPersona)
}

// GetFallback returns the fallback builder (useful for testing)
func (t *TemplatePromptBuilder) GetFallback() *DefaultPromptBuilder {
	return t.fallback
}
