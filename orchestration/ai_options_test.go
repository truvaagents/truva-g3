package orchestration

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestMergeAIOptions_ExplicitZeroValuesOverrideDefaults(t *testing.T) {
	base := &coreAIOptionsStub{
		Temperature: 0.5,
		MaxTokens:   5000,
	}

	merged := mergeAIOptions(base.toCore(), &AIOptionsOverride{
		Temperature: Float32Ptr(0),
		MaxTokens:   IntPtr(0),
	})

	if merged.Temperature != 0 {
		t.Fatalf("expected temperature override to preserve explicit 0, got %v", merged.Temperature)
	}
	if merged.MaxTokens != 0 {
		t.Fatalf("expected max tokens override to preserve explicit 0, got %d", merged.MaxTokens)
	}
}

func TestMergeAIOptions_CopiesMaps(t *testing.T) {
	base := &coreAIOptionsStub{
		Extra:   map[string]interface{}{"a": "b"},
		Headers: map[string]string{"x-test": "base"},
	}

	merged := mergeAIOptions(base.toCore(), &AIOptionsOverride{
		Extra:   map[string]interface{}{"c": "d"},
		Headers: map[string]string{"x-test": "override"},
	})

	merged.Extra["c"] = "mutated"
	merged.Headers["x-test"] = "mutated"

	if got := base.Extra["a"]; got != "b" {
		t.Fatalf("expected base extra map to stay unchanged, got %v", got)
	}
	if got := base.Headers["x-test"]; got != "base" {
		t.Fatalf("expected base headers map to stay unchanged, got %v", got)
	}
}

func TestMergeAIOptions_NilBaseAllocates(t *testing.T) {
	merged := mergeAIOptions(nil, &AIOptionsOverride{
		Model:       StringPtr("smart"),
		Temperature: Float32Ptr(0),
		MaxTokens:   IntPtr(100),
	})

	if merged == nil {
		t.Fatal("expected merged options to be allocated")
	}
	if merged.Model != "smart" || merged.Temperature != 0 || merged.MaxTokens != 100 {
		t.Fatalf("unexpected merged options: %#v", merged)
	}
}

func TestMergeAIOptions_NilOverrideClonesBase(t *testing.T) {
	base := &core.AIOptions{
		Model:          "default",
		Temperature:    0.6,
		MaxTokens:      1200,
		ResponseFormat: "json",
		Extra:          map[string]interface{}{"foo": "bar"},
		Headers:        map[string]string{"x-test": "1"},
	}

	merged := mergeAIOptions(base, nil)
	if merged == base {
		t.Fatal("expected nil override path to clone base, not reuse pointer")
	}

	merged.Extra["foo"] = "mutated"
	merged.Headers["x-test"] = "mutated"
	if base.Extra["foo"] != "bar" || base.Headers["x-test"] != "1" {
		t.Fatalf("expected base maps to stay isolated, got extra=%v headers=%v", base.Extra, base.Headers)
	}
}

func TestMergeAIOptions_AllNilScalarFieldsPassThroughBase(t *testing.T) {
	base := &core.AIOptions{
		Model:           "default",
		Temperature:     0.7,
		MaxTokens:       2500,
		SystemPrompt:    "system",
		ReasoningEffort: "none",
		ResponseFormat:  "json",
	}

	merged := mergeAIOptions(base, &AIOptionsOverride{})
	if merged.Model != base.Model ||
		merged.Temperature != base.Temperature ||
		merged.MaxTokens != base.MaxTokens ||
		merged.SystemPrompt != base.SystemPrompt ||
		merged.ReasoningEffort != base.ReasoningEffort ||
		merged.ResponseFormat != base.ResponseFormat {
		t.Fatalf("expected base scalar fields to pass through unchanged, got %#v", merged)
	}
}

func TestCloneCoreAIOptions_NilInput(t *testing.T) {
	if cloneCoreAIOptions(nil) != nil {
		t.Fatal("expected nil clone for nil input")
	}
}

func TestCloneCoreAIOptions_MapIsolation(t *testing.T) {
	src := &core.AIOptions{
		Extra:   map[string]interface{}{"foo": "bar"},
		Headers: map[string]string{"x-test": "1"},
	}

	cloned := cloneCoreAIOptions(src)
	cloned.Extra["foo"] = "mutated"
	cloned.Headers["x-test"] = "mutated"

	if src.Extra["foo"] != "bar" || src.Headers["x-test"] != "1" {
		t.Fatalf("expected source maps to stay isolated, got extra=%v headers=%v", src.Extra, src.Headers)
	}
}

func TestApplyLegacyAIOptionFields_DoesNotOverrideExplicitNewAPIOptions(t *testing.T) {
	config := DefaultConfig()
	config.SynthesisAIOptions = &AIOptionsOverride{
		Temperature: Float32Ptr(0),
		MaxTokens:   IntPtr(1234),
	}
	config.PlanAIOptions = &AIOptionsOverride{
		MaxTokens: IntPtr(4321),
	}

	applyLegacyAIOptionFields(config)

	if got := config.SynthesisAIOptions.Temperature; got == nil || *got != 0 {
		t.Fatalf("expected explicit synthesis temperature override to survive legacy bridge, got %#v", got)
	}
	if got := config.SynthesisAIOptions.MaxTokens; got == nil || *got != 1234 {
		t.Fatalf("expected explicit synthesis max tokens override to survive legacy bridge, got %#v", got)
	}
	if got := config.PlanAIOptions.MaxTokens; got == nil || *got != 4321 {
		t.Fatalf("expected explicit plan max tokens override to survive legacy bridge, got %#v", got)
	}
}

type coreAIOptionsStub struct {
	Temperature float32
	MaxTokens   int
	Extra       map[string]interface{}
	Headers     map[string]string
}

func (s *coreAIOptionsStub) toCore() *core.AIOptions {
	return &core.AIOptions{
		Temperature: s.Temperature,
		MaxTokens:   s.MaxTokens,
		Extra:       s.Extra,
		Headers:     s.Headers,
	}
}
