package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const currencyBeaconBaseURL = "https://api.currencybeacon.com/v1"

// CurrencyBeaconClient handles API communication with CurrencyBeacon
type CurrencyBeaconClient struct {
	apiKey     string
	httpClient *http.Client
}

// CurrencyBeaconMeta represents the meta field in CurrencyBeacon responses
type CurrencyBeaconMeta struct {
	Code        int    `json:"code"`
	Disclaimer  string `json:"disclaimer,omitempty"`
	ErrorType   string `json:"error_type,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
}

// CurrencyBeaconConvertResponse represents the /convert endpoint response
type CurrencyBeaconConvertResponse struct {
	Meta     CurrencyBeaconMeta `json:"meta"`
	Response struct {
		Timestamp int64   `json:"timestamp"`
		Date      string  `json:"date"`
		From      string  `json:"from"`
		To        string  `json:"to"`
		Amount    float64 `json:"amount"`
		Value     float64 `json:"value"`
	} `json:"response"`
}

// CurrencyBeaconLatestResponse represents the /latest endpoint response
type CurrencyBeaconLatestResponse struct {
	Meta     CurrencyBeaconMeta `json:"meta"`
	Response struct {
		Date  string             `json:"date"`
		Base  string             `json:"base"`
		Rates map[string]float64 `json:"rates"`
	} `json:"response"`
}

// NewCurrencyBeaconClient creates a new CurrencyBeacon API client with traced HTTP
// for distributed tracing visibility into external API calls.
func NewCurrencyBeaconClient(apiKey string) *CurrencyBeaconClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &CurrencyBeaconClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// Convert calls the CurrencyBeacon /convert endpoint
func (c *CurrencyBeaconClient) Convert(ctx context.Context, from, to string, amount float64) (*CurrencyBeaconConvertResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("CurrencyBeacon API key not configured - set CURRENCYBEACON_API_KEY")
	}

	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add("from", from)
	params.Add("to", to)
	params.Add("amount", fmt.Sprintf("%.2f", amount))

	fullURL := fmt.Sprintf("%s/convert?%s", currencyBeaconBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TruvaG3-CurrencyGlobalTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("API error 401: invalid API key - check CURRENCYBEACON_API_KEY")
		}
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp CurrencyBeaconConvertResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if apiResp.Meta.Code != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s - %s", apiResp.Meta.Code, apiResp.Meta.ErrorType, apiResp.Meta.ErrorDetail)
	}

	return &apiResp, nil
}

// LatestRates calls the CurrencyBeacon /latest endpoint
func (c *CurrencyBeaconClient) LatestRates(ctx context.Context, base string, symbols []string) (*CurrencyBeaconLatestResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("CurrencyBeacon API key not configured - set CURRENCYBEACON_API_KEY")
	}

	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add("base", base)
	if len(symbols) > 0 {
		params.Add("symbols", strings.Join(symbols, ","))
	}

	fullURL := fmt.Sprintf("%s/latest?%s", currencyBeaconBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TruvaG3-CurrencyGlobalTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("API error 401: invalid API key - check CURRENCYBEACON_API_KEY")
		}
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp CurrencyBeaconLatestResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if apiResp.Meta.Code != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s - %s", apiResp.Meta.Code, apiResp.Meta.ErrorType, apiResp.Meta.ErrorDetail)
	}

	return &apiResp, nil
}
