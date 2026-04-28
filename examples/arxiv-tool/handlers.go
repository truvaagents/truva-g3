package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (a *ArxivTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// sendError sends a structured error response using core.ToolResponse.
// WriteHeader is called BEFORE Encode to ensure correct HTTP status.
func (a *ArxivTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE") || strings.Contains(code, "RATE_LIMITED"),
		},
	})
}

// handleSearchPapers processes paper search requests with full telemetry.
// Follows the 16-step handler checklist from the Truva-G3 tool pattern.
func (a *ArxivTool) handleSearchPapers(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "arxiv-tool"),
		attribute.String("truvag3.capability", "search_papers"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_papers"),
	)

	// Step 6: Log request start
	a.Logger.InfoWithContext(ctx, "Processing search papers request", map[string]interface{}{
		"operation":  "search_papers",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchPapersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.errors.total",
			"capability", "search_papers",
			"error_type", "decode_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_papers",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		a.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.Counter("arxiv.errors.total",
			"capability", "search_papers",
			"error_type", "validation_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "search_papers",
			"request_id": upstreamRequestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		a.sendError(rw, "Query is required", http.StatusBadRequest, "INVALID_QUERY")
		return
	}

	// Add query to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("arxiv.query", req.Query),
		attribute.String("arxiv.category", req.Category),
		attribute.Int("arxiv.max_results", req.MaxResults),
		attribute.String("arxiv.sort_by", req.SortBy),
	)

	a.Logger.InfoWithContext(ctx, "Received search papers request", map[string]interface{}{
		"operation":   "search_papers",
		"query":       req.Query,
		"category":    req.Category,
		"max_results": req.MaxResults,
		"sort_by":     req.SortBy,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_arxiv_api",
		attribute.String("query", req.Query),
		attribute.String("api", "search_papers"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	feed, err := a.client.SearchPapers(ctx, req.Query, req.Category, req.MaxResults, req.SortBy)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("arxiv.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_papers",
		"api", "arxiv",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.api.errors.total",
			"capability", "search_papers",
			"error_type", "api_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "arXiv API call failed", map[string]interface{}{
			"operation":   "search_papers",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		a.sendUpstreamError(rw, "arXiv API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	a.Logger.InfoWithContext(ctx, "arXiv API call successful", map[string]interface{}{
		"operation":     "search_papers",
		"query":         req.Query,
		"duration_ms":   apiDuration.Milliseconds(),
		"total_results": feed.TotalResults,
		"entries":       len(feed.Entries),
		"request_id":    upstreamRequestID,
	})

	// Convert XML entries to JSON PaperResults
	papers := make([]PaperResult, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		papers = append(papers, entryToPaperResult(entry))
	}

	response := SearchPapersResponse{
		Query:        req.Query,
		TotalResults: feed.TotalResults,
		Papers:       papers,
		Source:       "arXiv API",
	}

	// Add result attributes to span
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("arxiv.total_results", response.TotalResults),
		attribute.Int("arxiv.papers_returned", len(response.Papers)),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("arxiv.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_papers",
	)
	telemetry.Counter("arxiv.requests.total",
		"capability", "search_papers",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics (RecordToolCall)
	telemetry.RecordToolCall("arxiv-tool", "search_papers", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "search_papers_completed",
		attribute.String("query", req.Query),
		attribute.Int("total_results", response.TotalResults),
		attribute.Int("papers_returned", len(response.Papers)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	a.Logger.InfoWithContext(ctx, "Search papers request completed", map[string]interface{}{
		"operation":       "search_papers",
		"status":          "success",
		"query":           req.Query,
		"total_results":   response.TotalResults,
		"papers_returned": len(response.Papers),
		"source":          response.Source,
		"duration_ms":     duration.Milliseconds(),
		"request_id":      upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGetPaper processes single paper lookup requests with full telemetry.
// Follows the same 16-step handler checklist.
// Validates arxiv_id, calls client.GetPaper(), converts single entry to PaperResult.
// Special case: if feed has 0 entries, returns 400 "Paper not found".
func (a *ArxivTool) handleGetPaper(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "arxiv-tool"),
		attribute.String("truvag3.capability", "get_paper"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_paper"),
	)

	// Step 6: Log request start
	a.Logger.InfoWithContext(ctx, "Processing get paper request", map[string]interface{}{
		"operation":  "get_paper",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req GetPaperRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.errors.total",
			"capability", "get_paper",
			"error_type", "decode_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_paper",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		a.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.ArxivID = strings.TrimSpace(req.ArxivID)
	if req.ArxivID == "" {
		telemetry.Counter("arxiv.errors.total",
			"capability", "get_paper",
			"error_type", "validation_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Empty arxiv_id provided", map[string]interface{}{
			"operation":  "get_paper",
			"request_id": upstreamRequestID,
			"error":      "arxiv_id is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: arxiv_id is required"))
		a.sendError(rw, "arxiv_id is required", http.StatusBadRequest, "INVALID_ARXIV_ID")
		return
	}

	// Add arxiv_id to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("arxiv.arxiv_id", req.ArxivID),
	)

	a.Logger.InfoWithContext(ctx, "Received get paper request", map[string]interface{}{
		"operation":  "get_paper",
		"arxiv_id":   req.ArxivID,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_arxiv_api",
		attribute.String("arxiv_id", req.ArxivID),
		attribute.String("api", "get_paper"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	feed, err := a.client.GetPaper(ctx, req.ArxivID)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("arxiv.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_paper",
		"api", "arxiv",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.api.errors.total",
			"capability", "get_paper",
			"error_type", "api_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "arXiv API call failed", map[string]interface{}{
			"operation":   "get_paper",
			"error":       err.Error(),
			"arxiv_id":    req.ArxivID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		a.sendUpstreamError(rw, "arXiv API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	a.Logger.InfoWithContext(ctx, "arXiv API call successful", map[string]interface{}{
		"operation":   "get_paper",
		"arxiv_id":    req.ArxivID,
		"duration_ms": apiDuration.Milliseconds(),
		"entries":     len(feed.Entries),
		"request_id":  upstreamRequestID,
	})

	// Check if paper was found
	if len(feed.Entries) == 0 {
		telemetry.Counter("arxiv.errors.total",
			"capability", "get_paper",
			"error_type", "not_found",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Paper not found", map[string]interface{}{
			"operation":  "get_paper",
			"arxiv_id":   req.ArxivID,
			"request_id": upstreamRequestID,
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("paper not found: %s", req.ArxivID))
		a.sendError(rw, fmt.Sprintf("Paper not found: %s", req.ArxivID), http.StatusBadRequest, "PAPER_NOT_FOUND")
		return
	}

	// Convert XML entry to JSON PaperResult
	paper := entryToPaperResult(feed.Entries[0])

	response := GetPaperResponse{
		Paper:  paper,
		Source: "arXiv API",
	}

	// Add result attributes to span
	telemetry.SetSpanAttributes(ctx,
		attribute.String("arxiv.paper_title", paper.Title),
		attribute.String("arxiv.primary_category", paper.PrimaryCategory),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("arxiv.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_paper",
	)
	telemetry.Counter("arxiv.requests.total",
		"capability", "get_paper",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics (RecordToolCall)
	telemetry.RecordToolCall("arxiv-tool", "get_paper", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_paper_completed",
		attribute.String("arxiv_id", req.ArxivID),
		attribute.String("paper_title", paper.Title),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	a.Logger.InfoWithContext(ctx, "Get paper request completed", map[string]interface{}{
		"operation":   "get_paper",
		"status":      "success",
		"arxiv_id":    req.ArxivID,
		"paper_title": paper.Title,
		"source":      response.Source,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleRecentPapers processes recent papers requests with full telemetry.
// Follows the same 16-step handler checklist.
// Validates category, calls client.GetRecentPapers(), converts entries to PaperResults.
func (a *ArxivTool) handleRecentPapers(rw http.ResponseWriter, r *http.Request) {
	// Step 1: startTime + ctx
	startTime := time.Now()
	ctx := r.Context()

	// Step 2: Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 3: Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// Step 4: Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "arxiv-tool"),
		attribute.String("truvag3.capability", "recent_papers"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "recent_papers"),
	)

	// Step 6: Log request start
	a.Logger.InfoWithContext(ctx, "Processing recent papers request", map[string]interface{}{
		"operation":  "recent_papers",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req RecentPapersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.errors.total",
			"capability", "recent_papers",
			"error_type", "decode_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "recent_papers",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		a.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Category = strings.TrimSpace(req.Category)
	if req.Category == "" {
		telemetry.Counter("arxiv.errors.total",
			"capability", "recent_papers",
			"error_type", "validation_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "Empty category provided", map[string]interface{}{
			"operation":  "recent_papers",
			"request_id": upstreamRequestID,
			"error":      "category is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: category is required"))
		a.sendError(rw, "Category is required", http.StatusBadRequest, "INVALID_CATEGORY")
		return
	}

	// Add category to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("arxiv.category", req.Category),
		attribute.Int("arxiv.max_results", req.MaxResults),
	)

	a.Logger.InfoWithContext(ctx, "Received recent papers request", map[string]interface{}{
		"operation":   "recent_papers",
		"category":    req.Category,
		"max_results": req.MaxResults,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_arxiv_api",
		attribute.String("category", req.Category),
		attribute.String("api", "recent_papers"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	feed, err := a.client.GetRecentPapers(ctx, req.Category, req.MaxResults)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("arxiv.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "recent_papers",
		"api", "arxiv",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("arxiv.api.errors.total",
			"capability", "recent_papers",
			"error_type", "api_error",
			"module", "tool",
		)
		a.Logger.ErrorWithContext(ctx, "arXiv API call failed", map[string]interface{}{
			"operation":   "recent_papers",
			"error":       err.Error(),
			"category":    req.Category,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		a.sendUpstreamError(rw, "arXiv API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	a.Logger.InfoWithContext(ctx, "arXiv API call successful", map[string]interface{}{
		"operation":     "recent_papers",
		"category":      req.Category,
		"duration_ms":   apiDuration.Milliseconds(),
		"total_results": feed.TotalResults,
		"entries":       len(feed.Entries),
		"request_id":    upstreamRequestID,
	})

	// Convert XML entries to JSON PaperResults
	papers := make([]PaperResult, 0, len(feed.Entries))
	for _, entry := range feed.Entries {
		papers = append(papers, entryToPaperResult(entry))
	}

	response := RecentPapersResponse{
		Category:     req.Category,
		TotalResults: feed.TotalResults,
		Papers:       papers,
		Source:       "arXiv API",
	}

	// Add result attributes to span
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("arxiv.total_results", response.TotalResults),
		attribute.Int("arxiv.papers_returned", len(response.Papers)),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("arxiv.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "recent_papers",
	)
	telemetry.Counter("arxiv.requests.total",
		"capability", "recent_papers",
		"status", "success",
		"module", "tool",
	)

	// Step 14: Record unified metrics (RecordToolCall)
	telemetry.RecordToolCall("arxiv-tool", "recent_papers", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "recent_papers_completed",
		attribute.String("category", req.Category),
		attribute.Int("total_results", response.TotalResults),
		attribute.Int("papers_returned", len(response.Papers)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	a.Logger.InfoWithContext(ctx, "Recent papers request completed", map[string]interface{}{
		"operation":       "recent_papers",
		"status":          "success",
		"category":        req.Category,
		"total_results":   response.TotalResults,
		"papers_returned": len(response.Papers),
		"source":          response.Source,
		"duration_ms":     duration.Milliseconds(),
		"request_id":      upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
