package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type stableGeminiTestMiddleware struct {
	name  string
	apply func(context.Context, requestpolicy.RequestEditor) error
}

func (middleware *stableGeminiTestMiddleware) Name() string { return middleware.name }
func (*stableGeminiTestMiddleware) Version() string         { return "1" }
func (*stableGeminiTestMiddleware) StablePolicyFingerprint() bool {
	return true
}
func (middleware *stableGeminiTestMiddleware) Apply(ctx context.Context, editor requestpolicy.RequestEditor) error {
	return middleware.apply(ctx, editor)
}

type unstableGeminiTestMiddleware struct{}

func (*unstableGeminiTestMiddleware) Name() string    { return "unstable-gemini-test" }
func (*unstableGeminiTestMiddleware) Version() string { return "1" }
func (*unstableGeminiTestMiddleware) Apply(_ context.Context, _ requestpolicy.RequestEditor) error {
	return nil
}

func TestFactoryWiresGeminiApplicationRulesMiddlewareAndRequestPatches(t *testing.T) {
	middleware := &stableGeminiTestMiddleware{
		name: "gemini-policy-middleware",
		apply: func(_ context.Context, editor requestpolicy.RequestEditor) error {
			if err := editor.Set("/generationConfig/topK", 12); err != nil {
				return err
			}
			return editor.SetHeader("X-Gemini-Policy", "enabled")
		},
	}
	requestClient, err := (&Factory{}).CreateRequestClient(
		&ai.AIConfig{APIKey: "key", Model: "gemini-2.5-flash"},
		ai.ProviderIntegrationConfig{
			RequestRules: []core.AIProviderPatch{{
				Name: "application-top-p", Version: "1",
				Selector: core.AIProviderSelector{Provider: "gemini", Surface: "generate-content"},
				Set:      map[string]interface{}{`/generationConfig/topP`: 0.2},
			}},
			RequestMiddleware: []requestpolicy.RequestMiddleware{middleware},
			CompatibilityMode: requestpolicy.CompatibilityStrict,
		},
	)
	if err != nil {
		t.Fatalf("CreateRequestClient: %v", err)
	}
	client := requestClient.(*Client)
	request := core.NewAIRequest("hello", "application-policy")
	request.Generation.Model = "gemini-2.5-flash"
	request.Patches = []core.AIProviderPatch{{
		Name: "request-top-p", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini"},
		Set:      map[string]interface{}{`/generationConfig/topP`: 0.8},
	}}

	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepare invocation: %v", err)
	}
	config := preparedGenerationConfig(t, decodeGeminiPreparedBody(t, invocation.Request.Body))
	if config["topP"] != 0.8 || config["topK"] != float64(12) {
		t.Fatalf("policy precedence/config = %#v", config)
	}
	if invocation.Request.Headers.Get("X-Gemini-Policy") != "enabled" {
		t.Fatalf("middleware header = %q", invocation.Request.Headers.Get("X-Gemini-Policy"))
	}
	if report := invocation.Request.Report; report == nil || !report.Stable || len(report.Fingerprint) != 64 {
		t.Fatalf("request report = %#v", report)
	}
}

func TestGeminiPoliciesRejectProtectedAndWrongSurfaceMutations(t *testing.T) {
	tests := []struct {
		name        string
		application core.AIProviderPatch
		middleware  requestpolicy.RequestMiddleware
		want        string
	}{
		{
			name: "application protected body",
			application: core.AIProviderPatch{
				Name: "protected-body", Version: "1",
				Selector: core.AIProviderSelector{Provider: "gemini"},
				Set:      map[string]interface{}{`/store`: true},
			},
			want: "protected",
		},
		{
			name: "middleware protected header",
			middleware: &stableGeminiTestMiddleware{name: "protected-header", apply: func(_ context.Context, editor requestpolicy.RequestEditor) error {
				return editor.SetHeader("x-goog-api-key", "must-not-win")
			}},
			want: "protected",
		},
		{
			name: "middleware interactions removal",
			middleware: &stableGeminiTestMiddleware{name: "wrong-surface", apply: func(_ context.Context, editor requestpolicy.RequestEditor) error {
				return editor.Remove("/generation_config/temperature")
			}},
			want: "canonical GenerateContent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			integration := ai.ProviderIntegrationConfig{CompatibilityMode: requestpolicy.CompatibilityCompatible}
			if test.application.Name != "" {
				integration.RequestRules = []core.AIProviderPatch{test.application}
			}
			if test.middleware != nil {
				integration.RequestMiddleware = []requestpolicy.RequestMiddleware{test.middleware}
			}
			requestClient, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{APIKey: "key"}, integration)
			if err != nil {
				t.Fatalf("CreateRequestClient: %v", err)
			}
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			invocation, err := requestClient.(*Client).prepareInvocation(t.Context(), request, false)
			if err == nil || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("prepare invocation = %#v, %v; want %q", invocation, err, test.want)
			}
		})
	}

	// A policy explicitly scoped to another surface is an ordinary nonmatch;
	// its native path must not be interpreted as a GenerateContent mutation.
	engine, err := newRequestPolicyEngineWithIntegration([]core.AIProviderPatch{{
		Name: "interactions-only", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini", Surface: "interactions"},
		Remove:   []string{"/generation_config/temperature"},
	}}, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatalf("create nonmatching policy: %v", err)
	}
	client := NewClient("key", "", &core.NoOpLogger{})
	client.requestPolicy = engine
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
		t.Fatalf("nonmatching surface rule affected request: %v", err)
	}
}

func TestGeminiFingerprintIdentityIncludesOnlyContractDimensions(t *testing.T) {
	client := NewClient("secret-key", "", &core.NoOpLogger{})
	base := core.NewAIRequest("prompt one", "planning")
	base.Generation.Model = "gemini-2.5-flash"
	base.Generation.Temperature = core.SetAIParameter(float32(0.2))
	base.Patches = []core.AIProviderPatch{{
		Name: "policy", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini"},
		Set:      map[string]interface{}{`/generationConfig/topP`: 0.3},
		SetHeaders: map[string]string{
			"X-Tenant-Policy": "tenant-one",
		},
	}}
	fingerprint, stable := client.RequestFingerprint(t.Context(), base)
	if !stable || len(fingerprint) != 64 {
		t.Fatalf("base fingerprint = %q, stable=%t", fingerprint, stable)
	}

	valueOnly := core.NewAIRequest("different secret prompt", "planning")
	valueOnly.Generation.Model = "gemini-2.5-flash"
	valueOnly.Generation.Temperature = core.SetAIParameter(float32(1.4))
	valueOnly.Patches = []core.AIProviderPatch{{
		Name: "policy", Version: "1",
		Selector: core.AIProviderSelector{Provider: "gemini"},
		Set:      map[string]interface{}{`/generationConfig/topP`: 0.9},
		SetHeaders: map[string]string{
			"X-Tenant-Policy": "tenant-two",
		},
	}}
	if got, ok := client.RequestFingerprint(t.Context(), valueOnly); !ok || got != fingerprint {
		t.Fatalf("value-only fingerprint = %q, stable=%t; want %q", got, ok, fingerprint)
	}

	for _, mutation := range []struct {
		name   string
		mutate func(*core.AIRequest)
	}{
		{name: "purpose", mutate: func(request *core.AIRequest) { request.Purpose = "summarization" }},
		{name: "model", mutate: func(request *core.AIRequest) { request.Generation.Model = "gemini-2.5-pro" }},
		{name: "policy version", mutate: func(request *core.AIRequest) { request.Patches[0].Version = "2" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			candidate, err := core.CloneAIRequest(base)
			if err != nil {
				t.Fatalf("clone request: %v", err)
			}
			mutation.mutate(candidate)
			got, ok := client.RequestFingerprint(t.Context(), candidate)
			if !ok || got == fingerprint {
				t.Fatalf("mutated fingerprint = %q, stable=%t; base %q", got, ok, fingerprint)
			}
		})
	}
	if strings.Contains(fingerprint, "secret-key") || strings.Contains(fingerprint, base.Prompt) {
		t.Fatalf("fingerprint exposed request secret: %q", fingerprint)
	}

	unstable := NewClient("key", "", &core.NoOpLogger{})
	engine, err := newRequestPolicyEngineWithIntegration(nil, []requestpolicy.RequestMiddleware{&unstableGeminiTestMiddleware{}}, requestpolicy.CompatibilityCompatible)
	if err != nil {
		t.Fatalf("create unstable policy: %v", err)
	}
	unstable.requestPolicy = engine
	if got, ok := unstable.RequestFingerprint(t.Context(), base); ok || got != "" {
		t.Fatalf("unstable middleware fingerprint = %q, stable=%t", got, ok)
	}
}

func TestGeminiFactoryRejectsInvalidConstructionInputs(t *testing.T) {
	factory := &Factory{}
	if _, err := factory.CreateValidated(nil); err == nil || !strings.Contains(err.Error(), "config is nil") {
		t.Fatalf("CreateValidated(nil) error = %v", err)
	}
	for _, test := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "unsupported scheme", baseURL: "ftp://gateway.example/v1beta", want: "unsupported"},
		{name: "missing host", baseURL: "https:///v1beta", want: "host is empty"},
		{name: "user information", baseURL: "https://user:secret@gateway.example/v1beta", want: "user information"},
		{name: "query", baseURL: "https://gateway.example/v1beta?key=secret", want: "credentials"},
		{name: "fragment", baseURL: "https://gateway.example/v1beta#secret", want: "fragment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := factory.CreateValidated(&ai.AIConfig{BaseURL: test.baseURL})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CreateValidated error = %v; want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "user:secret") || strings.Contains(err.Error(), "key=secret") {
				t.Fatalf("endpoint validation exposed a secret: %v", err)
			}
		})
	}

	if _, err := factory.CreateRequestClient(&ai.AIConfig{}, ai.ProviderIntegrationConfig{
		CompatibilityMode: requestpolicy.CompatibilityMode(255),
	}); err == nil || !strings.Contains(err.Error(), "compatibility mode") {
		t.Fatalf("invalid compatibility mode error = %v", err)
	}
	var nilMiddleware *stableGeminiTestMiddleware
	if _, err := factory.CreateRequestClient(&ai.AIConfig{}, ai.ProviderIntegrationConfig{
		RequestMiddleware: []requestpolicy.RequestMiddleware{nilMiddleware},
	}); err == nil || !strings.Contains(err.Error(), "middleware 0 is nil") {
		t.Fatalf("typed-nil middleware error = %v", err)
	}
}
