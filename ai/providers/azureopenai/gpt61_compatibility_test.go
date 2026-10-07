package azureopenai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestAzureV1GPT61SolProfile(t *testing.T) {
	for _, model := range []string{"default", "fast", "gpt-6.1-sol", "gpt-6.1-sol-2026-09-29", "gpt-4.1"} {
		for _, omit := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/omit=%t/stream=%t", model, omit, stream), func(t *testing.T) {
					semanticModel := model
					if model == "default" {
						t.Setenv("TRUVAG3_OPENAI_MODEL_DEFAULT", "")
						semanticModel = "gpt-6.1-sol"
					}
					if model == "fast" {
						t.Setenv("TRUVAG3_OPENAI_MODEL_FAST", "")
						semanticModel = "gpt-6-luna"
					}
					resolver := &testResolver{resolved: ai.ResolvedEndpoint{
						URL:        mustURL(t, "https://resource.openai.azure.com/openai/v1/chat/completions"),
						Deployment: "opaque-deployment", RouteIdentity: "sol-profile-v1",
					}}
					client := mustClient(t, &ai.AIConfig{
						ProviderAlias: "azureopenai.v1", APIKey: "synthetic-secret", Model: model,
						MaxTokens: 100, Temperature: 0.2, ReasoningEffort: "high", ReasoningTokenMultiplier: 2,
					}, ai.ProviderIntegrationConfig{EndpointResolver: resolver})
					request := core.NewAIRequest("Return JSON", "profile-contract")
					request.Generation.ResponseFormat = core.SetAIParameter("json")
					if omit {
						request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
					}
					before, err := core.CloneAIRequest(request)
					if err != nil {
						t.Fatal(err)
					}
					invocation, err := client.prepareInvocation(t.Context(), request, stream)
					if err != nil {
						t.Fatal(err)
					}
					var body map[string]interface{}
					if err := json.Unmarshal(invocation.Request.Body, &body); err != nil {
						t.Fatal(err)
					}
					if body["model"] != "opaque-deployment" || len(resolver.requests) != 1 || resolver.requests[0].ResolvedModel != semanticModel {
						t.Fatalf("semantic/wire model separation lost: body=%#v route=%#v", body, resolver.requests)
					}
					wantTokens := 100
					wantTemperature := core.OmitAIParameter[float32]()
					if model == "gpt-4.1" {
						wantTemperature = core.SetAIParameter[float32](0.2)
						if body["max_tokens"] != float64(100) || body["max_completion_tokens"] != nil || body["temperature"] == nil || body["reasoning_effort"] != nil {
							t.Fatalf("ordinary model behavior changed: %#v", body)
						}
					} else {
						if !omit {
							wantTokens = 200
							if body["reasoning_effort"] != "high" {
								t.Fatalf("inherited effort lost: %#v", body)
							}
						} else if _, present := body["reasoning_effort"]; present {
							t.Fatal("explicit omission lost")
						}
						if body["max_completion_tokens"] != float64(wantTokens) || body["max_tokens"] != nil || body["temperature"] != nil || body["reasoning"] != nil {
							t.Fatalf("reasoning profile changed: %#v", body)
						}
					}
					if body["response_format"].(map[string]interface{})["type"] != "json_object" {
						t.Fatal("JSON mode lost")
					}
					if stream {
						if body["stream"] != true || body["stream_options"].(map[string]interface{})["include_usage"] != true {
							t.Fatal("stream usage contract lost")
						}
					} else if _, present := body["stream"]; present {
						t.Fatal("non-streaming profile enables stream")
					}
					report := invocation.Request.Report
					if report == nil || !report.Stable || report.Fingerprint == "" || report.ResolvedModel != semanticModel || report.EffectiveMaxTokens != core.SetAIParameter(wantTokens) || report.EffectiveTemperature != wantTemperature {
						t.Fatalf("effective report mismatch: %#v", report)
					}
					encoded, err := json.Marshal(report)
					if err != nil {
						t.Fatal(err)
					}
					for _, secret := range []string{"opaque-deployment", "synthetic-secret", "resource.openai.azure.com"} {
						if strings.Contains(string(encoded), secret) {
							t.Fatalf("report exposed route/credential: %s", encoded)
						}
					}
					first, stable := client.RequestFingerprint(t.Context(), request)
					if !stable || first == "" {
						t.Fatal("missing stable fingerprint")
					}
					resolver.resolved.Deployment = "another-opaque-deployment"
					second, stable := client.RequestFingerprint(t.Context(), request)
					if !stable || second != first {
						t.Fatal("raw deployment changed semantic identity")
					}
					resolver.resolved.RouteIdentity = "sol-profile-v2"
					third, stable := client.RequestFingerprint(t.Context(), request)
					if !stable || third == first {
						t.Fatal("route identity did not change fingerprint")
					}
					if !reflect.DeepEqual(request, before) {
						t.Fatal("caller input mutated")
					}
				})
			}
		}
	}
}

func TestAzureClassicGPT61SolRejectedBeforeTransport(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "gpt-6.1-sol-2026-09-29", "gpt-4.1"} {
		t.Run(model, func(t *testing.T) {
			resolver := &testResolver{resolved: ai.ResolvedEndpoint{
				URL:        mustURL(t, "https://resource.openai.azure.com/openai/deployments/opaque-deployment/chat/completions?api-version=2024-10-21"),
				Deployment: "opaque-deployment", RouteIdentity: "classic-profile-v1",
			}}
			transport := &recordingTransport{respond: func(_ int, r *http.Request) (*http.Response, error) {
				return successResponse(r, "opaque-deployment", "answer"), nil
			}}
			client := mustClient(t, &ai.AIConfig{
				ProviderAlias: "azureopenai.classic", APIKey: "synthetic", Model: model, MaxTokens: 100,
			}, ai.ProviderIntegrationConfig{EndpointResolver: resolver, HTTPClient: &http.Client{Transport: transport}})
			request := core.NewAIRequest("synthetic", "classic-contract")
			_, err := client.Generate(t.Context(), request)
			if model == "gpt-4.1" {
				if err != nil || len(transport.requests) != 1 {
					t.Fatalf("ordinary classic request changed: error=%v requests=%d", err, len(transport.requests))
				}
				body := transport.requests[0].body
				if body["model"] != nil || body["max_tokens"] != float64(100) || body["max_completion_tokens"] != nil {
					t.Fatalf("classic wire profile changed: %#v", body)
				}
				return
			}
			if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) || len(transport.requests) != 0 {
				t.Fatalf("unverified classic reasoning reached transport: err=%v calls=%d", err, len(transport.requests))
			}
			if _, err := client.Stream(t.Context(), request, func(core.StreamChunk) error {
				t.Error("rejected request emitted a stream chunk")
				return nil
			}); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) || len(transport.requests) != 0 {
				t.Fatalf("unverified classic reasoning stream reached transport: %v", err)
			}
			if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
				t.Fatal("rejected classic profile has stable identity")
			}
		})
	}
}
