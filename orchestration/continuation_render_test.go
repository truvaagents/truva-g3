package orchestration

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Layer 1 (Cause 2a): renderContinuationStepResult must mark failed StepResults
// with [FAILED: ...] so the continuation planner sees the failure status.
// Without this, the renderer uses result.Response (empty for failures) and the
// planner re-issues downstream steps that depend on the failed one.

func TestRenderContinuationStepResult_SuccessRendersResponseVerbatim(t *testing.T) {
	result := &StepResult{
		StepID:   "step-1",
		Success:  true,
		Response: "AAPL: $150.25",
		Error:    "", // Success path: Error must not leak in
	}
	got := renderContinuationStepResult(result, "AAPL: $150.25")
	if got != "AAPL: $150.25" {
		t.Errorf("expected verbatim Response, got %q", got)
	}
	if strings.Contains(got, "FAILED") {
		t.Errorf("success path must not contain FAILED marker, got %q", got)
	}
}

func TestRenderContinuationStepResult_SuccessWithEmptyResponse(t *testing.T) {
	result := &StepResult{
		StepID:   "step-1",
		Success:  true,
		Response: "",
	}
	// Truncated/empty response on success is still verbatim — caller's choice.
	if got := renderContinuationStepResult(result, ""); got != "" {
		t.Errorf("expected empty string for empty success response, got %q", got)
	}
}

func TestRenderContinuationStepResult_FailureRendersFailedMarker(t *testing.T) {
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   "rate limit exceeded",
	}
	got := renderContinuationStepResult(result, "")
	want := "[FAILED: rate limit exceeded]"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestRenderContinuationStepResult_FailureIgnoresResponseField(t *testing.T) {
	// Even if Response happens to be set on a failed result, the renderer
	// must show the FAILED marker — Response on a failed step is unreliable
	// (could be a stale partial body, an empty placeholder, etc.).
	result := &StepResult{
		StepID:   "step-1",
		Success:  false,
		Response: "partial garbage that should NOT appear",
		Error:    "service unavailable",
	}
	got := renderContinuationStepResult(result, "partial garbage that should NOT appear")
	if !strings.HasPrefix(got, "[FAILED:") {
		t.Errorf("failed step must render [FAILED: ...] marker, got %q", got)
	}
	if strings.Contains(got, "garbage") {
		t.Errorf("Response of failed step must not leak into render, got %q", got)
	}
	if !strings.Contains(got, "service unavailable") {
		t.Errorf("Error must appear in render, got %q", got)
	}
}

func TestRenderContinuationStepResult_FailureTruncatesLongError(t *testing.T) {
	// Pathological 5KB upstream error body must be capped at 200 chars + "…".
	longErr := strings.Repeat("x", 5000)
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   longErr,
	}
	got := renderContinuationStepResult(result, "")
	// Marker overhead: "[FAILED: " (9) + "…" (3 bytes UTF-8) + "]" (1) = 13 + 200 chars = 213.
	if len(got) > 220 {
		t.Errorf("expected truncated render under 220 bytes, got %d bytes: %s", len(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected truncation ellipsis, got %q", got)
	}
	if !strings.HasPrefix(got, "[FAILED: ") {
		t.Errorf("expected [FAILED: prefix, got %q", got)
	}
}

func TestRenderContinuationStepResult_FailureSingleLinesMultilineError(t *testing.T) {
	// A multi-line error (e.g. Go panic stack) must collapse to first line so
	// the continuation prompt doesn't get unbalanced by stray newlines.
	multilineErr := "connection refused\n\tat tcp.Dial (line 42)\n\tat ... 30 more frames"
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   multilineErr,
	}
	got := renderContinuationStepResult(result, "")
	if strings.Contains(got, "\n") {
		t.Errorf("multi-line error must be single-lined, got %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("first line of error must appear, got %q", got)
	}
	if strings.Contains(got, "tcp.Dial") {
		t.Errorf("trailing lines must be dropped, got %q", got)
	}
}

func TestRenderContinuationStepResult_FailureWithEmptyError(t *testing.T) {
	// Edge: failed step with no Error string. Render must still emit the
	// marker so the planner sees failure status, even if no detail.
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   "",
	}
	got := renderContinuationStepResult(result, "")
	if got != "[FAILED: ]" {
		t.Errorf("expected [FAILED: ] for empty-error failure, got %q", got)
	}
}

func TestRenderContinuationStepResult_FailureUTF8SafeTruncation(t *testing.T) {
	// 250 multi-byte runes (each "é" = 2 bytes in UTF-8 → 500 bytes total).
	// Byte-naive truncation at 200 would split a codepoint and produce
	// invalid UTF-8. truncateRunes must back up to a rune boundary.
	multibyteErr := strings.Repeat("é", 250)
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   multibyteErr,
	}
	got := renderContinuationStepResult(result, "")
	// Strip the marker prefix/suffix to inspect the truncated body.
	body := strings.TrimSuffix(strings.TrimPrefix(got, "[FAILED: "), "]")
	// Body should be valid UTF-8.
	for i := 0; i < len(body); {
		_, size := utf8.DecodeRuneInString(body[i:])
		if size == 0 {
			t.Fatalf("invalid UTF-8 byte at offset %d in: %q", i, body)
		}
		i += size
	}
	// Should contain the ellipsis indicating truncation.
	if !strings.Contains(body, "…") {
		t.Errorf("expected truncation ellipsis, got: %q", body)
	}
}

func TestRenderContinuationStepResult_FailureBoundaryAt200Chars(t *testing.T) {
	// Exactly-200-char error must NOT trigger truncation ellipsis.
	exact200 := strings.Repeat("a", 200)
	result := &StepResult{
		StepID:  "step-1",
		Success: false,
		Error:   exact200,
	}
	got := renderContinuationStepResult(result, "")
	if strings.Contains(got, "…") {
		t.Errorf("200-char error should NOT be truncated, got %q", got)
	}
	if !strings.Contains(got, exact200) {
		t.Errorf("expected full 200-char error in render, got %q", got)
	}

	// 201 chars MUST trigger truncation.
	exact201 := strings.Repeat("a", 201)
	result.Error = exact201
	got = renderContinuationStepResult(result, "")
	if !strings.Contains(got, "…") {
		t.Errorf("201-char error MUST be truncated, got %q", got)
	}
}
