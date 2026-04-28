package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// TreasuryClient handles communication with the U.S. Treasury Fiscal Data API
type TreasuryClient struct {
	baseURL    string
	httpClient *http.Client
}

// TreasuryAPIResponse represents the generic Treasury API response format
type TreasuryAPIResponse struct {
	Data []map[string]interface{} `json:"data"`
	Meta TreasuryMeta             `json:"meta"`
}

// TreasuryMeta holds pagination metadata from the Treasury API
type TreasuryMeta struct {
	Count      int `json:"count"`
	TotalCount int `json:"total-count"`
	TotalPages int `json:"total-pages"`
}

// NewTreasuryClient creates a configured API client with distributed tracing
func NewTreasuryClient() *TreasuryClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &TreasuryClient{
		baseURL:    "https://api.fiscaldata.treasury.gov/services/api/fiscal_service",
		httpClient: tracedClient,
	}
}

// doRequest executes an HTTP request with context and parses the response
func (c *TreasuryClient) doRequest(ctx context.Context, endpoint string) (*TreasuryAPIResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-FiscalDataTool/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result TreasuryAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// buildURL constructs a Treasury API URL with query parameters
func (c *TreasuryClient) buildURL(path string, pageSize int, startDate string, extraFilters ...string) string {
	params := fmt.Sprintf("sort=-record_date&page[size]=%d", pageSize)
	filters := make([]string, 0)
	if startDate != "" {
		filters = append(filters, fmt.Sprintf("record_date:gte:%s", startDate))
	}
	filters = append(filters, extraFilters...)
	if len(filters) > 0 {
		params += "&filter=" + strings.Join(filters, ",")
	}
	return fmt.Sprintf("%s/%s?%s", c.baseURL, path, params)
}

// GetDebtToPenny returns the daily total public debt outstanding
func (c *TreasuryClient) GetDebtToPenny(ctx context.Context, limit int, startDate string) (*TreasuryAPIResponse, error) {
	if limit <= 0 {
		limit = 1
	}
	endpoint := c.buildURL("v2/accounting/od/debt_to_penny", limit, startDate)
	return c.doRequest(ctx, endpoint)
}

// GetAvgInterestRates returns average interest rates on Treasury securities
func (c *TreasuryClient) GetAvgInterestRates(ctx context.Context, securityType string, limit int, startDate string) (*TreasuryAPIResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	var extraFilters []string
	if securityType != "" {
		extraFilters = append(extraFilters, fmt.Sprintf("security_desc:eq:%s", securityType))
	}
	endpoint := c.buildURL("v2/accounting/od/avg_interest_rates", limit, startDate, extraFilters...)
	return c.doRequest(ctx, endpoint)
}

// GetExchangeRates returns Treasury Reporting Rates of Exchange
func (c *TreasuryClient) GetExchangeRates(ctx context.Context, currencies string, limit int, startDate string) (*TreasuryAPIResponse, error) {
	if limit <= 0 {
		limit = 10
	}
	var extraFilters []string
	if currencies != "" {
		parts := strings.Split(currencies, ",")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
		}
		extraFilters = append(extraFilters, fmt.Sprintf("country_currency_desc:in:(%s)", strings.Join(parts, ",")))
	}
	endpoint := c.buildURL("v1/accounting/od/rates_of_exchange", limit, startDate, extraFilters...)
	return c.doRequest(ctx, endpoint)
}

// GetMonthlyTreasuryStatement returns federal receipts and outlays
func (c *TreasuryClient) GetMonthlyTreasuryStatement(ctx context.Context, limit int, startDate string) (*TreasuryAPIResponse, error) {
	if limit <= 0 {
		limit = 12
	}
	endpoint := c.buildURL("v1/accounting/mts/mts_table_5", limit, startDate, "line_code_nbr:eq:5694")
	return c.doRequest(ctx, endpoint)
}
