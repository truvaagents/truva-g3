package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// handleSummarizeText implements the summarize_text capability.
func (t *OpenClawTool) handleSummarizeText(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	const op = "summarize_text"
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
	t.Logger.InfoWithContext(ctx, "processing summarize_text request", map[string]interface{}{
		"operation":  op,
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": requestID,
	})

	var req SummarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.decodeFail(ctx, w, op, requestID, start, err)
		return
	}

	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		t.validationFail(ctx, w, op, requestID, start, "text is required")
		return
	}
	if len(req.Text) > t.cfg.MaxInputChars {
		t.tooLargeFail(ctx, w, op, requestID, start, len(req.Text))
		return
	}

	style := normalizeStyle(req.Style)
	prompt := buildSummarizePrompt(req.Text, style, req.Focus, req.TargetWords)

	t.Logger.InfoWithContext(ctx, "summarize_text validated", map[string]interface{}{
		"operation":   op,
		"request_id":  requestID,
		"input_chars": len(req.Text),
		"style":       style,
	})

	output, ok := t.runTransaction(ctx, w, op, requestID, prompt, "", t.resolveTimeout(req.TimeoutSecs))
	if !ok {
		return
	}

	result := SummarizeResponse{
		Summary:    output,
		InputChars: len(req.Text),
		Style:      style,
		Truncated:  false,
	}
	status = "success"

	telemetry.AddSpanEvent(ctx, "summary_completed",
		attribute.String("operation", op),
		attribute.Int("input_chars", len(req.Text)),
	)
	t.Logger.InfoWithContext(ctx, "summarize_text completed", map[string]interface{}{
		"operation":   op,
		"request_id":  requestID,
		"input_chars": len(req.Text),
		"status":      "success",
		"duration_ms": time.Since(start).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: result})
}

// handleAnswerOverText implements the answer_over_text capability.
func (t *OpenClawTool) handleAnswerOverText(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	const op = "answer_over_text"
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
	t.Logger.InfoWithContext(ctx, "processing answer_over_text request", map[string]interface{}{
		"operation":  op,
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": requestID,
	})

	var req AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.decodeFail(ctx, w, op, requestID, start, err)
		return
	}

	req.Text = strings.TrimSpace(req.Text)
	req.Question = strings.TrimSpace(req.Question)
	if req.Text == "" {
		t.validationFail(ctx, w, op, requestID, start, "text is required")
		return
	}
	if req.Question == "" {
		t.validationFail(ctx, w, op, requestID, start, "question is required")
		return
	}
	if len(req.Text) > t.cfg.MaxInputChars {
		t.tooLargeFail(ctx, w, op, requestID, start, len(req.Text))
		return
	}

	prompt := buildAnswerPrompt(req.Text, req.Question)

	t.Logger.InfoWithContext(ctx, "answer_over_text validated", map[string]interface{}{
		"operation":   op,
		"request_id":  requestID,
		"input_chars": len(req.Text),
	})

	output, ok := t.runTransaction(ctx, w, op, requestID, prompt, "", t.resolveTimeout(req.TimeoutSecs))
	if !ok {
		return
	}

	result := parseAnswer(output)
	status = "success"

	telemetry.AddSpanEvent(ctx, "answer_completed",
		attribute.String("operation", op),
		attribute.Bool("found", result.Found),
	)
	t.Logger.InfoWithContext(ctx, "answer_over_text completed", map[string]interface{}{
		"operation":   op,
		"request_id":  requestID,
		"input_chars": len(req.Text),
		"found":       result.Found,
		"status":      "success",
		"duration_ms": time.Since(start).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: result})
}

// handleRunTask implements the run_task capability — the autonomous mode (§13). The agent
// solves the task with its own tools; we pass an empty tool_choice so tools are allowed.
func (t *OpenClawTool) handleRunTask(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	const op = "run_task"
	requestID := requestIDFrom(ctx, r)

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
	t.Logger.InfoWithContext(ctx, "processing run_task request", map[string]interface{}{
		"operation":  op,
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": requestID,
	})

	var req RunTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.decodeFail(ctx, w, op, requestID, start, err)
		return
	}
	req.Task = strings.TrimSpace(req.Task)
	if req.Task == "" {
		t.validationFail(ctx, w, op, requestID, start, "task is required")
		return
	}
	if len(req.Task) > t.cfg.MaxInputChars {
		t.tooLargeFail(ctx, w, op, requestID, start, len(req.Task))
		return
	}

	t.Logger.InfoWithContext(ctx, "run_task validated", map[string]interface{}{
		"operation": op, "request_id": requestID, "task_chars": len(req.Task),
	})

	// Empty tool_choice → the agent is free to use its tools (exec/fs).
	output, ok := t.runTransaction(ctx, w, op, requestID, req.Task, "", t.resolveTimeout(req.TimeoutSecs))
	if !ok {
		return
	}

	result := RunTaskResponse{Result: output}
	status = "success"

	telemetry.AddSpanEvent(ctx, "task_completed", attribute.String("operation", op))
	t.Logger.InfoWithContext(ctx, "run_task completed", map[string]interface{}{
		"operation": op, "request_id": requestID,
		"status": "success", "duration_ms": time.Since(start).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: result})
}

// runTransaction is the shared OpenClaw transaction runner: serialize (sem of 1, bounded
// wait) -> reset workspace -> write input -> POST with a transaction deadline -> map
// errors. On any failure it writes the error response and returns ok=false (ANALYSIS.md §4,
// §7, §8).
func (t *OpenClawTool) runTransaction(ctx context.Context, w http.ResponseWriter, op, requestID, prompt, toolChoice string, timeout time.Duration) (string, bool) {
	// 1. Acquire the semaphore with a bounded wait — fail fast rather than queue unboundedly.
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
	case <-time.After(t.cfg.SemAcquireTimeout):
		err := errors.New("no free OpenClaw slot")
		telemetry.RecordSpanError(ctx, err)
		t.Logger.WarnWithContext(ctx, "OpenClaw busy", map[string]interface{}{
			"operation": op, "error": err.Error(), "error_type": "busy", "request_id": requestID, "status": "failure",
		})
		t.sendError(w, "OpenClaw is busy; retry shortly", http.StatusServiceUnavailable, ErrCodeBusy)
		return "", false
	case <-ctx.Done():
		return "", false // client disconnected before we got a slot; nothing to write
	}

	// 2. Transaction-bounded call to OpenClaw (task/prompt inline; the agent uses its tools).
	// Statelessness comes from a fresh `user` per call + memory plugin "none". We do NOT clear
	// the workspace between tasks: that dir holds OpenClaw's live agent/session state, and
	// deleting it mid-run corrupts the gateway (500 on the next request). Per-task *file*
	// isolation is a follow-up via OpenClaw's own sandbox (§13).
	txCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	telemetry.AddSpanEvent(ctx, "calling_openclaw",
		attribute.String("operation", op),
		attribute.String("request_id", requestID),
	)
	apiStart := time.Now()
	output, err := t.client.RunResponses(txCtx, t.cfg.AgentID, prompt, newSessionID(), toolChoice)
	apiLatency := time.Since(apiStart)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		// A transaction timeout is a local deadline, not an upstream HTTP status -> 504 retryable.
		if errors.Is(err, context.DeadlineExceeded) || txCtx.Err() == context.DeadlineExceeded {
			t.Logger.WarnWithContext(ctx, "OpenClaw task timed out", map[string]interface{}{
				"operation": op, "error": err.Error(), "error_type": "timeout", "request_id": requestID,
				"api_latency": apiLatency.String(), "status": "failure",
			})
			t.sendError(w, "OpenClaw task timed out", http.StatusGatewayTimeout, ErrCodeTimeout)
			return "", false
		}
		t.Logger.ErrorWithContext(ctx, "OpenClaw task failed", map[string]interface{}{
			"operation": op, "error": err.Error(), "error_type": "api_error",
			"request_id": requestID, "api_latency": apiLatency.String(), "status": "failure",
		})
		t.sendUpstreamError(w, fmt.Sprintf("OpenClaw task failed: %v", err), core.ClassifyUpstreamError(err))
		return "", false
	}

	t.Logger.InfoWithContext(ctx, "OpenClaw task ok", map[string]interface{}{
		"operation": op, "request_id": requestID, "api_latency": apiLatency.String(),
	})
	return output, true
}

// resolveTimeout clamps a caller-supplied timeout to [1s, MaxTimeout], defaulting when unset.
func (t *OpenClawTool) resolveTimeout(reqSecs int) time.Duration {
	if reqSecs <= 0 {
		return t.cfg.DefaultTimeout
	}
	d := time.Duration(reqSecs) * time.Second
	if d > t.cfg.MaxTimeout {
		return t.cfg.MaxTimeout
	}
	return d
}

// ---- prompt construction (the adapter's seam — never exposed to the orchestrator) ----

func normalizeStyle(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "bullets", "bullet", "bullet_points":
		return "bullets"
	case "detailed":
		return "detailed"
	case "tldr":
		return "tldr"
	default:
		return "executive"
	}
}

func buildSummarizePrompt(text, style, focus string, targetWords int) string {
	var b strings.Builder
	b.WriteString("You are a summarization process. Produce a single ")
	b.WriteString(style)
	b.WriteString(" summary of the document below")
	if f := strings.TrimSpace(focus); f != "" {
		b.WriteString(", focused on ")
		b.WriteString(f)
	}
	if targetWords > 0 {
		fmt.Fprintf(&b, ", about %d words", targetWords)
	}
	b.WriteString(". Do not use any tools. Output ONLY the final summary text, with no preamble.\n\n--- DOCUMENT ---\n")
	b.WriteString(text)
	return b.String()
}

func buildAnswerPrompt(text, question string) string {
	return "You are a question-answering process. Answer the question strictly from the document below. " +
		"Do not use any tools. Respond with ONLY a JSON object of the form " +
		"{\"answer\": string, \"found\": boolean, \"supporting_excerpts\": [string]} where found is true only if the answer is supported by the document. " +
		"No text outside the JSON.\n\n--- QUESTION ---\n" + question + "\n\n--- DOCUMENT ---\n" + text
}

// parseAnswer extracts the structured answer from OpenClaw's output, falling back to
// treating the whole output as the answer if it isn't valid JSON.
func parseAnswer(raw string) AnswerResponse {
	var a AnswerResponse
	if err := json.Unmarshal([]byte(extractJSON(raw)), &a); err == nil && strings.TrimSpace(a.Answer) != "" {
		return a
	}
	trimmed := strings.TrimSpace(raw)
	return AnswerResponse{Answer: trimmed, Found: trimmed != ""}
}

// extractJSON strips common code-fence wrapping and isolates the outermost { ... } object.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			return s[i : j+1]
		}
	}
	return s
}

// ---- request-id + error helpers ----

func requestIDFrom(ctx context.Context, r *http.Request) string {
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		if id := bag["request_id"]; id != "" {
			return id
		}
	}
	return r.Header.Get("X-TruvaG3-Request-ID")
}

func (t *OpenClawTool) decodeFail(ctx context.Context, w http.ResponseWriter, op, requestID string, start time.Time, err error) {
	telemetry.RecordSpanError(ctx, err)
	t.Logger.ErrorWithContext(ctx, "failed to decode request", map[string]interface{}{
		"operation": op, "error": err.Error(), "error_type": "decode_error",
		"request_id": requestID, "status": "failure", "duration_ms": time.Since(start).Milliseconds(),
	})
	t.sendError(w, "invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
}

func (t *OpenClawTool) validationFail(ctx context.Context, w http.ResponseWriter, op, requestID string, start time.Time, msg string) {
	telemetry.RecordSpanError(ctx, errors.New(msg))
	t.Logger.WarnWithContext(ctx, msg, map[string]interface{}{
		"operation": op, "error_type": "validation_error",
		"request_id": requestID, "status": "failure", "duration_ms": time.Since(start).Milliseconds(),
	})
	t.sendError(w, msg, http.StatusBadRequest, ErrCodeInvalidRequest)
}

func (t *OpenClawTool) tooLargeFail(ctx context.Context, w http.ResponseWriter, op, requestID string, start time.Time, n int) {
	telemetry.RecordSpanError(ctx, fmt.Errorf("input too large: %d", n))
	t.Logger.WarnWithContext(ctx, "input exceeds size cap", map[string]interface{}{
		"operation": op, "error_type": "validation_error", "request_id": requestID,
		"input_chars": n, "max_input_chars": t.cfg.MaxInputChars,
		"status": "failure", "duration_ms": time.Since(start).Milliseconds(),
	})
	t.sendError(w, fmt.Sprintf("text exceeds MAX_INPUT_CHARS (%d > %d)", n, t.cfg.MaxInputChars),
		http.StatusRequestEntityTooLarge, ErrCodeInputTooLarge)
}

// sendError sends a structured error for LOCAL failures (validation, size cap, busy,
// timeout, workspace). Retryable is derived from the status code (>= 500). WriteHeader MUST
// precede Encode — the orchestrator reads success solely from the HTTP status.
func (t *OpenClawTool) sendError(w http.ResponseWriter, message string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

// sendUpstreamError sends a structured error for OpenClaw HTTP failures, classified by
// core.ClassifyUpstreamError so the orchestrator routes it correctly.
func (t *OpenClawTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(info.HTTPStatus)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}
