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

// FREDClient handles communication with the Federal Reserve Economic Data API
type FREDClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// FRED API response types

type FREDObservationsResponse struct {
	RealtimeStart    string            `json:"realtime_start"`
	RealtimeEnd      string            `json:"realtime_end"`
	ObservationStart string            `json:"observation_start"`
	ObservationEnd   string            `json:"observation_end"`
	Units            string            `json:"units"`
	OutputType       int               `json:"output_type"`
	FileType         string            `json:"file_type"`
	OrderBy          string            `json:"order_by"`
	SortOrder        string            `json:"sort_order"`
	Count            int               `json:"count"`
	Offset           int               `json:"offset"`
	Limit            int               `json:"limit"`
	Observations     []FREDObservation `json:"observations"`
}

type FREDObservation struct {
	RealtimeStart string `json:"realtime_start"`
	RealtimeEnd   string `json:"realtime_end"`
	Date          string `json:"date"`
	Value         string `json:"value"`
}

type FREDSearchResponse struct {
	RealtimeStart string       `json:"realtime_start"`
	RealtimeEnd   string       `json:"realtime_end"`
	OrderBy       string       `json:"order_by"`
	SortOrder     string       `json:"sort_order"`
	Count         int          `json:"count"`
	Offset        int          `json:"offset"`
	Limit         int          `json:"limit"`
	Seriess       []FREDSeries `json:"seriess"`
}

type FREDSeriesResponse struct {
	RealtimeStart string       `json:"realtime_start"`
	RealtimeEnd   string       `json:"realtime_end"`
	Seriess       []FREDSeries `json:"seriess"`
}

type FREDSeries struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	ObservationStart   string `json:"observation_start"`
	ObservationEnd     string `json:"observation_end"`
	Frequency          string `json:"frequency"`
	FrequencyShort     string `json:"frequency_short"`
	Units              string `json:"units"`
	UnitsShort         string `json:"units_short"`
	SeasonalAdjustment string `json:"seasonal_adjustment"`
	SeasonalAdjShort   string `json:"seasonal_adjustment_short"`
	LastUpdated        string `json:"last_updated"`
	Popularity         int    `json:"popularity"`
	Notes              string `json:"notes"`
}

// NewFREDClient creates a configured API client with distributed tracing
func NewFREDClient(apiKey string) *FREDClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &FREDClient{
		apiKey:     apiKey,
		baseURL:    "https://api.stlouisfed.org/fred",
		httpClient: tracedClient,
	}
}

// GetObservations returns data values for an economic time series
func (c *FREDClient) GetObservations(ctx context.Context, seriesID string, limit int, startDate, endDate string) (*FREDObservationsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("FRED API key not configured")
	}

	params := url.Values{}
	params.Set("series_id", seriesID)
	params.Set("api_key", c.apiKey)
	params.Set("file_type", "json")
	params.Set("sort_order", "desc")
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}
	if startDate != "" {
		params.Set("observation_start", startDate)
	}
	if endDate != "" {
		params.Set("observation_end", endDate)
	}

	endpoint := fmt.Sprintf("%s/series/observations?%s", c.baseURL, params.Encode())
	return doFREDRequest[FREDObservationsResponse](ctx, c, endpoint)
}

// SearchSeries searches FRED for economic data series by keyword
func (c *FREDClient) SearchSeries(ctx context.Context, query string, limit int) (*FREDSearchResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("FRED API key not configured")
	}

	params := url.Values{}
	params.Set("search_text", query)
	params.Set("api_key", c.apiKey)
	params.Set("file_type", "json")
	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}

	endpoint := fmt.Sprintf("%s/series/search?%s", c.baseURL, params.Encode())
	return doFREDRequest[FREDSearchResponse](ctx, c, endpoint)
}

// GetSeriesInfo returns metadata about a specific series
func (c *FREDClient) GetSeriesInfo(ctx context.Context, seriesID string) (*FREDSeriesResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("FRED API key not configured")
	}

	params := url.Values{}
	params.Set("series_id", seriesID)
	params.Set("api_key", c.apiKey)
	params.Set("file_type", "json")

	endpoint := fmt.Sprintf("%s/series?%s", c.baseURL, params.Encode())
	return doFREDRequest[FREDSeriesResponse](ctx, c, endpoint)
}

// doFREDRequest executes an HTTP request and decodes the response into the specified type
func doFREDRequest[T any](ctx context.Context, c *FREDClient, endpoint string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-EconomicDataTool/1.0")
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

	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
