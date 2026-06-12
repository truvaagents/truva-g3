package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// countriesData is the embedded mledoze/countries dataset (ODbL). It powers
// offline name/ISO-code matching and supplies all fields except population and
// timezones (those are enriched at runtime). See data/ATTRIBUTION.md.
//
//go:embed data/countries.json
var countriesData []byte

// CountryTool provides country information from an embedded offline dataset
// (mledoze/countries), enriched with population/timezones from apicountries.com.
type CountryTool struct {
	*core.BaseTool
	httpClient *http.Client
	countries  []mledozeCountry
	index      map[string]*mledozeCountry
	// cache stores enrichment results (population/timezones). BaseTool deliberately
	// has no Memory field; response caching is opted into per the Tool Dev Guide.
	cache *core.MemoryStore
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

// mledozeCountry is the subset of an mledoze/countries record this tool uses.
type mledozeCountry struct {
	Name struct {
		Common   string `json:"common"`
		Official string `json:"official"`
	} `json:"name"`
	Capital    []string          `json:"capital"`
	Region     string            `json:"region"`
	Subregion  string            `json:"subregion"`
	Area       float64           `json:"area"`
	Languages  map[string]string `json:"languages"`
	Currencies map[string]struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
	} `json:"currencies"`
	Flag         string   `json:"flag"`
	CCA2         string   `json:"cca2"`
	CCA3         string   `json:"cca3"`
	CIOC         string   `json:"cioc"`
	AltSpellings []string `json:"altSpellings"`
}

// enrichment holds the two fields the offline dataset lacks (population,
// timezones). Fetched from apicountries.com by exact ISO code and cached.
type enrichment struct {
	Population int64    `json:"population"`
	Timezones  []string `json:"timezones"`
}

const (
	ErrCodeCountryNotFound = "COUNTRY_NOT_FOUND"
	ErrCodeInvalidRequest  = "INVALID_REQUEST"
)

// CountryAPIBaseURL is the keyless enrichment source for population/timezones,
// which the embedded dataset does not provide. Looked up by exact ISO code.
const CountryAPIBaseURL = "https://apicountries.com"

func NewCountryTool() *CountryTool {
	// Traced HTTP client: creates a child span per outgoing call (population/
	// timezone enrichment) for end-to-end visibility in Jaeger.
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	tool := &CountryTool{
		BaseTool:   core.NewTool("country-info-tool"),
		httpClient: tracedClient,
		cache:      core.NewMemoryStore(),
	}

	tool.loadCountries()
	tool.registerCapabilities()
	return tool
}

// loadCountries parses the embedded mledoze dataset and builds a case-insensitive
// lookup index. Authoritative keys (common/official name, ISO alpha-2/alpha-3,
// IOC code) are added first; alternative spellings only fill gaps so they can
// never shadow a real country name. Panics on failure — the data is embedded and
// validated at build time, so a parse error is a broken build, not a runtime case.
func (c *CountryTool) loadCountries() {
	if err := json.Unmarshal(countriesData, &c.countries); err != nil {
		panic(fmt.Sprintf("country-info-tool: failed to parse embedded countries.json: %v", err))
	}

	c.index = make(map[string]*mledozeCountry, len(c.countries)*4)
	add := func(key string, country *mledozeCountry) {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return
		}
		if _, exists := c.index[key]; !exists {
			c.index[key] = country
		}
	}

	for i := range c.countries {
		country := &c.countries[i]
		add(country.Name.Common, country)
		add(country.Name.Official, country)
		add(country.CCA2, country)
		add(country.CCA3, country)
		add(country.CIOC, country)
	}
	for i := range c.countries {
		country := &c.countries[i]
		for _, alt := range country.AltSpellings {
			add(alt, country)
		}
	}
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
