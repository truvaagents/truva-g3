package providers

import (
	"testing"
)

func TestEstimateCostUSD(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		promptTokens     int
		completionTokens int
		wantKnown        bool
		wantApprox       float64 // approximate cost; exact match within 1e-9
	}{
		{
			name:             "empty model",
			model:            "",
			promptTokens:     100,
			completionTokens: 50,
			wantKnown:        false,
		},
		{
			name:             "zero tokens",
			model:            "gpt-4.1",
			promptTokens:     0,
			completionTokens: 0,
			wantKnown:        false,
		},
		{
			name:             "unknown model",
			model:            "made-up-model-xyz",
			promptTokens:     1000,
			completionTokens: 500,
			wantKnown:        false,
		},
		{
			name:             "gpt-4.1 dated revision matches gpt-4.1 prefix",
			model:            "gpt-4.1-2025-04-14",
			promptTokens:     1_000_000,
			completionTokens: 1_000_000,
			wantKnown:        true,
			wantApprox:       2.00 + 8.00, // input + output rates
		},
		{
			name:             "gpt-4.1-mini wins over gpt-4.1 (longest prefix)",
			model:            "gpt-4.1-mini-2024-07-18",
			promptTokens:     1_000_000,
			completionTokens: 1_000_000,
			wantKnown:        true,
			wantApprox:       0.40 + 1.60,
		},
		{
			name:             "case insensitive",
			model:            "GPT-4O-MINI",
			promptTokens:     1_000_000,
			completionTokens: 0,
			wantKnown:        true,
			wantApprox:       0.15,
		},
		{
			name:             "groq gpt-oss",
			model:            "openai/gpt-oss-120b",
			promptTokens:     1_000_000,
			completionTokens: 1_000_000,
			wantKnown:        true,
			wantApprox:       0.15 + 0.60,
		},
		{
			name:             "claude sonnet 4 partial token count",
			model:            "claude-sonnet-4-20250514",
			promptTokens:     500,
			completionTokens: 200,
			wantKnown:        true,
			wantApprox:       (500.0/1_000_000)*3.00 + (200.0/1_000_000)*15.00,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, known := EstimateCostUSD(tt.model, tt.promptTokens, tt.completionTokens)
			if known != tt.wantKnown {
				t.Fatalf("known=%v want %v (cost=%v)", known, tt.wantKnown, cost)
			}
			if !known {
				return
			}
			if abs(cost-tt.wantApprox) > 1e-9 {
				t.Fatalf("cost=%v want %v", cost, tt.wantApprox)
			}
		})
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
