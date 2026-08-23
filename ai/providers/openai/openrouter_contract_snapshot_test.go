package openai

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type openRouterContractSnapshot struct {
	CheckedAt string `json:"checked_at"`
	Models    map[string]struct {
		ID                  string   `json:"id"`
		Present             bool     `json:"present"`
		SupportedParameters []string `json:"supported_parameters"`
		LiveProbe           string   `json:"live_probe"`
		ReasoningProbe      string   `json:"reasoning_probe"`
		JSONModeProbe       string   `json:"json_mode_probe"`
		JSONSchemaProbe     string   `json:"json_schema_probe"`
	} `json:"models"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

func TestOpenRouterContractSnapshot(t *testing.T) {
	body, err := os.ReadFile("testdata/openrouter_contract_snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot openRouterContractSnapshot
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.CheckedAt != "2026-08-21" {
		t.Fatalf("checked_at = %q", snapshot.CheckedAt)
	}
	for _, model := range []string{"openrouter/auto", "openrouter/auto:nitro", "openrouter/pareto-code", "openrouter/free", "sample_free_variant", "fast_concrete_model", "sample_concrete_model"} {
		if _, present := snapshot.Models[model]; !present {
			t.Errorf("snapshot is missing %q", model)
		}
	}
	auto := snapshot.Models["openrouter/auto"]
	nitro := snapshot.Models["openrouter/auto:nitro"]
	if auto.LiveProbe != "pass" || auto.JSONModeProbe != "intermittent_404" ||
		auto.JSONSchemaProbe != "intermittent_404" || nitro.LiveProbe != "pass" ||
		nitro.ReasoningProbe != "pass" || nitro.JSONModeProbe != "intermittent_404" ||
		nitro.JSONSchemaProbe != "intermittent_404" {
		t.Fatal("router feature stop-rule evidence changed; re-run and review before adding positive capabilities")
	}
	fast := snapshot.Models["fast_concrete_model"]
	if fast.ID != "openai/gpt-5.6-luna" || !fast.Present || fast.LiveProbe != "pass" ||
		!reflect.DeepEqual(fast.SupportedParameters, []string{"max_completion_tokens", "reasoning", "reasoning_effort", "response_format", "structured_outputs"}) ||
		fast.ReasoningProbe != "pass" || fast.JSONModeProbe != "pass" || fast.JSONSchemaProbe != "pass" {
		t.Fatal("concrete fast alias requires all Luna catalog and live proofs")
	}
	if snapshot.Models["openrouter/free"].LiveProbe != "privacy_404" ||
		snapshot.Models["sample_free_variant"].LiveProbe != "privacy_404" {
		t.Fatal("free-route stop-rule evidence changed; re-run and review before advertising free support")
	}
	if snapshot.Models["sample_concrete_model"].ID != "openai/gpt-5.6-sol" ||
		snapshot.Models["sample_concrete_model"].LiveProbe != "pass" {
		t.Fatal("pinned OpenRouter model evidence is missing")
	}
	wantEfforts := []string{"max", "xhigh", "high", "medium", "low", "minimal", "none"}
	if !reflect.DeepEqual(snapshot.ReasoningEfforts, wantEfforts) {
		t.Fatalf("reasoning efforts = %#v", snapshot.ReasoningEfforts)
	}
}
