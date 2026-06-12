package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (c *CountryTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

func (c *CountryTool) handleCountryInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Request ID for correlation: upstream baggage first, then executor header.
	requestID := telemetry.GetBaggage(ctx)["request_id"]
	if requestID == "" {
		requestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_country_info"),
	)

	var req CountryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		c.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "get_country_info",
			"error":       err.Error(),
			"error_type":  "decode_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		c.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Country = strings.TrimSpace(req.Country)
	if req.Country == "" {
		err := fmt.Errorf("country is required")
		telemetry.RecordSpanError(ctx, err)
		c.Logger.WarnWithContext(ctx, "Empty country in request", map[string]interface{}{
			"operation":   "get_country_info",
			"error_type":  "validation_error",
			"request_id":  requestID,
			"status":      "failure",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		c.sendError(w, "Country name is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	c.Logger.InfoWithContext(ctx, "Received get_country_info request", map[string]interface{}{
		"operation":  "get_country_info",
		"country":    req.Country,
		"request_id": requestID,
	})

	telemetry.AddSpanEvent(ctx, "resolving_country",
		attribute.String("country", req.Country),
	)

	result, err := c.resolveCountryInfo(ctx, req.Country)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if strings.Contains(err.Error(), "not found") {
			// Expected client-input case (unknown/misspelled country) — log as Warn.
			c.Logger.WarnWithContext(ctx, "Country not found", map[string]interface{}{
				"operation":   "get_country_info",
				"error_type":  "not_found",
				"country":     req.Country,
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
			c.sendError(w, fmt.Sprintf("Country '%s' not found", req.Country), http.StatusNotFound, ErrCodeCountryNotFound)
		} else {
			c.Logger.ErrorWithContext(ctx, "Country lookup failed", map[string]interface{}{
				"operation":   "get_country_info",
				"error":       err.Error(),
				"error_type":  "lookup_error",
				"country":     req.Country,
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
			c.sendUpstreamError(w, "Country lookup failed: "+err.Error(), core.ClassifyUpstreamError(err))
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})

	telemetry.AddSpanEvent(ctx, "country_info_retrieved",
		attribute.String("country", result.Name),
		attribute.Int64("population", result.Population),
		attribute.String("capital", result.Capital),
		attribute.String("region", result.Region),
	)

	c.Logger.InfoWithContext(ctx, "get_country_info request completed", map[string]interface{}{
		"operation":   "get_country_info",
		"country":     result.Name,
		"population":  result.Population,
		"request_id":  requestID,
		"status":      "success",
		"duration_ms": time.Since(startTime).Milliseconds(),
	})
}

// resolveCountryInfo builds the response from the embedded offline dataset, then
// enriches it with population/timezones from apicountries.com (best-effort).
func (c *CountryTool) resolveCountryInfo(ctx context.Context, country string) (*CountryResponse, error) {
	m := c.lookupCountry(country)
	if m == nil {
		return nil, fmt.Errorf("country not found")
	}

	result := &CountryResponse{
		Name:        m.Name.Common,
		OfficialN:   m.Name.Official,
		Region:      m.Region,
		Subregion:   m.Subregion,
		Area:        m.Area,
		Flag:        m.Flag, // mledoze ships the Unicode flag emoji directly
		FlagURL:     fmt.Sprintf("https://flagcdn.com/w320/%s.png", strings.ToLower(m.CCA2)),
		CountryCode: m.CCA2,
	}

	if len(m.Capital) > 0 {
		result.Capital = m.Capital[0]
	}

	// Languages: mledoze maps ISO code -> language name. Sort the names so the
	// output is stable across requests (map iteration order is randomized).
	for _, name := range m.Languages {
		if name != "" {
			result.Languages = append(result.Languages, name)
		}
	}
	sort.Strings(result.Languages)

	// Currency: a country may list several; pick the first by sorted ISO 4217
	// code so the choice is deterministic across requests (not map-order dependent).
	if len(m.Currencies) > 0 {
		codes := make([]string, 0, len(m.Currencies))
		for code := range m.Currencies {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		cur := m.Currencies[codes[0]]
		result.Currency.Code = codes[0]
		result.Currency.Name = cur.Name
		result.Currency.Symbol = cur.Symbol
	}

	// Population + timezones are not in the offline dataset — enrich them.
	c.enrichPopulationTimezones(ctx, m.CCA2, result)

	return result, nil
}

// lookupCountry resolves a free-form name or ISO code against the embedded index.
// Falls back to a substring match on the common/official name (shortest common
// name wins) for partial queries. Returns nil when nothing matches.
func (c *CountryTool) lookupCountry(query string) *mledozeCountry {
	key := strings.ToLower(strings.TrimSpace(query))
	if key == "" {
		return nil
	}
	if m, ok := c.index[key]; ok {
		return m
	}

	var best *mledozeCountry
	for i := range c.countries {
		m := &c.countries[i]
		if strings.Contains(strings.ToLower(m.Name.Common), key) ||
			strings.Contains(strings.ToLower(m.Name.Official), key) {
			if best == nil || len(m.Name.Common) < len(best.Name.Common) {
				best = m
			}
		}
	}
	return best
}

// enrichPopulationTimezones fills population and timezones from apicountries.com,
// looked up by exact ISO alpha-2 code (no fuzzy matching). It is best-effort: if
// the service is unavailable the offline result still stands, just without these
// two fields, so a future deprecation degrades gracefully instead of failing.
func (c *CountryTool) enrichPopulationTimezones(ctx context.Context, cca2 string, result *CountryResponse) {
	if cca2 == "" {
		return
	}

	// Population/timezones are stable, so cache them to avoid an external call on
	// repeat lookups (Performance best practice in the Tool Development Guide).
	cacheKey := "enrich:" + cca2
	if cached, err := c.cache.Get(ctx, cacheKey); err == nil && cached != "" {
		var e enrichment
		if json.Unmarshal([]byte(cached), &e) == nil {
			result.Population = e.Population
			result.Timezones = e.Timezones
			return
		}
	}

	telemetry.AddSpanEvent(ctx, "enriching_population_timezones",
		attribute.String("code", cca2),
	)

	reqURL := fmt.Sprintf("%s/alpha/%s", CountryAPIBaseURL, url.PathEscape(cca2))
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "TruvaG3-CountryTool/1.0")

	apiStart := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Best-effort enrichment: record for tracing but do not fail the request.
		telemetry.RecordSpanError(ctx, err)
		c.Logger.WarnWithContext(ctx, "population/timezone enrichment unavailable", map[string]interface{}{
			"operation":   "get_country_info",
			"error":       err.Error(),
			"error_type":  "api_error",
			"code":        cca2,
			"api_latency": time.Since(apiStart).String(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var e enrichment
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return
	}

	result.Population = e.Population
	result.Timezones = e.Timezones

	// Cache for a day — country population/timezones change rarely.
	if data, marshalErr := json.Marshal(e); marshalErr == nil {
		_ = c.cache.Set(ctx, cacheKey, string(data), 24*time.Hour)
	}
}

func (c *CountryTool) sendError(w http.ResponseWriter, message string, status int, code string) {
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
