package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// errBadJSON marks a request body that is not valid JSON, so handleStructured can log it as a
// decode error (error_type "decode_error") rather than a validation error, matching the
// TOOL_DEVELOPMENT_GUIDE handler contract (§6).
var errBadJSON = errors.New("invalid request format")

// capabilitySpec is the data-driven description of a typed capability (ANALYSIS.md §14): a
// prompt+schema wrapper over the same contained OpenClaw engine that backs run_task. `build`
// parses+validates the request and returns the OpenClaw prompt (plus the per-call timeout and the
// primary-input size for the cap check); `parse` turns the agent's text output into the structured
// response payload. Adding a capability is one spec function + a prompt.
type capabilitySpec struct {
	Name          string
	Description   string
	InputSummary  *core.SchemaSummary
	OutputSummary *core.SchemaSummary
	ToolChoice    string // "" lets the agent use its tools; OpenClaw 500s on "none", so we never set it
	build         func(raw []byte) (prompt string, timeoutSecs, inputChars int, err error)
	parse         func(output string) interface{}
	// postParse, if set, runs after parse with the original request bytes. It exists for
	// capabilities whose final shaping depends on a request option the parse step can't see —
	// e.g. detect_pii's reveal flag deciding whether to mask detected values Go-side. Optional;
	// nil for capabilities whose output is a pure function of the model text.
	postParse func(result interface{}, raw []byte) interface{}
}

// capabilitySpecs assembles the full structured-capability table (ANALYSIS.md §14): the validated
// first batch (this file) plus the fan-out (capabilities_more.go).
func capabilitySpecs() []capabilitySpec {
	specs := []capabilitySpec{
		analyzeDatasetSpec(),
		extractStructuredSpec(),
		reviewCodeSpec(),
	}
	return append(specs, moreCapabilitySpecs()...)
}

// registerStructured turns a capabilitySpec into a registered TruvaG3 capability.
func (t *OpenClawTool) registerStructured(spec capabilitySpec) {
	t.RegisterCapability(core.Capability{
		Name:          spec.Name,
		Description:   spec.Description,
		InputTypes:    []string{"json"},
		OutputTypes:   []string{"json"},
		Handler:       t.handleStructured(spec),
		InputSummary:  spec.InputSummary,
		OutputSummary: spec.OutputSummary,
	})
}

// handleStructured is the generic handler shared by every spec-table capability. It mirrors the
// bespoke handlers (telemetry, validation, transaction, structured response) but is parameterized
// by the spec's build/parse, so a new capability needs no new handler.
func (t *OpenClawTool) handleStructured(spec capabilitySpec) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		op := spec.Name
		requestID := requestIDFrom(ctx, r)

		// Unified tool-call metric for every outcome (flipped to success on the happy path).
		status := "error"
		defer func() {
			telemetry.RecordToolCall("openclaw-tool", op, float64(time.Since(start).Milliseconds()), status)
		}()

		telemetry.SetSpanAttributes(ctx,
			attribute.String("truvag3.tool.name", "openclaw-tool"),
			attribute.String("truvag3.capability", op),
		)
		telemetry.AddSpanEvent(ctx, "request_received",
			attribute.String("method", r.Method),
			attribute.String("path", r.URL.Path),
			attribute.String("operation", op),
		)
		t.Logger.InfoWithContext(ctx, "processing "+op+" request", map[string]interface{}{
			"operation": op, "method": r.Method, "path": r.URL.Path, "request_id": requestID,
		})

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.decodeFail(ctx, w, op, requestID, start, err)
			return
		}

		prompt, timeoutSecs, inputChars, err := spec.build(raw)
		if err != nil {
			// Distinguish a malformed JSON body (decode_error) from a failed field check
			// (validation_error) so telemetry matches the guide's handler contract.
			if errors.Is(err, errBadJSON) {
				t.decodeFail(ctx, w, op, requestID, start, err)
			} else {
				t.validationFail(ctx, w, op, requestID, start, err.Error())
			}
			return
		}
		if inputChars > t.cfg.MaxInputChars {
			t.tooLargeFail(ctx, w, op, requestID, start, inputChars)
			return
		}

		t.Logger.InfoWithContext(ctx, op+" validated", map[string]interface{}{
			"operation": op, "request_id": requestID, "input_chars": inputChars,
		})

		output, ok := t.runTransaction(ctx, w, op, requestID, prompt, spec.ToolChoice, t.resolveTimeout(timeoutSecs))
		if !ok {
			return
		}

		result := spec.parse(output)
		if spec.postParse != nil {
			result = spec.postParse(result, raw)
		}
		status = "success"

		telemetry.AddSpanEvent(ctx, "capability_completed", attribute.String("operation", op))
		t.Logger.InfoWithContext(ctx, op+" completed", map[string]interface{}{
			"operation": op, "request_id": requestID, "status": "success",
			"duration_ms": time.Since(start).Milliseconds(),
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: result})
	}
}

// ---- analyze_dataset ----

func analyzeDatasetSpec() capabilitySpec {
	return capabilitySpec{
		Name: "analyze_dataset",
		Description: "Compute an answer or statistics over a dataset by actually running code in a sandbox, so the numbers are computed, not estimated. " +
			"Provide the dataset inline (CSV, JSON, TSV, or similar) and a question; the agent writes it to a file, analyzes it with shell/script tools, and returns the answer plus the method it used. " +
			"Use for aggregations, rankings, anomaly-finding, and any \"what does the data say?\" question where a guessed number is unacceptable. " +
			"Required: data, question. Optional: format, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "data", Type: "string", Example: "name,score\\nalice,9\\nbob,4\\ncarol,7", Description: "The dataset to analyze, inline (CSV, JSON, TSV, or similar); may be large"},
				{Name: "question", Type: "string", Example: "Who has the highest score and what is the average?", Description: "The question to answer from the data"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "format", Type: "string", Example: "csv", Description: "Hint for the data format (csv, json, tsv, ...)"},
				{Name: "timeout_seconds", Type: "number", Example: "300", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "answer", Type: "string", Description: "The computed answer to the question"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "method", Type: "string", Description: "One-line note of how it was computed (command/approach)"},
				{Name: "result_table", Type: "string", Description: "Small plain-text table of the supporting numbers"},
			},
		},
		build: analyzeDatasetBuild,
		parse: analyzeDatasetParse,
	}
}

// AnalyzeDatasetRequest is the input for the analyze_dataset capability.
type AnalyzeDatasetRequest struct {
	Data        string `json:"data"`
	Question    string `json:"question"`
	Format      string `json:"format,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// AnalyzeDatasetResponse is the output of analyze_dataset.
type AnalyzeDatasetResponse struct {
	Answer      string `json:"answer"`
	Method      string `json:"method,omitempty"`
	ResultTable string `json:"result_table,omitempty"`
}

func analyzeDatasetBuild(raw []byte) (string, int, int, error) {
	var req AnalyzeDatasetRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Data = strings.TrimSpace(req.Data)
	req.Question = strings.TrimSpace(req.Question)
	if req.Data == "" {
		return "", 0, 0, errors.New("data is required")
	}
	if req.Question == "" {
		return "", 0, 0, errors.New("question is required")
	}

	var b strings.Builder
	b.WriteString("You are a data-analysis process in a sandbox with shell and file tools. ")
	b.WriteString("Write the DATASET below to a file in your workspace, then compute the answer to the QUESTION by running code (awk/python/etc.) — do not estimate by eye. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"answer": string, "method": string, "result_table": string}`)
	b.WriteString(" where method is a one-line note of how you computed it and result_table is a small plain-text table of the supporting numbers (use \"\" if not applicable). No text outside the JSON.\n\n")
	b.WriteString("--- QUESTION ---\n")
	b.WriteString(req.Question)
	b.WriteString("\n\n--- DATASET")
	if f := strings.TrimSpace(req.Format); f != "" {
		b.WriteString(" (" + f + ")")
	}
	b.WriteString(" ---\n")
	b.WriteString(req.Data)
	return b.String(), req.TimeoutSecs, len(req.Data), nil
}

func analyzeDatasetParse(output string) interface{} {
	var resp AnalyzeDatasetResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && strings.TrimSpace(resp.Answer) != "" {
		return resp
	}
	return AnalyzeDatasetResponse{Answer: strings.TrimSpace(output)}
}

// ---- extract_structured ----

func extractStructuredSpec() capabilitySpec {
	return capabilitySpec{
		Name: "extract_structured",
		Description: "Extract structured JSON from unstructured text, conforming to a target schema you provide. " +
			"The agent maps the text onto your schema, validates the JSON, and returns the data plus a confidence score and any fields it could not fill. " +
			"Use to turn documents, emails, or records into typed data (invoice to line items, resume to fields, contract to clauses). " +
			"Required: text, schema (the target JSON shape or a JSON-Schema). Optional: instructions, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "text", Type: "string", Example: "Invoice #42 - 3x Widget @ $5, 1x Gizmo @ $20. Total $35.", Description: "The unstructured text to extract from"},
				{Name: "schema", Type: "object", Example: `{"invoice_number":"string","items":[{"name":"string","qty":"number","price":"number"}],"total":"number"}`, Description: "The target JSON shape (or a JSON-Schema) the output must conform to"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "instructions", Type: "string", Example: "Prices are in USD; omit tax.", Description: "Extra guidance for the extraction"},
				{Name: "timeout_seconds", Type: "number", Example: "300", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "data", Type: "object", Description: "The extracted data, conforming to the supplied schema"},
				{Name: "confidence", Type: "number", Description: "Model confidence in the extraction, 0..1"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "unmapped", Type: "array", Description: "Schema fields that could not be filled from the text"},
			},
		},
		build: extractStructuredBuild,
		parse: extractStructuredParse,
	}
}

// ExtractStructuredRequest is the input for the extract_structured capability.
type ExtractStructuredRequest struct {
	Text         string          `json:"text"`
	Schema       json.RawMessage `json:"schema"`
	Instructions string          `json:"instructions,omitempty"`
	TimeoutSecs  int             `json:"timeout_seconds,omitempty"`
}

// ExtractStructuredResponse is the output of extract_structured. Data is the extracted object,
// carried as raw JSON so it conforms to whatever schema the caller supplied.
type ExtractStructuredResponse struct {
	Data       json.RawMessage `json:"data"`
	Confidence float64         `json:"confidence"`
	Unmapped   []string        `json:"unmapped,omitempty"`
}

func extractStructuredBuild(raw []byte) (string, int, int, error) {
	var req ExtractStructuredRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return "", 0, 0, errors.New("text is required")
	}
	schema := strings.TrimSpace(string(req.Schema))
	if schema == "" || schema == "null" {
		return "", 0, 0, errors.New("schema is required")
	}

	var b strings.Builder
	b.WriteString("You are a structured-extraction process. Extract data from the TEXT below that conforms to the target SCHEMA. ")
	if ins := strings.TrimSpace(req.Instructions); ins != "" {
		b.WriteString("Follow these instructions: " + ins + " ")
	}
	b.WriteString("Ensure your output is valid JSON matching the schema's shape. ")
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"data": <object matching the schema>, "confidence": number, "unmapped": [string]}`)
	b.WriteString(" where confidence is 0..1 and unmapped lists schema fields you could not fill from the text. No text outside the JSON.\n\n")
	b.WriteString("--- SCHEMA ---\n")
	b.WriteString(schema)
	b.WriteString("\n\n--- TEXT ---\n")
	b.WriteString(req.Text)
	return b.String(), req.TimeoutSecs, len(req.Text), nil
}

func extractStructuredParse(output string) interface{} {
	js := extractJSON(output)
	var resp ExtractStructuredResponse
	if err := json.Unmarshal([]byte(js), &resp); err == nil && len(resp.Data) > 0 {
		return resp
	}
	// Fallback: the agent returned the data object directly (not wrapped).
	if json.Valid([]byte(js)) {
		return ExtractStructuredResponse{Data: json.RawMessage(js)}
	}
	b, _ := json.Marshal(strings.TrimSpace(output))
	return ExtractStructuredResponse{Data: json.RawMessage(b)}
}

// ---- review_code ----

func reviewCodeSpec() capabilitySpec {
	return capabilitySpec{
		Name: "review_code",
		Description: "Review code (a snippet/file or a unified diff) and return structured findings. " +
			"The agent reads the code, reasons about bugs, security issues, correctness, and concrete improvements (and can run tools when available), and returns a list of findings with severity, location, the issue, evidence, and a suggested fix. " +
			"Doubles as the review engine for a PR-review pipeline. " +
			"Required: one of code or diff. Optional: language, focus, timeout_seconds (default 300).",
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "code", Type: "string", Example: "def divide(a, b):\\n    return a / b", Description: "The code to review (provide this OR diff)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "diff", Type: "string", Example: "@@ -1,3 +1,4 @@ ...", Description: "A unified diff to review (alternative to code)"},
				{Name: "language", Type: "string", Example: "python", Description: "Language hint"},
				{Name: "focus", Type: "string", Example: "security", Description: "Aspect to emphasize (security, performance, ...)"},
				{Name: "timeout_seconds", Type: "number", Example: "300", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "findings", Type: "array", Description: "Findings: {severity, path, line, side, claim, evidence, suggestion, confidence}"},
			},
		},
		build: reviewCodeBuild,
		parse: reviewCodeParse,
	}
}

// ReviewCodeRequest is the input for the review_code capability (provide code OR diff).
type ReviewCodeRequest struct {
	Code        string `json:"code,omitempty"`
	Diff        string `json:"diff,omitempty"`
	Language    string `json:"language,omitempty"`
	Focus       string `json:"focus,omitempty"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// ReviewFinding mirrors the github-pr-review-agent's finding schema so review_code can feed that
// pipeline with zero parser changes (ANALYSIS.md §14 cross-reference).
type ReviewFinding struct {
	Severity   string  `json:"severity"`
	Path       string  `json:"path,omitempty"`
	Line       int     `json:"line,omitempty"`
	Side       string  `json:"side,omitempty"`
	Claim      string  `json:"claim"`
	Evidence   string  `json:"evidence,omitempty"`
	Suggestion string  `json:"suggestion,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// ReviewCodeResponse is the output of review_code.
type ReviewCodeResponse struct {
	Findings []ReviewFinding `json:"findings"`
}

func reviewCodeBuild(raw []byte) (string, int, int, error) {
	var req ReviewCodeRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", 0, 0, errBadJSON
	}
	req.Code = strings.TrimSpace(req.Code)
	req.Diff = strings.TrimSpace(req.Diff)
	if req.Code == "" && req.Diff == "" {
		return "", 0, 0, errors.New("one of code or diff is required")
	}

	isDiff := req.Diff != ""
	var b strings.Builder
	b.WriteString("You are a code-review process. Review the ")
	if isDiff {
		b.WriteString("unified DIFF")
	} else {
		b.WriteString("CODE")
	}
	b.WriteString(" below for bugs, security issues, correctness, and concrete improvements")
	if f := strings.TrimSpace(req.Focus); f != "" {
		b.WriteString(", focusing on " + f)
	}
	b.WriteString(". ")
	if lang := strings.TrimSpace(req.Language); lang != "" {
		b.WriteString("Language: " + lang + ". ")
	}
	b.WriteString("Respond with ONLY a JSON object of the form ")
	b.WriteString(`{"findings": [{"severity": "critical|high|medium|low|info", "path": string, "line": number, "side": "LEFT|RIGHT", "claim": string, "evidence": string, "suggestion": string, "confidence": number}]}`)
	b.WriteString(" where claim states the issue, evidence quotes the relevant code, suggestion is the fix, and confidence is 0..1. Omit path/line/side when not applicable. Return an empty findings array if there are no issues. No text outside the JSON.\n\n")
	if isDiff {
		b.WriteString("--- DIFF ---\n")
		b.WriteString(req.Diff)
	} else {
		b.WriteString("--- CODE ---\n")
		b.WriteString(req.Code)
	}
	return b.String(), req.TimeoutSecs, len(req.Code) + len(req.Diff), nil
}

func reviewCodeParse(output string) interface{} {
	var resp ReviewCodeResponse
	if err := json.Unmarshal([]byte(extractJSON(output)), &resp); err == nil && resp.Findings != nil {
		return resp
	}
	// Fallback: a bare findings array, not wrapped in {"findings": ...}.
	if arr := extractJSONArray(output); arr != "" {
		var findings []ReviewFinding
		if err := json.Unmarshal([]byte(arr), &findings); err == nil {
			return ReviewCodeResponse{Findings: findings}
		}
	}
	return ReviewCodeResponse{Findings: []ReviewFinding{}}
}

// ---- shared helpers ----

// extractJSONArray isolates the outermost [ ... ] array from model output — the bare-array
// fallback for findings-style capabilities, complementing extractJSON's object handling.
func extractJSONArray(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "["); i >= 0 {
		if j := strings.LastIndex(s, "]"); j > i {
			return s[i : j+1]
		}
	}
	return ""
}
