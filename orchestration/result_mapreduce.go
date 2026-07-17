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
	chunks, wrapperDropped, wrapperCov := chunkWholeUnits(result, chunkBytes)
	total := len(chunks)
	if total <= 1 {
		// Nothing to fan out — treat as a single call's input via the floor.
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
			llmInputBytes.Add(int64(len(chunk))) // bytes sent, counted pre-dispatch (Phase 16)
			out, err := d.extractChunk(mrCtx, chunk, perChunkTarget, stepCtx, fmt.Sprintf("map-reduce chunk %d/%d", i+1, total))
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
	if len(combined) > targetSize {
		if estimateTokens(combined) <= d.config.ModelContextTokens {
			llmInputBytes.Add(int64(len(combined))) // reduce-call input is LLM volume too (Phase 16)
			if reduced, err := d.extractChunk(mrCtx, combined, targetSize, stepCtx, "map-reduce reduce"); err == nil && reduced != "" {
				combined = reduced
			} else {
				// Transient reduce failure (provider 429/timeout/empty): the truncation is a
				// degraded fallback — recompute next time rather than serve it from cache for
				// the TTL. The deterministic over-context truncation below stays cacheable.
				combined = truncateResultBytes(combined, targetSize)
				combineTruncated = true
				markResultNonCacheable(ctx)
			}
		} else {
			combined = truncateResultBytes(combined, targetSize)
			combineTruncated = true
		}
	}

	// Phase 16 — compose ALL applicable disclosures and append ONCE: a wrapper drop lost SOURCE
	// fields, combine-truncation dropped extracted FINDINGS, partial-on-timeout dropped whole
	// SEGMENTS — distinct losses, each carrying UNKNOWN. The targetSize re-bound applies ONLY
	// when the body was already deterministically truncated; LLM-analyzed output always gets a
	// plain append (bounded overshoot) — never cut analyzed output to fit a note.
	partial := completed < total
	// Coverage basis: segment-count by default (byte-weighting is confounded once the hardened
	// chunker replicates wrappers — see Phase 17). On a wrapper drop, dominantArray served only
	// the record array, so the segment ratio would read 1.0 while wrapper keys, sibling arrays,
	// and scalar metadata never reached any LLM — the chunker reports the compact-basis served
	// ratio for exactly that loss. The DISCLOSURE states the pure byte figure (its wording says
	// "by bytes"; segment losses get their own note below), while the metadata records the
	// byte×segment product as the total-source-seen estimate — so a consumer reading
	// SourceCoverageRatio alone is never told "full" about a partial source.
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

	telemetry.AddSpanEvent(ctx, "result_distill.mapreduce_complete",
		attribute.String("request_id", requestIDFromBaggage(ctx)),
		attribute.String("step_id", stepCtx.StepID),
		attribute.Int("chunks_total", total),
		attribute.Int("chunks_completed", completed),
		attribute.Int("combined_bytes", len(combined)),
		attribute.Int("llm_input_bytes", int(llmInputBytes.Load())),
	)
	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("orchestration.result_distill.mapreduce", "agent_name", stepCtx.AgentName)
	}

	captureTrimMetadata(ctx, ResultTrimMetadata{
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
		ContentLost:         partial || combineTruncated || wrapperDropped,
	})
	return combined
}

// extractChunk runs the extractive distillation prompt on a single chunk (or, for the reduce
// step, the joined chunk extracts). It inherits the caller's (shared) deadline via ctx — it does
// not impose its own. callDesc labels the call ("map-reduce chunk i/N" or "map-reduce reduce") for
// the LLM-Debug record. Each call records a typed result_distillation interaction here for parity
// with the single-call path (the generic agent_llm_call is deferred). (Phase 12)
func (d *LLMDistiller) extractChunk(
	ctx context.Context, chunk string, targetSize int, stepCtx ResultProcessorContext, callDesc string,
) (string, error) {
	// coverage=1.0: each chunk is shown to the LLM in full, so no partial-source note (map-reduce
	// segment coverage is tracked separately via SegmentsAnalyzed/SegmentsTotal metadata). (Phase 16)
	prompt := d.buildDistillationPrompt(chunk, targetSize, stepCtx, 1.0)
	maxTokens := targetSize/3 + 100
	if maxTokens < 500 {
		maxTokens = 500
	}
	baseOptions := &core.AIOptions{Temperature: 0.1, MaxTokens: maxTokens, Model: d.config.Model, SystemPrompt: distillationSystemPrompt}
	options := mergeAIOptions(baseOptions, d.aiOptionsOverride)

	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}
	start := time.Now()
	resp, err := d.aiClient.GenerateResponse(d.deferLLMRecordingIfWeWillRecord(ctx), prompt, options)
	durMs := time.Since(start).Milliseconds()
	if err != nil {
		d.recordDebugInteraction(ctx, requestID, LLMInteraction{
			Type:            "result_distillation",
			Timestamp:       start,
			DurationMs:      durMs,
			Prompt:          prompt,
			SystemPrompt:    distillationSystemPrompt,
			Response:        fmt.Sprintf("[%s FAILED: %s]", callDesc, err.Error()),
			Model:           options.Model,
			Temperature:     float64(options.Temperature),
			MaxTokens:       options.MaxTokens,
			Success:         false,
			Error:           err.Error(),
			StepID:          stepCtx.StepID,
			CallDescription: fmt.Sprintf("Distill %s (%s)", stepCtx.AgentName, callDesc),
		})
		return "", err
	}
	core.RecordTokenUsage(ctx, "distillation_mapreduce", resp.Usage)
	d.recordDebugInteraction(ctx, requestID, LLMInteraction{
		Type:             "result_distillation",
		Timestamp:        start,
		DurationMs:       durMs,
		Prompt:           prompt,
		SystemPrompt:     distillationSystemPrompt,
		Response:         resp.Content,
		Model:            resp.Model,
		Provider:         resp.Provider,
		Temperature:      float64(options.Temperature),
		MaxTokens:        options.MaxTokens,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Success:          true,
		StepID:           stepCtx.StepID,
		CallDescription:  fmt.Sprintf("Distill %s (%s)", stepCtx.AgentName, callDesc),
	})
	return resp.Content, nil
}

// chunkWholeUnits splits result into chunks of at most chunkBytes, never splitting a
// record. It prefers structural boundaries — top-level JSON array elements, then the
// elements of the dominant array inside a JSON object, then newline-delimited records —
// and only as a last resort does a hard byte split (bounded, may split a record).
// The second return reports whether chunking DISCARDED source content: the dominant-array
// path serves only the array, so wrapper keys, sibling arrays, and scalar metadata never
// reach any LLM — a real loss the caller must disclose until the Phase 17 hardened chunker
// replicates wrappers per chunk. Byte/line splits keep all content across chunks (false).
// The third return is the served-content coverage ratio for that loss (1 when nothing was
// discarded), measured COMPACT-vs-COMPACT: dividing re-marshaled chunk bytes by the raw
// (possibly indented/escape-heavy) source would understate coverage by the formatting
// overhead, not by lost content — a near-complete analysis must never read as "~9% seen".
func chunkWholeUnits(result string, chunkBytes int) ([]string, bool, float64) {
	if chunkBytes <= 0 || len(result) <= chunkBytes {
		return []string{result}, false, 1
	}

	var v interface{}
	if json.Unmarshal([]byte(result), &v) == nil {
		switch t := v.(type) {
		case []interface{}:
			return chunkJSONElements(t, chunkBytes)
		case map[string]interface{}:
			if arr := dominantArray(t); arr != nil {
				chunks, _, subCov := chunkJSONElements(arr, chunkBytes)
				cov := 0.99
				arrBytes, arrErr := json.Marshal(arr)
				objBytes, objErr := json.Marshal(t)
				if arrErr == nil && objErr == nil {
					cov = lossyByteCoverage(len(arrBytes), len(objBytes))
				}
				if subCov < cov {
					cov = subCov // a nested drop compounds; min is the conservative bound
				}
				return chunks, true, cov
			}
		}
	}

	if strings.Contains(result, "\n") {
		return chunkByLines(result, chunkBytes), false, 1
	}
	return chunkByBytes(result, chunkBytes), false, 1
}

// dominantArray returns the array value that holds the bulk of an object's bytes, or nil
// if no array dominates. Used to chunk wrappers like {"streams":[...]} or
// {"data":{"result":[...]}} by their record array.
func dominantArray(obj map[string]interface{}) []interface{} {
	var best []interface{}
	bestSize := 0
	for _, val := range obj {
		switch t := val.(type) {
		case []interface{}:
			if b, _ := json.Marshal(t); len(b) > bestSize {
				bestSize, best = len(b), t
			}
		case map[string]interface{}:
			if inner := dominantArray(t); inner != nil {
				if b, _ := json.Marshal(inner); len(b) > bestSize {
					bestSize, best = len(b), inner
				}
			}
		}
	}
	return best
}

// chunkJSONElements groups array elements into chunks of at most chunkBytes. Grouped
// elements form a valid JSON array; an element larger than chunkBytes on its own is split
// recursively (it may itself be an array or an object with a dominant array, else a byte
// split) so no chunk exceeds chunkBytes — which keeps every chunk well under the model
// context and ensures a single huge record still reaches the LLM path. Each element is
// marshaled exactly once: the bytes from the size check are reused to assemble the chunk.
func chunkJSONElements(arr []interface{}, chunkBytes int) ([]string, bool, float64) {
	var chunks []string
	wrapperDropped := false // set when an oversized element's recursive split discards ITS wrapper
	wrapperCov := 1.0       // min served-coverage across recursive splits (conservative)
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
			sub, subDropped, subCov := chunkWholeUnits(string(b), chunkBytes)
			chunks = append(chunks, sub...)
			wrapperDropped = wrapperDropped || subDropped
			if subCov < wrapperCov {
				wrapperCov = subCov
			}
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
		return []string{"[]"}, wrapperDropped, wrapperCov
	}
	return chunks, wrapperDropped, wrapperCov
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
