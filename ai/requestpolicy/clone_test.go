package requestpolicy

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestCloneJSONValue_Conformance(t *testing.T) {
	conformance.RunJSONValueCloneConformance(t, CloneJSONValue)
}

func TestCloneJSONValue_RejectsUnsupportedValues(t *testing.T) {
	cycle := map[string]interface{}{}
	cycle["self"] = cycle
	value := 1
	tests := []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "pointer", value: &value, want: "unsupported"},
		{name: "struct", value: time.Time{}, want: "unsupported"},
		{name: "map key", value: map[int]string{1: "one"}, want: "map key"},
		{name: "NaN", value: math.NaN(), want: "non-finite"},
		{name: "positive infinity", value: math.Inf(1), want: "non-finite"},
		{name: "cycle", value: cycle, want: "cyclic map"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CloneJSONValue(map[string]interface{}{"nested": test.value})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CloneJSONValue error = %v, want substring %q", err, test.want)
			}
			if !strings.Contains(err.Error(), "$/nested") {
				t.Fatalf("error is not path-qualified: %v", err)
			}
		})
	}
}
