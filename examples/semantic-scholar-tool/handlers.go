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
func (s *SemanticScholarTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// sendError sends a structured error response using core.ToolResponse
func (s *SemanticScholarTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status) // WriteHeader BEFORE Encode
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE") || strings.Contains(code, "RATE_LIMITED"),
		},
	})
}

// handleSearchPapers processes paper search requests with full telemetry
func (s *SemanticScholarTool) handleSearchPapers(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "semantic-scholar-tool"),
		attribute.String("truvag3.capability", "search_papers"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_papers"),
	)

	// Step 6: Log request start
	s.Logger.InfoWithContext(ctx, "Processing search papers request", map[string]interface{}{
		"operation":  "search_papers",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req SearchPapersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.errors.total",
			"capability", "search_papers",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_papers",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.Counter("s2.errors.total",
			"capability", "search_papers",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Empty query provided", map[string]interface{}{
			"operation":  "search_papers",
			"request_id": upstreamRequestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		s.sendError(rw, "Query is required", http.StatusBadRequest, "INVALID_QUERY")
		return
	}

	// Add query to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.query", req.Query),
		attribute.Int("s2.max_results", req.MaxResults),
		attribute.String("s2.year", req.Year),
		attribute.String("s2.fields_of_study", req.FieldsOfStudy),
	)

	s.Logger.InfoWithContext(ctx, "Received search papers request", map[string]interface{}{
		"operation":       "search_papers",
		"query":           req.Query,
		"max_results":     req.MaxResults,
		"year":            req.Year,
		"fields_of_study": req.FieldsOfStudy,
		"request_id":      upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_s2_api",
		attribute.String("query", req.Query),
		attribute.String("api", "paper_search"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := s.client.SearchPapers(ctx, req.Query, req.MaxResults, req.Year, req.FieldsOfStudy)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("s2.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_papers",
		"api", "semantic_scholar",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.api.errors.total",
			"capability", "search_papers",
			"error_type", "api_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "S2 API call failed", map[string]interface{}{
			"operation":   "search_papers",
			"error":       err.Error(),
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Semantic Scholar API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "S2 API call successful", map[string]interface{}{
		"operation":    "search_papers",
		"query":        req.Query,
		"total":        result.Total,
		"result_count": len(result.Data),
		"duration_ms":  apiDuration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	// Convert S2Paper to PaperResult
	papers := make([]PaperResult, 0, len(result.Data))
	for _, p := range result.Data {
		authors := make([]Author, 0, len(p.Authors))
		for _, a := range p.Authors {
			authors = append(authors, Author{
				AuthorID: a.AuthorID,
				Name:     a.Name,
			})
		}
		papers = append(papers, PaperResult{
			PaperID:         p.PaperID,
			Title:           p.Title,
			Authors:         authors,
			Year:            p.Year,
			CitationCount:   p.CitationCount,
			Abstract:        p.Abstract,
			URL:             p.URL,
			PublicationDate: p.PublicationDate,
		})
	}

	response := SearchPapersResponse{
		Query:  req.Query,
		Total:  result.Total,
		Papers: papers,
		Source: "Semantic Scholar API",
	}

	// Add span attributes for results
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("s2.result_count", len(response.Papers)),
		attribute.Int("s2.total", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("s2.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_papers",
	)
	telemetry.Counter("s2.requests.total",
		"capability", "search_papers",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall unified metrics
	telemetry.RecordToolCall("semantic-scholar-tool", "search_papers", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "search_papers_completed",
		attribute.String("query", req.Query),
		attribute.Int("result_count", len(response.Papers)),
		attribute.Int("total", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	s.Logger.InfoWithContext(ctx, "Search papers request completed", map[string]interface{}{
		"operation":    "search_papers",
		"status":       "success",
		"query":        req.Query,
		"result_count": len(response.Papers),
		"total":        response.Total,
		"source":       response.Source,
		"duration_ms":  duration.Milliseconds(),
		"request_id":   upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGetPaperDetails processes paper details requests with full telemetry
func (s *SemanticScholarTool) handleGetPaperDetails(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "semantic-scholar-tool"),
		attribute.String("truvag3.capability", "get_paper_details"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_paper_details"),
	)

	// Step 6: Log request start
	s.Logger.InfoWithContext(ctx, "Processing get paper details request", map[string]interface{}{
		"operation":  "get_paper_details",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req PaperDetailsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.errors.total",
			"capability", "get_paper_details",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_paper_details",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.PaperID = strings.TrimSpace(req.PaperID)
	if req.PaperID == "" {
		telemetry.Counter("s2.errors.total",
			"capability", "get_paper_details",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Empty paper_id provided", map[string]interface{}{
			"operation":  "get_paper_details",
			"request_id": upstreamRequestID,
			"error":      "paper_id is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: paper_id is required"))
		s.sendError(rw, "Paper ID is required", http.StatusBadRequest, "INVALID_PAPER_ID")
		return
	}

	// Add paper_id to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.paper_id", req.PaperID),
	)

	s.Logger.InfoWithContext(ctx, "Received get paper details request", map[string]interface{}{
		"operation":  "get_paper_details",
		"paper_id":   req.PaperID,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_s2_api",
		attribute.String("paper_id", req.PaperID),
		attribute.String("api", "paper_details"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	paper, err := s.client.GetPaperDetails(ctx, req.PaperID)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("s2.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_paper_details",
		"api", "semantic_scholar",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.api.errors.total",
			"capability", "get_paper_details",
			"error_type", "api_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "S2 API call failed", map[string]interface{}{
			"operation":   "get_paper_details",
			"error":       err.Error(),
			"paper_id":    req.PaperID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Semantic Scholar API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "S2 API call successful", map[string]interface{}{
		"operation":   "get_paper_details",
		"paper_id":    req.PaperID,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// Convert authors
	authors := make([]Author, 0, len(paper.Authors))
	for _, a := range paper.Authors {
		authors = append(authors, Author{
			AuthorID: a.AuthorID,
			Name:     a.Name,
		})
	}

	// Convert S2PaperRef references to PaperResult, filtering out null paperId entries
	refs := make([]PaperResult, 0, len(paper.References))
	for _, r := range paper.References {
		if r.PaperID == nil {
			continue // Skip null paperId entries
		}
		refAuthors := make([]Author, 0, len(r.Authors))
		for _, a := range r.Authors {
			refAuthors = append(refAuthors, Author{
				AuthorID: a.AuthorID,
				Name:     a.Name,
			})
		}
		refs = append(refs, PaperResult{
			PaperID:       *r.PaperID,
			Title:         r.Title,
			Authors:       refAuthors,
			Year:          r.Year,
			CitationCount: r.CitationCount,
			URL:           r.URL,
		})
	}

	// Convert S2PaperRef citations to PaperResult, filtering out null paperId entries
	cits := make([]PaperResult, 0, len(paper.Citations))
	for _, c := range paper.Citations {
		if c.PaperID == nil {
			continue // Skip null paperId entries
		}
		citAuthors := make([]Author, 0, len(c.Authors))
		for _, a := range c.Authors {
			citAuthors = append(citAuthors, Author{
				AuthorID: a.AuthorID,
				Name:     a.Name,
			})
		}
		cits = append(cits, PaperResult{
			PaperID:       *c.PaperID,
			Title:         c.Title,
			Authors:       citAuthors,
			Year:          c.Year,
			CitationCount: c.CitationCount,
			URL:           c.URL,
		})
	}

	response := PaperDetailsResponse{
		PaperID:                  paper.PaperID,
		Title:                    paper.Title,
		Authors:                  authors,
		Year:                     paper.Year,
		Abstract:                 paper.Abstract,
		URL:                      paper.URL,
		CitationCount:            paper.CitationCount,
		ReferenceCount:           paper.ReferenceCount,
		InfluentialCitationCount: paper.InfluentialCitationCount,
		PublicationDate:          paper.PublicationDate,
		References:               refs,
		Citations:                cits,
		Source:                   "Semantic Scholar API",
	}

	// Extract TLDR text
	if paper.TLDR != nil {
		response.TLDR = paper.TLDR.Text
	}

	// Extract open access PDF URL
	if paper.OpenAccessPdf != nil {
		response.OpenAccessPDF = paper.OpenAccessPdf.URL
	}

	// Add span attributes for results
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.paper_id", response.PaperID),
		attribute.Int("s2.citation_count", response.CitationCount),
		attribute.Int("s2.reference_count", response.ReferenceCount),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("s2.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_paper_details",
	)
	telemetry.Counter("s2.requests.total",
		"capability", "get_paper_details",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall unified metrics
	telemetry.RecordToolCall("semantic-scholar-tool", "get_paper_details", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_paper_details_completed",
		attribute.String("paper_id", response.PaperID),
		attribute.String("title", response.Title),
		attribute.Int("citation_count", response.CitationCount),
		attribute.Int("reference_count", response.ReferenceCount),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	s.Logger.InfoWithContext(ctx, "Get paper details request completed", map[string]interface{}{
		"operation":       "get_paper_details",
		"status":          "success",
		"paper_id":        response.PaperID,
		"title":           response.Title,
		"citation_count":  response.CitationCount,
		"reference_count": response.ReferenceCount,
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

// handleGetAuthor processes author profile requests with full telemetry
func (s *SemanticScholarTool) handleGetAuthor(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "semantic-scholar-tool"),
		attribute.String("truvag3.capability", "get_author"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_author"),
	)

	// Step 6: Log request start
	s.Logger.InfoWithContext(ctx, "Processing get author request", map[string]interface{}{
		"operation":  "get_author",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req AuthorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.errors.total",
			"capability", "get_author",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_author",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.AuthorID = strings.TrimSpace(req.AuthorID)
	if req.AuthorID == "" {
		telemetry.Counter("s2.errors.total",
			"capability", "get_author",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Empty author_id provided", map[string]interface{}{
			"operation":  "get_author",
			"request_id": upstreamRequestID,
			"error":      "author_id is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: author_id is required"))
		s.sendError(rw, "Author ID is required", http.StatusBadRequest, "INVALID_AUTHOR_ID")
		return
	}

	// Add author_id to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.author_id", req.AuthorID),
	)

	s.Logger.InfoWithContext(ctx, "Received get author request", map[string]interface{}{
		"operation":  "get_author",
		"author_id":  req.AuthorID,
		"request_id": upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_s2_api",
		attribute.String("author_id", req.AuthorID),
		attribute.String("api", "author_profile"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	author, err := s.client.GetAuthor(ctx, req.AuthorID)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("s2.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_author",
		"api", "semantic_scholar",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.api.errors.total",
			"capability", "get_author",
			"error_type", "api_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "S2 API call failed", map[string]interface{}{
			"operation":   "get_author",
			"error":       err.Error(),
			"author_id":   req.AuthorID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Semantic Scholar API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "S2 API call successful", map[string]interface{}{
		"operation":   "get_author",
		"author_id":   req.AuthorID,
		"author_name": author.Name,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// Cap papers to first 20 entries
	papers := author.Papers
	if len(papers) > 20 {
		papers = papers[:20]
	}

	// Convert S2Paper to PaperResult
	paperResults := make([]PaperResult, 0, len(papers))
	for _, p := range papers {
		pAuthors := make([]Author, 0, len(p.Authors))
		for _, a := range p.Authors {
			pAuthors = append(pAuthors, Author{
				AuthorID: a.AuthorID,
				Name:     a.Name,
			})
		}
		paperResults = append(paperResults, PaperResult{
			PaperID:       p.PaperID,
			Title:         p.Title,
			Authors:       pAuthors,
			Year:          p.Year,
			CitationCount: p.CitationCount,
			URL:           p.URL,
		})
	}

	response := AuthorResponse{
		AuthorID:      author.AuthorID,
		Name:          author.Name,
		Affiliations:  author.Affiliations,
		PaperCount:    author.PaperCount,
		CitationCount: author.CitationCount,
		HIndex:        author.HIndex,
		Papers:        paperResults,
		URL:           author.URL,
		Source:        "Semantic Scholar API",
	}

	// Add span attributes for results
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.author_id", response.AuthorID),
		attribute.String("s2.author_name", response.Name),
		attribute.Int("s2.h_index", response.HIndex),
		attribute.Int("s2.paper_count", response.PaperCount),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("s2.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_author",
	)
	telemetry.Counter("s2.requests.total",
		"capability", "get_author",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall unified metrics
	telemetry.RecordToolCall("semantic-scholar-tool", "get_author", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_author_completed",
		attribute.String("author_id", response.AuthorID),
		attribute.String("author_name", response.Name),
		attribute.Int("h_index", response.HIndex),
		attribute.Int("paper_count", response.PaperCount),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	s.Logger.InfoWithContext(ctx, "Get author request completed", map[string]interface{}{
		"operation":      "get_author",
		"status":         "success",
		"author_id":      response.AuthorID,
		"author_name":    response.Name,
		"h_index":        response.HIndex,
		"paper_count":    response.PaperCount,
		"citation_count": response.CitationCount,
		"source":         response.Source,
		"duration_ms":    duration.Milliseconds(),
		"request_id":     upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGetCitations processes citation requests with full telemetry
func (s *SemanticScholarTool) handleGetCitations(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "semantic-scholar-tool"),
		attribute.String("truvag3.capability", "get_citations"),
	)

	// Step 5: Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_citations"),
	)

	// Step 6: Log request start
	s.Logger.InfoWithContext(ctx, "Processing get citations request", map[string]interface{}{
		"operation":  "get_citations",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// Step 7: Decode request
	var req CitationsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.errors.total",
			"capability", "get_citations",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "get_citations",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Step 8: Validate required fields
	req.PaperID = strings.TrimSpace(req.PaperID)
	if req.PaperID == "" {
		telemetry.Counter("s2.errors.total",
			"capability", "get_citations",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Empty paper_id provided", map[string]interface{}{
			"operation":  "get_citations",
			"request_id": upstreamRequestID,
			"error":      "paper_id is required",
			"error_type": "validation_error",
		})
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: paper_id is required"))
		s.sendError(rw, "Paper ID is required", http.StatusBadRequest, "INVALID_PAPER_ID")
		return
	}

	// Add paper_id to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.paper_id", req.PaperID),
		attribute.Int("s2.max_results", req.MaxResults),
	)

	s.Logger.InfoWithContext(ctx, "Received get citations request", map[string]interface{}{
		"operation":   "get_citations",
		"paper_id":    req.PaperID,
		"max_results": req.MaxResults,
		"request_id":  upstreamRequestID,
	})

	// Step 9: Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_s2_api",
		attribute.String("paper_id", req.PaperID),
		attribute.String("api", "citations"),
	)

	// Step 10: API call with timing
	apiStartTime := time.Now()
	result, err := s.client.GetCitations(ctx, req.PaperID, req.MaxResults)
	apiDuration := time.Since(apiStartTime)

	// Step 11: Record API latency histogram
	telemetry.Histogram("s2.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "get_citations",
		"api", "semantic_scholar",
	)

	// Step 12: Handle API errors with core.ClassifyUpstreamError()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("s2.api.errors.total",
			"capability", "get_citations",
			"error_type", "api_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "S2 API call failed", map[string]interface{}{
			"operation":   "get_citations",
			"error":       err.Error(),
			"paper_id":    req.PaperID,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Semantic Scholar API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "S2 API call successful", map[string]interface{}{
		"operation":      "get_citations",
		"paper_id":       req.PaperID,
		"citation_count": len(result.Data),
		"duration_ms":    apiDuration.Milliseconds(),
		"request_id":     upstreamRequestID,
	})

	// Extract citing papers from nested citingPaper field
	citations := make([]PaperResult, 0, len(result.Data))
	for _, item := range result.Data {
		p := item.CitingPaper
		authors := make([]Author, 0, len(p.Authors))
		for _, a := range p.Authors {
			authors = append(authors, Author{
				AuthorID: a.AuthorID,
				Name:     a.Name,
			})
		}
		citations = append(citations, PaperResult{
			PaperID:         p.PaperID,
			Title:           p.Title,
			Authors:         authors,
			Year:            p.Year,
			CitationCount:   p.CitationCount,
			Abstract:        p.Abstract,
			URL:             p.URL,
			PublicationDate: p.PublicationDate,
		})
	}

	response := CitationsResponse{
		PaperID:   req.PaperID,
		Total:     len(citations),
		Citations: citations,
		Source:    "Semantic Scholar API",
	}

	// Add span attributes for results
	telemetry.SetSpanAttributes(ctx,
		attribute.String("s2.paper_id", response.PaperID),
		attribute.Int("s2.citation_count", response.Total),
	)

	// Step 13: Record success counters
	duration := time.Since(startTime)
	telemetry.Histogram("s2.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_citations",
	)
	telemetry.Counter("s2.requests.total",
		"capability", "get_citations",
		"status", "success",
		"module", "tool",
	)

	// Step 14: RecordToolCall unified metrics
	telemetry.RecordToolCall("semantic-scholar-tool", "get_citations", float64(duration.Milliseconds()), "success")

	// Step 15: Add completion span event
	telemetry.AddSpanEvent(ctx, "get_citations_completed",
		attribute.String("paper_id", response.PaperID),
		attribute.Int("citation_count", response.Total),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// Step 16: Log completion + send response
	s.Logger.InfoWithContext(ctx, "Get citations request completed", map[string]interface{}{
		"operation":      "get_citations",
		"status":         "success",
		"paper_id":       response.PaperID,
		"citation_count": response.Total,
		"source":         response.Source,
		"duration_ms":    duration.Milliseconds(),
		"request_id":     upstreamRequestID,
	})

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
