package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// DemographicsTool wraps the U.S. Census Bureau API and World Bank API
type DemographicsTool struct {
	*core.BaseTool
	client   *CensusClient
	wbClient *WorldBankClient
	apiKey   string
}

// Census variable list for demographic queries
const demographicVariables = "NAME,B01003_001E,B19013_001E,B25077_001E,B25064_001E," +
	"B15003_001E,B15003_022E,B15003_023E,B15003_025E,B17001_002E," +
	"B23025_005E,B23025_002E,B01002_001E,B25001_001E,B25002_003E"

// Request types

type AreaStatisticsRequest struct {
	Location string `json:"location"`
}

type CompareAreasRequest struct {
	Locations string `json:"locations"`
}

type PopulationRankingRequest struct {
	Metric string `json:"metric"`
	Order  string `json:"order,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type GlobalDemographicsRequest struct {
	Country string `json:"country"`
	Year    string `json:"year,omitempty"`
}

type CompareCountriesDemoRequest struct {
	Countries string `json:"countries"`
	Year      string `json:"year,omitempty"`
}

// Response types

type AreaStatisticsResponse struct {
	Location   LocationInfo   `json:"location"`
	Population PopulationData `json:"population"`
	Income     IncomeData     `json:"income"`
	Housing    HousingData    `json:"housing"`
	Education  EducationData  `json:"education"`
	Employment EmploymentData `json:"employment"`
	DataYear   string         `json:"data_year"`
	Source     string         `json:"source"`
}

type LocationInfo struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	FIPS    string `json:"fips"`
	ZipCode string `json:"zip_code,omitempty"`
}

type PopulationData struct {
	Total     int     `json:"total"`
	MedianAge float64 `json:"median_age"`
}

type IncomeData struct {
	MedianHousehold float64 `json:"median_household"`
}

type HousingData struct {
	MedianHomeValue float64 `json:"median_home_value"`
	MedianRent      float64 `json:"median_rent"`
	TotalUnits      int     `json:"total_units"`
	VacancyRate     float64 `json:"vacancy_rate"`
}

type EducationData struct {
	BachelorsDegree float64 `json:"bachelors_degree_pct"`
	GraduateDegree  float64 `json:"graduate_degree_pct"`
}

type EmploymentData struct {
	UnemploymentRate float64 `json:"unemployment_rate"`
	LaborForce       int     `json:"labor_force"`
	Unemployed       int     `json:"unemployed"`
	PovertyRate      float64 `json:"poverty_rate"`
}

type CompareAreasResponse struct {
	Areas  []AreaStatisticsResponse `json:"areas"`
	Source string                   `json:"source"`
}

type PopulationRankingResponse struct {
	Metric   string        `json:"metric"`
	Order    string        `json:"order"`
	Rankings []RankedState `json:"rankings"`
	DataYear string        `json:"data_year"`
	Source   string        `json:"source"`
}

type RankedState struct {
	Rank  int     `json:"rank"`
	State string  `json:"state"`
	FIPS  string  `json:"fips"`
	Value float64 `json:"value"`
	Units string  `json:"units"`
}

// Global demographics response types

type GlobalDemographicsResponse struct {
	Country          string   `json:"country"`
	CountryCode      string   `json:"country_code"`
	Region           string   `json:"region"`
	IncomeLevel      string   `json:"income_level"`
	Population       *float64 `json:"population,omitempty"`
	LifeExpectancy   *float64 `json:"life_expectancy,omitempty"`
	LiteracyRate     *float64 `json:"literacy_rate,omitempty"`
	UrbanizationRate *float64 `json:"urbanization_rate,omitempty"`
	PopulationGrowth *float64 `json:"population_growth,omitempty"`
	DataYear         string   `json:"data_year"`
	Source           string   `json:"source"`
}

type CompareCountriesDemoResponse struct {
	Countries []GlobalDemographicsResponse `json:"countries"`
	DataYear  string                       `json:"data_year"`
	Source    string                       `json:"source"`
}

// LocationQuery represents a parsed location query
type LocationQuery struct {
	Type       string // "state", "county", "zip"
	StateFIPS  string
	CountyFIPS string
	CountyName string
	ZipCode    string
}

// Error codes
const (
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidInput       = "INVALID_INPUT"
)

// Valid ranking metrics
var validMetrics = map[string]struct {
	Variable string
	Units    string
}{
	"population":        {Variable: "B01003_001E", Units: "people"},
	"median_income":     {Variable: "B19013_001E", Units: "dollars"},
	"home_value":        {Variable: "B25077_001E", Units: "dollars"},
	"median_rent":       {Variable: "B25064_001E", Units: "dollars/month"},
	"poverty_rate":      {Variable: "B17001_002E", Units: "percent"},
	"unemployment_rate": {Variable: "B23025_005E", Units: "percent"},
	"median_age":        {Variable: "B01002_001E", Units: "years"},
}

// NewDemographicsTool creates and initializes the tool
func NewDemographicsTool() *DemographicsTool {
	apiKey := os.Getenv("CENSUS_API_KEY")

	tool := &DemographicsTool{
		BaseTool: core.NewTool("demographics-tool"),
		client:   NewCensusClient(apiKey),
		wbClient: NewWorldBankClient(),
		apiKey:   apiKey,
	}
	tool.registerCapabilities()
	return tool
}

func (t *DemographicsTool) registerCapabilities() {
	t.RegisterCapability(core.Capability{
		Name: "area_statistics",
		Description: "Gets comprehensive demographic and socioeconomic statistics for a U.S. geographic area from the Census Bureau's American Community Survey.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleAreaStatistics,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "location", Type: "string", Example: "Texas", Description: "Geographic location - state name or abbreviation (Texas, TX), zip code (78701), or state:county (TX:Travis). Case-insensitive."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "location", Type: "object", Description: "Location metadata including name, type, and FIPS code"},
				{Name: "population", Type: "object", Description: "Population data including total count and median age"},
				{Name: "income", Type: "object", Description: "Income data including median household income"},
				{Name: "housing", Type: "object", Description: "Housing data including median home value, median rent, total units, and vacancy rate"},
				{Name: "education", Type: "object", Description: "Education data including bachelors and graduate degree percentages"},
				{Name: "employment", Type: "object", Description: "Employment data including unemployment rate, labor force, unemployed count, and poverty rate"},
				{Name: "data_year", Type: "string", Description: "Year of the data (e.g., '2023 (ACS 5-Year)')"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "compare_areas",
		Description: "Compares demographic statistics across multiple U.S. geographic areas side by side.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareAreas,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "locations", Type: "string", Example: "TX,CA,NY", Description: "Comma-separated list of locations to compare (state names, abbreviations, zip codes, or state:county pairs)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "areas", Type: "array", Description: "Array of area statistics objects with population, income, housing, education, and employment data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "population_ranking",
		Description: "Ranks U.S. states by a specific demographic metric from the Census Bureau.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handlePopulationRanking,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "metric", Type: "string", Example: "median_income", Description: "Metric to rank by: population, median_income, home_value, median_rent, poverty_rate, unemployment_rate, median_age"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "order", Type: "string", Example: "desc", Description: "Sort order: desc (highest first, default) or asc (lowest first)"},
				{Name: "limit", Type: "integer", Example: "10", Description: "Number of states to return (default 10, max 52 for all states + DC + PR)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "metric", Type: "string", Description: "The metric used for ranking"},
				{Name: "order", Type: "string", Description: "Sort order used (asc or desc)"},
				{Name: "rankings", Type: "array", Description: "Array of ranked states with rank, state name, FIPS code, value, and units"},
				{Name: "data_year", Type: "string", Description: "Year of the data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "global_demographics",
		Description: "Gets demographic data for any country worldwide from the World Bank. " +
			"For U.S.-specific detailed data (income, housing, education), use area_statistics instead.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGlobalDemographics,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Example: "India", Description: "Country name (e.g., 'India', 'Brazil', 'Japan') or ISO3 code (e.g., 'IND', 'BRA', 'JPN')"},
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
				{Name: "population", Type: "number", Description: "Total population"},
				{Name: "life_expectancy", Type: "number", Description: "Life expectancy at birth in years"},
				{Name: "literacy_rate", Type: "number", Description: "Adult literacy rate percentage"},
				{Name: "urbanization_rate", Type: "number", Description: "Urban population percentage"},
				{Name: "population_growth", Type: "number", Description: "Annual population growth rate percentage"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "compare_countries_demographics",
		Description: "Compares demographic indicators across multiple countries using World Bank data.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareCountriesDemographics,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "string", Example: "IND,CHN,USA", Description: "Comma-separated country names or ISO3 codes to compare"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "year", Type: "string", Example: "2022", Description: "Year for data (default: latest available)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "array", Description: "Array of country demographic data objects"},
				{Name: "data_year", Type: "string", Description: "Year of the data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}

// parseLocation parses a user-provided location string into a structured query
func parseLocation(location string) (*LocationQuery, error) {
	location = strings.TrimSpace(location)
	if location == "" {
		return nil, fmt.Errorf("location is required")
	}

	// 1. Zip code: 5 digits
	if matched, _ := regexp.MatchString(`^\d{5}$`, location); matched {
		return &LocationQuery{Type: "zip", ZipCode: location}, nil
	}

	// 2. State:County format (e.g., "TX:Travis")
	if parts := strings.SplitN(location, ":", 2); len(parts) == 2 {
		stateFIPS := resolveStateFIPS(strings.TrimSpace(parts[0]))
		if stateFIPS == "" {
			return nil, fmt.Errorf("unrecognized state in '%s'", location)
		}
		countyName := strings.TrimSpace(parts[1])
		countyFIPS := resolveCountyFIPS(stateFIPS, countyName)
		return &LocationQuery{
			Type:       "county",
			StateFIPS:  stateFIPS,
			CountyFIPS: countyFIPS,
			CountyName: countyName,
		}, nil
	}

	// 3. State name or abbreviation
	if fips := resolveStateFIPS(location); fips != "" {
		return &LocationQuery{Type: "state", StateFIPS: fips}, nil
	}

	return nil, fmt.Errorf("unrecognized location format: %s (use state name, abbreviation, zip code, or state:county)", location)
}

// resolveStateFIPS converts a state name or abbreviation to a FIPS code
func resolveStateFIPS(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	if fips, ok := stateFIPS[lower]; ok {
		return fips
	}
	return ""
}

// resolveCountyFIPS converts a county name to a FIPS code using the built-in mapping
func resolveCountyFIPS(stateFIPS, countyName string) string {
	key := stateFIPS + ":" + strings.ToLower(strings.TrimSpace(countyName))
	if fips, ok := countyFIPSMap[key]; ok {
		return fips
	}
	return ""
}

// State FIPS code mapping (all 50 states + DC + PR)
var stateFIPS = map[string]string{
	"alabama": "01", "al": "01",
	"alaska": "02", "ak": "02",
	"arizona": "04", "az": "04",
	"arkansas": "05", "ar": "05",
	"california": "06", "ca": "06",
	"colorado": "08", "co": "08",
	"connecticut": "09", "ct": "09",
	"delaware": "10", "de": "10",
	"district of columbia": "11", "dc": "11",
	"florida": "12", "fl": "12",
	"georgia": "13", "ga": "13",
	"hawaii": "15", "hi": "15",
	"idaho": "16", "id": "16",
	"illinois": "17", "il": "17",
	"indiana": "18", "in": "18",
	"iowa": "19", "ia": "19",
	"kansas": "20", "ks": "20",
	"kentucky": "21", "ky": "21",
	"louisiana": "22", "la": "22",
	"maine": "23", "me": "23",
	"maryland": "24", "md": "24",
	"massachusetts": "25", "ma": "25",
	"michigan": "26", "mi": "26",
	"minnesota": "27", "mn": "27",
	"mississippi": "28", "ms": "28",
	"missouri": "29", "mo": "29",
	"montana": "30", "mt": "30",
	"nebraska": "31", "ne": "31",
	"nevada": "32", "nv": "32",
	"new hampshire": "33", "nh": "33",
	"new jersey": "34", "nj": "34",
	"new mexico": "35", "nm": "35",
	"new york": "36", "ny": "36",
	"north carolina": "37", "nc": "37",
	"north dakota": "38", "nd": "38",
	"ohio": "39", "oh": "39",
	"oklahoma": "40", "ok": "40",
	"oregon": "41", "or": "41",
	"pennsylvania": "42", "pa": "42",
	"puerto rico": "72", "pr": "72",
	"rhode island": "44", "ri": "44",
	"south carolina": "45", "sc": "45",
	"south dakota": "46", "sd": "46",
	"tennessee": "47", "tn": "47",
	"texas": "48", "tx": "48",
	"utah": "49", "ut": "49",
	"vermont": "50", "vt": "50",
	"virginia": "51", "va": "51",
	"washington": "53", "wa": "53",
	"west virginia": "54", "wv": "54",
	"wisconsin": "55", "wi": "55",
	"wyoming": "56", "wy": "56",
}

// County FIPS mapping for popular counties (state_fips:county_name_lower -> county_fips)
var countyFIPSMap = map[string]string{
	// Texas
	"48:travis":     "453",
	"48:harris":     "201",
	"48:dallas":     "113",
	"48:bexar":      "029",
	"48:tarrant":    "439",
	"48:collin":     "085",
	"48:williamson": "491",
	"48:hays":       "209",
	"48:denton":     "121",
	"48:fort bend":  "157",
	"48:el paso":    "141",
	// California
	"06:los angeles":  "037",
	"06:san diego":    "073",
	"06:orange":       "059",
	"06:san francisco":"075",
	"06:santa clara":  "085",
	"06:alameda":      "001",
	"06:sacramento":   "067",
	"06:riverside":    "065",
	"06:san bernardino":"071",
	"06:san mateo":    "081",
	// New York
	"36:new york":  "061",
	"36:kings":     "047",
	"36:queens":    "081",
	"36:bronx":     "005",
	"36:richmond":  "085",
	"36:nassau":    "059",
	"36:suffolk":   "103",
	"36:westchester":"119",
	"36:erie":      "029",
	// Florida
	"12:miami-dade":  "086",
	"12:broward":     "011",
	"12:palm beach":  "099",
	"12:hillsborough":"057",
	"12:orange":      "095",
	"12:duval":       "031",
	"12:pinellas":    "103",
	// Illinois
	"17:cook":    "031",
	"17:dupage":  "043",
	"17:lake":    "097",
	"17:will":    "197",
	"17:kane":    "089",
	// Washington
	"53:king":     "033",
	"53:pierce":   "053",
	"53:snohomish":"061",
	"53:clark":    "011",
	// Colorado
	"08:denver":   "031",
	"08:arapahoe": "005",
	"08:jefferson":"059",
	"08:adams":    "001",
	"08:el paso":  "041",
	"08:douglas":  "035",
	// Georgia
	"13:fulton":   "121",
	"13:gwinnett": "135",
	"13:dekalb":   "089",
	"13:cobb":     "067",
	// Arizona
	"04:maricopa": "013",
	"04:pima":     "019",
	// Massachusetts
	"25:middlesex":  "017",
	"25:suffolk":    "025",
	"25:norfolk":    "021",
	"25:worcester":  "027",
	// Pennsylvania
	"42:philadelphia": "101",
	"42:allegheny":    "003",
	"42:montgomery":   "091",
	"42:bucks":        "017",
	// Ohio
	"39:franklin":  "049",
	"39:cuyahoga":  "035",
	"39:hamilton":  "061",
	"39:summit":    "153",
	// Michigan
	"26:wayne":    "163",
	"26:oakland":  "125",
	"26:macomb":   "099",
	// North Carolina
	"37:mecklenburg": "119",
	"37:wake":        "183",
	"37:guilford":    "081",
	// Virginia
	"51:fairfax":      "059",
	"51:prince william":"153",
	"51:loudoun":      "107",
	// Maryland
	"24:montgomery": "031",
	"24:prince georges":"033",
	"24:baltimore":  "005",
	// Nevada
	"32:clark": "003",
	// Minnesota
	"27:hennepin":  "053",
	"27:ramsey":    "123",
	// Oregon
	"41:multnomah": "051",
	// Tennessee
	"47:davidson": "037",
	"47:shelby":   "157",
}

// fipsToStateName provides reverse FIPS -> state name lookup
var fipsToStateName = map[string]string{
	"01": "Alabama", "02": "Alaska", "04": "Arizona", "05": "Arkansas",
	"06": "California", "08": "Colorado", "09": "Connecticut", "10": "Delaware",
	"11": "District of Columbia", "12": "Florida", "13": "Georgia", "15": "Hawaii",
	"16": "Idaho", "17": "Illinois", "18": "Indiana", "19": "Iowa",
	"20": "Kansas", "21": "Kentucky", "22": "Louisiana", "23": "Maine",
	"24": "Maryland", "25": "Massachusetts", "26": "Michigan", "27": "Minnesota",
	"28": "Mississippi", "29": "Missouri", "30": "Montana", "31": "Nebraska",
	"32": "Nevada", "33": "New Hampshire", "34": "New Jersey", "35": "New Mexico",
	"36": "New York", "37": "North Carolina", "38": "North Dakota", "39": "Ohio",
	"40": "Oklahoma", "41": "Oregon", "42": "Pennsylvania", "44": "Rhode Island",
	"45": "South Carolina", "46": "South Dakota", "47": "Tennessee", "48": "Texas",
	"49": "Utah", "50": "Vermont", "51": "Virginia", "53": "Washington",
	"54": "West Virginia", "55": "Wisconsin", "56": "Wyoming", "72": "Puerto Rico",
}

