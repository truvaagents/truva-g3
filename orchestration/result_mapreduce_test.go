package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// countingAI records how many times GenerateResponse ran and returns a fixed extract.
type countingAI struct {
	mu  sync.Mutex
	n   int
	out string
}

func (c *countingAI) GenerateResponse(_ context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return &core.AIResponse{Content: c.out, Usage: core.TokenUsage{}}, nil
}

func (c *countingAI) StreamResponse(ctx context.Context, p string, o *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	return c.GenerateResponse(ctx, p, o)
}

func (c *countingAI) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

func mapReduceTestArray(n int) string {
	recs := make([]interface{}, n)
	for i := range recs {
		recs[i] = map[string]interface{}{"id": fmt.Sprintf("r%02d", i), "v": "data"}
	}
	raw, _ := json.Marshal(recs)
	return string(raw)
}

// TestLLMDistiller_MapReduce_FansOut verifies an over-context result is chunked and
// each chunk extracted (multiple LLM calls), with the extracts combined.
func TestLLMDistiller_MapReduce_FansOut(t *testing.T) {
	mockAI := &countingAI{out: "EXTRACT"}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	raw := mapReduceTestArray(20) // ~440B → ~146 tok > 50 → map-reduce; chunks at 100B

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := distiller.ProcessForPrompt(ctx, raw, 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list",
	})

	if mockAI.count() < 2 {
		t.Errorf("expected multiple chunk extractions (map-reduce fan-out), got %d", mockAI.count())
	}
	if !strings.Contains(out, "EXTRACT") {
		t.Errorf("expected combined chunk extracts in output, got: %.200s", out)
	}
	if meta.Method != "distill_mapreduce" {
		t.Errorf("expected Method=distill_mapreduce, got %q", meta.Method)
	}
}

// TestLLMDistiller_MapReduce_CollapsesEmptyChunks verifies that when every chunk finds nothing
// (each returns the noMatchSentinel), the map-reduce output collapses to a SINGLE sentinel rather
// than N copies. Regression for orch-1782176022971457913 (a 1.99 MB query → sentinel ×16). (Phase 12)
func TestLLMDistiller_MapReduce_CollapsesEmptyChunks(t *testing.T) {
	mockAI := &countingAI{out: noMatchSentinel}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	out := distiller.ProcessForPrompt(context.Background(), mapReduceTestArray(20), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find 5xx errors",
	})

	if mockAI.count() < 2 {
		t.Fatalf("expected map-reduce fan-out, got %d calls", mockAI.count())
	}
	if n := strings.Count(out, noMatchSentinel); n != 1 {
		t.Errorf("expected the empty sentinel exactly once after consolidation, got %d in: %.200q", n, out)
	}
	if strings.TrimSpace(out) != noMatchSentinel {
		t.Errorf("expected output to collapse to a single sentinel, got: %.200q", out)
	}
}

// oneHitAI returns `hit` on the first call and the empty sentinel on every other call — models a
// run where exactly one chunk has a relevant match and the rest find nothing.
type oneHitAI struct {
	mu  sync.Mutex
	n   int
	hit string
}

func (a *oneHitAI) GenerateResponse(_ context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	a.mu.Lock()
	a.n++
	idx := a.n
	a.mu.Unlock()
	if idx == 1 {
		return &core.AIResponse{Content: a.hit}, nil
	}
	return &core.AIResponse{Content: noMatchSentinel}, nil
}

func (a *oneHitAI) StreamResponse(ctx context.Context, p string, o *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	return a.GenerateResponse(ctx, p, o)
}

// TestLLMDistiller_MapReduce_DropsEmptiesKeepsHit verifies consolidation drops the empty-sentinel
// chunks but preserves the one substantive extract — the real hit is not buried among empties. (Phase 12)
func TestLLMDistiller_MapReduce_DropsEmptiesKeepsHit(t *testing.T) {
	mockAI := &oneHitAI{hit: "REAL_ERROR_XYZ"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	out := distiller.ProcessForPrompt(context.Background(), mapReduceTestArray(20), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find errors",
	})

	if !strings.Contains(out, "REAL_ERROR_XYZ") {
		t.Errorf("expected the one real hit to survive consolidation, got: %.200q", out)
	}
	if strings.Contains(out, noMatchSentinel) {
		t.Errorf("expected empty-chunk sentinels to be dropped, got: %.200q", out)
	}
}

// TestLLMDistiller_MapReduce_RecordsDebugInteractions verifies the map-reduce path records typed
// result_distillation interactions (LLM-Debug parity with the single-call path). (Phase 12)
func TestLLMDistiller_MapReduce_RecordsDebugInteractions(t *testing.T) {
	mockAI := &countingAI{out: "EXTRACT"}
	store := &mockLLMDebugStore{}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)
	distiller.SetLLMDebugStore(store)

	ctx := telemetry.WithBaggage(context.Background(), "request_id", "req-mr")
	_ = distiller.ProcessForPrompt(ctx, mapReduceTestArray(20), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "obs-agent", Instruction: "list",
	})
	distiller.Shutdown() // wait for async debug recordings

	if len(store.interactions) < 2 {
		t.Fatalf("expected multiple map-reduce LLM-debug interactions, got %d", len(store.interactions))
	}
	for _, it := range store.interactions {
		if it.Type != "result_distillation" {
			t.Errorf("expected type result_distillation, got %q", it.Type)
		}
		if !strings.Contains(it.CallDescription, "map-reduce") {
			t.Errorf("expected a map-reduce CallDescription, got %q", it.CallDescription)
		}
	}
}

// partialBlockAI lets exactly one call succeed and blocks the rest until the context is
// cancelled — to exercise partial-on-timeout in the map-reduce path.
type partialBlockAI struct {
	mu sync.Mutex
	n  int
}

func (p *partialBlockAI) GenerateResponse(ctx context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	p.mu.Lock()
	p.n++
	idx := p.n
	p.mu.Unlock()
	if idx == 1 {
		return &core.AIResponse{Content: "FIRST"}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *partialBlockAI) StreamResponse(ctx context.Context, pr string, o *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	return p.GenerateResponse(ctx, pr, o)
}

// TestLLMDistiller_MapReduce_PartialOnTimeout verifies that when the deadline kills some
// chunks, the completed chunks' LLM output is returned with an honest "[partial:" note.
func TestLLMDistiller_MapReduce_PartialOnTimeout(t *testing.T) {
	mockAI := &partialBlockAI{}
	trimmer := NewStructuralTrimmer(nil, nil)
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 1, // sequential → deterministic
		CompactionDeadline: 100 * time.Millisecond,
	}
	distiller := NewLLMDistiller(mockAI, config, trimmer, nil)

	raw := mapReduceTestArray(20) // chunks into several ≤100B chunks

	start := time.Now()
	out := distiller.ProcessForPrompt(context.Background(), raw, 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list",
	})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("expected return near the %v deadline, took %v", config.CompactionDeadline, elapsed)
	}
	if !strings.Contains(out, "FIRST") {
		t.Errorf("expected the completed chunk's content, got: %.200s", out)
	}
	if !strings.Contains(out, "[partial:") {
		t.Errorf("expected an honest [partial: …] disclosure, got: %.200s", out)
	}
}

func TestChunkWholeUnits(t *testing.T) {
	t.Run("small input returns single chunk", func(t *testing.T) {
		got := chunkWholeUnits("hello", 100)
		if len(got) != 1 || got[0] != "hello" {
			t.Errorf("expected single chunk, got %v", got)
		}
	})

	t.Run("json array splits at element boundaries into valid arrays", func(t *testing.T) {
		raw := mapReduceTestArray(20)
		chunks := chunkWholeUnits(raw, 100)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple chunks, got %d", len(chunks))
		}
		for i, c := range chunks {
			var arr []interface{}
			if err := json.Unmarshal([]byte(c), &arr); err != nil {
				t.Errorf("chunk %d is not a valid JSON array (record was split): %v", i, err)
			}
		}
	})

	t.Run("object with dominant array is chunked by that array", func(t *testing.T) {
		recs := make([]interface{}, 20)
		for i := range recs {
			recs[i] = map[string]interface{}{"line": fmt.Sprintf("entry-%02d-with-some-content", i)}
		}
		raw, _ := json.Marshal(map[string]interface{}{"streams": recs})
		chunks := chunkWholeUnits(string(raw), 100)
		if len(chunks) < 2 {
			t.Fatalf("expected the dominant array to be chunked, got %d chunks", len(chunks))
		}
	})

	t.Run("newline-delimited records split on line boundaries", func(t *testing.T) {
		var lines []string
		for i := 0; i < 50; i++ {
			lines = append(lines, fmt.Sprintf("log line number %d with content", i))
		}
		raw := strings.Join(lines, "\n")
		chunks := chunkWholeUnits(raw, 100)
		if len(chunks) < 2 {
			t.Fatalf("expected multiple line chunks, got %d", len(chunks))
		}
		// No chunk should start or end mid-line beyond the originals (rejoin must match).
		if strings.Join(chunks, "\n") != raw {
			t.Error("line chunks must rejoin to the original without losing content")
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	// Delegates to HeuristicTokenCounter: ceil(300/3.5) = 86.
	if got := estimateTokens(strings.Repeat("x", 300)); got != 86 {
		t.Errorf("estimateTokens(300 bytes) = %d, want 86", got)
	}
}

// ignoresCtxAI returns immediately on the first call and then blocks IGNORING context
// cancellation, to prove the map-reduce deadline is enforced by the collector (not by the
// AI client honoring ctx).
type ignoresCtxAI struct {
	mu      sync.Mutex
	n       int
	release chan struct{}
}

func (a *ignoresCtxAI) GenerateResponse(_ context.Context, _ string, _ *core.AIOptions) (*core.AIResponse, error) {
	a.mu.Lock()
	a.n++
	idx := a.n
	a.mu.Unlock()
	if idx == 1 {
		return &core.AIResponse{Content: "DONE0"}, nil
	}
	<-a.release // block, ignoring ctx cancellation
	return &core.AIResponse{Content: "LATE"}, nil
}

func (a *ignoresCtxAI) StreamResponse(ctx context.Context, p string, o *core.AIOptions, _ func(string)) (*core.AIResponse, error) {
	return a.GenerateResponse(ctx, p, o)
}

// TestLLMDistiller_MapReduce_HardDeadline verifies the caller returns near the deadline
// even when a chunk's AI call blocks past it ignoring cancellation (Finding 2).
func TestLLMDistiller_MapReduce_HardDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release) // let the blocked straggler exit after the test

	mockAI := &ignoresCtxAI{release: release}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 1,
		CompactionDeadline: 100 * time.Millisecond,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	start := time.Now()
	out := distiller.ProcessForPrompt(context.Background(), mapReduceTestArray(20), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list"})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("expected a hard return near the %v deadline despite a blocking chunk, took %v",
			config.CompactionDeadline, elapsed)
	}
	if !strings.Contains(out, "DONE0") {
		t.Errorf("expected the completed chunk's content, got: %.200s", out)
	}
	if !strings.Contains(out, "[partial:") {
		t.Errorf("expected an honest partial disclosure, got: %.200s", out)
	}
}

// TestChunkWholeUnits_SplitsOversizedElement guards Finding 4: a single array element
// larger than chunkBytes is split, not emitted as one over-budget chunk.
func TestChunkWholeUnits_SplitsOversizedElement(t *testing.T) {
	huge := map[string]interface{}{"blob": strings.Repeat("x", 500)}
	raw, _ := json.Marshal([]interface{}{huge}) // one element ~510B > chunkBytes
	chunks := chunkWholeUnits(string(raw), 100)
	if len(chunks) < 2 {
		t.Fatalf("expected an oversized single element to split into multiple chunks, got %d", len(chunks))
	}
}

// TestLLMDistiller_MapReduce_SingleHugeRecordReachesLLM guards Finding 4: a top-level
// array holding one record larger than the chunk budget must still reach the LLM path
// rather than collapsing to total<=1 and structural-trimming.
func TestLLMDistiller_MapReduce_SingleHugeRecordReachesLLM(t *testing.T) {
	mockAI := &countingAI{out: "EXTRACT"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	one := map[string]interface{}{"blob": strings.Repeat("y", 600)} // ~615B > PreFilterBudget
	raw, _ := json.Marshal([]interface{}{one})

	out := distiller.ProcessForPrompt(context.Background(), string(raw), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "x"})

	if mockAI.count() == 0 {
		t.Errorf("a single huge record must reach the LLM, not skip to structural (AI calls=0)")
	}
	if !strings.Contains(out, "EXTRACT") {
		t.Errorf("expected the LLM extract in the output, got: %.200s", out)
	}
}

// TestChunkByLines_SplitsOversizedLine guards the follow-up finding: a single
// newline-delimited record larger than chunkBytes must be byte-split, not emitted whole.
func TestChunkByLines_SplitsOversizedLine(t *testing.T) {
	// One giant line (no JSON), plus a trailing newline.
	raw := strings.Repeat("L", 500) + "\n"
	chunks := chunkWholeUnits(raw, 100) // routes to chunkByLines (non-JSON, has '\n')
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized line to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 100 {
			t.Errorf("chunk %d exceeds chunkBytes (%d > 100)", i, len(c))
		}
	}
}

// TestLLMDistiller_MapReduce_ReducePreservesContent reproduces the live incident
// (orch-1781968441143087789): the joined chunk extracts exceed targetSize and must be
// reduced (LLM) or truncated — NEVER sent through the structural sentence floor, which
// collapsed log/record extracts to "[trimmed: 0/N sentences]" and silently lost everything.
func TestLLMDistiller_MapReduce_ReducePreservesContent(t *testing.T) {
	mockAI := &countingAI{out: "WARN travel-chat-agent DNS lookup failed"}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 100,
		Model: "fast", ModelContextTokens: 100, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	distiller := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	// ~690B array → map-reduce; the combined chunk extracts exceed targetSize → reduce.
	out := distiller.ProcessForPrompt(context.Background(), mapReduceTestArray(30), 2000,
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find warnings"})

	if out == "" {
		t.Fatal("map-reduce produced empty output")
	}
	if strings.Contains(out, "[trimmed: 0/") || strings.Contains(out, "sentences") {
		t.Errorf("map-reduce regressed to the structural sentence floor (content destroyed): %.160s", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Errorf("expected real extracted content to survive the reduce, got: %.160s", out)
	}
	if mockAI.count() < 2 {
		t.Errorf("expected chunk extractions + a reduce call, got %d", mockAI.count())
	}
}

// TestChunkWholeUnits_128KBReducesChunkCount verifies the 128 KB default chunk size cuts the
// map-reduce chunk (hence LLM-call) count ~4× vs the prior 32 KB, while every chunk stays
// within the byte bound and is whole-unit valid JSON (no mid-record split).
func TestChunkWholeUnits_128KBReducesChunkCount(t *testing.T) {
	payload := mapReduceTestArray(48000) // ~1.2 MB JSON array of records
	if len(payload) < 1_000_000 {
		t.Fatalf("test payload too small to exercise chunking: %d bytes", len(payload))
	}

	chunks32 := chunkWholeUnits(payload, 32768)
	chunks128 := chunkWholeUnits(payload, defaultPreFilterBudget)
	if len(chunks32) == 0 || len(chunks128) == 0 {
		t.Fatalf("unexpected empty chunking: 32K=%d 128K=%d", len(chunks32), len(chunks128))
	}

	// ~4× fewer chunks at 128 KB (allow tolerance for record-boundary packing).
	ratio := float64(len(chunks32)) / float64(len(chunks128))
	if ratio < 3.0 || ratio > 5.0 {
		t.Errorf("expected ~4x fewer chunks at 128KB; got 32K=%d 128K=%d (ratio %.2f)",
			len(chunks32), len(chunks128), ratio)
	}

	// Every 128 KB chunk: within the byte bound AND a valid JSON array (whole-unit invariant).
	for i, c := range chunks128 {
		if len(c) > defaultPreFilterBudget {
			t.Errorf("chunk %d exceeds the 128KB bound: %d bytes", i, len(c))
		}
		var arr []interface{}
		if err := json.Unmarshal([]byte(c), &arr); err != nil {
			t.Errorf("chunk %d is not a valid JSON array (mid-record split?): %v", i, err)
		}
	}
}

// TestDistillPrompt_128KBChunkFitsFastTierContext is the executable form of the headroom
// argument behind the 128 KB default: the prompt template wrapping a full 128 KB chunk, plus
// the output reserve, must fit the smallest fast-tier compaction window. We guard against
// DeepSeek-chat's 64K-token combined endpoint (the tightest in the `fast` alias set) using a
// PESSIMISTIC 2.5 B/tok ratio so the bound holds for dense JSON/log data, not just the
// optimistic heuristic. If a future PreFilterBudget bump no longer fits 64K, this test fails.
func TestDistillPrompt_128KBChunkFitsFastTierContext(t *testing.T) {
	cfg := DefaultConfig().ResultDistill
	d := NewLLMDistiller(nil, cfg, NewStructuralTrimmer(nil, nil), nil)

	stepCtx := ResultProcessorContext{
		StepID:        "step-1",
		AgentName:     "devops-observability-tool",
		Capability:    "query_logs",
		Instruction:   "Retrieve the last 5 minutes of ERROR logs from the flight-tool pod, tail 1000 lines.",
		OriginalQuery: "Run a full cluster health and observability check and post an SRE report to Slack.",
	}

	// (a) Template overhead alone (empty data) — the "instruction window" — must be small.
	overheadTokens := estimateTokens(d.buildDistillationPrompt("", cfg.TargetSize, stepCtx))
	if overheadTokens > 600 {
		t.Errorf("prompt template overhead = %d tokens, want < 600 (it should be ~2%% of the chunk)", overheadTokens)
	}

	// (b) Worst-case: 128 KB of dense data at 2.5 B/tok + template + output reserve < 64K.
	const fastTierFloor = 64000     // DeepSeek-chat 64K combined input+output endpoint
	pessimisticBytesPerToken := 2.5 // dense JSON/logs tokenize worse than the 3.5 heuristic
	chunkTokensWorstCase := int(float64(defaultPreFilterBudget) / pessimisticBytesPerToken)
	outputReserve := cfg.TargetSize/3 + 100 // mirrors extractChunk / single-call maxTokens
	if total := chunkTokensWorstCase + overheadTokens + outputReserve; total > fastTierFloor {
		t.Errorf("128KB chunk does not fit the 64K fast-tier floor: chunk(%d)+template(%d)+output(%d) = %d tokens",
			chunkTokensWorstCase, overheadTokens, outputReserve, total)
	}
}
