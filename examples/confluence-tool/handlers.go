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
func (t *ConfluenceTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
// CRITICAL: WriteHeader is called BEFORE Encode so the orchestrator sees the
// correct HTTP status code (Go defaults to 200 if WriteHeader is not called).
func (t *ConfluenceTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

// ---------------------------------------------------------------------------
// Handler: handleCreatePage
// ---------------------------------------------------------------------------

// handleCreatePage processes create-page requests with full telemetry.
func (t *ConfluenceTool) handleCreatePage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Set span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "confluence-tool"),
		attribute.String("truvag3.capability", "create_page"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "create_page"),
	)

	// 5. Log incoming request
	t.Logger.InfoWithContext(ctx, "Processing create_page request", map[string]interface{}{
		"operation":  "create_page",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 6. Decode JSON body
	var req CreatePageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.errors.total",
			"capability", "create_page",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "create_page",
			"request_id":  upstreamRequestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 7. Validate/normalize input
	req.SpaceID = strings.TrimSpace(req.SpaceID)
	req.Title = strings.TrimSpace(req.Title)
	req.ParentID = strings.TrimSpace(req.ParentID)
	if req.SpaceID == "" || req.Title == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: space_id and title are required"))
		telemetry.Counter("confluence.errors.total",
			"capability", "create_page",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":   "create_page",
			"request_id":  upstreamRequestID,
			"error":       "space_id and title are required",
			"error_type":  "validation_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "space_id and title are required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	// Add input to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.space_id", req.SpaceID),
		attribute.String("confluence.title", req.Title),
	)

	t.Logger.InfoWithContext(ctx, "Received create_page request", map[string]interface{}{
		"operation":  "create_page",
		"space_id":   req.SpaceID,
		"title":      req.Title,
		"request_id": upstreamRequestID,
	})

	// 8. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_confluence_api",
		attribute.String("space_id", req.SpaceID),
		attribute.String("api", "create_page"),
	)

	// 9. Call Confluence API with timing
	apiStartTime := time.Now()
	page, err := t.client.CreatePage(ctx, req.SpaceID, req.Title, req.Content, req.ParentID)
	apiDuration := time.Since(apiStartTime)

	// 10. Record API histogram
	telemetry.Histogram("confluence.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "create_page",
		"api", "confluence",
	)

	// 11. Handle errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.api.errors.total",
			"capability", "create_page",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Confluence API call failed", map[string]interface{}{
			"operation":   "create_page",
			"error":       err.Error(),
			"space_id":    req.SpaceID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Confluence API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "Confluence API call successful", map[string]interface{}{
		"operation":   "create_page",
		"page_id":     page.ID,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// Build response
	version := 1
	if page.Version != nil {
		version = page.Version.Number
	}
	response := CreatePageResponse{
		PageID:    page.ID,
		URL:       t.client.pageURL(page),
		Title:     page.Title,
		SpaceID:   page.SpaceID,
		Version:   version,
		CreatedAt: page.CreatedAt,
		Source:    "Confluence API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.page_id", response.PageID),
	)

	// 12. Record success counters + histograms + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("confluence.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "create_page",
	)
	telemetry.Counter("confluence.requests.total",
		"capability", "create_page",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("confluence-tool", "create_page", float64(duration.Milliseconds()), "success")

	// 13. Add completion span event
	telemetry.AddSpanEvent(ctx, "page_created",
		attribute.String("page_id", response.PageID),
		attribute.String("title", response.Title),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 14. Log completion
	t.Logger.InfoWithContext(ctx, "Create page request completed", map[string]interface{}{
		"operation":   "create_page",
		"page_id":     response.PageID,
		"title":       response.Title,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// ---------------------------------------------------------------------------
// Handler: handleSearchPages
// ---------------------------------------------------------------------------

// handleSearchPages processes search requests with full telemetry.
func (t *ConfluenceTool) handleSearchPages(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Trace context
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Baggage
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "confluence-tool"),
		attribute.String("truvag3.capability", "search_pages"),
	)

	// 4. Span event
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_pages"),
	)

	// 5. Log
	t.Logger.InfoWithContext(ctx, "Processing search_pages request", map[string]interface{}{
		"operation":  "search_pages",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 6. Decode
	var req SearchPagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.errors.total",
			"capability", "search_pages",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "search_pages",
			"request_id":  upstreamRequestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 7. Validate
	req.Query = strings.TrimSpace(req.Query)
	req.SpaceKey = strings.TrimSpace(req.SpaceKey)
	if req.Query == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		telemetry.Counter("confluence.errors.total",
			"capability", "search_pages",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":   "search_pages",
			"request_id":  upstreamRequestID,
			"error":       "query is required",
			"error_type":  "validation_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "query is required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.query", req.Query),
		attribute.String("confluence.space_key", req.SpaceKey),
		attribute.Int("confluence.limit", req.Limit),
	)

	t.Logger.InfoWithContext(ctx, "Received search_pages request", map[string]interface{}{
		"operation":  "search_pages",
		"query":      req.Query,
		"space_key":  req.SpaceKey,
		"limit":      req.Limit,
		"request_id": upstreamRequestID,
	})

	// 8. Span event before API call
	telemetry.AddSpanEvent(ctx, "calling_confluence_api",
		attribute.String("query", req.Query),
		attribute.String("api", "search_pages"),
	)

	// 9. Call API
	apiStartTime := time.Now()
	result, err := t.client.SearchPages(ctx, req.Query, req.SpaceKey, req.Limit)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("confluence.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_pages",
		"api", "confluence",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.api.errors.total",
			"capability", "search_pages",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Confluence API call failed", map[string]interface{}{
			"operation":   "search_pages",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Confluence API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "Confluence API call successful", map[string]interface{}{
		"operation":    "search_pages",
		"query":        req.Query,
		"result_count": len(result.Results),
		"duration_ms":  apiDuration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Convert results
	results := make([]SearchPageResult, 0, len(result.Results))
	for _, hit := range result.Results {
		updatedAt := ""
		if hit.History.LastUpdated.When != "" {
			updatedAt = hit.History.LastUpdated.When
		}
		results = append(results, SearchPageResult{
			PageID:    hit.ID,
			Title:     hit.Title,
			SpaceKey:  hit.Space.Key,
			SpaceName: hit.Space.Name,
			URL:       hit.URL,
			Excerpt:   hit.Excerpt,
			Version:   hit.Version.Number,
			UpdatedAt: updatedAt,
		})
	}

	response := SearchPagesResponse{
		Query:   req.Query,
		Results: results,
		Total:   result.Size,
		Source:  "Confluence API",
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.Int("confluence.result_count", len(response.Results)),
	)

	// 10. Success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("confluence.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_pages",
	)
	telemetry.Counter("confluence.requests.total",
		"capability", "search_pages",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("confluence-tool", "search_pages", float64(duration.Milliseconds()), "success")

	// 11. Completion span event
	telemetry.AddSpanEvent(ctx, "search_pages_completed",
		attribute.String("query", req.Query),
		attribute.Int("result_count", len(response.Results)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 12. Log completion
	t.Logger.InfoWithContext(ctx, "Search pages request completed", map[string]interface{}{
		"operation":    "search_pages",
		"query":        req.Query,
		"result_count": len(response.Results),
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// 13. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// ---------------------------------------------------------------------------
// Handler: handleGetPage
// ---------------------------------------------------------------------------

// handleGetPage processes get-page requests with full telemetry.
func (t *ConfluenceTool) handleGetPage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "confluence-tool"),
		attribute.String("truvag3.capability", "get_page"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_page"),
	)

	t.Logger.InfoWithContext(ctx, "Processing get_page request", map[string]interface{}{
		"operation":  "get_page",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	var req GetPageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.errors.total",
			"capability", "get_page",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "get_page",
			"request_id":  upstreamRequestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.PageID = strings.TrimSpace(req.PageID)
	if req.PageID == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: page_id is required"))
		telemetry.Counter("confluence.errors.total",
			"capability", "get_page",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "Empty page_id provided", map[string]interface{}{
			"operation":   "get_page",
			"request_id":  upstreamRequestID,
			"error":       "page_id is required",
			"error_type":  "validation_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "page_id is required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.page_id", req.PageID),
		attribute.Bool("confluence.include_body", req.IncludeBody),
	)

	t.Logger.InfoWithContext(ctx, "Received get_page request", map[string]interface{}{
		"operation":    "get_page",
		"page_id":      req.PageID,
		"include_body": req.IncludeBody,
		"request_id":   upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_confluence_api",
		attribute.String("page_id", req.PageID),
		attribute.String("api", "get_page"),
	)

	apiStartTime := time.Now()
	page, err := t.client.GetPage(ctx, req.PageID, req.IncludeBody)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("confluence.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_page",
		"api", "confluence",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.api.errors.total",
			"capability", "get_page",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Confluence API call failed", map[string]interface{}{
			"operation":   "get_page",
			"error":       err.Error(),
			"page_id":     req.PageID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Confluence API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "Confluence API call successful", map[string]interface{}{
		"operation":   "get_page",
		"page_id":     page.ID,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	version := 0
	if page.Version != nil {
		version = page.Version.Number
	}
	content := ""
	if page.Body != nil && page.Body.Storage != nil {
		content = page.Body.Storage.Value
	}

	response := GetPageResponse{
		PageID:    page.ID,
		Title:     page.Title,
		SpaceID:   page.SpaceID,
		URL:       t.client.pageURL(page),
		Version:   version,
		Status:    page.Status,
		Content:   content,
		CreatedAt: page.CreatedAt,
		Source:    "Confluence API",
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.retrieved_page_id", response.PageID),
	)

	duration := time.Since(startTime)
	telemetry.Histogram("confluence.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_page",
	)
	telemetry.Counter("confluence.requests.total",
		"capability", "get_page",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("confluence-tool", "get_page", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "get_page_completed",
		attribute.String("page_id", response.PageID),
		attribute.String("title", response.Title),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Get page request completed", map[string]interface{}{
		"operation":   "get_page",
		"page_id":     response.PageID,
		"title":       response.Title,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// ---------------------------------------------------------------------------
// Handler: handleUpdatePage
// ---------------------------------------------------------------------------

// handleUpdatePage processes update-page requests with full telemetry.
func (t *ConfluenceTool) handleUpdatePage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "confluence-tool"),
		attribute.String("truvag3.capability", "update_page"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "update_page"),
	)

	t.Logger.InfoWithContext(ctx, "Processing update_page request", map[string]interface{}{
		"operation":  "update_page",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	var req UpdatePageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.errors.total",
			"capability", "update_page",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "update_page",
			"request_id":  upstreamRequestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.PageID = strings.TrimSpace(req.PageID)
	req.Title = strings.TrimSpace(req.Title)
	if req.PageID == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: page_id is required"))
		telemetry.Counter("confluence.errors.total",
			"capability", "update_page",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":   "update_page",
			"request_id":  upstreamRequestID,
			"error":       "page_id is required",
			"error_type":  "validation_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "page_id is required", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	if req.Title == "" && req.Content == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: title or content must be provided"))
		telemetry.Counter("confluence.errors.total",
			"capability", "update_page",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "No update fields provided", map[string]interface{}{
			"operation":   "update_page",
			"request_id":  upstreamRequestID,
			"error":       "title or content must be provided",
			"error_type":  "validation_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(rw, "at least one of title or content must be provided", http.StatusBadRequest, "INVALID_INPUT")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.page_id", req.PageID),
	)

	t.Logger.InfoWithContext(ctx, "Received update_page request", map[string]interface{}{
		"operation":  "update_page",
		"page_id":    req.PageID,
		"has_title":  req.Title != "",
		"has_content": req.Content != "",
		"request_id": upstreamRequestID,
	})

	telemetry.AddSpanEvent(ctx, "calling_confluence_api",
		attribute.String("page_id", req.PageID),
		attribute.String("api", "update_page"),
	)

	apiStartTime := time.Now()
	page, err := t.client.UpdatePage(ctx, req.PageID, req.Title, req.Content)
	apiDuration := time.Since(apiStartTime)

	telemetry.Histogram("confluence.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "update_page",
		"api", "confluence",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.api.errors.total",
			"capability", "update_page",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Confluence API call failed", map[string]interface{}{
			"operation":   "update_page",
			"error":       err.Error(),
			"page_id":     req.PageID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Confluence API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "Confluence API call successful", map[string]interface{}{
		"operation":   "update_page",
		"page_id":     page.ID,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	version := 0
	if page.Version != nil {
		version = page.Version.Number
	}
	response := UpdatePageResponse{
		PageID:  page.ID,
		URL:     t.client.pageURL(page),
		Title:   page.Title,
		Version: version,
		Source:  "Confluence API",
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("confluence.updated_page_id", response.PageID),
	)

	duration := time.Since(startTime)
	telemetry.Histogram("confluence.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "update_page",
	)
	telemetry.Counter("confluence.requests.total",
		"capability", "update_page",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("confluence-tool", "update_page", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "page_updated",
		attribute.String("page_id", response.PageID),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Update page request completed", map[string]interface{}{
		"operation":   "update_page",
		"page_id":     response.PageID,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// ---------------------------------------------------------------------------
// Handler: handleListSpaces
// ---------------------------------------------------------------------------

// handleListSpaces lists available Confluence spaces.
func (t *ConfluenceTool) handleListSpaces(rw http.ResponseWriter, r *http.Request) {
	// 1. Start timing
	startTime := time.Now()

	// 2. Extract context
	ctx := r.Context()

	// 3. Extract upstream request ID
	upstreamRequestID := r.Header.Get("X-Request-ID")

	// 4. Set initial span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "confluence-tool"),
		attribute.String("truvag3.capability", "list_spaces"),
		attribute.String("truvag3.request_id", upstreamRequestID),
	)

	// 5. Add request received span event
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("capability", "list_spaces"),
	)

	// 6. Decode request
	var req ListSpacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.errors.total",
			"capability", "list_spaces",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.WarnWithContext(ctx, "Failed to decode list_spaces request", map[string]interface{}{
			"operation":   "list_spaces",
			"error":       err.Error(),
			"error_type":  "decode_error",
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendError(rw, "Invalid request body: "+err.Error(), http.StatusBadRequest, "DECODE_ERROR")
		return
	}

	// 7. No required fields — limit has defaults in client

	telemetry.SetSpanAttributes(ctx,
		attribute.Int("confluence.limit", req.Limit),
	)

	t.Logger.InfoWithContext(ctx, "Received list_spaces request", map[string]interface{}{
		"operation":  "list_spaces",
		"limit":      req.Limit,
		"request_id": upstreamRequestID,
	})

	// 8. Call Confluence API
	telemetry.AddSpanEvent(ctx, "calling_confluence_api",
		attribute.String("api", "list_spaces"),
	)

	apiStartTime := time.Now()
	result, err := t.client.ListSpaces(ctx, req.Limit)
	apiDuration := time.Since(apiStartTime)

	// 9. Record API histogram
	telemetry.Histogram("confluence.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "list_spaces",
		"api", "confluence",
	)

	// 10. Handle API error
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("confluence.api.errors.total",
			"capability", "list_spaces",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Confluence API call failed", map[string]interface{}{
			"operation":   "list_spaces",
			"error":       err.Error(),
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "Confluence API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.Logger.InfoWithContext(ctx, "Confluence API call successful", map[string]interface{}{
		"operation":   "list_spaces",
		"space_count": len(result.Results),
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 11. Build response
	spaces := make([]SpaceInfo, 0, len(result.Results))
	for _, s := range result.Results {
		desc := ""
		if s.Description != nil {
			desc = *s.Description
		}
		spaceURL := ""
		if s.Links != nil && s.Links.WebUI != "" {
			spaceURL = t.client.baseURL + "/wiki" + s.Links.WebUI
		}
		spaces = append(spaces, SpaceInfo{
			ID:          s.ID,
			Key:         s.Key,
			Name:        s.Name,
			Type:        s.Type,
			Status:      s.Status,
			HomepageID:  s.HomepageID,
			URL:         spaceURL,
			Description: desc,
		})
	}

	response := ListSpacesResponse{
		Spaces: spaces,
		Total:  len(spaces),
		Source: "Confluence API",
	}

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("confluence.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "list_spaces",
	)
	telemetry.Counter("confluence.requests.total",
		"capability", "list_spaces",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("confluence-tool", "list_spaces", float64(duration.Milliseconds()), "success")

	// 13. Add completion span event
	telemetry.AddSpanEvent(ctx, "list_spaces_completed",
		attribute.Int("space_count", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "List spaces request completed", map[string]interface{}{
		"operation":   "list_spaces",
		"space_count": response.Total,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 14-15. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
