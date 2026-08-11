package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// partialDisclosureMarker prefixes the honest note appended when a map-reduce run does
// not finish every chunk within the deadline. The cache keys off it to avoid storing
// incomplete results (see result_distill_cache.go).
const partialDisclosureMarker = "[partial:"

// estimateTokens approximates the token count of s using the framework's shared
// HeuristicTokenCounter (≈3.5 bytes/token), so the map-reduce routing threshold uses the
// same accounting as conversation-history budgeting instead of a divergent constant.
func estimateTokens(s string) int {
	n, _ := HeuristicTokenCounter{}.CountTokens(context.Background(), s)
	return n
}

// mapReduceCompact handles results too large for a single compaction-model context:
// it splits the result into whole-unit chunks, extracts each concurrently under one
// shared wall-clock deadline, then combines the per-chunk extracts.
//
// It degrades using LLM output, never a heuristic cut: chunks that do not finish within
// the deadline are disclosed honestly ("N of M segments analyzed") rather than silently
// dropped. Fail-open: if nothing completes it falls back to the structural floor.
func (d *LLMDistiller) mapReduceCompact(
	ctx context.Context, result string, targetSize int, stepCtx ResultProcessorContext,
) string {
	chunkBytes := d.config.PreFilterBudget
	if chunkBytes <= 0 {
		chunkBytes = defaultPreFilterBudget
	}
	chunks, wrapperDropped, wrapperCov, chunkStrategy := chunkWholeUnits(result, chunkBytes)
	total := len(chunks)
	if total <= 1 {
		// Zero chunks (a degenerate payload the chunkers dropped entirely — e.g. only blank
		// lines) or a lone chunk that does not fit the model CONTEXT cannot fit one extract
		// call: dispatching it would be a doomed over-context request (the routing precondition
		// put the raw result over the context/threshold, and its compact single chunk can still
		// exceed the context). Gate on the same fits-context token math the reduce uses — NOT
		// bytes-vs-chunkBytes, which a token-routed payload passes while overflowing the model
		// (T2a). Deterministic floor, cacheable (stable).
		if total == 0 || !d.chunkFitsContext(chunks[0], targetSize, stepCtx) {
			return d.preFilter.ProcessForPrompt(ctx, result, targetSize, stepCtx)
		}
		// One chunk means the whole result — typically its COMPACT re-serialization, which can
		// shrink under chunkBytes even when the raw form routed here — fits a single extract
		// call. Run that call directly (map-reduce with one segment) rather than dropping to the
		// structural floor: the floor makes zero LLM calls, silently gutting record-shaped data
		// (the P17 single-chunk hole; the caller routed here to get MORE LLM coverage, not none).
		chunk := chunks[0]
		scCtx := ctx
		if d.config.CompactionDeadline > 0 {
			var cancel context.CancelFunc
			scCtx, cancel = context.WithTimeout(ctx, d.config.CompactionDeadline)
			defer cancel()
		}
		scStart := time.Now()
		extracted, exErr := d.extractChunk(scCtx, chunk, targetSize, stepCtx, "map-reduce single chunk", 1.0)
		if exErr == nil && extracted != "" {
			telemetry.AddSpanEvent(ctx, "result_distill.mapreduce_complete",
				attribute.String("request_id", requestIDFromBaggage(ctx)),
				attribute.String("step_id", stepCtx.StepID),
				attribute.Int("chunks_total", 1),
				attribute.Int("chunks_completed", 1),
				attribute.Int("combined_bytes", len(extracted)),
				attribute.Int("llm_input_bytes", len(chunk)),
				attribute.String("chunk_strategy", chunkStrategy),
			)
			if registry := core.GetGlobalMetricsRegistry(); registry != nil {
				registry.Counter("orchestration.result_distill.mapreduce", "agent_name", stepCtx.AgentName)
			}
			CaptureResultTrimMetadata(ctx, ResultTrimMetadata{
				OriginalBytes:       len(result),
				TrimmedBytes:        len(extracted),
				Method:              "distill_mapreduce",
				SourceCoverageRatio: 1, // the lone chunk carries the full compact content
				LLMInputBytes:       len(chunk),
				SegmentsAnalyzed:    1,
				SegmentsTotal:       1,
				ChunkStrategy:       chunkStrategy,
			})
			return extracted
		}
		// Fail-open: LLM error/empty → structural floor, non-cacheable so the degraded output
		// is recomputed next time instead of served for the TTL (mirrors the single-call
		// distiller's post-LLM-error fallback policy). Full failure observability per
		// DISTRIBUTED_TRACING_GUIDE Pattern 4 (span error → event → metrics → log), mirroring
		// the single-call llm_failed path — a paid, failed LLM call must never be metrics-silent.
		scDuration := time.Since(scStart)
		if exErr == nil {
			exErr = fmt.Errorf("empty extraction result")
		}
		telemetry.RecordSpanError(ctx, exErr)
		telemetry.AddSpanEvent(ctx, "result_distill.llm_failed",
			attribute.String("request_id", requestIDFromBaggage(ctx)),
			attribute.String("step_id", stepCtx.StepID),
			attribute.String("error", exErr.Error()),
			attribute.Int64("duration_ms", scDuration.Milliseconds()),
		)
		if registry := core.GetGlobalMetricsRegistry(); registry != nil {
			registry.Counter("orchestration.result_distill.failed", "agent_name", stepCtx.AgentName)
		}
		if d.logger != nil {
			d.logger.WarnWithContext(ctx, "Single-chunk map-reduce extraction failed, falling back to structural trim", map[string]interface{}{
				"operation":   "result_distill.mapreduce",
				"request_id":  requestIDFromBaggage(ctx),
				"step_id":     stepCtx.StepID,
				"agent_name":  stepCtx.AgentName,
				"error":       exErr.Error(),
				"error_type":  "compaction",
				"duration_ms": scDuration.Milliseconds(),
			})
		}
		markResultNonCacheable(ctx)
		return d.preFilter.ProcessForPrompt(ctx, result, targetSize, stepCtx)
	}

	// Phase 16 — total bytes SENT across LLM calls (map chunks + the reduce call). Incremented with an
	// atomic immediately BEFORE each dispatch: the collector breaks at the deadline, so channel-carried
	// counts would miss in-flight/late-completing calls, and a plain shared int would race.
	var llmInputBytes atomic.Int64

	// One shared deadline across all chunks (the global wall-clock bound), so the whole
	// map-reduce — not each chunk — is bounded.
	mrCtx := ctx
	if d.config.CompactionDeadline > 0 {
		var cancel context.CancelFunc
		mrCtx, cancel = context.WithTimeout(ctx, d.config.CompactionDeadline)
		defer cancel()
	}

	concurrency := d.config.MapConcurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	// Per-chunk target keeps the combined output near targetSize.
	perChunkTarget := targetSize / total
	if perChunkTarget < minDistillTargetSize {
		perChunkTarget = minDistillTargetSize
	}

	// Fan out: each chunk runs under the shared deadline and reports on a buffered channel
	// rather than via a WaitGroup, so the collector can stop AT the deadline without
	// blocking on a straggler whose AI call ignores context cancellation. The wall-clock
	// bound is therefore hard for the caller even if a provider hangs (the straggler
	// goroutine finishes on its own; its late send lands in the buffered channel).
	type chunkOut struct {
		i   int
		out string
		ok  bool
	}
	resCh := make(chan chunkOut, total) // buffered to total so late sends never block
	sem := make(chan struct{}, concurrency)
	for i, chunk := range chunks {
		go func(i int, chunk string) {
			select {
			case sem <- struct{}{}:
			case <-mrCtx.Done():
				return // deadline hit before this chunk started — counts as incomplete
			}
			defer func() { <-sem }()
			// The deadline may have fired while we waited for a slot (select can pick the
			// sem branch even when Done is also ready). Re-check so we don't start a fresh,
			// already-doomed LLM call after the bound — a needless cost on slow providers.
			if mrCtx.Err() != nil {
				return
			}
			llmInputBytes.Add(int64(len(chunk)))                                                                                      // bytes sent, counted pre-dispatch (Phase 16)
			out, err := d.extractChunk(mrCtx, chunk, perChunkTarget, stepCtx, fmt.Sprintf("map-reduce chunk %d/%d", i+1, total), 1.0) // each chunk shown in full
			resCh <- chunkOut{i: i, out: out, ok: err == nil && out != ""}
		}(i, chunk)
	}

	outs := make([]string, total)
	oks := make([]bool, total)
	reported := 0
collect:
	for reported < total {
		select {
		case r := <-resCh:
			if r.ok {
				outs[r.i] = r.out
				oks[r.i] = true
			}
			reported++
		case <-mrCtx.Done():
			// Deadline: combine whatever finished; do not wait on in-flight stragglers.
			break collect
		}
	}

	// Consolidate completed chunks. Drop the ones that found nothing (the exact noMatchSentinel)
	// so N empty chunks don't reach synthesis as N identical "No matching entries found" lines
	// (live evidence: orch-1782176022971457913 — a 1.99 MB 5xx query → that sentinel ×16). Keep
	// only substantive extracts; `completed` still counts every finished chunk for the partial
	// disclosure below.
	var substantive []string
	completed := 0
	for i := 0; i < total; i++ {
		if !oks[i] {
			continue
		}
		completed++
		if strings.TrimSpace(outs[i]) == noMatchSentinel {
			continue
		}
		substantive = append(substantive, outs[i])
	}

	if completed == 0 {
		// Nothing finished within the budget — fail open to the structural floor.
		if d.logger != nil {
			d.logger.WarnWithContext(ctx, "map-reduce compaction produced no chunks, falling back to structural", map[string]interface{}{
				"operation": "result_distill.mapreduce", "step_id": stepCtx.StepID, "chunks": total,
				"error_type": "compaction",
			})
		}
		// Transient: all chunks failed/timed out — don't cache the degraded fallback.
		markResultNonCacheable(ctx)
		return d.preFilter.ProcessForPrompt(ctx, result, targetSize, stepCtx)
	}

	combined := strings.Join(substantive, "\n")
	if combined == "" {
		// Every completed chunk found nothing relevant — emit ONE sentinel, not N copies.
		combined = noMatchSentinel
	}
	// Reduce: the joined per-chunk extracts can exceed the caller's target (each chunk is
	// bounded only by its own per-chunk target/MaxTokens). Distill them with one more
	// fast-model call — the canonical map-reduce "reduce" — falling open to a VERBATIM
	// head-truncation. Never the structural sentence floor: on joined log/record extracts
	// it keeps zero sentences and silently discards everything (the live
	// orch-1781968441143087789 incident: 1.16 MB → "[trimmed: 0/226 sentences]").
	combineTruncated := false
	combineReason := "" // "reduce_failed" (transient) vs "over_context" (deterministic) — the canary's reduce-fallback measurement needs them distinguishable
	if len(combined) > targetSize {
		// The reduce sees only the completed segments' extracts — on a PARTIAL run its coverage
		// is < 1.0, adding the caveat line so the reduce model does not claim absence over
		// segments it never saw (T2b). completed/total is <= 1.0 (== 1.0 on a full run → no
		// caveat, identical to before).
		reduceCoverage := float64(completed) / float64(total)
		// P17.7 — budget the REAL reduce call (data + prompt wrapper + system prompt + output
		// reserve), not the data alone: a `combined` near the limit would otherwise overflow the
		// actual request. extractCallOverheadTokens derives the overhead from the same builder the
		// call uses (same coverage → the caveat line is counted), so this gate can't drift from it.
		reduceOverhead := d.extractCallOverheadTokens(targetSize, stepCtx, reduceCoverage)
		if estimateTokens(combined)+reduceOverhead <= d.config.ModelContextTokens {
			llmInputBytes.Add(int64(len(combined))) // reduce-call input is LLM volume too (Phase 16)
			if reduced, err := d.extractChunk(mrCtx, combined, targetSize, stepCtx, "map-reduce reduce", reduceCoverage); err == nil && reduced != "" {
				combined = reduced
			} else {
				// Transient reduce failure (provider 429/timeout/empty): the truncation is a
				// degraded fallback — recompute next time rather than serve it from cache for
				// the TTL. The deterministic over-context truncation below stays cacheable.
				combined = truncateResultBytes(combined, targetSize)
				combineTruncated = true
				combineReason = "reduce_failed"
				markResultNonCacheable(ctx)
			}
		} else {
			combined = truncateResultBytes(combined, targetSize)
			combineTruncated = true
			combineReason = "over_context"
		}
	}

	// Phase 16 — compose ALL applicable disclosures and append ONCE: a wrapper drop lost SOURCE
	// fields, combine-truncation dropped extracted FINDINGS, partial-on-timeout dropped whole
	// SEGMENTS — distinct losses, each carrying UNKNOWN. The targetSize re-bound applies ONLY
	// when the body was already deterministically truncated; LLM-analyzed output always gets a
	// plain append (bounded overshoot) — never cut analyzed output to fit a note.
	partial := completed < total
	// Coverage basis: segment-count. Since P17.6 the object chunker replicates the wrapper into
	// every chunk, so wrapperDropped is false-by-construction on the dominant-array path and this
	// branch is a dead-man's switch — it fires (compact-basis served ratio + partial-source note)
	// only if that full-representation invariant ever regresses. The DISCLOSURE would state the
	// pure byte figure (its wording says "by bytes"; segment losses get their own note below),
	// while the metadata records the byte×segment product as the total-source-seen estimate — so
	// a consumer reading SourceCoverageRatio alone is never told "full" about a partial source.
	coverageRatio := float64(completed) / float64(total)
	var notes string
	if wrapperDropped {
		notes += partialSourceDisclosure(wrapperCov)
		coverageRatio = wrapperCov * coverageRatio
	}
	if combineTruncated {
		notes += combineTruncationDisclosure()
	}
	if partial {
		notes += partialSegmentsDisclosure(completed, total)
		// Incomplete — recompute next time rather than serve a partial result from cache.
		markResultNonCacheable(ctx)
	}
	if notes != "" {
		if combineTruncated {
			// The body was already deterministically truncated (a loss the combine note itself
			// discloses), so re-bounding body+notes within targetSize is safe.
			combined = appendDisclosure(combined, notes, targetSize)
		} else {
			// LLM-analyzed content: never silently cut it to fit a note — plain append, bounded
			// overshoot by the notes' length (same policy as the single-call disclosure).
			combined += notes
		}
	}

	completeAttrs := []attribute.KeyValue{
		attribute.String("request_id", requestIDFromBaggage(ctx)),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("chunks_total", total),
		attribute.Int("chunks_completed", completed),
		attribute.Int("combined_bytes", len(combined)),
		attribute.Int("llm_input_bytes", int(llmInputBytes.Load())),
		attribute.String("chunk_strategy", chunkStrategy),
	}
	if combineReason != "" {
		completeAttrs = append(completeAttrs, attribute.String("combine_truncated_reason", combineReason))
	}
	telemetry.AddSpanEvent(ctx, "result_distill.mapreduce_complete", completeAttrs...)
	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("orchestration.result_distill.mapreduce", "agent_name", stepCtx.AgentName)
	}

	CaptureResultTrimMetadata(ctx, ResultTrimMetadata{
		OriginalBytes: len(result),
		TrimmedBytes:  len(combined),
		Method:        "distill_mapreduce",
		// coverageRatio: segment-count, or the byte-scaled figure on a wrapper drop — the same
		// number the appended disclosure states (composed above).
		SourceCoverageRatio: coverageRatio,
		LLMInputBytes:       int(llmInputBytes.Load()),
		SegmentsAnalyzed:    completed,
		SegmentsTotal:       total,
		PartialCoverage:     partial || wrapperDropped,
		CombineTruncated:    combineTruncated,
		ChunkStrategy:       chunkStrategy,
		ContentLost:         partial || combineTruncated || wrapperDropped,
	})
	return combined
}

// buildExtractCall constructs the (prompt, options) for one extract/reduce LLM call. It is the
// single source of truth for how the map/reduce calls are shaped, so extractChunk, the context
// gates, and extractCallOverheadTokens stay in sync — no site may re-derive the prompt/options
// formula independently (P17.7). coverage: 1.0 for a map chunk or a lone/single chunk (shown to
// the LLM in full, no partial-source note); < 1.0 for the REDUCE call on a PARTIAL run, where the
// joined extracts cover only completed-of-total segments — the note stops the reduce model making
// confident absence claims over segments it never saw (T2b). MaxTokens and SystemPrompt are
// post-override — mergeAIOptions may raise MaxTokens or swap the system prompt.
func (d *LLMDistiller) buildExtractCall(content string, targetSize int, stepCtx ResultProcessorContext, coverage float64) (string, *core.AIOptions) {
	prompt := d.buildDistillationPrompt(content, targetSize, stepCtx, coverage)
	maxTokens := targetSize/3 + 100
	if maxTokens < 500 {
		maxTokens = 500
	}
	baseOptions := &core.AIOptions{Temperature: 0.1, MaxTokens: maxTokens, Model: d.config.Model, SystemPrompt: distillationSystemPrompt}
	return prompt, mergeAIOptions(baseOptions, d.aiOptionsOverride)
}

// extractCallOverheadTokens estimates the NON-data token cost of one extract/reduce call — the
// prompt wrapper (everything but the data), the system prompt, and the output reserve (MaxTokens)
// — via the same builder the real call uses, so the context gates account for the full request,
// not the data alone (P17.7). The caller adds the data tokens. coverage must match the call being
// gated: a < 1.0 coverage adds the caveat line, so passing the same value keeps the estimate
// conservative (never-under) for that call.
func (d *LLMDistiller) extractCallOverheadTokens(targetSize int, stepCtx ResultProcessorContext, coverage float64) int {
	wrapperPrompt, options := d.buildExtractCall("", targetSize, stepCtx, coverage)
	return estimateTokens(wrapperPrompt) + estimateTokens(options.SystemPrompt) + options.MaxTokens
}

// chunkFitsContext reports whether a single chunk (shown in full, coverage 1.0) plus the extract
// call's overhead fits the model context — the same fits-context math the reduce gate uses. Used
// by the single-chunk path to floor deterministically instead of dispatching a doomed
// over-context extract (T2a). ModelContextTokens is backfilled at normalization, so it is > 0;
// the guard is defensive.
func (d *LLMDistiller) chunkFitsContext(chunk string, targetSize int, stepCtx ResultProcessorContext) bool {
	if d.config.ModelContextTokens <= 0 {
		return true
	}
	return estimateTokens(chunk)+d.extractCallOverheadTokens(targetSize, stepCtx, 1.0) <= d.config.ModelContextTokens
}

// extractChunk runs the extractive distillation prompt on a single chunk (or, for the reduce
// step, the joined chunk extracts). It inherits the caller's (shared) deadline via ctx — it does
// not impose its own. callDesc labels the call ("map-reduce chunk i/N" or "map-reduce reduce") for
// the LLM-Debug record. coverage is 1.0 for a chunk shown in full and < 1.0 for the reduce on a
// partial run (T2b). Each call records a typed result_distillation interaction here for parity
// with the single-call path (the generic agent_llm_call is deferred). (Phase 12)
func (d *LLMDistiller) extractChunk(
	ctx context.Context, chunk string, targetSize int, stepCtx ResultProcessorContext, callDesc string, coverage float64,
) (string, error) {
	prompt, options := d.buildExtractCall(chunk, targetSize, stepCtx, coverage)

	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}
	start := time.Now()
	invocation := aiInvocation{
		Purpose:        "result-distillation",
		Prompt:         prompt,
		Options:        options,
		DeferRecording: d.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, d.aiClient, invocation)
	var resp *core.AIResponse
	if invocationResult != nil {
		resp = invocationResult.Response
	}
	durMs := time.Since(start).Milliseconds()
	if err == nil && resp != nil {
		// Usage first (the call billed its prompt tokens even when content is unusable),
		// mirroring the single-call path's ordering.
		core.RecordTokenUsage(ctx, "distillation_mapreduce", resp.Usage)
	}
	if err == nil && (resp == nil || strings.TrimSpace(resp.Content) == "") {
		// nil-response and whitespace-only 200s are the same transient class as an error:
		// the nil deref below would otherwise PANIC a worker goroutine, and a "\n" extract
		// previously passed the callers' out != "" checks as a successful chunk.
		err = fmt.Errorf("empty extraction result")
	}
	if err != nil {
		d.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
			Type:            "result_distillation",
			Timestamp:       start,
			DurationMs:      durMs,
			Response:        fmt.Sprintf("[%s FAILED: %s]", callDesc, err.Error()),
			Success:         false,
			Error:           err.Error(),
			StepID:          stepCtx.StepID,
			CallDescription: fmt.Sprintf("Distill %s (%s)", stepCtx.AgentName, callDesc),
		}, invocationResult, invocation, resp, err))
		return "", err
	}
	d.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
		Type:             "result_distillation",
		Timestamp:        start,
		DurationMs:       durMs,
		Response:         resp.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Success:          true,
		StepID:           stepCtx.StepID,
		CallDescription:  fmt.Sprintf("Distill %s (%s)", stepCtx.AgentName, callDesc),
	}, invocationResult, invocation, resp, nil))
	return resp.Content, nil
}

// chunkWholeUnits splits result into chunks of at most chunkBytes, never splitting a
// record. It prefers structural boundaries — top-level JSON array elements, then the
// elements of the dominant array inside a JSON object, then newline-delimited records —
// and only as a last resort does a hard byte split (bounded, may split a record).
// The second return reports whether chunking DISCARDED source content and the third is that
// loss's served-content coverage ratio (1 when nothing was discarded). Since Phase 17 (P17.6)
// the object path REPLICATES the wrapper into every chunk (chunkJSONObjectPreservingWrapper),
// so wrapper keys, sibling arrays, and scalar metadata all reach an LLM — no source field is
// dropped and this path returns (chunks, false, 1). The wrapperDropped plumbing is retained as
// a dead-man's switch: it can only turn true if that full-representation invariant regresses.
// Byte/line splits also keep all content across chunks (false, 1); they are byte-lossless even
// when the chunk boundary lands mid-record.
// The fourth return names the chunking strategy for observability
// ("single" | "array" | "wrapper" | "lines" | "bytes") — the WORST strategy used across the
// top-level split AND any recursive sub-splits (worstChunkStrategy): lines/bytes splits are
// byte-lossless but may tear records mid-JSON, so a structural top-level split whose oversized
// elements degraded to byte-splitting must still surface as "bytes". mapReduceCompact stamps it
// into ResultTrimMetadata and the completion span event — the degraded mode must be
// distinguishable from clean structural chunking.
func chunkWholeUnits(result string, chunkBytes int) ([]string, bool, float64, string) {
	if chunkBytes <= 0 || len(result) <= chunkBytes {
		return []string{result}, false, 1, "single"
	}

	if v, err := unmarshalPreservingNumbers([]byte(result)); err == nil {
		switch t := v.(type) {
		case []interface{}:
			return chunkJSONElements(t, chunkBytes)
		case map[string]interface{}:
			if parent, key, arr := findDominantArray(t); arr != nil {
				// P17.6: preserve the wrapper — every chunk is the whole object with the
				// dominant array replaced by one element-group, so sibling arrays and scalar
				// metadata survive into every chunk (no source field dropped).
				if chunks, strategy, ok := chunkJSONObjectPreservingWrapper(t, parent, key, arr, chunkBytes); ok {
					return chunks, false, 1, strategy
				}
				// Wrapper too heavy to replicate (maxWrapperShare) — fall through to a raw
				// byte/line split below: lossless byte-wise, so still no dropped field.
			}
		}
	}

	if strings.Contains(result, "\n") {
		return chunkByLines(result, chunkBytes), false, 1, "lines"
	}
	return chunkByBytes(result, chunkBytes), false, 1, "bytes"
}

// chunkStrategyRank orders strategies by degradation severity for worst-of composition:
// structural modes (single/wrapper/array) tie at the bottom; line-splitting can tear multi-line
// records; byte-splitting can tear anything, including JSON records and ID digit runs.
var chunkStrategyRank = map[string]int{"single": 0, "wrapper": 0, "array": 0, "lines": 1, "bytes": 2}

// worstChunkStrategy returns the more degraded of two strategies (ties keep a — the caller's
// top-level label), so recursive sub-splits can only make the reported strategy worse.
func worstChunkStrategy(a, b string) string {
	if chunkStrategyRank[b] > chunkStrategyRank[a] {
		return b
	}
	return a
}

// findDominantArray locates the array value holding the bulk of an object's bytes — anywhere in
// the object tree (recursing through nested maps) — and returns its parent map, key, and value,
// so a caller can swap it out in place. Returns a nil array if no array is found. Chunks wrappers
// like {"streams":[...]} or {"data":{"result":[...]}} by their record array.
func findDominantArray(obj map[string]interface{}) (parent map[string]interface{}, key string, arr []interface{}) {
	bestSize := 0
	var walk func(m map[string]interface{})
	walk = func(m map[string]interface{}) {
		for k, val := range m {
			switch t := val.(type) {
			case []interface{}:
				if b, mErr := json.Marshal(t); mErr == nil && len(b) > bestSize {
					bestSize, parent, key, arr = len(b), m, k, t
				}
			case map[string]interface{}:
				walk(t)
			}
		}
	}
	walk(obj)
	return parent, key, arr
}

// maxWrapperShare bounds how much of a chunk the replicated wrapper may occupy. When the object's
// non-dominant-array part (sibling arrays, scalar metadata) serializes to more than this fraction
// of chunkBytes, replicating it into every chunk would multiply token cost instead of bounding it,
// so chunkJSONObjectPreservingWrapper declines and the caller byte-splits the raw serialization
// (lossless byte-wise). An internal algorithmic ratio (cf. degenerateKeptRatio), NOT an operator
// knob — documented as a hardcoded const in LIMITS_CHEATSHEET.md; promote to env only if the
// Phase 17 canary shows a need.
const maxWrapperShare = 0.5

// chunkJSONObjectPreservingWrapper chunks a JSON object by its dominant array WITHOUT dropping
// wrapper/sibling fields: each grouped chunk is the original object with the dominant array
// (parent[key]) replaced by one element-group, so wrapper keys, sibling arrays, and scalar
// metadata are replicated verbatim and no source field is lost. Every chunk stays within
// ~chunkBytes: an element larger than the per-chunk element budget is recursively split into
// standalone (wrapper-less) chunks — the wrapper still reaches an LLM via the grouped chunks or
// the all-oversized fallback emit. Returns ok=false when the wrapper is too large to replicate
// (maxWrapperShare); the caller then byte-splits the raw serialization.
func chunkJSONObjectPreservingWrapper(obj, parent map[string]interface{}, key string, arr []interface{}, chunkBytes int) ([]string, string, bool) {
	orig := parent[key]
	defer func() { parent[key] = orig }()

	strategy := "wrapper" // worst-of composed with recursive sub-splits (worstChunkStrategy)

	// Wrapper size = the whole object with the dominant array emptied. If it dominates the chunk
	// budget, replicating it N times is wasteful — decline so the caller byte-splits instead.
	parent[key] = []interface{}{}
	wrapperBytes, err := json.Marshal(obj)
	if err != nil {
		return nil, strategy, false
	}
	wrapperSize := len(wrapperBytes)
	if wrapperSize > int(maxWrapperShare*float64(chunkBytes)) {
		return nil, strategy, false
	}
	// Budget for the element array within one chunk. The empty array contributes 2 bytes ("[]")
	// to wrapperSize; the real array replaces it, so a group of ≤ (chunkBytes - wrapperSize + 2)
	// array-bytes keeps the whole chunk within chunkBytes.
	budget := chunkBytes - wrapperSize + 2
	if budget < 2 {
		budget = 2
	}

	var chunks []string
	var group []interface{}
	groupSize := 2 // "[]"
	wrapperEmitted := false
	emit := func() {
		if len(group) == 0 {
			return
		}
		parent[key] = group
		if b, mErr := json.Marshal(obj); mErr == nil {
			chunks = append(chunks, string(b))
			wrapperEmitted = true
		}
		group = nil
		groupSize = 2
	}
	for _, el := range arr {
		b, mErr := json.Marshal(el)
		if mErr != nil {
			continue
		}
		sz := len(b) + 1 // element bytes + comma separator
		// An element larger than the per-chunk element budget can't ride in any group — split
		// it recursively into STANDALONE chunks (mirroring chunkJSONElements) so no chunk
		// exceeds ~chunkBytes: shipping it whole would produce an overshoot bounded only by the
		// element size, which can exceed the model context and fail the map call. The sub-chunks
		// don't carry the wrapper (they get the full chunkBytes budget); the wrapper still
		// reaches an LLM via the grouped chunks, or via the fallback emit below when EVERY
		// element is oversized — so no source field is dropped either way.
		if len(b) > budget {
			emit()
			sub, _, _, subStrategy := chunkWholeUnits(string(b), chunkBytes)
			chunks = append(chunks, sub...)
			strategy = worstChunkStrategy(strategy, subStrategy)
			continue
		}
		if len(group) > 0 && groupSize+sz > budget {
			emit()
		}
		group = append(group, el)
		groupSize += sz
	}
	emit()
	if len(chunks) == 0 {
		// Empty dominant array: still emit the wrapper once so its fields survive.
		parent[key] = arr
		if b, mErr := json.Marshal(obj); mErr == nil {
			chunks = append(chunks, string(b))
		}
	} else if !wrapperEmitted {
		// Every element was oversized and split standalone — emit the wrapper once so its fields
		// still reach an LLM (no source field dropped). The dominant array is replaced with a
		// SENTINEL, never left literally empty: an emptied array reads as an authoritative
		// "zero records" claim to the extract LLM (a manufactured false-negative that can survive
		// the reduce), while the sentinel states what actually happened.
		parent[key] = []interface{}{fmt.Sprintf("[%d oversized records split into separate segments]", len(arr))}
		if b, mErr := json.Marshal(obj); mErr == nil {
			chunks = append(chunks, string(b))
		}
	}
	return chunks, strategy, true
}

// chunkJSONElements groups array elements into chunks of at most chunkBytes. Grouped
// elements form a valid JSON array; an element larger than chunkBytes on its own is split
// recursively (it may itself be an array or an object with a dominant array, else a byte
// split) so no chunk exceeds chunkBytes — which keeps every chunk well under the model
// context and ensures a single huge record still reaches the LLM path. Each element is
// marshaled exactly once: the bytes from the size check are reused to assemble the chunk.
func chunkJSONElements(arr []interface{}, chunkBytes int) ([]string, bool, float64, string) {
	var chunks []string
	wrapperDropped := false // set when an oversized element's recursive split discards ITS wrapper
	wrapperCov := 1.0       // min served-coverage across recursive splits (conservative)
	strategy := "array"     // worst-of composed with recursive sub-splits (worstChunkStrategy)
	var group [][]byte      // pre-marshaled element bytes for the current chunk
	groupSize := 2          // "[]"
	flush := func() {
		if len(group) == 0 {
			return
		}
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, b := range group {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(b)
		}
		buf.WriteByte(']')
		chunks = append(chunks, buf.String())
		group = nil
		groupSize = 2
	}
	for _, el := range arr {
		b, err := json.Marshal(el)
		if err != nil {
			continue
		}
		// A single element bigger than the budget can't ride in a group — split it
		// recursively so no chunk exceeds chunkBytes (and an oversized lone record is not
		// left as one over-context chunk, nor collapsed to total==1 in the caller).
		if len(b) > chunkBytes {
			flush()
			sub, subDropped, subCov, subStrategy := chunkWholeUnits(string(b), chunkBytes)
			chunks = append(chunks, sub...)
			wrapperDropped = wrapperDropped || subDropped
			if subCov < wrapperCov {
				wrapperCov = subCov
			}
			strategy = worstChunkStrategy(strategy, subStrategy)
			continue
		}
		sz := len(b) + 1 // element bytes + comma separator
		if len(group) > 0 && groupSize+sz > chunkBytes {
			flush()
		}
		group = append(group, b)
		groupSize += sz
	}
	flush()
	if len(chunks) == 0 {
		return []string{"[]"}, wrapperDropped, wrapperCov, strategy
	}
	return chunks, wrapperDropped, wrapperCov, strategy
}

// chunkByLines groups newline-delimited records into chunks of at most chunkBytes. A
// single line longer than chunkBytes (a giant log record) can't fit any chunk, so it is
// byte-split — otherwise it would form one over-budget chunk that overflows the model
// context or collapses the caller to total<=1.
func chunkByLines(result string, chunkBytes int) []string {
	lines := strings.Split(result, "\n")
	var chunks []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			chunks = append(chunks, b.String())
			b.Reset()
		}
	}
	for _, line := range lines {
		if len(line) > chunkBytes {
			flush()
			chunks = append(chunks, chunkByBytes(line, chunkBytes)...)
			continue
		}
		if b.Len() > 0 && b.Len()+len(line)+1 > chunkBytes {
			flush()
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	flush()
	return chunks
}

// chunkByBytes splits on byte boundaries as a last resort (UTF-8 safe).
func chunkByBytes(result string, chunkBytes int) []string {
	var chunks []string
	for i := 0; i < len(result); {
		end := i + chunkBytes
		if end >= len(result) {
			chunks = append(chunks, result[i:])
			break
		}
		// Avoid splitting a multi-byte UTF-8 rune.
		for end > i && result[end] >= 0x80 && result[end] < 0xC0 {
			end--
		}
		chunks = append(chunks, result[i:end])
		i = end
	}
	return chunks
}
