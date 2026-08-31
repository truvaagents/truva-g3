package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxJaegerTraceResponseBytes = 8 * 1024 * 1024

var jaegerTraceIDPattern = regexp.MustCompile(`^[[:xdigit:]]{16,32}$`)
var jaegerSpanIDPattern = regexp.MustCompile(`^[[:xdigit:]]{16}$`)

type LinkedTrace struct {
	TraceID         string `json:"trace_id"`
	SpanID          string `json:"span_id,omitempty"`
	Relationship    string `json:"relationship"`
	SourceOperation string `json:"source_operation,omitempty"`
}

// PipelineHookExecution is a trace-backed observation of one framework
// pipeline-hook invocation. Hook implementations are not necessarily backed by
// an LLM call, so this model is deliberately independent of LLMInteraction.
type PipelineHookExecution struct {
	TraceID       string    `json:"trace_id"`
	SpanID        string    `json:"span_id,omitempty"`
	OperationName string    `json:"operation_name"`
	Phase         string    `json:"phase"`
	HookName      string    `json:"hook_name"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	DurationUS    int64     `json:"duration_us"`
}

type jaegerTraceResponse struct {
	Data []struct {
		Spans []jaegerTraceSpan `json:"spans"`
	} `json:"data"`
}

type jaegerTraceSpan struct {
	TraceID       string                 `json:"traceID"`
	SpanID        string                 `json:"spanID"`
	ParentSpanID  string                 `json:"parentSpanID,omitempty"`
	OperationName string                 `json:"operationName"`
	StartTime     int64                  `json:"startTime"` // microseconds since Unix epoch
	Duration      int64                  `json:"duration"`  // microseconds
	Tags          []jaegerTraceTag       `json:"tags"`
	References    []jaegerTraceReference `json:"references"`
}

type jaegerTraceTag struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

type jaegerTraceReference struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerTraceLinkage struct {
	TaskID              string
	TaskDurationMs      int64
	TaskOperation       string
	LinkedTriggerTraces []LinkedTrace
	PipelineHooks       []PipelineHookExecution
}

var pipelineHookPhases = []string{
	"before_planning",
	"after_planning",
	"after_execution",
	"after_synthesis",
}

var (
	jaegerQueryURL   = strings.TrimRight(strings.TrimSpace(getEnvOrDefault("JAEGER_QUERY_URL", "")), "/")
	jaegerUIURL      = strings.TrimRight(strings.TrimSpace(getEnvOrDefault("JAEGER_UI_URL", "http://jaeger.localhost")), "/")
	jaegerReadClient = &http.Client{Timeout: 3 * time.Second}
)

func jaegerBrowserTraceURL(traceID string) (string, error) {
	traceID = strings.TrimSpace(traceID)
	if !jaegerTraceIDPattern.MatchString(traceID) {
		return "", fmt.Errorf("trace ID is invalid")
	}
	base, err := url.Parse(jaegerUIURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("jaeger UI URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/trace/" + traceID
	return base.String(), nil
}

func handleJaegerTraceRedirect(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimPrefix(r.URL.Path, "/api/traces/")
	target, err := jaegerBrowserTraceURL(traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

func fetchJaegerTraceLinkage(ctx context.Context, traceID string) (*jaegerTraceLinkage, error) {
	if jaegerQueryURL == "" {
		return nil, fmt.Errorf("jaeger query URL is not configured")
	}
	traceID = strings.TrimSpace(traceID)
	if !jaegerTraceIDPattern.MatchString(traceID) {
		return nil, fmt.Errorf("trace ID is invalid")
	}
	base, err := url.Parse(jaegerQueryURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("jaeger query URL is invalid")
	}
	traceURL := strings.TrimRight(base.String(), "/") + "/api/traces/" + url.PathEscape(traceID)
	// #nosec G704 -- traceURL is built from an operator-owned, validated
	// http(s) base URL and a bounded hexadecimal trace identifier.
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		traceURL,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create Jaeger trace request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// #nosec G704 -- req.URL was validated above and cannot be influenced by
	// arbitrary path, query, credential, or scheme input.
	resp, err := jaegerReadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query Jaeger trace: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("query Jaeger trace: HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxJaegerTraceResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read Jaeger trace: %w", err)
	}
	if len(body) > maxJaegerTraceResponseBytes {
		return nil, fmt.Errorf("jaeger trace exceeds %d bytes", maxJaegerTraceResponseBytes)
	}
	var payload jaegerTraceResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Jaeger trace: %w", err)
	}
	if len(payload.Data) == 0 {
		return nil, fmt.Errorf("jaeger trace not found")
	}
	return extractJaegerTraceLinkage(traceID, payload.Data[0].Spans), nil
}

func extractJaegerTraceLinkage(executionTraceID string, spans []jaegerTraceSpan) *jaegerTraceLinkage {
	linkage := &jaegerTraceLinkage{}
	seenLinks := make(map[string]struct{})
	for _, span := range spans {
		if hook, ok := pipelineHookExecutionFromSpan(executionTraceID, span); ok {
			linkage.PipelineHooks = append(linkage.PipelineHooks, hook)
		}
		isTaskProcess := span.OperationName == "task.process"
		if isTaskProcess {
			linkage.TaskOperation = span.OperationName
			linkage.TaskDurationMs = span.Duration / 1000
			for _, tag := range span.Tags {
				if tag.Key == "task.id" {
					if value, ok := tag.Value.(string); ok {
						linkage.TaskID = value
					}
				}
			}
		}
		for _, reference := range span.References {
			if reference.RefType != "FOLLOWS_FROM" ||
				!jaegerTraceIDPattern.MatchString(reference.TraceID) ||
				(reference.SpanID != "" && !jaegerSpanIDPattern.MatchString(reference.SpanID)) ||
				reference.TraceID == executionTraceID {
				continue
			}
			key := reference.TraceID + "\x00" + reference.SpanID
			if _, duplicate := seenLinks[key]; duplicate {
				continue
			}
			seenLinks[key] = struct{}{}
			linkage.LinkedTriggerTraces = append(linkage.LinkedTriggerTraces, LinkedTrace{
				TraceID:         reference.TraceID,
				SpanID:          reference.SpanID,
				Relationship:    reference.RefType,
				SourceOperation: span.OperationName,
			})
		}
	}
	sort.SliceStable(linkage.PipelineHooks, func(i, j int) bool {
		return linkage.PipelineHooks[i].StartedAt.Before(linkage.PipelineHooks[j].StartedAt)
	})
	return linkage
}

func pipelineHookExecutionFromSpan(traceID string, span jaegerTraceSpan) (PipelineHookExecution, bool) {
	const prefix = "pipeline.hook."
	if !strings.HasPrefix(span.OperationName, prefix) {
		return PipelineHookExecution{}, false
	}

	remainder := strings.TrimPrefix(span.OperationName, prefix)
	phase := ""
	hookName := ""
	for _, candidate := range pipelineHookPhases {
		candidatePrefix := candidate + "."
		if strings.HasPrefix(remainder, candidatePrefix) {
			phase = candidate
			hookName = strings.TrimSpace(strings.TrimPrefix(remainder, candidatePrefix))
			break
		}
	}
	if phase == "" || hookName == "" {
		return PipelineHookExecution{}, false
	}

	status := "completed"
	for _, tag := range span.Tags {
		switch tag.Key {
		case "error":
			if failed, ok := tag.Value.(bool); ok && failed {
				status = "failed"
			}
		case "otel.status_code":
			if value, ok := tag.Value.(string); ok && strings.EqualFold(value, "error") {
				status = "failed"
			}
		}
	}

	durationUS := span.Duration
	if durationUS < 0 {
		durationUS = 0
	}
	spanID := span.SpanID
	if !jaegerSpanIDPattern.MatchString(spanID) {
		spanID = ""
	}
	startedAt := time.Time{}
	if span.StartTime > 0 {
		startedAt = time.UnixMicro(span.StartTime).UTC()
	}

	return PipelineHookExecution{
		TraceID:       traceID,
		SpanID:        spanID,
		OperationName: span.OperationName,
		Phase:         phase,
		HookName:      hookName,
		Status:        status,
		StartedAt:     startedAt,
		DurationUS:    durationUS,
	}, true
}

func enrichUnifiedViewWithTrace(ctx context.Context, unified *UnifiedExecutionView) {
	if unified == nil || unified.TraceID == "" || useMock {
		return
	}
	linkage, err := fetchJaegerTraceLinkage(ctx, unified.TraceID)
	if err != nil {
		unified.TraceEnrichmentStatus = "unavailable"
		return
	}
	unified.TraceEnrichmentStatus = "available"
	unified.AsyncTaskID = linkage.TaskID
	unified.LinkedTraces = linkage.LinkedTriggerTraces
	unified.PipelineHooks = linkage.PipelineHooks
	if linkage.TaskDurationMs > 0 {
		unified.WallClockDurationMs = linkage.TaskDurationMs
		unified.DurationSource = "jaeger_task.process"
	}
}
