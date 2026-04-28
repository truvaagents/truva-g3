package main

import (
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// CurrencyTool provides currency conversion capabilities using Frankfurter API
type CurrencyTool struct {
	*core.BaseTool
	httpClient *http.Client
}

// ConvertRequest represents the input for currency conversion
type ConvertRequest struct {
	From   string  `json:"from"`   // Source currency code (e.g., "USD")
	To     string  `json:"to"`     // Target currency code (e.g., "JPY")
	Amount float64 `json:"amount"` // Amount to convert
}

// ConvertResponse represents the currency conversion result
type ConvertResponse struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Amount float64 `json:"amount"`
	Result float64 `json:"result"`
	Rate   float64 `json:"rate"`
	Date   string  `json:"date"`
}

// RatesRequest represents input for getting exchange rates
type RatesRequest struct {
	Base       string   `json:"base"`                 // Base currency (e.g., "USD")
	Currencies []string `json:"currencies,omitempty"` // Target currencies (empty = all)
}

// RatesResponse represents exchange rates result
type RatesResponse struct {
	Base  string             `json:"base"`
	Date  string             `json:"date"`
	Rates map[string]float64 `json:"rates"`
}

// FrankfurterResponse represents the Frankfurter API response
type FrankfurterResponse struct {
	Amount float64            `json:"amount"`
	Base   string             `json:"base"`
	Date   string             `json:"date"`
	Rates  map[string]float64 `json:"rates"`
}

// Error codes for currency tool
const (
	ErrCodeInvalidCurrency    = "INVALID_CURRENCY"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

// FrankfurterBaseURL is the base URL for Frankfurter API
const FrankfurterBaseURL = "https://api.frankfurter.app"

// NewCurrencyTool creates a new currency tool instance
func NewCurrencyTool() *CurrencyTool {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}

	tool := &CurrencyTool{
		BaseTool: core.NewTool("currency-tool"),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   30 * time.Second,
		},
	}

	tool.registerCapabilities()
	return tool
}

func (c *CurrencyTool) registerCapabilities() {
	// Capability 1: Convert currency
	c.RegisterCapability(core.Capability{
		Name: "convert_currency",
		Description: "Converts an amount from one currency to another using ECB (European Central Bank) daily rates. " +
			"Limited to 31 ECB-tracked currencies. " +
			"For currencies outside ECB coverage, use currency-global-tool instead.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     c.handleConvert,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "from", Type: "string", Example: "USD", Description: "Source currency code (ISO 4217). Limited to 31 ECB currencies. For non-ECB currencies, use currency-global-tool instead."},
				{Name: "to", Type: "string", Example: "EUR", Description: "Target currency code (ISO 4217). Limited to 31 ECB currencies. Use country-info tool's currency.code field. DO NOT use example values. For non-ECB currencies, use currency-global-tool instead."},
				{Name: "amount", Type: "number", Example: "100", Description: "Amount to convert. Must be a positive number from user request."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "from", Type: "string", Description: "Source currency code"},
				{Name: "to", Type: "string", Description: "Target currency code"},
				{Name: "amount", Type: "number", Description: "Original amount"},
				{Name: "result", Type: "number", Description: "Converted amount in target currency"},
				{Name: "rate", Type: "number", Description: "Exchange rate used"},
				{Name: "date", Type: "string", Description: "Date of the exchange rate"},
			},
		},
	})

	// Capability 2: Get exchange rates
	c.RegisterCapability(core.Capability{
		Name: "get_exchange_rates",
		Description: "Gets current exchange rates for a base currency from ECB daily data. " +
			"Limited to 31 ECB-tracked currencies. " +
			"For comprehensive global rates (170+ currencies), use currency-global-tool instead.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     c.handleRates,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "base", Type: "string", Example: "USD", Description: "Base currency code (ISO 4217). Limited to 31 ECB currencies."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "currencies", Type: "array", Example: "[\"EUR\", \"GBP\", \"JPY\"]", Description: "Target currency codes. Limited to 31 ECB currencies. Empty = all available."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "base", Type: "string", Description: "Base currency code"},
				{Name: "date", Type: "string", Description: "Date of the exchange rates"},
				{Name: "rates", Type: "object", Description: "Map of currency codes to exchange rates"},
			},
		},
	})
}
