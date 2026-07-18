package orchestration

// Tests for the result-trim honest-disclosure system (Phase 16): the annotation registry
// and agent-input stripper round-trip, the ContentLost loss-accounting signal, coverage
// disclosure across the single-call / map-reduce / structural paths, the versioned distill
// cache envelope, and the synthesis-fallback seams.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/truvaagents/truva-g3/core"
)

// TestDisclosureWordings verifies each stage-specific disclosure carries the UNKNOWN
// safeguard, that the three forms are distinct, and that partial-source never displays as ~100%.
func TestDisclosureWordings(t *testing.T) {
	forms := map[string]string{
		"partial-source": partialSourceDisclosure(0.28),
		"combine":        combineTruncationDisclosure(),
		"truncation":     truncationDisclosure(),
	}
	seen := map[string]bool{}
	for name, s := range forms {
		if !strings.Contains(s, "UNKNOWN") {
			t.Errorf("%s disclosure missing UNKNOWN safeguard: %q", name, s)
		}
		if seen[s] {
			t.Errorf("%s disclosure is not distinct from another form: %q", name, s)
		}
		seen[s] = true
	}
	// partialSourceDisclosure is only called with coverage < 1, so it must never render ~100%.
	if got := partialSourceDisclosure(0.999); strings.Contains(got, "100%") {
		t.Errorf("partial source rounded to ~100%%, must clamp: %q", got)
	}
	if got := partialSourceDisclosure(0.28); !strings.Contains(got, "~28%") {
		t.Errorf("expected ~28%% in %q", got)
	}
}

// TestStripResultAnnotation_AllForms verifies the agent-input stripper removes EVERY
// registered annotation form — the guarantee that a new disclosure form can't defeat the re-parse.
func TestStripResultAnnotation_AllForms(t *testing.T) {
	body := `{"a":1}`
	for _, pfx := range annotationPrefixes {
		in := body + pfx + " something something]"
		if got := stripResultAnnotation(in); got != body {
			t.Errorf("prefix %q not stripped: got %q, want %q", pfx, got, body)
		}
	}
	// No annotation → unchanged.
	if got := stripResultAnnotation(body); got != body {
		t.Errorf("unannotated input changed: %q", got)
	}
	// A trailing line that ends with "]" but is NOT a registered prefix must be left intact —
	// the safety property that keeps bracketed body/log content from being cut.
	for _, s := range []string{
		body + "\n[not a registered annotation form]",
		body + "\nsome log line ending in a bracket]",
	} {
		if got := stripResultAnnotation(s); got != s {
			t.Errorf("non-registered trailing line must not be cut: %q → %q", s, got)
		}
	}
}

// TestAppendDisclosure verifies the note always survives: it fits when there is room;
// when there is not, the BODY is shortened (never the note) and the pair stays within maxBytes —
// except on a pathological budget smaller than the note itself, where the note wins and the
// total overshoots (losing the UNKNOWN signal would be worse than the overshoot).
func TestAppendDisclosure(t *testing.T) {
	note := "\n[note]"
	// Fits: appended verbatim.
	if got := appendDisclosure("abc", note, 100); got != "abc"+note {
		t.Errorf("fits case: got %q", got)
	}
	// Overshoot: body cut to make room, note preserved, total <= maxBytes.
	max := 10
	got := appendDisclosure("abcdefghijklmnop", note, max)
	if len(got) > max {
		t.Errorf("expected result <= %d, got %d (%q)", max, len(got), got)
	}
	if !strings.HasSuffix(got, note) {
		t.Errorf("note must survive the cut, got %q", got)
	}
	// Pathological: note alone exceeds budget → note still wins (disclosure over silence).
	if got := appendDisclosure("x", note, 3); !strings.Contains(got, "[note]") {
		t.Errorf("note must win on a tiny budget, got %q", got)
	}
}

// TestSingleCallPartialCoverage verifies the single-call distiller appends the partial-source
// disclosure to the OUTPUT and records coverage metadata when stage-1 dropped content.
func TestSingleCallPartialCoverage(t *testing.T) {
	mockAI := &distillerMockAI{response: &core.AIResponse{Content: "SUMMARY-OF-LOGS"}}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: 4096, TargetSize: 4096}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	input := `{"streams":"` + strings.Repeat("x", 20000) + `"}` // pre-filters to ~4096 → coverage ~20%
	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := d.ProcessForPrompt(ctx, input, 4096, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find errors",
	})

	if !strings.Contains(out, "SUMMARY-OF-LOGS") {
		t.Errorf("distilled content missing from output: %q", out)
	}
	if !strings.Contains(out, "partial source") || !strings.Contains(out, "UNKNOWN") {
		t.Errorf("expected partial-source disclosure appended to output, got %q", out)
	}
	if !meta.PartialCoverage {
		t.Error("expected metadata.PartialCoverage = true")
	}
	if meta.SourceCoverageRatio <= 0 || meta.SourceCoverageRatio >= 1 {
		t.Errorf("expected 0 < SourceCoverageRatio < 1, got %v", meta.SourceCoverageRatio)
	}
	if meta.SegmentsAnalyzed != 1 || meta.SegmentsTotal != 1 {
		t.Errorf("single-call segments should be 1/1, got %d/%d", meta.SegmentsAnalyzed, meta.SegmentsTotal)
	}
	// Secondary signal reaches the prompt (in <context>, not relied upon for the guarantee).
	if !strings.Contains(mockAI.prompt, "% of the source") {
		t.Errorf("expected the coverage note in the distill prompt, got head: %.200s", mockAI.prompt)
	}
}

// metaProcessor is a fake inner ResultProcessor that records trim metadata and returns a fixed output.
type metaProcessor struct {
	out   string
	meta  ResultTrimMetadata
	calls int
}

func (f *metaProcessor) ProcessForPrompt(ctx context.Context, _ string, _ int, _ ResultProcessorContext) string {
	f.calls++
	captureTrimMetadata(ctx, f.meta)
	return f.out
}

// TestCacheEnvelopeReplaysMetadata verifies a cache HIT replays the stored coverage metadata
// (not just the output), and that an undecodable entry is discarded and recomputed.
func TestCacheEnvelopeReplaysMetadata(t *testing.T) {
	cache := newMapDigestCache()
	inner := &metaProcessor{
		out:  "DISTILLED",
		meta: ResultTrimMetadata{Method: "distill", PartialCoverage: true, SourceCoverageRatio: 0.3, SegmentsAnalyzed: 1, SegmentsTotal: 1},
	}
	p := NewCachingProcessor(inner, cache, time.Minute, 10, "salt", nil)
	input := strings.Repeat("a", 50) // >= minBytes so it is cached
	sc := ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "find errors"}

	// First call: miss → inner runs → envelope stored; caller sees the metadata.
	ctx1, m1 := WithTrimMetadataCapture(context.Background())
	if out := p.ProcessForPrompt(ctx1, input, 4096, sc); out != "DISTILLED" {
		t.Fatalf("first call output = %q", out)
	}
	if inner.calls != 1 || !m1.PartialCoverage {
		t.Fatalf("first call: inner.calls=%d, PartialCoverage=%v", inner.calls, m1.PartialCoverage)
	}

	// Second call: HIT → inner NOT run, metadata replayed from the envelope.
	ctx2, m2 := WithTrimMetadataCapture(context.Background())
	if out := p.ProcessForPrompt(ctx2, input, 4096, sc); out != "DISTILLED" {
		t.Fatalf("cache-hit output = %q", out)
	}
	if inner.calls != 1 {
		t.Errorf("cache hit should not re-run inner, calls=%d", inner.calls)
	}
	if !m2.PartialCoverage || m2.SegmentsTotal != 1 || m2.Method != "distill" {
		t.Errorf("cache hit did not replay metadata: %+v", *m2)
	}

	// Corrupt the stored entry → decode fails → recompute (miss), inner runs again.
	for k := range cache.data {
		cache.data[k] = []byte("legacy-bare-string-not-an-envelope")
	}
	ctx3, _ := WithTrimMetadataCapture(context.Background())
	if out := p.ProcessForPrompt(ctx3, input, 4096, sc); out != "DISTILLED" {
		t.Fatalf("post-corruption output = %q", out)
	}
	if inner.calls != 2 {
		t.Errorf("undecodable entry should trigger recompute, inner.calls=%d (want 2)", inner.calls)
	}
}

// Disclosure-integrity tests: annotation stacking through the agent-input guard, silent cuts
// of model output, missing or false UNKNOWN disclosures, coverage accounting, and the
// annotation-registry round-trip guarantee.

// TestStripStackedAnnotations verifies the stripper removes ALL trailing
// annotation-shaped notes (structural note under the degenerate floor note; map-reduce combine +
// partial), while a non-annotation "[…" embedded mid-body still stops the loop.
func TestStripStackedAnnotations(t *testing.T) {
	body := `{"a":1}`
	stacked := body +
		"\n[trimmed: 1/100 items; omitted content is UNKNOWN — do not infer it is absent]" +
		"\n[severely reduced: kept 600 of ~60000 bytes (1.00%); most content omitted — treat anything NOT shown as UNKNOWN, do not infer it is absent]"
	if got := stripResultAnnotation(stacked); got != body {
		t.Errorf("stacked structural notes not fully stripped: %q", got)
	}

	mapreduce := body + combineTruncationDisclosure() + partialSegmentsDisclosure(3, 5)
	if got := stripResultAnnotation(mapreduce); got != body {
		t.Errorf("stacked map-reduce notes not fully stripped: %q", got)
	}

	// A prefix-like line quoted mid-body must NEVER cause a cut: the peel only removes a
	// trailing line that starts with a registered prefix and ends with "]". Content after a
	// quoted note survives intact (the pre-fix loop deleted it).
	quoted := body + "\n[trimmed: quoted in a log line\nmore real content"
	if got := stripResultAnnotation(quoted); got != quoted {
		t.Errorf("mid-body quoted prefix must not trigger a cut: got %q", got)
	}
	// Same with a genuine trailing note after the quoted one: only the real note peels.
	withNote := quoted + "\n[trimmed: 3/10 sentences; omitted content is UNKNOWN — do not infer it is absent]"
	if got := stripResultAnnotation(withNote); got != quoted {
		t.Errorf("expected only the genuine trailing note peeled, got %q", got)
	}
}

// TestDegenerateArrayAgentInputRoundTrip is the end-to-end guard round-trip: a
// degenerate array trim must reach the agent-input guard with exactly ONE trailing annotation
// so the re-parse succeeds and the param ships TRIMMED (pre-fix, stacked notes made the
// re-parse fail and the guard failed open with the full-size original).
func TestDegenerateArrayAgentInputRoundTrip(t *testing.T) {
	items := make([]interface{}, 100)
	for i := range items {
		items[i] = strings.Repeat("a", 590)
	}
	serialized, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}

	// Direct trimmer check: degenerate, one annotation, strippable to valid JSON.
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, string(serialized), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list documents",
	})
	if !meta.Degenerate {
		t.Fatalf("fixture should be degenerate (kept ratio %v)", meta.KeptRatio)
	}
	if !strings.Contains(out, "severely reduced") {
		t.Errorf("expected the severe disclosure, got tail: %q", out[max(0, len(out)-200):])
	}
	stripped := stripResultAnnotation(out)
	if !json.Valid([]byte(stripped)) {
		t.Errorf("stripped body must be valid JSON, got: %q", stripped)
	}
	for _, pfx := range annotationPrefixes {
		if strings.Contains(stripped, pfx) {
			t.Errorf("stacked annotation survived the strip (prefix %q): %q", pfx, stripped)
		}
	}

	// Round trip through the real agent-input guard: the param must come back trimmed.
	proc := NewByteBudgetAgentInputProcessor(trimmer, 2000, nil)
	params, err := proc.ProcessInput(context.Background(), map[string]interface{}{"docs": items},
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "list documents"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(params["docs"])
	if err != nil {
		t.Fatal(err)
	}
	if len(got) >= len(serialized) {
		t.Errorf("guard failed open: param shipped untrimmed (%d bytes)", len(got))
	}
	if len(got) > 2500 {
		t.Errorf("trimmed param exceeds budget+slack: %d bytes", len(got))
	}
}

// TestDistillerOutputNeverCutForNote verifies the partial-source disclosure is a plain
// append: the model's analyzed output survives whole (bounded overshoot) instead of losing its
// tail to make room for the note.
func TestDistillerOutputNeverCutForNote(t *testing.T) {
	content := strings.Repeat("F", 4090) // within "at most 4096 chars" yet > targetSize-len(note)
	mockAI := &distillerMockAI{response: &core.AIResponse{Content: content}}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: 4096, TargetSize: 4096}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	input := `{"streams":"` + strings.Repeat("x", 20000) + `"}` // forces coverage < 1
	out := d.ProcessForPrompt(context.Background(), input, 4096, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find errors",
	})

	if !strings.Contains(out, content) {
		t.Error("model output was cut to make room for the disclosure note")
	}
	if !strings.Contains(out, "partial source") {
		t.Errorf("expected the partial-source disclosure, got tail: %q", out[max(0, len(out)-160):])
	}
}

// TestUnknownOnFallbackCuts verifies the above-cliff deterministic cuts that previously
// shipped with a neutral note (or none) now carry the UNKNOWN safeguard: keyword-less plain text
// and top-level JSON scalars.
func TestUnknownOnFallbackCuts(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Keyword-less plain text (empty instruction → no keywords → head byte-cut).
	text := strings.Repeat("word ", 60)
	out := trimmer.ProcessForPrompt(context.Background(), text, 150, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "",
	})
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("keyword-less text cut lacks the UNKNOWN safeguard: %q", out)
	}

	// Top-level JSON scalar (a giant JSON string) — parses as JSON but is neither object nor array.
	scalar := `"` + strings.Repeat("a", 300) + `"`
	out = trimmer.ProcessForPrompt(context.Background(), scalar, 150, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "inspect payload",
	})
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("JSON-scalar cut lacks the UNKNOWN safeguard: %q", out)
	}
}

// TestNoFalsePartialityOnCompleteData verifies that when a result only exceeded the
// budget via serialization overhead (pretty-printing) and NOTHING was dropped, no partiality
// note is appended — a complete dataset must not instruct synthesis to hedge.
func TestNoFalsePartialityOnCompleteData(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Array: 20 small items, pretty-printed over budget, compact under it.
	items := make([]map[string]interface{}, 20)
	for i := range items {
		items[i] = map[string]interface{}{"id": i, "name": fmt.Sprintf("item-%02d", i)}
	}
	pretty, _ := json.MarshalIndent(items, "", "  ")
	compact, _ := json.Marshal(items)
	budget := len(compact) + 100
	if len(pretty) <= budget {
		t.Fatalf("fixture broken: pretty %d must exceed budget %d", len(pretty), budget)
	}
	out := trimmer.ProcessForPrompt(context.Background(), string(pretty), budget, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list items",
	})
	if strings.Contains(out, "UNKNOWN") || strings.Contains(out, "\n[") {
		t.Errorf("complete array must carry no partiality note, got: %q", out)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("expected bare valid JSON for complete data, got: %q", out)
	}

	// Object: same shape, all fields kept.
	obj := map[string]interface{}{
		"prices": []interface{}{1.5, 2.5, 3.5}, "summary": "ok", "source": "unit", "count": 3,
	}
	prettyObj, _ := json.MarshalIndent(obj, "", "    ")
	compactObj, _ := json.Marshal(obj)
	budgetObj := len(compactObj) + 40
	if len(prettyObj) <= budgetObj {
		t.Fatalf("fixture broken: pretty %d must exceed budget %d", len(prettyObj), budgetObj)
	}
	out = trimmer.ProcessForPrompt(context.Background(), string(prettyObj), budgetObj, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "summarize prices",
	})
	if strings.Contains(out, "UNKNOWN") {
		t.Errorf("complete object must carry no partiality note, got: %q", out)
	}
}

// TestNearBudgetCoverageStaysPartial verifies a source just over PreFilterBudget whose
// trim drops fields still discloses partial coverage: pre-fix, the annotation-inclusive numerator
// pushed the byte ratio to >= 1 and silently suppressed every coverage<1 gate.
func TestNearBudgetCoverageStaysPartial(t *testing.T) {
	obj := make(map[string]interface{}, 30)
	for i := 0; i < 30; i++ {
		obj[fmt.Sprintf("k%02d", i)] = strings.Repeat("v", 30)
	}
	input, _ := json.Marshal(obj)
	budget := 1000
	if len(input) <= budget || len(input) > budget+400 {
		t.Fatalf("fixture must sit just over the pre-filter budget, got %d vs %d", len(input), budget)
	}

	mockAI := &distillerMockAI{response: &core.AIResponse{Content: "SUMMARY"}}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: budget, TargetSize: 2048}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := d.ProcessForPrompt(ctx, string(input), 2048, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "check values",
	})

	if meta.FieldsDropped == 0 {
		t.Fatal("fixture broken: the pre-filter should have dropped at least one field")
	}
	if !meta.PartialCoverage {
		t.Error("expected PartialCoverage=true for a near-budget trim with drops")
	}
	if !meta.ContentLost {
		t.Error("expected ContentLost=true to survive into the distiller's record")
	}
	if meta.SourceCoverageRatio <= 0 || meta.SourceCoverageRatio >= 1 {
		t.Errorf("expected 0 < SourceCoverageRatio < 1, got %v", meta.SourceCoverageRatio)
	}
	if !strings.Contains(out, "partial source") {
		t.Errorf("expected the partial-source disclosure, got: %q", out)
	}
}

// TestCoveragePctClamps pins the shared percentage clamp used by both the appended
// disclosure and the prompt's secondary signal.
func TestCoveragePctClamps(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int
	}{{0.28, 28}, {0.999, 99}, {1.2, 99}, {-0.5, 0}, {0.004, 0}} {
		if got := coveragePct(tc.in); got != tc.want {
			t.Errorf("coveragePct(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestPlainTextKeepsLastSentenceWhole: selection runs against the FULL budget and
// the note rides as a bounded overshoot, so the last selected sentence survives whole (the
// old 50-byte reservation under-sized the ~84-byte note and cut the final sentence mid-word;
// reserving the full note length was also tried and starves small budgets — don't re-add it).
func TestPlainTextKeepsLastSentenceWhole(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&sb, "the error flag is set to on %02d. ", i) // 33-byte sentences, total > budget
	}
	trimmer := NewStructuralTrimmer(nil, nil)
	out := trimmer.ProcessForPrompt(context.Background(), sb.String(), 300, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find the error flag",
	})

	body := out
	if idx := strings.Index(out, "\n["); idx >= 0 {
		body = out[:idx]
	}
	if !strings.HasSuffix(strings.TrimRight(body, " "), ".") {
		t.Errorf("last selected sentence was cut mid-word: %q", body)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("expected the sentence-trim UNKNOWN note, got: %q", out)
	}
}

// TestEveryEmitterStrippable is the registry's forward guarantee: every disclosure
// form the package can emit must be recognized (and fully removed) by stripResultAnnotation, so
// a new emitter that skips the registry fails here instead of failing open in production.
func TestEveryEmitterStrippable(t *testing.T) {
	// Bare notes from the named emitters: shape-checked (single line, ends "]") — the exact
	// contract the stripper's peel depends on; a multi-line note would survive stacking.
	notes := map[string]string{
		"partialSourceDisclosure": partialSourceDisclosure(0.4),
		"combineTruncation":       combineTruncationDisclosure(),
		"truncationDisclosure":    truncationDisclosure(),
		"degenerateNote":          degenerateNote(1000, 10),
		"partialSegments":         partialSegmentsDisclosure(1, 2),
	}
	body := `{"a":1}`
	for name, note := range notes {
		if strings.Count(note, "\n") != 1 || !strings.HasSuffix(note, "]") {
			t.Errorf("%s: note violates the annotation shape contract (one line, ends ']'): %q", name, note)
		}
		if got := stripResultAnnotation(body + note); got != body {
			t.Errorf("%s: not cleanly peeled — missing from annotationPrefixes? got %q", name, got)
		}
	}

	// Full outputs from the cut helpers: strippable back to a bare body.
	emitted := map[string]string{
		"truncateResultBytes":      truncateResultBytes(body+strings.Repeat("x", 300), 100),
		"truncateBytesWithUnknown": truncateBytesWithUnknown(body+strings.Repeat("x", 300), 150),
	}
	for name, s := range emitted {
		stripped := stripResultAnnotation(s)
		if stripped == s {
			t.Errorf("%s: emitted form is not strippable — missing from annotationPrefixes? %q", name, s)
			continue
		}
		for _, pfx := range annotationPrefixes {
			if strings.Contains(stripped, pfx) {
				t.Errorf("%s: residue after strip (prefix %q): %q", name, pfx, stripped)
			}
		}
	}
}

// TestTruncatedBackfillStillDisclosed verifies a backfilled field whose VALUE was
// truncated (droppedCount decremented back to zero — invisible to unit counts) still carries
// the UNKNOWN annotation and records ContentLost.
func TestTruncatedBackfillStillDisclosed(t *testing.T) {
	obj := map[string]interface{}{
		"alpha": strings.Repeat("a", 100),
		"beta":  strings.Repeat("b", 100),
		"gamma": "gamma is relevant " + strings.Repeat("g", 5000),
	}
	input, _ := json.Marshal(obj)
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, string(input), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "gamma relevant",
	})

	if !meta.ContentLost {
		t.Error("expected ContentLost=true for a value-truncated backfill")
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("value-truncated result must carry the UNKNOWN disclosure, got tail: %q", out[max(0, len(out)-200):])
	}
}

// TestNoFalsePartialOnReserialization verifies the distiller appends NO partial-source
// disclosure when the pre-filter's byte shrink is pure re-serialization (zero content lost).
func TestNoFalsePartialOnReserialization(t *testing.T) {
	items := make([]map[string]interface{}, 20)
	for i := range items {
		items[i] = map[string]interface{}{"id": i, "name": fmt.Sprintf("item-%02d", i)}
	}
	pretty, _ := json.MarshalIndent(items, "", "    ")
	compact, _ := json.Marshal(items)
	budget := len(compact) + 100
	if len(pretty) <= budget {
		t.Fatalf("fixture broken: pretty %d must exceed pre-filter budget %d", len(pretty), budget)
	}

	mockAI := &distillerMockAI{response: &core.AIResponse{Content: "SUMMARY"}}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: budget, TargetSize: 2048}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := d.ProcessForPrompt(ctx, string(pretty), 2048, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list items",
	})

	if strings.Contains(out, "partial source") {
		t.Errorf("re-serialization is not loss — no partial-source disclosure expected, got: %q", out)
	}
	if meta.PartialCoverage {
		t.Error("expected PartialCoverage=false when no content was lost")
	}
	if strings.Contains(mockAI.prompt, "% of the source") {
		t.Error("expected no coverage note in the prompt when no content was lost")
	}
}

// TestPassthroughWithTrailingNoteShapedLine verifies a source that legitimately ends
// with an annotation-shaped line and passes the pre-filter untouched is NOT reported partial.
func TestPassthroughWithTrailingNoteShapedLine(t *testing.T) {
	input := `{"upstream":"data"}` + "\n[trimmed: 5/10 items; omitted content is UNKNOWN — do not infer it is absent]"
	mockAI := &distillerMockAI{response: &core.AIResponse{Content: "SUMMARY"}}
	config := ResultDistillConfig{Enabled: true, DistillThreshold: 10, PreFilterBudget: 4096, TargetSize: 2048}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := d.ProcessForPrompt(ctx, input, 2048, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "inspect",
	})

	if strings.Contains(out, "partial source") || meta.PartialCoverage {
		t.Errorf("passthrough source must never be reported partial, got: %q (meta %+v)", out, *meta)
	}
}

// TestUnknownNoteReportsActualKeptBytes verifies the byte-trim note states the real
// post-cut body length, not the budget.
func TestUnknownNoteReportsActualKeptBytes(t *testing.T) {
	s := strings.Repeat("x", 3000)
	out := truncateBytesWithUnknown(s, 150)
	if len(out) > 150 {
		t.Errorf("expected total within budget, got %d", len(out))
	}
	body := stripResultAnnotation(out)
	want := fmt.Sprintf("→ %d bytes", len(body))
	if !strings.Contains(out, want) {
		t.Errorf("note must state the actual kept length %q, got: %q", want, out)
	}
}

// TestZeroFitArrayIsEmptyNotNull verifies an array whose every item exceeds the
// budget trims to "[]" (typed empty array), never "null" — the guard's re-parse would accept
// null and ship a wrongly-typed param.
func TestZeroFitArrayIsEmptyNotNull(t *testing.T) {
	items := make([]interface{}, 4)
	for i := range items {
		items[i] = strings.Repeat("z", 3000)
	}
	input, _ := json.Marshal(items)
	trimmer := NewStructuralTrimmer(nil, nil)
	out := trimmer.ProcessForPrompt(context.Background(), string(input), 200, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list",
	})
	body := stripResultAnnotation(out)
	if body == "null" {
		t.Fatalf("zero-fit array must not become typed null: %q", out)
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("body must re-parse, got %v (%q)", err, body)
	}
	if _, ok := parsed.([]interface{}); !ok {
		t.Errorf("expected an array body, got %T (%q)", parsed, body)
	}
}

// TestNoSevereNoteOnCompleteReserialization verifies the degenerate tier is gated on
// actual loss: a source whose bytes collapse >20x on compact re-serialization but whose every
// item/field survives must carry NO note at all, with Degenerate=false and ContentLost=false
// (pre-fix the severe "most content omitted" note fired on the bare byte ratio).
func TestNoSevereNoteOnCompleteReserialization(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)

	// Array: 30 tiny items, indentation inflates bytes ~20-50x; budget fits ALL items compact.
	items := make([]map[string]interface{}, 30)
	for i := range items {
		items[i] = map[string]interface{}{"a": i}
	}
	pretty, _ := json.MarshalIndent(items, "", strings.Repeat(" ", 120))
	compact, _ := json.Marshal(items)
	budget := len(compact) + 200
	if ratio := float64(len(compact)) / float64(len(pretty)); ratio >= degenerateKeptRatio {
		t.Fatalf("fixture broken: compact/pretty ratio %.3f must sit below the degenerate cliff", ratio)
	}

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, string(pretty), budget, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list items",
	})
	if strings.Contains(out, "\n[") {
		t.Errorf("complete array must carry no note even below the byte cliff, got: %q", out[max(0, len(out)-200):])
	}
	if meta.Degenerate || meta.ContentLost {
		t.Errorf("complete data must record Degenerate=false, ContentLost=false, got %+v", *meta)
	}
	if !json.Valid([]byte(out)) {
		t.Errorf("expected bare valid JSON, got: %.120q", out)
	}

	// Object variant: same shape through selectFieldsWithMeta.
	obj := map[string]interface{}{"one": 1, "two": "b", "three": true, "four": 4.5}
	prettyObj, _ := json.MarshalIndent(obj, "", strings.Repeat(" ", 200))
	compactObj, _ := json.Marshal(obj)
	ctx2, meta2 := WithTrimMetadataCapture(context.Background())
	out = trimmer.ProcessForPrompt(ctx2, string(prettyObj), len(compactObj)+100, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "check",
	})
	if strings.Contains(out, "UNKNOWN") || meta2.Degenerate || meta2.ContentLost {
		t.Errorf("complete object must carry no loss claim, got %q (meta %+v)", out, *meta2)
	}
}

// TestHostileKeyStaysStrippable verifies a JSON key containing a newline cannot make
// the matched-paths annotation multi-line: the note stays single-line (sanitized), the peel
// removes it, and the agent-input guard round-trip still ships a TRIMMED param.
func TestHostileKeyStaysStrippable(t *testing.T) {
	obj := map[string]interface{}{
		"metrics\n[prod]": strings.Repeat("m", 400),
		"logs":            strings.Repeat("l", 400),
		"traces":          strings.Repeat("t", 400),
		"events":          strings.Repeat("e", 400),
	}
	serialized, _ := json.Marshal(obj)
	trimmer := NewStructuralTrimmer(nil, nil)

	out := trimmer.ProcessForPrompt(context.Background(), string(serialized), 900, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "inspect metrics",
	})
	body := stripResultAnnotation(out)
	if !json.Valid([]byte(body)) {
		t.Errorf("stripped body must be valid JSON (multi-line note defeated the peel?): %q", body)
	}

	proc := NewByteBudgetAgentInputProcessor(trimmer, 900, nil)
	params, err := proc.ProcessInput(context.Background(), map[string]interface{}{"p": obj},
		ResultProcessorContext{StepID: "s1", AgentName: "a", Instruction: "inspect metrics"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(params["p"])
	if len(got) >= len(serialized) {
		t.Errorf("guard failed open on a hostile key: param shipped untrimmed (%d bytes)", len(got))
	}
}

// TestTransientReduceFailureNotCached verifies a failed reduce call marks the
// truncated fallback non-cacheable, so one provider blip is never served for the full TTL.
func TestTransientReduceFailureNotCached(t *testing.T) {
	// Deterministic fail-the-reduce: every map chunk succeeds, the (chunks+1)th call — the
	// reduce — errors. Extract lines are long enough that the joined chunk outputs exceed the
	// floored targetSize (minDistillTargetSize = 256), so the reduce call actually fires.
	pre, _, _, _ := chunkWholeUnits(mapReduceTestArray(30), 100)
	mockAI := &failAfterAI{succeedUntil: len(pre), out: "WARN " + strings.Repeat("x", 60)}
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 100, TargetSize: 100,
		Model: "fast", ModelContextTokens: 1500, MapConcurrency: 4,
		// Route by BYTES (threshold == PreFilterBudget: survives normalization and always yields
		// >1 chunk), decoupled from ModelContextTokens — which is now sized above
		// tokens(combined)+reduce-overhead (~973 for the 500-token output reserve, P17.7) so the
		// reduce call actually fires instead of the too-big deterministic truncation.
		MapReduceThresholdBytes: 100,
		CompactionDeadline:      5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, nonCacheable := withNonCacheableCapture(context.Background())
	out := d.ProcessForPrompt(ctx, mapReduceTestArray(30), 100, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find warnings",
	})

	if !strings.Contains(out, "findings truncated") {
		t.Errorf("expected the combine-truncation disclosure on a failed reduce, got: %q", out)
	}
	if !*nonCacheable {
		t.Error("a transiently-failed reduce must be marked non-cacheable")
	}
}

// TestWrapperPreservedNoDisclosure verifies the P17.6 wrapper-preserving chunker: a wrapped
// object ({status, stats, data:{result:[...]}}) routed through map-reduce keeps every wrapper /
// sibling field in every chunk, so the result carries NO partial-source note and records no loss.
// This is the inverse of the old dominant-array drop — the drop-disclosure wiring is now a
// dead-man's switch that must stay silent on a wrapped fixture.
func TestWrapperPreservedNoDisclosure(t *testing.T) {
	records := make([]interface{}, 30)
	for i := range records {
		records[i] = map[string]interface{}{"line": fmt.Sprintf("log entry %02d", i)}
	}
	wrapped := map[string]interface{}{
		"status": "ok",
		"stats":  map[string]interface{}{"count": 30, "elapsed_ms": 12},
		"data":   map[string]interface{}{"result": records},
	}
	raw, _ := json.Marshal(wrapped)

	mockAI := &countingAI{out: "EXTRACT"}
	// PreFilterBudget 200 keeps the ~68-byte wrapper well under maxWrapperShare (0.5×200=100), so
	// the preserving path engages rather than the byte-split fallback; ModelContextTokens 50 forces
	// the map-reduce route.
	config := ResultDistillConfig{
		Enabled: true, DistillThreshold: 10, PreFilterBudget: 200, TargetSize: 2000,
		Model: "fast", ModelContextTokens: 50, MapConcurrency: 4,
		CompactionDeadline: 5 * time.Second,
	}
	d := NewLLMDistiller(mockAI, config, NewStructuralTrimmer(nil, nil), nil)

	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := d.ProcessForPrompt(ctx, string(raw), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "summarize logs",
	})

	if strings.Contains(out, "partial source") {
		t.Errorf("wrapper-preserving chunker must NOT disclose a wrapper drop, got: %q", out)
	}
	if meta.ContentLost || meta.PartialCoverage {
		t.Errorf("wrapper preserved: expected ContentLost=false, PartialCoverage=false, got %+v", *meta)
	}
	if meta.SourceCoverageRatio != 1 {
		t.Errorf("wrapper preserved: expected full coverage 1.0, got %v", meta.SourceCoverageRatio)
	}
}

// TestEqualSizeLossyTrimEmitsTelemetry verifies the telemetry gate keys on the
// authoritative ContentLost signal: a lossy trim whose output happens to match the original
// byte count must still stamp metadata and fire the result_trim log/span path (pre-fix, the
// byte-inequality gate skipped the whole block, content_lost attribute included).
func TestEqualSizeLossyTrimEmitsTelemetry(t *testing.T) {
	logger := &TestLogger{}
	input := strings.Repeat("X", 50)
	proc := &metaProcessor{
		out:  strings.Repeat("Y", 50), // same size, different (lossy) content
		meta: ResultTrimMetadata{Method: "distill", OriginalBytes: 50, TrimmedBytes: 50, ContentLost: true},
	}
	synth := &AISynthesizer{
		logger:           logger,
		resultProcessor:  proc,
		resultTrimConfig: &ResultTrimConfig{Enabled: true, MaxTotalPromptBytes: 4096},
	}
	results := &ExecutionResult{Steps: []StepResult{{
		StepID: "s1", AgentName: "a", Instruction: "do", Response: input, Success: true,
	}}}

	_ = synth.buildSynthesisPrompt(context.Background(), "req", results)

	tm, _ := results.Steps[0].Metadata["result_trim"].(*ResultTrimMetadata)
	if tm == nil || !tm.ContentLost {
		t.Fatalf("expected ContentLost metadata stamped on the step, got %+v", tm)
	}
	if len(logger.GetLogsByOperation("result_trim")) == 0 {
		t.Error("equal-size lossy trim must fire the result_trim telemetry (gate keyed on ContentLost)")
	}
}

// TestSynthesizerFallbackDisclosure drives the synthesizer's byte-truncation
// fallback seam end-to-end: no result processor configured, response over budget → the prompt
// carries the no-model-analyzed UNKNOWN disclosure and the metadata records the loss.
func TestSynthesizerFallbackDisclosure(t *testing.T) {
	synth := &AISynthesizer{resultTrimConfig: &ResultTrimConfig{Enabled: true, MaxTotalPromptBytes: 100}}
	results := &ExecutionResult{Steps: []StepResult{{
		StepID: "s1", AgentName: "a", Instruction: "do", Response: strings.Repeat("z", 300), Success: true,
	}}}

	prompt := synth.buildSynthesisPrompt(context.Background(), "req", results)

	if !strings.Contains(prompt, "reduced without model analysis") {
		t.Errorf("fallback truncation must carry its disclosure in the prompt, got: %.300q", prompt)
	}
	tm, _ := results.Steps[0].Metadata["result_trim"].(*ResultTrimMetadata)
	if tm == nil || tm.Method != "truncate" || !tm.ContentLost || !tm.PartialCoverage {
		t.Errorf("fallback metadata must record truncate + ContentLost + PartialCoverage, got %+v", tm)
	}
}

// TestOrchestratorFallbackDisclosure — the same seam through the orchestrator's
// (streaming-side) prompt builder, which mirrors the synthesizer copy.
func TestOrchestratorFallbackDisclosure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ResultTrim.Enabled = true
	cfg.ResultTrim.MaxTotalPromptBytes = 100
	o := &AIOrchestrator{config: cfg}
	result := &ExecutionResult{Steps: []StepResult{{
		StepID: "s1", AgentName: "a", Instruction: "do", Response: strings.Repeat("z", 300), Success: true,
	}}}

	prompt := o.buildSynthesisPrompt(context.Background(), "req", result)

	if !strings.Contains(prompt, "reduced without model analysis") {
		t.Errorf("fallback truncation must carry its disclosure in the prompt, got: %.300q", prompt)
	}
	tm, _ := result.Steps[0].Metadata["result_trim"].(*ResultTrimMetadata)
	if tm == nil || tm.Method != "truncate" || !tm.ContentLost || !tm.PartialCoverage {
		t.Errorf("fallback metadata must record truncate + ContentLost + PartialCoverage, got %+v", tm)
	}
}

// TestDigestElisionSignal verifies ContentLost on continuation digests comes from
// the digester's explicit elision signal — including the case the byte proxy got wrong:
// omission sentinels making a lossy digest LONGER than its source.
func TestDigestElisionSignal(t *testing.T) {
	in := `[1,2,3,4,5,6,7,8,9,10,11,12]`
	digest, degen, elided := buildDecisionDigest(in, 10, 48, 64)
	if degen {
		t.Fatal("valid JSON must not be degenerate")
	}
	if !elided {
		t.Error("sampling 10 of 12 array items must report elided=true")
	}
	if len(digest) <= len(in) {
		t.Skipf("fixture no longer pins the proxy-breaking case (digest %d <= source %d)", len(digest), len(in))
	}

	if _, _, el := buildDecisionDigest(`{"a":1,"b":"short"}`, 10, 48, 64); el {
		t.Error("a compact object with nothing sampled/elided must report elided=false")
	}
	if _, _, el := buildDecisionDigest(`{"log":"`+strings.Repeat("x", 100)+`"}`, 10, 48, 64); !el {
		t.Error("a scalar elided past scalarMax must report elided=true")
	}

	// End-to-end: renderContinuationDigests carries the signal into ContentLost, and the
	// non-JSON floor path reports loss exactly when the preview cut its source.
	steps := []StepResult{
		{Response: in},
		{Response: `{"a":1}`},
		{Response: "plain text blob far beyond the floor"},
		{Response: "abcdefg"}, // non-JSON, 2 runes over the floor: the "…" makes the cut preview LONGER than its source
	}
	_, meta := renderContinuationDigests(steps, continuationDigestOpts{floorChars: 5, sampleN: 10, scalarMax: 48, maxKeys: 64})
	if !meta[0].ContentLost {
		t.Error("sampled array digest must record ContentLost=true (even though the digest grew)")
	}
	if meta[1].ContentLost {
		t.Error("complete digest must record ContentLost=false")
	}
	if meta[2].Method != "truncate" || !meta[2].ContentLost {
		t.Errorf("cut floor preview must record truncate + ContentLost, got %+v", meta[2])
	}
	// Boundary regression: a byte-length comparison would read this cut as lossless because
	// the appended ellipsis makes the preview >= its source; the exact digest != resp check
	// must still report loss.
	if !meta[3].ContentLost {
		t.Errorf("ellipsis-boundary floor cut must record ContentLost=true, got %+v", meta[3])
	}
}

// TestCaptureNormalizesContentLost verifies the superset invariant is structural:
// any specific loss flag implies ContentLost at the capture choke point, even when an
// emitter (in-tree or a custom ResultProcessor) forgets to set the authoritative bit.
func TestCaptureNormalizesContentLost(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ResultTrimMetadata
		want bool
	}{
		{"partial implies lost", ResultTrimMetadata{PartialCoverage: true}, true},
		{"degenerate implies lost", ResultTrimMetadata{Degenerate: true}, true},
		{"combine-truncated implies lost", ResultTrimMetadata{CombineTruncated: true}, true},
		{"no flags stays lossless", ResultTrimMetadata{Method: "structural"}, false},
	} {
		ctx, meta := WithTrimMetadataCapture(context.Background())
		captureTrimMetadata(ctx, tc.in)
		if meta.ContentLost != tc.want {
			t.Errorf("%s: ContentLost = %v, want %v", tc.name, meta.ContentLost, tc.want)
		}
	}
}

// TestReshrinkCountsDescribeReturnedOutput verifies FieldsKept matches the items
// actually present in the body after a degenerate reshrink re-trims the array.
func TestReshrinkCountsDescribeReturnedOutput(t *testing.T) {
	items := make([]interface{}, 100)
	for i := range items {
		items[i] = strings.Repeat("q", 646) // 3 items fill ~1950 of a 2000 budget → note forces reshrink
	}
	input, _ := json.Marshal(items)
	trimmer := NewStructuralTrimmer(nil, nil)
	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, string(input), 2000, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "list records",
	})

	var parsed []interface{}
	if err := json.Unmarshal([]byte(stripResultAnnotation(out)), &parsed); err != nil {
		t.Fatalf("body must re-parse as an array: %v", err)
	}
	if meta.FieldsKept != len(parsed) {
		t.Errorf("FieldsKept=%d but the returned body contains %d items", meta.FieldsKept, len(parsed))
	}
	if !meta.ContentLost {
		t.Error("expected ContentLost=true for a lossy array trim")
	}
}

// --- Coverage of the pure disclosure/accounting helpers (branch-complete) ---

// TestCompletePlainTextCarriesNoNote covers the plain-text zero-drop branch: a non-JSON blob
// over budget only because of trailing whitespace, whose single scoreable sentence survives
// whole, must carry no partiality note and record ContentLost=false.
func TestCompletePlainTextCarriesNoNote(t *testing.T) {
	trimmer := NewStructuralTrimmer(nil, nil)
	input := "the error occurred here. " + strings.Repeat(" ", 300) // >budget via whitespace; 1 sentence
	ctx, meta := WithTrimMetadataCapture(context.Background())
	out := trimmer.ProcessForPrompt(ctx, input, 100, ResultProcessorContext{
		StepID: "s1", AgentName: "a", Instruction: "find the error",
	})
	if strings.Contains(out, "UNKNOWN") || strings.Contains(out, "\n[") {
		t.Errorf("complete text (every sentence kept) must carry no note, got: %q", out)
	}
	if meta.ContentLost {
		t.Errorf("complete text must record ContentLost=false, got %+v", *meta)
	}
	if !strings.Contains(out, "the error occurred here") {
		t.Errorf("the surviving sentence must be present, got: %q", out)
	}
}

// TestTruncateBytesWithUnknownPassthrough covers the no-cut branch: an input already within
// budget is returned verbatim (no note appended).
func TestTruncateBytesWithUnknownPassthrough(t *testing.T) {
	if got := truncateBytesWithUnknown("fits", 100); got != "fits" {
		t.Errorf("input within budget must pass through unchanged, got %q", got)
	}
}

// TestLossyByteCoverageClamps pins the "never read as full coverage" clamps — the honesty
// guard that keeps escape/annotation inflation or a bad denominator from reporting a lossy
// trim as complete.
func TestLossyByteCoverageClamps(t *testing.T) {
	for _, c := range []struct {
		kept, total int
		want        float64
	}{
		{0, 0, 0.99},    // zero/negative denominator → floor, never divide
		{50, 0, 0.99},   // denominator guard ignores the numerator
		{100, 90, 0.99}, // inflation (kept > total) → floor, never > 1
		{90, 90, 0.99},  // exactly full → floor (a known-lossy trim must not read 100%)
		{-5, 90, 0},     // negative numerator → 0
		{45, 90, 0.5},   // normal sub-1 ratio passes through
	} {
		if got := lossyByteCoverage(c.kept, c.total); got != c.want {
			t.Errorf("lossyByteCoverage(%d,%d) = %v, want %v", c.kept, c.total, got, c.want)
		}
	}
}

// TestLossyTrimEvent pins the telemetry/log gate predicate: fire on the authoritative
// ContentLost signal or any byte change, never on a nil record or a lossless passthrough.
func TestLossyTrimEvent(t *testing.T) {
	for _, c := range []struct {
		name          string
		meta          *ResultTrimMetadata
		orig, trimmed int
		want          bool
	}{
		{"nil meta", nil, 100, 50, false},
		{"content lost, equal size", &ResultTrimMetadata{ContentLost: true}, 100, 100, true},
		{"lossless size change", &ResultTrimMetadata{}, 100, 80, true},
		{"lossless passthrough (equal size)", &ResultTrimMetadata{}, 100, 100, false},
	} {
		if got := lossyTrimEvent(c.meta, c.orig, c.trimmed); got != c.want {
			t.Errorf("%s: lossyTrimEvent = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSanitizeAnnotationText verifies interpolated free text stays a single line (the
// registry's shape contract) and is rune-safely capped, so a hostile key can neither break
// the stripper nor embed invalid UTF-8 in the prompt.
func TestSanitizeAnnotationText(t *testing.T) {
	got := sanitizeAnnotationText("a\nb\rc")
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("raw newline/CR survived: %q", got)
	}
	if got != `a\nb\rc` {
		t.Errorf("expected escaped form, got %q", got)
	}
	// Cap at 200 bytes, without splitting a multi-byte rune at the boundary.
	boundary := strings.Repeat("a", 199) + "€€€" // € is 3 bytes; a byte cut at 200 would split one
	if got := sanitizeAnnotationText(boundary); len(got) > 200 || !utf8.ValidString(got) {
		t.Errorf("expected a rune-safe cut ≤ 200 bytes, got len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

// TestWrapperTooLargeFallsBackToByteSplit exercises the maxWrapperShare guard: when the wrapper
// would occupy more than half a chunk (a heavy sibling / scalar wrapper), the preserving chunker
// declines and the raw serialization is byte-split instead — lossless byte-wise (no dropped
// field), so wrapperDropped stays false and coverage stays 1.
func TestWrapperTooLargeFallsBackToByteSplit(t *testing.T) {
	outer := map[string]interface{}{
		"hdr":  strings.Repeat("h", 300), // heavy wrapper: exceeds maxWrapperShare of a 100-byte chunk
		"data": []interface{}{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
	}
	raw, _ := json.Marshal(outer)

	chunks, dropped, cov, _ := chunkWholeUnits(string(raw), 100)
	if dropped || cov != 1 {
		t.Errorf("byte-split fallback is byte-lossless: expected dropped=false, cov=1, got dropped=%v cov=%v", dropped, cov)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized payload to be split, got %d chunk(s)", len(chunks))
	}
	// Byte-split reassembles exactly (chunkByBytes slices without separators; raw is single-line),
	// so no content is lost even though chunk boundaries land mid-structure.
	if strings.Join(chunks, "") != string(raw) {
		t.Error("byte-split chunks must reassemble to the original bytes (no content lost)")
	}
}
