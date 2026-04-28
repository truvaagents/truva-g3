package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (c *CurrencyTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

func (c *CurrencyTool) handleConvert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "convert"),
	)

	c.Logger.InfoWithContext(ctx, "Processing currency conversion", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	var req ConvertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate input
	if req.From == "" || req.To == "" {
		c.sendError(w, "Both 'from' and 'to' currencies are required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.Amount <= 0 {
		c.sendError(w, "Amount must be greater than 0", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.From = strings.ToUpper(req.From)
	req.To = strings.ToUpper(req.To)

	c.Logger.InfoWithContext(ctx, "Converting currency", map[string]interface{}{
		"from":   req.From,
		"to":     req.To,
		"amount": req.Amount,
	})

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_frankfurter_api",
		attribute.String("from", req.From),
		attribute.String("to", req.To),
		attribute.Float64("amount", req.Amount),
	)

	result, err := c.callFrankfurterConvert(ctx, req.From, req.To, req.Amount)
	if err != nil {
		// Record error on span for Jaeger visibility
		telemetry.RecordSpanError(ctx, err)
		c.Logger.ErrorWithContext(ctx, "Frankfurter API call failed", map[string]interface{}{
			"error": err.Error(),
		})
		if strings.Contains(err.Error(), "not found") {
			c.sendError(w, err.Error(), http.StatusBadRequest, ErrCodeInvalidCurrency)
		} else {
			c.sendUpstreamError(w, "Currency API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		}
		return
	}

	c.Logger.InfoWithContext(ctx, "Currency conversion successful", map[string]interface{}{
		"from":   req.From,
		"to":     req.To,
		"amount": req.Amount,
		"result": result.Result,
		"rate":   result.Rate,
	})

	// Add success span event
	telemetry.AddSpanEvent(ctx, "currency_converted",
		attribute.String("from", req.From),
		attribute.String("to", req.To),
		attribute.Float64("amount", req.Amount),
		attribute.Float64("result", result.Result),
		attribute.Float64("rate", result.Rate),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

func (c *CurrencyTool) handleRates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "rates"),
	)

	c.Logger.InfoWithContext(ctx, "Processing exchange rates request", map[string]interface{}{
		"method": r.Method,
		"path":   r.URL.Path,
	})

	var req RatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Base == "" {
		c.sendError(w, "Base currency is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	req.Base = strings.ToUpper(req.Base)

	c.Logger.InfoWithContext(ctx, "Fetching exchange rates", map[string]interface{}{
		"base":       req.Base,
		"currencies": req.Currencies,
	})

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_frankfurter_api",
		attribute.String("base", req.Base),
		attribute.Int("currencies_count", len(req.Currencies)),
	)

	result, err := c.callFrankfurterRates(ctx, req.Base, req.Currencies)
	if err != nil {
		// Record error on span for Jaeger visibility
		telemetry.RecordSpanError(ctx, err)
		c.Logger.ErrorWithContext(ctx, "Frankfurter API call failed", map[string]interface{}{
			"error": err.Error(),
		})
		if strings.Contains(err.Error(), "not found") {
			c.sendError(w, err.Error(), http.StatusBadRequest, ErrCodeInvalidCurrency)
		} else {
			c.sendUpstreamError(w, "Currency API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		}
		return
	}

	c.Logger.InfoWithContext(ctx, "Exchange rates retrieved", map[string]interface{}{
		"base":        req.Base,
		"rates_count": len(result.Rates),
	})

	// Add success span event
	telemetry.AddSpanEvent(ctx, "exchange_rates_retrieved",
		attribute.String("base", req.Base),
		attribute.Int("rates_count", len(result.Rates)),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

func (c *CurrencyTool) callFrankfurterConvert(ctx context.Context, from, to string, amount float64) (*ConvertResponse, error) {
	// https://api.frankfurter.dev/latest?amount=1000&from=USD&to=JPY
	reqURL := fmt.Sprintf("%s/latest?amount=%.2f&from=%s&to=%s",
		FrankfurterBaseURL, amount, from, to)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-CurrencyTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("API error 404: currency not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp FrankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	rate, ok := apiResp.Rates[to]
	if !ok {
		return nil, fmt.Errorf("target currency %s not found in response", to)
	}

	return &ConvertResponse{
		From:   from,
		To:     to,
		Amount: amount,
		Result: rate,
		Rate:   rate / amount,
		Date:   apiResp.Date,
	}, nil
}

func (c *CurrencyTool) callFrankfurterRates(ctx context.Context, base string, currencies []string) (*RatesResponse, error) {
	reqURL := fmt.Sprintf("%s/latest?from=%s", FrankfurterBaseURL, base)

	if len(currencies) > 0 {
		reqURL += "&to=" + strings.Join(currencies, ",")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-CurrencyTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("API error 404: base currency not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp FrankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &RatesResponse{
		Base:  base,
		Date:  apiResp.Date,
		Rates: apiResp.Rates,
	}, nil
}

func (c *CurrencyTool) sendError(w http.ResponseWriter, message string, status int, code string) {
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
