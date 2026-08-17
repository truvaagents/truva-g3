package anthropic

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestClientPrepareRequest_OmitsSamplingForRestrictedFamilies(t *testing.T) {
	models := []string{
		"claude-opus-5",
		"claude-opus-4-7",
		"claude-opus-4-8-20260701",
		"claude-sonnet-5",
		"claude-fable-5-20260701",
		"claude-mythos-5",
		"claude-mythos-preview-20260701",
	}

	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			client := NewClient("anthropic-key", "", &core.NoOpLogger{})
			client.defaultExtra = map[string]interface{}{
				"Temperature": 0.9,
				"Top_P":       0.8,
			}
			prepared, err := client.prepareRequest("hello", &core.AIOptions{
				Model:       model,
				Temperature: 0.3,
				Extra:       map[string]interface{}{"TOP_K": 17},
			}, false)
			if err != nil {
				t.Fatalf("prepareRequest returned error: %v", err)
			}

			body := decodePreparedBody(t, prepared.Body)
			assertNoSamplingKeys(t, body)
			if prepared.SamplingPolicy != samplingOmitted {
				t.Fatalf("sampling policy = %s, want omitted", prepared.SamplingPolicy)
			}
			if len(prepared.Adjustments) != 3 {
				t.Fatalf("adjustment count = %d, want 3", len(prepared.Adjustments))
			}
		})
	}
}

func TestClientPrepareRequest_PreservesSamplingForAllowedAndUnknownModels(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  samplingPolicy
	}{
		{name: "allowed", model: "claude-sonnet-4-6", want: samplingAllowed},
		{name: "unknown", model: "enterprise-claude-v1", want: samplingUnknown},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("anthropic-key", "", &core.NoOpLogger{})
			prepared, err := client.prepareRequest("hello", &core.AIOptions{
				Model:       test.model,
				Temperature: 0.2,
				Extra: map[string]interface{}{
					"top_p": 0.8,
					"top_k": 11,
				},
			}, false)
			if err != nil {
				t.Fatalf("prepareRequest returned error: %v", err)
			}

			body := decodePreparedBody(t, prepared.Body)
			if got := body["temperature"]; got != 0.2 {
				t.Fatalf("temperature = %#v, want 0.2", got)
			}
			if got := body["top_p"]; got != 0.8 {
				t.Fatalf("top_p = %#v, want 0.8", got)
			}
			if got := body["top_k"]; got != float64(11) {
				t.Fatalf("top_k = %#v, want 11", got)
			}
			if prepared.SamplingPolicy != test.want {
				t.Fatalf("sampling policy = %s, want %s", prepared.SamplingPolicy, test.want)
			}
			if len(prepared.Adjustments) != 0 {
				t.Fatalf("unexpected adjustments: %#v", prepared.Adjustments)
			}
		})
	}
}

func TestClientPrepareRequest_AppliesPolicyAfterAliasResolution(t *testing.T) {
	t.Setenv("TRUVAG3_ANTHROPIC_MODEL_DEFAULT", "claude-sonnet-5-20260701")
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})

	prepared, err := client.prepareRequest("hello", nil, false)
	if err != nil {
		t.Fatalf("prepareRequest returned error: %v", err)
	}
	if prepared.Model != "claude-sonnet-5-20260701" {
		t.Fatalf("resolved model = %q", prepared.Model)
	}
	assertNoSamplingKeys(t, decodePreparedBody(t, prepared.Body))
	if got := adjustmentPaths(prepared.Adjustments); !reflect.DeepEqual(got, []string{"/temperature"}) {
		t.Fatalf("adjusted paths = %#v, want only temperature", got)
	}
}

func TestClientPrepareRequest_BuiltInDefaultUsesSonnet5(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})

	prepared, err := client.prepareRequest("hello", nil, false)
	if err != nil {
		t.Fatalf("prepareRequest returned error: %v", err)
	}
	if prepared.Model != "claude-sonnet-5" {
		t.Fatalf("resolved model = %q, want claude-sonnet-5", prepared.Model)
	}
	assertNoSamplingKeys(t, decodePreparedBody(t, prepared.Body))
	if prepared.SamplingPolicy != samplingOmitted {
		t.Fatalf("sampling policy = %s, want omitted", prepared.SamplingPolicy)
	}
}

func TestClientPrepareRequest_BuiltInPremiumUsesFable5(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})

	prepared, err := client.prepareRequest("hello", &core.AIOptions{Model: "premium"}, false)
	if err != nil {
		t.Fatalf("prepareRequest returned error: %v", err)
	}
	if prepared.Model != "claude-fable-5" {
		t.Fatalf("resolved model = %q, want claude-fable-5", prepared.Model)
	}
	assertNoSamplingKeys(t, decodePreparedBody(t, prepared.Body))
	if prepared.SamplingPolicy != samplingOmitted {
		t.Fatalf("sampling policy = %s, want omitted", prepared.SamplingPolicy)
	}
}

func TestClientPrepareRequest_SyncStreamParityAndIsolation(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	client.defaultHeaders = map[string]string{
		"X-Shared":          "default",
		"anthropic-version": "invalid",
	}
	client.defaultExtra = map[string]interface{}{
		"top_p": 0.9,
		"metadata": map[string]interface{}{
			"source": "default",
		},
	}
	options := &core.AIOptions{
		Model:          "claude-sonnet-5",
		Temperature:    0.3,
		MaxTokens:      2048,
		SystemPrompt:   "system",
		ResponseFormat: "json",
		Extra: map[string]interface{}{
			"top_k": 7,
			"metadata": map[string]interface{}{
				"source": "request",
				"tags":   []interface{}{"one", "two"},
			},
		},
		Headers: map[string]string{
			"X-Shared":          "request",
			"X-Request":         "present",
			"anthropic-version": "invalid",
			"Accept":            "application/json",
		},
	}
	optionsBefore := mustJSON(t, options)
	defaultExtraBefore := mustJSON(t, client.defaultExtra)
	defaultHeadersBefore := mustJSON(t, client.defaultHeaders)

	syncPrepared, err := client.prepareRequest("hello", options, false)
	if err != nil {
		t.Fatalf("prepare sync request: %v", err)
	}
	streamPrepared, err := client.prepareRequest("hello", options, true)
	if err != nil {
		t.Fatalf("prepare stream request: %v", err)
	}

	syncBody := decodePreparedBody(t, syncPrepared.Body)
	streamBody := decodePreparedBody(t, streamPrepared.Body)
	if streamBody["stream"] != true {
		t.Fatalf("stream flag = %#v, want true", streamBody["stream"])
	}
	delete(streamBody, "stream")
	if !reflect.DeepEqual(syncBody, streamBody) {
		t.Fatalf("sync and stream bodies differ:\nsync: %#v\nstream: %#v", syncBody, streamBody)
	}
	assertNoSamplingKeys(t, syncBody)

	if got := syncPrepared.Headers.Get("X-Shared"); got != "request" {
		t.Fatalf("sync X-Shared = %q", got)
	}
	if got := streamPrepared.Headers.Get("X-Shared"); got != "request" {
		t.Fatalf("stream X-Shared = %q", got)
	}
	if got := syncPrepared.Headers.Get("Accept"); got != "application/json" {
		t.Fatalf("sync Accept = %q", got)
	}
	if got := streamPrepared.Headers.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("stream Accept = %q", got)
	}
	for _, prepared := range []*preparedRequest{syncPrepared, streamPrepared} {
		if got := prepared.Headers.Get("anthropic-version"); got != APIVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := prepared.Headers.Get("x-api-key"); got != "anthropic-key" {
			t.Fatalf("x-api-key = %q", got)
		}
		if !reflect.DeepEqual(prepared.LegacySamplingExtras, []string{"/top_k", "/top_p"}) {
			t.Fatalf("legacy sampling extras = %#v", prepared.LegacySamplingExtras)
		}
	}
	if !reflect.DeepEqual(syncPrepared.ProtectedConflicts, []string{"Anthropic-Version"}) {
		t.Fatalf("sync protected conflicts = %#v", syncPrepared.ProtectedConflicts)
	}
	if !reflect.DeepEqual(streamPrepared.ProtectedConflicts, []string{"Accept", "Anthropic-Version"}) {
		t.Fatalf("stream protected conflicts = %#v", streamPrepared.ProtectedConflicts)
	}

	if got := mustJSON(t, options); got != optionsBefore {
		t.Fatalf("caller options mutated:\nbefore: %s\nafter:  %s", optionsBefore, got)
	}
	if got := mustJSON(t, client.defaultExtra); got != defaultExtraBefore {
		t.Fatalf("default extras mutated:\nbefore: %s\nafter:  %s", defaultExtraBefore, got)
	}
	if got := mustJSON(t, client.defaultHeaders); got != defaultHeadersBefore {
		t.Fatalf("default headers mutated:\nbefore: %s\nafter:  %s", defaultHeadersBefore, got)
	}
}

func TestClientRecordRequestPreparation_ReportsEffectiveSampling(t *testing.T) {
	client := NewClient("anthropic-key", "", &core.NoOpLogger{})
	span := &attributeSpan{attributes: make(map[string]interface{})}
	prepared := &preparedRequest{
		Model:                "claude-sonnet-5",
		SamplingPolicy:       samplingOmitted,
		RequestedTemperature: 0.3,
		TemperatureSent:      false,
		Adjustments: []core.AIRequestAdjustment{{
			Source: "built-in-rule", Rule: samplingAdjustmentRule + "@1", Path: "/temperature", Action: "remove",
		}},
	}

	client.recordRequestPreparation(context.Background(), span, prepared)

	if got := span.attributes["ai.sampling.policy"]; got != "omitted" {
		t.Fatalf("sampling policy attribute = %#v", got)
	}
	if got := span.attributes["ai.temperature.requested"]; got != float64(prepared.RequestedTemperature) {
		t.Fatalf("requested temperature attribute = %#v", got)
	}
	if got := span.attributes["ai.temperature.sent"]; got != false {
		t.Fatalf("temperature sent attribute = %#v", got)
	}
	if got := span.attributes["ai.parameters.omitted"]; got != "temperature" {
		t.Fatalf("omitted parameters attribute = %#v", got)
	}
}

func decodePreparedBody(t *testing.T, encoded []byte) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	return body
}

func assertNoSamplingKeys(t *testing.T, body map[string]interface{}) {
	t.Helper()
	for key := range body {
		normalized := strings.ToLower(key)
		if normalized == "temperature" || normalized == "top_p" || normalized == "top_k" {
			t.Fatalf("restricted sampling key %q present in body: %#v", key, body)
		}
	}
}

func mustJSON(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test value: %v", err)
	}
	return string(encoded)
}

type attributeSpan struct {
	attributes map[string]interface{}
}

func (s *attributeSpan) End() {}

func (s *attributeSpan) SetAttribute(key string, value interface{}) {
	s.attributes[key] = value
}

func (s *attributeSpan) RecordError(error) {}

var _ core.Span = (*attributeSpan)(nil)
