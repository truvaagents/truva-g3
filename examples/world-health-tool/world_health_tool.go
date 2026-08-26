package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// WHO GHO indicator codes
var whoIndicatorMap = map[string]string{
	"life_expectancy":        "WHOSIS_000001",
	"neonatal_mortality":     "MDG_0000000001",
	"infant_mortality":       "MDG_0000000003",
	"under5_mortality":       "MDG_0000000007",
	"maternal_mortality":     "MDG_0000000025",
	"immunization_dpt3":      "WHS4_100",
	"immunization_measles":   "WHS4_117",
	"tuberculosis_incidence": "MDG_0000000020",
	"hiv_prevalence":         "MDG_0000000029",
	"malaria_incidence":      "MALARIA_EST_INCIDENCE",
	"health_expenditure":     "GHED_CHE_pc_US_SHA2011",
	"physicians_density":     "HWF_0001",
	"hospital_beds":          "HWF_0006",
	"tobacco_use":            "M_Est_tob_curr_std",
	"obesity_prevalence":     "NCD_BMI_30A",
}

// World Bank indicator codes (fallback)
var worldBankIndicatorMap = map[string]string{
	"life_expectancy":        "SP.DYN.LE00.IN",
	"neonatal_mortality":     "SH.DYN.NMRT",
	"infant_mortality":       "SP.DYN.IMRT.IN",
	"under5_mortality":       "SH.DYN.MORT",
	"maternal_mortality":     "SH.STA.MMRT",
	"immunization_dpt3":      "SH.IMM.IDPT",
	"immunization_measles":   "SH.IMM.MEAS",
	"tuberculosis_incidence": "SH.TBS.INCD",
	"hiv_prevalence":         "SH.DYN.AIDS.ZS",
	"health_expenditure":     "SH.XPD.CHEX.PC.CD",
	"hospital_beds":          "SH.MED.BEDS.ZS",
	"physicians_density":     "SH.MED.PHYS.ZS",
	"obesity_prevalence":     "SH.STA.OWGH.ZS",
}

// Country name resolution
var countryNames = map[string]string{
	"USA": "United States",
	"GBR": "United Kingdom",
	"JPN": "Japan",
	"DEU": "Germany",
	"FRA": "France",
	"CAN": "Canada",
	"AUS": "Australia",
	"BRA": "Brazil",
	"IND": "India",
	"CHN": "China",
	"RUS": "Russian Federation",
	"ZAF": "South Africa",
	"NGA": "Nigeria",
	"MEX": "Mexico",
	"KOR": "Republic of Korea",
	"SWE": "Sweden",
	"NOR": "Norway",
	"CHE": "Switzerland",
	"ITA": "Italy",
	"ESP": "Spain",
	"NLD": "Netherlands",
	"BEL": "Belgium",
	"AUT": "Austria",
	"DNK": "Denmark",
	"FIN": "Finland",
	"IRL": "Ireland",
	"NZL": "New Zealand",
	"SGP": "Singapore",
	"ISR": "Israel",
	"ARG": "Argentina",
	"CHL": "Chile",
	"COL": "Colombia",
	"PER": "Peru",
	"EGY": "Egypt",
	"TUR": "Turkey",
	"SAU": "Saudi Arabia",
	"ARE": "United Arab Emirates",
	"THA": "Thailand",
	"VNM": "Vietnam",
	"IDN": "Indonesia",
	"MYS": "Malaysia",
	"PHL": "Philippines",
	"PAK": "Pakistan",
	"BGD": "Bangladesh",
	"ETH": "Ethiopia",
	"KEN": "Kenya",
	"GHA": "Ghana",
	"TZA": "Tanzania",
	"UGA": "Uganda",
	"POL": "Poland",
	"CZE": "Czech Republic",
	"ROU": "Romania",
	"HUN": "Hungary",
	"GRC": "Greece",
	"PRT": "Portugal",
	"UKR": "Ukraine",
}

func resolveCountryName(code string) string {
	if name, ok := countryNames[code]; ok {
		return name
	}
	return code // Fallback to code if not in mapping
}

// Indicator unit resolution
var indicatorUnits = map[string]string{
	"life_expectancy":        "years",
	"neonatal_mortality":     "per 1000 live births",
	"infant_mortality":       "per 1000 live births",
	"under5_mortality":       "per 1000 live births",
	"maternal_mortality":     "per 100000 live births",
	"immunization_dpt3":      "percent",
	"immunization_measles":   "percent",
	"tuberculosis_incidence": "per 100000 population",
	"hiv_prevalence":         "percent (15-49 age group)",
	"malaria_incidence":      "per 1000 population at risk",
	"health_expenditure":     "US dollars per capita",
	"physicians_density":     "per 10000 population",
	"hospital_beds":          "per 10000 population",
	"tobacco_use":            "percent",
	"obesity_prevalence":     "percent",
}

func resolveIndicatorUnit(indicator string) string {
	if unit, ok := indicatorUnits[indicator]; ok {
		return unit
	}
	return "" // Unknown unit
}

// WorldHealthTool provides global health indicator data via WHO GHO and World Bank APIs
type WorldHealthTool struct {
	*core.BaseTool
	client *HealthClient
}

// NewWorldHealthTool creates a new world health tool
func NewWorldHealthTool() *WorldHealthTool {
	tool := &WorldHealthTool{
		BaseTool: core.NewTool("world-health-tool"),
		client:   NewHealthClient(),
	}
	tool.registerCapabilities()
	return tool
}

// GetHealthIndicatorRequest represents the input for health indicator queries
type GetHealthIndicatorRequest struct {
	Indicator string `json:"indicator"`      // Required: indicator code or friendly name
	Country   string `json:"country"`        // Required: ISO 3166-1 alpha-3 country code
	Year      *int   `json:"year,omitempty"` // Optional: specific year, defaults to latest
	Sex       string `json:"sex,omitempty"`  // Optional: BTSX (both, default), MLE, FMLE
}

// ListIndicatorsRequest represents the input for listing indicators
type ListIndicatorsRequest struct {
	Search string `json:"search,omitempty"` // Optional: keyword to filter
	Limit  *int   `json:"limit,omitempty"`  // Optional: max indicators to return
}

// CompareCountriesRequest represents the input for country comparison
type CompareCountriesRequest struct {
	Indicator string `json:"indicator"`      // Required: indicator code or friendly name
	Countries string `json:"countries"`      // Required: comma-separated ISO alpha-3 codes
	Year      *int   `json:"year,omitempty"` // Optional: specific year
}

// HealthIndicatorResponse represents the output for a health indicator query
type HealthIndicatorResponse struct {
	Indicator    string   `json:"indicator"`     // Resolved indicator code
	FriendlyName string   `json:"friendly_name"` // Human-readable name
	Country      string   `json:"country"`       // ISO alpha-3 code
	CountryName  string   `json:"country_name"`  // Human-readable country name
	Year         int      `json:"year"`          // Data year
	Value        *float64 `json:"value"`         // Numeric value (nullable)
	Unit         string   `json:"unit"`          // Unit of measurement
	Sex          string   `json:"sex,omitempty"` // Sex dimension if applicable
	Source       string   `json:"source"`        // "WHO GHO" or "World Bank"
}

// IndicatorInfo represents a single indicator in the list
type IndicatorInfo struct {
	Code        string `json:"code"`        // WHO indicator code
	Name        string `json:"name"`        // Human-readable name
	Description string `json:"description"` // Detailed description
}

// ListIndicatorsResponse represents the output for listing indicators
type ListIndicatorsResponse struct {
	Indicators []IndicatorInfo `json:"indicators"`
	Count      int             `json:"count"`
	Source     string          `json:"source"`
}

// CountryComparison represents a single country's data in a comparison
type CountryComparison struct {
	Country     string   `json:"country"`      // ISO alpha-3 code
	CountryName string   `json:"country_name"` // Human-readable name
	Value       *float64 `json:"value"`        // Numeric value (nullable if no data)
	Year        int      `json:"year"`         // Data year
}

// CompareCountriesResponse represents the output for country comparison
type CompareCountriesResponse struct {
	Indicator    string              `json:"indicator"`
	FriendlyName string              `json:"friendly_name"`
	Unit         string              `json:"unit"`
	Countries    []CountryComparison `json:"countries"`
	Source       string              `json:"source"`
}

func (t *WorldHealthTool) registerCapabilities() {
	// Capability 1: get_health_indicator
	t.RegisterCapability(core.Capability{
		Name:        "get_health_indicator",
		Description: "Gets a health indicator value for a specific country. Supports friendly names (life_expectancy, infant_mortality) or raw WHO codes.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetHealthIndicator,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "indicator",
					Type:        "string",
					Example:     "life_expectancy",
					Description: "Indicator code or friendly name (use list_indicators to discover)",
				},
				{
					Name:        "country",
					Type:        "string",
					Example:     "USA",
					Description: "ISO 3166-1 alpha-3 country code",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "year",
					Type:        "number",
					Example:     "2020",
					Description: "Specific year for the data; defaults to latest available",
				},
				{
					Name:        "sex",
					Type:        "string",
					Example:     "BTSX",
					Description: "Optional sex filter: BTSX (both sexes), MLE (male), FMLE (female). Omit to get all available data.",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicator", Type: "string", Description: "Resolved WHO indicator code"},
				{Name: "friendly_name", Type: "string", Description: "Human-readable indicator name"},
				{Name: "country", Type: "string", Description: "ISO alpha-3 country code"},
				{Name: "country_name", Type: "string", Description: "Human-readable country name"},
				{Name: "year", Type: "number", Description: "Data year"},
				{Name: "unit", Type: "string", Description: "Unit of measurement"},
				{Name: "source", Type: "string", Description: "Data source (WHO GHO or World Bank)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "value", Type: "number", Description: "Numeric indicator value (null if no data available)"},
				{Name: "sex", Type: "string", Description: "Sex dimension if applicable"},
			},
		},
	})

	// Capability 2: list_indicators
	t.RegisterCapability(core.Capability{
		Name:        "list_indicators",
		Description: "Lists available health indicators from the WHO Global Health Observatory catalog.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleListIndicators,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "search",
					Type:        "string",
					Example:     "mortality",
					Description: "Keyword to filter indicators (e.g., mortality, immunization, tuberculosis)",
				},
				{
					Name:        "limit",
					Type:        "number",
					Example:     "20",
					Description: "Maximum number of indicators to return (default 20)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicators", Type: "array", Description: "Array of indicator objects with code, name, and description"},
				{Name: "count", Type: "number", Description: "Number of indicators returned"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	// Capability 3: compare_countries
	t.RegisterCapability(core.Capability{
		Name:        "compare_countries",
		Description: "Compares a health indicator across multiple countries side by side.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareCountries,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "indicator",
					Type:        "string",
					Example:     "life_expectancy",
					Description: "Indicator code or friendly name to compare across countries",
				},
				{
					Name:        "countries",
					Type:        "string",
					Example:     "USA,JPN,GBR",
					Description: "Comma-separated ISO 3166-1 alpha-3 country codes to compare",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "year",
					Type:        "number",
					Example:     "2020",
					Description: "Specific year for the data; defaults to latest available",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "indicator", Type: "string", Description: "Indicator code used for comparison"},
				{Name: "friendly_name", Type: "string", Description: "Human-readable indicator name"},
				{Name: "unit", Type: "string", Description: "Unit of measurement"},
				{Name: "countries", Type: "array", Description: "Array of country comparison objects with country code, name, value, and year"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}
