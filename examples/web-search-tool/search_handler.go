package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/examples/web-search-tool/providers"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// WebSearchRequest represents the input for web_search capability
type WebSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
	SearchType string `json:"search_type,omitempty"`
}

// WebSearchResponse represents the output for web_search capability
type WebSearchResponse struct {
	Query        string                   `json:"query"`
	Results      []providers.SearchResult `json:"results"`
	TotalResults int                      `json:"total_results,omitempty"`
	SearchTime   string                   `json:"search_time"`
	Provider     string                   `json:"provider"`
	Cached       bool                     `json:"cached,omitempty"`
}

func (w *WebSearchTool) handleWebSearch(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "web-search"),
		attribute.String("truvag3.capability", "web_search"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "web_search"),
	)

	w.Logger.InfoWithContext(ctx, "Processing web search request", map[string]interface{}{
		"operation":  "web_search",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req WebSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("search.errors.total",
			"capability", "web_search",
			"error_type", "decode_error",
		)
		w.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "web_search",
			"error":      err.Error(),
			"error_type": "decode_error",
			"request_id": upstreamRequestID,
		})
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// Normalize and validate query
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.Counter("search.errors.total",
			"capability", "web_search",
			"error_type", "missing_query",
		)
		w.Logger.WarnWithContext(ctx, "Empty query in request", map[string]interface{}{
			"operation":  "web_search",
			"error_type": "validation_error",
			"request_id": upstreamRequestID,
		})
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "MISSING_QUERY",
				Message: "Query is required",
			},
		})
		return
	}

	// Apply defaults
	if req.MaxResults <= 0 || req.MaxResults > 10 {
		req.MaxResults = 5
	}
	if req.SearchType == "" {
		req.SearchType = "web"
	}

	// Add query details to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("search.query", req.Query),
		attribute.Int("search.max_results", req.MaxResults),
		attribute.String("search.type", req.SearchType),
	)

	w.Logger.InfoWithContext(ctx, "Received web search request", map[string]interface{}{
		"operation":   "web_search",
		"query":       req.Query,
		"max_results": req.MaxResults,
		"search_type": req.SearchType,
		"request_id":  upstreamRequestID,
	})

	// 6. Check cache
	cacheKey := fmt.Sprintf("search:%s:%s:%d", req.SearchType, req.Query, req.MaxResults)
	if cached := w.checkCache(ctx, cacheKey); cached != nil {
		cached.Cached = true
		cached.SearchTime = "0ms (cached)"

		telemetry.AddSpanEvent(ctx, "cache_hit",
			attribute.String("cache_key", cacheKey),
		)
		telemetry.Counter("search.requests.total",
			"capability", "web_search",
			"status", "success",
			"cached", "true",
		)

		duration := time.Since(startTime)
		telemetry.RecordToolCall("web-search", "web_search", float64(duration.Milliseconds()), "success")

		w.Logger.InfoWithContext(ctx, "Web search request completed (cached)", map[string]interface{}{
			"operation":   "web_search",
			"status":      "success",
			"query":       req.Query,
			"results":     len(cached.Results),
			"cached":      true,
			"duration_ms": duration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})

		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: true,
			Data:    cached,
		})
		return
	}

	// 7. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_search_api",
		attribute.String("query", req.Query),
		attribute.String("provider", w.provider.Name()),
	)

	// 8. Execute search via provider
	apiStartTime := time.Now()
	results, err := w.provider.Search(ctx, req.Query, req.MaxResults, req.SearchType)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("search.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "web_search",
		"provider", w.provider.Name(),
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("search.api.errors.total",
			"capability", "web_search",
			"error_type", "api_error",
			"provider", w.provider.Name(),
		)
		w.Logger.ErrorWithContext(ctx, "Search API call failed", map[string]interface{}{
			"operation":   "web_search",
			"status":      "error",
			"error":       err.Error(),
			"error_type":  "api_error",
			"provider":    w.provider.Name(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		info := core.ClassifyUpstreamError(err)
		w.sendUpstreamError(rw, fmt.Sprintf("Search failed: %v", err), info)
		return
	}

	w.Logger.InfoWithContext(ctx, "Search API call successful", map[string]interface{}{
		"operation":   "web_search",
		"query":       req.Query,
		"results":     len(results),
		"provider":    w.provider.Name(),
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// Build response
	response := WebSearchResponse{
		Query:      req.Query,
		Results:    results,
		SearchTime: time.Since(startTime).String(),
		Provider:   w.provider.Name(),
	}

	// Add result count to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("search.results_count", len(results)),
		attribute.String("search.provider", w.provider.Name()),
	)

	// 9. Cache result
	w.setCache(ctx, cacheKey, &response, 15*time.Minute)

	// 10. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("search.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "web_search",
	)
	telemetry.Counter("search.requests.total",
		"capability", "web_search",
		"status", "success",
		"provider", w.provider.Name(),
		"cached", "false",
	)

	// 11. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("web-search", "web_search", float64(duration.Milliseconds()), "success")

	// 12. Add completion span event
	telemetry.AddSpanEvent(ctx, "search_completed",
		attribute.String("query", req.Query),
		attribute.Int("results_count", len(results)),
		attribute.String("provider", w.provider.Name()),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 13. Log completion with context
	w.Logger.InfoWithContext(ctx, "Web search request completed", map[string]interface{}{
		"operation":   "web_search",
		"status":      "success",
		"query":       req.Query,
		"results":     len(results),
		"provider":    w.provider.Name(),
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 14. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (w *WebSearchTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(info.HTTPStatus)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

// Helper methods
func (w *WebSearchTool) checkCache(ctx context.Context, key string) *WebSearchResponse {
	if w.cache == nil {
		return nil
	}
	cached, err := w.cache.Get(ctx, key)
	if err != nil || cached == "" {
		return nil
	}
	var response WebSearchResponse
	if json.Unmarshal([]byte(cached), &response) == nil {
		return &response
	}
	return nil
}

func (w *WebSearchTool) setCache(ctx context.Context, key string, response *WebSearchResponse, ttl time.Duration) {
	if w.cache == nil {
		return
	}
	if data, err := json.Marshal(response); err == nil {
		w.cache.Set(ctx, key, string(data), ttl)
	}
}
