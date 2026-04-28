package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	whoBaseURL       = "https://ghoapi.azureedge.net/api"
	worldBankBaseURL = "https://api.worldbank.org/v2"
)

// HealthClient handles API communication with WHO GHO and World Bank APIs
type HealthClient struct {
	httpClient *http.Client
}

func NewHealthClient() *HealthClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &HealthClient{
		httpClient: tracedClient,
	}
}

// WHOResponse represents the OData response wrapper from WHO GHO
type WHOResponse struct {
	ODataContext string     `json:"@odata.context"`
	Value        []WHOValue `json:"value"`
}

// WHOValue represents a single data point from WHO GHO
// Uses pointer types for nullable JSON fields
type WHOValue struct {
	ID            int      `json:"Id"`
	IndicatorCode string   `json:"IndicatorCode"`
	SpatialDim    string   `json:"SpatialDim"`  // ISO alpha-3 country code
	TimeDim       int      `json:"TimeDim"`      // Year as integer
	Dim1Type      *string  `json:"Dim1Type"`     // e.g., "SEX"
	Dim1          *string  `json:"Dim1"`         // e.g., "BTSX", "MLE", "FMLE"
	Value         *string  `json:"Value"`        // String with confidence interval
	NumericValue  *float64 `json:"NumericValue"` // Numeric value for computation
	Low           *float64 `json:"Low"`          // Lower bound (nullable)
	High          *float64 `json:"High"`         // Upper bound (nullable)
}

// WHOIndicator represents an indicator from the WHO catalog
type WHOIndicator struct {
	IndicatorCode string `json:"IndicatorCode"`
	IndicatorName string `json:"IndicatorName"`
	Language      string `json:"Language"`
}

// WHOIndicatorResponse is the OData wrapper for indicator listings
type WHOIndicatorResponse struct {
	Value []WHOIndicator `json:"value"`
}

// WorldBankDataPoint represents a single data point from World Bank
type WorldBankDataPoint struct {
	Indicator   WorldBankRef `json:"indicator"`
	Country     WorldBankRef `json:"country"`
	CountryISO3 string       `json:"countryiso3code"`
	Date        string       `json:"date"`      // Year as string
	Value       *float64     `json:"value"`      // Nullable float64
	Unit        string       `json:"unit"`
	ObsStatus   string       `json:"obs_status"`
	Decimal     int          `json:"decimal"`
}

// WorldBankRef is a common {id, value} pair in World Bank responses
type WorldBankRef struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// HealthIndicatorResult is the unified result from either API source
type HealthIndicatorResult struct {
	IndicatorCode string
	Value         *float64
	Year          int
	Sex           string
	Source        string // "WHO GHO" or "World Bank"
}

// mapSexToWHODim maps user-facing sex values to WHO GHO Dim1 format.
// WHO uses SEX_BTSX, SEX_MLE, SEX_FMLE as dimension values.
func mapSexToWHODim(sex string) string {
	switch strings.ToUpper(sex) {
	case "BTSX", "SEX_BTSX":
		return "SEX_BTSX"
	case "MLE", "SEX_MLE":
		return "SEX_MLE"
	case "FMLE", "SEX_FMLE":
		return "SEX_FMLE"
	default:
		return ""
	}
}

// GetIndicatorData fetches indicator data from WHO GHO API
func (c *HealthClient) GetIndicatorData(ctx context.Context, indicatorCode, country string, year *int, sex string) ([]WHOValue, error) {
	// Build OData filter
	filterParts := []string{
		fmt.Sprintf("SpatialDim eq '%s'", country),
	}
	if year != nil {
		filterParts = append(filterParts, fmt.Sprintf("TimeDim eq %d", *year))
	}
	if sex != "" {
		whoDim := mapSexToWHODim(sex)
		if whoDim != "" {
			filterParts = append(filterParts, fmt.Sprintf("Dim1 eq '%s'", whoDim))
		}
	}

	params := url.Values{}
	params.Set("$filter", strings.Join(filterParts, " and "))
	params.Set("$format", "json")
	params.Set("$orderby", "TimeDim desc")
	params.Set("$top", "10")

	fullURL := fmt.Sprintf("%s/%s?%s", whoBaseURL, indicatorCode, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create WHO request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-WorldHealthTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WHO API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WHO API returned status %d: %s", resp.StatusCode, string(body))
	}

	var whoResp WHOResponse
	if err := json.NewDecoder(resp.Body).Decode(&whoResp); err != nil {
		return nil, fmt.Errorf("failed to decode WHO response: %w", err)
	}

	return whoResp.Value, nil
}

// ListIndicators fetches available indicators from WHO GHO catalog
func (c *HealthClient) ListIndicators(ctx context.Context, search string, limit int) ([]WHOIndicator, error) {
	params := url.Values{}
	params.Set("$format", "json")
	if search != "" {
		// Split multi-word searches into OR conditions
		// WHO OData contains() treats the string as literal, so "immunization vaccination"
		// won't match anything. Split into individual words and OR them.
		words := strings.Fields(search)
		if len(words) > 1 {
			parts := make([]string, 0, len(words))
			for _, w := range words {
				parts = append(parts, fmt.Sprintf("contains(IndicatorName,'%s')", w))
			}
			params.Set("$filter", strings.Join(parts, " or "))
		} else {
			params.Set("$filter", fmt.Sprintf("contains(IndicatorName,'%s')", search))
		}
	}
	if limit > 0 {
		params.Set("$top", strconv.Itoa(limit))
	}

	fullURL := fmt.Sprintf("%s/Indicator?%s", whoBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create WHO indicator request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-WorldHealthTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("WHO indicator API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("WHO indicator API returned status %d: %s", resp.StatusCode, string(body))
	}

	var indicatorResp WHOIndicatorResponse
	if err := json.NewDecoder(resp.Body).Decode(&indicatorResp); err != nil {
		return nil, fmt.Errorf("failed to decode WHO indicator response: %w", err)
	}

	return indicatorResp.Value, nil
}

// GetWorldBankData fetches indicator data from World Bank API as fallback
// CRITICAL: World Bank returns a TOP-LEVEL ARRAY [metadata, [data]], not an object
func (c *HealthClient) GetWorldBankData(ctx context.Context, indicatorCode, country string, year *int) ([]WorldBankDataPoint, error) {
	dateParam := "2000:2025" // Range for latest data
	if year != nil {
		dateParam = strconv.Itoa(*year)
	}

	params := url.Values{}
	params.Set("format", "json")
	params.Set("date", dateParam)
	params.Set("per_page", "10")

	fullURL := fmt.Sprintf("%s/country/%s/indicator/%s?%s",
		worldBankBaseURL, country, indicatorCode, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create World Bank request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-WorldHealthTool/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("World Bank API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("World Bank API returned status %d: %s", resp.StatusCode, string(body))
	}

	// CRITICAL: World Bank returns a top-level array [metadata, [data]]
	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode World Bank response: %w", err)
	}

	if len(raw) < 2 {
		return nil, fmt.Errorf("unexpected World Bank response format: expected 2 elements, got %d", len(raw))
	}

	// Second element is the data array (can be null if no data)
	if string(raw[1]) == "null" {
		return nil, fmt.Errorf("no data available from World Bank for %s/%s", country, indicatorCode)
	}

	var dataPoints []WorldBankDataPoint
	if err := json.Unmarshal(raw[1], &dataPoints); err != nil {
		return nil, fmt.Errorf("failed to decode World Bank data: %w", err)
	}

	return dataPoints, nil
}

// reverseWHOCodeMap maps WHO indicator codes (case-insensitive) back to friendly names
var reverseWHOCodeMap map[string]string

func init() {
	reverseWHOCodeMap = make(map[string]string, len(whoIndicatorMap))
	for friendly, code := range whoIndicatorMap {
		reverseWHOCodeMap[strings.ToLower(code)] = friendly
	}
}

// GetHealthIndicatorWithFallback tries WHO GHO first, falls back to World Bank
func (c *HealthClient) GetHealthIndicatorWithFallback(
	ctx context.Context,
	friendlyName string,
	country string,
	year *int,
	sex string,
) (*HealthIndicatorResult, error) {

	// 1. Resolve friendly name to WHO indicator code
	// Also check if a raw WHO code was passed and resolve back to friendly name
	whoCode, whoOK := whoIndicatorMap[friendlyName]
	if !whoOK {
		// Check if it's a raw WHO code — resolve to friendly name for World Bank fallback
		if resolved, ok := reverseWHOCodeMap[strings.ToLower(friendlyName)]; ok {
			whoCode = whoIndicatorMap[resolved]
			friendlyName = resolved
		} else {
			// Treat as raw WHO indicator code
			whoCode = friendlyName
		}
	}

	// 2. Try WHO GHO API first
	whoData, whoErr := c.GetIndicatorData(ctx, whoCode, country, year, sex)
	if whoErr == nil && len(whoData) > 0 {
		// Find the best result (latest year, matching sex)
		best := selectBestWHOValue(whoData, sex)
		return &HealthIndicatorResult{
			IndicatorCode: whoCode,
			Value:         best.NumericValue,
			Year:          best.TimeDim,
			Sex:           derefString(best.Dim1),
			Source:        "WHO GHO",
		}, nil
	}

	// 3. Fallback to World Bank if WHO failed or returned empty
	wbCode, wbOK := worldBankIndicatorMap[friendlyName]
	if !wbOK {
		// No World Bank mapping -- return the WHO error
		if whoErr != nil {
			return nil, fmt.Errorf("WHO API failed and no World Bank fallback available: %w", whoErr)
		}
		return nil, fmt.Errorf("no data found in WHO for indicator %s, country %s", whoCode, country)
	}

	wbData, wbErr := c.GetWorldBankData(ctx, wbCode, country, year)
	if wbErr != nil {
		// Both sources failed
		if whoErr != nil {
			return nil, fmt.Errorf("both WHO (%v) and World Bank (%v) failed", whoErr, wbErr)
		}
		return nil, fmt.Errorf("WHO returned no data and World Bank failed: %w", wbErr)
	}

	if len(wbData) == 0 {
		return nil, fmt.Errorf("no data available from either WHO or World Bank for %s/%s", friendlyName, country)
	}

	// Find the latest non-null value
	best := selectBestWorldBankValue(wbData)
	if best == nil {
		return nil, fmt.Errorf("all World Bank values are null for %s/%s", friendlyName, country)
	}

	wbYear, _ := strconv.Atoi(best.Date)
	return &HealthIndicatorResult{
		IndicatorCode: wbCode,
		Value:         best.Value,
		Year:          wbYear,
		Source:        "World Bank",
	}, nil
}

// selectBestWHOValue picks the most relevant WHO data point
// Prefers: matching sex > SEX_BTSX (both sexes) > latest year > non-null NumericValue
func selectBestWHOValue(values []WHOValue, preferredSex string) WHOValue {
	// Map user-facing sex to WHO Dim1 format for matching
	whoSex := mapSexToWHODim(preferredSex)

	// Already ordered by TimeDim desc from the API query
	// First pass: look for exact sex match or SEX_BTSX
	for _, v := range values {
		if v.NumericValue != nil {
			dim1 := derefString(v.Dim1)
			if whoSex != "" && dim1 == whoSex {
				return v // Exact match
			}
			if whoSex == "" && (dim1 == "SEX_BTSX" || dim1 == "") {
				return v // No preference — prefer both-sexes or no-sex-dimension
			}
		}
	}
	// Fallback: return first non-null value regardless of sex
	for _, v := range values {
		if v.NumericValue != nil {
			return v
		}
	}
	// Last resort: return first value
	return values[0]
}

// selectBestWorldBankValue picks the latest non-null World Bank data point
func selectBestWorldBankValue(data []WorldBankDataPoint) *WorldBankDataPoint {
	// World Bank data is usually sorted by date desc
	for i := range data {
		if data[i].Value != nil {
			return &data[i]
		}
	}
	return nil
}

// derefString safely dereferences a *string, returning "" if nil
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
