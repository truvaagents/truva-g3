package orchestration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestCompactSchemaAtom(t *testing.T) {
	for _, value := range []string{"outcome_id", "object", "", "a,b", "a:b", "two words", "quote\"", "a\\b", "line\nnext", "tab\t", "\u0001", "\u007f", "\u2028", "\u2029"} {
		t.Run(value, func(t *testing.T) {
			got := compactSchemaAtom(value)
			if value == "outcome_id" || value == "object" {
				if got != value {
					t.Fatalf("ordinary atom changed: %q", got)
				}
				return
			}
			var decoded string
			if err := json.Unmarshal([]byte(got), &decoded); err != nil || decoded != value {
				t.Fatalf("ambiguous atom did not round trip: %q -> %q (%v)", value, got, err)
			}
			if strings.ContainsAny(got, "\n\r\t") {
				t.Fatalf("raw whitespace in atom: %q", got)
			}
		})
	}
}

func TestCatalogReturnFieldsWithoutTopLevelType(t *testing.T) {
	catalog := NewAgentCatalog(NewMockDiscovery())
	catalog.agents = map[string]*AgentInfo{"tool": {
		Registration: &core.ServiceRegistration{ID: "tool", Name: "test-tool"},
		Capabilities: catalog.convertBasicCapabilities([]core.Capability{
			{Name: "read", OutputSummary: &core.SchemaSummary{
				RequiredFields: []core.FieldHint{{Name: "reference", Type: "object", Description: "Authoritative reference"}},
				OptionalFields: []core.FieldHint{{Name: "count", Type: "number"}},
			}},
			{Name: "hidden", Internal: true, OutputSummary: &core.SchemaSummary{
				RequiredFields: []core.FieldHint{{Name: "secret", Type: "string"}},
			}},
		}),
	}}
	for name, output := range map[string]string{
		"full":     catalog.FormatForLLM(),
		"selected": catalog.FormatToolsForLLM([]string{"test-tool/read", "test-tool/hidden"}),
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(output, "Return Fields: reference:object, count:number") {
				t.Fatalf("missing compact schema: %s", output)
			}
			if strings.Contains(output, "secret") || strings.Contains(output, "Authoritative reference") {
				t.Fatalf("hidden fields or verbose descriptions rendered: %s", output)
			}
			if strings.Contains(output, "Returns:") {
				t.Fatalf("empty top-level return type rendered: %s", output)
			}
		})
	}
	if got := catalog.agents["tool"].Capabilities[0].Returns.Fields[0].Description; got != "Authoritative reference" {
		t.Fatalf("catalog metadata mutated: %q", got)
	}
	if got := catalog.FormatToolsForLLM([]string{"test-tool/other"}); strings.Contains(got, "Return Fields:") {
		t.Fatalf("unselected schema leaked: %s", got)
	}
}
