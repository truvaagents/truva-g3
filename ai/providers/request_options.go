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
// JSON-compatible maps, slices, arrays, and named scalar shapes are copied
// recursively. Opaque leaf values are retained by reference for backward
// compatibility and are never mutated by the framework.
func CloneAIOptions(options *core.AIOptions) (*core.AIOptions, error) {
	if options == nil {
		return nil, nil
	}

	clone := *options
	clone.Headers = cloneStringMap(options.Headers)
	if options.Extra != nil {
		clone.Extra = cloneLegacyReflect(
			reflect.ValueOf(options.Extra),
			make(map[cloneVisit]reflect.Value),
		).Interface().(map[string]interface{})
	}
	return &clone, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneLegacyReflect(value reflect.Value, seen map[cloneVisit]reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(cloneLegacyReflect(value.Elem(), seen))
		return clone
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{kind: reflect.Map, ptr: value.Pointer()}
		if existing, ok := seen[visit]; ok {
			return existing
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		seen[visit] = clone
		iterator := value.MapRange()
		for iterator.Next() {
			clone.SetMapIndex(iterator.Key(), cloneLegacyReflect(iterator.Value(), seen))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{kind: reflect.Slice, ptr: value.Pointer()}
		if existing, ok := seen[visit]; ok {
			return existing
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		seen[visit] = clone
		for index := range value.Len() {
			clone.Index(index).Set(cloneLegacyReflect(value.Index(index), seen))
		}
		return clone
	case reflect.Array:
		clone := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			clone.Index(index).Set(cloneLegacyReflect(value.Index(index), seen))
		}
		return clone
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
