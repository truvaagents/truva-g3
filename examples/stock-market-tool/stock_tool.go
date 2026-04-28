package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// StockTool is a focused tool that provides stock market capabilities via Finnhub API
// It demonstrates the passive tool pattern - can register but not discover
type StockTool struct {
	*core.BaseTool
	apiKey string
	client *FinnhubClient
	cache  *core.MemoryStore // local response cache; was previously BaseTool.Memory
}

// StockQuoteRequest represents the input for stock quote requests
type StockQuoteRequest struct {
	Symbol string `json:"symbol"` // Stock ticker symbol (e.g., "AAPL", "GOOGL")
}

// CompanyProfileRequest represents the input for company profile requests
type CompanyProfileRequest struct {
	Symbol string `json:"symbol"` // Stock ticker symbol
}

// CompanyNewsRequest represents the input for company news requests
type CompanyNewsRequest struct {
	Symbol string `json:"symbol"`         // Stock ticker symbol
	From   string `json:"from,omitempty"` // Start date (YYYY-MM-DD)
	To     string `json:"to,omitempty"`   // End date (YYYY-MM-DD)
}

// MarketNewsRequest represents the input for market news requests
type MarketNewsRequest struct {
	Category string `json:"category,omitempty"` // News category (general, forex, crypto, merger)
}

// StockQuoteResponse represents the output for stock quote
type StockQuoteResponse struct {
	Symbol        string  `json:"symbol"`
	CurrentPrice  float64 `json:"current_price"`
	Change        float64 `json:"change"`
	PercentChange float64 `json:"percent_change"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Open          float64 `json:"open"`
	PreviousClose float64 `json:"previous_close"`
	Timestamp     int64   `json:"timestamp"`
	Source        string  `json:"source"`
}

// CompanyProfileResponse represents the output for company profile
type CompanyProfileResponse struct {
	Name                 string  `json:"name"`
	Ticker               string  `json:"ticker"`
	Exchange             string  `json:"exchange"`
	Industry             string  `json:"industry"`
	Country              string  `json:"country"`
	Currency             string  `json:"currency"`
	MarketCapitalization float64 `json:"market_capitalization"`
	IPO                  string  `json:"ipo"`
	Website              string  `json:"website"`
	Logo                 string  `json:"logo"`
	Source               string  `json:"source"`
}

// NewsItem represents a single news article
type NewsItem struct {
	Headline  string `json:"headline"`
	Summary   string `json:"summary"`
	Source    string `json:"source"`
	URL       string `json:"url"`
	Image     string `json:"image,omitempty"`
	Published int64  `json:"published"`
}

// CompanyNewsResponse represents the output for company news
type CompanyNewsResponse struct {
	Symbol string     `json:"symbol"`
	News   []NewsItem `json:"news"`
	From   string     `json:"from,omitempty"`
	To     string     `json:"to,omitempty"`
	Source string     `json:"source"`
}

// MarketNewsResponse represents the output for market news
type MarketNewsResponse struct {
	Category string     `json:"category,omitempty"`
	News     []NewsItem `json:"news"`
	Source   string     `json:"source"`
}

// BasicFinancialsRequest represents the input for basic financials requests
type BasicFinancialsRequest struct {
	Symbol string `json:"symbol"` // Required: Stock ticker symbol (e.g., "AAPL")
}

// BasicFinancialsResponse represents the output for basic financials
// Contains 100+ financial metrics organized by category
type BasicFinancialsResponse struct {
	Symbol     string                 `json:"symbol"`
	MetricType string                 `json:"metric_type"`
	Metric     map[string]interface{} `json:"metric"` // 100+ financial ratios
	Series     FinancialSeries        `json:"series"` // Historical data
	Source     string                 `json:"source"`
}

// FinancialSeries contains annual and quarterly historical data
type FinancialSeries struct {
	Annual    map[string][]PeriodValue `json:"annual"`
	Quarterly map[string][]PeriodValue `json:"quarterly"`
}

// PeriodValue represents a single period value in the series
type PeriodValue struct {
	Period string  `json:"period"`
	Value  float64 `json:"value"`
}

// CompanyEarningsRequest represents the input for company earnings requests
type CompanyEarningsRequest struct {
	Symbol string `json:"symbol"` // Required: Stock ticker symbol
}

// CompanyEarningsResponse represents the output for company earnings
type CompanyEarningsResponse struct {
	Symbol   string            `json:"symbol"`
	Earnings []QuarterEarnings `json:"earnings"`
	Source   string            `json:"source"`
}

// QuarterEarnings represents earnings for a single quarter
type QuarterEarnings struct {
	Period          string  `json:"period"` // e.g., "2025-12-31"
	Year            int     `json:"year"`
	Quarter         int     `json:"quarter"`
	Actual          float64 `json:"actual"`           // Actual EPS
	Estimate        float64 `json:"estimate"`         // Estimated EPS
	Surprise        float64 `json:"surprise"`         // Actual - Estimate
	SurprisePercent float64 `json:"surprise_percent"` // Surprise as percentage
}

// AnnualRevenueRequest represents the input for annual revenue requests
type AnnualRevenueRequest struct {
	Symbol string `json:"symbol"` // Required: Stock ticker symbol
}

// AnnualRevenueResponse represents the output for annual revenue
// Contains annual revenue figures extracted from SEC 10-K filings
type AnnualRevenueResponse struct {
	Symbol  string              `json:"symbol"`
	Revenue []AnnualRevenueItem `json:"revenue"`
	Source  string              `json:"source"`
}

// AnnualRevenueItem represents a single year's revenue
type AnnualRevenueItem struct {
	Year      int     `json:"year"`       // Fiscal year (e.g., 2025)
	Form      string  `json:"form"`       // "10-K"
	Revenue   float64 `json:"revenue"`    // Revenue in dollars
	FiledDate string  `json:"filed_date"` // When the 10-K was filed
}

// NewStockTool creates a new stock market analysis tool
func NewStockTool() *StockTool {
	apiKey := os.Getenv("FINNHUB_API_KEY")

	tool := &StockTool{
		BaseTool: core.NewTool("stock-market-tool"),
		apiKey:   apiKey,
		client:   NewFinnhubClient(apiKey),
		cache:    core.NewMemoryStore(),
	}

	// Register multiple focused capabilities
	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all stock market-related capabilities
func (s *StockTool) registerCapabilities() {
	// Capability 1: Stock Quote (real-time price data)
	// Auto-generated endpoint: /api/capabilities/stock_quote
	// Schema endpoint: /api/capabilities/stock_quote/schema
	s.RegisterCapability(core.Capability{
		Name:        "stock_quote",
		Description: "Gets real-time stock quote including current price, change, high, low, and trading volume.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleStockQuote,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "AAPL",
					Description: "Stock ticker symbol (e.g., AAPL for Apple, GOOGL for Google)",
				},
			},
		},

		// Phase 2b: Output schema — fields match StockQuoteResponse JSON tags
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Description: "Stock ticker symbol that was queried"},
				{Name: "current_price", Type: "number", Description: "Current trading price in the stock's quote currency"},
				{Name: "change", Type: "number", Description: "Absolute change from previous close"},
				{Name: "percent_change", Type: "number", Description: "Percentage change from previous close"},
				{Name: "high", Type: "number", Description: "Today's high price"},
				{Name: "low", Type: "number", Description: "Today's low price"},
				{Name: "open", Type: "number", Description: "Today's opening price"},
				{Name: "previous_close", Type: "number", Description: "Previous trading day's closing price"},
				{Name: "timestamp", Type: "number", Example: "1712419200", Description: "Unix timestamp of the quote"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
		},
	})

	// Capability 2: Company Profile
	// Auto-generated endpoint: /api/capabilities/company_profile
	s.RegisterCapability(core.Capability{
		Name:        "company_profile",
		Description: "Gets comprehensive company information including name, industry, market cap, IPO date, and website.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleCompanyProfile,

		// Phase 2: Field hints
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "TSLA",
					Description: "Stock ticker symbol for company information",
				},
			},
		},

		// Phase 2b: Output schema — fields match CompanyProfileResponse JSON tags
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "name", Type: "string", Description: "Full legal name of the company"},
				{Name: "ticker", Type: "string", Description: "Stock ticker symbol"},
				{Name: "exchange", Type: "string", Description: "Stock exchange where the company is listed"},
				{Name: "industry", Type: "string", Description: "Industry classification"},
				{Name: "country", Type: "string", Description: "Country of incorporation (ISO code)"},
				{Name: "currency", Type: "string", Description: "Reporting currency (ISO 4217 code)"},
				{Name: "market_capitalization", Type: "number", Description: "Market capitalization in millions, denominated in the listing currency returned by the provider (may differ from the reporting currency field; e.g., BMW.DE reports in EUR ~38459 = 38.4B EUR)"},
				{Name: "ipo", Type: "string", Description: "IPO date in YYYY-MM-DD format (empty if not public)"},
				{Name: "website", Type: "string", Description: "Company website URL"},
				{Name: "logo", Type: "string", Description: "URL to the company logo image"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
		},
	})

	// Capability 3: Company News
	// Auto-generated endpoint: /api/capabilities/company_news
	s.RegisterCapability(core.Capability{
		Name:        "company_news",
		Description: "Gets recent news articles for a specific company.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleCompanyNews,

		// Phase 2: Field hints
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "NVDA",
					Description: "Stock ticker symbol for company news",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "from",
					Type:        "string",
					Example:     "2024-01-01",
					Description: "Start date in YYYY-MM-DD format",
				},
				{
					Name:        "to",
					Type:        "string",
					Example:     "2024-01-31",
					Description: "End date in YYYY-MM-DD format",
				},
			},
		},

		// Phase 2b: Output schema — fields match CompanyNewsResponse JSON tags.
		// Each entry in `news` is a NewsItem with: headline, summary, source, url, image (optional), published.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Description: "Stock ticker symbol that was queried"},
				{Name: "news", Type: "array", Description: "List of news articles with headline, summary, source, url, image, and published timestamp"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "from", Type: "string", Description: "Start date that was used for filtering (YYYY-MM-DD)"},
				{Name: "to", Type: "string", Description: "End date that was used for filtering (YYYY-MM-DD)"},
			},
		},
	})

	// Capability 4: Market News
	// Auto-generated endpoint: /api/capabilities/market_news
	s.RegisterCapability(core.Capability{
		Name:        "market_news",
		Description: "Gets general market news and headlines.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleMarketNews,

		// Phase 2: Field hints
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "category",
					Type:        "string",
					Example:     "general",
					Description: "News category: general, forex, crypto, or merger",
				},
			},
		},

		// Phase 2b: Output schema — fields match MarketNewsResponse JSON tags.
		// Each entry in `news` is a NewsItem with: headline, summary, source, url, image (optional), published.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "news", Type: "array", Description: "List of news articles with headline, summary, source, url, image, and published timestamp"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "category", Type: "string", Description: "News category that was used for filtering"},
			},
		},
	})

	// Capability 5: Basic Financials (P0 - Complex nested response for parameter binding testing)
	// Auto-generated endpoint: /api/capabilities/basic_financials
	// Schema endpoint: /api/capabilities/basic_financials/schema
	s.RegisterCapability(core.Capability{
		Name: "basic_financials",
		Description: "Gets comprehensive financial metrics including PE ratios, margins, growth rates, and valuation metrics.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleBasicFinancials,

		// Phase 2: Field hints for AI payload generation (~95% accuracy)
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "AAPL",
					Description: "Stock ticker symbol for financial metrics",
				},
			},
		},

		// Phase 2b: Output schema — fields match BasicFinancialsResponse JSON tags.
		// `metric` is a flat object with 100+ financial ratios; `series` contains
		// nested annual/quarterly historical data (period + value).
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Description: "Stock ticker symbol that was queried"},
				{Name: "metric_type", Type: "string", Description: "Type of metrics returned (e.g., all)"},
				{Name: "metric", Type: "object", Description: "Flat object of 100+ financial ratios such as peNormalizedAnnual, roeRfy, currentRatioAnnual, etc."},
				{Name: "series", Type: "object", Description: "Historical series with annual and quarterly maps; each entry is an array of period/value pairs"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
		},
	})

	// Capability 6: Company Earnings (P0 - For Layer 4 semantic retry testing)
	// Auto-generated endpoint: /api/capabilities/company_earnings
	// Schema endpoint: /api/capabilities/company_earnings/schema
	s.RegisterCapability(core.Capability{
		Name: "company_earnings",
		Description: "Gets historical quarterly earnings showing actual vs estimated EPS and earnings surprises.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleCompanyEarnings,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "TSLA",
					Description: "Stock ticker symbol for earnings history",
				},
			},
		},

		// Phase 2b: Output schema — fields match CompanyEarningsResponse JSON tags.
		// Each entry in `earnings` is a QuarterEarnings with: period, year, quarter,
		// actual, estimate, surprise, surprise_percent.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Description: "Stock ticker symbol that was queried"},
				{Name: "earnings", Type: "array", Description: "List of quarterly earnings entries with period, year, quarter, actual EPS, estimate EPS, surprise, and surprise_percent"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., finnhub)"},
			},
		},
	})

	// Capability 7: Annual Revenue (P1 - Revenue from SEC 10-K filings)
	// Auto-generated endpoint: /api/capabilities/annual_revenue
	// Schema endpoint: /api/capabilities/annual_revenue/schema
	// Note: Free tier only provides annual 10-K filings, not quarterly 10-Q
	s.RegisterCapability(core.Capability{
		Name: "annual_revenue",
		Description: "Gets annual revenue figures from SEC 10-K filings showing historical revenue in dollars.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleAnnualRevenue,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "symbol",
					Type:        "string",
					Example:     "NVDA",
					Description: "Stock ticker symbol for annual revenue from SEC 10-K filings",
				},
			},
		},

		// Phase 2b: Output schema — fields match AnnualRevenueResponse JSON tags.
		// Each entry in `revenue` is an AnnualRevenueItem with: year, form, revenue, filed_date.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "symbol", Type: "string", Description: "Stock ticker symbol that was queried"},
				{Name: "revenue", Type: "array", Description: "List of annual revenue entries with year, form (10-K), revenue in dollars, and filed_date"},
				{Name: "source", Type: "string", Description: "Data provider identifier (e.g., sec, finnhub)"},
			},
		},
	})
}
