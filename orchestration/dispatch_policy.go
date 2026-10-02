package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
)

// componentError preserves the original error object. Retry policy is parsed
// separately so an omitted/null/wrongly typed declaration is not false.
func componentError(body string) json.RawMessage {
	var envelope struct {
		Success *bool           `json:"success"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil || envelope.Success == nil || *envelope.Success {
		return nil
	}
	raw := bytes.TrimSpace(envelope.Error)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func componentRetryPolicy(body string) *bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(componentError(body), &fields) != nil {
		return nil
	}
	value := bytes.TrimSpace(fields["retryable"])
	if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
		return nil
	}
	allowed := bytes.Equal(value, []byte("true"))
	return &allowed
}

func transientHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status <= 599
}

func terminalHTTPStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusMethodNotAllowed
}

func nonTransientClientError(status int) bool {
	return status >= 400 && status < 500 && !transientHTTPStatus(status)
}

// boundedFailureProjection uses existing continuation budgets. Raw evidence stays
// on the step; any omitted projection content is explicitly disclosed.
func boundedFailureProjection(raw json.RawMessage, budget int, opts continuationDigestOpts) string {
	if len(raw) <= budget {
		return string(raw)
	}
	digest, _, _ := buildDecisionDigest(string(raw), opts.sampleN, opts.scalarMax, opts.maxKeys)
	const omitted = " [structured error projection truncated; full error retained on step]"
	if len(digest)+len(omitted) <= budget {
		return digest + omitted
	}
	if budget <= len(omitted) {
		return truncateString(omitted, max(0, budget))
	}
	return truncateString(digest, budget-len(omitted)) + omitted
}

// validateDispatchParameters checks only declared generic input invariants.
// It never guesses identifier formats or domain-specific missing values.
func validateDispatchParameters(params map[string]interface{}, capability *EnhancedCapability) error {
	if unresolved := findUnresolvedFrameworkMacros(params); len(unresolved) > 0 {
		return fmt.Errorf("unresolved framework references: %s", strings.Join(unresolved, ", "))
	}
	if capability == nil {
		return nil
	}
	for _, p := range capability.Parameters {
		value, exists := params[p.Name]
		if p.Required && !exists {
			return fmt.Errorf("required parameter %q is unresolved", p.Name)
		}
		if !exists || value == nil {
			continue
		} // An explicitly supplied null is application data.
		if err := validateContainerShape(p.Name, value, p.Type); err != nil {
			return err
		}
		if len(p.Enum) > 0 {
			found := false
			for _, allowed := range p.Enum {
				if fmt.Sprint(value) == allowed {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("parameter %q is outside its declared enum", p.Name)
			}
		}
	}
	return nil
}

// Only known container/scalar mismatches are rejected. Scalar coercion and
// unknown/custom schema types retain their established behavior.
func validateContainerShape(name string, value interface{}, expected string) error {
	if value == nil {
		return nil
	}
	kind := reflect.TypeOf(value).Kind()
	isObject := kind == reflect.Map
	isArray := kind == reflect.Slice || kind == reflect.Array
	mismatch := false
	switch strings.ToLower(expected) {
	case "string", "number", "integer", "boolean", "bool", "int", "int64", "int32", "float", "float64", "double":
		mismatch = isObject || isArray
	case "object":
		mismatch = isArray
	case "array":
		mismatch = isObject
	}
	if mismatch {
		return fmt.Errorf("parameter %q resolved to %s, incompatible with declared %s; reference the intended field or supply an explicitly encoded string", name, kind, expected)
	}
	return nil
}
