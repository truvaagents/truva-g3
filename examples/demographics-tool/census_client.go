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

// CensusClient handles communication with the U.S. Census Bureau API
type CensusClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewCensusClient creates a configured API client with distributed tracing
func NewCensusClient(apiKey string) *CensusClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &CensusClient{
		apiKey:     apiKey,
		baseURL:    "https://api.census.gov/data/2023/acs/acs5",
		httpClient: tracedClient,
	}
}

// doRequest executes a Census API request and returns the 2D string array response
func (c *CensusClient) doRequest(ctx context.Context, endpoint string) ([][]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-DemographicsTool/1.0")
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

	var result [][]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result, nil
}

// buildURL constructs a Census API URL with the given geography and optional API key
func (c *CensusClient) buildURL(variables, forClause, inClause string) string {
	params := url.Values{}
	params.Set("get", variables)
	params.Set("for", forClause)
	if inClause != "" {
		params.Set("in", inClause)
	}
	if c.apiKey != "" {
		params.Set("key", c.apiKey)
	}
	return fmt.Sprintf("%s?%s", c.baseURL, params.Encode())
}

// GetStateData fetches demographic data for a specific state
func (c *CensusClient) GetStateData(ctx context.Context, stateFIPS string, variables string) ([][]string, error) {
	endpoint := c.buildURL(variables, fmt.Sprintf("state:%s", stateFIPS), "")
	return c.doRequest(ctx, endpoint)
}

// GetCountyData fetches demographic data for a specific county
func (c *CensusClient) GetCountyData(ctx context.Context, stateFIPS, countyFIPS string, variables string) ([][]string, error) {
	endpoint := c.buildURL(variables, fmt.Sprintf("county:%s", countyFIPS), fmt.Sprintf("state:%s", stateFIPS))
	return c.doRequest(ctx, endpoint)
}

// GetZipCodeData fetches demographic data for a specific zip code (ZCTA)
func (c *CensusClient) GetZipCodeData(ctx context.Context, zipCode string, variables string) ([][]string, error) {
	endpoint := c.buildURL(variables, fmt.Sprintf("zip code tabulation area:%s", zipCode), "")
	return c.doRequest(ctx, endpoint)
}

// GetAllStatesData fetches demographic data for all states (for rankings)
func (c *CensusClient) GetAllStatesData(ctx context.Context, variables string) ([][]string, error) {
	endpoint := c.buildURL(variables, "state:*", "")
	return c.doRequest(ctx, endpoint)
}

// GetAllCountiesInState fetches all counties in a state (for county name resolution)
func (c *CensusClient) GetAllCountiesInState(ctx context.Context, stateFIPS string) ([][]string, error) {
	endpoint := c.buildURL("NAME", "county:*", fmt.Sprintf("state:%s", stateFIPS))
	return c.doRequest(ctx, endpoint)
}
