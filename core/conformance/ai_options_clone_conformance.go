package conformance

import (
	"reflect"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type cloneNamedKey string
type cloneNamedBool bool
type cloneNamedString string
type cloneNamedInt int
type cloneNamedUint uint
type cloneNamedFloat float64
type cloneNamedMap map[cloneNamedKey][]cloneNamedInt
type cloneNamedSlice []cloneNamedString
type cloneNamedArray [1][]cloneNamedInt

type cloneOpaqueValue struct {
	Value string
}

// LegacyAIOptionsCloner returns an isolated legacy-options snapshot.
type LegacyAIOptionsCloner func(*core.AIOptions) (*core.AIOptions, error)

// JSONValueCloner returns an isolated copy of a JSON-compatible value.
type JSONValueCloner func(interface{}) (interface{}, error)

// RunJSONValueCloneConformance verifies the common acyclic JSON-value cloning
// contract shared by Core, provider adapters, and the request-policy engine.
func RunJSONValueCloneConformance(t *testing.T, clone JSONValueCloner) {
	t.Helper()
	if clone == nil {
		t.Fatal("JSON value cloner is nil")
	}

	clonedNil, err := clone(nil)
	if err != nil || clonedNil != nil {
		t.Fatalf("clone(nil) = (%#v, %v), want (nil, nil)", clonedNil, err)
	}

	original := newAcyclicCloneFixture()
	clonedValue, err := clone(original)
	if err != nil {
		t.Fatalf("clone returned error: %v", err)
	}
	cloned, ok := clonedValue.(map[string]interface{})
	if !ok {
		t.Fatalf("clone type = %T, want map[string]interface{}", clonedValue)
	}
	if !reflect.DeepEqual(cloned, original) {
		t.Fatalf("clone changed values:\n got: %#v\nwant: %#v", cloned, original)
	}

	assertCloneTypesPreserved(t, original, cloned)
	mutateAcyclicClone(t, cloned)
	assertAcyclicFixtureUnchanged(t, original)

	secondValue, err := clone(original)
	if err != nil {
		t.Fatalf("second clone returned error: %v", err)
	}
	second, ok := secondValue.(map[string]interface{})
	if !ok {
		t.Fatalf("second clone type = %T, want map[string]interface{}", secondValue)
	}
	assertAcyclicFixtureUnchanged(t, second)
}

// RunLegacyAIOptionsCloneConformance verifies the shared legacy cloning
// contract used by Core and provider adapters. It adds header, cycle, and
// backward-compatible opaque-leaf requirements to the common JSON contract.
func RunLegacyAIOptionsCloneConformance(t *testing.T, clone LegacyAIOptionsCloner) {
	t.Helper()
	if clone == nil {
		t.Fatal("legacy AI options cloner is nil")
	}

	t.Run("NilOptions", func(t *testing.T) {
		cloned, err := clone(nil)
		if err != nil || cloned != nil {
			t.Fatalf("clone(nil) = (%#v, %v), want (nil, nil)", cloned, err)
		}
	})

	t.Run("HeadersAreIsolated", func(t *testing.T) {
		original := &core.AIOptions{Headers: map[string]string{"X-Test": "original"}}
		cloned, err := clone(original)
		if err != nil {
			t.Fatalf("clone returned error: %v", err)
		}
		if cloned == nil {
			t.Fatal("clone returned nil options")
		}
		cloned.Headers["X-Test"] = "clone"
		if original.Headers["X-Test"] != "original" {
			t.Fatal("cloned headers alias the original")
		}

		empty := &core.AIOptions{Headers: map[string]string{}, Extra: map[string]interface{}{}}
		clonedEmpty, err := clone(empty)
		if err != nil {
			t.Fatalf("clone with empty containers returned error: %v", err)
		}
		if clonedEmpty == nil {
			t.Fatal("clone with empty containers returned nil options")
		}
		if clonedEmpty.Headers == nil || clonedEmpty.Extra == nil {
			t.Fatalf("non-nil empty containers were not preserved: %#v", clonedEmpty)
		}
		clonedEmpty.Headers["clone-only"] = "value"
		clonedEmpty.Extra["clone-only"] = true
		if len(empty.Headers) != 0 || len(empty.Extra) != 0 {
			t.Fatal("cloned empty containers alias the originals")
		}
	})

	t.Run("JSONValues", func(t *testing.T) {
		RunJSONValueCloneConformance(t, func(value interface{}) (interface{}, error) {
			cloned, err := clone(&core.AIOptions{Extra: map[string]interface{}{"value": value}})
			if err != nil {
				return nil, err
			}
			if cloned == nil {
				t.Fatal("clone returned nil options")
			}
			return cloned.Extra["value"], nil
		})
	})

	t.Run("CyclesAreIsolatedAndPreserved", func(t *testing.T) {
		mapCycle := map[string]interface{}{}
		mapCycle["self"] = mapCycle
		sliceCycle := make([]interface{}, 1)
		sliceCycle[0] = sliceCycle
		original := &core.AIOptions{Extra: map[string]interface{}{
			"map-cycle":   mapCycle,
			"slice-cycle": sliceCycle,
		}}

		cloned, err := clone(original)
		if err != nil {
			t.Fatalf("clone returned error: %v", err)
		}
		clonedMap := cloned.Extra["map-cycle"].(map[string]interface{})
		clonedMap["clone-only"] = true
		if _, exists := mapCycle["clone-only"]; exists {
			t.Fatal("cloned cyclic map aliases the original")
		}
		if _, exists := clonedMap["self"].(map[string]interface{})["clone-only"]; !exists {
			t.Fatal("cloned cyclic map does not preserve its self-reference")
		}

		clonedSlice := cloned.Extra["slice-cycle"].([]interface{})
		clonedSlice[0].([]interface{})[0] = "clone-only"
		if got := clonedSlice[0]; got != "clone-only" {
			t.Fatalf("cloned cyclic slice does not preserve its self-reference: %v", got)
		}
		if _, ok := sliceCycle[0].([]interface{}); !ok {
			t.Fatal("cloned cyclic slice aliases the original")
		}
	})

	t.Run("OpaqueLeavesRemainShared", func(t *testing.T) {
		opaque := &cloneOpaqueValue{Value: "original"}
		originalMap := map[string]*cloneOpaqueValue{"value": opaque}
		originalSlice := []*cloneOpaqueValue{opaque}
		original := &core.AIOptions{Extra: map[string]interface{}{
			"map":   originalMap,
			"slice": originalSlice,
		}}

		cloned, err := clone(original)
		if err != nil {
			t.Fatalf("clone returned error: %v", err)
		}
		clonedMap := cloned.Extra["map"].(map[string]*cloneOpaqueValue)
		clonedSlice := cloned.Extra["slice"].([]*cloneOpaqueValue)
		if clonedMap["value"] != opaque || clonedSlice[0] != opaque {
			t.Fatal("opaque leaves were not retained by reference")
		}

		clonedMap["clone-only"] = opaque
		clonedSlice[0] = &cloneOpaqueValue{Value: "clone-only"}
		if _, exists := originalMap["clone-only"]; exists {
			t.Fatal("map containing opaque leaves aliases the original container")
		}
		if originalSlice[0] != opaque {
			t.Fatal("slice containing opaque leaves aliases the original container")
		}
	})
}

func newAcyclicCloneFixture() map[string]interface{} {
	return map[string]interface{}{
		"nested": map[string]interface{}{
			"items": []interface{}{map[string]string{"value": "original"}},
		},
		"typed-map":    map[string]int{"value": 1},
		"typed-slice":  []int{1, 2},
		"typed-array":  [1][]int{{1, 2}},
		"named-map":    cloneNamedMap{"value": {1, 2}},
		"named-slice":  cloneNamedSlice{"one", "two"},
		"named-array":  cloneNamedArray{{1, 2}},
		"named-bool":   cloneNamedBool(true),
		"named-string": cloneNamedString("value"),
		"named-int":    cloneNamedInt(-1),
		"named-uint":   cloneNamedUint(1),
		"named-float":  cloneNamedFloat(1.5),
		"nil":          nil,
		"nil-map":      map[string]int(nil),
		"nil-slice":    []int(nil),
	}
}

func assertCloneTypesPreserved(t *testing.T, original, cloned map[string]interface{}) {
	t.Helper()
	for key, originalValue := range original {
		clonedValue, exists := cloned[key]
		if !exists {
			t.Fatalf("clone is missing key %q", key)
		}
		if reflect.TypeOf(clonedValue) != reflect.TypeOf(originalValue) {
			t.Errorf(
				"clone type for %q = %T, want %T",
				key,
				clonedValue,
				originalValue,
			)
		}
	}
}

func mutateAcyclicClone(t *testing.T, cloned map[string]interface{}) {
	t.Helper()
	cloned["nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]string)["value"] = "clone"
	cloned["typed-map"].(map[string]int)["value"] = 9
	cloned["typed-slice"].([]int)[0] = 9
	cloned["typed-array"].([1][]int)[0][0] = 9
	cloned["named-map"].(cloneNamedMap)["value"][0] = 9
	cloned["named-slice"].(cloneNamedSlice)[0] = "clone"
	cloned["named-array"].(cloneNamedArray)[0][0] = 9
}

func assertAcyclicFixtureUnchanged(t *testing.T, value map[string]interface{}) {
	t.Helper()
	if got := value["nested"].(map[string]interface{})["items"].([]interface{})[0].(map[string]string)["value"]; got != "original" {
		t.Fatalf("nested map changed through clone: %q", got)
	}
	if got := value["typed-map"].(map[string]int)["value"]; got != 1 {
		t.Fatalf("typed map changed through clone: %d", got)
	}
	if got := value["typed-slice"].([]int)[0]; got != 1 {
		t.Fatalf("typed slice changed through clone: %d", got)
	}
	if got := value["typed-array"].([1][]int)[0][0]; got != 1 {
		t.Fatalf("typed array changed through clone: %d", got)
	}
	if got := value["named-map"].(cloneNamedMap)["value"][0]; got != 1 {
		t.Fatalf("named map changed through clone: %d", got)
	}
	if got := value["named-slice"].(cloneNamedSlice)[0]; got != "one" {
		t.Fatalf("named slice changed through clone: %q", got)
	}
	if got := value["named-array"].(cloneNamedArray)[0][0]; got != 1 {
		t.Fatalf("named array changed through clone: %d", got)
	}
	if value["nil-map"].(map[string]int) != nil {
		t.Fatal("nil map was not preserved")
	}
	if value["nil-slice"].([]int) != nil {
		t.Fatal("nil slice was not preserved")
	}
}
