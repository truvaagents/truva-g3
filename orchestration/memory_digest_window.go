package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// A digest covers a bounded returned window, not an advancing timestamp cursor.
// Event fingerprints include content, so edits, expiry and late arrivals are
// detected without timestamp epsilon rules or unbounded boundary-ID storage.
type digestWindow struct {
	Fingerprint string   `json:"fingerprint"`
	Policy      string   `json:"policy"`
	Events      []string `json:"events"`
	Coverage    string   `json:"coverage"`
}

func (h *MemoryEnrichmentHook) digestWindow(events []core.AgentEvent) (*digestWindow, error) {
	if len(events) == 0 || len(events) > h.compactionRawLimit {
		return nil, fmt.Errorf("digest window outside configured event bound")
	}
	w := &digestWindow{Coverage: "returned_window"}
	if len(events) == h.compactionRawLimit {
		w.Coverage = "limited_window"
	}
	identities := make(map[string]bool, len(events))
	for _, event := range events {
		identity, _ := json.Marshal([]string{event.AgentDomain, event.EventID})
		if event.EventID == "" || identities[string(identity)] {
			return nil, fmt.Errorf("digest window lacks unique event identities")
		}
		identities[string(identity)] = true
		data, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		w.Events = append(w.Events, fmt.Sprintf("%x", sha256.Sum256(data)))
	}
	ordered := append([]string(nil), w.Events...)
	sort.Strings(ordered)
	data, _ := json.Marshal(ordered)
	w.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(data))
	policy, _ := json.Marshal([]any{h.agentDomain, h.lookbackWindow, h.compactionRawLimit, h.compactionMaxTokens})
	w.Policy = fmt.Sprintf("%x", sha256.Sum256(policy))
	return w, nil
}

// windowDelta permits incremental addition only while every previously
// represented event remains in the current window, unchanged. Otherwise a full
// bounded rebaseline replaces the digest; omitted older history is never claimed.
func windowDelta(previous, current *digestWindow, events []core.AgentEvent) ([]core.AgentEvent, bool) {
	if previous == nil || current == nil || previous.Policy != current.Policy || !validDigestWindow(previous) {
		return nil, false
	}
	currentSet := make(map[string]bool, len(current.Events))
	for _, hash := range current.Events {
		currentSet[hash] = true
	}
	previousSet := make(map[string]bool, len(previous.Events))
	for _, hash := range previous.Events {
		if !currentSet[hash] {
			return nil, false
		}
		previousSet[hash] = true
	}
	var delta []core.AgentEvent
	for i, hash := range current.Events {
		if !previousSet[hash] {
			delta = append(delta, events[i])
		}
	}
	return delta, true
}

func validDigestWindow(w *digestWindow) bool {
	if len(w.Events) == 0 || (w.Coverage != "returned_window" && w.Coverage != "limited_window") {
		return false
	}
	ordered := append([]string(nil), w.Events...)
	sort.Strings(ordered)
	for i, hash := range ordered {
		if decoded, err := hex.DecodeString(hash); err != nil || len(decoded) != sha256.Size || (i > 0 && ordered[i-1] == hash) {
			return false
		}
	}
	data, _ := json.Marshal(ordered)
	return w.Fingerprint == fmt.Sprintf("%x", sha256.Sum256(data))
}

func (h *MemoryEnrichmentHook) readRecentWindow(ctx context.Context, since time.Time, limit int) ([]core.AgentEvent, bool, error) {
	started := time.Now()
	var events []core.AgentEvent
	var err error
	reader, observed := h.episodic.(core.ObservedEpisodicReader)
	status := "legacy_unobserved"
	if observed {
		events, err = reader.QueryEventsObserved(ctx, h.agentDomain, core.EventFilter{Since: since, Limit: limit})
		status = "read_succeeded"
	} else {
		events, err = h.episodic.QueryRecentEvents(ctx, h.agentDomain, since, limit)
	}
	if err != nil {
		status = "read_failed"
		telemetry.RecordSpanError(ctx, errors.New("episodic memory read failed"))
	}
	telemetry.AddSpanEvent(ctx, "memory.episodic.read",
		attribute.String("request_id", GetRequestID(ctx)), attribute.String("status", status),
		attribute.Int("returned_events", len(events)), attribute.Int("limit", limit))
	effectStatus := core.PipelineHookEffectSucceeded
	if err != nil {
		effectStatus = core.PipelineHookEffectFailed
	}
	reportStructuredPipelineHookEffect(ctx, "memory_episodic_read", "Episodic read observation", effectStatus,
		"Read-health observation for the bounded retained window", map[string]interface{}{"status": status, "returned_events": len(events), "limit": limit}, err, started)
	return events, observed && err == nil, err
}

func (h *MemoryEnrichmentHook) compactWindow(ctx context.Context, events []core.AgentEvent, observed bool) (digest, path string, cacheHit bool, coverage string) {
	policyFingerprint := ""
	cacheSafe := false // Unknown custom behavior remains usable, but is not cache-equivalent.
	if semantic, ok := h.compactor.(aiSemanticCacheFingerprinter); ok {
		var stable bool
		policyFingerprint, stable = semantic.aiSemanticFingerprint(ctx)
		cacheSafe = observed && stable
	}
	window, windowErr := h.digestWindow(events)
	cacheSafe = cacheSafe && windowErr == nil
	coverage = "unknown"
	if window != nil && observed {
		coverage = window.Coverage
	}
	var cached *cachedDigestData
	if cacheSafe {
		cached, _ = h.getCachedDigest(ctx, policyFingerprint)
	}
	path = "full"
	var delta []core.AgentEvent
	if cached != nil && cached.Window != nil {
		cacheHit = true
		var extends bool
		delta, extends = windowDelta(cached.Window, window, events)
		if extends && cached.Window.Fingerprint == window.Fingerprint {
			telemetry.Counter("orchestration.digest_cache.hit", "module", telemetry.ModuleOrchestration)
			return cached.Content, "cached", true, coverage
		}
		path = "full_recompact"
		if extends && len(delta) > 0 && len(delta) <= h.incrementalThreshold {
			path = "incremental"
		}
	}
	telemetry.Counter("orchestration.digest_cache.miss", "module", telemetry.ModuleOrchestration)
	compactionCtx := ctx
	var producedFingerprint *aiFingerprintCapture
	if policyFingerprint != "" {
		compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
	}
	var err error
	if path == "incremental" {
		digest, err = h.compactor.UpdateDigest(compactionCtx, cached.Content, delta, h.compactionMaxTokens)
	} else {
		digest, err = h.compactor.CompactEvents(compactionCtx, events, h.compactionMaxTokens)
	}
	if err != nil || strings.TrimSpace(digest) == "" {
		// Preserve the prior cache but show current raw evidence, not a stale
		// summary pretending to cover new/expired/changed events.
		return "", "raw_fallback", cacheHit, coverage
	}
	h.storeCachedDigest(ctx, digest, newestEventTS(events), policyFingerprint,
		cacheSafe && producedFingerprint.matches(policyFingerprint), window)
	return digest, path, cacheHit, coverage
}
