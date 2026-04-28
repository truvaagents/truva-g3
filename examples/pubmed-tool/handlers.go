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
func (t *PubMedTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
// CRITICAL: WriteHeader MUST be called before Encode (Go HTTP constraint).
func (t *PubMedTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}

func (t *PubMedTool) handleSearchArticles(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "pubmed-tool"),
		attribute.String("truvag3.capability", "search_articles"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_articles"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing search articles request", map[string]interface{}{
		"operation":  "search_articles",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchArticlesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.errors.total",
			"capability", "search_articles",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_articles",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate input
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.Counter("pubmed.errors.total",
			"capability", "search_articles",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "search_articles",
			"request_id": upstreamRequestID,
			"error":      "query is required",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		t.sendError(rw, "Query is required", http.StatusBadRequest, "INVALID_QUERY")
		return
	}

	// Default max_results to 10, clamp to 1-100
	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}
	if req.MaxResults > 100 {
		req.MaxResults = 100
	}

	// Add query to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("pubmed.query", req.Query),
		attribute.Int("pubmed.max_results", req.MaxResults),
		attribute.String("pubmed.sort", req.Sort),
	)

	t.Logger.InfoWithContext(ctx, "Received search articles request", map[string]interface{}{
		"operation":   "search_articles",
		"query":       req.Query,
		"max_results": req.MaxResults,
		"sort":        req.Sort,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_ncbi_api",
		attribute.String("query", req.Query),
		attribute.String("api", "esearch+esummary"),
		attribute.Int("max_results", req.MaxResults),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	searchResult, err := t.client.SearchArticles(ctx, req.Query, req.MaxResults, req.Sort)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("pubmed.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_articles",
		"api", "ncbi",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.api.errors.total",
			"capability", "search_articles",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "NCBI API call failed", map[string]interface{}{
			"operation":   "search_articles",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "NCBI API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Convert client result to response format
	t.Logger.InfoWithContext(ctx, "NCBI API call successful", map[string]interface{}{
		"operation":    "search_articles",
		"query":        req.Query,
		"duration_ms":  apiDuration.Milliseconds(),
		"total_count":  searchResult.TotalCount,
		"result_count": len(searchResult.Articles),
		"request_id":   upstreamRequestID,
	})

	articles := make([]ArticleSummary, 0, len(searchResult.Articles))
	for _, a := range searchResult.Articles {
		authors := make([]string, 0, len(a.Authors))
		for _, auth := range a.Authors {
			authors = append(authors, auth.Name)
		}

		articles = append(articles, ArticleSummary{
			PMID:        a.UID,
			Title:       a.Title,
			Authors:     authors,
			Journal:     a.FullJournalName,
			PubDate:     a.PubDate,
			DOI:         extractDOI(a.ArticleIDs),
			PMCRefCount: a.PMCRefCount,
			HasAbstract: hasAbstract(a.Attributes),
		})
	}

	response := SearchArticlesResponse{
		Query:      req.Query,
		TotalCount: searchResult.TotalCount,
		Articles:   articles,
		Source:     "NCBI PubMed E-utilities",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("pubmed.total_count", response.TotalCount),
		attribute.Int("pubmed.result_count", len(response.Articles)),
	)

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("pubmed.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_articles",
	)
	telemetry.Counter("pubmed.requests.total",
		"capability", "search_articles",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("pubmed-tool", "search_articles", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "search_articles_completed",
		attribute.String("query", req.Query),
		attribute.Int("total_count", response.TotalCount),
		attribute.Int("result_count", len(response.Articles)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion with context
	t.Logger.InfoWithContext(ctx, "Search articles request completed", map[string]interface{}{
		"operation":    "search_articles",
		"query":        req.Query,
		"total_count":  response.TotalCount,
		"result_count": len(response.Articles),
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (t *PubMedTool) handleGetArticleDetails(rw http.ResponseWriter, r *http.Request) {
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

	// Step 4: Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "pubmed-tool"),
		attribute.String("truvag3.capability", "get_article_details"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_article_details"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing get article details request", map[string]interface{}{
		"operation":  "get_article_details",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req GetArticleDetailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.errors.total",
			"capability", "get_article_details",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_article_details",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate input
	req.PMIDs = strings.TrimSpace(req.PMIDs)
	if req.PMIDs == "" {
		telemetry.Counter("pubmed.errors.total",
			"capability", "get_article_details",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty PMIDs provided", map[string]interface{}{
			"operation":  "get_article_details",
			"request_id": upstreamRequestID,
			"error":      "pmids is required",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: pmids is required"))
		t.sendError(rw, "PMIDs is required", http.StatusBadRequest, "INVALID_PMIDS")
		return
	}

	// Split PMIDs by comma, trim whitespace
	rawPMIDs := strings.Split(req.PMIDs, ",")
	pmidList := make([]string, 0, len(rawPMIDs))
	for _, p := range rawPMIDs {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			pmidList = append(pmidList, trimmed)
		}
	}

	// Add PMIDs to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("pubmed.pmids", req.PMIDs),
		attribute.Int("pubmed.pmid_count", len(pmidList)),
	)

	t.Logger.InfoWithContext(ctx, "Received get article details request", map[string]interface{}{
		"operation":  "get_article_details",
		"pmids":      req.PMIDs,
		"pmid_count": len(pmidList),
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_ncbi_api",
		attribute.String("pmids", req.PMIDs),
		attribute.String("api", "esummary"),
		attribute.Int("pmid_count", len(pmidList)),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	summaryArticles, err := t.client.GetSummaries(ctx, pmidList)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("pubmed.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_article_details",
		"api", "ncbi",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.api.errors.total",
			"capability", "get_article_details",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "NCBI API call failed", map[string]interface{}{
			"operation":   "get_article_details",
			"error":       err.Error(),
			"pmids":       req.PMIDs,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "NCBI API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Convert client result to response format
	t.Logger.InfoWithContext(ctx, "NCBI API call successful", map[string]interface{}{
		"operation":    "get_article_details",
		"pmids":        req.PMIDs,
		"duration_ms":  apiDuration.Milliseconds(),
		"result_count": len(summaryArticles),
		"request_id":   upstreamRequestID,
	})

	articles := make([]ArticleDetail, 0, len(summaryArticles))
	for _, a := range summaryArticles {
		authors := make([]Author, 0, len(a.Authors))
		for _, auth := range a.Authors {
			authors = append(authors, Author{
				Name:     auth.Name,
				AuthType: auth.AuthType,
			})
		}

		articleIDs := make([]ArticleID, 0, len(a.ArticleIDs))
		for _, aid := range a.ArticleIDs {
			articleIDs = append(articleIDs, ArticleID{
				IDType: aid.IDType,
				Value:  aid.Value,
			})
		}

		articles = append(articles, ArticleDetail{
			PMID:        a.UID,
			Title:       a.Title,
			Authors:     authors,
			Journal:     a.FullJournalName,
			PubDate:     a.PubDate,
			DOI:         extractDOI(a.ArticleIDs),
			Volume:      a.Volume,
			Issue:       a.Issue,
			Pages:       a.Pages,
			PMCRefCount: a.PMCRefCount,
			HasAbstract: hasAbstract(a.Attributes),
			ArticleIDs:  articleIDs,
		})
	}

	response := GetArticleDetailsResponse{
		Articles: articles,
		Source:   "NCBI PubMed E-utilities",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("pubmed.result_count", len(response.Articles)),
	)

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("pubmed.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_article_details",
	)
	telemetry.Counter("pubmed.requests.total",
		"capability", "get_article_details",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("pubmed-tool", "get_article_details", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_article_details_completed",
		attribute.String("pmids", req.PMIDs),
		attribute.Int("result_count", len(response.Articles)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion with context
	t.Logger.InfoWithContext(ctx, "Get article details request completed", map[string]interface{}{
		"operation":    "get_article_details",
		"pmids":        req.PMIDs,
		"result_count": len(response.Articles),
		"source":       response.Source,
		"status":       "success",
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

func (t *PubMedTool) handleGetCitations(rw http.ResponseWriter, r *http.Request) {
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

	// Step 4: Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "pubmed-tool"),
		attribute.String("truvag3.capability", "get_citations"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_citations"),
	)

	// Step 6: Log request start
	t.Logger.InfoWithContext(ctx, "Processing get citations request", map[string]interface{}{
		"operation":  "get_citations",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req GetCitationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.errors.total",
			"capability", "get_citations",
			"error_type", "decode_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_citations",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate input
	req.PMID = strings.TrimSpace(req.PMID)
	if req.PMID == "" {
		telemetry.Counter("pubmed.errors.total",
			"capability", "get_citations",
			"error_type", "validation_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "Empty PMID provided", map[string]interface{}{
			"operation":  "get_citations",
			"request_id": upstreamRequestID,
			"error":      "pmid is required",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: pmid is required"))
		t.sendError(rw, "PMID is required", http.StatusBadRequest, "INVALID_PMID")
		return
	}

	// Add PMID to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("pubmed.pmid", req.PMID),
	)

	t.Logger.InfoWithContext(ctx, "Received get citations request", map[string]interface{}{
		"operation":  "get_citations",
		"pmid":       req.PMID,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_ncbi_api",
		attribute.String("pmid", req.PMID),
		attribute.String("api", "elink+esummary"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	citingArticles, err := t.client.GetCitingArticles(ctx, req.PMID)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("pubmed.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_citations",
		"api", "ncbi",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("pubmed.api.errors.total",
			"capability", "get_citations",
			"error_type", "api_error",
			"module", "tool",
		)
		t.Logger.ErrorWithContext(ctx, "NCBI API call failed", map[string]interface{}{
			"operation":   "get_citations",
			"error":       err.Error(),
			"pmid":        req.PMID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		t.sendUpstreamError(rw, "NCBI API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Convert client result to response format
	// Handle nil return (no citations) as valid empty result
	t.Logger.InfoWithContext(ctx, "NCBI API call successful", map[string]interface{}{
		"operation":      "get_citations",
		"pmid":           req.PMID,
		"duration_ms":    apiDuration.Milliseconds(),
		"citation_count": len(citingArticles),
		"request_id":     upstreamRequestID,
	})

	citations := make([]ArticleSummary, 0, len(citingArticles))
	for _, a := range citingArticles {
		authors := make([]string, 0, len(a.Authors))
		for _, auth := range a.Authors {
			authors = append(authors, auth.Name)
		}

		citations = append(citations, ArticleSummary{
			PMID:        a.UID,
			Title:       a.Title,
			Authors:     authors,
			Journal:     a.FullJournalName,
			PubDate:     a.PubDate,
			DOI:         extractDOI(a.ArticleIDs),
			PMCRefCount: a.PMCRefCount,
			HasAbstract: hasAbstract(a.Attributes),
		})
	}

	response := GetCitationsResponse{
		PMID:          req.PMID,
		CitationCount: len(citations),
		Citations:     citations,
		Source:        "NCBI PubMed E-utilities",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("pubmed.citation_count", response.CitationCount),
	)

	// Step 13: Record success counters + RecordToolCall
	duration := time.Since(startTime)
	telemetry.Histogram("pubmed.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_citations",
	)
	telemetry.Counter("pubmed.requests.total",
		"capability", "get_citations",
		"status", "success",
		"module", "tool",
	)
	telemetry.RecordToolCall("pubmed-tool", "get_citations", float64(duration.Milliseconds()), "success")

	// Step 14: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_citations_completed",
		attribute.String("pmid", req.PMID),
		attribute.Int("citation_count", response.CitationCount),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 15: Log completion with context
	t.Logger.InfoWithContext(ctx, "Get citations request completed", map[string]interface{}{
		"operation":      "get_citations",
		"pmid":           req.PMID,
		"citation_count": response.CitationCount,
		"source":         response.Source,
		"status":         "success",
		"duration_ms":    duration.Milliseconds(),
		"request_id":     upstreamRequestID,
	})

	// Step 16: Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
