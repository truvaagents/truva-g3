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
