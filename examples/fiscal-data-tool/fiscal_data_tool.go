package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// FiscalDataTool wraps the U.S. Treasury Fiscal Data API and World Bank API
type FiscalDataTool struct {
	*core.BaseTool
	client   *TreasuryClient
	wbClient *WorldBankClient
}

// Request types

type NationalDebtRequest struct {
	Limit     int    `json:"limit,omitempty"`
	StartDate string `json:"start_date,omitempty"`
}

type TreasuryRatesRequest struct {
	SecurityType string `json:"security_type,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	StartDate    string `json:"start_date,omitempty"`
}

type ExchangeRatesRequest struct {
	Currencies string `json:"currencies,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	StartDate  string `json:"start_date,omitempty"`
}

type FederalSpendingRequest struct {
	Limit     int    `json:"limit,omitempty"`
	StartDate string `json:"start_date,omitempty"`
}

type GlobalFiscalRequest struct {
	Country string `json:"country"`
	Year    string `json:"year,omitempty"`
}

type CompareCountryFiscalRequest struct {
	Countries string `json:"countries"`
	Year      string `json:"year,omitempty"`
}

// Response types

type NationalDebtResponse struct {
	Records []DebtRecord `json:"records"`
	Source  string       `json:"source"`
}

type DebtRecord struct {
	Date             string  `json:"date"`
	TotalPublicDebt  float64 `json:"total_public_debt"`
	DebtHeldByPublic float64 `json:"debt_held_by_public"`
	IntragovHoldings float64 `json:"intragovernmental_holdings"`
	FiscalYear       string  `json:"fiscal_year"`
	FiscalQuarter    string  `json:"fiscal_quarter"`
}

type TreasuryRatesResponse struct {
	Records []TreasuryRateRecord `json:"records"`
	Source  string               `json:"source"`
}

type TreasuryRateRecord struct {
	Date            string  `json:"date"`
	SecurityType    string  `json:"security_type"`
	SecurityDesc    string  `json:"security_desc"`
	AvgInterestRate float64 `json:"avg_interest_rate"`
	FiscalYear      string  `json:"fiscal_year"`
}

type ExchangeRatesResponse struct {
	Records []ExchangeRateRecord `json:"records"`
	Source  string               `json:"source"`
}

type ExchangeRateRecord struct {
	Date          string  `json:"date"`
	Country       string  `json:"country_currency"`
	ExchangeRate  float64 `json:"exchange_rate"`
	EffectiveDate string  `json:"effective_date"`
}

type FederalSpendingResponse struct {
	Records []SpendingRecord `json:"records"`
	Source  string           `json:"source"`
}

type SpendingRecord struct {
	Date             string  `json:"date"`
	FiscalYear       string  `json:"fiscal_year"`
	FiscalMonth      string  `json:"fiscal_month"`
	Receipts         float64 `json:"receipts"`
	Outlays          float64 `json:"outlays"`
	SurplusOrDeficit float64 `json:"surplus_or_deficit"`
}

// Global fiscal response types

type GlobalFiscalResponse struct {
	Country             string   `json:"country"`
	CountryCode         string   `json:"country_code"`
	Region              string   `json:"region"`
	IncomeLevel         string   `json:"income_level"`
	DebtToGDPPct        *float64 `json:"debt_to_gdp_pct,omitempty"`
	RevenueToGDPPct     *float64 `json:"revenue_to_gdp_pct,omitempty"`
	ExpenditureToGDPPct *float64 `json:"expenditure_to_gdp_pct,omitempty"`
	DataYear            string   `json:"data_year"`
	Source              string   `json:"source"`
}

type CompareCountryFiscalResponse struct {
	Countries []GlobalFiscalResponse `json:"countries"`
	DataYear  string                 `json:"data_year"`
	Source    string                 `json:"source"`
}

// Error codes
const (
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidInput       = "INVALID_INPUT"
)

// NewFiscalDataTool creates and initializes the tool
func NewFiscalDataTool() *FiscalDataTool {
	tool := &FiscalDataTool{
		BaseTool: core.NewTool("fiscal-data-tool"),
		client:   NewTreasuryClient(),
		wbClient: NewWorldBankClient(),
	}
	tool.registerCapabilities()
	return tool
}

func (t *FiscalDataTool) registerCapabilities() {
	t.RegisterCapability(core.Capability{
		Name:        "national_debt",
		Description: "Gets the current U.S. national debt from the Treasury Department, broken down into debt held by the public and intragovernmental holdings.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleNationalDebt,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "10", Description: "Number of records to return (default 1 for latest, max 100)"},
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date for historical data in YYYY-MM-DD format"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "records", Type: "array", Description: "Array of debt records with date, total_public_debt, debt_held_by_public, intragovernmental_holdings, fiscal_year, and fiscal_quarter"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "treasury_rates",
		Description: "Gets average interest rates on U.S. Treasury securities including Treasury Bills, Notes, Bonds, and TIPS.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleTreasuryRates,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "security_type", Type: "string", Example: "Treasury Bonds", Description: "Filter by security type: Treasury Bills, Treasury Notes, Treasury Bonds, Treasury Inflation-Protected Securities, or leave empty for all"},
				{Name: "limit", Type: "integer", Example: "10", Description: "Number of records to return (default 10)"},
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date for historical data in YYYY-MM-DD format"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "records", Type: "array", Description: "Array of rate records with date, security_type, security_desc, avg_interest_rate, and fiscal_year"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "exchange_rates",
		Description: "Gets official U.S. Treasury exchange rates for foreign currencies. These are quarterly Treasury reporting rates used for federal government reporting, not real-time market rates.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleExchangeRates,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "currencies", Type: "string", Example: "Euro Zone-Euro,Japan-Yen,United Kingdom-Pound", Description: "Comma-separated currency filter using Treasury format (e.g., 'Euro Zone-Euro', 'Japan-Yen', 'Canada-Dollar'). Leave empty for all currencies."},
				{Name: "limit", Type: "integer", Example: "10", Description: "Number of records per currency (default 10)"},
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date in YYYY-MM-DD format"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "records", Type: "array", Description: "Array of exchange rate records with date, country_currency, exchange_rate, and effective_date"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "federal_spending",
		Description: "Gets a summary of federal government receipts (revenue) and outlays (spending) from the Monthly Treasury Statement.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleFederalSpending,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "limit", Type: "integer", Example: "12", Description: "Number of monthly records to return (default 12)"},
				{Name: "start_date", Type: "string", Example: "2024-01-01", Description: "Start date in YYYY-MM-DD format"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "records", Type: "array", Description: "Array of spending records with date, fiscal_year, fiscal_month, receipts, outlays, and surplus_or_deficit"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "global_fiscal_data",
		Description: "Gets government fiscal data (debt, revenue, expenditure as percentage of GDP) for any country worldwide from the World Bank. Covers 200+ countries. For detailed U.S. Treasury data, use national_debt or federal_spending instead.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGlobalFiscalData,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Example: "Japan", Description: "Country name (e.g., 'Japan', 'Germany') or ISO3 code (e.g., 'JPN', 'DEU')"},
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
				{Name: "debt_to_gdp_pct", Type: "number", Description: "Central government debt as percentage of GDP"},
				{Name: "revenue_to_gdp_pct", Type: "number", Description: "Government revenue as percentage of GDP"},
				{Name: "expenditure_to_gdp_pct", Type: "number", Description: "Government expenditure as percentage of GDP"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name:        "compare_country_fiscal",
		Description: "Compares government fiscal health across multiple countries using World Bank data, covering debt-to-GDP, revenue-to-GDP, and expenditure-to-GDP ratios.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCompareCountryFiscal,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "string", Example: "JPN,DEU,USA", Description: "Comma-separated country names or ISO3 codes to compare"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "year", Type: "string", Example: "2022", Description: "Year for data (default: latest available)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "countries", Type: "array", Description: "Array of country fiscal data objects"},
				{Name: "data_year", Type: "string", Description: "Year of the data"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}
