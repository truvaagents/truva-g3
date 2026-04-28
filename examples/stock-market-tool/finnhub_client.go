package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	finnhubBaseURL = "https://finnhub.io/api/v1"
)

// FinnhubClient handles API communication with Finnhub.io
type FinnhubClient struct {
	apiKey     string
	httpClient *http.Client
}

// FinnhubQuote represents the raw API response for stock quote
type FinnhubQuote struct {
	C  float64 `json:"c"`  // Current price
	D  float64 `json:"d"`  // Change
	DP float64 `json:"dp"` // Percent change
	H  float64 `json:"h"`  // High price of the day
	L  float64 `json:"l"`  // Low price of the day
	O  float64 `json:"o"`  // Open price of the day
	PC float64 `json:"pc"` // Previous close price
	T  int64   `json:"t"`  // Timestamp
}

// FinnhubCompanyProfile represents the raw API response for company profile
type FinnhubCompanyProfile struct {
	Country              string  `json:"country"`
	Currency             string  `json:"currency"`
	Exchange             string  `json:"exchange"`
	FinnhubIndustry      string  `json:"finnhubIndustry"`
	IPO                  string  `json:"ipo"`
	Logo                 string  `json:"logo"`
	MarketCapitalization float64 `json:"marketCapitalization"`
	Name                 string  `json:"name"`
	Phone                string  `json:"phone"`
	ShareOutstanding     float64 `json:"shareOutstanding"`
	Ticker               string  `json:"ticker"`
	Weburl               string  `json:"weburl"`
}

// FinnhubNewsItem represents a single news article from the API
type FinnhubNewsItem struct {
	Category string `json:"category"`
	Datetime int64  `json:"datetime"`
	Headline string `json:"headline"`
	ID       int64  `json:"id"`
	Image    string `json:"image"`
	Related  string `json:"related"`
	Source   string `json:"source"`
	Summary  string `json:"summary"`
	URL      string `json:"url"`
}

// FinnhubMetrics represents the raw API response for basic financials
// This is a complex nested structure with 100+ financial metrics
type FinnhubMetrics struct {
	Metric     map[string]interface{} `json:"metric"`
	MetricType string                 `json:"metricType"`
	Series     FinnhubSeries          `json:"series"`
	Symbol     string                 `json:"symbol"`
}

// FinnhubSeries contains annual and quarterly historical data
type FinnhubSeries struct {
	Annual    map[string][]FinnhubPeriodValue `json:"annual"`
	Quarterly map[string][]FinnhubPeriodValue `json:"quarterly"`
}

// FinnhubPeriodValue represents a single period value in the series
type FinnhubPeriodValue struct {
	Period string  `json:"period"`
	V      float64 `json:"v"`
}

// FinnhubEarnings represents the raw API response for company earnings
type FinnhubEarnings struct {
	Actual          float64 `json:"actual"`
	Estimate        float64 `json:"estimate"`
	Period          string  `json:"period"`
	Quarter         int     `json:"quarter"`
	Surprise        float64 `json:"surprise"`
	SurprisePercent float64 `json:"surprisePercent"`
	Symbol          string  `json:"symbol"`
	Year            int     `json:"year"`
}

// FinnhubFinancialsReported represents the raw API response for SEC filings
type FinnhubFinancialsReported struct {
	CIK    string              `json:"cik"`
	Data   []FinnhubFilingData `json:"data"`
	Symbol string              `json:"symbol"`
}

// FinnhubFilingData represents a single SEC filing
type FinnhubFilingData struct {
	AccessNumber string              `json:"accessNumber"`
	Symbol       string              `json:"symbol"`
	CIK          string              `json:"cik"`
	Year         int                 `json:"year"`
	Quarter      int                 `json:"quarter"`
	Form         string              `json:"form"`
	StartDate    string              `json:"startDate"`
	EndDate      string              `json:"endDate"`
	FiledDate    string              `json:"filedDate"`
	AcceptedDate string              `json:"acceptedDate"`
	Report       FinnhubFilingReport `json:"report"`
}

// FinnhubFilingReport contains the financial statements from a filing
type FinnhubFilingReport struct {
	BS []FinnhubLineItem `json:"bs"` // Balance Sheet
	IC []FinnhubLineItem `json:"ic"` // Income Statement
	CF []FinnhubLineItem `json:"cf"` // Cash Flow
}

// FinnhubLineItem represents a single line item in a financial statement
type FinnhubLineItem struct {
	Concept string  `json:"concept"` // GAAP concept ID (e.g., "us-gaap_Revenues")
	Label   string  `json:"label"`   // Human-readable name
	Value   float64 `json:"value"`
	Unit    string  `json:"unit"`
}

// AnnualRevenue represents extracted annual revenue from 10-K filings
type AnnualRevenue struct {
	Year      int     `json:"year"`
	Form      string  `json:"form"`
	Revenue   float64 `json:"revenue"`
	FiledDate string  `json:"filed_date"`
}

// NewFinnhubClient creates a new Finnhub API client with traced HTTP client
// for distributed tracing visibility into external API calls.
// Even though Finnhub won't understand traceparent headers, using TracedHTTPClient
// provides client-side span visibility in Jaeger showing exact API call durations.
func NewFinnhubClient(apiKey string) *FinnhubClient {
	// Use TracedHTTPClientWithTransport for client-side span visibility
	// This creates spans for each Finnhub API call, visible in Jaeger
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 10 * time.Second

	return &FinnhubClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// GetStockQuote fetches real-time quote for a given symbol
func (c *FinnhubClient) GetStockQuote(ctx context.Context, symbol string) (*FinnhubQuote, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	endpoint := fmt.Sprintf("%s/quote", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var quote FinnhubQuote
	if err := json.NewDecoder(resp.Body).Decode(&quote); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check if the quote has valid data (c = current price should be > 0)
	if quote.C == 0 {
		return nil, fmt.Errorf("API error 404: invalid symbol or no data available")
	}

	return &quote, nil
}

// GetCompanyProfile fetches company information for a given symbol
func (c *FinnhubClient) GetCompanyProfile(ctx context.Context, symbol string) (*FinnhubCompanyProfile, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	endpoint := fmt.Sprintf("%s/stock/profile2", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var profile FinnhubCompanyProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check if profile has valid data
	if profile.Name == "" {
		return nil, fmt.Errorf("API error 404: invalid symbol or no data available")
	}

	return &profile, nil
}

// GetCompanyNews fetches news articles for a specific company
func (c *FinnhubClient) GetCompanyNews(ctx context.Context, symbol, from, to string) ([]FinnhubNewsItem, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	// Default to last 7 days if dates not provided
	if from == "" || to == "" {
		now := time.Now()
		to = now.Format("2006-01-02")
		from = now.AddDate(0, 0, -7).Format("2006-01-02")
	}

	endpoint := fmt.Sprintf("%s/company-news", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("from", from)
	params.Add("to", to)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var news []FinnhubNewsItem
	if err := json.NewDecoder(resp.Body).Decode(&news); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return news, nil
}

// GetMarketNews fetches general market news
func (c *FinnhubClient) GetMarketNews(ctx context.Context, category string) ([]FinnhubNewsItem, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	// Default to general if not specified
	if category == "" {
		category = "general"
	}

	endpoint := fmt.Sprintf("%s/news", finnhubBaseURL)

	params := url.Values{}
	params.Add("category", category)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var news []FinnhubNewsItem
	if err := json.NewDecoder(resp.Body).Decode(&news); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return news, nil
}

// GetBasicFinancials fetches comprehensive financial metrics for a symbol
// Returns 100+ metrics including PE ratios, margins, growth rates, and valuation metrics
func (c *FinnhubClient) GetBasicFinancials(ctx context.Context, symbol string) (*FinnhubMetrics, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	endpoint := fmt.Sprintf("%s/stock/metric", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("metric", "all")
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var metrics FinnhubMetrics
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Check if metrics has valid data
	if len(metrics.Metric) == 0 {
		return nil, fmt.Errorf("API error 404: invalid symbol or no data available")
	}

	return &metrics, nil
}

// GetCompanyEarnings fetches historical quarterly earnings for a symbol
// Returns actual vs estimated EPS and earnings surprises
func (c *FinnhubClient) GetCompanyEarnings(ctx context.Context, symbol string) ([]FinnhubEarnings, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	endpoint := fmt.Sprintf("%s/stock/earnings", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var earnings []FinnhubEarnings
	if err := json.NewDecoder(resp.Body).Decode(&earnings); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return earnings, nil
}

// GetAnnualRevenue fetches annual revenue from SEC 10-K filings
// Free tier only provides annual 10-K filings, not quarterly 10-Q
func (c *FinnhubClient) GetAnnualRevenue(ctx context.Context, symbol string) ([]AnnualRevenue, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Finnhub API key not configured")
	}

	endpoint := fmt.Sprintf("%s/stock/financials-reported", finnhubBaseURL)

	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("token", c.apiKey)

	fullURL := fmt.Sprintf("%s?%s", endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var financials FinnhubFinancialsReported
	if err := json.NewDecoder(resp.Body).Decode(&financials); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract annual revenue from 10-K filings
	var revenues []AnnualRevenue
	for _, filing := range financials.Data {
		// Only process annual 10-K filings
		if filing.Form != "10-K" {
			continue
		}

		// Look for revenue in income statement
		var revenueValue float64
		for _, item := range filing.Report.IC {
			// Check for common revenue concepts
			if item.Concept == "us-gaap_Revenues" ||
				item.Concept == "us-gaap_RevenueFromContractWithCustomerExcludingAssessedTax" ||
				item.Concept == "us-gaap_SalesRevenueNet" ||
				item.Concept == "us-gaap_RevenueFromContractWithCustomerIncludingAssessedTax" {
				revenueValue = item.Value
				break
			}
		}

		// Only add if we found revenue
		if revenueValue > 0 {
			revenues = append(revenues, AnnualRevenue{
				Year:      filing.Year,
				Form:      filing.Form,
				Revenue:   revenueValue,
				FiledDate: filing.FiledDate,
			})
		}
	}

	if len(revenues) == 0 {
		return nil, fmt.Errorf("no annual revenue data found for symbol %s", symbol)
	}

	return revenues, nil
}
