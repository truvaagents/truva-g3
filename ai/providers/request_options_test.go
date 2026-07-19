package providers

import (
	"net/http"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestCloneAIOptions_Conformance(t *testing.T) {
	conformance.RunLegacyAIOptionsCloneConformance(t, CloneAIOptions)
}

func TestCloneAIOptions_IsolatesNestedLegacyValues(t *testing.T) {
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	original := &core.AIOptions{
		Model:   "model",
		Headers: map[string]string{"X-Test": "original"},
		Extra: map[string]interface{}{
			"nested": map[string]interface{}{
				"items": []interface{}{map[string]interface{}{"value": "original"}},
			},
			"cycle": cycle,
		},
	}

	clone, err := CloneAIOptions(original)
	if err != nil {
		t.Fatalf("CloneAIOptions returned error: %v", err)
	}
	clone.Headers["X-Test"] = "clone"
	cloneNested := clone.Extra["nested"].(map[string]interface{})
	cloneItems := cloneNested["items"].([]interface{})
	cloneItems[0].(map[string]interface{})["value"] = "clone"
	cloneCycle := clone.Extra["cycle"].(map[string]interface{})
	cloneCycle["clone-only"] = true

	if got := original.Headers["X-Test"]; got != "original" {
		t.Fatalf("original header was mutated: %q", got)
	}
	originalNested := original.Extra["nested"].(map[string]interface{})
	originalItems := originalNested["items"].([]interface{})
	if got := originalItems[0].(map[string]interface{})["value"]; got != "original" {
		t.Fatalf("original nested extra was mutated: %#v", got)
	}
	if _, exists := cycle["clone-only"]; exists {
		t.Fatal("original cyclic extra was mutated")
	}
	if _, exists := cloneCycle["self"].(map[string]interface{})["clone-only"]; !exists {
		t.Fatal("cloned cycle does not point to the cloned map")
	}
}

func TestMergeAnyMaps_RequestOverridesDefault(t *testing.T) {
	merged := MergeAnyMaps(
		map[string]interface{}{"temperature": 0.3, "foo": "default"},
		map[string]interface{}{"foo": "request", "bar": true},
	)

	if merged["foo"] != "request" {
		t.Fatalf("expected request value to win, got %v", merged["foo"])
	}
	if merged["bar"] != true {
		t.Fatalf("expected request-only key to be preserved")
	}
}

func TestMergeStringMaps_RequestOverridesDefault(t *testing.T) {
	merged := MergeStringMaps(
		map[string]string{"x-default": "a", "x-shared": "default"},
		map[string]string{"x-shared": "request", "x-request": "b"},
	)

	if merged["x-shared"] != "request" {
		t.Fatalf("expected request value to win, got %q", merged["x-shared"])
	}
	if merged["x-request"] != "b" {
		t.Fatalf("expected request-only key to be preserved, got %q", merged["x-request"])
	}
}

func TestApplyHeaders_ProtectedHeadersWin(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	ApplyHeaders(req, map[string]struct{}{
		"content-type": {},
	}, map[string]string{"X-Default": "a"}, map[string]string{
		"Content-Type": "text/plain",
		"X-Default":    "b",
		"X-Request":    "c",
	})

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected protected content-type to stay unchanged, got %q", got)
	}
	if got := req.Header.Get("X-Default"); got != "b" {
		t.Fatalf("expected request header to override default header, got %q", got)
	}
	if got := req.Header.Get("X-Request"); got != "c" {
		t.Fatalf("expected request header to be applied, got %q", got)
	}
}
