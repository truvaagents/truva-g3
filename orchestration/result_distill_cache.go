package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// nonCacheableKey marks a context so a producer can flag its result as not worth caching:
// a fallback after an LLM failure/timeout, or a partial/incomplete compaction. Caching
// such a result would let one transient model/provider hiccup poison repeated scheduled
// runs for the whole TTL instead of retrying distillation. The cache wrapper installs the
// capture; a producer used without the cache finds no capture and marking is a no-op.
type nonCacheableKey struct{}

// withNonCacheableCapture derives a context carrying a flag that producers can set via
// markResultNonCacheable. The returned *bool is safe to read after the inner call returns.
func withNonCacheableCapture(ctx context.Context) (context.Context, *bool) {
	flag := new(bool)
	return context.WithValue(ctx, nonCacheableKey{}, flag), flag
}

// markResultNonCacheable flags the current result as non-cacheable. No-op if the context
// was not prepared by the cache wrapper (e.g. the distiller is used without a cache).
func markResultNonCacheable(ctx context.Context) {
	if flag, ok := ctx.Value(nonCacheableKey{}).(*bool); ok {
		*flag = true
	}
}

// distillCacheKey derives a stable, content-addressed cache key for a compaction.
//
// The key folds in everything that changes the output: the step instruction (the
// downstream task the model compacts toward), the per-result byte budget (the output
// is bounded by it, so a smaller budget must not reuse a larger result), and the raw
// result bytes. Identical (result, instruction, budget) tuples — the norm for
// scheduled/repetitive runs, which are the worst offenders for redundant LLM cost —
// collapse to one key, so the expensive compaction runs once and every later run is a
// hit. The agent name is deliberately excluded so the cache can be shared across pods
// and agents in a domain (it appears only as a context hint in the prompt, not as the
// thing being compacted).
func distillCacheKey(response, instruction, originalQuery string, maxBytes int) string {
	h := sha256.New()
	h.Write([]byte(instruction))
	h.Write([]byte{0}) // separator so ("ab","") != ("a","b")
	h.Write([]byte(originalQuery))
	h.Write([]byte{0}) // the user goal changes what the distiller selects, so it changes the output
	h.Write([]byte(strconv.Itoa(maxBytes)))
	h.Write([]byte{0})
	h.Write([]byte(response))
	return "distill:" + hex.EncodeToString(h.Sum(nil))
}

// distillKeySalt derives the config-level portion of the cache key: inputs that change
// the distilled output but are constant across calls. Folding these in means a config or
// prompt change does not serve output produced under the old config for the TTL:
//   - prompt template version (bump distillPromptVersion when the prompt changes);
//   - model (different models distill differently);
//   - target size (bounds output when below the per-result budget, so not otherwise keyed);
//   - pre-filter budget (sets how much input the LLM actually sees, see result_distiller.go);
//   - the per-phase AI options override (model / max tokens / temperature / system prompt …),
//     which can change the model and sampling out from under cfg.Model.
//
// Per-call context (agent, capability) is intentionally excluded: the cached value is the
// distilled CONTENT, driven by the result + instruction, not by those prompt hints — so the
// cache stays shareable across agents and pods.
func distillKeySalt(cfg ResultDistillConfig, opts *AIOptionsOverride) string {
	h := sha256.New()
	h.Write([]byte(distillPromptVersion))
	h.Write([]byte{0})
	h.Write([]byte(cfg.Model))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(cfg.TargetSize)))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(cfg.PreFilterBudget)))
	h.Write([]byte{0})
	if opts != nil {
		// AIOptionsOverride is JSON-tagged; json.Marshal sorts map keys, so this is stable.
		if b, err := json.Marshal(opts); err == nil {
			h.Write(b)
		}
	}
	return "cfg:" + hex.EncodeToString(h.Sum(nil)) + "|"
}

// cachingProcessor wraps a ResultProcessor with a content-addressed cache.
//
// It is fail-open in every direction: NewCachingProcessor unwraps a nil cache, and any
// cache Get/Set error falls through to the inner processor — a cache problem never fails
// synthesis. Passthrough-sized results (≤ budget) skip the cache entirely: the inner
// processor does no work on them, so there is nothing worth caching.
type cachingProcessor struct {
	inner    ResultProcessor
	cache    core.DigestCache
	ttl      time.Duration
	minBytes int    // below this the inner does no expensive (LLM) work — skip the cache
	keySalt  string // config-level key prefix (prompt version, model, target size)
	logger   core.Logger
}

// NewCachingProcessor wraps inner with a distillation cache. A nil cache returns inner
// unchanged (fail-open — no wrapper overhead when caching is disabled). minBytes is the
// input size at/above which the inner does expensive work worth caching (the distiller's
// DistillThreshold); smaller inputs bypass the cache since recomputing them is cheap.
// keySalt namespaces entries by output-affecting config (see distillKeySalt) so a
// model/target/prompt change cannot serve stale output.
func NewCachingProcessor(inner ResultProcessor, cache core.DigestCache, ttl time.Duration, minBytes int, keySalt string, logger core.Logger) ResultProcessor {
	if cache == nil {
		return inner
	}
	return &cachingProcessor{inner: inner, cache: cache, ttl: ttl, minBytes: minBytes, keySalt: keySalt, logger: logger}
}

// EffectiveSize forwards to the wrapped processor so budget allocation sees the post-distill
// footprint through the cache layer (Phase 9). Implements EffectiveSizer.
func (c *cachingProcessor) EffectiveSize(rawSize int) int {
	if sizer, ok := c.inner.(EffectiveSizer); ok {
		return sizer.EffectiveSize(rawSize)
	}
	return rawSize
}

// distillEnvelopeVersion is the on-disk format version of distillCacheEnvelope. Bump it if the
// stored shape changes; a decode mismatch is treated as a cache miss (fail-open).
const distillEnvelopeVersion = 1

// distillCacheEnvelope is what the cache stores: the distilled OUTPUT plus its trim METADATA, so a
// cache hit can replay coverage metadata (a bare string cannot — the most repetitive, cached
// workloads would otherwise be unauditable for sampling). Legacy bare-string entries become
// unreachable via the Phase 16 distillPromptVersion salt bump, so no dual-format reader is needed.
// (Phase 16)
type distillCacheEnvelope struct {
	V    int                `json:"v"`
	Out  string             `json:"out"`
	Meta ResultTrimMetadata `json:"meta"`
}

// ProcessForPrompt returns a cached compaction when one exists for this
// (result, instruction, budget), otherwise runs the inner processor and stores the
// result. Caching is skipped for passthrough-sized inputs.
func (c *cachingProcessor) ProcessForPrompt(
	ctx context.Context, result string, maxBytes int, stepCtx ResultProcessorContext,
) string {
	// Below the inner's work threshold the inner does no expensive (LLM) compaction —
	// just a cheap structural pass or passthrough — so caching it adds Redis traffic for
	// little gain. Gate on minBytes (the distiller's DistillThreshold), NOT maxBytes: a
	// result larger than the threshold is distilled by an LLM even when it is within the
	// per-result budget, and those are exactly the (paid) calls worth caching.
	if len(result) < c.minBytes {
		return c.inner.ProcessForPrompt(ctx, result, maxBytes, stepCtx)
	}

	key := c.keySalt + distillCacheKey(result, stepCtx.Instruction, stepCtx.OriginalQuery, maxBytes)

	if data, err := c.cache.GetDigest(ctx, key); err == nil && len(data) > 0 {
		var env distillCacheEnvelope
		if json.Unmarshal(data, &env) == nil && env.V == distillEnvelopeVersion {
			telemetry.AddSpanEvent(ctx, "result_distill.cache_hit",
				attribute.String("request_id", requestIDFromBaggage(ctx)),
				attribute.String("step_id", stepCtx.StepID),
				attribute.Int("cached_bytes", len(env.Out)), // output size, NOT the serialized envelope
			)
			if registry := core.GetGlobalMetricsRegistry(); registry != nil {
				registry.Counter("orchestration.result_distill.cache_hit", "agent_name", stepCtx.AgentName)
			}
			captureTrimMetadata(ctx, env.Meta) // cached workloads stay auditable for coverage (Phase 16)
			return env.Out
		}
		// Undecodable entry — after the Phase 16 distillPromptVersion salt bump made legacy bare-string
		// entries unreachable, a decode failure is real corruption, not a migration artifact. Recompute.
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Discarding undecodable distillation cache entry, recomputing", map[string]interface{}{
				"operation":  "result_distill.cache_envelope",
				"request_id": requestIDFromBaggage(ctx),
				"step_id":    stepCtx.StepID,
				"error_type": "cache_decode",
			})
		}
	}

	// Capture whether the inner processor produced a non-cacheable result (a fallback
	// after LLM failure/timeout, or a partial/incomplete map-reduce). Such results must
	// not be cached, or a transient hiccup would be served for the full TTL. Also nest a trim-metadata
	// capture so the inner's coverage metadata can be stored in the envelope, then re-propagate it to
	// the caller's slot (the nested capture shadowed it). (Phase 16)
	innerCtx, nonCacheable := withNonCacheableCapture(ctx)
	innerCtx, meta := WithTrimMetadataCapture(innerCtx)
	out := c.inner.ProcessForPrompt(innerCtx, result, maxBytes, stepCtx)
	captureTrimMetadata(ctx, *meta)

	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("orchestration.result_distill.cache_miss", "agent_name", stepCtx.AgentName)
	}

	if *nonCacheable {
		return out
	}

	blob, mErr := json.Marshal(distillCacheEnvelope{V: distillEnvelopeVersion, Out: out, Meta: *meta})
	if mErr != nil {
		return out // fail-open: serve the result uncached rather than cache garbage
	}
	if err := c.cache.SetDigest(ctx, key, blob, c.ttl); err != nil {
		// Fail-open: a cache write failure must never affect the returned compaction.
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Failed to cache distillation result", map[string]interface{}{
				"operation":  "result_distill.cache_set",
				"request_id": requestIDFromBaggage(ctx),
				"step_id":    stepCtx.StepID,
				"error":      err.Error(),
				"error_type": "cache_write",
			})
		}
	}
	return out
}

// SetLLMDebugStore forwards the debug store to the wrapped processor when it supports
// one, so the orchestrator's propagation reaches the inner distiller even though the
// public result processor is now this cache wrapper.
func (c *cachingProcessor) SetLLMDebugStore(store LLMDebugStore) {
	if d, ok := c.inner.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
		d.SetLLMDebugStore(store)
	}
}

// Shutdown forwards shutdown to the wrapped processor (e.g. to flush the distiller's
// in-flight debug recordings) when it supports one.
func (c *cachingProcessor) Shutdown() {
	if d, ok := c.inner.(interface{ Shutdown() }); ok {
		d.Shutdown()
	}
}
