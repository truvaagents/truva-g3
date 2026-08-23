package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestOpenRouterPortableProfileAndProtectedDefaults(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "planning", &core.AIOptions{
		Model:       "fast",
		MaxTokens:   100,
		Temperature: 0.4,
		Extra: map[string]interface{}{
			"provider": map[string]interface{}{"sort": "throughput", "order": []interface{}{"Anthropic"}},
			"plugins":  []interface{}{map[string]interface{}{"id": "auto-router"}},
		},
	})
	request.Generation.ReasoningEffort = core.SetAIParameter("HIGH")
	request.Generation.ResponseFormat = core.SetAIParameter("json")

	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepareInvocation returned error: %v", err)
	}
	body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
	if body["model"] != "openai/gpt-5.6-luna" || body["max_completion_tokens"] != float64(100) {
		t.Fatalf("OpenRouter model/token shape = %#v", body)
	}
	for _, absent := range []string{"max_tokens", "temperature", "reasoning_effort", "route"} {
		if _, present := body[absent]; present {
			t.Fatalf("OpenRouter body unexpectedly contains %q: %#v", absent, body)
		}
	}
	reasoning, ok := body["reasoning"].(map[string]interface{})
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	responseFormat, ok := body["response_format"].(map[string]interface{})
	if !ok || responseFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v", body["response_format"])
	}
	provider := requireOpenRouterProviderObject(t, body)
	if provider["data_collection"] != "deny" || provider["zdr"] != true ||
		provider["require_parameters"] != true || provider["sort"] != "throughput" {
		t.Fatalf("provider routing = %#v", provider)
	}
	if invocation.Request.Report == nil || !invocation.Request.Report.Stable || len(invocation.Request.Report.Fingerprint) != 64 {
		t.Fatalf("concrete report = %#v", invocation.Request.Report)
	}
}

func TestOpenRouterPortableExplicitOmitOverridesLegacyValues(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "planning", &core.AIOptions{
		Model: "openai/gpt-5.6-luna", ReasoningEffort: "high", ResponseFormat: "json",
	})
	request.Generation.ReasoningEffort = core.OmitAIParameter[string]()
	request.Generation.ResponseFormat = core.OmitAIParameter[string]()

	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
	for _, field := range []string{"reasoning", "reasoning_effort", "response_format"} {
		if _, present := body[field]; present {
			t.Fatalf("explicit omit left %q in body: %#v", field, body)
		}
	}
}

func TestOpenRouterProfileNoneAndOrdinarySampling(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	for _, test := range []struct {
		name   string
		effort string
	}{
		{name: "ordinary"},
		{name: "none", effort: "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
				Model: "openai/gpt-5.6-luna", MaxTokens: 25, Temperature: 0.7, ReasoningEffort: test.effort,
			})
			invocation, err := client.prepareInvocation(t.Context(), request, false)
			if err != nil {
				t.Fatal(err)
			}
			body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
			if body["max_completion_tokens"] != float64(25) || body["temperature"] != 0.7 {
				t.Fatalf("body = %#v", body)
			}
			if test.effort == "none" {
				reasoning := body["reasoning"].(map[string]interface{})
				if reasoning["effort"] != "none" {
					t.Fatalf("reasoning = %#v", reasoning)
				}
			}
		})
	}
}

func TestOpenRouterPortableReasoningPreservesNativeSiblingsAndReportsConflict(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-luna",
		Extra: map[string]interface{}{
			"reasoning": map[string]interface{}{"effort": "low", "exclude": true},
		},
	})
	request.Generation.ReasoningEffort = core.SetAIParameter("high")
	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
	reasoning, ok := body["reasoning"].(map[string]interface{})
	if !ok || reasoning["effort"] != "high" || reasoning["exclude"] != true {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	found := false
	for _, adjustment := range invocation.Request.Report.Adjustments {
		if adjustment.Path == "/reasoning/effort" && adjustment.Rule == "generation-precedence" {
			found = true
		}
	}
	if !found {
		t.Fatalf("portable/native reasoning conflict was not reported: %#v", invocation.Request.Report.Adjustments)
	}
}

func TestOpenRouterNativeFieldsAndCapabilityBoundary(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openrouter/pareto-code",
		Extra: map[string]interface{}{
			"reasoning":        map[string]interface{}{"effort": "low"},
			"reasoning_effort": "high",
			"session_id":       "sticky-session",
			"models":           []interface{}{},
		},
	})
	invocation, err := client.prepareInvocation(t.Context(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	body := decodePreparedOpenRouterBody(t, invocation.Request.Body)
	if body["session_id"] != "sticky-session" || body["reasoning"] == nil {
		t.Fatalf("native fields = %#v", body)
	}
	if _, present := body["reasoning_effort"]; present {
		t.Fatalf("top-level native reasoning_effort survived: %#v", body)
	}

	inherited := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openrouter/pareto-code", ReasoningEffort: "high", ResponseFormat: "json",
	})
	invocation, err = client.prepareInvocation(t.Context(), inherited, false)
	if err != nil {
		t.Fatal(err)
	}
	body = decodePreparedOpenRouterBody(t, invocation.Request.Body)
	if _, present := body["reasoning"]; present {
		t.Fatalf("inherited unsupported reasoning survived: %#v", body)
	}
	if _, present := body["response_format"]; present {
		t.Fatalf("inherited unsupported response format survived: %#v", body)
	}
	adjustments := invocation.Request.Report.Adjustments
	if len(adjustments) != 2 || adjustments[0].Source != "portable-default" || adjustments[1].Source != "portable-default" {
		t.Fatalf("preparation adjustments = %#v", adjustments)
	}

	for _, model := range []string{"openrouter/auto", "openrouter/auto:nitro", "openrouter/pareto-code"} {
		explicit := core.NewAIRequest("hello", "")
		explicit.Generation.Model = model
		explicit.Generation.ResponseFormat = core.SetAIParameter("json")
		if _, err := client.prepareInvocation(t.Context(), explicit, false); !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
			t.Fatalf("model=%q explicit unsupported error = %v", model, err)
		}
	}
}

func TestOpenRouterRejectsInvalidEffortAndProtectedConflicts(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	invalidEffort := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-luna", ReasoningEffort: "extreme",
	})
	if _, err := client.prepareInvocation(t.Context(), invalidEffort, false); err == nil || strings.Contains(err.Error(), "extreme") {
		t.Fatalf("invalid effort error = %v", err)
	}

	for _, provider := range []interface{}{
		map[string]interface{}{"data_collection": "allow"},
		map[string]interface{}{"zdr": false},
		map[string]interface{}{"require_parameters": false},
		"invalid",
	} {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openai/gpt-5.6-luna", ReasoningEffort: "high",
			Extra: map[string]interface{}{"provider": provider},
		})
		_, err := client.prepareInvocation(t.Context(), request, false)
		var policyErr *requestpolicy.PolicyError
		if !errors.As(err, &policyErr) || strings.Contains(err.Error(), "allow") {
			t.Fatalf("protected conflict error = %v", err)
		}
	}

	for _, extra := range []map[string]interface{}{
		{
			"provider": map[string]interface{}{"sort": "price"},
			"Provider": map[string]interface{}{"sort": "throughput"},
		},
		{
			"provider": map[string]interface{}{"zdr": true, "ZDR": true},
		},
	} {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openai/gpt-5.6-luna", Extra: extra,
		})
		_, err := client.prepareInvocation(t.Context(), request, false)
		var policyErr *requestpolicy.PolicyError
		if !errors.As(err, &policyErr) || policyErr.Path != "/provider" {
			t.Fatalf("case-variant provider conflict error = %v", err)
		}
	}
}

func TestOpenRouterPostPolicyInvariantsAndSessionID(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	for _, patch := range []core.AIProviderPatch{
		{Name: "weaken-zdr", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/provider/zdr": false}},
		{Name: "remove-privacy", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Remove: []string{"/provider/data_collection"}},
		{Name: "weaken-require", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/provider/require_parameters": false}},
		{Name: "case-zdr", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/provider/ZDR": false}},
		{Name: "case-data-collection", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/provider/Data_Collection": "allow"}},
		{Name: "case-require", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/provider/Require_Parameters": false}},
		{Name: "case-provider", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/Provider/zdr": false}},
		{Name: "case-model", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/Model": "paid/other"}},
	} {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "openai/gpt-5.6-luna", ReasoningEffort: "high"})
		request.Patches = []core.AIProviderPatch{patch}
		invocation, err := client.prepareInvocation(t.Context(), request, false)
		var policyErr *requestpolicy.PolicyError
		if !errors.As(err, &policyErr) || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil || invocation.Request.Report.Stable {
			t.Fatalf("patch=%q invocation=%#v error=%v", patch.Name, invocation, err)
		}
	}

	valid := []string{strings.Repeat("界", 256), string([]byte{0x61, 0x62})}
	for _, sessionID := range valid {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openai/gpt-5.6-sol", Extra: map[string]interface{}{"session_id": sessionID},
		})
		if _, err := client.prepareInvocation(t.Context(), request, false); err != nil {
			t.Fatalf("valid session rejected: %v", err)
		}
	}
	invalid := []interface{}{strings.Repeat("界", 257), string([]byte{0xff}), 42}
	for _, sessionID := range invalid {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openai/gpt-5.6-sol", Extra: map[string]interface{}{"session_id": sessionID},
		})
		_, err := client.prepareInvocation(t.Context(), request, false)
		if err == nil || strings.Contains(err.Error(), strings.Repeat("界", 10)) {
			t.Fatalf("invalid session error = %v", err)
		}
	}
}

func TestOpenRouterFingerprintAndFreeCostBoundary(t *testing.T) {
	client := NewClient("key", "", openRouterProviderAlias, &core.NoOpLogger{})
	for _, model := range []string{"default", "smart", "code", "openrouter/free", "~anthropic/claude-opus-latest"} {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: model})
		if fingerprint, stable := client.RequestFingerprint(t.Context(), request); stable || fingerprint != "" {
			t.Fatalf("model=%q fingerprint=%q stable=%t", model, fingerprint, stable)
		}
	}
	fast := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "fast"})
	if fingerprint, stable := client.RequestFingerprint(t.Context(), fast); !stable || len(fingerprint) != 64 {
		t.Fatalf("fast fingerprint=%q stable=%t", fingerprint, stable)
	}
	concrete := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"})
	if fingerprint, stable := client.RequestFingerprint(t.Context(), concrete); !stable || len(fingerprint) != 64 {
		t.Fatalf("concrete fingerprint=%q stable=%t", fingerprint, stable)
	}
	concreteFree := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "vendor/model:free"})
	if fingerprint, stable := client.RequestFingerprint(t.Context(), concreteFree); !stable || len(fingerprint) != 64 {
		t.Fatalf("concrete free fingerprint=%q stable=%t", fingerprint, stable)
	}
	concreteWithFallback := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openai/gpt-5.6-sol", Extra: map[string]interface{}{"models": []interface{}{"anthropic/claude-sonnet"}},
	})
	if fingerprint, stable := client.RequestFingerprint(t.Context(), concreteWithFallback); stable || fingerprint != "" {
		t.Fatalf("fallback fingerprint=%q stable=%t", fingerprint, stable)
	}
	for _, models := range []interface{}{"not-a-list", 42, map[string]interface{}{"model": "anthropic/claude-sonnet"}} {
		malformed := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openai/gpt-5.6-sol", Extra: map[string]interface{}{"models": models},
		})
		invocation, err := client.prepareInvocation(t.Context(), malformed, false)
		var policyErr *requestpolicy.PolicyError
		if !errors.As(err, &policyErr) || policyErr.Path != "/models" || invocation == nil ||
			invocation.Request == nil || invocation.Request.Report == nil || invocation.Request.Report.Stable ||
			invocation.Request.Report.Fingerprint != "" {
			t.Fatalf("paid malformed models=%T invocation=%#v error=%v", models, invocation, err)
		}
		if fingerprint, stable := client.RequestFingerprint(t.Context(), malformed); stable || fingerprint != "" {
			t.Fatalf("paid malformed models=%T fingerprint=%q stable=%t", models, fingerprint, stable)
		}
	}
	patchedMalformed := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "openai/gpt-5.6-sol"})
	patchedMalformed.Patches = []core.AIProviderPatch{{
		Name: "malformed-fallback", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
		Set: map[string]interface{}{"/models": "not-a-list"},
	}}
	if invocation, err := client.prepareInvocation(t.Context(), patchedMalformed, false); err == nil ||
		invocation == nil || invocation.Request == nil || invocation.Request.Report == nil ||
		invocation.Request.Report.Stable || invocation.Request.Report.Fingerprint != "" {
		t.Fatalf("post-policy malformed models invocation=%#v error=%v", invocation, err)
	}

	for _, models := range []interface{}{
		[]interface{}{"openai/gpt-5.6-sol"}, []interface{}{42}, "not-a-list",
	} {
		request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
			Model: "openrouter/free", Extra: map[string]interface{}{"models": models},
		})
		if _, err := client.prepareInvocation(t.Context(), request, false); err == nil || strings.Contains(err.Error(), "gpt-5.6-sol") {
			t.Fatalf("paid/malformed fallback error = %v", err)
		}
	}
	free := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "openrouter/free", Extra: map[string]interface{}{"models": []interface{}{"vendor/a:free", "openrouter/free"}},
	})
	if _, err := client.prepareInvocation(t.Context(), free, false); err != nil {
		t.Fatalf("free-only fallbacks rejected: %v", err)
	}

	t.Setenv("TRUVAG3_OPENROUTER_MODEL_FREE", "")
	semanticFree := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "free"})
	if _, err := client.prepareInvocation(t.Context(), semanticFree, false); err == nil {
		t.Fatal("unverified semantic free alias was accepted")
	}

	t.Setenv("TRUVAG3_OPENROUTER_MODEL_FREE", "vendor/student-model:free")
	verifiedFree, err := client.prepareInvocation(t.Context(), semanticFree, false)
	if err != nil {
		t.Fatalf("verified semantic free override rejected: %v", err)
	}
	if body := decodePreparedOpenRouterBody(t, verifiedFree.Request.Body); body["model"] != "vendor/student-model:free" {
		t.Fatalf("verified semantic free model = %#v", body["model"])
	}

	t.Setenv("TRUVAG3_OPENROUTER_MODEL_FREE", "vendor/paid-model")
	if _, err := client.prepareInvocation(t.Context(), semanticFree, false); err == nil ||
		strings.Contains(err.Error(), "vendor/paid-model") {
		t.Fatalf("paid semantic free override error = %v", err)
	}

	policyFallback := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: "openrouter/free"})
	policyFallback.Patches = []core.AIProviderPatch{{
		Name: "paid-fallback", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
		Set: map[string]interface{}{"/models": []interface{}{"paid/model"}},
	}}
	if _, err := client.prepareInvocation(t.Context(), policyFallback, false); err == nil || strings.Contains(err.Error(), "paid/model") {
		t.Fatalf("policy fallback error = %v", err)
	}

	for _, model := range []string{"openai/gpt-5.6-sol", "openrouter/free"} {
		caseVariantFallback := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{Model: model})
		caseVariantFallback.Patches = []core.AIProviderPatch{{
			Name: "case-variant-fallback", Version: "1", Selector: core.AIProviderSelector{AllProviders: true},
			Set: map[string]interface{}{"/Models": []interface{}{"paid/model"}},
		}}
		invocation, err := client.prepareInvocation(t.Context(), caseVariantFallback, false)
		var policyErr *requestpolicy.PolicyError
		if !errors.As(err, &policyErr) || policyErr.Path != "/models" || invocation == nil ||
			invocation.Request == nil || invocation.Request.Report == nil || invocation.Request.Report.Stable ||
			invocation.Request.Report.Fingerprint != "" {
			t.Fatalf("model=%q invocation=%#v error=%v", model, invocation, err)
		}
		if fingerprint, stable := client.RequestFingerprint(t.Context(), caseVariantFallback); stable || fingerprint != "" {
			t.Fatalf("model=%q case-variant fingerprint=%q stable=%t", model, fingerprint, stable)
		}
	}
}

func TestMergeProtectedProviderValuesDoesNotMutateInput(t *testing.T) {
	provider := map[string]interface{}{"data_collection": "deny", "order": []interface{}{"A"}}
	extra := map[string]interface{}{"provider": provider}
	merged, err := mergeProtectedProviderValues(extra, map[string]interface{}{"data_collection": "deny", "zdr": true})
	if err != nil {
		t.Fatal(err)
	}
	mergedProvider := merged["provider"].(map[string]interface{})
	mergedProvider["order"].([]interface{})[0] = "B"
	if provider["order"].([]interface{})[0] != "A" || reflect.DeepEqual(provider, mergedProvider) {
		t.Fatalf("input was mutated: input=%#v merged=%#v", provider, mergedProvider)
	}
}

func TestOpenRouterCredentialSourceRotatesPerTransportAttempt(t *testing.T) {
	var (
		mu             sync.Mutex
		authorizations []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		attempt := len(authorizations)
		mu.Unlock()

		writer.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"code":503}}`))
			return
		}
		writer.Header().Set("X-Generation-Id", "gen-rotated-2")
		_, _ = writer.Write([]byte(`{"model":"openai/gpt-5.6-sol","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	credentials := &rotatingOpenRouterCredentialSource{}
	requestClient, err := (&Factory{}).CreateRequestClient(&ai.AIConfig{
		ProviderAlias: openRouterProviderAlias,
		BaseURL:       server.URL + "/v1",
		Model:         "openai/gpt-5.6-sol",
		MaxTokens:     32,
		MaxRetries:    1,
		RetryDelay:    time.Millisecond,
	}, ai.ProviderIntegrationConfig{CredentialSource: credentials})
	if err != nil {
		t.Fatalf("CreateRequestClient returned error: %v", err)
	}

	result, err := requestClient.Generate(t.Context(), core.NewAIRequest("hello", "credential-rotation"))
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	mu.Lock()
	gotAuthorizations := append([]string(nil), authorizations...)
	mu.Unlock()
	if !reflect.DeepEqual(gotAuthorizations, []string{"Bearer rotated-1", "Bearer rotated-2"}) {
		t.Fatalf("Authorization headers = %#v", gotAuthorizations)
	}
	if credentials.CallCount() != 2 {
		t.Fatalf("credential calls = %d, want 2", credentials.CallCount())
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" || result.RequestReport == nil {
		t.Fatalf("result = %#v", result)
	}
	report, err := json.Marshal(result.RequestReport)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "rotated-1") || strings.Contains(string(report), "rotated-2") {
		t.Fatalf("request report contains credential material: %s", report)
	}
}

type rotatingOpenRouterCredentialSource struct {
	mu    sync.Mutex
	calls int
}

func (source *rotatingOpenRouterCredentialSource) Credential(
	_ context.Context,
	request ai.CredentialRequest,
) (ai.HeaderCredential, error) {
	if request.ProviderAlias != openRouterProviderAlias || request.ResolvedModel != "openai/gpt-5.6-sol" {
		return ai.HeaderCredential{}, errors.New("unexpected OpenRouter credential request")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return ai.NewHeaderCredential("Authorization", "Bearer rotated-"+strconv.Itoa(source.calls)), nil
}

func (source *rotatingOpenRouterCredentialSource) CallCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func decodePreparedOpenRouterBody(t *testing.T, encoded []byte) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode prepared OpenRouter body: %v", err)
	}
	return body
}

func requireOpenRouterProviderObject(t *testing.T, body map[string]interface{}) map[string]interface{} {
	t.Helper()
	provider, ok := body["provider"].(map[string]interface{})
	if !ok {
		t.Fatalf("provider = %#v", body["provider"])
	}
	return provider
}
