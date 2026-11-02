package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func delegatedViewCatalog(types ...core.CapabilityType) *AgentCatalog {
	agents := make(map[string]*AgentInfo)
	for i, capabilityType := range types {
		agents[fmt.Sprintf("replica-%d", i)] = &AgentInfo{
			Registration: &core.ServiceInfo{Name: "analysis-agent", Metadata: map[string]interface{}{"namespace": "example"}},
			Capabilities: []EnhancedCapability{{Name: "analyze", Type: capabilityType}},
		}
	}
	return NewAgentCatalogWithFixtures(agents)
}

func delegatedViewStep(t *testing.T, extensions map[string]interface{}) *StepResult {
	t.Helper()
	object := map[string]interface{}{
		"request_id": "child-request", "routing_mode": "autonomous", "response": "Analysis completed.",
	}
	for key, value := range extensions {
		object[key] = value
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return &StepResult{
		StepID: "step-2", AgentName: "analysis-agent", Capability: "analyze", Namespace: "example",
		Instruction: "Analyze the provided material", Success: true, Response: string(encoded),
		Metadata: map[string]interface{}{"application_fact": "unchanged"},
	}
}

func delegatedViewOptions() continuationDigestOpts {
	return continuationDigestOpts{floorChars: 10000, sampleN: 3, scalarMax: 64, maxKeys: 32}
}

func TestRegisteredOrchestratorRequiresExactCatalogIdentity(t *testing.T) {
	step := delegatedViewStep(t, nil)
	for _, test := range []struct {
		name    string
		catalog *AgentCatalog
		change  func(*StepResult)
		want    bool
	}{
		{name: "registered", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), want: true},
		{name: "consistent replicas", catalog: delegatedViewCatalog(core.CapabilityOrchestrator, core.CapabilityOrchestrator), want: true},
		{name: "conflicting replicas", catalog: delegatedViewCatalog(core.CapabilityOrchestrator, core.CapabilityTool)},
		{name: "tool", catalog: delegatedViewCatalog(core.CapabilityTool)},
		{name: "unregistered", catalog: delegatedViewCatalog()},
		{name: "nil catalog"},
		{name: "missing agent", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), change: func(s *StepResult) { s.AgentName = "" }},
		{name: "missing capability", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), change: func(s *StepResult) { s.Capability = "" }},
		{name: "different agent", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), change: func(s *StepResult) { s.AgentName = "another-agent" }},
		{name: "different capability", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), change: func(s *StepResult) { s.Capability = "other" }},
		{name: "different namespace", catalog: delegatedViewCatalog(core.CapabilityOrchestrator), change: func(s *StepResult) { s.Namespace = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *step
			if test.change != nil {
				test.change(&copy)
			}
			if got := isRegisteredOrchestrator(test.catalog, &copy); got != test.want {
				t.Fatalf("catalog recognition = %t, want %t", got, test.want)
			}
		})
	}
	if isRegisteredOrchestrator(delegatedViewCatalog(core.CapabilityOrchestrator), nil) {
		t.Fatal("nil result accepted")
	}
}

func TestFrameworkDelegatedEnvelopeValidatesPresenceAndTypes(t *testing.T) {
	for _, test := range []struct {
		name, response string
		want           bool
	}{
		{"bare", `{"request_id":"child","response":"answer","routing_mode":"autonomous"}`, true},
		{"workflow", `{"request_id":"child","response":"answer","routing_mode":"workflow","steps":null,"clarification":null}`, true},
		{"clarification", `{"request_id":"child","response":"","routing_mode":"autonomous","clarification":{"question":"Which region?"},"steps":[]}`, true},
		{"missing id", `{"response":"answer","routing_mode":"autonomous"}`, false},
		{"empty id", `{"request_id":"","response":"answer","routing_mode":"autonomous"}`, false},
		{"numeric id", `{"request_id":7,"response":"answer","routing_mode":"autonomous"}`, false},
		{"missing response", `{"request_id":"child","routing_mode":"autonomous"}`, false},
		{"nonstring response", `{"request_id":"child","response":{},"routing_mode":"autonomous"}`, false},
		{"missing mode", `{"request_id":"child","response":"answer"}`, false},
		{"unknown mode", `{"request_id":"child","response":"answer","routing_mode":"other"}`, false},
		{"nonobject clarification", `{"request_id":"child","response":"answer","routing_mode":"autonomous","clarification":"question"}`, false},
		{"missing question", `{"request_id":"child","response":"answer","routing_mode":"autonomous","clarification":{}}`, false},
		{"invalid missing fields", `{"request_id":"child","response":"answer","routing_mode":"autonomous","clarification":{"question":"question","missing_fields":[7]}}`, false},
		{"invalid partial progress", `{"request_id":"child","response":"answer","routing_mode":"autonomous","clarification":{"question":"question","partial_progress":7}}`, false},
		{"nonarray steps", `{"request_id":"child","response":"answer","routing_mode":"autonomous","steps":{}}`, false},
		{"invalid child field", `{"request_id":"child","response":"answer","routing_mode":"autonomous","steps":[{"success":"yes"}]}`, false},
		{"invalid usage", `{"request_id":"child","response":"answer","routing_mode":"autonomous","usage":"tokens"}`, false},
		{"invalid phase usage", `{"request_id":"child","response":"answer","routing_mode":"autonomous","usage_by_phase":[]}`, false},
		{"custom receipt", `{"success":true,"data":{"proof":"ok"},"request_id":"child","response":"answer","routing_mode":"autonomous"}`, false},
		{"not object", `[]`, false},
		{"invalid", `{`, false},
		{"trailing content", `{"request_id":"child","response":"answer","routing_mode":"autonomous"}[]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, got := frameworkDelegatedEnvelope(test.response); got != test.want {
				t.Fatalf("envelope recognized = %t, want %t", got, test.want)
			}
		})
	}
}

func TestDelegatedViewExcludesOnlyFrameworkBookkeepingAndPreservesApplicationMetadata(t *testing.T) {
	step := delegatedViewStep(t, map[string]interface{}{
		"usage":          map[string]interface{}{"total_tokens": 11},
		"usage_by_phase": map[string]interface{}{"planning": map[string]interface{}{"total_tokens": 11}},
		"execution_time": 123,
		"metadata":       map[string]interface{}{"application_output": map[string]interface{}{"reference": "readable-proof"}},
		"steps": []interface{}{map[string]interface{}{
			"agent_name": "tool", "capability": "read", "success": true, "response": "ok",
			"metadata": map[string]interface{}{"result_trim": map[string]interface{}{"content_lost": true}, "application_output": "nested-proof"},
		}},
	})
	before := step.Response
	object, ok := frameworkDelegatedEnvelope(step.Response)
	if !ok {
		t.Fatal("fixture invalid")
	}
	view := renderDelegatedContinuationBody(object, delegatedViewOptions(), 10000)
	jsonBody := strings.SplitN(view.body, "\n  Observed child outcomes", 2)[0]
	var fields map[string]interface{}
	if err := json.Unmarshal([]byte(jsonBody), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"usage", "usage_by_phase", "execution_time"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("bookkeeping %s retained", key)
		}
	}
	if extractFieldValue(fields, "metadata.application_output.reference") != "readable-proof" || extractFieldValue(fields, "steps.0.metadata.application_output") != "nested-proof" {
		t.Fatalf("application metadata paths changed: %s", view.body)
	}
	if extractFieldValue(fields, "steps.0.metadata.result_trim") != nil || !view.meta.ContentLost || step.Response != before {
		t.Fatal("bookkeeping omission not disclosed, or raw response changed")
	}
	if extractFieldValue(object, "steps.0.metadata.result_trim") == nil {
		t.Fatal("raw child trim metadata removed")
	}
}

func TestDelegatedViewReadableScalarsDoNotFalselyReportLoss(t *testing.T) {
	text := strings.Repeat("Complete answer and clarification. ", 20)
	step := delegatedViewStep(t, map[string]interface{}{
		"response":      text,
		"clarification": map[string]interface{}{"question": text, "partial_progress": text},
	})
	view, err := prepareDelegatedContinuationView(t.Context(), step, delegatedViewCatalog(core.CapabilityOrchestrator), delegatedViewOptions(), 10000)
	if err != nil || view == nil {
		t.Fatalf("view = %+v, error %v", view, err)
	}
	if view.meta.ContentLost || strings.Count(view.body, text) != 3 {
		t.Fatalf("full readable values reported lost: %+v %s", view.meta, view.body)
	}
	for _, metadata := range []interface{}{
		&ResultTrimMetadata{ContentLost: true}, ResultTrimMetadata{ContentLost: true}, map[string]interface{}{"content_lost": true},
	} {
		step.Metadata["result_trim"] = metadata
		view, err = prepareDelegatedContinuationView(t.Context(), step, delegatedViewCatalog(core.CapabilityOrchestrator), delegatedViewOptions(), 10000)
		if err != nil || !view.meta.ContentLost || !reflect.DeepEqual(step.Metadata["result_trim"], metadata) {
			t.Fatalf("prior actual processor loss changed or disappeared: %v %+v", err, view)
		}
	}
	step.Metadata["result_trim"] = (*ResultTrimMetadata)(nil)
	if delegatedRecordedContentLost(step) {
		t.Fatal("nil metadata reported loss")
	}
}

func TestDelegatedCustomReceiptSampleBindsAllOriginalFilesAndApproval(t *testing.T) {
	files := make([]interface{}, 10)
	for i := range files {
		files[i] = map[string]interface{}{"path": fmt.Sprintf("part-%d.md", i), "content": "whole file"}
	}
	signature := "exact-approval-signature"
	receipt := map[string]interface{}{
		"success": true, "data": map[string]interface{}{
			"candidate": map[string]interface{}{"files": files}, "approval": map[string]interface{}{"signature": signature},
		},
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	step := delegatedViewStep(t, nil)
	step.Response = string(encoded)
	orch := newDigestTestOrchestrator(10000)
	orch.catalog = delegatedViewCatalog(core.CapabilityOrchestrator)
	result, err := orch.buildContinuationPrompt(t.Context(), "next", map[string]*StepResult{step.StepID: step}, []string{step.StepID}, "continue", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Prompt, "7 more of 10") || !strings.Contains(result.Prompt, `"candidate":{"files"`) {
		t.Fatalf("custom receipt nesting/sample lost: %s", result.Prompt)
	}
	dependencies := map[string]map[string]interface{}{"step-2": {"response": receipt}}
	executor := &SmartExecutor{}
	boundFiles := executor.interpolateValue("{{step-2.response.data.candidate.files}}", dependencies)
	boundSignature := executor.interpolateValue("{{step-2.response.data.approval.signature}}", dependencies)
	if !reflect.DeepEqual(boundFiles, files) || boundSignature != signature || step.Response != string(encoded) {
		t.Fatalf("original bindings changed: files=%#v signature=%v", boundFiles, boundSignature)
	}
}

func TestContinuationDelegatedReadableViewReportsActualLossOnly(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	ctx, span := provider.Tracer("delegated-view").Start(WithRequestID(t.Context(), "parent"), "continuation")
	orch := newDigestTestOrchestrator(10000)
	orch.catalog = delegatedViewCatalog(core.CapabilityOrchestrator)
	answer := strings.Repeat("Readable whole response. ", 30)
	step := delegatedViewStep(t, map[string]interface{}{"response": answer})
	result, err := orch.buildContinuationPrompt(ctx, "next", map[string]*StepResult{step.StepID: step}, []string{step.StepID}, "continue", 2)
	if err != nil || !strings.Contains(result.Prompt, answer) {
		t.Fatalf("readable response unavailable: %v", err)
	}
	span.End()
	seen := false
	for _, recorded := range recorder.Ended() {
		for _, event := range recorded.Events() {
			if event.Name != "result_trim.completed" {
				continue
			}
			seen = true
			for _, attr := range event.Attributes {
				if string(attr.Key) == "content_lost" && attr.Value.AsBool() {
					t.Fatal("unused generic digest reported false loss")
				}
			}
		}
	}
	if !seen {
		t.Fatal("missing derived view size observation")
	}
}

func TestDelegatedViewKeepsReadableClarificationAndRawStructuredBindings(t *testing.T) {
	files := make([]interface{}, 10)
	for i := range files {
		files[i] = map[string]interface{}{"path": fmt.Sprintf("part-%d.md", i), "content": strings.Repeat("source text ", 30)}
	}
	question := "请提供批准的内容 — Quelle version faut-il publier? " + strings.Repeat("detail ", 30)
	answer := "Analysis requires approval.\n```go\nif a < b { return `x*y` }\n```\n" + strings.Repeat("explanation ", 30)
	step := delegatedViewStep(t, map[string]interface{}{
		"response": answer, "clarification": map[string]interface{}{"question": question, "missing_fields": []interface{}{"approval"}},
		"candidate": map[string]interface{}{"files": files}, "approval": map[string]interface{}{"signature": "exact-signature"},
	})
	before, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	view, err := prepareDelegatedContinuationView(t.Context(), step, delegatedViewCatalog(core.CapabilityOrchestrator), delegatedViewOptions(), 10000)
	if err != nil || view == nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	var shown map[string]interface{}
	if err := json.Unmarshal([]byte(view.body), &shown); err != nil {
		t.Fatal(err)
	}
	if shown["response"] != answer || shown["clarification"].(map[string]interface{})["question"] != question {
		t.Fatalf("answer/question elided: %s", view.body)
	}
	if len(extractFieldValue(shown, "candidate.files").([]interface{})) != 4 || extractFieldValue(shown, "approval.signature") != "exact-signature" {
		t.Fatalf("sampled paths unavailable: %s", view.body)
	}
	raw, err := unmarshalPreservingNumbers([]byte(step.Response))
	if err != nil {
		t.Fatal(err)
	}
	params := (&SmartExecutor{}).interpolateValue("{{step-2.candidate.files}}", map[string]map[string]interface{}{"step-2": {"response": raw}})
	if !reflect.DeepEqual(params, files) {
		t.Fatalf("bound sampled rather than full value: %#v", params)
	}
	after, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !view.meta.ContentLost || !utf8.ValidString(view.body) {
		t.Fatalf("raw result changed or loss not disclosed: %+v", view.meta)
	}
}

func TestDelegatedChildFactsPreserveObservedStatesAndOmissions(t *testing.T) {
	step := delegatedViewStep(t, map[string]interface{}{"steps": []interface{}{
		map[string]interface{}{"agent_name": "tool", "capability": "inspect", "success": true, "response": "read-only evidence"},
		map[string]interface{}{"agent_name": "tool", "capability": "write", "success": false, "response": "partial", "error": "upstream disconnected"},
		map[string]interface{}{"agent_name": "tool", "capability": "publish", "success": false, "skipped": true, "skip_reason": "dependency failed"},
	}})
	object, ok := frameworkDelegatedEnvelope(step.Response)
	if !ok {
		t.Fatal("fixture invalid")
	}
	facts, shown, omitted, _ := delegatedChildFacts(object, 2000)
	for _, want := range []string{"inspect [SUCCESS]", "write [FAILED]: upstream disconnected", "publish [SKIPPED]: dependency failed"} {
		if !strings.Contains(facts, want) {
			t.Fatalf("missing %q: %s", want, facts)
		}
	}
	if shown != 3 || omitted != 0 || strings.Contains(facts, "Do NOT") {
		t.Fatalf("facts = %s, shown %d omitted %d", facts, shown, omitted)
	}
	for _, limit := range []int{0, 50, 100, 180} {
		facts, shown, omitted, caps := delegatedChildFacts(object, limit)
		if len(facts) > limit || shown+omitted != 3 || len(caps) != shown || !utf8.ValidString(facts) {
			t.Fatalf("limit %d: %d bytes, shown %d omitted %d", limit, len(facts), shown, omitted)
		}
		if shown > 0 && omitted > 0 && !strings.Contains(facts, "outcomes omitted") {
			t.Fatal("partial child facts lacked disclosure")
		}
	}
}

func TestDelegatedViewBoundsMultilingualTextAndLargeSkeleton(t *testing.T) {
	extensions := map[string]interface{}{"response": strings.Repeat("你好\n```go\nx := `a*b`\n``` ", 500)}
	for i := 0; i < 80; i++ {
		extensions[fmt.Sprintf("extension-%03d", i)] = map[string]interface{}{"nested": strings.Repeat("é", 100)}
	}
	step := delegatedViewStep(t, extensions)
	object, _ := frameworkDelegatedEnvelope(step.Response)
	for _, limit := range []int{1, 40, 200, 512, 2048, 10000} {
		view := renderDelegatedContinuationBody(object, delegatedViewOptions(), limit)
		if !utf8.ValidString(view.body) || !view.meta.ContentLost {
			t.Fatalf("limit %d: invalid view/meta %+v", limit, view.meta)
		}
		if len(view.body) > max(limit, len("[delegated output omitted for budget; original fields remain referenceable]")) {
			t.Fatalf("unbounded view at limit %d: %d bytes", limit, len(view.body))
		}
	}
}

func TestDelegatedChildPreviewBytePolicy(t *testing.T) {
	for _, tc := range []struct{ name, message, want string }{
		{"exact bytes", strings.Repeat("é", 100), strings.Repeat("é", 100)},
		{"split codepoint", strings.Repeat("中", 67), strings.Repeat("中", 66) + "…"},
		{"first line", "summary\nprivate second line", "summary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := map[string]interface{}{"steps": []interface{}{
				map[string]interface{}{"agent_name": "a", "capability": "c", "success": true, "response": tc.message},
			}}
			facts, shown, omitted, _ := delegatedChildFacts(object, 1000)
			if facts != "    - a/c [SUCCESS]: "+tc.want+"\n" || shown != 1 || omitted != 0 || !utf8.ValidString(facts) {
				t.Fatalf("unexpected preview: %q shown=%d omitted=%d", facts, shown, omitted)
			}
		})
	}
}

func TestDelegatedChildAllocationLeavesRoomForAnswer(t *testing.T) {
	children := make([]interface{}, 20)
	for i := range children {
		children[i] = map[string]interface{}{"agent_name": "a", "capability": fmt.Sprintf("c%d", i), "success": true, "response": "observed"}
	}
	step := delegatedViewStep(t, map[string]interface{}{"steps": children})
	object, ok := frameworkDelegatedEnvelope(step.Response)
	if !ok {
		t.Fatal("invalid fixture")
	}
	view := renderDelegatedContinuationBody(object, continuationDigestOpts{sampleN: 1, scalarMax: 200, maxKeys: 50}, 1000)
	// At this allocation the body fits without reducing the initial child share.
	// Pin one quarter (250 bytes), including the whole-line omission disclosure.
	facts, shown, omitted, _ := delegatedChildFacts(object, 250)
	if shown == 0 || omitted == 0 || view.childrenShown != shown || view.childrenOmitted != omitted || !strings.HasSuffix(view.body, facts) {
		t.Fatalf("child share changed: shown=%d omitted=%d body=%s", view.childrenShown, view.childrenOmitted, view.body)
	}
	if len(view.body) > 1000 || !strings.Contains(view.body, "Analysis completed.") || !view.meta.ContentLost {
		t.Fatalf("answer or budget/omission accounting changed: %+v", view)
	}
}

type delegatedViewPreparer struct {
	responses []string
	expand    int
	err       error
}

func (p *delegatedViewPreparer) PreparePromptValue(_ context.Context, _ promptKind, _ promptDynamicValueKind, field, value string) (string, error) {
	if field != promptFieldPriorResultResponse {
		return value, nil
	}
	p.responses = append(p.responses, value)
	if p.err != nil {
		return "", p.err
	}
	return "<encoded>" + strings.Repeat("encoded-padding", p.expand) + value + "</encoded>", nil
}

func TestContinuationDelegatedViewUsesFullCatalogAndEncodesOnce(t *testing.T) {
	orch := newDigestTestOrchestrator(10000)
	orch.catalog = delegatedViewCatalog(core.CapabilityOrchestrator)
	question := strings.Repeat("Which approved version? ", 10)
	step := delegatedViewStep(t, map[string]interface{}{"clarification": map[string]interface{}{"question": question}})
	preparer := &delegatedViewPreparer{}
	result, err := orch.buildContinuationPrompt(withPromptInputPreparer(t.Context(), preparer), "next", map[string]*StepResult{step.StepID: step}, []string{step.StepID}, "continue", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(preparer.responses) != 1 || strings.Count(result.Prompt, "<encoded>") != 1 || !strings.Contains(result.Prompt, question) {
		t.Fatalf("not full-catalog/once encoded: responses=%d\n%s", len(preparer.responses), result.Prompt)
	}
	if _, exists := step.Metadata["capability_type"]; exists {
		t.Fatal("prompt view changed memory recording semantics")
	}
	preparer.err = errors.New("encoding rejected")
	if _, err := orch.buildContinuationPrompt(withPromptInputPreparer(t.Context(), preparer), "next", map[string]*StepResult{step.StepID: step}, []string{step.StepID}, "continue", 2); !errors.Is(err, preparer.err) {
		t.Fatalf("encoding failure not propagated: %v", err)
	}
}

func TestContinuationCustomReceiptAndOrdinaryToolPromptsAreUnchanged(t *testing.T) {
	for _, response := range []string{
		`{"success":true,"data":{"candidate":{"files":[{"path":"part.md","content":"source"}]},"approval":{"signature":"proof"}}}`,
		`{"request_id":"child","routing_mode":"autonomous","response":"answer","steps":[{"agent_name":"tool","capability":"read","success":true}],"success":true,"data":{"approval":"proof"}}`,
	} {
		step := delegatedViewStep(t, nil)
		step.Response = response
		generic := newDigestTestOrchestrator(10000)
		registered := newDigestTestOrchestrator(10000)
		registered.catalog = delegatedViewCatalog(core.CapabilityOrchestrator)
		var prompts []string
		for _, orch := range []*AIOrchestrator{generic, registered} {
			result, err := orch.buildContinuationPrompt(t.Context(), "next", map[string]*StepResult{step.StepID: step}, []string{step.StepID}, "continue", 2)
			if err != nil {
				t.Fatal(err)
			}
			prompts = append(prompts, result.Prompt)
		}
		if prompts[0] != prompts[1] || strings.Contains(prompts[1], "Observed child outcomes") {
			t.Fatalf("custom receipt rendering changed: %s", prompts[1])
		}
	}
	step := delegatedViewStep(t, nil)
	step.Response = `{"response":"tool data","steps":[{"agent_name":"tool","success":true}]}`
	view, err := prepareDelegatedContinuationView(t.Context(), step, delegatedViewCatalog(core.CapabilityTool), delegatedViewOptions(), 10000)
	if err != nil || view != nil {
		t.Fatalf("ordinary tool promoted to delegation: %+v %v", view, err)
	}
}

func TestContinuationDelegatedBudgetPreservesFailuresNewestAndReportsEncodedOverflow(t *testing.T) {
	conditions := []struct {
		name   string
		expand int
	}{
		{"normal encoding", 0},
		{"custom expanded encoding", 100},
	}
	for _, condition := range conditions {
		t.Run(condition.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
			ctx, span := provider.Tracer("delegated-view").Start(WithRequestID(t.Context(), "parent"), "continuation")
			preparer := &delegatedViewPreparer{expand: condition.expand}
			ctx = withPromptInputPreparer(ctx, preparer)
			orch := newDigestTestOrchestrator(450)
			orch.config.ContinuationResultMaxChars = 250
			orch.catalog = delegatedViewCatalog(core.CapabilityOrchestrator)
			step := delegatedViewStep(t, map[string]interface{}{"response": strings.Repeat("answer ", 80)})
			step.StepID = "step-3"
			results := map[string]*StepResult{
				"step-1": {StepID: "step-1", AgentName: "tool", Success: true, Response: `{"value":"older evidence"}`},
				"step-2": {StepID: "step-2", AgentName: "tool", Success: false, Error: "preserved failure"},
				"step-3": step,
			}
			result, err := orch.buildContinuationPrompt(ctx, "next", results, []string{"step-1", "step-2", "step-3"}, "continue", 2)
			if err != nil {
				t.Fatal(err)
			}
			span.End()
			if !strings.Contains(result.Prompt, "Step step-3 (") || !strings.Contains(result.Prompt, "preserved failure") || !strings.Contains(result.Prompt, "[FAILED:") {
				t.Fatalf("protected result lost: %s", result.Prompt)
			}
			if condition.expand > 0 && (strings.Contains(result.Prompt, "Step step-1 (") || !strings.Contains(result.Prompt, "</encoded>")) {
				t.Fatal("encoded cost not used for eviction or encoded body cut")
			}
			budgetSeen, trimSeen := false, false
			for _, recorded := range recorder.Ended() {
				for _, event := range recorded.Events() {
					if event.Name == "result_trim.completed" {
						trimSeen = true
					}
					if event.Name != "orchestrator.continuation_budget" {
						continue
					}
					budgetSeen = true
					if len(event.Attributes) == 0 || event.Attributes[0].Value.AsString() != "parent" {
						t.Fatal("budget event lost request correlation")
					}
					if condition.expand > 0 {
						overflow := false
						for _, attr := range event.Attributes {
							if string(attr.Key) == "protected_budget_overflow" {
								overflow = attr.Value.AsBool()
							}
						}
						if !overflow {
							t.Fatal("protected encoded overflow not disclosed")
						}
					}
				}
			}
			if !budgetSeen || !trimSeen {
				t.Fatalf("missing view observability: budget=%t trim=%t", budgetSeen, trimSeen)
			}
		})
	}
}

func TestPrepareDelegatedViewFailureAndMissingIdentityUseGenericPath(t *testing.T) {
	if view, err := prepareDelegatedContinuationView(t.Context(), nil, delegatedViewCatalog(core.CapabilityOrchestrator), delegatedViewOptions(), 10000); view != nil || err != nil {
		t.Fatalf("nil result changed: %+v %v", view, err)
	}
	step := delegatedViewStep(t, nil)
	step.Success = false
	if view, err := prepareDelegatedContinuationView(t.Context(), step, delegatedViewCatalog(core.CapabilityOrchestrator), delegatedViewOptions(), 10000); view != nil || err != nil {
		t.Fatalf("failed result changed: %+v %v", view, err)
	}
	step.Success = true
	for _, catalog := range []*AgentCatalog{nil, delegatedViewCatalog()} {
		if view, err := prepareDelegatedContinuationView(t.Context(), step, catalog, delegatedViewOptions(), 10000); view != nil || err != nil {
			t.Fatalf("missing identity changed: %+v %v", view, err)
		}
	}
}
