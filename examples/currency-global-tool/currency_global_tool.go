package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// CurrencyGlobalTool provides global currency conversion using CurrencyBeacon API
// It supports 170+ currencies, unlike currency-tool which is limited to 31 ECB currencies
type CurrencyGlobalTool struct {
	*core.BaseTool
	apiKey string
	client *CurrencyBeaconClient
}

// ConvertRequest represents the input for currency conversion
type ConvertRequest struct {
	From   string  `json:"from"`   // Source currency code (e.g., "USD")
	To     string  `json:"to"`     // Target currency code (e.g., "INR")
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

// Error codes for currency-global-tool
const (
	ErrCodeInvalidCurrency    = "INVALID_CURRENCY"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

// NewCurrencyGlobalTool creates and initializes the tool
func NewCurrencyGlobalTool() *CurrencyGlobalTool {
	apiKey := os.Getenv("CURRENCYBEACON_API_KEY")

	tool := &CurrencyGlobalTool{
		BaseTool: core.NewTool("currency-global-tool"),
		apiKey:   apiKey,
		client:   NewCurrencyBeaconClient(apiKey),
	}

	tool.registerCapabilities()
	return tool
}

func (t *CurrencyGlobalTool) registerCapabilities() {
	// Capability 1: Convert currency (170+ currencies)
	t.RegisterCapability(core.Capability{
		Name: "convert_currency",
		Description: "Converts an amount from one currency to another using real-time exchange rates. " +
			"Supports 170+ global currencies. " +
			"Use this instead of currency-tool when the query involves non-ECB currencies.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleConvert,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "from",
					Type:        "string",
					Example:     "USD",
					Description: "Source currency code (ISO 4217). Supports 170+ global currencies. Use country-info tool's currency.code field for accurate codes.",
				},
				{
					Name:        "to",
					Type:        "string",
					Example:     "INR",
					Description: "Target currency code (ISO 4217). Supports 170+ global currencies. Use country-info tool's currency.code field. DO NOT use example values.",
				},
				{
					Name:        "amount",
					Type:        "number",
					Example:     "100",
					Description: "Amount to convert. Must be a positive number from user request.",
				},
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

	// Capability 2: Get exchange rates (170+ currencies)
	t.RegisterCapability(core.Capability{
		Name: "get_exchange_rates",
		Description: "Gets current exchange rates for a base currency against multiple targets. " +
			"Supports 170+ global currencies with hourly updates. " +
			"Use this instead of currency-tool when comprehensive global coverage is needed.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleRates,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "base",
					Type:        "string",
					Example:     "USD",
					Description: "Base currency code (ISO 4217). Supports 170+ global currencies.",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "currencies",
					Type:        "array",
					Example:     `["EUR", "GBP", "JPY", "INR", "CNY"]`,
					Description: "Target currency codes to retrieve rates for. Supports 170+ currencies. If empty, returns all available rates.",
				},
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
