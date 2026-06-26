package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// --- B: buildDecisionDigest / digestValue ---------------------------------------------------------

func TestBuildDecisionDigest_KeepsAllKeysValidJSON(t *testing.T) {
	digest, degenerate := buildDecisionDigest(`{"a":1,"b":{"c":"short"},"d":true}`, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if degenerate {
		t.Fatal("valid JSON must not be degenerate")
	}
	if !json.Valid([]byte(digest)) {
		t.Fatalf("digest must be valid JSON, got %q", digest)
	}
	for _, k := range []string{`"a"`, `"b"`, `"c"`, `"d"`} {
		if !strings.Contains(digest, k) {
			t.Errorf("digest must keep key %s (structure-complete); got %q", k, digest)
		}
	}
}

func TestBuildDecisionDigest_ArraySampleAndSentinel(t *testing.T) {
	digest, _ := buildDecisionDigest(`{"arr":[1,2,3,4,5,6,7,8,9,10]}`, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if !json.Valid([]byte(digest)) {
		t.Fatalf("digest must stay valid JSON, got %q", digest)
	}
	if !strings.Contains(digest, "more of 10") {
		t.Errorf("array must carry a length sentinel; got %q", digest)
	}
	// Stays an array (sample head retained) so the planner's index model holds.
	if !strings.Contains(digest, "[1,2,3,") {
		t.Errorf("array must keep a %d-item head sample; got %q", defaultDigestSampleN, digest)
	}
}

func TestBuildDecisionDigest_ElidesLongScalar(t *testing.T) {
	in := fmt.Sprintf(`{"big":%q,"small":"keep"}`, strings.Repeat("x", 300))
	digest, degenerate := buildDecisionDigest(in, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if degenerate {
		t.Fatal("valid JSON must not be degenerate even when a value is elided")
	}
	if !strings.Contains(digest, "300 chars") {
		t.Errorf("long scalar must elide to a sentinel; got %q", digest)
	}
	if !strings.Contains(digest, "keep") {
		t.Errorf("short scalar must be kept inline; got %q", digest)
	}
	if !strings.Contains(digest, `"big"`) {
		t.Errorf("elided field's key must remain (structure-complete); got %q", digest)
	}
}

func TestBuildDecisionDigest_WideObjectKeySampling(t *testing.T) {
	// A map-shaped object (many dynamic-ID keys) must be key-sampled (not unbounded): keep
	// defaultDigestMaxKeys keys + a sentinel.
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"instance-%d":{"cpu":%d}`, i, i)
	}
	b.WriteString("}")
	digest, degenerate := buildDecisionDigest(b.String(), defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if degenerate {
		t.Fatal("valid JSON must not be degenerate")
	}
	if !json.Valid([]byte(digest)) {
		t.Fatalf("digest must stay valid JSON; got %q", digest)
	}
	if !strings.Contains(digest, "more of 200 keys") {
		t.Errorf("wide object must carry a key-count sentinel; got %q", digest)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(digest), &m); err != nil {
		t.Fatalf("digest must parse: %v", err)
	}
	if len(m) != defaultDigestMaxKeys+1 { // maxKeys sampled keys + the sentinel key
		t.Errorf("wide object must keep %d keys + 1 sentinel; got %d", defaultDigestMaxKeys, len(m))
	}
}

func TestBuildDecisionDigest_SchemaObjectKeptWhole(t *testing.T) {
	// A normal schema object (<= maxKeys fields) keeps every key — no sampling.
	digest, _ := buildDecisionDigest(`{"a":1,"b":2,"c":3,"d":4}`, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if strings.Contains(digest, "__truncated_keys__") {
		t.Errorf("a small schema object must not be key-sampled; got %q", digest)
	}
	for _, k := range []string{`"a"`, `"b"`, `"c"`, `"d"`} {
		if !strings.Contains(digest, k) {
			t.Errorf("schema object must keep key %s; got %q", k, digest)
		}
	}
}

func TestBuildDecisionDigest_PreservesLargeIntegers(t *testing.T) {
	// 19-digit snowflake-style IDs exceed float64's exact-integer range (2^53); a float64 round-trip
	// would mangle them to scientific notation. UseNumber must keep them verbatim.
	digest, degenerate := buildDecisionDigest(`{"id":1782297941780804184,"ts":1700000000000000000}`, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if degenerate {
		t.Fatal("valid JSON must not be degenerate")
	}
	for _, n := range []string{"1782297941780804184", "1700000000000000000"} {
		if !strings.Contains(digest, n) {
			t.Errorf("large integer %s must survive verbatim (no float64 mangling); got %q", n, digest)
		}
	}
}

func TestBuildDecisionDigest_TrailingGarbageIsDegenerate(t *testing.T) {
	// Valid JSON followed by trailing junk is not clean JSON — treat as a blob (→ C), matching the
	// prior json.Unmarshal strictness.
	if _, degenerate := buildDecisionDigest(`{"a":1} then some log text`, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys); !degenerate {
		t.Error("JSON with trailing garbage must be degenerate")
	}
}

func TestBuildDecisionDigest_NonJSONDegenerate(t *testing.T) {
	digest, degenerate := buildDecisionDigest("this is a plain-text log line, not JSON", defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if !degenerate {
		t.Error("a non-JSON blob must be degenerate (escalates to C)")
	}
	if digest != "" {
		t.Errorf("non-JSON digest body must be empty (caller substitutes the floor); got %q", digest)
	}
}

func TestBuildDecisionDigest_ValidJSONNeverDegenerate(t *testing.T) {
	// Narrative-dominated but still valid JSON: per the owner decision (C = non-JSON only) this must
	// NOT be degenerate — the skeleton keeps the key and the value resolves via template at execution.
	in := fmt.Sprintf(`{"text":%q}`, strings.Repeat("y", 50000))
	digest, degenerate := buildDecisionDigest(in, defaultDigestSampleN, defaultDigestScalarMax, defaultDigestMaxKeys)
	if degenerate {
		t.Error("narrative-dominated valid JSON must NOT be degenerate (C is non-JSON only)")
	}
	if !strings.Contains(digest, "50000 chars") {
		t.Errorf("the long value must be elided to a sentinel; got %q", digest)
	}
}

// --- C gate ---------------------------------------------------------------------------------------

func TestIsStructurallyDegenerate(t *testing.T) {
	cases := []struct {
		meta *ResultTrimMetadata
		want bool
	}{
		{nil, false},
		{&ResultTrimMetadata{Method: "digest"}, false},
		{&ResultTrimMetadata{Method: "structural"}, false},
		{&ResultTrimMetadata{Method: "truncate"}, true},
	}
	for _, c := range cases {
		if got := isStructurallyDegenerate(c.meta); got != c.want {
			t.Errorf("isStructurallyDegenerate(%v) = %v, want %v", c.meta, got, c.want)
		}
	}
}

func TestRenderContinuationDigests_MixedJSONAndBlob(t *testing.T) {
	steps := []StepResult{
		{StepID: "step-1", Response: `{"k":"v"}`, Success: true},
		{StepID: "step-2", Response: "plain text blob", Success: true},
	}
	bodies, meta := renderContinuationDigests(steps, continuationDigestOpts{
		floorChars: 1000, sampleN: defaultDigestSampleN, scalarMax: defaultDigestScalarMax, maxKeys: defaultDigestMaxKeys,
	})
	if !json.Valid([]byte(bodies[0])) || meta[0].Method != "digest" {
		t.Errorf("valid-JSON step must be a digest; body=%q method=%q", bodies[0], meta[0].Method)
	}
	if meta[1].Method != "truncate" {
		t.Errorf("non-JSON step must be marked truncate (→ C); method=%q", meta[1].Method)
	}
	if bodies[1] == "" {
		t.Error("non-JSON step must get a non-empty structural-floor body (fail-open)")
	}
}

// --- A: ordering, cost, N-of-M --------------------------------------------------------------------

func TestOrderedStepResults_ChronologicalNotLexical(t *testing.T) {
	completed := map[string]*StepResult{
		"step-10":  {StepID: "step-10"},
		"step-2":   {StepID: "step-2"},
		"step-1":   {StepID: "step-1"},
		"step-nil": nil, // must be dropped
	}
	got := orderedStepResults(completed)
	if len(got) != 3 {
		t.Fatalf("nil entries must be dropped; got %d steps, want 3", len(got))
	}
	want := []string{"step-1", "step-2", "step-10"} // numeric, NOT lexical (which gives 1,10,2)
	for i, w := range want {
		if got[i].StepID != w {
			t.Errorf("orderedStepResults[%d] = %s, want %s", i, got[i].StepID, w)
		}
	}
}

func TestStepSeq(t *testing.T) {
	cases := map[string]int{"step-1": 1, "step-5": 5, "step-10": 10}
	for id, want := range cases {
		if got := stepSeq(id); got != want {
			t.Errorf("stepSeq(%q) = %d, want %d", id, got, want)
		}
	}
	for _, weird := range []string{"weird", "step-", "step-abc"} {
		if got := stepSeq(weird); got != 1<<30 {
			t.Errorf("stepSeq(%q) = %d, want sentinel %d", weird, got, 1<<30)
		}
	}
}

func TestEmitNOfMNote(t *testing.T) {
	var full strings.Builder
	emitNOfMNote(&full, 3, 3)
	if !strings.Contains(full.String(), "showing 3 of 3 completed steps") {
		t.Errorf("N==M must still emit the note; got %q", full.String())
	}
	if strings.Contains(full.String(), "omitted for budget") {
		t.Errorf("N==M must NOT include the eviction note; got %q", full.String())
	}

	var evicted strings.Builder
	emitNOfMNote(&evicted, 1, 3)
	if !strings.Contains(evicted.String(), "showing 1 of 3 completed steps") ||
		!strings.Contains(evicted.String(), "referenceable by step-ID at execution") {
		t.Errorf("N<M must include the eviction/addressability note; got %q", evicted.String())
	}

	var empty strings.Builder
	emitNOfMNote(&empty, 0, 0)
	if empty.String() != "" {
		t.Errorf("zero steps must emit nothing; got %q", empty.String())
	}
}

func TestStepRenderCost(t *testing.T) {
	success := stepRenderCost(&StepResult{Instruction: "hi", Success: true}, "abcd")
	if success != continuationStepOverhead+2+4 {
		t.Errorf("success cost = %d, want %d", success, continuationStepOverhead+2+4)
	}
	failed := stepRenderCost(&StepResult{Instruction: "hi", Success: false, Error: "boom\nmore"}, "ignored-body")
	if failed != continuationStepOverhead+2+len("boom") {
		t.Errorf("failed cost = %d, want %d (uses first error line, not body)", failed, continuationStepOverhead+2+len("boom"))
	}
}

// --- Integration: the continuation builder --------------------------------------------------------

func newDigestTestOrchestrator(budget int) *AIOrchestrator {
	return &AIOrchestrator{
		config: &OrchestratorConfig{
			RoutingMode:                     ModeAutonomous,
			ContinuationResultMaxTotalChars: budget,
			IterativePlanning:               IterativePlanConfig{Enabled: true, MaxPhases: 5, MaxTotalSteps: 200},
		},
		capabilityProvider: &mockCapabilityProviderForPhaseContext{captureFunc: func(map[string]interface{}) {}},
	}
}

func TestBuildContinuationPrompt_GreedyEvictionAndNOfM(t *testing.T) {
	orch := newDigestTestOrchestrator(50) // tiny budget forces eviction (per-step overhead alone > 50)
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: `{"id":"s1"}`, Success: true},
		"step-2": {StepID: "step-2", AgentName: "a", Response: `{"id":"s2"}`, Success: true},
		"step-3": {StepID: "step-3", AgentName: "a", Response: `{"id":"s3"}`, Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1", "step-2", "step-3"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if !strings.Contains(res.Prompt, "showing 1 of 3 completed steps") {
		t.Errorf("expected N-of-M eviction note 'showing 1 of 3'; prompt:\n%s", res.Prompt)
	}
	if !strings.Contains(res.Prompt, "older steps omitted for budget") {
		t.Error("expected eviction explanation in the N-of-M note")
	}
	if !strings.Contains(res.Prompt, "Step step-3 (") {
		t.Error("newest step (step-3) must be kept")
	}
	if strings.Contains(res.Prompt, "Step step-1 (") {
		t.Error("oldest step (step-1) must be evicted under the tiny budget")
	}
}

// stubContinuationDistiller is a mock ResultProcessor for the C-escalation path (no real LLM). It
// records the step IDs it was invoked for so tests can assert escalation order (newest-first).
type stubContinuationDistiller struct {
	out     string
	calls   int
	stepIDs []string
}

func (s *stubContinuationDistiller) ProcessForPrompt(_ context.Context, _ string, _ int, sc ResultProcessorContext) string {
	s.calls++
	s.stepIDs = append(s.stepIDs, sc.StepID)
	return s.out
}

func TestBuildContinuationPrompt_CEscalatesNonJSONBlob(t *testing.T) {
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationMaxEscalations = 8
	stub := &stubContinuationDistiller{out: "DISTILLED SIGNAL"}
	orch.SetContinuationDistiller(stub)
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: "plain non-JSON log blob", Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("C must escalate the non-JSON step once; calls=%d", stub.calls)
	}
	if !strings.Contains(res.Prompt, "[summary] DISTILLED SIGNAL") {
		t.Errorf("C summary must replace the floor for a non-JSON step; prompt:\n%s", res.Prompt)
	}
	if strings.Contains(res.Prompt, "plain non-JSON log blob") {
		t.Error("C must REPLACE the floor (not append) — the raw blob preview should be gone")
	}
}

func TestBuildContinuationPrompt_CDoesNotEscalateValidJSON(t *testing.T) {
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationMaxEscalations = 8
	stub := &stubContinuationDistiller{out: "SHOULD NOT APPEAR"}
	orch.SetContinuationDistiller(stub)
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: `{"k":"v"}`, Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("a valid-JSON step must NOT escalate to C; calls=%d", stub.calls)
	}
	if strings.Contains(res.Prompt, "SHOULD NOT APPEAR") {
		t.Error("no C summary for valid JSON")
	}
}

func TestBuildContinuationPrompt_CEscalationCap(t *testing.T) {
	orch := newDigestTestOrchestrator(1 << 20) // large budget so nothing is evicted
	orch.config.ContinuationMaxEscalations = 1 // cap at 1
	stub := &stubContinuationDistiller{out: "SUMMARY"}
	orch.SetContinuationDistiller(stub)
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: "blob one", Success: true},
		"step-2": {StepID: "step-2", AgentName: "a", Response: "blob two", Success: true},
		"step-3": {StepID: "step-3", AgentName: "a", Response: "blob three", Success: true},
	}
	if _, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1", "step-2", "step-3"}, "continue", 2); err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("escalations must be capped at ContinuationMaxEscalations=1; calls=%d", stub.calls)
	}
	// The cap must be spent on the NEWEST non-JSON step (step-3), per the recency principle.
	if len(stub.stepIDs) != 1 || stub.stepIDs[0] != "step-3" {
		t.Errorf("cap must escalate the newest non-JSON step (step-3); got %v", stub.stepIDs)
	}
}

func TestBuildContinuationPrompt_CFailOpenEmptySummary(t *testing.T) {
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationMaxEscalations = 8
	stub := &stubContinuationDistiller{out: ""} // distiller yields nothing
	orch.SetContinuationDistiller(stub)
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: "non-json blob preview text", Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if !strings.Contains(res.Prompt, "non-json blob preview text") {
		t.Error("floor body must remain when C returns empty (fail-open)")
	}
	if strings.Contains(res.Prompt, "[summary]") {
		t.Error("no summary marker when C returns an empty string")
	}
}

func TestBuildContinuationPrompt_FailedAlwaysKeptUnderEviction(t *testing.T) {
	orch := newDigestTestOrchestrator(50) // tiny budget forces eviction of older successful steps
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Success: false, Error: "boom"},                      // oldest, failed
		"step-2": {StepID: "step-2", AgentName: "a", Response: strings.Repeat("y", 2000), Success: true}, // big blob
		"step-3": {StepID: "step-3", AgentName: "a", Response: strings.Repeat("z", 2000), Success: true}, // newest big blob
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1", "step-2", "step-3"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if !strings.Contains(res.Prompt, "[FAILED: boom]") {
		t.Error("failed step must render a [FAILED] marker and be kept despite the tiny budget")
	}
	if strings.Contains(res.Prompt, "Step step-2 (") {
		t.Error("older successful step must be evicted under the tiny budget")
	}
	if !strings.Contains(res.Prompt, "Step step-3 (") {
		t.Error("newest successful step must be kept")
	}
}

func TestBuildContinuationPrompt_ChildSummaryNote(t *testing.T) {
	orch := newDigestTestOrchestrator(32768)
	orch.logger = &TestLogger{} // exercise the o.logger != nil branch of the child-summary block
	completed := map[string]*StepResult{
		"step-1": {
			StepID: "step-1", AgentName: "orchestrator-agent", Success: true,
			Response: `{"steps":[{"agent_name":"sub","capability":"do_thing","success":true,"response":"done"}]}`,
		},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if !strings.Contains(res.Prompt, "internally executed these sub-steps") {
		t.Errorf("an orchestrator-delegation step must emit the child-steps NOTE; prompt:\n%s", res.Prompt)
	}
	if !strings.Contains(res.Prompt, "Do NOT duplicate") {
		t.Error("child-steps NOTE must carry the de-dup directive")
	}
}

func TestBuildContinuationPrompt_ScalarMaxKnobFlowsThrough(t *testing.T) {
	// The ContinuationDigestScalarMax knob must flow config → builder → digest (mitigates finding #6:
	// long salient strings can be made visible by raising it, or elided sooner by lowering it).
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationDigestScalarMax = 10 // elide strings longer than 10 chars
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: `{"note":"this value is definitely longer than ten chars"}`, Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if strings.Contains(res.Prompt, "definitely longer than ten chars") {
		t.Errorf("ContinuationDigestScalarMax=10 must elide the long value; prompt:\n%s", res.Prompt)
	}
	if !strings.Contains(res.Prompt, "(46 chars)") { // the "…(N chars)" elision sentinel
		t.Errorf("expected an elision sentinel; prompt:\n%s", res.Prompt)
	}
	if !strings.Contains(res.Prompt, `"note"`) {
		t.Error("the key must remain (structure-complete) even when its value is elided")
	}
}

func TestBuildContinuationPrompt_NonJSONFloorUsesResultMaxChars(t *testing.T) {
	// Phase 14 re-wire: the non-JSON floor preview is governed by ContinuationResultMaxChars (no longer
	// a hardcoded const), so the previously-orphaned knob has effect again.
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationResultMaxChars = 20 // tiny non-JSON floor
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Response: strings.Repeat("Q", 500), Success: true}, // non-JSON
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if strings.Contains(res.Prompt, strings.Repeat("Q", 21)) {
		t.Error("non-JSON floor must respect ContinuationResultMaxChars=20 (no 21-char run)")
	}
	if !strings.Contains(res.Prompt, strings.Repeat("Q", 20)) {
		t.Error("the 20-char floor preview should be present")
	}
}

func TestBuildContinuationPrompt_DigestMaxKeysAndArraySampleKnobsFlowThrough(t *testing.T) {
	// The ContinuationDigestMaxKeys and ContinuationDigestArraySample knobs must flow
	// config → builder → digestOpts → buildDecisionDigest. (ScalarMax and ResultMaxChars already
	// have end-to-end tests above; these two previously had only buildDecisionDigest-level coverage.)
	orch := newDigestTestOrchestrator(32768)
	orch.config.ContinuationDigestMaxKeys = 2     // keep 2 sorted keys + a sentinel
	orch.config.ContinuationDigestArraySample = 1 // keep 1 array element + a sentinel
	completed := map[string]*StepResult{
		"step-1": {StepID: "step-1", AgentName: "a", Success: true,
			Response: `{"arr":[111,222,333,444,555],"kA":1,"kB":2,"kC":3}`},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	// MaxKeys=2: the 4-key object is sampled to 2 sorted keys (arr, kA) + a "__truncated_keys__" sentinel.
	if !strings.Contains(res.Prompt, "2 more of 4 keys") {
		t.Errorf("ContinuationDigestMaxKeys=2 must sample the wide object (expected key sentinel); prompt:\n%s", res.Prompt)
	}
	if strings.Contains(res.Prompt, `"kC"`) {
		t.Error("a key past the MaxKeys cap must be dropped (kC should be absent)")
	}
	// ArraySample=1: the 5-element array is sampled to its head + a length sentinel.
	if !strings.Contains(res.Prompt, "4 more of 5") {
		t.Errorf("ContinuationDigestArraySample=1 must sample the array (expected array sentinel); prompt:\n%s", res.Prompt)
	}
	if strings.Contains(res.Prompt, "555") {
		t.Error("array elements past the sample size must be dropped (555 should be absent)")
	}
}

func TestBuildContinuationPrompt_DigestLegendAndChronological(t *testing.T) {
	orch := newDigestTestOrchestrator(32768)
	completed := map[string]*StepResult{
		"step-1":  {StepID: "step-1", AgentName: "a", Response: `{"id":"s1"}`, Success: true},
		"step-2":  {StepID: "step-2", AgentName: "a", Response: `{"id":"s2"}`, Success: true},
		"step-10": {StepID: "step-10", AgentName: "a", Response: `{"id":"s10"}`, Success: true},
	}
	res, err := orch.buildContinuationPrompt(context.Background(), "next", completed, []string{"step-1", "step-2", "step-10"}, "continue", 2)
	if err != nil {
		t.Fatalf("buildContinuationPrompt: %v", err)
	}
	if !strings.Contains(res.Prompt, "structure-preserving digest") {
		t.Error("planning_instructions must carry the digest-format legend")
	}
	if !strings.Contains(res.Prompt, "showing 3 of 3 completed steps") {
		t.Errorf("all three steps fit; expected 'showing 3 of 3'; prompt:\n%s", res.Prompt)
	}
	// Chronological (numeric) order: step-2 must render before step-10.
	if i2, i10 := strings.Index(res.Prompt, "Step step-2 ("), strings.Index(res.Prompt, "Step step-10 ("); i2 < 0 || i10 < 0 || i2 > i10 {
		t.Errorf("steps must render in chronological order (step-2 before step-10); got i2=%d i10=%d", i2, i10)
	}
}
