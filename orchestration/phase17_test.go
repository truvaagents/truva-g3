package orchestration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
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
		PreFilterBudget: 131072, MapReduceThresholdBytes: 1000, // 1000 < 128 KB → normalized to 0
	}, NewStructuralTrimmer(nil, nil), direct)
	if direct.normalizationWarnings() != 1 {
		t.Errorf("direct NewLLMDistiller: expected one normalization warning, got %d", direct.normalizationWarnings())
	}

	// Layer-2 helper construction path.
	helper := &warnCapturingLogger{}
	BuildDistillationEnabledResultProcessor(ResultDistillConfig{
		PreFilterBudget: 131072, MapReduceThresholdBytes: 1000,
	}, &countingAI{}, nil, helper)
	if helper.normalizationWarnings() != 1 {
		t.Errorf("Layer-2 helper: expected one normalization warning, got %d", helper.normalizationWarnings())
	}

	// A valid threshold (>= PreFilterBudget) must NOT warn.
	valid := &warnCapturingLogger{}
	NewLLMDistiller(&countingAI{}, ResultDistillConfig{
		PreFilterBudget: 131072, MapReduceThresholdBytes: 262144, // 256 KB >= 128 KB
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
		PreFilterBudget: 0, MapReduceThresholdBytes: 1000,
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

	baseSalt := distillKeySalt(base, nil)
	if distillKeySalt(base, nil) != baseSalt {
		t.Error("salt must be stable for an identical config")
	}
	if distillKeySalt(ctxChanged, nil) == baseSalt {
		t.Error("a ModelContextTokens change must change the salt (routing/reduce-gate behavior changed)")
	}
	if distillKeySalt(thrChanged, nil) == baseSalt {
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
	if distillKeySalt(raw, nil) != distillKeySalt(normalized, nil) {
		t.Error("a raw config must salt identically to its normalized form (same runtime behavior → same key)")
	}
	unsetCtx := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072}
	explicitDefault := ResultDistillConfig{Model: "fast", TargetSize: 4096, PreFilterBudget: 131072, ModelContextTokens: defaultModelContextTokens}
	if distillKeySalt(unsetCtx, nil) != distillKeySalt(explicitDefault, nil) {
		t.Error("an unset ModelContextTokens must salt as its backfilled default")
	}
}
