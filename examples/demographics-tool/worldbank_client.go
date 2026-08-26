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

// WorldBankClient handles communication with the World Bank API (v2)
type WorldBankClient struct {
	baseURL    string
	httpClient *http.Client
}

// WBDataPoint represents a single data point from the World Bank API
type WBDataPoint struct {
	Indicator struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	} `json:"indicator"`
	Country struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	} `json:"country"`
	Date  string   `json:"date"`
	Value *float64 `json:"value"`
}

// WBCountry represents country metadata from the World Bank API
type WBCountry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region struct {
		Value string `json:"value"`
	} `json:"region"`
	IncomeLevel struct {
		Value string `json:"value"`
	} `json:"incomeLevel"`
	CapitalCity string `json:"capitalCity"`
}

// NewWorldBankClient creates a configured API client with distributed tracing
func NewWorldBankClient() *WorldBankClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &WorldBankClient{
		baseURL:    "https://api.worldbank.org/v2",
		httpClient: tracedClient,
	}
}

// GetIndicator fetches a single indicator for a single country.
// If year is non-empty, it filters to that specific year; otherwise returns most recent 5 values.
func (c *WorldBankClient) GetIndicator(ctx context.Context, countryCode, indicatorID string, perPage int, year string) ([]WBDataPoint, error) {
	if perPage <= 0 {
		perPage = 10
	}
	var url string
	if year != "" {
		url = fmt.Sprintf("%s/country/%s/indicator/%s?format=json&per_page=%d&date=%s",
			c.baseURL, countryCode, indicatorID, perPage, year)
	} else {
		url = fmt.Sprintf("%s/country/%s/indicator/%s?format=json&per_page=%d&mrv=5",
			c.baseURL, countryCode, indicatorID, perPage)
	}
	return c.fetchDataPoints(ctx, url)
}

// GetMultiCountryIndicator fetches a single indicator for multiple countries
func (c *WorldBankClient) GetMultiCountryIndicator(ctx context.Context, countryCodes []string, indicatorID string, perPage int) ([]WBDataPoint, error) {
	if perPage <= 0 {
		perPage = 50
	}
	joined := strings.Join(countryCodes, ";")
	url := fmt.Sprintf("%s/country/%s/indicator/%s?format=json&per_page=%d&mrv=5",
		c.baseURL, joined, indicatorID, perPage)
	return c.fetchDataPoints(ctx, url)
}

// GetCountryInfo fetches metadata for a country
func (c *WorldBankClient) GetCountryInfo(ctx context.Context, countryCode string) (*WBCountry, error) {
	url := fmt.Sprintf("%s/country/%s?format=json", c.baseURL, countryCode)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-DemographicsTool/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("World Bank API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("World Bank API error (status %d): %s", resp.StatusCode, string(body))
	}

	// World Bank country endpoint returns: [metadata, [country_data]]
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var envelope []json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse envelope: %w", err)
	}
	if len(envelope) < 2 {
		return nil, fmt.Errorf("unexpected World Bank response format")
	}

	var countries []WBCountry
	if err := json.Unmarshal(envelope[1], &countries); err != nil {
		return nil, fmt.Errorf("failed to parse country data: %w", err)
	}
	if len(countries) == 0 {
		return nil, fmt.Errorf("no country data returned for code: %s", countryCode)
	}

	return &countries[0], nil
}

// fetchDataPoints handles the common World Bank API response format: [metadata, data[]]
func (c *WorldBankClient) fetchDataPoints(ctx context.Context, url string) ([]WBDataPoint, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TruvaG3-DemographicsTool/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("World Bank API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("World Bank API error (status %d): %s", resp.StatusCode, string(body))
	}

	// World Bank returns: [{"page":1,"pages":1,...}, [{data_point}, ...]]
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	var envelope []json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse envelope: %w", err)
	}
	if len(envelope) < 2 {
		return nil, fmt.Errorf("unexpected World Bank response format (no data)")
	}

	var dataPoints []WBDataPoint
	if err := json.Unmarshal(envelope[1], &dataPoints); err != nil {
		return nil, fmt.Errorf("failed to parse data points: %w", err)
	}

	return dataPoints, nil
}

// resolveCountryCode converts a common country name to an ISO3 code.
// If the input is already an ISO3 code (3 uppercase letters), it is returned as-is.
func resolveCountryCode(input string) string {
	trimmed := strings.TrimSpace(input)
	upper := strings.ToUpper(trimmed)

	// If it looks like an ISO3 code already, return it
	if len(upper) == 3 && isAlpha(upper) {
		return upper
	}
	// Also accept ISO2
	if len(upper) == 2 && isAlpha(upper) {
		if code, ok := countryISO2ToISO3[upper]; ok {
			return code
		}
		// Don't return raw 2-letter input — fall through to name lookup
	}

	lower := strings.ToLower(trimmed)
	if code, ok := countryNameToISO3[lower]; ok {
		return code
	}

	// Fallback: return the input uppercased (user may have typed a valid code)
	return upper
}

func isAlpha(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// countryISO2ToISO3 maps ISO 3166-1 alpha-2 codes to alpha-3 codes
var countryISO2ToISO3 = map[string]string{
	"US": "USA", "GB": "GBR", "FR": "FRA", "DE": "DEU", "JP": "JPN",
	"CN": "CHN", "IN": "IND", "BR": "BRA", "CA": "CAN", "AU": "AUS",
	"RU": "RUS", "KR": "KOR", "MX": "MEX", "ID": "IDN", "TR": "TUR",
	"SA": "SAU", "IT": "ITA", "ES": "ESP", "NL": "NLD", "CH": "CHE",
	"SE": "SWE", "NO": "NOR", "DK": "DNK", "FI": "FIN", "PL": "POL",
	"BE": "BEL", "AT": "AUT", "IE": "IRL", "PT": "PRT", "GR": "GRC",
	"CZ": "CZE", "IL": "ISR", "SG": "SGP", "MY": "MYS", "TH": "THA",
	"PH": "PHL", "VN": "VNM", "ZA": "ZAF", "EG": "EGY", "NG": "NGA",
	"KE": "KEN", "AR": "ARG", "CL": "CHL", "CO": "COL", "PE": "PER",
	"NZ": "NZL", "PK": "PAK", "BD": "BGD", "UA": "UKR", "RO": "ROU",
	"HU": "HUN", "AE": "ARE",
}

// countryNameToISO3 maps common country names (lowercase) to ISO3 codes
var countryNameToISO3 = map[string]string{
	// Major economies
	"united states": "USA", "usa": "USA", "us": "USA", "america": "USA",
	"united kingdom": "GBR", "uk": "GBR", "britain": "GBR", "england": "GBR",
	"france": "FRA", "germany": "DEU", "japan": "JPN", "china": "CHN",
	"india": "IND", "brazil": "BRA", "canada": "CAN", "australia": "AUS",
	"russia": "RUS", "south korea": "KOR", "korea": "KOR", "mexico": "MEX",
	"indonesia": "IDN", "turkey": "TUR", "turkiye": "TUR",
	"saudi arabia": "SAU", "italy": "ITA", "spain": "ESP",
	"netherlands": "NLD", "holland": "NLD", "switzerland": "CHE",
	"sweden": "SWE", "norway": "NOR", "denmark": "DNK", "finland": "FIN",
	"poland": "POL", "belgium": "BEL", "austria": "AUT",
	"ireland": "IRL", "portugal": "PRT", "greece": "GRC",
	"czech republic": "CZE", "czechia": "CZE",
	"israel": "ISR", "singapore": "SGP", "malaysia": "MYS",
	"thailand": "THA", "philippines": "PHL", "vietnam": "VNM",
	"south africa": "ZAF", "egypt": "EGY", "nigeria": "NGA", "kenya": "KEN",
	"argentina": "ARG", "chile": "CHL", "colombia": "COL", "peru": "PER",
	"new zealand": "NZL", "pakistan": "PAK", "bangladesh": "BGD",
	"ukraine": "UKR", "romania": "ROU", "hungary": "HUN",
	"united arab emirates": "ARE", "uae": "ARE",
	// Additional countries
	"ethiopia": "ETH", "tanzania": "TZA", "ghana": "GHA",
	"morocco": "MAR", "algeria": "DZA", "tunisia": "TUN",
	"iran": "IRN", "iraq": "IRQ", "afghanistan": "AFG",
	"sri lanka": "LKA", "nepal": "NPL", "myanmar": "MMR",
	"cambodia": "KHM", "laos": "LAO",
	"taiwan": "TWN", "hong kong": "HKG", "macau": "MAC",
	"ecuador": "ECU", "venezuela": "VEN", "bolivia": "BOL",
	"paraguay": "PRY", "uruguay": "URY", "costa rica": "CRI",
	"panama": "PAN", "cuba": "CUB", "jamaica": "JAM",
	"dominican republic": "DOM", "guatemala": "GTM",
	"honduras": "HND", "el salvador": "SLV", "nicaragua": "NIC",
	"luxembourg": "LUX", "iceland": "ISL", "malta": "MLT",
	"cyprus": "CYP", "estonia": "EST", "latvia": "LVA",
	"lithuania": "LTU", "slovakia": "SVK", "slovenia": "SVN",
	"croatia": "HRV", "serbia": "SRB", "bulgaria": "BGR",
	"north macedonia": "MKD", "albania": "ALB",
	"bosnia": "BIH", "bosnia and herzegovina": "BIH",
	"montenegro": "MNE", "kosovo": "XKX",
	"georgia": "GEO", "armenia": "ARM", "azerbaijan": "AZE",
	"kazakhstan": "KAZ", "uzbekistan": "UZB",
	"mongolia": "MNG", "north korea": "PRK",
	"world": "WLD",
}
