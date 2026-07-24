package requestpolicy

import (
	"reflect"
	"strings"
	"testing"
)

func TestDocument_JSONPointerSemantics(t *testing.T) {
	document := mustDocument(t, DocumentConfig{Body: map[string]interface{}{
		"array": []interface{}{"zero", map[string]interface{}{"old": true}, "two"},
		"fixed": [2]string{"zero", "one"},
	}})

	if err := document.Set("/created/escaped~1key/tilde~0key", "value"); err != nil {
		t.Fatalf("Set missing object parents: %v", err)
	}
	if got, ok := document.Get("/created/escaped~1key/tilde~0key"); !ok || got != "value" {
		t.Fatalf("escaped pointer value = (%#v, %v)", got, ok)
	}
	if err := document.Set("/array/1/new", 2); err != nil {
		t.Fatalf("Set array member: %v", err)
	}
	if got, ok := document.Get("/array/1/new"); !ok || got != 2 {
		t.Fatalf("array member = (%#v, %v)", got, ok)
	}
	if err := document.Remove("/array/0"); err != nil {
		t.Fatalf("Remove array item: %v", err)
	}
	if got := document.Body()["array"].([]interface{}); !reflect.DeepEqual(got, []interface{}{map[string]interface{}{"old": true, "new": 2}, "two"}) {
		t.Fatalf("array after remove = %#v", got)
	}
	if err := document.Remove("/missing/child"); err != nil {
		t.Fatalf("missing remove should be idempotent: %v", err)
	}
	if err := document.Set("/array/-", "append"); err == nil {
		t.Fatal("append token unexpectedly accepted")
	}
	if err := document.Set("/array/03", "bad index"); err == nil {
		t.Fatal("leading-zero array index unexpectedly accepted")
	}
	if err := document.Remove("/fixed/0"); err == nil {
		t.Fatal("fixed-length array element removal unexpectedly accepted")
	}
	for _, path := range []string{"", "not-a-pointer", "/bad~2escape"} {
		if err := document.Set(path, true); err == nil {
			t.Fatalf("invalid path %q unexpectedly accepted", path)
		}
	}
}

func TestDocument_ProtectedAndCaseInsensitiveFields(t *testing.T) {
	document := mustDocument(t, DocumentConfig{
		Body: map[string]interface{}{
			"model":       "protected",
			"Temperature": 0.7,
			"TOP_P":       0.8,
		},
		Headers:              map[string]string{"x-test": "one"},
		ProtectedPaths:       []string{"/model", "/messages", "/structural/child"},
		ProtectedHeaders:     []string{"authorization", "x-api-key"},
		CaseInsensitivePaths: []string{"/temperature", "/top_p"},
	})

	if got, ok := document.Get("/temperature"); !ok || got != 0.7 {
		t.Fatalf("case-insensitive Get = (%#v, %v)", got, ok)
	}
	if err := document.Set("/temperature", 0.2); err != nil {
		t.Fatalf("case-insensitive Set: %v", err)
	}
	if _, duplicate := document.Body()["Temperature"]; duplicate {
		t.Fatal("case-insensitive Set retained the old casing")
	}
	if err := document.Remove("/top_p"); err != nil {
		t.Fatalf("case-insensitive Remove: %v", err)
	}
	if _, exists := document.Get("/top_p"); exists {
		t.Fatal("case-insensitive Remove left a value")
	}
	for _, path := range []string{"/model", "/model/name", "/messages/0/content", "/structural"} {
		if err := document.Set(path, "override"); err == nil || !strings.Contains(err.Error(), "protected") {
			t.Fatalf("protected path %q error = %v", path, err)
		}
	}
	if err := document.SetHeader("X-Test", "two"); err != nil {
		t.Fatalf("SetHeader: %v", err)
	}
	if got, ok := document.Header("x-test"); !ok || got != "two" {
		t.Fatalf("canonical header = (%q, %v)", got, ok)
	}
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "Authorization", value: "secret"},
		{name: "bad header", value: "value"},
		{name: "x-new", value: "bad\r\nvalue"},
		{name: "x-control", value: "bad\x01value"},
	} {
		if err := document.SetHeader(test.name, test.value); err == nil {
			t.Fatalf("invalid/protected header %q unexpectedly accepted", test.name)
		}
	}
	if err := document.RemoveHeader("X-Api-Key"); err == nil {
		t.Fatal("protected header removal unexpectedly accepted")
	}
	if err := document.RemoveHeader("x-test"); err != nil {
		t.Fatalf("RemoveHeader: %v", err)
	}
	if _, exists := document.Header("X-Test"); exists {
		t.Fatal("RemoveHeader left the header present")
	}
	if err := document.RemoveHeader("x-missing"); err != nil {
		t.Fatalf("missing RemoveHeader should be idempotent: %v", err)
	}
	if got := document.Headers(); len(got) != 0 {
		t.Fatalf("Headers snapshot = %#v, want empty", got)
	}
}

func TestDocument_TypedJSONContainers(t *testing.T) {
	document := mustDocument(t, DocumentConfig{Body: map[string]interface{}{
		"map":     map[string]int{"one": 1},
		"slice":   []int{1, 2, 3},
		"array":   [2]int{1, 2},
		"nested":  [1]map[string]interface{}{{"old": true}},
		"nil-map": map[string]interface{}(nil),
	}})

	for path, value := range map[string]interface{}{
		"/map/one":         9,
		"/map/two":         2,
		"/slice/1":         8,
		"/array/0":         7,
		"/nested/0/added":  "value",
		"/nil-map/created": true,
	} {
		if err := document.Set(path, value); err != nil {
			t.Fatalf("Set(%q): %v", path, err)
		}
	}
	if got := document.Body()["map"].(map[string]int); !reflect.DeepEqual(got, map[string]int{"one": 9, "two": 2}) {
		t.Fatalf("typed map = %#v", got)
	}
	if got := document.Body()["slice"].([]int); !reflect.DeepEqual(got, []int{1, 8, 3}) {
		t.Fatalf("typed slice = %#v", got)
	}
	if got := document.Body()["array"].([2]int); got != [2]int{7, 2} {
		t.Fatalf("typed array = %#v", got)
	}
	if got, ok := document.Get("/nested/0/added"); !ok || got != "value" {
		t.Fatalf("nested typed array value = (%#v, %v)", got, ok)
	}
	if got, ok := document.Get("/nil-map/created"); !ok || got != true {
		t.Fatalf("nil map parent value = (%#v, %v)", got, ok)
	}

	if err := document.Remove("/map/one"); err != nil {
		t.Fatalf("remove typed map member: %v", err)
	}
	if _, exists := document.Get("/map/one"); exists {
		t.Fatal("typed map member was not removed")
	}
	if err := document.Remove("/nested/0/old"); err != nil {
		t.Fatalf("remove nested array member: %v", err)
	}
	if _, exists := document.Get("/nested/0/old"); exists {
		t.Fatal("nested array map member was not removed")
	}
	if err := document.Set("/map/wrong", "string"); err == nil {
		t.Fatal("incompatible typed map assignment unexpectedly accepted")
	}
	if err := document.Set("/map/null", nil); err == nil {
		t.Fatal("null into typed scalar map unexpectedly accepted")
	}
	if err := document.Set("/map/parent/child", 1); err == nil {
		t.Fatal("object parent inside scalar typed map unexpectedly created")
	}
}

func TestNewDocument_RejectsAmbiguousInitialHeaders(t *testing.T) {
	_, err := NewDocument(DocumentConfig{
		Body:    map[string]interface{}{},
		Headers: map[string]string{"X-Test": "one", "x-test": "two"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("NewDocument error = %v, want duplicate-casing rejection", err)
	}
}

func mustDocument(t *testing.T, config DocumentConfig) *Document {
	t.Helper()
	document, err := NewDocument(config)
	if err != nil {
		t.Fatalf("NewDocument returned error: %v", err)
	}
	return document
}
