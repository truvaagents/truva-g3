package ai_test

import (
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

var _ ai.ClientOption = ai.WithProvider("anthropic")

func TestPublicClientOptionCompatibility(t *testing.T) {
	integration := ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityStrict,
	}
	if integration.CompatibilityMode != requestpolicy.CompatibilityStrict {
		t.Fatal("public integration configuration was not constructible with keyed fields")
	}

	// AIConfig intentionally remains the same 15-field legacy shape because
	// external callers may still use positional literals.
	if fields := reflect.TypeOf(ai.AIConfig{}).NumField(); fields != 15 {
		t.Fatalf("AIConfig field count = %d, want legacy shape of 15", fields)
	}
}
