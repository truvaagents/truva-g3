package providers

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

type cloneVisit struct {
	kind reflect.Kind
	ptr  uintptr
}

// CloneAIOptions returns a request-local copy of legacy AI options.
// JSON-shaped maps and slices are copied recursively; opaque leaf values are
// retained by reference for backward compatibility.
func CloneAIOptions(options *core.AIOptions) (*core.AIOptions, error) {
	if options == nil {
		return nil, nil
	}

	clone := *options
	clone.Headers = MergeStringMaps(nil, options.Headers)
	clone.Extra = cloneLegacyMap(options.Extra, make(map[cloneVisit]interface{}))
	return &clone, nil
}

func cloneLegacyMap(source map[string]interface{}, seen map[cloneVisit]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}

	visit := cloneVisit{kind: reflect.Map, ptr: reflect.ValueOf(source).Pointer()}
	if existing, ok := seen[visit]; ok {
		return existing.(map[string]interface{})
	}

	clone := make(map[string]interface{}, len(source))
	seen[visit] = clone
	for key, value := range source {
		clone[key] = cloneLegacyValue(value, seen)
	}
	return clone
}

func cloneLegacyValue(value interface{}, seen map[cloneVisit]interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneLegacyMap(typed, seen)
	case []interface{}:
		if typed == nil {
			return []interface{}(nil)
		}
		visit := cloneVisit{kind: reflect.Slice, ptr: reflect.ValueOf(typed).Pointer()}
		if existing, ok := seen[visit]; ok {
			return existing.([]interface{})
		}
		clone := make([]interface{}, len(typed))
		seen[visit] = clone
		for index, item := range typed {
			clone[index] = cloneLegacyValue(item, seen)
		}
		return clone
	case map[string]string:
		return MergeStringMaps(nil, typed)
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func MergeAnyMaps(base, override map[string]interface{}) map[string]interface{} {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	merged := make(map[string]interface{}, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

func MergeStringMaps(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}

	merged := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

func ApplyHeaders(req *http.Request, protected map[string]struct{}, defaultHeaders, requestHeaders map[string]string) {
	if req == nil {
		return
	}
	ApplyLegacyHeaders(req.Header, protected, defaultHeaders, requestHeaders)
}

// ApplyLegacyHeaders merges legacy header maps without replacing protected
// provider headers. Header names are matched case-insensitively.
func ApplyLegacyHeaders(headers http.Header, protected map[string]struct{}, defaultHeaders, requestHeaders map[string]string) {
	if headers == nil {
		return
	}
	merged := MergeStringMaps(defaultHeaders, requestHeaders)
	for k, v := range merged {
		if _, ok := protected[strings.ToLower(k)]; ok {
			continue
		}
		headers.Set(k, v)
	}
}
