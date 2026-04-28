package main

import (
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// CountryTool provides country information using RestCountries API
type CountryTool struct {
	*core.BaseTool
	httpClient *http.Client
}

// CountryRequest represents the input for country info
type CountryRequest struct {
	Country string `json:"country"` // Country name or code
}

// CountryResponse represents country information
type CountryResponse struct {
	Name       string   `json:"name"`
	OfficialN  string   `json:"official_name"`
	Capital    string   `json:"capital"`
	Region     string   `json:"region"`
	Subregion  string   `json:"subregion"`
	Population int64    `json:"population"`
	Area       float64  `json:"area"`
	Languages  []string `json:"languages"`
	Timezones  []string `json:"timezones"`
	Currency   struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"currency"`
	Flag        string `json:"flag"`
	FlagURL     string `json:"flag_url"`
	CountryCode string `json:"country_code"`
}

// RestCountriesResponse represents the API response
type RestCountriesResponse struct {
	Name struct {
		Common   string `json:"common"`
		Official string `json:"official"`
	} `json:"name"`
	Capital    []string          `json:"capital"`
	Region     string            `json:"region"`
	Subregion  string            `json:"subregion"`
	Population int64             `json:"population"`
	Area       float64           `json:"area"`
	Languages  map[string]string `json:"languages"`
	Timezones  []string          `json:"timezones"`
	Currencies map[string]struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"currencies"`
	Flag  string `json:"flag"`
	Flags struct {
		PNG string `json:"png"`
		SVG string `json:"svg"`
	} `json:"flags"`
	CCA2 string `json:"cca2"`
}

const (
	ErrCodeCountryNotFound    = "COUNTRY_NOT_FOUND"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

const RestCountriesBaseURL = "https://restcountries.com/v3.1"

func NewCountryTool() *CountryTool {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}

	tool := &CountryTool{
		BaseTool: core.NewTool("country-info-tool"),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   30 * time.Second,
		},
	}

	tool.registerCapabilities()
	return tool
}

func (c *CountryTool) registerCapabilities() {
	c.RegisterCapability(core.Capability{
		Name:        "get_country_info",
		Description: "Gets detailed information about a country including capital, population, languages, currency, timezones, and flag.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     c.handleCountryInfo,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Example: "Japan", Description: "Country name or ISO code (e.g., Japan, JP, JPN)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "name", Type: "string", Description: "Common name of the country"},
				{Name: "official_name", Type: "string", Description: "Official name of the country"},
				{Name: "capital", Type: "string", Description: "Capital city"},
				{Name: "region", Type: "string", Description: "Geographic region (e.g., Asia, Europe)"},
				{Name: "subregion", Type: "string", Description: "Geographic subregion (e.g., Eastern Asia)"},
				{Name: "population", Type: "number", Description: "Country population"},
				{Name: "area", Type: "number", Description: "Country area in square kilometers"},
				{Name: "languages", Type: "array", Description: "List of spoken languages"},
				{Name: "timezones", Type: "array", Description: "List of timezones"},
				{Name: "currency", Type: "object", Description: "Currency info with code, name, and symbol fields"},
				{Name: "flag", Type: "string", Description: "Flag emoji character"},
				{Name: "flag_url", Type: "string", Description: "URL to flag image (PNG)"},
				{Name: "country_code", Type: "string", Description: "ISO 3166-1 alpha-2 country code"},
			},
		},
	})
}
