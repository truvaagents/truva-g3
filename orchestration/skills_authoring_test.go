package orchestration

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type authoringTestAIClient struct {
	responses []string
	prompts   []string
	options   []*core.AIOptions
	purposes  []string
}

type requestAwareAuthoringTestClient struct{}

type failingRequestAwareAuthoringTestClient struct{}

func TestDefaultSkillAuthoringAdvisorRejectsTypedNilDependencies(t *testing.T) {
	var client *authoringTestAIClient
	if _, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{
		AIClient: client,
	}); !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("typed-nil AI client error = %v", err)
	}

	validClient := &authoringTestAIClient{}
	var debugStore *MemoryLLMDebugStore
	var logger *core.NoOpLogger
	var telemetryProvider *skillHTTPSpanTelemetry
	tests := []struct {
		name         string
		dependencies DefaultSkillAuthoringAdvisorDependencies
	}{
		{"LLM debug store", DefaultSkillAuthoringAdvisorDependencies{AIClient: validClient, LLMDebugStore: debugStore}},
		{"logger", DefaultSkillAuthoringAdvisorDependencies{AIClient: validClient, Logger: logger}},
		{"telemetry", DefaultSkillAuthoringAdvisorDependencies{AIClient: validClient, Telemetry: telemetryProvider}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewDefaultSkillAuthoringAdvisor(test.dependencies); !errors.Is(err, ErrInvalidSkillPackage) {
				t.Fatalf("typed-nil %s error = %v", test.name, err)
			}
		})
	}
}

func (*requestAwareAuthoringTestClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	return nil, errors.New("legacy path must not be used")
}

func (*requestAwareAuthoringTestClient) Generate(
	_ context.Context,
	request *core.AIRequest,
) (*core.AIResult, error) {
	return &core.AIResult{
		Response: &core.AIResponse{
			Content:  `{"summary":"Clear package.","findings":[],"proposed_patch":[]}`,
			Model:    "effective-reviewer",
			Provider: "test",
		},
		RequestReport: &core.AIRequestReport{
			Provider: "test", Purpose: request.Purpose,
			RequestedModel: "requested-reviewer", ResolvedModel: "effective-reviewer",
			EffectiveTemperature: core.SetAIParameter(float32(0.01)),
			EffectiveMaxTokens:   core.SetAIParameter(700),
			Adjustments: []core.AIRequestAdjustment{{
				Source: "provider", Rule: "model_alias", Path: "generation.model", Action: "resolve",
			}},
			Fingerprint: "sha256:authoring-policy", Stable: true,
		},
	}, nil
}

func (*failingRequestAwareAuthoringTestClient) GenerateResponse(
	context.Context,
	string,
	*core.AIOptions,
) (*core.AIResponse, error) {
	return nil, errors.New("legacy path must not be used")
}

func (*failingRequestAwareAuthoringTestClient) Generate(
	context.Context,
	*core.AIRequest,
) (*core.AIResult, error) {
	return &core.AIResult{Response: &core.AIResponse{Usage: core.TokenUsage{
		PromptTokens: 17, CompletionTokens: 3, TotalTokens: 20,
	}}}, errors.New("provider failed after returning usage")
}

func (client *authoringTestAIClient) GenerateResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	client.prompts = append(client.prompts, prompt)
	client.options = append(client.options, cloneCoreAIOptions(options))
	client.purposes = append(client.purposes, telemetry.GetBaggage(ctx)["ai.purpose"])
	index := min(len(client.prompts)-1, len(client.responses)-1)
	return &core.AIResponse{Content: client.responses[index]}, nil
}

func TestDefaultSkillAuthoringAdvisorOmitsResourceBodiesAndUsesFixedContract(t *testing.T) {
	client := &authoringTestAIClient{responses: []string{
		`{"summary":"Clear package.","findings":[],"proposed_patch":[]}`,
	}}
	metricCapture := &mockTelemetry{}
	advisor, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{
		AIClient: client, AIOptions: &AIOptionsOverride{Model: StringPtr("small-reviewer")},
		MaxOutputTokens: 700, AdditionalGuidance: "Check <b>travel</b> terminology.", Telemetry: metricCapture,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := SkillAuthoringAnalysisInput{
		Ref:        SkillRef{Namespace: "travel", Name: "weather"},
		Package:    validSkillHTTPPackage(),
		Validation: SkillValidationResult{Valid: true},
	}
	input.Package.Resources[0].Content = "DO-NOT-PROJECT-RESOURCE-BODY"
	advice, err := advisor.Analyze(WithRequestID(t.Context(), "request-1"), input)
	if err != nil || advice.Summary != "Clear package." {
		t.Fatalf("Analyze() = %#v, %v", advice, err)
	}
	if len(client.prompts) != 1 || strings.Contains(client.prompts[0], "DO-NOT-PROJECT-RESOURCE-BODY") ||
		!strings.Contains(client.prompts[0], `"content_not_analyzed":true`) {
		t.Fatalf("authoring prompt disclosed resource body or omitted marker: %q", client.prompts[0])
	}
	options := client.options[0]
	if options.Temperature != 0.01 || options.MaxTokens != 700 || options.ResponseFormat != "" ||
		options.Model != "small-reviewer" || !strings.Contains(options.SystemPrompt, "<developer_guidance>") ||
		strings.Contains(options.SystemPrompt, "<b>") ||
		!strings.Contains(options.SystemPrompt, `\u003cb\u003etravel\u003c/b\u003e`) {
		t.Fatalf("authoring options = %#v", options)
	}
	if guidance, output := strings.Index(options.SystemPrompt, "<developer_guidance>"), strings.Index(options.SystemPrompt, "<output_contract>"); guidance < 0 || output < 0 || guidance > output {
		t.Fatalf("authoring guidance must precede the fixed example: %q", options.SystemPrompt)
	}
	if len(client.purposes) != 1 || client.purposes[0] != skillAuthoringAIPurpose {
		t.Fatalf("authoring ai.purpose baggage = %#v", client.purposes)
	}
	assertSkillMetricRecordsUseExactLabels(t, metricCapture.metricRecords)
}

func TestDefaultSkillAuthoringAdvisorRetriesMalformedBoundedOutput(t *testing.T) {
	client := &authoringTestAIClient{responses: []string{
		`{"summary":"missing arrays"}`,
		`{"summary":"Corrected.","findings":[],"proposed_patch":[]}`,
	}}
	advisor, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{AIClient: client})
	if err != nil {
		t.Fatal(err)
	}
	advice, err := advisor.Analyze(t.Context(), SkillAuthoringAnalysisInput{
		Ref: SkillRef{Namespace: "travel", Name: "weather"}, Package: validSkillHTTPPackage(),
	})
	if err != nil || advice.Summary != "Corrected." || len(client.prompts) != 2 {
		t.Fatalf("Analyze() = %#v, %v; calls=%d", advice, err, len(client.prompts))
	}
	if !strings.Contains(client.prompts[1], "previous response did not match") {
		t.Fatalf("retry prompt = %q", client.prompts[1])
	}
}

func TestDefaultSkillAuthoringAdvisorRecordsEffectiveRequestEvidence(t *testing.T) {
	store := NewMemoryLLMDebugStore()
	advisor, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{
		AIClient: &requestAwareAuthoringTestClient{}, LLMDebugStore: store, MaxOutputTokens: 700,
		AIOptions: &AIOptionsOverride{Model: StringPtr("requested-reviewer")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRequestID(t.Context(), "authoring-evidence")
	if _, err := advisor.Analyze(ctx, SkillAuthoringAnalysisInput{
		Ref: SkillRef{Namespace: "travel", Name: "weather"}, Package: validSkillHTTPPackage(),
	}); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetRecord(ctx, "authoring-evidence")
	if err != nil || len(record.Interactions) != 1 {
		t.Fatalf("debug record = %#v, %v", record, err)
	}
	interaction := record.Interactions[0]
	if interaction.RequestedModel != "requested-reviewer" || interaction.EffectiveModel != "effective-reviewer" ||
		interaction.PolicyFingerprint != "sha256:authoring-policy" || !interaction.PolicyStable ||
		len(interaction.Adjustments) != 1 || interaction.Temperature != 0.01 || interaction.MaxTokens != 700 {
		t.Fatalf("authoring interaction = %#v", interaction)
	}
}

func TestDefaultSkillAuthoringAdvisorRecordsReturnedUsageOnFailure(t *testing.T) {
	capture := &mockTelemetry{}
	advisor, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{
		AIClient: &failingRequestAwareAuthoringTestClient{}, Telemetry: capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advisor.Analyze(t.Context(), SkillAuthoringAnalysisInput{
		Ref: SkillRef{Namespace: "travel", Name: "weather"}, Package: validSkillHTTPPackage(),
	}); err == nil {
		t.Fatal("Analyze() succeeded, want provider failure")
	}
	want := map[string]float64{"prompt": 17, "completion": 3, "total": 20}
	seen := make(map[string]float64)
	for _, record := range capture.metricRecords {
		if record.name != skillSelectorTokensMetric || record.labels["selector"] != "authoring" {
			continue
		}
		if record.labels["outcome"] != "error" {
			t.Fatalf("failure token metric labels = %#v", record.labels)
		}
		seen[record.labels["token_kind"]] = record.value
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("failure token metrics = %#v, want %#v", seen, want)
	}
}

func TestDefaultSkillAuthoringAdvisorRejectsUnsafeOverridesAndPatchPaths(t *testing.T) {
	if _, err := NewDefaultSkillAuthoringAdvisor(DefaultSkillAuthoringAdvisorDependencies{
		AIClient: &authoringTestAIClient{}, AIOptions: &AIOptionsOverride{SystemPrompt: StringPtr("replace")},
	}); err == nil {
		t.Fatal("system prompt replacement was accepted")
	}
	if _, err := parseSkillAuthoringAdvice(
		`{"summary":"Review.","findings":[],"proposed_patch":[{"op":"replace","path":"/server_owned_hash","value":"bad"}]}`,
	); err == nil {
		t.Fatal("server-owned patch path was accepted")
	}
	for _, advice := range []string{
		`{"summary":"Review.","findings":[{"code":"Not-Bounded","path":"/description","message":"Clarify activation."}],"proposed_patch":[]}`,
		"{\"summary\":\"Review.\",\"findings\":[{\"code\":\"activation_wording\",\"path\":\"/description\\nforged\",\"message\":\"Clarify activation.\"}],\"proposed_patch\":[]}",
	} {
		if _, err := parseSkillAuthoringAdvice(advice); err == nil {
			t.Fatalf("invalid authoring finding was accepted: %q", advice)
		}
	}
}
