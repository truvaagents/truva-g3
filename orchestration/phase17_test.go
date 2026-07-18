package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel"
)

// Phase 17 — result-compaction hardening and routing: number-preserving JSON decode (P17.5),
// wrapper-preserving map-reduce chunking (P17.6), reduce-gate overhead accounting (P17.7), and
// the dedicated MapReduceThresholdBytes routing threshold (P17.1–P17.4, default 0 = disabled).

// bigSnowflakeID is > 2^53, so a float64 round-trip mangles it to scientific notation. Every trim
// hop must preserve it verbatim via unmarshalPreservingNumbers (UseNumber).
const bigSnowflakeID = "1781784008606859622"

// warnCapturingLogger records Warn calls (normalizeResultDistillConfig uses Warn, not
// WarnWithContext, so the shared capturingLogger — which only overrides WarnWithContext — misses it).
type warnCapturingLogger struct {
	core.NoOpLogger
	mu    sync.Mutex
	warns []map[string]interface{}
}

func (l *warnCapturingLogger) Warn(_ string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fields)
}

func (l *warnCapturingLogger) normalizationWarnings() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, f := range l.warns {
		if f["operation"] == "result_distill.config_normalization" {
			n++
		}
	}
	return n
}

// advisoryWarnings counts the deadline-opt-out advisories, which carry a DISTINCT operation
// from genuine normalization warnings so misconfiguration alerting is not paged by a
// documented opt-out (review finding).
func (l *warnCapturingLogger) advisoryWarnings() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, f := range l.warns {
		if f["operation"] == "result_distill.config_advisory" {
			n++
		}
	}
	return n
}

// --- P17.5: number-preserving decode ---

func TestUnmarshalPreservingNumbers_LargeIDVerbatim(t *testing.T) {
	cases := []string{
		`{"id":` + bigSnowflakeID + `}`,
		`{"outer":{"id":` + bigSnowflakeID + `}}`,
		`[{"id":` + bigSnowflakeID + `}]`,
	}
	for _, in := range cases {
		v, err := unmarshalPreservingNumbers([]byte(in))
		if err != nil {
			t.Fatalf("unmarshalPreservingNumbers(%q) errored: %v", in, err)
		}
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("re-marshal errored: %v", err)
		}
		if !strings.Contains(string(out), bigSnowflakeID) {
			t.Errorf("large ID not preserved: in=%q out=%q", in, out)
		}
		if strings.Contains(string(out), "e+") || strings.Contains(string(out), "E+") {
			t.Errorf("large ID mangled to scientific notation: %q", out)
		}
	}
}

func TestUnmarshalPreservingNumbers_Strictness(t *testing.T) {
	ok := []string{
		`{"a":1}`,
		`[1,2,3]`,
		`123`,
		`"s"`,
		"{\"a\":1}\n  ", // trailing whitespace is allowed (json.Unmarshal parity)
	}
	for _, in := range ok {
		if _, err := unmarshalPreservingNumbers([]byte(in)); err != nil {
			t.Errorf("expected %q to decode, got error %v", in, err)
		}
	}
	// Trailing non-whitespace after the top-level value must be rejected, exactly like
	// json.Unmarshal — dec.More() would wrongly accept the ']'/'}' cases (that's the hole the
	// helper's io.EOF check closes).
	bad := []string{
		`{"a":1}]`,
		`{"a":1} {"b":2}`,
		`[1,2] 3`,
		`123 456`,
		``,
		`not json`,
		`{"a":1`,
	}
	for _, in := range bad {
		if _, err := unmarshalPreservingNumbers([]byte(in)); err == nil {
			t.Errorf("expected %q to be rejected, got nil error", in)
		}
	}
}

func TestIsScalar_JSONNumber(t *testing.T) {
	if !isScalar(json.Number("1781784008606859622")) {
		t.Error("json.Number must classify as scalar (else UseNumber values misroute through the complex path)")
	}
}

// TestLargeID_SurvivesChunker proves the map-reduce object chunker (P17.6) keeps large IDs exact
// (P17.5): every chunk re-marshals the wrapper+records, and the IDs must not be float64-mangled.
func TestLargeID_SurvivesChunker(t *testing.T) {
	records := make([]interface{}, 20)
	for i := range records {
		records[i] = map[string]interface{}{"id": json.Number(bigSnowflakeID), "line": "x"}
	}
	raw, _ := json.Marshal(map[string]interface{}{"streams": records})

	chunks, dropped, _, _ := chunkWholeUnits(string(raw), 120)
	if dropped {
		t.Fatal("wrapper must be preserved")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if !strings.Contains(c, bigSnowflakeID) {
			t.Errorf("chunk %d dropped/mangled the large ID: %q", i, c)
		}
		if strings.Contains(c, "e+18") {
			t.Errorf("chunk %d mangled the large ID to scientific notation: %q", i, c)
		}
	}
}

// TestLargeID_SurvivesSynthesisFormatting exercises the exact re-parse both synthesis prompt
// builders run (synthesizer.go / orchestrator.go): unmarshalPreservingNumbers →
// deserializeStringValues → MarshalIndent. A top-level ID AND an ID inside a JSON-encoded string
// field (the incident's Loki log-line shape) must both survive verbatim.
func TestLargeID_SurvivesSynthesisFormatting(t *testing.T) {
	response := `{"id":` + bigSnowflakeID + `,"nested":"{\"trace_id\":` + bigSnowflakeID + `}"}`

	parsed, err := unmarshalPreservingNumbers([]byte(response))
	if err != nil {
		t.Fatalf("re-parse errored: %v", err)
	}
	parsed = deserializeStringValues(parsed)
	formatted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent errored: %v", err)
	}
	// Both the top-level id and the embedded trace_id must appear verbatim (twice total).
	if got := strings.Count(string(formatted), bigSnowflakeID); got != 2 {
		t.Errorf("expected the large ID verbatim twice (top-level + embedded), got %d in:\n%s", got, formatted)
	}
	if strings.Contains(string(formatted), "e+") {
		t.Errorf("large ID mangled to scientific notation:\n%s", formatted)
	}
}

// --- P17.1/P17.3: dedicated map-reduce routing threshold ---

// TestMapReduceThreshold_RoutesMidBandVsDisabled verifies the byte threshold routes an in-context
// result to map-reduce when set, and leaves it single-call when disabled (0) — independent of
// ModelContextTokens (huge here, so the over-context trigger never fires).
func TestMapReduceThreshold_RoutesMidBandVsDisabled(t *testing.T) {
	payload := mapReduceTestArray(80) // ~1.9 KB, fits the huge context below

	newDistiller := func(threshold int) (*LLMDistiller, *countingAI) {
		ai := &countingAI{out: "EXTRACT"}
		cfg := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 4096,
			Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 4,
			MapReduceThresholdBytes: threshold,
			CompactionDeadline:      5 * time.Second,
		}
		return NewLLMDistiller(ai, cfg, NewStructuralTrimmer(nil, nil), nil), ai
	}

	// Disabled (0): over-context is false (huge ctx) and over-threshold is off → single call.
	dOff, aiOff := newDistiller(0)
	dOff.ProcessForPrompt(context.Background(), payload, 4096,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
	if aiOff.count() != 1 {
		t.Errorf("threshold disabled: expected a single distill call, got %d", aiOff.count())
	}

	// Enabled (== PreFilterBudget: survives normalization, yields >1 chunk): routes to map-reduce.
	dOn, aiOn := newDistiller(100)
	dOn.ProcessForPrompt(context.Background(), payload, 4096,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
	if aiOn.count() < 2 {
		t.Errorf("threshold enabled: expected map-reduce fan-out (>1 call), got %d", aiOn.count())
	}
}

// TestMapReduceThreshold_BelowThresholdStaysSingleCall verifies a result at/under the threshold is
// not routed to map-reduce.
func TestMapReduceThreshold_BelowThresholdStaysSingleCall(t *testing.T) {
	ai := &countingAI{out: "EXTRACT"}
	cfg := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 4096,
		Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 4,
		MapReduceThresholdBytes: 4096, // well above the payload
		CompactionDeadline:      5 * time.Second,
	}
	d := NewLLMDistiller(ai, cfg, NewStructuralTrimmer(nil, nil), nil)

	payload := mapReduceTestArray(30) // ~0.7 KB < 4096 threshold
	d.ProcessForPrompt(context.Background(), payload, 4096,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
	if ai.count() != 1 {
		t.Errorf("result below the threshold must stay single-call, got %d calls", ai.count())
	}
}

// --- P17.2: single-chunk-footgun normalization ---

func TestNormalizeThreshold_BelowPreFilterDisabledWithWarning(t *testing.T) {
	// Direct NewLLMDistiller construction path.
	direct := &warnCapturingLogger{}
	NewLLMDistiller(&countingAI{}, ResultDistillConfig{
		PreFilterBudget: 131072, MapReduceThresholdBytes: 1000, CompactionDeadline: 45 * time.Second, // 1000 < 128 KB → normalized to 0
	}, NewStructuralTrimmer(nil, nil), direct)
	if direct.normalizationWarnings() != 1 {
		t.Errorf("direct NewLLMDistiller: expected one normalization warning, got %d", direct.normalizationWarnings())
	}

	// Layer-2 helper construction path.
	helper := &warnCapturingLogger{}
	BuildDistillationEnabledResultProcessor(ResultDistillConfig{
		PreFilterBudget: 131072, MapReduceThresholdBytes: 1000, CompactionDeadline: 45 * time.Second,
	}, &countingAI{}, nil, helper)
	if helper.normalizationWarnings() != 1 {
		t.Errorf("Layer-2 helper: expected one normalization warning, got %d", helper.normalizationWarnings())
	}

	// A valid threshold (>= PreFilterBudget) must NOT warn.
	valid := &warnCapturingLogger{}
	NewLLMDistiller(&countingAI{}, ResultDistillConfig{
		PreFilterBudget: 131072, MapReduceThresholdBytes: 262144, CompactionDeadline: 45 * time.Second, // 256 KB >= 128 KB
	}, NewStructuralTrimmer(nil, nil), valid)
	if valid.normalizationWarnings() != 0 {
		t.Errorf("valid threshold must not warn, got %d", valid.normalizationWarnings())
	}
}

// TestNormalizeThreshold_UnsetPreFilterResolvedBeforeCompare guards the resolve-then-compare order:
// a Layer-3 caller may pass PreFilterBudget<=0 (resolved to defaultPreFilterBudget at use), so the
// threshold must be compared against the resolved default, not a raw 0 that would wave it through.
func TestNormalizeThreshold_UnsetPreFilterResolvedBeforeCompare(t *testing.T) {
	log := &warnCapturingLogger{}
	// PreFilterBudget 0 resolves to defaultPreFilterBudget (128 KB); threshold 1000 < that → warn.
	NewLLMDistiller(&countingAI{}, ResultDistillConfig{
		PreFilterBudget: 0, MapReduceThresholdBytes: 1000, CompactionDeadline: 45 * time.Second,
	}, NewStructuralTrimmer(nil, nil), log)
	if log.normalizationWarnings() != 1 {
		t.Errorf("unset PreFilterBudget must resolve to the default before the threshold compare; expected 1 warning, got %d", log.normalizationWarnings())
	}
}

// --- P17.4: cache salt keys the routing knobs ---

func TestDistillKeySalt_KeysRoutingKnobs(t *testing.T) {
	base := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, ModelContextTokens: 150000, MapReduceThresholdBytes: 0}

	ctxChanged := base
	ctxChanged.ModelContextTokens = 120000
	thrChanged := base
	thrChanged.MapReduceThresholdBytes = 262144

	baseSalt := distillKeySalt(base, nil, nil)
	if distillKeySalt(base, nil, nil) != baseSalt {
		t.Error("salt must be stable for an identical config")
	}
	if distillKeySalt(ctxChanged, nil, nil) == baseSalt {
		t.Error("a ModelContextTokens change must change the salt (routing/reduce-gate behavior changed)")
	}
	if distillKeySalt(thrChanged, nil, nil) == baseSalt {
		t.Error("a MapReduceThresholdBytes change must change the salt (routing behavior changed)")
	}
}

// --- Phase 17 review fixes (post-implementation code review, 2026-07-17) ---

// TestChunker_OversizedElementSplitBounded: an element larger than the per-chunk budget is
// recursively split into standalone chunks so no chunk exceeds ~chunkBytes (review finding: it
// previously shipped whole, with overshoot bounded only by element size — an over-context map
// call). The wrapper must still reach an LLM via a grouped or fallback chunk.
func TestChunker_OversizedElementSplitBounded(t *testing.T) {
	elements := []interface{}{
		map[string]interface{}{"blob": strings.Repeat("x", 3000)}, // ~3KB > budget
	}
	for i := 0; i < 5; i++ {
		elements = append(elements, map[string]interface{}{"id": i, "v": "small"})
	}
	raw, _ := json.Marshal(map[string]interface{}{"meta": "m", "records": elements})

	chunks, dropped, cov, strategy := chunkWholeUnits(string(raw), 1000)
	if dropped || cov != 1 {
		t.Errorf("no content is dropped by the split: expected dropped=false cov=1, got %v/%v", dropped, cov)
	}
	// Worst-of strategy: the oversized element's recursive split degrades to byte-splitting, and
	// the stamped strategy must SURFACE that (review finding: a top-level "wrapper" label here
	// hid the torn-record mode the marker exists to expose).
	if strategy != "bytes" {
		t.Errorf("expected worst-of strategy bytes (degraded recursive split), got %q", strategy)
	}
	if len(chunks) < 4 {
		t.Fatalf("expected the oversized element split across multiple chunks, got %d", len(chunks))
	}
	wrapperSeen := false
	for i, c := range chunks {
		if len(c) > 1000 {
			t.Errorf("chunk %d exceeds chunkBytes: %d bytes (unbounded overshoot regressed)", i, len(c))
		}
		if strings.Contains(c, `"meta"`) {
			wrapperSeen = true
		}
	}
	if !wrapperSeen {
		t.Error("wrapper fields must reach at least one chunk")
	}
}

// TestChunker_LoneGiantElementFansOut: a dominant array holding ONE giant element must produce
// multiple chunks (review finding: it previously collapsed to a single chunk, which the caller
// routed to the structural floor with zero LLM calls — the original incident class). The wrapper
// is emitted once via the all-oversized fallback.
func TestChunker_LoneGiantElementFansOut(t *testing.T) {
	raw, _ := json.Marshal(map[string]interface{}{
		"status": "ok",
		"data":   []interface{}{map[string]interface{}{"blob": strings.Repeat("y", 3000)}},
	})

	chunks, _, _, _ := chunkWholeUnits(string(raw), 1000)
	if len(chunks) < 3 {
		t.Fatalf("expected the lone giant element to fan out into multiple chunks, got %d", len(chunks))
	}
	wrapperSeen := false
	for i, c := range chunks {
		if len(c) > 1000 {
			t.Errorf("chunk %d exceeds chunkBytes: %d bytes", i, len(c))
		}
		if strings.Contains(c, `"status"`) {
			wrapperSeen = true
			// The fabricated wrapper chunk must carry the sentinel, never a literally empty
			// dominant array (review finding: {"data":[]} reads as an authoritative "zero
			// records" claim — a manufactured false-negative that can survive the reduce).
			if strings.Contains(c, `"data":[]`) {
				t.Errorf("wrapper chunk fabricates an empty dominant array: %q", c)
			}
			if !strings.Contains(c, "split into separate segments") {
				t.Errorf("wrapper chunk must carry the split-records sentinel, got: %q", c)
			}
		}
	}
	if !wrapperSeen {
		t.Error("all-oversized fallback must emit the wrapper once so its fields reach an LLM")
	}
}

// TestMapReduce_ZeroChunksFloorsWithoutLLM: a payload the chunkers drop entirely (only blank
// lines → chunkByLines emits zero chunks) must go straight to the structural floor — dispatching
// the raw over-budget result in one extract call would be a doomed over-context request (review
// finding).
func TestMapReduce_ZeroChunksFloorsWithoutLLM(t *testing.T) {
	mockAI := &countingAI{out: "EXTRACT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	payload := strings.Repeat("\n", 300) // ~86 tokens > 50 → routes; chunkByLines yields 0 chunks
	out := d.ProcessForPrompt(context.Background(), payload, 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})

	if mockAI.count() != 0 {
		t.Errorf("zero chunks must floor without any LLM call, got %d calls", mockAI.count())
	}
	_ = out // floor output shape is the StructuralTrimmer's concern; the guard is the LLM count
}

// TestToBool_JSONNumber: numeric truthy flags decoded as json.Number (P17.5 UseNumber) must
// coerce like float64 (review finding: the missing case silently flipped enabled:1 to false in
// auto-wired boolean tool parameters).
func TestToBool_JSONNumber(t *testing.T) {
	cases := []struct {
		in   json.Number
		want bool
	}{
		{"1", true}, {"0", false}, {"2.5", true}, {"-1", true}, {"0.0", false}, {"garbage", false},
	}
	for _, c := range cases {
		if got := toBool(c.in); got != c.want {
			t.Errorf("toBool(json.Number(%q)) = %v, want %v", c.in, got, c.want)
		}
	}
	if toBool(float64(1)) != toBool(json.Number("1")) {
		t.Error("json.Number and float64 truthiness must agree")
	}
}

// TestNormalize_BackfillsModelContextTokens: an unset context is backfilled so the reduce gate
// (data+overhead <= ModelContextTokens) is satisfiable on threshold-routed map-reduces (review
// finding: a zero context permanently truncated the reduce step for Layer-3 configs).
func TestNormalize_BackfillsModelContextTokens(t *testing.T) {
	got := normalizeResultDistillConfig(ResultDistillConfig{}, nil)
	if got.ModelContextTokens != defaultModelContextTokens {
		t.Errorf("expected backfill to %d, got %d", defaultModelContextTokens, got.ModelContextTokens)
	}
	kept := normalizeResultDistillConfig(ResultDistillConfig{ModelContextTokens: 42}, nil)
	if kept.ModelContextTokens != 42 {
		t.Errorf("an explicit context must be preserved, got %d", kept.ModelContextTokens)
	}
}

// TestBuildDecisionDigest_RejectsTrailingGarbage: the digest must treat trailing-garbage JSON as
// a blob (review finding: the old dec.More() guard passed `{"a":1}]` as clean, skeletonizing
// truncated/concatenated responses as trustworthy structure).
func TestBuildDecisionDigest_RejectsTrailingGarbage(t *testing.T) {
	for _, in := range []string{`{"a":1}]`, `{"a":1}}`, `[1,2] 3`} {
		if _, degenerate, _ := buildDecisionDigest(in, 3, 100, 50); !degenerate {
			t.Errorf("expected %q to be treated as a degenerate blob", in)
		}
	}
	digest, degenerate, _ := buildDecisionDigest(`{"a":1}`, 3, 100, 50)
	if degenerate || digest == "" {
		t.Errorf("clean JSON must still digest: degenerate=%v digest=%q", degenerate, digest)
	}
}

// TestChunkStrategy_Stamped: the map-reduce metadata records the chunking mode so the byte-split
// degraded mode (records torn mid-JSON) is distinguishable from structural chunking (review
// finding: the maxWrapperShare decline was invisible in every signal).
func TestChunkStrategy_Stamped(t *testing.T) {
	t.Run("heavy wrapper byte-split stamps bytes", func(t *testing.T) {
		elements := make([]interface{}, 20)
		for i := range elements {
			elements[i] = map[string]interface{}{"id": i, "v": strings.Repeat("d", 40)}
		}
		raw, _ := json.Marshal(map[string]interface{}{
			"hdr":  strings.Repeat("h", 800), // wrapper > maxWrapperShare*1000 → decline → byte-split
			"data": elements,
		})
		mockAI := &countingAI{out: "EXTRACT"}
		cfg := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 1000, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 100, MapConcurrency: 4,
			CompactionDeadline: 5 * time.Second,
		}
		d := NewLLMDistiller(mockAI, cfg, NewStructuralTrimmer(nil, nil), nil)
		ctx, meta := WithTrimMetadataCapture(context.Background())
		d.ProcessForPrompt(ctx, string(raw), 2000, ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
		if meta.ChunkStrategy != "bytes" {
			t.Errorf("expected chunk_strategy=bytes on the declined-wrapper path, got %q", meta.ChunkStrategy)
		}
	})
	t.Run("preserved wrapper stamps wrapper", func(t *testing.T) {
		elements := make([]interface{}, 30)
		for i := range elements {
			elements[i] = map[string]interface{}{"id": i, "v": strings.Repeat("d", 40)}
		}
		raw, _ := json.Marshal(map[string]interface{}{"status": "ok", "data": elements})
		mockAI := &countingAI{out: "EXTRACT"}
		cfg := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 200, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 100, MapConcurrency: 4,
			CompactionDeadline: 5 * time.Second,
		}
		d := NewLLMDistiller(mockAI, cfg, NewStructuralTrimmer(nil, nil), nil)
		ctx, meta := WithTrimMetadataCapture(context.Background())
		d.ProcessForPrompt(ctx, string(raw), 2000, ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
		if meta.ChunkStrategy != "wrapper" {
			t.Errorf("expected chunk_strategy=wrapper on the preserving path, got %q", meta.ChunkStrategy)
		}
	})
}

// TestDistillKeySalt_SelfNormalizes: the salt hashes the normalized config regardless of what the
// caller passes (review finding: normalize-before-salt was a comment-enforced contract; a caller
// salting a raw config would fragment the cache or serve stale output).
func TestDistillKeySalt_SelfNormalizes(t *testing.T) {
	raw := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, MapReduceThresholdBytes: 1000} // 1000 < prefilter → runs as 0
	normalized := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, MapReduceThresholdBytes: 0}
	if distillKeySalt(raw, nil, nil) != distillKeySalt(normalized, nil, nil) {
		t.Error("a raw config must salt identically to its normalized form (same runtime behavior → same key)")
	}
	unsetCtx := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072}
	explicitDefault := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, ModelContextTokens: defaultModelContextTokens}
	if distillKeySalt(unsetCtx, nil, nil) != distillKeySalt(explicitDefault, nil, nil) {
		t.Error("an unset ModelContextTokens must salt as its backfilled default")
	}
}

// --- Coverage-gap tests (pre-fix-batch regression net, 2026-07-18) ---
//
// These pin CURRENT correct behavior in the spots the coverage profile showed thin:
// metrics emissions (no prior test installed a global registry, leaving every counter
// unasserted — a working-tree metrics regression was once caught only in code review because
// the suite could not see counters), the single-chunk failure observability block, disclosure
// composition, the routing reason attribute, the new env knob, envelope replay breadth,
// strategy stamps, and the reduce-success path.

// captureMetricsRegistry is a test double for core.MetricsRegistry that records counter
// increments, so tests can assert the metric emissions the nil-registry default silently skips.
type captureMetricsRegistry struct {
	mu       sync.Mutex
	counters map[string]int
}

func newCaptureMetricsRegistry() *captureMetricsRegistry {
	return &captureMetricsRegistry{counters: map[string]int{}}
}

func (r *captureMetricsRegistry) Counter(name string, _ ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name]++
}
func (r *captureMetricsRegistry) EmitWithContext(_ context.Context, _ string, _ float64, _ ...string) {
}
func (r *captureMetricsRegistry) GetBaggage(_ context.Context) map[string]string { return nil }
func (r *captureMetricsRegistry) Gauge(_ string, _ float64, _ ...string)         {}
func (r *captureMetricsRegistry) Histogram(_ string, _ float64, _ ...string)     {}

func (r *captureMetricsRegistry) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[name]
}

// installTestMetricsRegistry installs a capturing global metrics registry and restores the
// previous one on cleanup.
func installTestMetricsRegistry(t *testing.T) *captureMetricsRegistry {
	t.Helper()
	prev := core.GetGlobalMetricsRegistry()
	reg := newCaptureMetricsRegistry()
	core.SetMetricsRegistry(reg)
	t.Cleanup(func() { core.SetMetricsRegistry(prev) })
	return reg
}

// TestSingleChunk_SuccessIncrementsMapreduceCounter: the single-chunk success path must count
// toward orchestration.result_distill.mapreduce like the fan-out path — the canary's volume
// measurement reads this counter.
func TestSingleChunk_SuccessIncrementsMapreduceCounter(t *testing.T) {
	reg := installTestMetricsRegistry(t)
	mockAI := &countingAI{out: "EXTRACT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 10, MapConcurrency: 4, CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	d.ProcessForPrompt(context.Background(), mapReduceTestArray(4), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

	if got := reg.count("orchestration.result_distill.mapreduce"); got != 1 {
		t.Errorf("single-chunk success must increment the mapreduce counter once, got %d", got)
	}
}

// TestMultiChunk_IncrementsMapreduceCounter pins the fan-out path's counter emission (previously
// unasserted: no test installed a registry).
func TestMultiChunk_IncrementsMapreduceCounter(t *testing.T) {
	reg := installTestMetricsRegistry(t)
	mockAI := &countingAI{out: "EXTRACT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4, CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	d.ProcessForPrompt(context.Background(), mapReduceTestArray(20), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

	if got := reg.count("orchestration.result_distill.mapreduce"); got != 1 {
		t.Errorf("fan-out must increment the mapreduce counter once, got %d", got)
	}
}

// TestSingleChunk_FailureFullObservability: a failed single-chunk extract must emit the full
// Pattern-4 sequence — llm_failed span event, failed counter, warn log — a paid-but-failed LLM
// call must never be metrics-silent (the canary's failure-rate readout depends on it).
func TestSingleChunk_FailureFullObservability(t *testing.T) {
	recorder := setupExecutorTestTracer(t)
	reg := installTestMetricsRegistry(t)
	logger := &TestLogger{}
	mockAI := &failAfterAI{succeedUntil: 0, out: "unused"} // every call errors
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 10, MapConcurrency: 4, CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), logger)

	tracer := otel.Tracer("phase17-test")
	ctx, span := tracer.Start(context.Background(), "single-chunk-failure")
	d.ProcessForPrompt(ctx, mapReduceTestArray(4), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})
	span.End()

	if got := reg.count("orchestration.result_distill.failed"); got != 1 {
		t.Errorf("failed counter = %d, want 1", got)
	}
	warns := logger.GetLogsByLevel("WARN")
	found := false
	for _, w := range warns {
		if w.Fields["operation"] == "result_distill.mapreduce" && w.Fields["error"] != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a WARN with operation=result_distill.mapreduce and an error field, got %v", warns)
	}
	eventSeen := false
	for _, s := range recorder.Ended() {
		for _, ev := range s.Events() {
			if ev.Name == "result_distill.llm_failed" {
				eventSeen = true
			}
		}
	}
	if !eventSeen {
		t.Error("expected the result_distill.llm_failed span event on the single-chunk failure path")
	}
}

// TestDisclosureComposition_PartialAndCombineTruncated: when a run is BOTH partial (failed
// chunks) and combine-truncated, both notes must appear exactly once — the
// compose-all-notes-append-ONCE contract, previously tested only one note at a time.
// Deterministic by construction: the first map call succeeds with a long extract and every later
// call errors IMMEDIATELY (failAfterAI), so completed(1) < total without any deadline dependence
// — no scheduling-timing flake window, and the test doesn't burn wall-clock waiting.
func TestDisclosureComposition_PartialAndCombineTruncated(t *testing.T) {
	mockAI := &failAfterAI{succeedUntil: 1, out: "FINDING " + strings.Repeat("x", 300)}
	// ModelContextTokens 100 both ROUTES the ~275-token payload to map-reduce (over-context) and
	// makes the over-target combine take the deterministic truncation arm (gate unsatisfiable),
	// so the partial and combine-truncation notes must compose on one output.
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 100, // floors to 256
		Model: "fast", ModelContextTokens: 100, MapConcurrency: 1,
		CompactionDeadline: 5 * time.Second, // never fires; partial comes from the failed chunks
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, nonCacheable := withNonCacheableCapture(context.Background())
	mctx, meta := WithTrimMetadataCapture(ctx)
	out := d.ProcessForPrompt(mctx, mapReduceTestArray(40), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find"})

	if got := strings.Count(out, "[partial:"); got != 1 {
		t.Errorf("expected the partial note exactly once, got %d in: %q", got, out)
	}
	if got := strings.Count(out, "[findings truncated:"); got != 1 {
		t.Errorf("expected the combine-truncation note exactly once, got %d in: %q", got, out)
	}
	if !*nonCacheable {
		t.Error("a partial + transient-truncated run must be non-cacheable")
	}
	if !meta.PartialCoverage || !meta.CombineTruncated || !meta.ContentLost {
		t.Errorf("metadata must record both losses: %+v", *meta)
	}
}

// TestMapreduceRoute_ReasonAttribute: the routing span event's reason attribute is the canary's
// cohort discriminator (threshold-routed vs pre-existing over-context) — assert both values.
func TestMapreduceRoute_ReasonAttribute(t *testing.T) {
	cases := []struct {
		name          string
		contextTokens int
		threshold     int
		wantReason    string
	}{
		{"threshold routing", 1_000_000, 100, "threshold"},
		{"context routing", 50, 0, "context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := setupExecutorTestTracer(t)
			mockAI := &countingAI{out: "EXTRACT"}
			config := ResultDistillConfig{
				Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
				Model: "fast", ModelContextTokens: tc.contextTokens, MapConcurrency: 4,
				MapReduceThresholdBytes: tc.threshold,
				CompactionDeadline:      5 * time.Second,
			}
			d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

			tracer := otel.Tracer("phase17-test")
			ctx, span := tracer.Start(context.Background(), "route")
			d.ProcessForPrompt(ctx, mapReduceTestArray(80), 2000,
				ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})
			span.End()

			var gotReason string
			for _, s := range recorder.Ended() {
				for _, ev := range s.Events() {
					if ev.Name == "result_distill.mapreduce_route" {
						for _, kv := range ev.Attributes {
							if string(kv.Key) == "reason" {
								gotReason = kv.Value.AsString()
							}
						}
					}
				}
			}
			if gotReason != tc.wantReason {
				t.Errorf("mapreduce_route reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

// TestMapReduceThresholdEnvParse pins the >= 0 parse of TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD.
// The explicit-"0" case is the post-canary kill switch: when the default flips to a nonzero value,
// an operator must still be able to disable routing with "0" — a consistency-minded change to the
// repo-wide `> 0` guard convention would silently break it.
func TestMapReduceThresholdEnvParse(t *testing.T) {
	t.Run("default is 0 (disabled)", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "") // ignore any ambient override (the canary tells operators to export this)
		if got := DefaultConfig().ResultDistill.MapReduceThresholdBytes; got != 0 {
			t.Errorf("default = %d, want 0", got)
		}
	})
	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "262144")
		if got := DefaultConfig().ResultDistill.MapReduceThresholdBytes; got != 262144 {
			t.Errorf("threshold = %d, want 262144 from env", got)
		}
	})
	// The kill-switch subtests run the parse helper against a NONZERO base: with the current
	// zero default, accepted-0 and ignored-0 are observationally identical through
	// DefaultConfig(), so a `> 0` regression of the deliberate `>= 0` guard would pass a
	// DefaultConfig-based assertion — mutation-verified during review.
	t.Run("explicit 0 overrides a nonzero base (kill switch, >= 0 parse)", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "0")
		if got := applyMapReduceThresholdEnv(262144); got != 0 {
			t.Errorf("explicit \"0\" must override a nonzero base (post-canary kill switch), got %d", got)
		}
	})
	t.Run("negative keeps the base", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "-5")
		if got := applyMapReduceThresholdEnv(262144); got != 262144 {
			t.Errorf("negative env must keep the base, got %d", got)
		}
	})
	t.Run("garbage keeps the base", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "not-a-number")
		if got := applyMapReduceThresholdEnv(262144); got != 262144 {
			t.Errorf("garbage env must keep the base, got %d", got)
		}
	})
	t.Run("unset keeps the base", func(t *testing.T) {
		t.Setenv("TRUVAG3_RESULT_DISTILL_MAPREDUCE_THRESHOLD", "")
		if got := applyMapReduceThresholdEnv(262144); got != 262144 {
			t.Errorf("unset env must keep the base, got %d", got)
		}
	})
}

// TestCacheEnvelopeReplaysAllMetadataFields: a cache hit must replay EVERY metadata field, not a
// spot-checked subset — a slimmed envelope would silently zero ChunkStrategy/CombineTruncated/
// coverage on exactly the most repetitive (cached) traffic.
func TestCacheEnvelopeReplaysAllMetadataFields(t *testing.T) {
	full := ResultTrimMetadata{
		OriginalBytes: 458000, TrimmedBytes: 4096, Method: "distill_mapreduce",
		FieldsKept: 12, FieldsDropped: 34, BackfilledCount: 2, ThresholdSkipped: 3,
		Keywords: []string{"error", "timeout"}, MatchedPaths: []string{"streams[0].line"},
		BudgetAllocated: 16384, Degenerate: false, KeptRatio: 0.41,
		SourceCoverageRatio: 0.3, LLMInputBytes: 131072, SegmentsAnalyzed: 3, SegmentsTotal: 16,
		PartialCoverage: true, CombineTruncated: true, ChunkStrategy: "bytes", ContentLost: true,
	}
	cache := newMapDigestCache()
	inner := &metaProcessor{out: "DISTILLED", meta: full}
	p := NewCachingProcessor(inner, cache, time.Minute, 10, "salt", nil)
	sc := ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find errors"}
	input := strings.Repeat("a", 50)

	ctx1, m1 := WithTrimMetadataCapture(context.Background())
	p.ProcessForPrompt(ctx1, input, 4096, sc)
	ctx2, m2 := WithTrimMetadataCapture(context.Background())
	p.ProcessForPrompt(ctx2, input, 4096, sc)

	if inner.calls != 1 {
		t.Fatalf("second call must be a cache hit, inner.calls=%d", inner.calls)
	}
	if !reflect.DeepEqual(*m1, *m2) {
		t.Errorf("cache hit must replay ALL metadata fields:\n miss: %+v\n hit:  %+v", *m1, *m2)
	}

	// ContentLost must survive the envelope ON ITS OWN: with any sibling loss flag set,
	// captureTrimMetadata's superset normalization re-derives it on the hit path and masks a
	// dropped field (mutation-verified during review). This fixture is the single-call
	// lossy-stage-1 shape — ContentLost true, Degenerate/PartialCoverage/CombineTruncated all
	// false — where only the stored field itself can carry the signal.
	lossOnly := ResultTrimMetadata{
		OriginalBytes: 50000, TrimmedBytes: 4096, Method: "distill",
		SourceCoverageRatio: 0.9, SegmentsAnalyzed: 1, SegmentsTotal: 1,
		ContentLost: true, // no sibling flags — the envelope must carry this bit verbatim
	}
	cache2 := newMapDigestCache()
	inner2 := &metaProcessor{out: "DISTILLED2", meta: lossOnly}
	p2 := NewCachingProcessor(inner2, cache2, time.Minute, 10, "salt", nil)
	ctx3, _ := WithTrimMetadataCapture(context.Background())
	p2.ProcessForPrompt(ctx3, input, 4096, sc)
	ctx4, m4 := WithTrimMetadataCapture(context.Background())
	p2.ProcessForPrompt(ctx4, input, 4096, sc)
	if inner2.calls != 1 {
		t.Fatalf("second call must be a cache hit, inner2.calls=%d", inner2.calls)
	}
	if !m4.ContentLost {
		t.Error("a cache hit must replay ContentLost=true even when no sibling loss flag can re-derive it")
	}
}

// TestMapReduce_ReduceSuccessReplacesCombined: the coverage profile showed the SUCCESSFUL reduce
// arm (combined = reduced) had never executed in any test — only reduce-failure and
// reduce-too-big. Pin it: the reduce output replaces the joined extracts, nothing is truncated,
// and the result stays cacheable. failAfterAI's afterOut makes the reduce call (len(pre)+1, with
// MapConcurrency=1) distinguishable from the map calls.
func TestMapReduce_ReduceSuccessReplacesCombined(t *testing.T) {
	payload := mapReduceTestArray(80)
	pre, _, _, _ := chunkWholeUnits(payload, 100)
	mockAI := &failAfterAI{succeedUntil: len(pre), out: "FINDING " + strings.Repeat("y", 60), afterOut: "REDUCED_SUMMARY"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 100, // floors to 256
		Model: "fast", ModelContextTokens: 2500, MapConcurrency: 1, // sequential: reduce is call len(pre)+1
		MapReduceThresholdBytes: 100,
		CompactionDeadline:      5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, nonCacheable := withNonCacheableCapture(context.Background())
	mctx, meta := WithTrimMetadataCapture(ctx)
	out := d.ProcessForPrompt(mctx, payload, 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find"})

	if !strings.Contains(out, "REDUCED_SUMMARY") {
		t.Errorf("expected the reduce output to replace the combined extracts, got: %.200q", out)
	}
	if strings.Contains(out, "[findings truncated:") {
		t.Errorf("a successful reduce must not carry the combine-truncation note: %.200q", out)
	}
	if *nonCacheable {
		t.Error("a fully-successful map-reduce with a successful reduce must remain cacheable")
	}
	if meta.CombineTruncated {
		t.Errorf("CombineTruncated must be false on a successful reduce: %+v", *meta)
	}
}

// TestWorstChunkStrategy_Table covers the tie/a-wins arms the profile showed unexercised.
func TestWorstChunkStrategy_Table(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"wrapper", "bytes", "bytes"},  // b more degraded
		{"bytes", "wrapper", "bytes"},  // a more degraded (a wins)
		{"array", "wrapper", "array"},  // structural tie → a
		{"lines", "bytes", "bytes"},    // b worse
		{"bytes", "lines", "bytes"},    // a worse
		{"single", "single", "single"}, // identical
		{"array", "", "array"},         // unknown ranks 0 → a
	}
	for _, c := range cases {
		if got := worstChunkStrategy(c.a, c.b); got != c.want {
			t.Errorf("worstChunkStrategy(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

// TestChunkStrategy_LinesStamped: a newline-delimited non-JSON payload routed to map-reduce
// stamps chunk_strategy="lines". The other strategies are pinned elsewhere: "bytes"/"wrapper" in
// TestChunkStrategy_Stamped, "single" in TestMapReduce_SingleChunkRunsOneExtract, "array" in
// TestLLMDistiller_MapReduce_FansOut.
func TestChunkStrategy_LinesStamped(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "log line with some content number "+strings.Repeat("x", 10))
	}
	payload := strings.Join(lines, "\n")

	mockAI := &countingAI{out: "EXTRACT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, meta := WithTrimMetadataCapture(context.Background())
	d.ProcessForPrompt(ctx, payload, 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})

	if meta.ChunkStrategy != "lines" {
		t.Errorf("expected chunk_strategy=lines for newline-delimited non-JSON, got %q", meta.ChunkStrategy)
	}
}

// --- Tier-1 review fixes (repro tests written BEFORE the fixes; 2026-07-18) ---

// TestSingleCall_EmptyLLMResponseFallsOpenNotCached: a 200-with-empty-content provider response
// on the SINGLE-CALL path must behave like an error — structural fallback + non-cacheable — as
// every map-reduce call already does. Pre-fix it was returned as a "successful" empty distill,
// stamped lossless, and cached for the TTL.
func TestSingleCall_EmptyLLMResponseFallsOpenNotCached(t *testing.T) {
	// Table-driven over exactly-empty AND whitespace-only (review pin: TrimSpace is
	// load-bearing — a "\n"-only 200 previously shipped as a successful lossless distill).
	for _, empty := range []string{"", " \n\t "} {
		mockAI := &countingAI{out: empty, usage: core.TokenUsage{PromptTokens: 42}}
		config := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 4,
			CompactionDeadline: 5 * time.Second,
		}
		d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

		ctx, nonCacheable := withNonCacheableCapture(context.Background())
		ctx, acc := core.WithTokenUsageAccumulator(ctx)
		out := d.ProcessForPrompt(ctx, mapReduceTestArray(30), 300, // > maxBytes → distill path
			ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

		if strings.TrimSpace(out) == "" {
			t.Errorf("out=%q: an empty LLM response must fall open to the structural floor, not ship empty", empty)
		}
		if !*nonCacheable {
			t.Errorf("out=%q: an empty LLM response must be non-cacheable (transient provider edge)", empty)
		}
		// The empty call still BILLED its prompt tokens — usage is recorded before the guard
		// (review pin: moving RecordTokenUsage back below the guard hides a content-filter
		// burst from cost telemetry).
		_, byPhase := acc.Snapshot()
		if byPhase["distillation"].PromptTokens != 42 {
			t.Errorf("out=%q: expected the billed empty call in usage accounting, got %+v", empty, byPhase)
		}
	}
}

// TestSingleCall_FailureFallbackUsesFlooredTarget: the single-call LLM-failure fallback must
// floor at targetSize (>= minDistillTargetSize) like every map-reduce fallback — not the raw
// per-result maxBytes, which can be tiny (or zero) and re-create the budget-0 near-empty output
// the floor exists to prevent, on exactly the runs that also lost their LLM pass.
func TestSingleCall_FailureFallbackUsesFlooredTarget(t *testing.T) {
	mockAI := &failAfterAI{succeedUntil: 0} // every call errors
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	out := d.ProcessForPrompt(context.Background(), mapReduceTestArray(40), 50, // tiny budget
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

	// The floored fallback must deliver substantive content (~minDistillTargetSize), not a
	// ~50-byte sliver that is mostly annotation.
	if len(stripResultAnnotation(out)) < 100 {
		t.Errorf("failure fallback must floor at targetSize, got %d body bytes: %q",
			len(stripResultAnnotation(out)), out)
	}
}

// TestNormalize_BackfillsDistillThreshold: a minimal config (Enabled+Model only) previously had
// threshold 0 → EVERY result, however tiny, took a paid LLM distill call. Same rationale as the
// ModelContextTokens backfill.
func TestNormalize_BackfillsDistillThreshold(t *testing.T) {
	got := normalizeResultDistillConfig(ResultDistillConfig{}, nil)
	if got.DistillThreshold != defaultDistillThreshold {
		t.Errorf("expected DistillThreshold backfill to %d, got %d", defaultDistillThreshold, got.DistillThreshold)
	}
	kept := normalizeResultDistillConfig(ResultDistillConfig{DistillThreshold: 42}, nil)
	if kept.DistillThreshold != 42 {
		t.Errorf("an explicit threshold must be preserved, got %d", kept.DistillThreshold)
	}
}

// TestDeadlineDisabledWarnsOncePerStack: CompactionDeadline==0 is a DOCUMENTED opt-out ("0
// disables the deadline"), so it is never backfilled — but an unbounded distill config can fan
// out with no wall-clock cap, so the STACK assemblers (factory / Layer-2 helper) warn exactly
// once. The warn deliberately does NOT live in normalizeResultDistillConfig, which runs per
// distiller construction (a factory builds two: synthesis + continuation → double warns).
func TestDeadlineDisabledWarnsOncePerStack(t *testing.T) {
	// Normalization itself must stay silent about the deadline and must not backfill it.
	log := &warnCapturingLogger{}
	got := normalizeResultDistillConfig(ResultDistillConfig{}, log)
	if got.CompactionDeadline != 0 {
		t.Errorf("the documented 0=disabled semantic must be preserved (no backfill), got %v", got.CompactionDeadline)
	}
	if log.normalizationWarnings() != 0 {
		t.Errorf("normalize must not warn about the deadline (stack assemblers do), got %d", log.normalizationWarnings())
	}
	// The Layer-2 helper warns once for a deadline-less config (distinct advisory operation,
	// never conflated with normalization warnings)…
	helper := &warnCapturingLogger{}
	BuildDistillationEnabledResultProcessor(ResultDistillConfig{Enabled: true, Model: "fast"},
		&countingAI{}, nil, helper)
	if helper.advisoryWarnings() != 1 {
		t.Errorf("Layer-2 helper: expected one advisory deadline warning, got %d", helper.advisoryWarnings())
	}
	if helper.normalizationWarnings() != 0 {
		t.Errorf("the advisory must not use the normalization operation, got %d normalization warns", helper.normalizationWarnings())
	}
	// …and stays quiet when a deadline is set.
	quiet := &warnCapturingLogger{}
	BuildDistillationEnabledResultProcessor(ResultDistillConfig{Enabled: true, Model: "fast", CompactionDeadline: 45 * time.Second},
		&countingAI{}, nil, quiet)
	if quiet.advisoryWarnings() != 0 {
		t.Errorf("a set deadline must not warn, got %d", quiet.advisoryWarnings())
	}
}

// TestLayer2Helper_EffectiveMinBytes pins the raw-vs-effective threshold alignment: a minimal
// config's zero DistillThreshold must yield a cache wrapper gating at the BACKFILLED 16 KB, not
// minBytes=0 (which would cache every sub-threshold passthrough).
func TestLayer2Helper_EffectiveMinBytes(t *testing.T) {
	p := BuildDistillationEnabledResultProcessor(ResultDistillConfig{Enabled: true, Model: "fast", CompactionDeadline: 45 * time.Second},
		&countingAI{}, newMapDigestCache(), nil)
	cp, ok := p.(*cachingProcessor)
	if !ok {
		t.Fatalf("expected *cachingProcessor, got %T", p)
	}
	if cp.minBytes != defaultDistillThreshold {
		t.Errorf("cache minBytes = %d, want the effective backfilled threshold %d", cp.minBytes, defaultDistillThreshold)
	}
}

// TestDistillKeySalt_KeysPreserveKeys: the stage-1 pre-filter's PreserveKeys change what the LLM
// sees on the cacheable single-call path, so they must be in the salt — two orchestrators
// sharing a Redis cache with different PreserveKeys previously computed identical keys and could
// serve each other's differently-filtered outputs for the TTL.
func TestDistillKeySalt_KeysPreserveKeys(t *testing.T) {
	cfg := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, ModelContextTokens: 150000}
	none := distillKeySalt(cfg, nil, nil)
	withKeys := distillKeySalt(cfg, nil, []string{"account_id"})
	if none == withKeys {
		t.Error("PreserveKeys must change the salt (they change what the LLM sees)")
	}
	// Order-insensitive: same set → same behavior → same key (no cache fragmentation).
	ab := distillKeySalt(cfg, nil, []string{"a", "b"})
	ba := distillKeySalt(cfg, nil, []string{"b", "a"})
	if ab != ba {
		t.Error("PreserveKeys order must not fragment the cache (same set, same salt)")
	}
}

// TestModelTruncationNoteNotPeeled pins a DELIBERATE exemption (review-reversed): the
// model-emitted "[truncated: …]" form (system prompt instruction #5) is NOT in
// annotationPrefixes, because real tools emit their own "[truncated: …]" trailers and the
// registry peel runs on tool-derived text — peeling would silently delete a TOOL's truncation
// signal and present visibly-truncated source as complete. The cost is latent-only (a distiller
// wired as an agent-input processor fails open with a warn). See the registry doc.
func TestModelTruncationNoteNotPeeled(t *testing.T) {
	body := "connection log line one"
	toolTrailer := "\n[truncated: output exceeded 4096 bytes]"
	if got := stripResultAnnotation(body + toolTrailer); got != body+toolTrailer {
		t.Errorf("a tool's own [truncated: trailer must survive the peel, got %q", got)
	}
}

// TestPartialNote_NeutralWording: completed<total is also reached on provider failures with the
// deadline never firing, so the note must not claim a cause ("within the time budget") it cannot
// know — disclosures state only what happened.
func TestPartialNote_NeutralWording(t *testing.T) {
	note := partialSegmentsDisclosure(3, 16)
	if strings.Contains(note, "time budget") {
		t.Errorf("the partial note must not attribute a cause it cannot know: %q", note)
	}
	if !strings.Contains(note, "3 of 16 segments analyzed") || !strings.Contains(note, "UNKNOWN") {
		t.Errorf("the partial note must keep the factual N-of-M + UNKNOWN safeguard: %q", note)
	}
	if strings.Count(note, "\n") != 1 || !strings.HasSuffix(note, "]") {
		t.Errorf("shape contract (one line, ends ']') violated: %q", note)
	}
}

// TestCombineTruncatedReason_SpanAttribute: transient reduce failure and deterministic
// context-overflow both end as CombineTruncated; the canary's reduce-fallback measurement needs
// them distinguishable in telemetry.
func TestCombineTruncatedReason_SpanAttribute(t *testing.T) {
	cases := []struct {
		name          string
		contextTokens int
		wantReason    string
	}{
		// MCT 1500: the reduce gate is satisfiable, the reduce call is attempted and fails
		// (failAfterAI) → transient.
		{"transient reduce failure", 1500, "reduce_failed"},
		// MCT 100: the gate is unsatisfiable → deterministic truncation without an attempt.
		{"deterministic over-context", 100, "over_context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := setupExecutorTestTracer(t)
			payload := mapReduceTestArray(40)
			pre, _, _, _ := chunkWholeUnits(payload, 100)
			mockAI := &failAfterAI{succeedUntil: len(pre), out: "FINDING " + strings.Repeat("x", 60)}
			config := ResultDistillConfig{
				Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 100,
				Model: "fast", ModelContextTokens: tc.contextTokens, MapConcurrency: 1,
				MapReduceThresholdBytes: 100,
				CompactionDeadline:      5 * time.Second,
			}
			d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

			tracer := otel.Tracer("phase17-test")
			ctx, span := tracer.Start(context.Background(), "combine-reason")
			d.ProcessForPrompt(ctx, payload, 2000,
				ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find"})
			span.End()

			var got string
			for _, s := range recorder.Ended() {
				for _, ev := range s.Events() {
					if ev.Name == "result_distill.mapreduce_complete" {
						for _, kv := range ev.Attributes {
							if string(kv.Key) == "combine_truncated_reason" {
								got = kv.Value.AsString()
							}
						}
					}
				}
			}
			if got != tc.wantReason {
				t.Errorf("combine_truncated_reason = %q, want %q", got, tc.wantReason)
			}
		})
	}
}

// nilRespAI models the provider edge both guards defend against: a nil *AIResponse with a nil
// error (middleware/stop-token edge). No other mock in the package can produce it.
type nilRespAI struct{}

func (nilRespAI) GenerateResponse(_ context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	return nil, nil
}
func (nilRespAI) StreamResponse(_ context.Context, _ string, _ *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	return nil, nil
}

// TestExtractChunk_NilAndWhitespaceGuard pins extractChunk's empty/whitespace/nil-response guard
// (review mutation check: disabling it left the whole suite green):
//   - a nil response with nil error must become an error, never a worker-goroutine panic;
//   - whitespace-only chunk extracts count as FAILED segments → the partial note fires and raw
//     "\n" never ships to synthesis (the validated pre-fix pathology).
func TestExtractChunk_NilAndWhitespaceGuard(t *testing.T) {
	t.Run("nil response with nil error becomes an error, not a panic", func(t *testing.T) {
		config := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 50, MapConcurrency: 2, CompactionDeadline: 5 * time.Second,
		}
		d := NewLLMDistiller(nilRespAI{}, config, NewStructuralTrimmer(nil, nil), nil)
		out, err := d.extractChunk(context.Background(), "chunk data", 256,
			ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"}, "test call")
		if err == nil || out != "" {
			t.Errorf("nil response must convert to an error, got out=%q err=%v", out, err)
		}
		// And through the single-call path: structural fallback + non-cacheable, no panic.
		ctx, nonCacheable := withNonCacheableCapture(context.Background())
		single := NewLLMDistiller(nilRespAI{}, ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 1_000_000, CompactionDeadline: 5 * time.Second,
		}, NewStructuralTrimmer(nil, nil), nil)
		if out := single.ProcessForPrompt(ctx, mapReduceTestArray(30), 300,
			ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"}); strings.TrimSpace(out) == "" {
			t.Error("single-call nil response must fall open to the structural floor")
		}
		if !*nonCacheable {
			t.Error("single-call nil response must be non-cacheable")
		}
	})
	t.Run("whitespace-only chunk extracts count as failed segments", func(t *testing.T) {
		payload := mapReduceTestArray(16)
		pre, _, _, _ := chunkWholeUnits(payload, 100)
		if len(pre) < 3 {
			t.Fatalf("fixture needs >=3 chunks, got %d", len(pre))
		}
		// First two chunks succeed with substance; the rest return "\n" — previously counted
		// as successful segments joining raw whitespace into the body.
		mockAI := &failAfterAI{succeedUntil: 2, out: "FINDING " + strings.Repeat("x", 60), afterOut: "\n"}
		config := ResultDistillConfig{
			Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
			Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 1,
			MapReduceThresholdBytes: 100, CompactionDeadline: 5 * time.Second,
		}
		d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)
		ctx, meta := WithTrimMetadataCapture(context.Background())
		out := d.ProcessForPrompt(ctx, payload, 2000,
			ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find"})
		if !strings.Contains(out, partialDisclosureMarker) {
			t.Errorf("whitespace segments must surface as a partial run, got: %q", out)
		}
		if want := fmt.Sprintf("2 of %d segments analyzed", len(pre)); !strings.Contains(out, want) {
			t.Errorf("expected %q in the partial note, got: %q", want, out)
		}
		if meta.SegmentsAnalyzed != 2 || !meta.PartialCoverage {
			t.Errorf("metadata must count whitespace extracts as failed: %+v", *meta)
		}
	})
}

// TestSingleChunk_EmptyExtractRecordsUsage pins usage-before-guard on the MAP-REDUCE side: an
// empty single-chunk extract still bills its prompt tokens, and that must reach the
// distillation_mapreduce phase accounting before the guard converts it to a failure.
func TestSingleChunk_EmptyExtractRecordsUsage(t *testing.T) {
	mockAI := &countingAI{out: "", usage: core.TokenUsage{PromptTokens: 17}}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 10, MapConcurrency: 4, CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, acc := core.WithTokenUsageAccumulator(context.Background())
	d.ProcessForPrompt(ctx, mapReduceTestArray(4), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

	_, byPhase := acc.Snapshot()
	if byPhase["distillation_mapreduce"].PromptTokens != 17 {
		t.Errorf("expected the billed empty extract in usage accounting, got %+v", byPhase)
	}
}

// TestFactory_EffectiveMinBytesAndAdvisoryOnce pins the CreateOrchestrator-path twins of the
// Layer-2 assertions (review mutation checks: both factory arms were revert-green):
//   - the cache wrapper's minBytes is the EFFECTIVE backfilled threshold for a zero-threshold
//     config, and
//   - the deadline-opt-out advisory fires exactly ONCE per orchestrator (not twice, despite two
//     distiller constructions), with the distinct advisory operation.
func TestFactory_EffectiveMinBytesAndAdvisoryOnce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ResultDistill.DistillThreshold = 0   // hand-built-config shape: unset threshold
	cfg.ResultDistill.CompactionDeadline = 0 // documented opt-out
	log := &warnCapturingLogger{}
	deps := OrchestratorDependencies{
		Discovery:    NewMockDiscovery(),
		AIClient:     NewMockAIClient(),
		DistillCache: &core.MockDigestCache{},
		Logger:       log,
	}
	orch, err := CreateOrchestrator(cfg, deps)
	if err != nil {
		t.Fatalf("CreateOrchestrator: %v", err)
	}
	cp, ok := orch.resultProcessor.(*cachingProcessor)
	if !ok {
		t.Fatalf("expected *cachingProcessor, got %T", orch.resultProcessor)
	}
	if cp.minBytes != defaultDistillThreshold {
		t.Errorf("factory cache minBytes = %d, want effective %d", cp.minBytes, defaultDistillThreshold)
	}
	if got := log.advisoryWarnings(); got != 1 {
		t.Errorf("deadline advisory must fire exactly once per orchestrator, got %d", got)
	}

	// With a deadline set: no advisory at all.
	cfg2 := DefaultConfig()
	log2 := &warnCapturingLogger{}
	deps2 := OrchestratorDependencies{
		Discovery: NewMockDiscovery(), AIClient: NewMockAIClient(), Logger: log2,
	}
	if _, err := CreateOrchestrator(cfg2, deps2); err != nil {
		t.Fatalf("CreateOrchestrator: %v", err)
	}
	if got := log2.advisoryWarnings(); got != 0 {
		t.Errorf("a set deadline must not produce the advisory, got %d", got)
	}
}

// TestSingleCall_FailureFallbackKeepsGenerousBudget pins the other direction of the fallback
// budget rule (review-caught regression: flooring ALONE capped a 16 KB allocation at
// ~TargetSize, quadrupling content loss on failure bursts): with maxBytes > targetSize the
// fallback must use the full maxBytes, not the floor.
func TestSingleCall_FailureFallbackKeepsGenerousBudget(t *testing.T) {
	mockAI := &failAfterAI{succeedUntil: 0} // every call errors
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100000, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 1_000_000, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	// ~19 KB payload, generous 16 KB budget: the fallback must deliver well beyond the
	// 2000-byte targetSize cap the regression imposed.
	payload := mapReduceTestArray(800)
	out := d.ProcessForPrompt(context.Background(), payload, 16384,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})

	if body := len(stripResultAnnotation(out)); body <= 3000 {
		t.Errorf("generous-budget fallback must not be capped at targetSize, got %d body bytes", body)
	}
}
