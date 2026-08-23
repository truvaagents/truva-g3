package openai

import (
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestNormalizeCompatibleEndpointErrorIsStatusStructured(t *testing.T) {
	client := NewClient("key", "", "openai.custom", &core.NoOpLogger{})
	for _, test := range []struct {
		code      int
		status    int
		retryable bool
	}{
		{code: 400, status: 400},
		{code: 402, status: 402, retryable: true},
		{code: 429, status: 429},
		{code: 503, status: 503},
		{code: 0, status: 500},
	} {
		err := client.normalizeCompatibleError(&openaiwire.EndpointError{Code: test.code, Type: "network value"}, "model")
		var providerErr core.ProviderError
		if !errors.As(err, &providerErr) || providerErr.StatusCode() != test.status ||
			providerErr.IsRetryable() != test.retryable || providerErr.Provider() != "openai.custom" ||
			providerErr.Model() != "model" || providerErr.IsTransient() || err.Error() == "" {
			t.Fatalf("code=%d normalized=%v", test.code, err)
		}
	}

	original := errors.New("decode failed")
	if got := client.normalizeCompatibleError(original, "model"); got != original {
		t.Fatalf("non-endpoint error changed: %v", got)
	}
}
