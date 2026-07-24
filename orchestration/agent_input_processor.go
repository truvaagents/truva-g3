package orchestration

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// AgentInputProcessor transforms a downstream tool's resolved input parameters before dispatch.
// It is the "agent-input" seam (Plan Phase 8) — a DATA-FLOW transform on the resolved param map
// (redact, validate, enrich, or trim), distinct from the prompt-trimming ResultProcessor.
//
// The default is identity (fidelity-first): the downstream tool receives the full upstream output.
// Returning an error ABORTS dispatch — letting a transform (e.g. a PII redactor) fail CLOSED rather
// than ship un-redacted data downstream. A byte-trimmer, by contrast, fails open internally and
// returns a nil error.
type AgentInputProcessor interface {
	ProcessInput(ctx context.Context, params map[string]interface{}, stepCtx ResultProcessorContext) (map[string]interface{}, error)
}

// identityAgentInputProcessor passes parameters through unchanged — the fidelity-first default.
// Tool A's full output reaches tool B unless an operator opts into the byte-budget guard or supplies
// a custom transform.
type identityAgentInputProcessor struct{}

// ProcessInput returns the parameters unchanged.
func (identityAgentInputProcessor) ProcessInput(_ context.Context, params map[string]interface{}, _ ResultProcessorContext) (map[string]interface{}, error) {
	return params, nil
}

// byteBudgetAgentInputProcessor is the opt-in guard (enabled via MaxAgentInputBytes > 0). It
// structurally trims any complex (non-scalar) parameter value whose serialized size exceeds maxBytes,
// using the deterministic StructuralTrimmer with the step instruction as keyword context. It fails
// OPEN in every direction — on any marshal/trim/parse error it keeps the original value, so it never
// corrupts a tool input. Scalars and values within budget pass through untouched.
type byteBudgetAgentInputProcessor struct {
	trimmer  ResultProcessor
	maxBytes int
	logger   core.Logger
}

// NewByteBudgetAgentInputProcessor returns the opt-in byte-budget agent-input guard. A nil trimmer
// or non-positive maxBytes makes ProcessInput a passthrough.
func NewByteBudgetAgentInputProcessor(trimmer ResultProcessor, maxBytes int, logger core.Logger) AgentInputProcessor {
	return &byteBudgetAgentInputProcessor{trimmer: trimmer, maxBytes: maxBytes, logger: logger}
}

// ProcessInput trims oversized complex parameter values back into valid JSON. result.Parameters
// retains the full resolved values upstream; only the dispatched copy is trimmed.
func (p *byteBudgetAgentInputProcessor) ProcessInput(ctx context.Context, params map[string]interface{}, stepCtx ResultProcessorContext) (map[string]interface{}, error) {
	if p.trimmer == nil || p.maxBytes <= 0 {
		return params, nil
	}

	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}

	trimmed := make(map[string]interface{}, len(params))
	for key, val := range params {
		// Only complex values (objects/arrays) are candidates; scalars never need trimming.
		if isScalar(val) {
			trimmed[key] = val
			continue
		}

		serialized, err := json.Marshal(val)
		if err != nil || len(serialized) <= p.maxBytes {
			trimmed[key] = val
			continue
		}

		// Deterministic structural trim using the step instruction as keyword context.
		trimmedJSON := p.trimmer.ProcessForPrompt(ctx, string(serialized), p.maxBytes, stepCtx)

		// Strip the trailing disclosure annotation(s) the trimmer appended — any registered
		// annotationPrefixes form, possibly stacked — so the JSON re-parses.
		cleanJSON := stripResultAnnotation(trimmedJSON)

		// UseNumber so large IDs in the trimmed tool input survive the re-parse verbatim.
		parsed, perr := unmarshalPreservingNumbers([]byte(cleanJSON))
		if perr != nil {
			// Fail open: keep the original value rather than corrupt the tool input.
			trimmed[key] = val
			if p.logger != nil {
				p.logger.WarnWithContext(ctx, "Failed to re-parse trimmed agent input, using original", map[string]interface{}{
					"operation":      "result_trim_agent_input_parse_error",
					"step_id":        stepCtx.StepID,
					"parameter_name": key,
				})
			}
			continue
		}
		trimmed[key] = parsed

		telemetry.AddSpanEvent(ctx, "result_trim.agent_input",
			attribute.String("request_id", requestID),
			attribute.String("step_id", stepCtx.StepID),
			attribute.String("agent_name", stepCtx.AgentName),
			attribute.String("parameter_name", key),
			attribute.Int("original_bytes", len(serialized)),
			attribute.Int("trimmed_bytes", len(cleanJSON)),
			attribute.Int("budget_bytes", p.maxBytes),
		)

		if p.logger != nil {
			p.logger.InfoWithContext(ctx, "Agent input parameter trimmed", map[string]interface{}{
				"operation":      "result_trim_agent_input",
				"request_id":     requestID,
				"step_id":        stepCtx.StepID,
				"agent_name":     stepCtx.AgentName,
				"parameter_name": key,
				"original_bytes": len(serialized),
				"trimmed_bytes":  len(cleanJSON),
				"budget_bytes":   p.maxBytes,
			})
		}

		if registry := core.GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("orchestration.result_trim.agent_input", "agent_name", stepCtx.AgentName)
		}
	}

	return trimmed, nil
}

// stripResultAnnotation removes the trailing trim/disclosure annotations appended by a
// ResultProcessor so trimmed output re-parses as JSON. No annotation present → input returned
// unchanged. Disclosures can legally stack (a structural trim note under the degenerate floor
// note, or the map-reduce combine + partial notes), so ALL trailing annotations are peeled,
// not just the last one. Each iteration peels ONLY the final line, and only when it is
// annotation-shaped — starts with a registered annotationPrefixes form (result_processor.go)
// and ends with "]" — so a cut is never committed before validation: prefix-like text quoted
// mid-body (a log line citing "[trimmed: …]") can never cause content after it to be deleted.
// A newly-added disclosure form must be registered or this re-parse fails and the guard
// fails open with a warn — visible, not silent.
func stripResultAnnotation(s string) string {
	for {
		i := strings.LastIndexByte(s, '\n')
		if i < 0 {
			return s
		}
		line := s[i:]
		if !strings.HasSuffix(line, "]") {
			return s
		}
		registered := false
		for _, pfx := range annotationPrefixes {
			if strings.HasPrefix(line, pfx) {
				registered = true
				break
			}
		}
		if !registered {
			return s
		}
		s = s[:i]
	}
}
