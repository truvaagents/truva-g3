package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

func literalPayloadPlan(t testing.TB, payload string) string {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"plan_id": "literal-plan", "terminal": true,
		"steps": []map[string]interface{}{{
			"step_id": "step-1", "agent_name": "test-agent", "instruction": "Transmit the supplied literal",
			"metadata": map[string]interface{}{"capability": "test_capability", "parameters": map[string]interface{}{"payload": payload}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertLiteralPlan(t testing.TB, input, payload string) {
	t.Helper()
	o := &AIOrchestrator{}
	plan, err := o.parsePlan(input)
	if err != nil || len(plan.Steps) != 1 {
		t.Fatalf("parse failed: %v", err)
	}
	got := plan.Steps[0].Metadata["parameters"].(map[string]interface{})["payload"]
	if got != payload {
		t.Fatalf("literal changed: got %q, want %q", got, payload)
	}
}

func payloadPlanVariants(raw string) []string {
	return []string{
		raw, "```json\n" + raw + "\n```", "```JSON\n" + raw + "\n```",
		"```\n" + raw + "\n```", "Here is the plan:\n" + raw + "\nEnd of plan.",
		"Here is the plan:\n```json\n" + raw + "\n```\nEnd of plan.",
		"An object uses {braces}; here is the plan:\n```json\n" + raw + "\n```",
		// Invalid structure elsewhere must not license rewriting a valid literal.
		raw[:len(raw)-1] + ",}",
		raw[:len(raw)-1] + `,"recovery_fixture":{{step-2.response.value}}}`,
	}
}

func TestPlanPayloadStrings(t *testing.T) {
	for _, payload := range []string{
		"", "**bold** and *italic*", "func f(p *int) { *p = *p * 2 }",
		"[1, 2, ] and {\"x\": 1, }", "{{city}}, {{country}}",
		"```json\n{\"inside\":\"value\"}\n```", "雪🙂\n\t\x00\"\\",
	} {
		for i, input := range payloadPlanVariants(literalPayloadPlan(t, payload)) {
			t.Run(string(rune('A'+i))+payload, func(t *testing.T) { assertLiteralPlan(t, input, payload) })
		}
	}
}

func TestPlanValidJSONBypassesSalvage(t *testing.T) {
	for _, raw := range []string{
		` [ {"steps":[]} ] `,
		`{"steps":"wrong type", "payload":"[1, 2, ]"}`,
		`"a literal containing {\"steps\":[]}"`,
	} {
		if cleaned := cleanLLMResponse(raw); cleaned != raw {
			t.Fatalf("valid JSON rewritten: %q => %q", raw, cleaned)
		}
		var expected RoutingPlan
		decodeErr := json.Unmarshal([]byte(raw), &expected)
		_, err := (&AIOrchestrator{}).parsePlan(raw)
		var typed *json.UnmarshalTypeError
		if !errors.As(err, &typed) || !strings.Contains(err.Error(), decodeErr.Error()) {
			t.Fatalf("original decoding error lost: got %v, want %v", err, decodeErr)
		}
	}
}

func TestPlanRepairPreservesStringTokens(t *testing.T) {
	input := `{"payload":"[1, 2, ] {{city}}, {{country}} \\\"", "value":{{step-1.response.id}}}`
	want := `{"payload":"[1, 2, ] {{city}}, {{country}} \\\"", "value":"{{step-1.response.id}}"}`
	if got := quoteUnquotedTemplates(input); got != want {
		t.Fatalf("template repair changed literal: %q", got)
	}
	input = `{"payload":"[1, 2, ] {{city}}, {{country}}",}`
	want = `{"payload":"[1, 2, ] {{city}}, {{country}}"}`
	if got, changed := attemptJSONRepair(input); !changed || got != want {
		t.Fatalf("comma repair changed literal: %q", got)
	}
}

func TestPlanRepairRejectsAmbiguousStrings(t *testing.T) {
	for _, raw := range []string{`{"payload":"unterminated, }`, `{"payload":"escaped closing quote\"}`} {
		if got := quoteUnquotedTemplates(raw); got != raw {
			t.Fatalf("unterminated string rewritten: %q", got)
		}
		if got, changed := attemptJSONRepair(raw); changed || got != raw {
			t.Fatalf("unterminated string repaired: %q", got)
		}
		if _, err := (&AIOrchestrator{}).parsePlan(raw); err == nil {
			t.Fatal("unterminated string accepted")
		}
	}
	for _, raw := range []string{`{"value":{{unterminated}`, `{"value":{{}}}`, `{"value":{{a{b}}}`, `{"value":{{a"b}}}`} {
		if got := quoteUnquotedTemplates(raw); got != raw {
			t.Fatalf("ambiguous template repaired: %q", got)
		}
	}
}

func TestPlanParseRetryAllowsLiteralCode(t *testing.T) {
	prior := &PlanningPromptResult{Prompt: "selected catalog", SystemPrompt: "system", AllowedAgents: map[string]bool{"test-agent": true}}
	got, err := (&AIOrchestrator{}).buildPlanningPromptWithParseError(context.Background(), prior, errors.New("invalid JSON"))
	if err != nil || !strings.Contains(got.Prompt, "code and Markdown") || strings.Contains(got.Prompt, "plain text only") ||
		!strings.HasSuffix(got.Prompt, prior.Prompt) || got.SystemPrompt != prior.SystemPrompt || !got.AllowedAgents["test-agent"] {
		t.Fatalf("retry contract not preserved: %+v, %v", got, err)
	}
}

func TestPlanPayloadSurvivesPlanningAndDispatch(t *testing.T) {
	payload := "func f(p *int) { *p = *p * 2 }\n**literal**\n```json\n{}\n```"
	ai := &promptCapturingAIClient{responses: []string{literalPayloadPlan(t, payload), "Transmitted."}}
	o := setupTestOrchestrator(t, ai)
	registration := &core.ServiceRegistration{ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080, Type: core.ComponentTypeTool}
	if err := o.discovery.Register(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	o.catalog.agents = map[string]*AgentInfo{"test-agent": {
		Registration: registration, Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/capture", Parameters: []Parameter{{Name: "payload", Type: "string", Required: true}}}},
	}}
	var body []byte
	transport := &paramCapturingRoundTripper{capturedBody: &body}
	o.executor.httpClient = &http.Client{Transport: transport}
	response, err := o.ProcessRequest(context.Background(), "Transmit the supplied literal", nil)
	if err != nil || response == nil || transport.callCount != 1 {
		t.Fatalf("normal planning failed: %v; calls=%d", err, transport.callCount)
	}
	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil || params["payload"] != payload {
		t.Fatalf("HTTP payload changed: %s, %v", body, err)
	}
	if len(response.Steps) != 1 || len(response.Steps[0].Dispatches) != 1 || response.Steps[0].Dispatches[0].Parameters["payload"] != payload {
		t.Fatalf("dispatch evidence differs: %+v", response.Steps)
	}
}

func FuzzPlanPayloadStrings(f *testing.F) {
	for _, seed := range []string{"", "**bold** *p", "[1, 2, ]", "{{a}}, {{b}}", "```json\n{}\n```", "雪🙂\x00\"\\"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		if !utf8.ValidString(payload) || len(payload) > 65536 {
			t.Skip()
		}
		for _, input := range payloadPlanVariants(literalPayloadPlan(t, payload)) {
			assertLiteralPlan(t, input, payload)
		}
	})
}

func BenchmarkPlanPayloadParsing(b *testing.B) {
	for _, n := range []int{32, 4096} {
		raw := literalPayloadPlan(b, strings.Repeat("**literal** 雪 \\\" ", n))
		for index, input := range []string{raw, "```json\n" + raw + "\n```"} {
			b.Run(fmt.Sprintf("repetitions=%d/wrapped=%t", n, index == 1), func(b *testing.B) {
				b.ReportAllocs()
				o := &AIOrchestrator{}
				for b.Loop() {
					if _, err := o.parsePlan(input); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
