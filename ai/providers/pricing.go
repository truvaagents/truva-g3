// Package providers — model pricing for trace-side cost annotations.
//
// EstimateCostUSD looks up per-million-token rates for the given model and
// returns the cost of a request in USD. Used by AI providers to stamp
// ai.cost_usd on each ai.generate_response / ai.stream_response span — gives
// per-trace cost visibility in Jaeger without an external billing system.
//
// The table is intentionally small. It covers the model families operators
// most often run in production; everything else returns (0, false) and the
// caller skips the attribute. Add models as you adopt them — the data is
// inherently mutable (providers change pricing) so prefer keeping the table
// short over maintaining a stale exhaustive list.
//
// Source rates are public list prices in USD per 1M tokens at the time of
// writing. Rates are NOT updated automatically; treat the resulting number
// as an estimate, not a billing source of truth.
package providers

import "strings"

// modelPricing holds list-price per 1M tokens for a model family. A value of
// 0 means "unpriced" — the caller should skip emitting cost.
type modelPricing struct {
	inputPerMTok  float64
	outputPerMTok float64
}

// pricingTable maps lowercased prefix → pricing. Lookup is "longest matching
// prefix wins" so "gpt-4.1-2025-04-14" resolves via the "gpt-4.1" row without
// needing one row per dated revision.
var pricingTable = map[string]modelPricing{
	// OpenAI
	"gpt-4.1-mini":  {inputPerMTok: 0.40, outputPerMTok: 1.60},
	"gpt-4.1-nano":  {inputPerMTok: 0.10, outputPerMTok: 0.40},
	"gpt-4.1":       {inputPerMTok: 2.00, outputPerMTok: 8.00},
	"gpt-4o-mini":   {inputPerMTok: 0.15, outputPerMTok: 0.60},
	"gpt-4o":        {inputPerMTok: 2.50, outputPerMTok: 10.00},
	"o3-mini":       {inputPerMTok: 1.10, outputPerMTok: 4.40},
	"o1-mini":       {inputPerMTok: 1.10, outputPerMTok: 4.40},
	"o1":            {inputPerMTok: 15.00, outputPerMTok: 60.00},
	"gpt-3.5-turbo": {inputPerMTok: 0.50, outputPerMTok: 1.50},
	// Anthropic
	"claude-opus-4":     {inputPerMTok: 15.00, outputPerMTok: 75.00},
	"claude-sonnet-4":   {inputPerMTok: 3.00, outputPerMTok: 15.00},
	"claude-haiku-4":    {inputPerMTok: 0.80, outputPerMTok: 4.00},
	"claude-3-5-sonnet": {inputPerMTok: 3.00, outputPerMTok: 15.00},
	"claude-3-5-haiku":  {inputPerMTok: 0.80, outputPerMTok: 4.00},
	"claude-3-opus":     {inputPerMTok: 15.00, outputPerMTok: 75.00},
	// Groq (gpt-oss-120b through Groq's API)
	// Source: https://console.groq.com/docs/model/openai/gpt-oss-120b
	"openai/gpt-oss-120b": {inputPerMTok: 0.15, outputPerMTok: 0.60},
	// Google Gemini
	"gemini-2.5-pro":   {inputPerMTok: 1.25, outputPerMTok: 5.00},
	"gemini-2.5-flash": {inputPerMTok: 0.075, outputPerMTok: 0.30},
	"gemini-1.5-pro":   {inputPerMTok: 1.25, outputPerMTok: 5.00},
	"gemini-1.5-flash": {inputPerMTok: 0.075, outputPerMTok: 0.30},
}

// EstimateCostUSD returns (cost, true) if the model is known. Cost is the sum
// of input and output costs at list price. Returns (0, false) for unknown
// models so the caller can skip the attribute rather than emit a misleading 0.
func EstimateCostUSD(model string, promptTokens, completionTokens int) (float64, bool) {
	if model == "" || (promptTokens == 0 && completionTokens == 0) {
		return 0, false
	}
	rate, ok := lookupPricing(model)
	if !ok {
		return 0, false
	}
	cost := (float64(promptTokens)/1_000_000)*rate.inputPerMTok +
		(float64(completionTokens)/1_000_000)*rate.outputPerMTok
	return cost, true
}

// lookupPricing finds the longest prefix in pricingTable that matches model.
// Case-insensitive. Linear scan — table is small enough that a sort+binary
// search would not pay for itself.
func lookupPricing(model string) (modelPricing, bool) {
	lower := strings.ToLower(model)
	var best modelPricing
	var bestLen int
	for prefix, p := range pricingTable {
		if strings.HasPrefix(lower, prefix) && len(prefix) > bestLen {
			best = p
			bestLen = len(prefix)
		}
	}
	if bestLen == 0 {
		return modelPricing{}, false
	}
	return best, true
}
