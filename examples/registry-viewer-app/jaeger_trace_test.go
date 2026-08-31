package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type jaegerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn jaegerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestEnrichUnifiedViewWithTraceSurfacesAsyncLineageAndWallClock(t *testing.T) {
	const executionTraceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const triggerTraceID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	previousClient := jaegerReadClient
	jaegerReadClient = &http.Client{Transport: jaegerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/traces/"+executionTraceID {
			t.Fatalf("path = %q, want execution trace API path", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
          "data":[{"spans":[{
			"traceID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"spanID":"1111111111111111",
            "operationName":"task.process",
            "duration":70677352,
            "tags":[{"key":"task.id","type":"string","value":"order-event-demo-123"}],
			"references":[
			  {"refType":"CHILD_OF","traceID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanID":"2222222222222222"},
			  {"refType":"FOLLOWS_FROM","traceID":"javascript:alert(1)","spanID":"not-a-span"},
			  {"refType":"FOLLOWS_FROM","traceID":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","spanID":"3333333333333333"}
            ]
          },{
			"traceID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"spanID":"4444444444444444",
			"operationName":"pipeline.hook.after_synthesis.governed-order-response",
			"startTime":1787845011689174,
			"duration":7,
			"tags":[{"key":"span.kind","type":"string","value":"internal"}]
          }]}]
        }`)),
		}, nil
	})}

	previousURL := jaegerQueryURL
	previousMock := useMock
	jaegerQueryURL = "http://jaeger.invalid"
	useMock = false
	t.Cleanup(func() {
		jaegerQueryURL = previousURL
		jaegerReadClient = previousClient
		useMock = previousMock
	})

	view := &UnifiedExecutionView{
		TraceID:             executionTraceID,
		TotalDurationMs:     59_343,
		WallClockDurationMs: 59_343,
		DurationSource:      "phase_loop_fallback",
	}
	enrichUnifiedViewWithTrace(t.Context(), view)

	if view.AsyncTaskID != "order-event-demo-123" {
		t.Errorf("async_task_id = %q", view.AsyncTaskID)
	}
	if view.WallClockDurationMs != 70_677 || view.DurationSource != "jaeger_task.process" {
		t.Errorf("duration = %d source=%q, want 70677 from task.process", view.WallClockDurationMs, view.DurationSource)
	}
	if view.TraceEnrichmentStatus != "available" {
		t.Errorf("trace_enrichment_status = %q", view.TraceEnrichmentStatus)
	}
	if len(view.LinkedTraces) != 1 {
		t.Fatalf("linked traces = %#v, want one cross-trace FOLLOWS_FROM", view.LinkedTraces)
	}
	link := view.LinkedTraces[0]
	if link.TraceID != triggerTraceID || link.SpanID != "3333333333333333" ||
		link.Relationship != "FOLLOWS_FROM" || link.SourceOperation != "task.process" {
		t.Errorf("linked trace = %#v", link)
	}
	if len(view.PipelineHooks) != 1 {
		t.Fatalf("pipeline hooks = %#v, want one trace-backed hook", view.PipelineHooks)
	}
	hook := view.PipelineHooks[0]
	if hook.Phase != "after_synthesis" || hook.HookName != "governed-order-response" ||
		hook.Status != "completed" || hook.DurationUS != 7 ||
		hook.SpanID != "4444444444444444" || hook.TraceID != executionTraceID {
		t.Errorf("pipeline hook = %#v", hook)
	}
}

func TestExtractJaegerTraceLinkageSortsAndClassifiesPipelineHooks(t *testing.T) {
	const traceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	linkage := extractJaegerTraceLinkage(traceID, []jaegerTraceSpan{
		{
			TraceID:       traceID,
			SpanID:        "2222222222222222",
			OperationName: "pipeline.hook.after_synthesis.response-governor",
			StartTime:     2_000_000,
			Duration:      9,
		},
		{
			TraceID:       traceID,
			SpanID:        "1111111111111111",
			OperationName: "pipeline.hook.after_execution.response-governor",
			StartTime:     1_000_000,
			Duration:      387,
			Tags: []jaegerTraceTag{
				{Key: "otel.status_code", Value: "ERROR"},
			},
		},
		{
			TraceID:       traceID,
			SpanID:        "3333333333333333",
			OperationName: "pipeline.hook.unknown_phase.not-a-framework-phase",
			StartTime:     3_000_000,
		},
	})

	if len(linkage.PipelineHooks) != 2 {
		t.Fatalf("pipeline hooks = %#v, want two recognized hooks", linkage.PipelineHooks)
	}
	first := linkage.PipelineHooks[0]
	if first.Phase != "after_execution" || first.HookName != "response-governor" ||
		first.Status != "failed" || first.DurationUS != 387 {
		t.Errorf("first hook = %#v", first)
	}
	second := linkage.PipelineHooks[1]
	if second.Phase != "after_synthesis" || second.Status != "completed" || second.DurationUS != 9 {
		t.Errorf("second hook = %#v", second)
	}
}

func TestEnrichUnifiedViewWithTraceIsOptional(t *testing.T) {
	previousURL := jaegerQueryURL
	previousMock := useMock
	jaegerQueryURL = ""
	useMock = false
	t.Cleanup(func() {
		jaegerQueryURL = previousURL
		useMock = previousMock
	})

	view := &UnifiedExecutionView{TraceID: "cccccccccccccccccccccccccccccccc", WallClockDurationMs: 12}
	enrichUnifiedViewWithTrace(t.Context(), view)
	if view.TraceEnrichmentStatus != "unavailable" {
		t.Errorf("status = %q, want unavailable", view.TraceEnrichmentStatus)
	}
	if view.WallClockDurationMs != 12 || len(view.LinkedTraces) != 0 || len(view.PipelineHooks) != 0 || view.AsyncTaskID != "" {
		t.Errorf("optional enrichment changed base view: %#v", view)
	}
}

func TestHandleJaegerTraceRedirectUsesConfiguredBrowserURL(t *testing.T) {
	previousURL := jaegerUIURL
	jaegerUIURL = "https://observability.example.com/jaeger"
	t.Cleanup(func() { jaegerUIURL = previousURL })

	req := httptest.NewRequest(http.MethodGet, "/api/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	rec := httptest.NewRecorder()
	handleJaegerTraceRedirect(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTemporaryRedirect)
	}
	if location := rec.Header().Get("Location"); location != "https://observability.example.com/jaeger/trace/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("Location = %q", location)
	}
}

func TestHandleJaegerTraceRedirectRejectsInvalidInputs(t *testing.T) {
	previousURL := jaegerUIURL
	t.Cleanup(func() { jaegerUIURL = previousURL })

	t.Run("trace ID", func(t *testing.T) {
		jaegerUIURL = "https://observability.example.com"
		req := httptest.NewRequest(http.MethodGet, "/api/traces/not-a-trace", nil)
		rec := httptest.NewRecorder()
		handleJaegerTraceRedirect(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("UI URL", func(t *testing.T) {
		jaegerUIURL = "javascript:alert(1)"
		req := httptest.NewRequest(http.MethodGet, "/api/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
		rec := httptest.NewRecorder()
		handleJaegerTraceRedirect(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
