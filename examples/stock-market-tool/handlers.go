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
func (s *StockTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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
func (s *StockTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
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

// handleStockQuote processes stock quote requests with full telemetry
func (s *StockTool) handleStockQuote(rw http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "stock_quote"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "stock_quote"),
	)

	s.Logger.InfoWithContext(ctx, "Processing stock quote request", map[string]interface{}{
		"operation":  "stock_quote",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req StockQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "stock_quote",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "stock_quote",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// Normalize symbol to uppercase
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))

	// Add symbol to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
	)

	s.Logger.InfoWithContext(ctx, "Received stock quote request", map[string]interface{}{
		"operation":  "stock_quote",
		"symbol":     req.Symbol,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "stock_quote"),
	)

	// 7. Try to get real data from Finnhub API
	apiStartTime := time.Now()
	quote, err := s.client.GetStockQuote(ctx, req.Symbol)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "stock_quote",
		"api", "finnhub",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "stock_quote",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "stock_quote",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if quote == nil {
		s.Logger.ErrorWithContext(ctx, "No data returned from Finnhub API", map[string]interface{}{
			"operation":   "stock_quote",
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendError(rw, "No data returned for "+req.Symbol, http.StatusBadRequest, "NO_DATA")
		return
	}

	// Convert Finnhub response to our response format
	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":   "stock_quote",
		"symbol":      req.Symbol,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})
	response := StockQuoteResponse{
		Symbol:        req.Symbol,
		CurrentPrice:  quote.C,
		Change:        quote.D,
		PercentChange: quote.DP,
		High:          quote.H,
		Low:           quote.L,
		Open:          quote.O,
		PreviousClose: quote.PC,
		Timestamp:     quote.T,
		Source:        "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Float64("stock.current_price", response.CurrentPrice),
	)

	// 8. Cache the result
	cacheKey := fmt.Sprintf("quote:%s", req.Symbol)
	cacheData, _ := json.Marshal(response)
	s.cache.Set(ctx, cacheKey, string(cacheData), 1*time.Minute)

	// 9. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "stock_quote",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "stock_quote",
		"status", "success",
	)

	// 10. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("stock-market-tool", "stock_quote", float64(duration.Milliseconds()), "success")

	// 11. Add completion span event
	telemetry.AddSpanEvent(ctx, "stock_quote_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.Float64("current_price", response.CurrentPrice),
		attribute.Float64("change", response.Change),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 12. Log completion with context
	s.Logger.InfoWithContext(ctx, "Stock quote request completed", map[string]interface{}{
		"operation":     "stock_quote",
		"symbol":        req.Symbol,
		"current_price": response.CurrentPrice,
		"change":        response.Change,
		"source":        response.Source,
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	// 13. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleCompanyProfile processes company profile requests with full telemetry
func (s *StockTool) handleCompanyProfile(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "company_profile"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "company_profile"),
	)

	s.Logger.InfoWithContext(ctx, "Processing company profile request", map[string]interface{}{
		"operation":  "company_profile",
		"method":     r.Method,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req CompanyProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "company_profile",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "company_profile",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))

	// Add symbol to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
	)

	s.Logger.InfoWithContext(ctx, "Received company profile request", map[string]interface{}{
		"operation":  "company_profile",
		"symbol":     req.Symbol,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "company_profile"),
	)

	// 7. Try to get real data from Finnhub API
	apiStartTime := time.Now()
	profile, err := s.client.GetCompanyProfile(ctx, req.Symbol)
	apiDuration := time.Since(apiStartTime)

	// Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "company_profile",
		"api", "finnhub",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "company_profile",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "company_profile",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if profile == nil {
		s.Logger.ErrorWithContext(ctx, "No data returned from Finnhub API", map[string]interface{}{
			"operation":   "company_profile",
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendError(rw, "No data returned for "+req.Symbol, http.StatusBadRequest, "NO_DATA")
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":   "company_profile",
		"symbol":      req.Symbol,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})
	response := CompanyProfileResponse{
		Name:                 profile.Name,
		Ticker:               profile.Ticker,
		Exchange:             profile.Exchange,
		Industry:             profile.FinnhubIndustry,
		Country:              profile.Country,
		Currency:             profile.Currency,
		MarketCapitalization: profile.MarketCapitalization,
		IPO:                  profile.IPO,
		Website:              profile.Weburl,
		Logo:                 profile.Logo,
		Source:               "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("company.name", response.Name),
		attribute.String("company.industry", response.Industry),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "company_profile",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "company_profile",
		"status", "success",
	)

	// 9. Record unified metrics
	telemetry.RecordToolCall("stock-market-tool", "company_profile", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "company_profile_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.String("name", response.Name),
		attribute.String("industry", response.Industry),
		attribute.Float64("market_cap", response.MarketCapitalization),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion
	s.Logger.InfoWithContext(ctx, "Company profile request completed", map[string]interface{}{
		"operation":   "company_profile",
		"symbol":      req.Symbol,
		"name":        response.Name,
		"industry":    response.Industry,
		"market_cap":  response.MarketCapitalization,
		"source":      response.Source,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleCompanyNews processes company news requests with full telemetry
func (s *StockTool) handleCompanyNews(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "company_news"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "company_news"),
	)

	s.Logger.InfoWithContext(ctx, "Processing company news request", map[string]interface{}{
		"operation":  "company_news",
		"method":     r.Method,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req CompanyNewsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "company_news",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "company_news",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))

	// Add symbol to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
		attribute.String("news.date_from", req.From),
		attribute.String("news.date_to", req.To),
	)

	s.Logger.InfoWithContext(ctx, "Received company news request", map[string]interface{}{
		"operation":  "company_news",
		"symbol":     req.Symbol,
		"from":       req.From,
		"to":         req.To,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "company_news"),
		attribute.String("from", req.From),
		attribute.String("to", req.To),
	)

	// 7. Try to get real data from Finnhub API
	apiStartTime := time.Now()
	newsItems, err := s.client.GetCompanyNews(ctx, req.Symbol, req.From, req.To)
	apiDuration := time.Since(apiStartTime)

	// Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "company_news",
		"api", "finnhub",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "company_news",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "company_news",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":   "company_news",
		"symbol":      req.Symbol,
		"news_count":  len(newsItems),
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	news := make([]NewsItem, 0, len(newsItems))
	for _, item := range newsItems {
		news = append(news, NewsItem{
			Headline:  item.Headline,
			Summary:   item.Summary,
			Source:    item.Source,
			URL:       item.URL,
			Image:     item.Image,
			Published: item.Datetime,
		})
	}

	response := CompanyNewsResponse{
		Symbol: req.Symbol,
		News:   news,
		From:   req.From,
		To:     req.To,
		Source: "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("news.count", len(response.News)),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "company_news",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "company_news",
		"status", "success",
	)

	// 9. Record unified metrics
	telemetry.RecordToolCall("stock-market-tool", "company_news", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "company_news_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.Int("news_count", len(response.News)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion
	s.Logger.InfoWithContext(ctx, "Company news request completed", map[string]interface{}{
		"operation":   "company_news",
		"symbol":      req.Symbol,
		"news_count":  len(response.News),
		"source":      response.Source,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleMarketNews processes market news requests with full telemetry
func (s *StockTool) handleMarketNews(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "market_news"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "market_news"),
	)

	s.Logger.InfoWithContext(ctx, "Processing market news request", map[string]interface{}{
		"operation":  "market_news",
		"method":     r.Method,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req MarketNewsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "market_news",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "market_news",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// Add category to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("news.category", req.Category),
	)

	s.Logger.InfoWithContext(ctx, "Received market news request", map[string]interface{}{
		"operation":  "market_news",
		"category":   req.Category,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("category", req.Category),
		attribute.String("api", "market_news"),
	)

	// 7. Try to get real data from Finnhub API
	apiStartTime := time.Now()
	newsItems, err := s.client.GetMarketNews(ctx, req.Category)
	apiDuration := time.Since(apiStartTime)

	// Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "market_news",
		"api", "finnhub",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "market_news",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "market_news",
			"error":       err.Error(),
			"category":    req.Category,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":   "market_news",
		"category":    req.Category,
		"news_count":  len(newsItems),
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	news := make([]NewsItem, 0, len(newsItems))
	for _, item := range newsItems {
		news = append(news, NewsItem{
			Headline:  item.Headline,
			Summary:   item.Summary,
			Source:    item.Source,
			URL:       item.URL,
			Image:     item.Image,
			Published: item.Datetime,
		})
	}

	response := MarketNewsResponse{
		Category: req.Category,
		News:     news,
		Source:   "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("news.count", len(response.News)),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "market_news",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "market_news",
		"status", "success",
	)

	// 9. Record unified metrics
	telemetry.RecordToolCall("stock-market-tool", "market_news", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "market_news_retrieved",
		attribute.String("category", req.Category),
		attribute.Int("news_count", len(response.News)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion
	s.Logger.InfoWithContext(ctx, "Market news request completed", map[string]interface{}{
		"operation":   "market_news",
		"category":    req.Category,
		"news_count":  len(response.News),
		"source":      response.Source,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleBasicFinancials processes basic financials requests with full telemetry
// Returns 100+ financial metrics for Layer 2 (Micro-Resolution) and Layer 4 (Semantic Retry) testing
func (s *StockTool) handleBasicFinancials(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "basic_financials"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "basic_financials"),
	)

	// 5. Log request start WITH CONTEXT
	s.Logger.InfoWithContext(ctx, "Processing basic financials request", map[string]interface{}{
		"operation":  "basic_financials",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 6. Decode request
	var req BasicFinancialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "basic_financials",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "basic_financials",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// 7. Validate and normalize input
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	if req.Symbol == "" {
		telemetry.Counter("stock.errors.total",
			"capability", "basic_financials",
			"error_type", "validation_error",
		)
		s.Logger.ErrorWithContext(ctx, "Empty symbol provided", map[string]interface{}{
			"operation":  "basic_financials",
			"request_id": upstreamRequestID,
			"error":      "symbol is required",
			"error_type": "validation_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_SYMBOL",
				Message: "Symbol is required",
			},
		})
		return
	}

	// Add symbol to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
	)

	s.Logger.InfoWithContext(ctx, "Received basic financials request", map[string]interface{}{
		"operation":  "basic_financials",
		"symbol":     req.Symbol,
		"request_id": upstreamRequestID,
	})

	// 8. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "basic_financials"),
	)

	// 9. Call external API with timing
	apiStartTime := time.Now()
	metrics, err := s.client.GetBasicFinancials(ctx, req.Symbol)
	apiDuration := time.Since(apiStartTime)

	// 10. Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "basic_financials",
		"api", "finnhub",
	)

	// 11. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "basic_financials",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "basic_financials",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if metrics == nil {
		s.Logger.ErrorWithContext(ctx, "No data returned from Finnhub API", map[string]interface{}{
			"operation":   "basic_financials",
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendError(rw, "No data returned for "+req.Symbol, http.StatusBadRequest, "NO_DATA")
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":     "basic_financials",
		"symbol":        req.Symbol,
		"duration_ms":   apiDuration.Milliseconds(),
		"metrics_count": len(metrics.Metric),
		"request_id":    upstreamRequestID,
	})

	// Convert FinnhubSeries to FinancialSeries
	series := FinancialSeries{
		Annual:    make(map[string][]PeriodValue),
		Quarterly: make(map[string][]PeriodValue),
	}
	for key, vals := range metrics.Series.Annual {
		periodVals := make([]PeriodValue, len(vals))
		for i, v := range vals {
			periodVals[i] = PeriodValue{Period: v.Period, Value: v.V}
		}
		series.Annual[key] = periodVals
	}
	for key, vals := range metrics.Series.Quarterly {
		periodVals := make([]PeriodValue, len(vals))
		for i, v := range vals {
			periodVals[i] = PeriodValue{Period: v.Period, Value: v.V}
		}
		series.Quarterly[key] = periodVals
	}

	response := BasicFinancialsResponse{
		Symbol:     req.Symbol,
		MetricType: metrics.MetricType,
		Metric:     metrics.Metric,
		Series:     series,
		Source:     "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("metrics.count", len(response.Metric)),
	)

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "basic_financials",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "basic_financials",
		"status", "success",
	)

	// 13. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("stock-market-tool", "basic_financials", float64(duration.Milliseconds()), "success")

	// 14. Add completion span event
	telemetry.AddSpanEvent(ctx, "basic_financials_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.Int("metrics_count", len(response.Metric)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 15. Log completion with context
	s.Logger.InfoWithContext(ctx, "Basic financials request completed", map[string]interface{}{
		"operation":     "basic_financials",
		"symbol":        req.Symbol,
		"metrics_count": len(response.Metric),
		"source":        response.Source,
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	// 16. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleCompanyEarnings processes company earnings requests with full telemetry
// Returns quarterly earnings data for Layer 4 (Semantic Retry) testing
func (s *StockTool) handleCompanyEarnings(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "company_earnings"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "company_earnings"),
	)

	// 5. Log request start WITH CONTEXT
	s.Logger.InfoWithContext(ctx, "Processing company earnings request", map[string]interface{}{
		"operation":  "company_earnings",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 6. Decode request
	var req CompanyEarningsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "company_earnings",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "company_earnings",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// 7. Validate and normalize input
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	if req.Symbol == "" {
		telemetry.Counter("stock.errors.total",
			"capability", "company_earnings",
			"error_type", "validation_error",
		)
		s.Logger.ErrorWithContext(ctx, "Empty symbol provided", map[string]interface{}{
			"operation":  "company_earnings",
			"request_id": upstreamRequestID,
			"error":      "symbol is required",
			"error_type": "validation_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_SYMBOL",
				Message: "Symbol is required",
			},
		})
		return
	}

	// Add symbol to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
	)

	s.Logger.InfoWithContext(ctx, "Received company earnings request", map[string]interface{}{
		"operation":  "company_earnings",
		"symbol":     req.Symbol,
		"request_id": upstreamRequestID,
	})

	// 8. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "company_earnings"),
	)

	// 9. Call external API with timing
	apiStartTime := time.Now()
	earnings, err := s.client.GetCompanyEarnings(ctx, req.Symbol)
	apiDuration := time.Since(apiStartTime)

	// 10. Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "company_earnings",
		"api", "finnhub",
	)

	// 11. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "company_earnings",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "company_earnings",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":      "company_earnings",
		"symbol":         req.Symbol,
		"duration_ms":    apiDuration.Milliseconds(),
		"earnings_count": len(earnings),
		"request_id":     upstreamRequestID,
	})

	// Convert FinnhubEarnings to QuarterEarnings
	quarterEarnings := make([]QuarterEarnings, len(earnings))
	for i, e := range earnings {
		quarterEarnings[i] = QuarterEarnings{
			Period:          e.Period,
			Year:            e.Year,
			Quarter:         e.Quarter,
			Actual:          e.Actual,
			Estimate:        e.Estimate,
			Surprise:        e.Surprise,
			SurprisePercent: e.SurprisePercent,
		}
	}

	response := CompanyEarningsResponse{
		Symbol:   req.Symbol,
		Earnings: quarterEarnings,
		Source:   "Finnhub API",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("earnings.count", len(response.Earnings)),
	)

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "company_earnings",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "company_earnings",
		"status", "success",
	)

	// 13. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("stock-market-tool", "company_earnings", float64(duration.Milliseconds()), "success")

	// 14. Add completion span event
	telemetry.AddSpanEvent(ctx, "company_earnings_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.Int("earnings_count", len(response.Earnings)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 15. Log completion with context
	s.Logger.InfoWithContext(ctx, "Company earnings request completed", map[string]interface{}{
		"operation":      "company_earnings",
		"symbol":         req.Symbol,
		"earnings_count": len(response.Earnings),
		"source":         response.Source,
		"duration_ms":    duration.Milliseconds(),
		"request_id":     upstreamRequestID,
	})

	// 16. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleAnnualRevenue processes annual revenue requests with full telemetry
// Returns annual revenue from SEC 10-K filings (free tier only provides annual data)
func (s *StockTool) handleAnnualRevenue(rw http.ResponseWriter, r *http.Request) {
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

	// 3. Add span attributes for business context
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "stock-market-tool"),
		attribute.String("truvag3.capability", "annual_revenue"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "annual_revenue"),
	)

	// 5. Log request start WITH CONTEXT
	s.Logger.InfoWithContext(ctx, "Processing annual revenue request", map[string]interface{}{
		"operation":  "annual_revenue",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 6. Decode request
	var req AnnualRevenueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.errors.total",
			"capability", "annual_revenue",
			"error_type", "decode_error",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "annual_revenue",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_REQUEST",
				Message: "Invalid request format",
			},
		})
		return
	}

	// 7. Validate and normalize input
	req.Symbol = strings.ToUpper(strings.TrimSpace(req.Symbol))
	if req.Symbol == "" {
		telemetry.Counter("stock.errors.total",
			"capability", "annual_revenue",
			"error_type", "validation_error",
		)
		s.Logger.ErrorWithContext(ctx, "Empty symbol provided", map[string]interface{}{
			"operation":  "annual_revenue",
			"request_id": upstreamRequestID,
			"error":      "symbol is required",
			"error_type": "validation_error",
		})
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(core.ToolResponse{
			Success: false,
			Error: &core.ToolError{
				Code:    "INVALID_SYMBOL",
				Message: "Symbol is required",
			},
		})
		return
	}

	// Add symbol to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("stock.symbol", req.Symbol),
	)

	s.Logger.InfoWithContext(ctx, "Received annual revenue request", map[string]interface{}{
		"operation":  "annual_revenue",
		"symbol":     req.Symbol,
		"request_id": upstreamRequestID,
	})

	// 8. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_finnhub_api",
		attribute.String("symbol", req.Symbol),
		attribute.String("api", "financials_reported"),
	)

	// 9. Call external API with timing
	apiStartTime := time.Now()
	revenues, err := s.client.GetAnnualRevenue(ctx, req.Symbol)
	apiDuration := time.Since(apiStartTime)

	// 10. Record API latency
	telemetry.Histogram("stock.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "annual_revenue",
		"api", "finnhub",
	)

	// 11. Handle API errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("stock.api.errors.total",
			"capability", "annual_revenue",
			"error_type", "api_error",
		)
		s.Logger.ErrorWithContext(ctx, "Finnhub API call failed", map[string]interface{}{
			"operation":   "annual_revenue",
			"error":       err.Error(),
			"symbol":      req.Symbol,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Finnhub API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	s.Logger.InfoWithContext(ctx, "Finnhub API call successful", map[string]interface{}{
		"operation":     "annual_revenue",
		"symbol":        req.Symbol,
		"duration_ms":   apiDuration.Milliseconds(),
		"revenue_count": len(revenues),
		"request_id":    upstreamRequestID,
	})

	// Convert AnnualRevenue to AnnualRevenueItem
	revenueItems := make([]AnnualRevenueItem, len(revenues))
	for i, r := range revenues {
		revenueItems[i] = AnnualRevenueItem{
			Year:      r.Year,
			Form:      r.Form,
			Revenue:   r.Revenue,
			FiledDate: r.FiledDate,
		}
	}

	response := AnnualRevenueResponse{
		Symbol:  req.Symbol,
		Revenue: revenueItems,
		Source:  "Finnhub API (SEC 10-K)",
	}

	// Add span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("revenue.years_count", len(response.Revenue)),
	)

	// 12. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("stock.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "annual_revenue",
	)
	telemetry.Counter("stock.requests.total",
		"capability", "annual_revenue",
		"status", "success",
	)

	// 13. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("stock-market-tool", "annual_revenue", float64(duration.Milliseconds()), "success")

	// 14. Add completion span event
	telemetry.AddSpanEvent(ctx, "annual_revenue_retrieved",
		attribute.String("symbol", req.Symbol),
		attribute.Int("years_count", len(response.Revenue)),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 15. Log completion with context
	s.Logger.InfoWithContext(ctx, "Annual revenue request completed", map[string]interface{}{
		"operation":   "annual_revenue",
		"symbol":      req.Symbol,
		"years_count": len(response.Revenue),
		"source":      response.Source,
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 16. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
