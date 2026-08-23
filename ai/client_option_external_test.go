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

	// Operational SSE control is an intentional public configuration field.
	if fields := reflect.TypeOf(ai.AIConfig{}).NumField(); fields != 18 {
		t.Fatalf("AIConfig field count = %d, want current shape of 18", fields)
	}
}
