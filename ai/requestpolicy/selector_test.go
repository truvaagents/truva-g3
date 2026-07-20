package requestpolicy

import (
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestClonePatches_ValidatesAndIsolates(t *testing.T) {
	nested := map[string]interface{}{
		"items": []interface{}{map[string]interface{}{"value": "original"}},
	}
	patches := []core.AIProviderPatch{{
		Name:     "application-policy",
		Version:  "2",
		Selector: core.AIProviderSelector{AllProviders: true},
		Set:      map[string]interface{}{`/metadata`: nested},
		Remove:   []string{"/temperature"},
		SetHeaders: map[string]string{
			"X-Policy": "original",
		},
		RemoveHeaders: []string{"X-Remove"},
	}}

	cloned, err := ClonePatches(patches)
	if err != nil {
		t.Fatalf("ClonePatches returned error: %v", err)
	}
	nested["items"].([]interface{})[0].(map[string]interface{})["value"] = "caller-mutated"
	patches[0].Remove[0] = "/caller-mutated"
	patches[0].SetHeaders["X-Policy"] = "caller-mutated"
	patches[0].RemoveHeaders[0] = "Caller-Mutated"

	wantNested := map[string]interface{}{
		"items": []interface{}{map[string]interface{}{"value": "original"}},
	}
	if !reflect.DeepEqual(cloned[0].Set[`/metadata`], wantNested) {
		t.Fatalf("cloned Set value was not isolated: %#v", cloned[0].Set[`/metadata`])
	}
	if cloned[0].Remove[0] != "/temperature" ||
		cloned[0].SetHeaders["X-Policy"] != "original" ||
		cloned[0].RemoveHeaders[0] != "X-Remove" {
		t.Fatalf("cloned patch collections were not isolated: %#v", cloned[0])
	}

	if result, err := ClonePatches(nil); err != nil || result != nil {
		t.Fatalf("ClonePatches(nil) = (%#v, %v), want (nil, nil)", result, err)
	}
	if _, err := ClonePatches([]core.AIProviderPatch{{
		Selector: core.AIProviderSelector{AllProviders: true},
	}}); err == nil {
		t.Fatal("invalid patch unexpectedly cloned")
	}
}

func TestCompatibilityModeValid(t *testing.T) {
	if !CompatibilityCompatible.Valid() || !CompatibilityStrict.Valid() {
		t.Fatal("supported compatibility mode reported invalid")
	}
	if CompatibilityMode(255).Valid() {
		t.Fatal("unsupported compatibility mode reported valid")
	}
}
