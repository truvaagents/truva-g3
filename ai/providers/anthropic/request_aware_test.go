package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

func TestClientPrepareAIRequest_AppliesPresenceAwareIntentAndRequestPatch(t *testing.T) {
	t.Setenv("TRUVAG3_ANTHROPIC_MODEL_DEFAULT", "claude-sonnet-5-20260701")
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	client.providerAlias = "anthropic.primary"
	client.defaultExtra = map[string]interface{}{"TOP_P": 0.9}

	patchValue := map[string]interface{}{"tenant": "original"}
	request := core.NewAIRequest("hello", "planning")
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopK = core.SetAIParameter(17)
	request.Generation.MaxTokens = core.SetAIParameter(2048)
	request.Generation.SystemPrompt = core.SetAIParameter("")
	request.Patches = []core.AIProviderPatch{{
		Name:    "tenant-override",
		Version: "2",
		Selector: core.AIProviderSelector{
			Provider:      "anthropic",
			ProviderAlias: "anthropic.primary",
			Surface:       "messages",
			Operation:     "generate",
			Purpose:       "planning",
			Model:         "claude-sonnet-5-*",
		},
		Set: map[string]interface{}{
			"/temperature": 0.4,
			"/metadata":    patchValue,
		},
		SetHeaders: map[string]string{"x-feature": "enabled"},
	}}

	prepared, err := client.prepareAIRequest(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepareAIRequest returned error: %v", err)
	}
	patchValue["tenant"] = "caller-mutated"
	body := decodePreparedBody(t, prepared.Body)
	if got := body["temperature"]; got != 0.4 {
		t.Fatalf("request patch did not override built-in removal: %#v", got)
	}
	if _, exists := body["top_p"]; exists {
		t.Fatal("case-folded legacy top_p survived built-in compatibility")
	}
	if _, exists := body["top_k"]; exists {
		t.Fatal("explicit top_k survived built-in compatibility")
	}
	if got := body["max_tokens"]; got != float64(2048) {
		t.Fatalf("max_tokens = %#v, want 2048", got)
	}
	if got := body["system"]; got != "" {
		t.Fatalf("explicit empty system prompt = %#v", got)
	}
	if got := body["metadata"].(map[string]interface{})["tenant"]; got != "original" {
		t.Fatalf("patch value was not isolated: %#v", got)
	}
	if got := prepared.Headers.Get("x-feature"); got != "enabled" {
		t.Fatalf("patched header = %q", got)
	}
	if !prepared.TemperatureSent {
		t.Fatal("effective temperature presence was not recorded")
	}
	report := prepared.Report
	if report == nil || !report.Stable || len(report.Fingerprint) != 64 {
		t.Fatalf("request report missing stable fingerprint: %#v", report)
	}
	if report.Provider != "anthropic" || report.ProviderAlias != "anthropic.primary" || report.Surface != "messages" || report.Operation != "generate" || report.Purpose != "planning" {
		t.Fatalf("request report identity = %#v", report)
	}
	if report.RequestedModel != "default" || report.ResolvedModel != "claude-sonnet-5-20260701" {
		t.Fatalf("request report model identity = %#v", report)
	}
	wantAdjustments := []struct {
		source string
		path   string
		action string
	}{
		{source: "built-in-rule", path: "/temperature", action: "remove"},
		{source: "built-in-rule", path: "/top_p", action: "remove"},
		{source: "built-in-rule", path: "/top_k", action: "remove"},
		{source: "request-patch", path: "/metadata", action: "set"},
		{source: "request-patch", path: "/temperature", action: "set"},
		{source: "request-patch", path: "header:x-feature", action: "set"},
	}
	if len(report.Adjustments) != len(wantAdjustments) {
		t.Fatalf("adjustments = %#v", report.Adjustments)
	}
	for index, want := range wantAdjustments {
		got := report.Adjustments[index]
		if got.Source != want.source || got.Path != want.path || got.Action != want.action {
			t.Fatalf("adjustment %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestClientPrepareAIRequest_PortableOmitAndSyncStreamParity(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "chat", &core.AIOptions{
		Model:        "claude-sonnet-4-6",
		Temperature:  0.7,
		SystemPrompt: "legacy system",
		Extra:        map[string]interface{}{"top_p": 0.8},
	})
	request.Generation.Temperature = core.OmitAIParameter[float32]()
	request.Generation.TopP = core.OmitAIParameter[float32]()
	request.Generation.SystemPrompt = core.OmitAIParameter[string]()

	syncPrepared, err := client.prepareAIRequest(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepare sync request: %v", err)
	}
	streamPrepared, err := client.prepareAIRequest(t.Context(), request, true)
	if err != nil {
		t.Fatalf("prepare stream request: %v", err)
	}
	syncBody := decodePreparedBody(t, syncPrepared.Body)
	streamBody := decodePreparedBody(t, streamPrepared.Body)
	if streamBody["stream"] != true {
		t.Fatalf("stream flag = %#v", streamBody["stream"])
	}
	delete(streamBody, "stream")
	if !reflect.DeepEqual(syncBody, streamBody) {
		t.Fatalf("sync/stream body drift:\nsync=%#v\nstream=%#v", syncBody, streamBody)
	}
	for _, path := range []string{"temperature", "top_p", "system"} {
		if _, exists := syncBody[path]; exists {
			t.Fatalf("portable omit left %q in body", path)
		}
	}
	if syncPrepared.Report.Operation != "generate" || streamPrepared.Report.Operation != "stream" {
		t.Fatalf("report operations = %q, %q", syncPrepared.Report.Operation, streamPrepared.Report.Operation)
	}
	if len(syncPrepared.Report.Adjustments) != 3 {
		t.Fatalf("portable adjustment count = %d, want 3", len(syncPrepared.Report.Adjustments))
	}
	for _, adjustment := range syncPrepared.Report.Adjustments {
		if adjustment.Source != "portable" || adjustment.Action != "remove" {
			t.Fatalf("unexpected portable adjustment: %#v", adjustment)
		}
	}
}

func TestClientPrepareAIRequest_ExplicitSamplingSetNormalizesLegacyCasing(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	request := core.NewAIRequestFromLegacy("hello", "", &core.AIOptions{
		Model: "claude-sonnet-4-6",
		Extra: map[string]interface{}{
			"Temperature": 0.9,
			"TOP_P":       0.8,
		},
		Headers: map[string]string{"x-shared": "request"},
	})
	client.defaultHeaders = map[string]string{"X-Shared": "default"}
	request.Generation.Temperature = core.SetAIParameter(float32(0))
	request.Generation.TopP = core.SetAIParameter(float32(0.25))

	prepared, err := client.prepareAIRequest(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepareAIRequest returned error: %v", err)
	}
	body := decodePreparedBody(t, prepared.Body)
	if got := body["temperature"]; got != float64(0) {
		t.Fatalf("explicit zero temperature = %#v", got)
	}
	if got := body["top_p"]; got != 0.25 {
		t.Fatalf("explicit top_p = %#v", got)
	}
	for key := range body {
		if key != "temperature" && key != "top_p" && (strings.EqualFold(key, "temperature") || strings.EqualFold(key, "top_p")) {
			t.Fatalf("legacy sampling casing survived explicit set: %q", key)
		}
	}
	if got := prepared.Headers.Get("X-Shared"); got != "request" {
		t.Fatalf("case-insensitive request header precedence = %q", got)
	}
}

func TestClientPrepareAIRequest_IsolatesNestedClientExtrasFromPatches(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	client.defaultExtra = map[string]interface{}{
		"metadata": map[string]interface{}{"source": "client"},
	}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "claude-sonnet-4-6"
	request.Patches = []core.AIProviderPatch{{
		Name:     "metadata",
		Version:  "1",
		Selector: core.AIProviderSelector{Provider: "anthropic"},
		Set:      map[string]interface{}{"/metadata/source": "request"},
	}}

	prepared, err := client.prepareAIRequest(t.Context(), request, false)
	if err != nil {
		t.Fatalf("prepareAIRequest returned error: %v", err)
	}
	if got := decodePreparedBody(t, prepared.Body)["metadata"].(map[string]interface{})["source"]; got != "request" {
		t.Fatalf("effective metadata source = %#v", got)
	}
	if got := client.defaultExtra["metadata"].(map[string]interface{})["source"]; got != "client" {
		t.Fatalf("client default extra mutated through draft: %#v", got)
	}
}

func TestClientGenerate_ReturnsReportAndRejectsProtectedPatchBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	var captured map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		defer func() { _ = r.Body.Close() }()
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":2}}`)
	}))
	defer server.Close()

	client := NewClient("anthropic-key", server.URL, &core.NoOpLogger{})
	request := core.NewAIRequest("hello", "generation-test")
	request.Generation.Model = "claude-sonnet-4-6"
	request.Generation.TopK = core.SetAIParameter(9)
	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Content != "ok" || result.RequestReport == nil {
		t.Fatalf("Generate result = %#v", result)
	}
	if got := captured["top_k"]; got != float64(9) {
		t.Fatalf("captured top_k = %#v", got)
	}

	protected := core.NewAIRequest("hello", "protected-test")
	protected.Generation.Model = "claude-sonnet-4-6"
	protected.Patches = []core.AIProviderPatch{{
		Name:       "steal-key",
		Version:    "1",
		Selector:   core.AIProviderSelector{Provider: "anthropic"},
		SetHeaders: map[string]string{"x-api-key": "attacker"},
	}}
	failed, err := client.Generate(t.Context(), protected)
	var policyErr *requestpolicy.PolicyError
	if !errors.As(err, &policyErr) || policyErr.Stage != "mutation" {
		t.Fatalf("protected patch error = %v", err)
	}
	if failed == nil || failed.RequestReport == nil {
		t.Fatalf("protected patch did not return a partial report: %#v", failed)
	}
	if calls.Load() != 1 {
		t.Fatalf("protected patch reached network: calls=%d", calls.Load())
	}
}

func TestClientStream_ReturnsRequestReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"ok\"}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
	}))
	defer server.Close()

	client := NewClient("anthropic-key", server.URL, &core.NoOpLogger{})
	if !client.SupportsStreaming() {
		t.Fatal("Anthropic client unexpectedly reports streaming unsupported")
	}
	request := core.NewAIRequest("hello", "stream-test")
	request.Generation.Model = "claude-sonnet-4-6"
	var content string
	result, err := client.Stream(t.Context(), request, func(chunk core.StreamChunk) error {
		content += chunk.Content
		return nil
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if content != "ok" || result == nil || result.Response == nil || result.Response.Content != "ok" {
		t.Fatalf("Stream result = %#v, callback content = %q", result, content)
	}
	if result.RequestReport == nil || result.RequestReport.Operation != "stream" || result.RequestReport.Purpose != "stream-test" || !result.RequestReport.Stable {
		t.Fatalf("stream request report = %#v", result.RequestReport)
	}
	if _, err := client.Stream(t.Context(), request, nil); err == nil {
		t.Fatal("nil stream callback unexpectedly accepted")
	}
}

func TestClientPrepareAIRequest_RejectsUnrepresentablePortableIntent(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})

	reasoning := core.NewAIRequest("hello", "")
	reasoning.Generation.ReasoningEffort = core.SetAIParameter("high")
	_, err := client.prepareAIRequest(t.Context(), reasoning, false)
	if !errors.Is(err, core.ErrAIRequestFeatureUnsupported) {
		t.Fatalf("reasoning effort error = %v", err)
	}

	maxTokens := core.NewAIRequest("hello", "")
	maxTokens.Generation.MaxTokens = core.OmitAIParameter[int]()
	prepared, err := client.prepareAIRequest(t.Context(), maxTokens, false)
	var policyErr *requestpolicy.PolicyError
	if !errors.As(err, &policyErr) || policyErr.Stage != "draft-validation" {
		t.Fatalf("max_tokens omit error = %v", err)
	}
	if prepared == nil || prepared.Report == nil {
		t.Fatalf("max_tokens omit did not preserve partial report: %#v", prepared)
	}
}

func TestClientGenerate_MissingCredentialStillReturnsPreparedReport(t *testing.T) {
	client := NewClient("", "http://127.0.0.1:1", &core.NoOpLogger{})
	request := core.NewAIRequest("hello", "credential-test")
	request.Generation.Model = "claude-sonnet-4-6"
	result, err := client.Generate(t.Context(), request)
	if err == nil || err.Error() != "anthropic API key not configured" {
		t.Fatalf("Generate error = %v", err)
	}
	if result == nil || result.RequestReport == nil || result.RequestReport.Purpose != "credential-test" {
		t.Fatalf("missing credential result = %#v", result)
	}
}

func TestClientPrepareAIRequest_IsSafeForConcurrentRequestReuse(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	request := core.NewAIRequest("hello", "concurrency")
	request.Generation.Model = "claude-sonnet-4-6"
	request.Patches = []core.AIProviderPatch{{
		Name:     "metadata",
		Version:  "1",
		Selector: core.AIProviderSelector{Provider: "anthropic"},
		Set: map[string]interface{}{
			"/metadata": map[string]interface{}{"tags": []interface{}{"one", "two"}},
		},
	}}
	before := mustJSON(t, request)

	const workers = 24
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			prepared, err := client.prepareAIRequest(context.Background(), request, false)
			if err != nil {
				errorsFound <- err
				return
			}
			if prepared.Report == nil || !prepared.Report.Stable {
				errorsFound <- fmt.Errorf("missing stable report")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent preparation: %v", err)
	}
	if after := mustJSON(t, request); after != before {
		t.Fatalf("shared request mutated:\nbefore=%s\nafter=%s", before, after)
	}
}
