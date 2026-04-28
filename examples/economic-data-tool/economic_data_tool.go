package main

import (
	"os"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// EconomicDataTool wraps the FRED (Federal Reserve Economic Data) API and World Bank API
type EconomicDataTool struct {
	*core.BaseTool
	client   *FREDClient
	wbClient *WorldBankClient
	apiKey   string
}

// Request types

type EconomicIndicatorRequest struct {
	Indicator string `json:"indicator"`
	Limit     int    `json:"limit,omitempty"`
	StartDate string `json:"start_date,omitempty"`
	EndDate   string `json:"end_date,omitempty"`
}

type CompareIndicatorsRequest struct {
	Indicators string `json:"indicators"`
	StartDate  string `json:"start_date,omitempty"`
	EndDate    string `json:"end_date,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type SearchIndicatorsRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type IndicatorInfoRequest struct {
	Indicator string `json:"indicator"`
}

type GlobalEconomicRequest struct {
	Country string `json:"country"`
	Year    string `json:"year,omitempty"`
}

type CompareCountryEconomiesRequest struct {
	Countries string `json:"countries"`
	Year      string `json:"year,omitempty"`
}

// Response types

type EconomicIndicatorResponse struct {
	Indicator    string        `json:"indicator"`
	Title        string        `json:"title"`
	Frequency    string        `json:"frequency"`
	Units        string        `json:"units"`
	LastUpdated  string        `json:"last_updated"`
	Observations []Observation `json:"observations"`
	Source       string        `json:"source"`
}

type Observation struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

type CompareIndicatorsResponse struct {
	Indicators []IndicatorSeries `json:"indicators"`
	Period     DateRange         `json:"period"`
	Source     string            `json:"source"`
}

type IndicatorSeries struct {
	SeriesID     string        `json:"series_id"`
	Title        string        `json:"title"`
	Units        string        `json:"units"`
	Frequency    string        `json:"frequency"`
	Observations []Observation `json:"observations"`
}

type DateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type SearchIndicatorsResponse struct {
	Query   string         `json:"query"`
	Count   int            `json:"count"`
	Results []SeriesResult `json:"results"`
	Source  string         `json:"source"`
}

type SeriesResult struct {
	SeriesID           string `json:"series_id"`
	Title              string `json:"title"`
	Frequency          string `json:"frequency"`
	Units              string `json:"units"`
	SeasonalAdjustment string `json:"seasonal_adjustment"`
	LastUpdated        string `json:"last_updated"`
	Notes              string `json:"notes"`
}

type IndicatorInfoResponse struct {
	SeriesID           string `json:"series_id"`
	Title              string `json:"title"`
	ObservationStart   string `json:"observation_start"`
	ObservationEnd     string `json:"observation_end"`
	Frequency          string `json:"frequency"`
	Units              string `json:"units"`
	SeasonalAdjustment string `json:"seasonal_adjustment"`
	LastUpdated        string `json:"last_updated"`
	Notes              string `json:"notes"`
	Source             string `json:"source"`
}

// Global economic response types

type GlobalEconomicResponse struct {
	Country          string   `json:"country"`
	CountryCode      string   `json:"country_code"`
	Region           string   `json:"region"`
	IncomeLevel      string   `json:"income_level"`
	GDP              *float64 `json:"gdp,omitempty"`
	GDPPerCapita     *float64 `json:"gdp_per_capita,omitempty"`
	InflationRate    *float64 `json:"inflation_rate,omitempty"`
	UnemploymentRate *float64 `json:"unemployment_rate,omitempty"`
	DataYear         string   `json:"data_year"`
	Source           string   `json:"source"`
}

type CompareCountryEconomiesResponse struct {
	Countries []GlobalEconomicResponse `json:"countries"`
	DataYear  string                   `json:"data_year"`
	Source    string                   `json:"source"`
}

// Error codes
const (
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidInput       = "INVALID_INPUT"
	ErrCodeAPIKeyMissing      = "API_KEY_MISSING"
)

// seriesShortcuts maps friendly names to FRED series IDs
var seriesShortcuts = map[string]string{
	"mortgage_30y":       "MORTGAGE30US",
	"mortgage_15y":       "MORTGAGE15US",
	"fed_funds_rate":     "FEDFUNDS",
	"inflation_cpi":      "CPIAUCSL",
	"unemployment":       "UNRATE",
	"gdp":               "GDP",
	"real_gdp":          "GDPC1",
	"treasury_10y":      "DGS10",
	"treasury_2y":       "DGS2",
	"sp500":             "SP500",
	"prime_rate":         "DPRIME",
	"housing_starts":     "HOUST",
	"home_price_index":   "CSUSHPINSA",
	"personal_income":    "PI",
	"consumer_sentiment": "UMCSENT",
}

// seriesMetadata provides title/units/frequency for known series
var seriesMetadata = map[string]struct {
	Title     string
	Units     string
	Frequency string
}{
	"MORTGAGE30US": {Title: "30-Year Fixed Rate Mortgage Average", Units: "Percent", Frequency: "Weekly"},
	"MORTGAGE15US": {Title: "15-Year Fixed Rate Mortgage Average", Units: "Percent", Frequency: "Weekly"},
	"FEDFUNDS":     {Title: "Federal Funds Effective Rate", Units: "Percent", Frequency: "Monthly"},
	"CPIAUCSL":     {Title: "Consumer Price Index for All Urban Consumers", Units: "Index 1982-84=100", Frequency: "Monthly"},
	"UNRATE":       {Title: "Civilian Unemployment Rate", Units: "Percent", Frequency: "Monthly"},
	"GDP":          {Title: "Gross Domestic Product", Units: "Billions of Dollars", Frequency: "Quarterly"},
	"GDPC1":        {Title: "Real Gross Domestic Product", Units: "Billions of Chained 2017 Dollars", Frequency: "Quarterly"},
	"DGS10":        {Title: "10-Year Treasury Constant Maturity Rate", Units: "Percent", Frequency: "Daily"},
	"DGS2":         {Title: "2-Year Treasury Constant Maturity Rate", Units: "Percent", Frequency: "Daily"},
	"SP500":        {Title: "S&P 500 Index", Units: "Index", Frequency: "Daily"},
	"DPRIME":       {Title: "Bank Prime Loan Rate", Units: "Percent", Frequency: "Daily"},
	"HOUST":        {Title: "Housing Starts: Total", Units: "Thousands of Units", Frequency: "Monthly"},
	"CSUSHPINSA":   {Title: "S&P/Case-Shiller U.S. National Home Price Index", Units: "Index Jan 2000=100", Frequency: "Monthly"},
	"PI":           {Title: "Personal Income", Units: "Billions of Dollars", Frequency: "Monthly"},
	"UMCSENT":      {Title: "University of Michigan Consumer Sentiment", Units: "Index 1966:Q1=100", Frequency: "Monthly"},
}

// resolveSeriesID converts a shortcut name or raw series ID to a FRED series ID
func resolveSeriesID(indicator string) string {
	lower := strings.ToLower(strings.TrimSpace(indicator))
	if id, ok := seriesShortcuts[lower]; ok {
		return id
	}
	return strings.ToUpper(strings.TrimSpace(indicator))
}

// NewEconomicDataTool creates and initializes the tool
func NewEconomicDataTool() *EconomicDataTool {
	apiKey := os.Getenv("FRED_API_KEY")

	tool := &EconomicDataTool{
		BaseTool: core.NewTool("economic-data-tool"),
		client:   NewFREDClient(apiKey),
		wbClient: NewWorldBankClient(),
		apiKey:   apiKey,
	}
	tool.registerCapabilities()
	return tool
}

func (t *EconomicDataTool) registerCapabilities() {
	t.RegisterCapability(core.Capability{
		Name: "economic_indicator",
		Description: "Gets current and historical values for a specific U.S. economic indicator from the Federal Reserve (FRED). " +
			"Covers U.S. data only: mortgage rates, inflation (CPI), unemployment rate, GDP, interest rates, treasury yields, housing data.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleEconomicIndicator,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicator", Type: "string", Example: "mortgage_30y", Description: "Economic indicator - use shortcut (mortgage_30y, unemployment, gdp, inflation_cpi, fed_funds_rate, treasury_10y) or FRED series ID (MORTGAGE30US, UNRATE, GDP)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "10", Description: "Number of observations to return (default 1, max 100)"},
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date for observations in YYYY-MM-DD format"},
				{Name: "end_date", Type: "string", Example: "2026-01-31", Description: "End date for observations in YYYY-MM-DD format"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicator", Type: "string", Description: "FRED series ID"},
				{Name: "title", Type: "string", Description: "Human-readable indicator title"},
				{Name: "frequency", Type: "string", Description: "Data frequency (e.g., Weekly, Monthly, Daily)"},
				{Name: "units", Type: "string", Description: "Unit of measurement"},
				{Name: "observations", Type: "array", Description: "Array of date/value observation pairs"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "last_updated", Type: "string", Description: "Last updated timestamp"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "compare_indicators",
		Description: "Compares multiple U.S. economic indicators from FRED over the same time period. " +
			"U.S. data only. Use for: comparing mortgage rates vs treasury yields, tracking inflation against unemployment, multi-indicator dashboards.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareIndicators,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicators", Type: "string", Example: "mortgage_30y,treasury_10y,fed_funds_rate", Description: "Comma-separated list of indicators to compare (shortcut names or FRED series IDs)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date in YYYY-MM-DD format"},
				{Name: "end_date", Type: "string", Example: "2026-01-31", Description: "End date in YYYY-MM-DD format"},
				{Name: "limit", Type: "integer", Example: "12", Description: "Number of observations per indicator (default 12)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicators", Type: "array", Description: "Array of indicator series with series_id, title, units, frequency, and observations"},
				{Name: "period", Type: "object", Description: "Date range with start and end fields"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "search_indicators",
		Description: "Searches the FRED database for U.S. economic data series matching a keyword query. " +
			"U.S. data only. Use for: discovering available U.S. economic indicators, finding series IDs for specific data, exploring what economic data is available.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchIndicators,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: "housing prices", Description: "Search terms for finding economic data series"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "5", Description: "Maximum number of results to return (default 5, max 20)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query used"},
				{Name: "count", Type: "number", Description: "Number of results returned"},
				{Name: "results", Type: "array", Description: "Array of series results with series_id, title, frequency, units, seasonal_adjustment, last_updated, and notes"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "indicator_info",
		Description: "Gets detailed metadata and description for a specific U.S. FRED economic indicator series. " +
			"U.S. data only. Use for: understanding what a U.S. indicator measures, checking data frequency and units, getting series descriptions.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleIndicatorInfo,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicator", Type: "string", Example: "unemployment", Description: "Indicator shortcut name or FRED series ID"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "series_id", Type: "string", Description: "FRED series identifier"},
				{Name: "title", Type: "string", Description: "Human-readable series title"},
				{Name: "observation_start", Type: "string", Description: "Earliest available observation date"},
				{Name: "observation_end", Type: "string", Description: "Latest available observation date"},
				{Name: "frequency", Type: "string", Description: "Data frequency"},
				{Name: "units", Type: "string", Description: "Unit of measurement"},
				{Name: "seasonal_adjustment", Type: "string", Description: "Seasonal adjustment type"},
				{Name: "last_updated", Type: "string", Description: "Last updated timestamp"},
				{Name: "notes", Type: "string", Description: "Detailed series description and notes"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "global_economic_indicator",
		Description: "Gets economic data for any country worldwide from the World Bank. Covers 200+ countries with GDP, GDP per capita, inflation rate, and unemployment rate. " +
			"For U.S.-specific detailed data (FRED series, mortgage rates, etc.), use economic_indicator instead.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGlobalEconomicIndicator,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Example: "Brazil", Description: "Country name (e.g., 'Brazil', 'Japan') or ISO3 code (e.g., 'BRA', 'JPN')"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "year", Type: "string", Example: "2022", Description: "Year for data (default: latest available). World Bank data may lag 1-2 years."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Description: "Country name"},
				{Name: "country_code", Type: "string", Description: "ISO3 country code"},
				{Name: "region", Type: "string", Description: "World Bank region classification"},
				{Name: "income_level", Type: "string", Description: "World Bank income level classification"},
				{Name: "data_year", Type: "string", Description: "Year of the data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "gdp", Type: "number", Description: "Gross domestic product in current US dollars"},
				{Name: "gdp_per_capita", Type: "number", Description: "GDP per capita in current US dollars"},
				{Name: "inflation_rate", Type: "number", Description: "Consumer price inflation rate percentage"},
				{Name: "unemployment_rate", Type: "number", Description: "Unemployment rate percentage"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "compare_country_economies",
		Description: "Compares economic indicators across multiple countries using World Bank data. " +
			"Use for cross-country economic comparison covering GDP, GDP per capita, inflation, and unemployment.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareCountryEconomies,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "string", Example: "BRA,IND,CHN", Description: "Comma-separated country names or ISO3 codes to compare"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "year", Type: "string", Example: "2022", Description: "Year for data (default: latest available)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "array", Description: "Array of country economic data objects"},
				{Name: "data_year", Type: "string", Description: "Year of the data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}
