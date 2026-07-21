package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const minConversationTokenBudget = 2000

type ConversationHistoryPreparer interface {
	PrepareFromText(ctx context.Context, sessionKey string, formatted string) (string, HistoryPreparationResult, error)
	PrepareFromTurns(ctx context.Context, sessionKey string, turns []core.ConversationTurn) (string, HistoryPreparationResult, error)
}

type HistoryPreparationResult struct {
	Path                 string
	TurnsRaw             int
	TurnsKeptVerbatim    int
	TurnsCompacted       int
	TurnsDropped         int
	ElidedTurns          int
	EstimatedTokensPre   int
	EstimatedTokensPost  int
	CompactionDurationMs int64
	Budget               int
	Compacted            bool
	Truncated            bool
	TokenCounterName     string
}

type ConversationHistoryProcessor struct {
	tokenBudget          int
	recentTurnsPreserved int
	tokenCounter         core.TokenCounter
	compactor            core.ConversationCompactor
	summaryCache         *SummaryCache
	logger               core.Logger
	telemetry            core.Telemetry
	debugStore           LLMDebugStore
	agentName            string
	debugWg              sync.WaitGroup
}

type ConversationHistoryProcessorConfig struct {
	TokenBudget          int
	RecentTurnsPreserved int
}

type ConversationHistoryProcessorOption func(*ConversationHistoryProcessor) error

func BuildConversationHistoryProcessor(
	config *OrchestratorConfig,
	opts ...ConversationHistoryProcessorOption,
) (*ConversationHistoryProcessor, error) {
	defaults := DefaultConfig()
	if config == nil {
		config = defaults
	}
	tokenBudget := config.ConversationTokenBudget
	if tokenBudget == 0 {
		tokenBudget = defaults.ConversationTokenBudget
	}
	recentTurnsPreserved := config.ConversationRecentTurnsPreserved
	if recentTurnsPreserved == 0 {
		recentTurnsPreserved = defaults.ConversationRecentTurnsPreserved
	}

	processor, err := NewConversationHistoryProcessor(ConversationHistoryProcessorConfig{
		TokenBudget:          tokenBudget,
		RecentTurnsPreserved: recentTurnsPreserved,
	}, opts...)
	if err != nil {
		return nil, err
	}
	processor.agentName = config.Name
	return processor, nil
}

// BuildCompactionEnabledConversationHistoryPreparer builds a shared
// conversation-history preparer with Tier 2 recursive compaction enabled.
//
// The helper keeps Layer 2 ergonomic: it wires a default SummaryCache and
// LLMConversationCompactor from resolved config and aiClient, then applies any
// caller-supplied options last so custom cache/compactor plugs can override the
// defaults without abandoning the convenience path.
func BuildCompactionEnabledConversationHistoryPreparer(
	config *OrchestratorConfig,
	aiClient core.AIClient,
	opts ...ConversationHistoryProcessorOption,
) (*ConversationHistoryProcessor, error) {
	defaults := DefaultConfig()
	if config == nil {
		config = defaults
	}

	cacheSize := config.ConversationSummaryCacheSize
	if cacheSize == 0 {
		cacheSize = defaults.ConversationSummaryCacheSize
	}
	cache, err := NewSummaryCache(cacheSize)
	if err != nil {
		return nil, err
	}

	compactor, err := NewLLMConversationCompactor(aiClient, nil)
	if err != nil {
		return nil, err
	}

	defaultOpts := []ConversationHistoryProcessorOption{
		WithConversationSummaryCache(cache),
		WithConversationCompactor(compactor),
	}
	defaultOpts = append(defaultOpts, opts...)
	return BuildConversationHistoryProcessor(config, defaultOpts...)
}

func NewConversationHistoryProcessor(
	cfg ConversationHistoryProcessorConfig,
	opts ...ConversationHistoryProcessorOption,
) (*ConversationHistoryProcessor, error) {
	if cfg.TokenBudget < minConversationTokenBudget {
		return nil, fmt.Errorf("conversation token budget must be >= %d, got %d", minConversationTokenBudget, cfg.TokenBudget)
	}
	if cfg.RecentTurnsPreserved < 1 {
		return nil, fmt.Errorf("conversation recent turns preserved must be >= 1, got %d", cfg.RecentTurnsPreserved)
	}

	p := &ConversationHistoryProcessor{
		tokenBudget:          cfg.TokenBudget,
		recentTurnsPreserved: cfg.RecentTurnsPreserved,
		tokenCounter:         HeuristicTokenCounter{},
		logger:               &core.NoOpLogger{},
		telemetry:            &core.NoOpTelemetry{},
	}
	for _, opt := range opts {
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func WithConversationTokenCounter(counter core.TokenCounter) ConversationHistoryProcessorOption {
	return func(p *ConversationHistoryProcessor) error {
		if counter == nil {
			return fmt.Errorf("conversation token counter cannot be nil")
		}
		p.tokenCounter = counter
		return nil
	}
}

func WithConversationCompactor(compactor core.ConversationCompactor) ConversationHistoryProcessorOption {
	return func(p *ConversationHistoryProcessor) error {
		if compactor == nil {
			return fmt.Errorf("conversation compactor cannot be nil")
		}
		p.compactor = compactor
		if aware, ok := compactor.(interface{ SetLogger(core.Logger) }); ok {
			aware.SetLogger(p.logger)
		}
		if aware, ok := compactor.(interface{ SetTelemetry(core.Telemetry) }); ok {
			aware.SetTelemetry(p.telemetry)
		}
		if aware, ok := compactor.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok && p.debugStore != nil {
			aware.SetLLMDebugStore(p.debugStore)
		}
		return nil
	}
}

func WithConversationSummaryCache(cache *SummaryCache) ConversationHistoryProcessorOption {
	return func(p *ConversationHistoryProcessor) error {
		if cache == nil {
			return fmt.Errorf("conversation summary cache cannot be nil")
		}
		p.summaryCache = cache
		return nil
	}
}

func (p *ConversationHistoryProcessor) SetLogger(logger core.Logger) {
	if logger == nil {
		p.logger = &core.NoOpLogger{}
	} else if cal, ok := logger.(core.ComponentAwareLogger); ok {
		p.logger = cal.WithComponent("framework/orchestration")
	} else {
		p.logger = logger
	}
	if aware, ok := p.compactor.(interface{ SetLogger(core.Logger) }); ok {
		aware.SetLogger(logger)
	}
}

func (p *ConversationHistoryProcessor) SetTelemetry(t core.Telemetry) {
	if t == nil {
		p.telemetry = &core.NoOpTelemetry{}
	} else {
		p.telemetry = t
	}
	if aware, ok := p.compactor.(interface{ SetTelemetry(core.Telemetry) }); ok {
		aware.SetTelemetry(p.telemetry)
	}
}

func (p *ConversationHistoryProcessor) SetLLMDebugStore(store LLMDebugStore) {
	p.debugStore = store
	if aware, ok := p.compactor.(interface{ SetLLMDebugStore(LLMDebugStore) }); ok {
		aware.SetLLMDebugStore(store)
	}
}

func (p *ConversationHistoryProcessor) PrepareFromText(ctx context.Context, sessionKey string, formatted string) (string, HistoryPreparationResult, error) {
	return p.prepareFromTextWithPath(ctx, sessionKey, formatted, "metadata_text")
}

func (p *ConversationHistoryProcessor) prepareFromTextWithPath(
	ctx context.Context,
	sessionKey string,
	formatted string,
	path string,
) (string, HistoryPreparationResult, error) {
	startTime := time.Now()
	ctx, span := startPrepareSpan(ctx, p.telemetry, path)
	defer span.End()

	result := HistoryPreparationResult{
		Path:   path,
		Budget: p.tokenBudget,
	}
	prepared, result := p.applyTier1ToText(ctx, formatted, result)
	p.logPreparationOutcome(ctx, result, startTime, nil)
	p.recordPreparationInteraction(ctx, startTime, result)
	emitConversationHistoryMetrics(p.agentName, result, outcomeFromResult(result), "", p.summaryCacheLen())
	return prepared, result, nil
}

func (p *ConversationHistoryProcessor) PrepareFromTurns(ctx context.Context, sessionKey string, turns []core.ConversationTurn) (string, HistoryPreparationResult, error) {
	return p.prepareFromTurnsWithPath(ctx, sessionKey, turns, "metadata_turns")
}

func (p *ConversationHistoryProcessor) prepareFromTurnsWithPath(
	ctx context.Context,
	sessionKey string,
	turns []core.ConversationTurn,
	path string,
) (string, HistoryPreparationResult, error) {
	startTime := time.Now()
	ctx, span := startPrepareSpan(ctx, p.telemetry, path)
	defer span.End()

	result := HistoryPreparationResult{
		Path:     path,
		Budget:   p.tokenBudget,
		TurnsRaw: len(turns),
	}
	if len(turns) == 0 {
		p.logPreparationOutcome(ctx, result, startTime, nil)
		p.recordPreparationInteraction(ctx, startTime, result)
		emitConversationHistoryMetrics(p.agentName, result, outcomeFromResult(result), "", p.summaryCacheLen())
		return "", result, nil
	}

	summary := ""
	recentTurns := cloneConversationTurns(turns)
	compactionOutcome := ""
	if sessionKey != "" && p.compactor != nil && p.summaryCache != nil && len(turns) > p.recentTurnsPreserved {
		var compacted bool
		summary, recentTurns, result, compactionOutcome, compacted = p.tryRecursiveCompaction(ctx, sessionKey, turns, result)
		if !compacted {
			recentTurns = cloneConversationTurns(turns)
		}
	}

	prepared, result := p.applyTier1ToTurns(ctx, summary, recentTurns, result)
	p.logPreparationOutcome(ctx, result, startTime, nil)
	p.recordPreparationInteraction(ctx, startTime, result)
	emitConversationHistoryMetrics(p.agentName, result, outcomeFromResult(result), compactionOutcome, p.summaryCacheLen())
	return prepared, result, nil
}

func (p *ConversationHistoryProcessor) tryRecursiveCompaction(
	ctx context.Context,
	sessionKey string,
	turns []core.ConversationTurn,
	result HistoryPreparationResult,
) (string, []core.ConversationTurn, HistoryPreparationResult, string, bool) {
	if len(turns) <= p.recentTurnsPreserved {
		return "", cloneConversationTurns(turns), result, "", false
	}

	boundary := len(turns) - p.recentTurnsPreserved
	policyFingerprint := ""
	cacheSafe := true
	if semantic, hasAI := p.compactor.(aiSemanticCacheFingerprinter); hasAI {
		policyFingerprint, cacheSafe = semantic.aiSemanticFingerprint(ctx)
	}
	state, ok := SummaryState{}, false
	if cacheSafe {
		state, ok = p.summaryCache.Get(sessionKey)
		if ok && state.PolicyFingerprint != policyFingerprint {
			p.summaryCache.Delete(sessionKey)
			state, ok = SummaryState{}, false
		}
	}
	if ok && !watermarkMatches(state, turns) {
		err := fmt.Errorf("conversation watermark mismatch for session %s", sessionKey)
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "conversation_history.watermark_reset",
			attribute.String("request_id", requestIDFromBaggage(ctx)),
			attribute.String("error", err.Error()),
		)
		if p.logger != nil {
			p.logger.WarnWithContext(ctx, "Conversation summary watermark mismatch; resetting cached summary", map[string]interface{}{
				"operation":           "conversation_history",
				"request_id":          requestIDFromBaggage(ctx),
				"original_request_id": originalRequestIDFromBaggage(ctx),
				"error":               err.Error(),
				"error_type":          "watermark_mismatch",
				"session_key":         sessionKey,
			})
		}
		p.summaryCache.Delete(sessionKey)
		state = SummaryState{}
		ok = false
	}

	if !ok {
		compactionStart := time.Now()
		compactionCtx := ctx
		var producedFingerprint *aiFingerprintCapture
		if policyFingerprint != "" {
			compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
		}
		summary, compacted, success := p.compactTurns(compactionCtx, "", turns[:boundary])
		result.CompactionDurationMs += time.Since(compactionStart).Milliseconds()
		if !success {
			return "", cloneConversationTurns(turns), result, "error", false
		}
		if compacted == 0 || summary == "" {
			return "", cloneConversationTurns(turns), result, "error", false
		}
		state = SummaryState{
			Summary:             summary,
			LastTurnFingerprint: fingerprintTurn(turns[boundary-1]),
			LastTurnOrdinal:     boundary,
			LastCompactedCount:  boundary,
			PolicyFingerprint:   policyFingerprint,
		}
		if cacheSafe && producedFingerprint.matches(policyFingerprint) {
			p.summaryCache.Set(sessionKey, state)
		}
		result.Compacted = true
		result.TurnsCompacted = boundary
		return state.Summary, cloneConversationTurns(turns[boundary:]), result, "success", true
	}

	if boundary <= state.LastCompactedCount {
		result.TurnsCompacted = minConversationInt(boundary, state.LastCompactedCount)
		result.Compacted = result.TurnsCompacted > 0
		return state.Summary, cloneConversationTurns(turns[boundary:]), result, "", true
	}

	compactionStart := time.Now()
	compactionCtx := ctx
	var producedFingerprint *aiFingerprintCapture
	if policyFingerprint != "" {
		compactionCtx, producedFingerprint = withAIFingerprintCapture(ctx)
	}
	summary, compacted, success := p.compactTurns(compactionCtx, state.Summary, turns[state.LastCompactedCount:boundary])
	result.CompactionDurationMs += time.Since(compactionStart).Milliseconds()
	if !success {
		return "", cloneConversationTurns(turns), result, "error", false
	}
	if compacted == 0 || summary == "" {
		return "", cloneConversationTurns(turns), result, "error", false
	}

	state = SummaryState{
		Summary:             summary,
		LastTurnFingerprint: fingerprintTurn(turns[boundary-1]),
		LastTurnOrdinal:     boundary,
		LastCompactedCount:  boundary,
		PolicyFingerprint:   policyFingerprint,
	}
	if cacheSafe && producedFingerprint.matches(policyFingerprint) {
		p.summaryCache.Set(sessionKey, state)
	}
	result.Compacted = true
	result.TurnsCompacted = boundary
	return state.Summary, cloneConversationTurns(turns[boundary:]), result, "success", true
}

func (p *ConversationHistoryProcessor) compactTurns(
	ctx context.Context,
	priorSummary string,
	newTurns []core.ConversationTurn,
) (string, int, bool) {
	if len(newTurns) == 0 {
		return priorSummary, 0, true
	}
	summary, err := p.compactor.Compact(ctx, priorSummary, cloneConversationTurns(newTurns))
	if err != nil {
		return "", 0, false
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", 0, false
	}
	return summary, len(newTurns), true
}

func (p *ConversationHistoryProcessor) applyTier1ToText(
	ctx context.Context,
	formatted string,
	result HistoryPreparationResult,
) (string, HistoryPreparationResult) {
	tokens, counterName := p.estimateTokens(ctx, formatted)
	result.TokenCounterName = counterName
	result.EstimatedTokensPre = tokens
	result.EstimatedTokensPost = tokens
	if tokens <= p.tokenBudget {
		return formatted, result
	}

	truncated := hardTruncateText(formatted, p.tokenBudget)
	result.Truncated = true
	result.EstimatedTokensPost, _ = p.estimateTokens(ctx, truncated)
	return truncated, result
}

func (p *ConversationHistoryProcessor) applyTier1ToTurns(
	ctx context.Context,
	summary string,
	turns []core.ConversationTurn,
	result HistoryPreparationResult,
) (string, HistoryPreparationResult) {
	working := cloneConversationTurns(turns)
	formatted := formatSummaryAndTurns(summary, working)
	tokens, counterName := p.estimateTokens(ctx, formatted)
	result.TokenCounterName = counterName
	result.EstimatedTokensPre = tokens
	result.EstimatedTokensPost = tokens
	result.TurnsKeptVerbatim = len(working)
	if tokens <= p.tokenBudget {
		return formatted, result
	}

	for tokens > p.tokenBudget && len(working) > p.recentTurnsPreserved {
		working = cloneConversationTurns(working[1:])
		result.TurnsDropped++
		result.TurnsKeptVerbatim = len(working)
		formatted = formatSummaryAndTurns(summary, working)
		tokens, _ = p.estimateTokens(ctx, formatted)
	}

	elided := map[int]bool{}
	for tokens > p.tokenBudget {
		changed := false
		for i := range working {
			nextContent, didElide := elideMiddle(working[i].Content)
			if !didElide {
				continue
			}
			working[i].Content = nextContent
			if !elided[i] {
				elided[i] = true
				result.ElidedTurns++
			}
			changed = true
			formatted = formatSummaryAndTurns(summary, working)
			tokens, _ = p.estimateTokens(ctx, formatted)
			if tokens <= p.tokenBudget {
				break
			}
		}
		if !changed {
			break
		}
	}

	if tokens > p.tokenBudget {
		formatted = hardTruncateText(formatted, p.tokenBudget)
		result.Truncated = true
		tokens, _ = p.estimateTokens(ctx, formatted)
	}

	result.TurnsKeptVerbatim = len(working)
	result.EstimatedTokensPost = tokens
	return formatted, result
}

func (p *ConversationHistoryProcessor) estimateTokens(ctx context.Context, text string) (int, string) {
	counterName := tokenCounterName(p.tokenCounter)
	count, err := p.tokenCounter.CountTokens(ctx, text)
	if err == nil {
		return count, counterName
	}

	telemetry.RecordSpanError(ctx, err)
	telemetry.AddSpanEvent(ctx, "conversation_history.token_count.fallback",
		attribute.String("request_id", requestIDFromBaggage(ctx)),
		attribute.String("error", err.Error()),
	)
	if p.logger != nil {
		p.logger.DebugWithContext(ctx, "Conversation token counter failed; using heuristic fallback", map[string]interface{}{
			"operation":           "conversation_history",
			"request_id":          requestIDFromBaggage(ctx),
			"original_request_id": originalRequestIDFromBaggage(ctx),
			"error":               err.Error(),
			"error_type":          "count_tokens",
			"status":              "heuristic_fallback",
		})
	}
	heuristicCount, _ := HeuristicTokenCounter{}.CountTokens(ctx, text)
	return heuristicCount, "heuristic"
}

func (p *ConversationHistoryProcessor) logPreparationOutcome(
	ctx context.Context,
	result HistoryPreparationResult,
	startTime time.Time,
	err error,
) {
	if p.logger == nil {
		return
	}

	fields := map[string]interface{}{
		"operation":              "conversation_history",
		"request_id":             requestIDFromBaggage(ctx),
		"original_request_id":    originalRequestIDFromBaggage(ctx),
		"path":                   result.Path,
		"turns_raw":              result.TurnsRaw,
		"turns_kept_verbatim":    result.TurnsKeptVerbatim,
		"turns_compacted":        result.TurnsCompacted,
		"turns_dropped":          result.TurnsDropped,
		"elided_turns":           result.ElidedTurns,
		"estimated_tokens_pre":   result.EstimatedTokensPre,
		"estimated_tokens_post":  result.EstimatedTokensPost,
		"compaction_duration_ms": result.CompactionDurationMs,
		"budget":                 result.Budget,
		"token_counter":          result.TokenCounterName,
		"compacted":              result.Compacted,
		"truncated":              result.Truncated,
		"duration_ms":            time.Since(startTime).Milliseconds(),
		"status":                 outcomeFromResult(result),
	}

	if err != nil {
		fields["error"] = err.Error()
		fields["error_type"] = "preparation"
		p.logger.ErrorWithContext(ctx, "Conversation history preparation failed", fields)
		return
	}

	p.logger.InfoWithContext(ctx, "Conversation history prepared", fields)
}

func (p *ConversationHistoryProcessor) recordPreparationInteraction(
	ctx context.Context,
	startTime time.Time,
	result HistoryPreparationResult,
) {
	if p.debugStore == nil {
		return
	}

	requestID := requestIDFromBaggage(ctx)
	if requestID == "" {
		requestID = fmt.Sprintf("conversation-history-prepare-%d", time.Now().UnixNano())
	}

	interaction := LLMInteraction{
		Type:            "conversation_history_prepare",
		HookPhase:       HookPhasePre,
		Timestamp:       startTime,
		Response:        formatPreparationSummary(result),
		Success:         true,
		Attempt:         1,
		CallDescription: "Token-aware conversation history preparation",
		Category:        "logic",
	}

	p.debugWg.Add(1)
	go func() {
		defer p.debugWg.Done()
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		interaction.DurationMs = time.Since(startTime).Milliseconds()
		if err := p.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil && p.logger != nil {
			p.logger.Warn("Failed to record conversation history preparation debug interaction", map[string]interface{}{
				"operation":  "conversation_history",
				"request_id": requestID,
				"type":       interaction.Type,
				"error":      err.Error(),
				"error_type": "debug_recording",
			})
		}
	}()
}

func (p *ConversationHistoryProcessor) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		p.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ConversationHistoryProcessor) summaryCacheLen() int {
	if p.summaryCache == nil {
		return 0
	}
	return p.summaryCache.Len()
}

func tokenCounterName(counter core.TokenCounter) string {
	if counter == nil {
		return "heuristic"
	}
	if _, ok := counter.(HeuristicTokenCounter); ok {
		return "heuristic"
	}
	t := reflect.TypeOf(counter)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return "custom"
	}
	return t.Name()
}

func outcomeFromResult(result HistoryPreparationResult) string {
	switch {
	case result.Truncated:
		return "truncated"
	case result.ElidedTurns > 0:
		return "elided"
	case result.Compacted:
		return "compacted"
	case result.TurnsDropped > 0:
		return "dropped"
	case result.EstimatedTokensPre == 0:
		return "empty"
	default:
		return "pass_through"
	}
}

func formatPreparationSummary(result HistoryPreparationResult) string {
	parts := []string{
		fmt.Sprintf("path=%s", result.Path),
		fmt.Sprintf("outcome=%s", outcomeFromResult(result)),
		fmt.Sprintf("tokens=%d→%d", result.EstimatedTokensPre, result.EstimatedTokensPost),
	}
	if result.Budget > 0 {
		parts = append(parts, fmt.Sprintf("budget=%d", result.Budget))
	}
	if result.TurnsRaw > 0 {
		parts = append(parts, fmt.Sprintf("raw_turns=%d", result.TurnsRaw))
	}
	if result.TurnsKeptVerbatim > 0 {
		parts = append(parts, fmt.Sprintf("kept=%d", result.TurnsKeptVerbatim))
	}
	if result.TurnsCompacted > 0 {
		parts = append(parts, fmt.Sprintf("compacted=%d", result.TurnsCompacted))
	}
	if result.TurnsDropped > 0 {
		parts = append(parts, fmt.Sprintf("dropped=%d", result.TurnsDropped))
	}
	if result.ElidedTurns > 0 {
		parts = append(parts, fmt.Sprintf("elided=%d", result.ElidedTurns))
	}
	if result.Truncated {
		parts = append(parts, "truncated=true")
	}
	return strings.Join(parts, " • ")
}

func watermarkMatches(state SummaryState, turns []core.ConversationTurn) bool {
	if state.LastCompactedCount == 0 {
		return true
	}
	if len(turns) < state.LastCompactedCount {
		return false
	}
	return fingerprintTurn(turns[state.LastCompactedCount-1]) == state.LastTurnFingerprint
}

func fingerprintTurn(turn core.ConversationTurn) string {
	sum := sha256.Sum256([]byte(turn.Role + "\x00" + turn.Content))
	return hex.EncodeToString(sum[:])
}

func cloneConversationTurns(turns []core.ConversationTurn) []core.ConversationTurn {
	if len(turns) == 0 {
		return nil
	}
	cloned := make([]core.ConversationTurn, len(turns))
	copy(cloned, turns)
	return cloned
}

func formatSummaryAndTurns(summary string, turns []core.ConversationTurn) string {
	parts := make([]string, 0, len(turns)+1)
	if summary != "" {
		parts = append(parts, "Conversation Summary:\n"+strings.TrimSpace(summary))
	}
	if len(turns) > 0 {
		parts = append(parts, formatConversationTurns(turns))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func formatConversationTurns(turns []core.ConversationTurn) string {
	if len(turns) == 0 {
		return ""
	}
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		role := "User"
		if turn.Role == "assistant" {
			role = "Assistant"
		} else if turn.Role != "" && turn.Role != "user" {
			role = strings.ToUpper(turn.Role[:1]) + turn.Role[1:]
		}
		lines = append(lines, fmt.Sprintf("%s: %s", role, turn.Content))
	}
	return strings.Join(lines, "\n")
}

func elideMiddle(content string) (string, bool) {
	if len(content) <= 96 {
		return content, false
	}
	keep := len(content) / 4
	if keep < 32 {
		keep = 32
	}
	if keep*2+9 >= len(content) {
		return content, false
	}
	return content[:keep] + "\n...\n" + content[len(content)-keep:], true
}

func hardTruncateText(text string, budget int) string {
	const prefix = "[conversation history truncated]\n"
	if text == "" {
		return text
	}

	current := text
	for i := 0; i < 8; i++ {
		tokens, _ := HeuristicTokenCounter{}.CountTokens(context.Background(), current)
		if tokens <= budget {
			return current
		}
		keep := int(float64(len(current)) * 0.75)
		if keep < 64 {
			break
		}
		if keep > len(current) {
			keep = len(current)
		}
		current = prefix + current[len(current)-keep:]
	}

	maxChars := budget * 3
	if maxChars > len(current) {
		maxChars = len(current)
	}
	if maxChars <= 0 {
		return prefix
	}
	return prefix + current[len(current)-maxChars:]
}

func minConversationInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
