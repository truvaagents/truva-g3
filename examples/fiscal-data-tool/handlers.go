package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const serviceName = "fiscal-data-tool"

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *FiscalDataTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// handleNationalDebt returns the current U.S. national debt
func (t *FiscalDataTool) handleNationalDebt(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "national_debt"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "national_debt"),
	)

	t.Logger.InfoWithContext(ctx, "National debt request received", map[string]interface{}{
		"operation":  "national_debt",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req NationalDebtRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "national_debt", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "national_debt",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Limit > 100 {
		req.Limit = 100
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetDebtToPenny(ctx, req.Limit, req.StartDate)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("fiscal.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "national_debt", "api", "debt_to_penny")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "national_debt", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Treasury API failed", map[string]interface{}{
			"operation":   "national_debt",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "api_error",
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "Treasury API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil || len(apiResp.Data) == 0 {
		telemetry.Counter("fiscal.errors.total", "capability", "national_debt", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "Treasury API returned no data", map[string]interface{}{
			"operation":   "national_debt",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No national debt data returned from Treasury API", http.StatusBadGateway, "API_ERROR")
		return
	}

	records := make([]DebtRecord, 0, len(apiResp.Data))
	for _, row := range apiResp.Data {
		records = append(records, DebtRecord{
			Date:             getStringField(row, "record_date"),
			TotalPublicDebt:  parseFloat(row, "tot_pub_debt_out_amt", t, ctx, requestID, "national_debt"),
			DebtHeldByPublic: parseFloat(row, "debt_held_public_amt", t, ctx, requestID, "national_debt"),
			IntragovHoldings: parseFloat(row, "intragov_hold_amt", t, ctx, requestID, "national_debt"),
			FiscalYear:       getStringField(row, "record_fiscal_year"),
			FiscalQuarter:    getStringField(row, "record_fiscal_quarter"),
		})
	}

	result := &NationalDebtResponse{
		Records: records,
		Source:  "U.S. Treasury Fiscal Data API",
	}

	t.sendSuccess(w, ctx, "national_debt", requestID, startTime, result)
}

// handleTreasuryRates returns average interest rates on Treasury securities
func (t *FiscalDataTool) handleTreasuryRates(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "treasury_rates"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "treasury_rates"),
	)

	t.Logger.InfoWithContext(ctx, "Treasury rates request received", map[string]interface{}{
		"operation":  "treasury_rates",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req TreasuryRatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "treasury_rates", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "treasury_rates",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetAvgInterestRates(ctx, req.SecurityType, req.Limit, req.StartDate)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("fiscal.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "treasury_rates", "api", "avg_interest_rates")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "treasury_rates", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Treasury API failed", map[string]interface{}{
			"operation":   "treasury_rates",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "api_error",
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "Treasury API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil || len(apiResp.Data) == 0 {
		telemetry.Counter("fiscal.errors.total", "capability", "treasury_rates", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "Treasury API returned no data", map[string]interface{}{
			"operation":   "treasury_rates",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No treasury rates data returned from Treasury API", http.StatusBadGateway, "API_ERROR")
		return
	}

	records := make([]TreasuryRateRecord, 0, len(apiResp.Data))
	for _, row := range apiResp.Data {
		records = append(records, TreasuryRateRecord{
			Date:            getStringField(row, "record_date"),
			SecurityType:    getStringField(row, "security_type_desc"),
			SecurityDesc:    getStringField(row, "security_desc"),
			AvgInterestRate: parseFloat(row, "avg_interest_rate_amt", t, ctx, requestID, "treasury_rates"),
			FiscalYear:      getStringField(row, "record_fiscal_year"),
		})
	}

	result := &TreasuryRatesResponse{
		Records: records,
		Source:  "U.S. Treasury Fiscal Data API",
	}

	t.sendSuccess(w, ctx, "treasury_rates", requestID, startTime, result)
}

// handleExchangeRates returns Treasury exchange rates for foreign currencies
func (t *FiscalDataTool) handleExchangeRates(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "exchange_rates"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "exchange_rates"),
	)

	t.Logger.InfoWithContext(ctx, "Exchange rates request received", map[string]interface{}{
		"operation":  "exchange_rates",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req ExchangeRatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "exchange_rates", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "exchange_rates",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetExchangeRates(ctx, req.Currencies, req.Limit, req.StartDate)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("fiscal.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "exchange_rates", "api", "exchange_rates")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "exchange_rates", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Treasury API failed", map[string]interface{}{
			"operation":   "exchange_rates",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "api_error",
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "Treasury API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil || len(apiResp.Data) == 0 {
		telemetry.Counter("fiscal.errors.total", "capability", "exchange_rates", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "Treasury API returned no data", map[string]interface{}{
			"operation":   "exchange_rates",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No exchange rates data returned from Treasury API", http.StatusBadGateway, "API_ERROR")
		return
	}

	records := make([]ExchangeRateRecord, 0, len(apiResp.Data))
	for _, row := range apiResp.Data {
		records = append(records, ExchangeRateRecord{
			Date:          getStringField(row, "record_date"),
			Country:       getStringField(row, "country_currency_desc"),
			ExchangeRate:  parseFloat(row, "exchange_rate", t, ctx, requestID, "exchange_rates"),
			EffectiveDate: getStringField(row, "effective_date"),
		})
	}

	result := &ExchangeRatesResponse{
		Records: records,
		Source:  "U.S. Treasury Fiscal Data API",
	}

	t.sendSuccess(w, ctx, "exchange_rates", requestID, startTime, result)
}

// handleFederalSpending returns federal receipts and outlays
func (t *FiscalDataTool) handleFederalSpending(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "federal_spending"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "federal_spending"),
	)

	t.Logger.InfoWithContext(ctx, "Federal spending request received", map[string]interface{}{
		"operation":  "federal_spending",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req FederalSpendingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "federal_spending", "error_type", "decode_error")
		t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":   "federal_spending",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "decode_error",
			"duration_ms": time.Since(startTime).Milliseconds(),
		})
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	apiStart := time.Now()
	apiResp, err := t.client.GetMonthlyTreasuryStatement(ctx, req.Limit, req.StartDate)
	apiDuration := time.Since(apiStart)

	telemetry.Histogram("fiscal.api.duration_ms", float64(apiDuration.Milliseconds()), "capability", "federal_spending", "api", "monthly_treasury_statement")

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "federal_spending", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "Treasury API failed", map[string]interface{}{
			"operation":   "federal_spending",
			"request_id":  requestID,
			"error":       err.Error(),
			"error_type":  "api_error",
			"api_latency": apiDuration.String(),
		})
		t.sendUpstreamError(w, "Treasury API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}
	if apiResp == nil || len(apiResp.Data) == 0 {
		telemetry.Counter("fiscal.errors.total", "capability", "federal_spending", "error_type", "no_data")
		t.Logger.ErrorWithContext(ctx, "Treasury API returned no data", map[string]interface{}{
			"operation":   "federal_spending",
			"request_id":  requestID,
			"api_latency": apiDuration.String(),
		})
		t.sendError(w, "No federal spending data returned from Treasury API", http.StatusBadGateway, "API_ERROR")
		return
	}

	records := make([]SpendingRecord, 0, len(apiResp.Data))
	for _, row := range apiResp.Data {
		receipts := parseFloat(row, "current_month_receipts_amt", t, ctx, requestID, "federal_spending")
		outlays := parseFloat(row, "current_month_outlays_amt", t, ctx, requestID, "federal_spending")
		records = append(records, SpendingRecord{
			Date:             getStringField(row, "record_date"),
			FiscalYear:       getStringField(row, "record_fiscal_year"),
			FiscalMonth:      getStringField(row, "record_calendar_month"),
			Receipts:         receipts,
			Outlays:          outlays,
			SurplusOrDeficit: receipts - outlays,
		})
	}

	result := &FederalSpendingResponse{
		Records: records,
		Source:  "U.S. Treasury Fiscal Data API",
	}

	t.sendSuccess(w, ctx, "federal_spending", requestID, startTime, result)
}

// handleGlobalFiscalData returns government fiscal data for any country via World Bank
func (t *FiscalDataTool) handleGlobalFiscalData(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "global_fiscal_data"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "global_fiscal_data"),
	)

	t.Logger.InfoWithContext(ctx, "Global fiscal data request received", map[string]interface{}{
		"operation":  "global_fiscal_data",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req GlobalFiscalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "global_fiscal_data", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Country) == "" {
		telemetry.Counter("fiscal.errors.total", "capability", "global_fiscal_data", "error_type", "validation_error")
		t.sendError(w, "country field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	countryCode := resolveCountryCode(req.Country)

	result, err := t.fetchGlobalFiscal(ctx, countryCode, req.Year)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "global_fiscal_data", "error_type", "api_error")
		t.Logger.ErrorWithContext(ctx, "World Bank API failed", map[string]interface{}{
			"operation":    "global_fiscal_data",
			"request_id":   requestID,
			"country_code": countryCode,
			"error":        err.Error(),
		})
		t.sendUpstreamError(w, "World Bank API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	t.sendSuccess(w, ctx, "global_fiscal_data", requestID, startTime, result)
}

// handleCompareCountryFiscal compares fiscal indicators across multiple countries
func (t *FiscalDataTool) handleCompareCountryFiscal(w http.ResponseWriter, r *http.Request) {
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
		attribute.String("truvag3.capability", "compare_country_fiscal"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "compare_country_fiscal"),
	)

	t.Logger.InfoWithContext(ctx, "Compare country fiscal request received", map[string]interface{}{
		"operation":  "compare_country_fiscal",
		"request_id": requestID,
		"method":     r.Method,
		"path":       r.URL.Path,
	})

	var req CompareCountryFiscalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("fiscal.errors.total", "capability", "compare_country_fiscal", "error_type", "decode_error")
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if strings.TrimSpace(req.Countries) == "" {
		telemetry.Counter("fiscal.errors.total", "capability", "compare_country_fiscal", "error_type", "validation_error")
		t.sendError(w, "countries field is required", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	parts := strings.Split(req.Countries, ",")
	if len(parts) < 2 {
		telemetry.Counter("fiscal.errors.total", "capability", "compare_country_fiscal", "error_type", "validation_error")
		t.sendError(w, "at least 2 countries required for comparison", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}
	if len(parts) > 10 {
		telemetry.Counter("fiscal.errors.total", "capability", "compare_country_fiscal", "error_type", "validation_error")
		t.sendError(w, "maximum 10 countries allowed", http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	var countries []GlobalFiscalResponse
	for _, part := range parts {
		code := resolveCountryCode(strings.TrimSpace(part))
		result, err := t.fetchGlobalFiscal(ctx, code, req.Year)
		if err != nil {
			t.Logger.WarnWithContext(ctx, "Failed to fetch country fiscal data, skipping", map[string]interface{}{
				"operation":    "compare_country_fiscal",
				"request_id":   requestID,
				"country_code": code,
				"error":        err.Error(),
			})
			continue
		}
		countries = append(countries, *result)
	}

	if len(countries) == 0 {
		telemetry.Counter("fiscal.errors.total", "capability", "compare_country_fiscal", "error_type", "api_error")
		t.sendError(w, "could not retrieve data for any of the specified countries", http.StatusInternalServerError, ErrCodeServiceUnavailable)
		return
	}

	response := &CompareCountryFiscalResponse{
		Countries: countries,
		DataYear:  countries[0].DataYear,
		Source:    "World Bank Open Data",
	}

	t.sendSuccess(w, ctx, "compare_country_fiscal", requestID, startTime, response)
}

// fetchGlobalFiscal fetches all fiscal indicators for a single country
func (t *FiscalDataTool) fetchGlobalFiscal(ctx context.Context, countryCode, year string) (*GlobalFiscalResponse, error) {
	countryInfo, err := t.wbClient.GetCountryInfo(ctx, countryCode)
	if err != nil {
		return nil, fmt.Errorf("failed to get country info: %w", err)
	}

	result := &GlobalFiscalResponse{
		Country:     countryInfo.Name,
		CountryCode: countryInfo.ID,
		Region:      countryInfo.Region.Value,
		IncomeLevel: countryInfo.IncomeLevel.Value,
		Source:      "World Bank Open Data",
	}

	// Fetch each indicator: Debt-to-GDP, Revenue-to-GDP, Expenditure-to-GDP
	indicators := []string{"GC.DOD.TOTL.GD.ZS", "GC.REV.XGRT.GD.ZS", "GC.XPN.TOTL.GD.ZS"}
	for _, ind := range indicators {
		dataPoints, err := t.wbClient.GetIndicator(ctx, countryCode, ind, 5, year)
		if err != nil {
			continue
		}

		val, dataYear := latestNonNilValue(dataPoints)
		if val == nil {
			continue
		}

		if result.DataYear == "" || dataYear > result.DataYear {
			result.DataYear = dataYear
		}

		switch ind {
		case "GC.DOD.TOTL.GD.ZS":
			result.DebtToGDPPct = val
		case "GC.REV.XGRT.GD.ZS":
			result.RevenueToGDPPct = val
		case "GC.XPN.TOTL.GD.ZS":
			result.ExpenditureToGDPPct = val
		}
	}

	return result, nil
}

// sendSuccess sends a successful response with business-specific telemetry
func (t *FiscalDataTool) sendSuccess(w http.ResponseWriter, ctx context.Context, operation, requestID string, startTime time.Time, data interface{}) {
	duration := time.Since(startTime)

	telemetry.Histogram("fiscal.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", operation,
	)
	telemetry.Counter("fiscal.requests.total",
		"capability", operation,
		"status", "success",
	)
	telemetry.RecordToolCall(serviceName, operation, float64(duration.Milliseconds()), "success")

	// Extract business-specific attributes per response type
	var recordCount int
	var dataSource string
	switch d := data.(type) {
	case *NationalDebtResponse:
		recordCount = len(d.Records)
		dataSource = d.Source
	case *TreasuryRatesResponse:
		recordCount = len(d.Records)
		dataSource = d.Source
	case *ExchangeRatesResponse:
		recordCount = len(d.Records)
		dataSource = d.Source
	case *FederalSpendingResponse:
		recordCount = len(d.Records)
		dataSource = d.Source
	case *GlobalFiscalResponse:
		recordCount = 1
		dataSource = d.Source
	case *CompareCountryFiscalResponse:
		recordCount = len(d.Countries)
		dataSource = d.Source
	}

	// Completion span event with business-specific attributes
	telemetry.AddSpanEvent(ctx, operation+"_retrieved",
		attribute.String("request_id", requestID),
		attribute.Int("record_count", recordCount),
		attribute.String("data_source", dataSource),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	t.Logger.InfoWithContext(ctx, "Request completed", map[string]interface{}{
		"operation":    operation,
		"request_id":   requestID,
		"record_count": recordCount,
		"data_source":  dataSource,
		"duration_ms":  duration.Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    data,
	})
}

// sendError sends a structured error response
func (t *FiscalDataTool) sendError(w http.ResponseWriter, message string, status int, code string) {
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

// Helper functions for parsing Treasury API map responses

func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func parseFloat(m map[string]interface{}, key string, t *FiscalDataTool, ctx context.Context, requestID, operation string) float64 {
	raw := getStringField(m, key)
	if raw == "" || raw == "null" {
		return 0
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Logger.InfoWithContext(ctx, "Failed to parse Treasury value", map[string]interface{}{
			"operation":  operation,
			"request_id": requestID,
			"field":      key,
			"raw_value":  raw,
			"error":      err.Error(),
		})
		return 0
	}
	return val
}
