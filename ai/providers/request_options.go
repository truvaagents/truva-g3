package providers

import (
	"net/http"
	"strings"
)

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

	merged := MergeStringMaps(defaultHeaders, requestHeaders)
	for k, v := range merged {
		if _, ok := protected[strings.ToLower(k)]; ok {
			continue
		}
		req.Header.Set(k, v)
	}
}
