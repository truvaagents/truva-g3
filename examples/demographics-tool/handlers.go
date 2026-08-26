package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const serviceName = "demographics-tool"

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *DemographicsTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(info.HTTPStatus)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

// handleAreaStatistics returns comprehensive demographics for a geographic area
func (t *DemographicsTool) handleAreaStatistics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Read upstream baggage
	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	// Set span attributes for Jaeger search
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "area_statistics"),
	)

	// Request start event
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "area_statistics"),
	)

	t.Logger.InfoWithContext(ctx, "Area statistics request received", map[string]interface{}{
		"operation":  "area_statistics",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req AreaStatisticsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "area_statistics", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "area_statistics",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Location == "" {
		telemetry.Counter("demographics.errors.total", "capability", "area_statistics", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty location provided", map[string]interface{}{
			"operation":  "area_statistics",
			"request_id": requestID,
			"error":      "location field is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "location field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	result, err := t.fetchAreaStatistics(ctx, req.Location, requestID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "area_statistics", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Failed to fetch area statistics", map[string]interface{}{
			"operation":  "area_statistics",
			"request_id": requestID,
			"location":   req.Location,
			"error":      err.Error(),
		})
		t.sendUpstreamError(w, "Census API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.sendSuccess(w, ctx, "area_statistics", requestID, startTime, result)
}

// handleCompareAreas compares demographics across multiple areas
func (t *DemographicsTool) handleCompareAreas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "compare_areas"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_areas"),
	)

	t.Logger.InfoWithContext(ctx, "Compare areas request received", map[string]interface{}{
		"operation":  "compare_areas",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req CompareAreasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "compare_areas", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "compare_areas",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Locations == "" {
		telemetry.Counter("demographics.errors.total", "capability", "compare_areas", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty locations provided", map[string]interface{}{
			"operation":  "compare_areas",
			"request_id": requestID,
			"error":      "locations field is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "locations field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	locations := strings.Split(req.Locations, ",")
	if len(locations) < 2 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_areas", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Insufficient locations for comparison", map[string]interface{}{
			"operation":      "compare_areas",
			"request_id":     requestID,
			"error":          "at least 2 locations required for comparison",
			"error_type":     "validation_error",
			"location_count": len(locations),
		})
		t.sendError(w, "at least 2 locations required for comparison", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}
	if len(locations) > 10 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_areas", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Too many locations for comparison", map[string]interface{}{
			"operation":      "compare_areas",
			"request_id":     requestID,
			"error":          "maximum 10 locations allowed",
			"error_type":     "validation_error",
			"location_count": len(locations),
		})
		t.sendError(w, "maximum 10 locations allowed", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	var areas []AreaStatisticsResponse
	for _, loc := range locations {
		loc = strings.TrimSpace(loc)
		if loc == "" {
			continue
		}
		result, err := t.fetchAreaStatistics(ctx, loc, requestID)
		if err != nil {
			t.Logger.WarnWithContext(ctx, "Failed to fetch area, skipping", map[string]interface{}{
				"operation":  "compare_areas",
				"request_id": requestID,
				"location":   loc,
				"error":      err.Error(),
			})
			continue
		}
		areas = append(areas, *result)
	}

	if len(areas) == 0 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_areas", "error_type", "api_error")
		t.sendError(w, "could not retrieve data for any of the specified locations", http.StatusInternalServerError, ErrCodeServiceUnavailable)
		return
	}

	response := &CompareAreasResponse{
		Areas:  areas,
		Source: "U.S. Census Bureau - American Community Survey 5-Year Estimates",
	}

	t.sendSuccess(w, ctx, "compare_areas", requestID, startTime, response)
}

// handlePopulationRanking ranks states by a demographic metric
func (t *DemographicsTool) handlePopulationRanking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	// Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "population_ranking"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "population_ranking"),
	)

	t.Logger.InfoWithContext(ctx, "Population ranking request received", map[string]interface{}{
		"operation":  "population_ranking",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req PopulationRankingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "population_ranking", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "population_ranking",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Metric == "" {
		telemetry.Counter("demographics.errors.total", "capability", "population_ranking", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Empty metric provided", map[string]interface{}{
			"operation":  "population_ranking",
			"request_id": requestID,
			"error":      "metric field is required",
			"error_type": "validation_error",
		})
		t.sendError(w, "metric field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	metricInfo, ok := validMetrics[req.Metric]
	if !ok {
		telemetry.Counter("demographics.errors.total", "capability", "population_ranking", "error_type", "validation_error")
		t.Logger.ErrorWithContext(ctx, "Invalid metric provided", map[string]interface{}{
			"operation":  "population_ranking",
			"request_id": requestID,
			"error":      fmt.Sprintf("invalid metric: %s", req.Metric),
			"error_type": "validation_error",
			"metric":     req.Metric,
		})
		t.sendError(w, fmt.Sprintf("invalid metric: %s (valid: population, median_income, home_value, median_rent, poverty_rate, unemployment_rate, median_age)", req.Metric), http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	order := "desc"
	if req.Order == "asc" {
		order = "asc"
	}
	limit := 10
	if req.Limit > 0 && req.Limit <= 52 {
		limit = req.Limit
	}

	// Fetch ranking variables: we need population for rate calculations
	variables := fmt.Sprintf("NAME,%s", metricInfo.Variable)
	needsPopulation := req.Metric == "poverty_rate" || req.Metric == "unemployment_rate"
	if needsPopulation {
		if req.Metric == "poverty_rate" {
			variables += ",B01003_001E" // total population for poverty rate
		} else {
			variables += ",B23025_002E" // labor force for unemployment rate
		}
	}

	apiStart := time.Now()
	data, err := t.client.GetAllStatesData(ctx, variables)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("demographics.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "population_ranking", "api", "census_all_states")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "population_ranking", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Census API failed for ranking", map[string]interface{}{
			"operation":   "population_ranking",
			"request_id":  requestID,
			"error":       err.Error(),
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "Census API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if len(data) < 2 {
		telemetry.Counter("demographics.errors.total", "capability", "population_ranking", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "Census API returned no data for ranking", map[string]interface{}{
			"operation":   "population_ranking",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No ranking data returned from Census API", http.StatusBadGateway, "API_ERROR")
		return
	}

	headerMap := buildHeaderMap(data[0])

	var rankings []RankedState
	for _, row := range data[1:] {
		name := getByHeader(row, headerMap, "NAME")
		stateFIPSCode := getByHeader(row, headerMap, "state")

		rawValue := getByHeader(row, headerMap, metricInfo.Variable)
		value := parseCensusFloat(rawValue)
		if value <= 0 {
			continue
		}

		// Calculate rates if needed
		if needsPopulation {
			var denominator float64
			if req.Metric == "poverty_rate" {
				denominator = parseCensusFloat(getByHeader(row, headerMap, "B01003_001E"))
			} else {
				denominator = parseCensusFloat(getByHeader(row, headerMap, "B23025_002E"))
			}
			if denominator > 0 {
				value = roundTo2(value / denominator * 100)
			} else {
				continue
			}
		}

		rankings = append(rankings, RankedState{
			State: name,
			FIPS:  stateFIPSCode,
			Value: value,
			Units: metricInfo.Units,
		})
	}

	// Sort
	sort.Slice(rankings, func(i, j int) bool {
		if order == "asc" {
			return rankings[i].Value < rankings[j].Value
		}
		return rankings[i].Value > rankings[j].Value
	})

	// Apply limit and assign ranks
	if limit < len(rankings) {
		rankings = rankings[:limit]
	}
	for i := range rankings {
		rankings[i].Rank = i + 1
	}

	result := &PopulationRankingResponse{
		Metric:   req.Metric,
		Order:    order,
		Rankings: rankings,
		DataYear: "2023 (ACS 5-Year)",
		Source:   "U.S. Census Bureau - American Community Survey 5-Year Estimates",
	}

	t.sendSuccess(w, ctx, "population_ranking", requestID, startTime, result)
}

// fetchAreaStatistics fetches and parses demographics for a single location
func (t *DemographicsTool) fetchAreaStatistics(ctx context.Context, location, requestID string) (*AreaStatisticsResponse, error) {
	query, err := parseLocation(location)
	if err != nil {
		return nil, err
	}

	var data [][]string

	switch query.Type {
	case "zip":
		data, err = t.client.GetZipCodeData(ctx, query.ZipCode, demographicVariables)
	case "county":
		countyFIPS := query.CountyFIPS
		if countyFIPS == "" {
			// Fallback: resolve county FIPS by querying all counties in the state
			countyFIPS, err = t.resolveCountyFIPSFromAPI(ctx, query.StateFIPS, query.CountyName)
			if err != nil {
				return nil, fmt.Errorf("could not resolve county '%s': %w", query.CountyName, err)
			}
		}
		data, err = t.client.GetCountyData(ctx, query.StateFIPS, countyFIPS, demographicVariables)
	case "state":
		data, err = t.client.GetStateData(ctx, query.StateFIPS, demographicVariables)
	}

	if err != nil {
		return nil, fmt.Errorf("Census API error: %w", err)
	}
	if len(data) < 2 {
		return nil, fmt.Errorf("no data returned for location: %s", location)
	}

	return t.parseCensusResponse(data, query)
}

// resolveCountyFIPSFromAPI queries all counties in a state to find the matching county name
func (t *DemographicsTool) resolveCountyFIPSFromAPI(ctx context.Context, stateFIPS, countyName string) (string, error) {
	data, err := t.client.GetAllCountiesInState(ctx, stateFIPS)
	if err != nil {
		return "", fmt.Errorf("failed to query counties: %w", err)
	}

	if len(data) < 2 {
		return "", fmt.Errorf("no counties found for state %s", stateFIPS)
	}

	headerMap := buildHeaderMap(data[0])
	lowerCounty := strings.ToLower(countyName)

	for _, row := range data[1:] {
		name := getByHeader(row, headerMap, "NAME")
		// Census returns "Travis County, Texas" — match on county name portion
		nameLower := strings.ToLower(name)
		if strings.Contains(nameLower, lowerCounty) {
			return getByHeader(row, headerMap, "county"), nil
		}
	}

	return "", fmt.Errorf("county '%s' not found in state %s", countyName, stateFIPS)
}

// parseCensusResponse converts a Census 2D string array into an AreaStatisticsResponse
func (t *DemographicsTool) parseCensusResponse(data [][]string, query *LocationQuery) (*AreaStatisticsResponse, error) {
	headerMap := buildHeaderMap(data[0])
	row := data[1] // First data row

	name := getByHeader(row, headerMap, "NAME")
	population := parseCensusInt(getByHeader(row, headerMap, "B01003_001E"))
	medianIncome := parseCensusFloat(getByHeader(row, headerMap, "B19013_001E"))
	medianHomeValue := parseCensusFloat(getByHeader(row, headerMap, "B25077_001E"))
	medianRent := parseCensusFloat(getByHeader(row, headerMap, "B25064_001E"))
	pop25Plus := parseCensusInt(getByHeader(row, headerMap, "B15003_001E"))
	bachelors := parseCensusFloat(getByHeader(row, headerMap, "B15003_022E"))
	masters := parseCensusFloat(getByHeader(row, headerMap, "B15003_023E"))
	doctorate := parseCensusFloat(getByHeader(row, headerMap, "B15003_025E"))
	povertyPop := parseCensusFloat(getByHeader(row, headerMap, "B17001_002E"))
	unemployed := parseCensusInt(getByHeader(row, headerMap, "B23025_005E"))
	laborForce := parseCensusInt(getByHeader(row, headerMap, "B23025_002E"))
	medianAge := parseCensusFloat(getByHeader(row, headerMap, "B01002_001E"))
	totalHousingUnits := parseCensusInt(getByHeader(row, headerMap, "B25001_001E"))
	vacantUnits := parseCensusInt(getByHeader(row, headerMap, "B25002_003E"))

	// Calculate derived fields
	var vacancyRate float64
	if totalHousingUnits > 0 {
		vacancyRate = roundTo2(float64(vacantUnits) / float64(totalHousingUnits) * 100)
	}

	var unemploymentRate float64
	if laborForce > 0 {
		unemploymentRate = roundTo2(float64(unemployed) / float64(laborForce) * 100)
	}

	var povertyRate float64
	if population > 0 {
		povertyRate = roundTo2(povertyPop / float64(population) * 100)
	}

	// Education percentages use population 25+ (B15003_001E) as denominator
	var bachelorsPct, graduatePct float64
	if pop25Plus > 0 {
		bachelorsPct = roundTo2(bachelors / float64(pop25Plus) * 100)
		graduatePct = roundTo2((masters + doctorate) / float64(pop25Plus) * 100)
	}

	// Build location info
	locInfo := LocationInfo{Name: name, Type: query.Type}
	switch query.Type {
	case "state":
		locInfo.FIPS = query.StateFIPS
	case "county":
		locInfo.FIPS = query.StateFIPS + query.CountyFIPS
	case "zip":
		locInfo.Type = "zip_code"
		locInfo.ZipCode = query.ZipCode
	}

	return &AreaStatisticsResponse{
		Location:   locInfo,
		Population: PopulationData{Total: population, MedianAge: medianAge},
		Income:     IncomeData{MedianHousehold: medianIncome},
		Housing: HousingData{
			MedianHomeValue: medianHomeValue,
			MedianRent:      medianRent,
			TotalUnits:      totalHousingUnits,
			VacancyRate:     vacancyRate,
		},
		Education:  EducationData{BachelorsDegree: bachelorsPct, GraduateDegree: graduatePct},
		Employment: EmploymentData{UnemploymentRate: unemploymentRate, LaborForce: laborForce, Unemployed: unemployed, PovertyRate: povertyRate},
		DataYear:   "2023 (ACS 5-Year)",
		Source:     "U.S. Census Bureau - American Community Survey 5-Year Estimates",
	}, nil
}

// sendSuccess sends a successful response with business-specific telemetry
func (t *DemographicsTool) sendSuccess(w http.ResponseWriter, ctx context.Context, operation, requestID string, startTime time.Time, data interface{}) {
	duration := time.Since(startTime)

	telemetry.Histogram("demographics.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", operation,
	)
	telemetry.Counter("demographics.requests.total",
		"capability", operation,
		"status", "success",
	)
	telemetry.RecordToolCall(serviceName, operation, float64(duration.Milliseconds()), "success")

	// Completion span event with business-specific attributes
	switch d := data.(type) {
	case *AreaStatisticsResponse:
		telemetry.AddSpanEvent(ctx, "area_statistics_retrieved",
			attribute.String("request_id", requestID),
			attribute.String("location", d.Location.Name),
			attribute.Int("population", d.Population.Total),
			attribute.Float64("median_income", d.Income.MedianHousehold),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Area statistics request completed", map[string]interface{}{
			"operation":     "area_statistics",
			"request_id":    requestID,
			"location":      d.Location.Name,
			"population":    d.Population.Total,
			"median_income": d.Income.MedianHousehold,
			"source":        d.Source,
			"duration_ms":   duration.Milliseconds(),
		})
	case *CompareAreasResponse:
		telemetry.AddSpanEvent(ctx, "areas_compared",
			attribute.String("request_id", requestID),
			attribute.Int("area_count", len(d.Areas)),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Compare areas request completed", map[string]interface{}{
			"operation":   "compare_areas",
			"request_id":  requestID,
			"area_count":  len(d.Areas),
			"source":      d.Source,
			"duration_ms": duration.Milliseconds(),
		})
	case *PopulationRankingResponse:
		telemetry.AddSpanEvent(ctx, "population_ranking_retrieved",
			attribute.String("request_id", requestID),
			attribute.String("metric", d.Metric),
			attribute.String("order", d.Order),
			attribute.Int("ranking_count", len(d.Rankings)),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Population ranking request completed", map[string]interface{}{
			"operation":     "population_ranking",
			"request_id":    requestID,
			"metric":        d.Metric,
			"order":         d.Order,
			"ranking_count": len(d.Rankings),
			"source":        d.Source,
			"duration_ms":   duration.Milliseconds(),
		})
	case *GlobalDemographicsResponse:
		telemetry.AddSpanEvent(ctx, "global_demographics_retrieved",
			attribute.String("request_id", requestID),
			attribute.String("country", d.Country),
			attribute.String("country_code", d.CountryCode),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Global demographics request completed", map[string]interface{}{
			"operation":    operation,
			"request_id":   requestID,
			"country":      d.Country,
			"country_code": d.CountryCode,
			"source":       d.Source,
			"duration_ms":  duration.Milliseconds(),
		})
	case *CompareCountriesDemoResponse:
		telemetry.AddSpanEvent(ctx, "countries_demographics_compared",
			attribute.String("request_id", requestID),
			attribute.Int("country_count", len(d.Countries)),
			attribute.String("source", d.Source),
			attribute.Int64("duration_ms", duration.Milliseconds()),
		)
		t.Logger.InfoWithContext(ctx, "Compare countries demographics request completed", map[string]interface{}{
			"operation":     operation,
			"request_id":    requestID,
			"country_count": len(d.Countries),
			"source":        d.Source,
			"duration_ms":   duration.Milliseconds(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    data,
	})
}

// sendError sends a structured error response
func (t *DemographicsTool) sendError(w http.ResponseWriter, message string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}

// handleGlobalDemographics returns demographic data for any country via World Bank
func (t *DemographicsTool) handleGlobalDemographics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "global_demographics"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "global_demographics"),
	)

	t.Logger.InfoWithContext(ctx, "Global demographics request received", map[string]interface{}{
		"operation":  "global_demographics",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req GlobalDemographicsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "global_demographics", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Country) == "" {
		telemetry.Counter("demographics.errors.total", "capability", "global_demographics", "error_type", "validation_error")
		t.sendError(w, "country field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	countryCode := resolveCountryCode(req.Country)

	result, err := t.fetchGlobalDemographics(ctx, countryCode, req.Year)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "global_demographics", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "World Bank API failed", map[string]interface{}{
			"operation":    "global_demographics",
			"request_id":   requestID,
			"country_code": countryCode,
			"error":        err.Error(),
		})
		t.sendUpstreamError(w, "World Bank API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.sendSuccess(w, ctx, "global_demographics", requestID, startTime, result)
}

// handleCompareCountriesDemographics compares demographics across multiple countries
func (t *DemographicsTool) handleCompareCountriesDemographics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	var requestID string
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", serviceName),
		attribute.String("truvag3.capability", "compare_countries_demographics"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_countries_demographics"),
	)

	t.Logger.InfoWithContext(ctx, "Compare countries demographics request received", map[string]interface{}{
		"operation":  "compare_countries_demographics",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req CompareCountriesDemoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("demographics.errors.total", "capability", "compare_countries_demographics", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Countries) == "" {
		telemetry.Counter("demographics.errors.total", "capability", "compare_countries_demographics", "error_type", "validation_error")
		t.sendError(w, "countries field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	parts := strings.Split(req.Countries, ",")
	if len(parts) < 2 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_countries_demographics", "error_type", "validation_error")
		t.sendError(w, "at least 2 countries required for comparison", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}
	if len(parts) > 10 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_countries_demographics", "error_type", "validation_error")
		t.sendError(w, "maximum 10 countries allowed", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	var countries []GlobalDemographicsResponse
	for _, part := range parts {
		code := resolveCountryCode(strings.TrimSpace(part))
		result, err := t.fetchGlobalDemographics(ctx, code, req.Year)
		if err != nil {
			t.Logger.WarnWithContext(ctx, "Failed to fetch country demographics, skipping", map[string]interface{}{
				"operation":    "compare_countries_demographics",
				"request_id":   requestID,
				"country_code": code,
				"error":        err.Error(),
			})
			continue
		}
		countries = append(countries, *result)
	}

	if len(countries) == 0 {
		telemetry.Counter("demographics.errors.total", "capability", "compare_countries_demographics", "error_type", "api_error")
		t.sendError(w, "could not retrieve data for any of the specified countries", http.StatusInternalServerError, ErrCodeServiceUnavailable)
		return
	}

	response := &CompareCountriesDemoResponse{
		Countries: countries,
		DataYear:  countries[0].DataYear,
		Source:    "World Bank Open Data",
	}

	t.sendSuccess(w, ctx, "compare_countries_demographics", requestID, startTime, response)
}

// fetchGlobalDemographics fetches all demographic indicators for a single country
func (t *DemographicsTool) fetchGlobalDemographics(ctx context.Context, countryCode, year string) (*GlobalDemographicsResponse, error) {
	// Fetch country info for metadata
	countryInfo, err := t.wbClient.GetCountryInfo(ctx, countryCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get country info: %w", err)
	}

	result := &GlobalDemographicsResponse{
		Country:     countryInfo.Name,
		CountryCode: countryInfo.ID,
		Region:      countryInfo.Region.Value,
		IncomeLevel: countryInfo.IncomeLevel.Value,
		Source:      "World Bank Open Data",
	}

	// Fetch each indicator
	indicators := []string{"SP.POP.TOTL", "SP.DYN.LE00.IN", "SE.ADT.LITR.ZS", "SP.URB.TOTL.IN.ZS", "SP.POP.GROW"}
	for _, ind := range indicators {
		dataPoints, err := t.wbClient.GetIndicator(ctx, countryCode, ind, 5, year)
		if err != nil {
			continue // Skip indicators that fail
		}

		// Find the most recent non-nil value
		val, dataYear := latestNonNilValue(dataPoints)
		if val == nil {
			continue
		}

		if result.DataYear == "" || dataYear > result.DataYear {
			result.DataYear = dataYear
		}

		switch ind {
		case "SP.POP.TOTL":
			result.Population = val
		case "SP.DYN.LE00.IN":
			result.LifeExpectancy = val
		case "SE.ADT.LITR.ZS":
			result.LiteracyRate = val
		case "SP.URB.TOTL.IN.ZS":
			result.UrbanizationRate = val
		case "SP.POP.GROW":
			result.PopulationGrowth = val
		}
	}

	return result, nil
}

// latestNonNilValue returns the most recent non-nil value from World Bank data points
func latestNonNilValue(points []WBDataPoint) (*float64, string) {
	for _, p := range points {
		if p.Value != nil {
			v := *p.Value
			return &v, p.Date
		}
	}
	return nil, ""
}

// Census response parsing helpers

// buildHeaderMap creates a header name → column index map from the Census response header row
func buildHeaderMap(headers []string) map[string]int {
	m := make(map[string]int, len(headers))
	for i, h := range headers {
		m[h] = i
	}
	return m
}

// getByHeader retrieves a value from a Census row by header name
func getByHeader(row []string, headerMap map[string]int, header string) string {
	idx, ok := headerMap[header]
	if !ok || idx >= len(row) {
		return ""
	}
	return row[idx]
}

// parseCensusInt parses a Census string value to int, handling sentinel values
func parseCensusInt(s string) int {
	if s == "" || s == "null" || s == "-666666666" || s == "-999999999" {
		return 0
	}
	val, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return val
}

// parseCensusFloat parses a Census string value to float64, handling sentinel values
func parseCensusFloat(s string) float64 {
	if s == "" || s == "null" || s == "-666666666" || s == "-666666666.0" || s == "-999999999" {
		return 0
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return val
}

// roundTo2 rounds a float64 to 2 decimal places
func roundTo2(v float64) float64 {
	return math.Round(v*100) / 100
}
