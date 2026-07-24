package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type invocationTestClient struct {
	request  *core.AIRequest
	deferred bool
}

func (c *invocationTestClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return nil, errors.New("legacy path must not be used")
}

func (c *invocationTestClient) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	c.request = request
	c.deferred = telemetry.IsLLMCallRecordingDeferred(ctx)
	return &core.AIResult{
		Response: &core.AIResponse{Content: "ok"},
		RequestReport: &core.AIRequestReport{
			Provider:    "test",
			Purpose:     request.Purpose,
			Fingerprint: "stable",
			Stable:      true,
		},
	}, nil
}

func (c *invocationTestClient) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	c.request = request
	c.deferred = telemetry.IsLLMCallRecordingDeferred(ctx)
	if err := callback(core.StreamChunk{Content: "chunk"}); err != nil {
		return nil, err
	}
	return &core.AIResult{
		Response:      &core.AIResponse{Content: "chunk"},
		RequestReport: &core.AIRequestReport{Purpose: request.Purpose, Fingerprint: "stable", Stable: true},
	}, nil
}

type invocationFingerprintClient struct {
	invocationTestClient
	fingerprint        string
	stable             bool
	fingerprintRequest *core.AIRequest
}

func (c *invocationFingerprintClient) RequestFingerprint(_ context.Context, request *core.AIRequest) (string, bool) {
	c.fingerprintRequest = request
	return c.fingerprint, c.stable
}

type invocationLegacyClient struct {
	prompt  string
	options *core.AIOptions
}

type nilResultInvocationClient struct {
	err error
}

func (c *nilResultInvocationClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return nil, c.err
}

func (c *nilResultInvocationClient) Generate(context.Context, *core.AIRequest) (*core.AIResult, error) {
	return nil, c.err
}

func (c *nilResultInvocationClient) Stream(context.Context, *core.AIRequest, core.StreamCallback) (*core.AIResult, error) {
	return nil, c.err
}

func (c *invocationLegacyClient) GenerateResponse(_ context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	c.prompt = prompt
	c.options = options
	return &core.AIResponse{Content: "legacy", Provider: "legacy", Model: options.Model}, nil
}

func TestInvokeAIPreservesRequestSemanticsAndDeferral(t *testing.T) {
	client := &invocationTestClient{}
	invocation := aiInvocation{
		Purpose: "planning",
		Prompt:  "make a plan",
		Options: &core.AIOptions{Model: "legacy-model", MaxTokens: 100},
		Generation: core.AIGenerationOptions{
			Model:       "request-model",
			Temperature: core.OmitAIParameter[float32](),
		},
		Patches: []core.AIProviderPatch{{
			Name:     "request-rule",
			Version:  "1",
			Selector: core.AIProviderSelector{AllProviders: true},
			Remove:   []string{"/temperature"},
		}},
		DeferRecording: true,
	}

	response, report, err := invokeAI(t.Context(), client, invocation)
	if err != nil {
		t.Fatalf("invokeAI returned error: %v", err)
	}
	if response == nil || response.Content != "ok" || report == nil || report.Purpose != "planning" {
		t.Fatalf("response/report = %#v / %#v", response, report)
	}
	if client.request == nil || client.request.Prompt != invocation.Prompt || client.request.Purpose != invocation.Purpose {
		t.Fatalf("request = %#v", client.request)
	}
	if client.request.Generation.Model != "request-model" || client.request.Generation.Temperature.Mode != core.AIParameterOmit {
		t.Fatalf("generation = %#v", client.request.Generation)
	}
	if got := client.request.LegacyOptions(); got == nil || got.Model != "legacy-model" || got.MaxTokens != 100 {
		t.Fatalf("legacy options = %#v", got)
	}
	if len(client.request.Patches) != 1 || client.request.Patches[0].Name != "request-rule" {
		t.Fatalf("patches = %#v", client.request.Patches)
	}
	if !client.deferred {
		t.Fatal("typed orchestration recording did not defer wrapper recording")
	}
}

func TestStreamAIUsesCoreRequestDispatcher(t *testing.T) {
	client := &invocationTestClient{}
	var chunks []string
	response, report, err := streamAI(t.Context(), client, aiInvocation{
		Purpose:        "synthesis",
		Prompt:         "answer",
		DeferRecording: true,
	}, func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk.Content)
		return nil
	})
	if err != nil {
		t.Fatalf("streamAI returned error: %v", err)
	}
	if response == nil || response.Content != "chunk" || report == nil || report.Purpose != "synthesis" {
		t.Fatalf("response/report = %#v / %#v", response, report)
	}
	if len(chunks) != 1 || chunks[0] != "chunk" || !client.deferred {
		t.Fatalf("chunks/deferred = %#v / %t", chunks, client.deferred)
	}
}

func TestAIInvocationPreservesNilResultErrors(t *testing.T) {
	wantErr := errors.New("provider failed before producing a result")
	client := &nilResultInvocationClient{err: wantErr}
	response, report, err := invokeAI(t.Context(), client, aiInvocation{Purpose: "planning"})
	if response != nil || report != nil || !errors.Is(err, wantErr) {
		t.Fatalf("invokeAI nil result = %#v, %#v, %v", response, report, err)
	}
	response, report, err = streamAI(t.Context(), client, aiInvocation{Purpose: "synthesis"}, func(core.StreamChunk) error { return nil })
	if response != nil || report != nil || !errors.Is(err, wantErr) {
		t.Fatalf("streamAI nil result = %#v, %#v, %v", response, report, err)
	}
}

func TestInvokeAILegacyFallbackAndFingerprintSafety(t *testing.T) {
	legacy := &invocationLegacyClient{}
	invocation := aiInvocation{
		Purpose: "knowledge-extraction",
		Prompt:  "extract",
		Options: &core.AIOptions{Model: "legacy-model"},
	}
	response, report, err := invokeAI(t.Context(), legacy, invocation)
	if err != nil {
		t.Fatalf("legacy invokeAI returned error: %v", err)
	}
	if response == nil || response.Content != "legacy" || report == nil || report.Purpose != invocation.Purpose {
		t.Fatalf("legacy response/report = %#v / %#v", response, report)
	}
	if fingerprint, stable := fingerprintAI(t.Context(), legacy, invocation); !stable || fingerprint != "" {
		t.Fatalf("legacy fingerprint = %q, %t", fingerprint, stable)
	}

	invocation.Generation.Model = "portable-model"
	invocation.Generation.Temperature = core.SetAIParameter(float32(0.2))
	if fingerprint, stable := fingerprintAI(t.Context(), legacy, invocation); !stable || fingerprint != "" {
		t.Fatalf("representable portable legacy fingerprint = %q, %t", fingerprint, stable)
	}
	invocation.Generation.TopK = core.SetAIParameter(10)
	if _, stable := fingerprintAI(t.Context(), legacy, invocation); stable {
		t.Fatal("legacy client with new request semantics must bypass AI-output caches")
	}

	requestAware := &invocationTestClient{}
	if _, stable := fingerprintAI(t.Context(), requestAware, aiInvocation{}); stable {
		t.Fatal("request-aware client without fingerprint capability must bypass AI-output caches")
	}

	fingerprinter := &invocationFingerprintClient{fingerprint: "policy-v1", stable: true}
	if fingerprint, stable := fingerprintAI(t.Context(), fingerprinter, aiInvocation{}); !stable || fingerprint != "policy-v1" {
		t.Fatalf("delegated fingerprint = %q, %t", fingerprint, stable)
	}
	fingerprinter.stable = false
	if _, stable := fingerprintAI(t.Context(), fingerprinter, aiInvocation{}); stable {
		t.Fatal("unstable provider fingerprint must bypass AI-output caches")
	}
}

func TestLLMActivityCompactorFingerprintUsesInvocationSemantics(t *testing.T) {
	client := &invocationFingerprintClient{fingerprint: "activity-policy", stable: true}
	compactor, err := NewLLMActivityCompactor(client,
		WithActivityCompactorModel("activity-model"),
		WithActivityCompactorTemperature(0.25),
	)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, stable := compactor.aiSemanticFingerprint(t.Context())
	if !stable || fingerprint != "activity-policy" {
		t.Fatalf("fingerprint = %q, stable = %t", fingerprint, stable)
	}
	request := client.fingerprintRequest
	if request == nil || request.Purpose != "activity-compaction" {
		t.Fatalf("fingerprint request = %#v", request)
	}
	options := request.LegacyOptions()
	if options == nil || options.Model != "activity-model" || options.Temperature != 0.25 || options.SystemPrompt != activityCompactorSystemPrompt {
		t.Fatalf("fingerprint options = %#v", options)
	}
}
