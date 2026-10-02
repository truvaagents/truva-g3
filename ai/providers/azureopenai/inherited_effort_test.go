package azureopenai

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

type effortLog struct {
	core.NoOpLogger
	debug, warn int
}

func (l *effortLog) DebugWithContext(context.Context, string, map[string]interface{}) { l.debug++ }
func (l *effortLog) WarnWithContext(context.Context, string, map[string]interface{})  { l.warn++ }

func TestAzureInheritedEffortDiagnostics(t *testing.T) {
	for _, alias := range []string{"azureopenai.classic", "azureopenai.v1"} {
		for _, stream := range []bool{false, true} {
			logger := &effortLog{}
			// Endpoint identity is static and synthetic; preparation never calls it.
			endpoint := "https://example.openai.azure.com/openai/deployments/test/chat/completions?api-version=2025-04-01-preview"
			if alias == "azureopenai.v1" {
				endpoint = "https://example.openai.azure.com/openai/v1/chat/completions"
			}
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{URL: mustURL(t, endpoint), Deployment: "test", RouteIdentity: "synthetic-v1"}}
			client := mustClient(t, &ai.AIConfig{ProviderAlias: alias, APIKey: "key", Model: "gpt-4.1", ReasoningEffort: "none", Logger: logger}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
			request := core.NewAIRequest("synthetic", "contract")
			semantics, err := client.prepareSemantics(t.Context(), request, stream)
			if err != nil {
				t.Fatal(err)
			}
			if semantics.Options.ReasoningEffort != "" || len(semantics.PreparationAdjustments) != 1 || logger.warn != 0 || logger.debug != 1 {
				t.Fatalf("unexpected default diagnostics: %#v debug=%d warn=%d", semantics, logger.debug, logger.warn)
			}
			invocation, err := client.prepareInvocation(t.Context(), request, stream)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, adjustment := range invocation.Request.Report.Adjustments {
				if adjustment.Path == "/reasoning_effort" && adjustment.Rule == "capability-guard" && adjustment.Action == "remove" {
					found = true
				}
			}
			if !found {
				t.Fatalf("prepared report lost omission: %#v", invocation.Request.Report)
			}
			if _, stable := client.RequestFingerprint(t.Context(), request); !stable || logger.warn != 0 {
				t.Fatal("fingerprint warned/failed")
			}
			priorDebug := logger.debug
			request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
			omitted, err := client.prepareSemantics(t.Context(), request, stream)
			if err != nil || len(omitted.PreparationAdjustments) != 0 || logger.debug != priorDebug || logger.warn != 0 {
				t.Fatal("explicit omission was not quiet")
			}
			request.Generation.ReasoningEffort = core.SetAIParameter("high")
			if _, err := client.prepareSemantics(t.Context(), request, stream); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
				t.Fatal("explicit unsupported intent accepted")
			}
			legacy := core.NewAIRequestFromLegacy("synthetic", "contract", &core.AIOptions{ReasoningEffort: "high"})
			legacySemantics, err := client.prepareSemantics(t.Context(), legacy, stream)
			if err != nil || len(legacySemantics.PreparationAdjustments) != 1 || logger.warn != 1 {
				t.Fatal("legacy request degradation lost")
			}
		}
	}
}
