package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	viewerLLMDebugKeyPrefix   = "truvag3:llm:debug:"
	viewerLLMDebugMetaSuffix  = ":meta"
	viewerLLMDebugInterSuffix = ":interactions"
	groupedCursorSchema       = 1
)

// ConversationTimelineTurn is one top-level user turn plus any related
// HITL/delegated executions. Related executions are lineage, not independent
// conversation turns.
type ConversationTimelineTurn struct {
	Execution         ExecutionSummary   `json:"execution"`
	RelatedExecutions []ExecutionSummary `json:"related_executions"`
	LLMCallCount      int                `json:"llm_call_count,omitempty"`
	HistoryStatus     string             `json:"history_status,omitempty"`
}

// ConversationTimelineResponse is the focused chronological conversation view.
type ConversationTimelineResponse struct {
	ConversationID          string                     `json:"conversation_id"`
	Turns                   []ConversationTimelineTurn `json:"turns"`
	Orphans                 []ExecutionSummary         `json:"orphans"`
	TurnCount               int                        `json:"turn_count"`
	ExecutionCount          int                        `json:"execution_count"`
	IndexIncomplete         bool                       `json:"index_incomplete"`
	LLMEnrichmentIncomplete bool                       `json:"llm_enrichment_incomplete"`
	Partial                 bool                       `json:"partial"`
	Timestamp               time.Time                  `json:"timestamp"`
}

// GroupedExecutionGroup is one atomic unit in the grouped execution list.
type GroupedExecutionGroup struct {
	GroupKey          string                     `json:"group_key"`
	ConversationID    string                     `json:"conversation_id,omitempty"`
	AnchorCreatedAt   time.Time                  `json:"anchor_created_at"`
	MatchingTurnCount int                        `json:"matching_turn_count"`
	TotalTurnCount    int                        `json:"total_turn_count"`
	Turns             []ConversationTimelineTurn `json:"turns"`
	Orphans           []ExecutionSummary         `json:"orphans"`
	IndexIncomplete   bool                       `json:"index_incomplete"`

	sortValue groupedSortValue
}

// GroupedExecutionListResponse is the additive server-owned grouping shape.
type GroupedExecutionListResponse struct {
	Groups                  []GroupedExecutionGroup `json:"groups"`
	NextCursor              string                  `json:"next_cursor,omitempty"`
	QueryFingerprint        string                  `json:"query_fingerprint"`
	LLMEnrichmentIncomplete bool                    `json:"llm_enrichment_incomplete"`
	Partial                 bool                    `json:"partial"`
	Timestamp               time.Time               `json:"timestamp"`
}

type groupedExecutionQuery struct {
	Search      string
	Status      string
	Sort        string
	Direction   string
	Limit       int
	Cursor      *groupedExecutionCursor
	Fingerprint string
}

type groupedSortValue struct {
	Kind   string `json:"kind"`
	String string `json:"string,omitempty"`
	Number int64  `json:"number,omitempty"`
}

type groupedExecutionCursor struct {
	Version         int              `json:"version"`
	Fingerprint     string           `json:"fingerprint"`
	SnapshotScore   string           `json:"snapshot_score"`
	SnapshotMember  string           `json:"snapshot_member"`
	SortValue       groupedSortValue `json:"sort_value"`
	AnchorCreatedAt int64            `json:"anchor_created_at"`
	GroupKey        string           `json:"group_key"`
}

type indexedExecution struct {
	RequestID string
	Score     float64
}

type executionReadCache struct {
	records  map[string]*StoredExecution
	missing  map[string]struct{}
	hydrated int
}

type llmDebugEnrichment struct {
	CallCount       int
	TotalDurationMs int64
	HistoryStatus   string
}

var (
	llmDebugReadClient   *redis.Client
	llmDebugReadClientMu sync.Mutex
)

func getLLMDebugReadClient() (*redis.Client, error) {
	llmDebugReadClientMu.Lock()
	defer llmDebugReadClientMu.Unlock()

	if llmDebugReadClient == nil {
		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			return nil, fmt.Errorf("invalid redis URL: %w", err)
		}
		opt.DB = core.RedisDBLLMDebug
		llmDebugReadClient = redis.NewClient(core.ApplyRedisClientDefaults(opt))
	}
	return llmDebugReadClient, nil
}

func summarizeExecution(execution *StoredExecution) ExecutionSummary {
	if execution == nil {
		return ExecutionSummary{}
	}
	summary := ExecutionSummary{
		RequestID:         execution.RequestID,
		OriginalRequestID: execution.OriginalRequestID,
		ConversationID:    execution.Metadata[orchestration.MetadataConversationID],
		TraceID:           execution.TraceID,
		AgentName:         execution.AgentName,
		OriginalRequest:   execution.OriginalRequest,
		Interrupted:       execution.Interrupted,
		CreatedAt:         execution.CreatedAt,
		Metadata:          execution.Metadata,
	}
	if execution.Result != nil {
		summary.Success = execution.Result.Success
		summary.TotalDurationMs = execution.Result.TotalDuration / 1_000_000
		summary.StepCount = len(execution.Result.Steps)
		for _, step := range execution.Result.Steps {
			if !step.Success {
				summary.FailedSteps++
			}
		}
	}
	return summary
}

func viewerConversationIndexKey(conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	return fmt.Sprintf("%sconversation:%x", executionKeyPrefix, digest)
}

func newExecutionReadCache() *executionReadCache {
	return &executionReadCache{
		records: make(map[string]*StoredExecution),
		missing: make(map[string]struct{}),
	}
}

func (c *executionReadCache) load(
	ctx context.Context,
	client *redis.Client,
	requestIDs []string,
) (bool, error) {
	unknown := make([]string, 0, len(requestIDs))
	seen := make(map[string]struct{}, len(requestIDs))
	for _, requestID := range requestIDs {
		if requestID == "" {
			continue
		}
		if _, duplicate := seen[requestID]; duplicate {
			continue
		}
		seen[requestID] = struct{}{}
		if _, ok := c.records[requestID]; ok {
			continue
		}
		if _, ok := c.missing[requestID]; ok {
			continue
		}
		unknown = append(unknown, requestID)
	}

	remaining := viewerExecutionHydrationLimit - c.hydrated
	truncated := len(unknown) > remaining
	if remaining <= 0 {
		return len(unknown) > 0, nil
	}
	if len(unknown) > remaining {
		unknown = unknown[:remaining]
	}
	if len(unknown) == 0 {
		return truncated, nil
	}

	keys := make([]string, len(unknown))
	for i, requestID := range unknown {
		keys[i] = executionKeyPrefix + requestID
	}
	values, err := client.MGet(ctx, keys...).Result()
	if err != nil {
		return truncated, fmt.Errorf("failed to hydrate executions: %w", err)
	}
	c.hydrated += len(unknown)
	for i, value := range values {
		requestID := unknown[i]
		if value == nil {
			c.missing[requestID] = struct{}{}
			continue
		}
		var data []byte
		switch typed := value.(type) {
		case string:
			data = []byte(typed)
		case []byte:
			data = typed
		default:
			c.missing[requestID] = struct{}{}
			continue
		}
		execution, decodeErr := deserializeExecution(data)
		if decodeErr != nil {
			c.missing[requestID] = struct{}{}
			continue
		}
		c.records[requestID] = execution
	}
	return truncated, nil
}

func handleConversationTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	values, present := r.URL.Query()["conversation_id"]
	if !present || len(values) != 1 || values[0] == "" {
		http.Error(w, "query parameter 'conversation_id' is required", http.StatusBadRequest)
		return
	}
	conversationID := values[0]
	if reason := core.ValidateConversationID(conversationID); reason != core.ConversationIDValidationNone {
		http.Error(w, "invalid conversation_id: "+string(reason), http.StatusBadRequest)
		return
	}

	if useMock {
		summaries := make([]ExecutionSummary, 0)
		for _, summary := range getMockExecutionSummaries() {
			if summary.ConversationID == conversationID {
				summaries = append(summaries, summary)
			}
		}
		response := buildConversationTimelineResponse(
			conversationID,
			summaries,
			nil,
			false,
			false,
			false,
		)
		_ = json.NewEncoder(w).Encode(response)
		return
	}

	response, err := loadConversationTimeline(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "conversation timeline unavailable", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func loadConversationTimeline(
	parent context.Context,
	conversationID string,
) (*ConversationTimelineResponse, error) {
	client, err := getExecutionDebugClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	conversationKey := viewerConversationIndexKey(conversationID)
	reverseIDs, err := client.ZRange(
		ctx,
		conversationKey,
		0,
		viewerConversationMemberReadLimit,
	).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read conversation index: %w", err)
	}
	reverseMore := len(reverseIDs) > viewerConversationMemberReadLimit
	if reverseMore {
		reverseIDs = reverseIDs[:viewerConversationMemberReadLimit]
	}

	globalRows, err := client.ZRevRangeWithScores(
		ctx,
		executionIndexKey,
		0,
		viewerGlobalExecutionScanLimit,
	).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read global execution index: %w", err)
	}
	globalMore := len(globalRows) > viewerGlobalExecutionScanLimit
	if globalMore {
		globalRows = globalRows[:viewerGlobalExecutionScanLimit]
	}
	globalIDs := make([]string, 0, len(globalRows))
	for _, row := range globalRows {
		if requestID, ok := row.Member.(string); ok {
			globalIDs = append(globalIDs, requestID)
		}
	}

	candidates := append(append([]string{}, reverseIDs...), globalIDs...)
	cache := newExecutionReadCache()
	hydrationPartial, err := cache.load(ctx, client, candidates)
	if err != nil {
		return nil, err
	}

	reverseSet := make(map[string]struct{}, len(reverseIDs))
	for _, requestID := range reverseIDs {
		reverseSet[requestID] = struct{}{}
	}
	indexIncomplete := false
	verified := make(map[string]ExecutionSummary)
	for _, requestID := range reverseIDs {
		execution := cache.records[requestID]
		if execution == nil ||
			execution.Metadata[orchestration.MetadataConversationID] != conversationID {
			indexIncomplete = true
			continue
		}
		verified[requestID] = summarizeExecution(execution)
	}
	for _, requestID := range globalIDs {
		execution := cache.records[requestID]
		if execution == nil ||
			execution.Metadata[orchestration.MetadataConversationID] != conversationID {
			continue
		}
		verified[requestID] = summarizeExecution(execution)
	}
	membershipChecks := make(map[string][]string)
	for requestID := range verified {
		if _, inReverse := reverseSet[requestID]; !inReverse {
			membershipChecks[conversationID] = append(
				membershipChecks[conversationID],
				requestID,
			)
		}
	}
	missingMembers, err := findMissingConversationIndexMembers(
		ctx,
		client,
		membershipChecks,
	)
	if err != nil {
		return nil, err
	}
	if len(missingMembers[conversationID]) > 0 {
		indexIncomplete = true
	}

	summaries := make([]ExecutionSummary, 0, len(verified))
	for _, summary := range verified {
		summaries = append(summaries, summary)
	}
	enrichment, enrichmentIncomplete, enrichmentErr :=
		enrichExecutionSummariesFromLLMDebug(ctx, summaries)
	return buildConversationTimelineResponse(
		conversationID,
		summaries,
		enrichment,
		reverseMore || globalMore || hydrationPartial,
		indexIncomplete,
		enrichmentIncomplete || enrichmentErr != nil,
	), nil
}

func buildConversationTimelineResponse(
	conversationID string,
	summaries []ExecutionSummary,
	enrichment map[string]llmDebugEnrichment,
	partial bool,
	indexIncomplete bool,
	llmEnrichmentIncomplete bool,
) *ConversationTimelineResponse {
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].CreatedAt.Equal(summaries[j].CreatedAt) {
			return summaries[i].RequestID < summaries[j].RequestID
		}
		return summaries[i].CreatedAt.Before(summaries[j].CreatedAt)
	})

	topLevel := make(map[string]int)
	turns := make([]ConversationTimelineTurn, 0)
	related := make([]ExecutionSummary, 0)
	for _, summary := range summaries {
		if isTopLevelExecution(summary) {
			topLevel[summary.RequestID] = len(turns)
			item := enrichment[summary.RequestID]
			turns = append(turns, ConversationTimelineTurn{
				Execution:         summary,
				RelatedExecutions: []ExecutionSummary{},
				LLMCallCount:      item.CallCount,
				HistoryStatus:     item.HistoryStatus,
			})
		} else {
			related = append(related, summary)
		}
	}

	orphans := make([]ExecutionSummary, 0)
	for _, summary := range related {
		if turnIndex, ok := topLevel[summary.OriginalRequestID]; ok {
			turns[turnIndex].RelatedExecutions = append(
				turns[turnIndex].RelatedExecutions,
				summary,
			)
			continue
		}
		orphans = append(orphans, summary)
	}

	return &ConversationTimelineResponse{
		ConversationID:          conversationID,
		Turns:                   nonNilSlice(turns),
		Orphans:                 nonNilSlice(orphans),
		TurnCount:               len(turns),
		ExecutionCount:          len(summaries),
		IndexIncomplete:         indexIncomplete,
		LLMEnrichmentIncomplete: llmEnrichmentIncomplete,
		Partial:                 partial,
		Timestamp:               time.Now(),
	}
}

func handleGroupedExecutionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query, err := parseGroupedExecutionQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if useMock {
		summaries := getMockExecutionSummaries()
		groups := buildGroupedExecutionUnits(
			summaries,
			nil,
			nil,
			query,
		)
		snapshotScore := "0"
		snapshotMember := "mock"
		for _, summary := range summaries {
			score := float64(summary.CreatedAt.UnixNano())
			current, _ := strconv.ParseFloat(snapshotScore, 64)
			if score > current ||
				(score == current && summary.RequestID > snapshotMember) {
				snapshotScore = strconv.FormatFloat(score, 'g', -1, 64)
				snapshotMember = summary.RequestID
			}
		}
		response := paginateGroupedExecutionUnits(
			groups,
			query,
			snapshotScore,
			snapshotMember,
			false,
			false,
		)
		_ = json.NewEncoder(w).Encode(response)
		return
	}

	response, err := loadGroupedExecutions(r.Context(), query)
	if err != nil {
		http.Error(w, "grouped executions unavailable", http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(response)
}

func parseGroupedExecutionQuery(r *http.Request) (*groupedExecutionQuery, error) {
	values := r.URL.Query()
	query := &groupedExecutionQuery{
		Search:    strings.ToLower(strings.TrimSpace(values.Get("q"))),
		Status:    values.Get("status"),
		Sort:      values.Get("sort"),
		Direction: values.Get("direction"),
		Limit:     viewerDefaultGroupPageSize,
	}
	if query.Status == "" {
		query.Status = "all"
	}
	switch query.Status {
	case "all", "success", "failed", "interrupted":
	default:
		return nil, fmt.Errorf("unsupported status")
	}
	if query.Sort == "" {
		query.Sort = "created_at"
	}
	switch query.Sort {
	case "original_request", "total_duration_ms", "created_at":
	default:
		return nil, fmt.Errorf("unsupported sort")
	}
	if query.Direction == "" {
		if query.Sort == "created_at" {
			query.Direction = "desc"
		} else {
			query.Direction = "asc"
		}
	}
	if query.Direction != "asc" && query.Direction != "desc" {
		return nil, fmt.Errorf("unsupported direction")
	}
	if rawLimit, present := values["limit"]; present {
		if len(rawLimit) != 1 {
			return nil, fmt.Errorf("limit must be specified once")
		}
		limit, err := strconv.Atoi(rawLimit[0])
		if err != nil || limit <= 0 {
			return nil, fmt.Errorf("limit must be a positive integer")
		}
		if limit > viewerMaxGroupPageSize {
			limit = viewerMaxGroupPageSize
		}
		query.Limit = limit
	}

	fingerprintInput := strings.Join([]string{
		"grouped-executions-v1",
		query.Search,
		query.Status,
		query.Sort,
		query.Direction,
	}, "\x00")
	digest := sha256.Sum256([]byte(fingerprintInput))
	query.Fingerprint = fmt.Sprintf("%x", digest)

	if rawCursor, present := values["cursor"]; present {
		if len(rawCursor) != 1 || rawCursor[0] == "" {
			return nil, fmt.Errorf("cursor must be specified once and non-empty")
		}
		if query.Sort == "total_duration_ms" {
			return nil, fmt.Errorf(
				"cursor is not supported for total_duration_ms sorting",
			)
		}
		cursor, err := decodeGroupedExecutionCursor(rawCursor[0])
		if err != nil {
			return nil, err
		}
		if cursor.Fingerprint != query.Fingerprint {
			return nil, fmt.Errorf("cursor does not match the current query")
		}
		expectedSortKind := "number"
		if query.Sort == "original_request" {
			expectedSortKind = "string"
		}
		if cursor.SortValue.Kind != expectedSortKind {
			return nil, fmt.Errorf("cursor sort value does not match the current query")
		}
		query.Cursor = cursor
	}
	return query, nil
}

func decodeGroupedExecutionCursor(raw string) (*groupedExecutionCursor, error) {
	if len(raw) > viewerGroupedCursorMaxEncodedLength {
		return nil, fmt.Errorf("cursor is too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	var cursor groupedExecutionCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf("malformed cursor")
	}
	if cursor.Version != groupedCursorSchema ||
		cursor.Fingerprint == "" ||
		cursor.SnapshotScore == "" ||
		cursor.SnapshotMember == "" ||
		cursor.GroupKey == "" {
		return nil, fmt.Errorf("unsupported or incomplete cursor")
	}
	if cursor.SortValue.Kind != "string" && cursor.SortValue.Kind != "number" {
		return nil, fmt.Errorf("unsupported cursor sort value")
	}
	snapshotScore, err := strconv.ParseFloat(cursor.SnapshotScore, 64)
	if err != nil || math.IsNaN(snapshotScore) || math.IsInf(snapshotScore, 0) {
		return nil, fmt.Errorf("malformed cursor snapshot")
	}
	return &cursor, nil
}

func loadGroupedExecutions(
	parent context.Context,
	query *groupedExecutionQuery,
) (*GroupedExecutionListResponse, error) {
	client, err := getExecutionDebugClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	rows, snapshotScore, snapshotMember, globalPartial, err :=
		readGroupedGlobalSnapshot(ctx, client, query.Cursor)
	if err != nil {
		return nil, err
	}
	globalIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		globalIDs = append(globalIDs, row.RequestID)
	}

	cache := newExecutionReadCache()
	hydrationPartial, err := cache.load(ctx, client, globalIDs)
	if err != nil {
		return nil, err
	}
	partial := globalPartial || hydrationPartial

	conversationOrder := make([]string, 0)
	conversationSeen := make(map[string]struct{})
	for _, row := range rows {
		execution := cache.records[row.RequestID]
		if execution == nil {
			continue
		}
		conversationID := execution.Metadata[orchestration.MetadataConversationID]
		if conversationID == "" {
			continue
		}
		if _, seen := conversationSeen[conversationID]; seen {
			continue
		}
		conversationSeen[conversationID] = struct{}{}
		if len(conversationOrder) == viewerDistinctConversationLimit {
			partial = true
			continue
		}
		conversationOrder = append(conversationOrder, conversationID)
	}

	reverseMembers, reversePartial, err := readConversationMembers(
		ctx,
		client,
		conversationOrder,
	)
	if err != nil {
		return nil, err
	}
	partial = partial || reversePartial

	reverseCandidates := make([]string, 0)
	for _, conversationID := range conversationOrder {
		reverseCandidates = append(
			reverseCandidates,
			reverseMembers[conversationID]...,
		)
	}
	reverseHydrationPartial, err := cache.load(ctx, client, reverseCandidates)
	if err != nil {
		return nil, err
	}
	partial = partial || reverseHydrationPartial

	snapshotScoreNumber, _ := strconv.ParseFloat(snapshotScore, 64)
	summariesByID := make(map[string]ExecutionSummary)
	indexIncomplete := make(map[string]bool)
	memberSets := make(map[string]map[string]struct{}, len(conversationOrder))
	for _, row := range rows {
		execution := cache.records[row.RequestID]
		if execution == nil {
			continue
		}
		summary := summarizeExecution(execution)
		summariesByID[summary.RequestID] = summary
	}
	for _, conversationID := range conversationOrder {
		groupKey := groupedConversationKey(conversationID)
		memberSet := make(map[string]struct{}, len(reverseMembers[conversationID]))
		memberSets[conversationID] = memberSet
		for _, requestID := range reverseMembers[conversationID] {
			memberSet[requestID] = struct{}{}
			execution := cache.records[requestID]
			if execution == nil {
				if _, confirmedMissing := cache.missing[requestID]; confirmedMissing {
					indexIncomplete[groupKey] = true
				}
				continue
			}
			if execution.Metadata[orchestration.MetadataConversationID] != conversationID {
				indexIncomplete[groupKey] = true
				continue
			}
			score := float64(execution.CreatedAt.UnixNano())
			if executionIsNewerThanSnapshot(
				score,
				execution.RequestID,
				snapshotScoreNumber,
				snapshotMember,
			) {
				continue
			}
			summary := summarizeExecution(execution)
			summariesByID[summary.RequestID] = summary
		}
	}

	membershipChecks := make(map[string][]string)
	for _, row := range rows {
		execution := cache.records[row.RequestID]
		if execution == nil {
			continue
		}
		conversationID := execution.Metadata[orchestration.MetadataConversationID]
		memberSet, expanded := memberSets[conversationID]
		if conversationID == "" || !expanded {
			continue
		}
		if _, indexed := memberSet[row.RequestID]; !indexed {
			membershipChecks[conversationID] = append(
				membershipChecks[conversationID],
				row.RequestID,
			)
		}
	}
	missingMembers, err := findMissingConversationIndexMembers(
		ctx,
		client,
		membershipChecks,
	)
	if err != nil {
		return nil, err
	}
	for conversationID, requestIDs := range missingMembers {
		if len(requestIDs) > 0 {
			indexIncomplete[groupedConversationKey(conversationID)] = true
		}
	}

	summaries := make([]ExecutionSummary, 0, len(summariesByID))
	for _, summary := range summariesByID {
		summaries = append(summaries, summary)
	}

	var (
		enrichment           map[string]llmDebugEnrichment
		enrichmentIncomplete bool
		enrichmentErr        error
	)
	if query.Sort == "total_duration_ms" {
		// Combined duration ordering depends on mutable DB 7 data, so it must
		// be enriched before grouping. An incomplete read makes this bounded
		// point-in-time result partial; duration cursors are rejected.
		enrichment, enrichmentIncomplete, enrichmentErr =
			enrichExecutionSummariesFromLLMDebug(ctx, summaries)
		if enrichmentIncomplete || enrichmentErr != nil {
			partial = true
		}
	}
	groups := buildGroupedExecutionUnits(
		summaries,
		enrichment,
		indexIncomplete,
		query,
	)
	response := paginateGroupedExecutionUnits(
		groups,
		query,
		snapshotScore,
		snapshotMember,
		partial,
		enrichmentIncomplete || enrichmentErr != nil,
	)
	if query.Sort == "total_duration_ms" {
		return response, nil
	}

	// Created-at and request-text ordering depend only on immutable DB 8
	// fields. Paginate first, then enrich just the returned page so optional
	// DB 7 bounds or failures cannot suppress a valid DB 8 continuation.
	pageSummaries := groupedExecutionPageSummaries(response.Groups)
	enrichment, enrichmentIncomplete, enrichmentErr =
		enrichExecutionSummariesFromLLMDebug(ctx, pageSummaries)
	applyGroupedExecutionEnrichment(response.Groups, enrichment)
	response.LLMEnrichmentIncomplete =
		enrichmentIncomplete || enrichmentErr != nil
	return response, nil
}

func readGroupedGlobalSnapshot(
	ctx context.Context,
	client *redis.Client,
	cursor *groupedExecutionCursor,
) ([]indexedExecution, string, string, bool, error) {
	maxScore := "+inf"
	var snapshotScore float64
	var snapshotMember string
	if cursor != nil {
		maxScore = cursor.SnapshotScore
		snapshotScore, _ = strconv.ParseFloat(cursor.SnapshotScore, 64)
		snapshotMember = cursor.SnapshotMember
	}
	rows, err := client.ZRevRangeByScoreWithScores(
		ctx,
		executionIndexKey,
		&redis.ZRangeBy{
			Min:   "-inf",
			Max:   maxScore,
			Count: int64(viewerGlobalExecutionScanLimit) + 1,
		},
	).Result()
	if err != nil {
		return nil, "", "", false, fmt.Errorf("failed to read global execution index: %w", err)
	}
	if cursor == nil && len(rows) > 0 {
		snapshotScore = rows[0].Score
		snapshotMember, _ = rows[0].Member.(string)
	}

	partial := len(rows) > viewerGlobalExecutionScanLimit
	if len(rows) > viewerGlobalExecutionScanLimit {
		rows = rows[:viewerGlobalExecutionScanLimit]
	}
	indexed := make([]indexedExecution, 0, len(rows))
	for _, row := range rows {
		requestID, ok := row.Member.(string)
		if !ok {
			continue
		}
		if cursor != nil && executionIsNewerThanSnapshot(
			row.Score,
			requestID,
			snapshotScore,
			snapshotMember,
		) {
			continue
		}
		indexed = append(indexed, indexedExecution{
			RequestID: requestID,
			Score:     row.Score,
		})
	}
	if snapshotMember == "" {
		return indexed, "0", "empty", partial, nil
	}
	return indexed,
		strconv.FormatFloat(snapshotScore, 'g', -1, 64),
		snapshotMember,
		partial,
		nil
}

func executionIsNewerThanSnapshot(
	score float64,
	member string,
	snapshotScore float64,
	snapshotMember string,
) bool {
	if score != snapshotScore {
		return score > snapshotScore
	}
	return member > snapshotMember
}

func readConversationMembers(
	ctx context.Context,
	client *redis.Client,
	conversationIDs []string,
) (map[string][]string, bool, error) {
	result := make(map[string][]string, len(conversationIDs))
	if len(conversationIDs) == 0 {
		return result, false, nil
	}
	pipe := client.Pipeline()
	commands := make(map[string]*redis.StringSliceCmd, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		commands[conversationID] = pipe.ZRange(
			ctx,
			viewerConversationIndexKey(conversationID),
			0,
			viewerConversationMemberReadLimit,
		)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, false, fmt.Errorf("failed to read conversation indexes: %w", err)
	}
	partial := false
	for _, conversationID := range conversationIDs {
		members, err := commands[conversationID].Result()
		if err != nil && err != redis.Nil {
			return nil, false, fmt.Errorf("failed to read conversation index: %w", err)
		}
		if len(members) > viewerConversationMemberReadLimit {
			members = members[:viewerConversationMemberReadLimit]
			partial = true
		}
		result[conversationID] = members
	}
	return result, partial, nil
}

func findMissingConversationIndexMembers(
	ctx context.Context,
	client *redis.Client,
	checks map[string][]string,
) (map[string][]string, error) {
	missing := make(map[string][]string)
	type membershipCheck struct {
		conversationID string
		requestID      string
		command        *redis.FloatCmd
	}
	commands := make([]membershipCheck, 0)
	pipe := client.Pipeline()
	for conversationID, requestIDs := range checks {
		for _, requestID := range requestIDs {
			commands = append(commands, membershipCheck{
				conversationID: conversationID,
				requestID:      requestID,
				command: pipe.ZScore(
					ctx,
					viewerConversationIndexKey(conversationID),
					requestID,
				),
			})
		}
	}
	if len(commands) == 0 {
		return missing, nil
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to verify conversation index membership: %w", err)
	}
	for _, check := range commands {
		if _, err := check.command.Result(); err == redis.Nil {
			missing[check.conversationID] = append(
				missing[check.conversationID],
				check.requestID,
			)
		} else if err != nil {
			return nil, fmt.Errorf("failed to verify conversation index membership: %w", err)
		}
	}
	return missing, nil
}

func buildGroupedExecutionUnits(
	summaries []ExecutionSummary,
	enrichment map[string]llmDebugEnrichment,
	indexIncomplete map[string]bool,
	query *groupedExecutionQuery,
) []GroupedExecutionGroup {
	summaryByID := make(map[string]ExecutionSummary, len(summaries))
	for _, summary := range summaries {
		summaryByID[summary.RequestID] = summary
	}
	groupKeyByID := make(map[string]string, len(summaries))
	for _, summary := range summaries {
		switch {
		case summary.ConversationID != "":
			groupKeyByID[summary.RequestID] =
				groupedConversationKey(summary.ConversationID)
		case !isTopLevelExecution(summary):
			if owner, ok := summaryByID[summary.OriginalRequestID]; ok {
				if owner.ConversationID == "" {
					groupKeyByID[summary.RequestID] =
						"request:" + owner.RequestID
				} else {
					// Conversation membership comes only from this record's
					// stored conversation_id. Request lineage cannot promote
					// a stateless related execution into the owner's
					// conversation.
					groupKeyByID[summary.RequestID] =
						"request:" + summary.RequestID
				}
			} else {
				groupKeyByID[summary.RequestID] =
					"request:" + summary.RequestID
			}
		default:
			groupKeyByID[summary.RequestID] =
				"request:" + summary.RequestID
		}
	}

	membersByGroup := make(map[string][]ExecutionSummary)
	for _, summary := range summaries {
		key := groupKeyByID[summary.RequestID]
		membersByGroup[key] = append(membersByGroup[key], summary)
	}

	groups := make([]GroupedExecutionGroup, 0, len(membersByGroup))
	for groupKey, members := range membersByGroup {
		sort.SliceStable(members, func(i, j int) bool {
			if members[i].CreatedAt.Equal(members[j].CreatedAt) {
				return members[i].RequestID < members[j].RequestID
			}
			return members[i].CreatedAt.After(members[j].CreatedAt)
		})
		conversationID := ""
		if strings.HasPrefix(groupKey, "conversation:") {
			for _, member := range members {
				if member.ConversationID != "" {
					conversationID = member.ConversationID
					break
				}
			}
		}

		topLevelByID := make(map[string]ExecutionSummary)
		related := make([]ExecutionSummary, 0)
		for _, member := range members {
			if isTopLevelExecution(member) {
				topLevelByID[member.RequestID] = member
			} else {
				related = append(related, member)
			}
		}
		topLevel := make([]ExecutionSummary, 0, len(topLevelByID))
		for _, member := range topLevelByID {
			topLevel = append(topLevel, member)
		}
		sort.SliceStable(topLevel, func(i, j int) bool {
			if topLevel[i].CreatedAt.Equal(topLevel[j].CreatedAt) {
				return topLevel[i].RequestID < topLevel[j].RequestID
			}
			return topLevel[i].CreatedAt.After(topLevel[j].CreatedAt)
		})

		relatedByOwner := make(map[string][]ExecutionSummary)
		orphans := make([]ExecutionSummary, 0)
		for _, member := range related {
			if _, ok := topLevelByID[member.OriginalRequestID]; ok {
				relatedByOwner[member.OriginalRequestID] = append(
					relatedByOwner[member.OriginalRequestID],
					member,
				)
			} else {
				orphans = append(orphans, member)
			}
		}
		for owner := range relatedByOwner {
			sort.SliceStable(relatedByOwner[owner], func(i, j int) bool {
				if relatedByOwner[owner][i].CreatedAt.Equal(
					relatedByOwner[owner][j].CreatedAt,
				) {
					return relatedByOwner[owner][i].RequestID <
						relatedByOwner[owner][j].RequestID
				}
				return relatedByOwner[owner][i].CreatedAt.After(
					relatedByOwner[owner][j].CreatedAt,
				)
			})
		}

		matching := make([]ExecutionSummary, 0, len(topLevel))
		for _, member := range topLevel {
			if executionMatchesGroupedQuery(member, conversationID, query) {
				matching = append(matching, member)
			}
		}
		if len(matching) == 0 {
			if len(topLevel) > 0 ||
				query.Status != "all" ||
				(query.Search != "" &&
					!strings.Contains(strings.ToLower(conversationID), query.Search)) {
				continue
			}
			// Preserve a globally-discovered related execution even when its
			// owning top-level record is outside the bounded snapshot.
			matching = append(matching, orphans[0])
		}

		anchor := matching[0]
		turns := make([]ConversationTimelineTurn, 0, len(matching))
		if len(topLevel) > 0 {
			for _, member := range matching {
				item := enrichment[member.RequestID]
				turns = append(turns, ConversationTimelineTurn{
					Execution:         member,
					RelatedExecutions: nonNilSlice(relatedByOwner[member.RequestID]),
					LLMCallCount:      item.CallCount,
					HistoryStatus:     item.HistoryStatus,
				})
			}
		}
		groups = append(groups, GroupedExecutionGroup{
			GroupKey:          groupKey,
			ConversationID:    conversationID,
			AnchorCreatedAt:   anchor.CreatedAt,
			MatchingTurnCount: len(turns),
			TotalTurnCount:    len(topLevel),
			Turns:             nonNilSlice(turns),
			Orphans:           nonNilSlice(orphans),
			IndexIncomplete:   indexIncomplete[groupKey],
			sortValue:         groupedExecutionSortValue(anchor, query.Sort),
		})
	}

	sort.SliceStable(groups, func(i, j int) bool {
		return compareGroupedExecutionPosition(groups[i], groups[j], query) < 0
	})
	return groups
}

func isTopLevelExecution(summary ExecutionSummary) bool {
	return summary.OriginalRequestID == "" ||
		summary.OriginalRequestID == summary.RequestID
}

func groupedConversationKey(conversationID string) string {
	digest := sha256.Sum256([]byte(conversationID))
	return fmt.Sprintf("conversation:%x", digest)
}

func executionMatchesGroupedQuery(
	summary ExecutionSummary,
	conversationID string,
	query *groupedExecutionQuery,
) bool {
	switch query.Status {
	case "success":
		if !summary.Success || summary.Interrupted {
			return false
		}
	case "failed":
		if summary.Success || summary.Interrupted {
			return false
		}
	case "interrupted":
		if !summary.Interrupted {
			return false
		}
	}
	if query.Search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(summary.OriginalRequest), query.Search) ||
		strings.Contains(strings.ToLower(summary.RequestID), query.Search) ||
		strings.Contains(strings.ToLower(conversationID), query.Search)
}

func groupedExecutionSortValue(
	summary ExecutionSummary,
	field string,
) groupedSortValue {
	switch field {
	case "original_request":
		return groupedSortValue{
			Kind:   "string",
			String: strings.ToLower(summary.OriginalRequest),
		}
	case "total_duration_ms":
		return groupedSortValue{
			Kind:   "number",
			Number: summary.TotalDurationMs + summary.LLMTotalDurationMs,
		}
	default:
		return groupedSortValue{
			Kind:   "number",
			Number: summary.CreatedAt.UnixNano(),
		}
	}
}

func compareGroupedExecutionPosition(
	left GroupedExecutionGroup,
	right GroupedExecutionGroup,
	query *groupedExecutionQuery,
) int {
	primary := compareGroupedSortValue(left.sortValue, right.sortValue)
	if query.Direction == "desc" {
		primary = -primary
	}
	if primary != 0 {
		return primary
	}
	if !left.AnchorCreatedAt.Equal(right.AnchorCreatedAt) {
		if left.AnchorCreatedAt.After(right.AnchorCreatedAt) {
			return -1
		}
		return 1
	}
	return strings.Compare(left.GroupKey, right.GroupKey)
}

func compareGroupedSortValue(left, right groupedSortValue) int {
	if left.Kind == "string" {
		return strings.Compare(left.String, right.String)
	}
	switch {
	case left.Number < right.Number:
		return -1
	case left.Number > right.Number:
		return 1
	default:
		return 0
	}
}

func groupedExecutionPageSummaries(
	groups []GroupedExecutionGroup,
) []ExecutionSummary {
	seen := make(map[string]struct{})
	summaries := make([]ExecutionSummary, 0)
	appendSummary := func(summary ExecutionSummary) {
		if summary.RequestID == "" {
			return
		}
		if _, exists := seen[summary.RequestID]; exists {
			return
		}
		seen[summary.RequestID] = struct{}{}
		summaries = append(summaries, summary)
	}

	for _, group := range groups {
		for _, turn := range group.Turns {
			appendSummary(turn.Execution)
			for _, related := range turn.RelatedExecutions {
				appendSummary(related)
			}
		}
		for _, orphan := range group.Orphans {
			appendSummary(orphan)
		}
	}
	return summaries
}

func applyGroupedExecutionEnrichment(
	groups []GroupedExecutionGroup,
	enrichment map[string]llmDebugEnrichment,
) {
	applySummary := func(summary *ExecutionSummary) {
		item, exists := enrichment[summary.RequestID]
		if !exists {
			return
		}
		summary.LLMTotalDurationMs = item.TotalDurationMs
	}

	for groupIndex := range groups {
		group := &groups[groupIndex]
		for turnIndex := range group.Turns {
			turn := &group.Turns[turnIndex]
			applySummary(&turn.Execution)
			if item, exists := enrichment[turn.Execution.RequestID]; exists {
				turn.LLMCallCount = item.CallCount
				turn.HistoryStatus = item.HistoryStatus
			}
			for relatedIndex := range turn.RelatedExecutions {
				applySummary(&turn.RelatedExecutions[relatedIndex])
			}
		}
		for orphanIndex := range group.Orphans {
			applySummary(&group.Orphans[orphanIndex])
		}
	}
}

func paginateGroupedExecutionUnits(
	groups []GroupedExecutionGroup,
	query *groupedExecutionQuery,
	snapshotScore string,
	snapshotMember string,
	partial bool,
	llmEnrichmentIncomplete bool,
) *GroupedExecutionListResponse {
	if query.Cursor != nil {
		cursorGroup := GroupedExecutionGroup{
			GroupKey:        query.Cursor.GroupKey,
			AnchorCreatedAt: time.Unix(0, query.Cursor.AnchorCreatedAt),
			sortValue:       query.Cursor.SortValue,
		}
		filtered := groups[:0]
		for _, group := range groups {
			if compareGroupedExecutionPosition(group, cursorGroup, query) > 0 {
				filtered = append(filtered, group)
			}
		}
		groups = filtered
	}

	hasMore := len(groups) > query.Limit
	if hasMore && query.Sort == "total_duration_ms" {
		// The combined duration includes mutable DB 7 LLM data. It is valid for
		// a bounded point-in-time sort, but cannot be used as a keyset cursor
		// across requests without risking skipped or duplicated groups.
		partial = true
	}
	if hasMore {
		groups = groups[:query.Limit]
	}
	response := &GroupedExecutionListResponse{
		Groups:                  nonNilSlice(groups),
		QueryFingerprint:        query.Fingerprint,
		LLMEnrichmentIncomplete: llmEnrichmentIncomplete,
		Partial:                 partial,
		Timestamp:               time.Now(),
	}
	if hasMore && !partial && len(groups) > 0 {
		last := groups[len(groups)-1]
		cursor := groupedExecutionCursor{
			Version:         groupedCursorSchema,
			Fingerprint:     query.Fingerprint,
			SnapshotScore:   snapshotScore,
			SnapshotMember:  snapshotMember,
			SortValue:       last.sortValue,
			AnchorCreatedAt: last.AnchorCreatedAt.UnixNano(),
			GroupKey:        last.GroupKey,
		}
		if encoded, err := encodeGroupedExecutionCursor(cursor); err == nil {
			response.NextCursor = encoded
		}
	}
	for i := range response.Groups {
		response.Groups[i].sortValue = groupedSortValue{}
	}
	return response
}

func encodeGroupedExecutionCursor(cursor groupedExecutionCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > viewerGroupedCursorMaxEncodedLength {
		return "", fmt.Errorf("cursor exceeds encoded limit")
	}
	return encoded, nil
}

func enrichExecutionSummariesFromLLMDebug(
	ctx context.Context,
	summaries []ExecutionSummary,
) (map[string]llmDebugEnrichment, bool, error) {
	enrichment := make(map[string]llmDebugEnrichment, len(summaries))
	if len(summaries) == 0 || useMock {
		return enrichment, false, nil
	}
	client, err := getLLMDebugReadClient()
	if err != nil {
		return enrichment, false, err
	}

	// Prefer the newest executions when a bounded grouped/timeline request
	// contains more candidates than the DB 7 enrichment budget. The DB 8
	// membership remains complete up to its independent bounds, while callers
	// receive an explicit incomplete signal for optional LLM-derived fields.
	summaryIndexes := make([]int, len(summaries))
	for i := range summaries {
		summaryIndexes[i] = i
	}
	sort.SliceStable(summaryIndexes, func(i, j int) bool {
		left := summaries[summaryIndexes[i]]
		right := summaries[summaryIndexes[j]]
		if left.CreatedAt.Equal(right.CreatedAt) {
			return left.RequestID < right.RequestID
		}
		return left.CreatedAt.After(right.CreatedAt)
	})
	incomplete := len(summaryIndexes) > viewerLLMEnrichmentExecutionLimit
	if incomplete {
		summaryIndexes = summaryIndexes[:viewerLLMEnrichmentExecutionLimit]
	}

	pipe := client.Pipeline()
	interactionCommands := make(map[string]*redis.StringSliceCmd, len(summaryIndexes))
	metaCommands := make(map[string]*redis.IntCmd, len(summaryIndexes))
	legacyCommands := make(map[string]*redis.StringCmd, len(summaryIndexes))
	for _, summaryIndex := range summaryIndexes {
		summary := summaries[summaryIndex]
		requestID := summary.RequestID
		interactionCommands[requestID] = pipe.LRange(
			ctx,
			viewerLLMDebugKeyPrefix+requestID+viewerLLMDebugInterSuffix,
			0,
			viewerLLMInteractionLimit,
		)
		metaCommands[requestID] = pipe.Exists(
			ctx,
			viewerLLMDebugKeyPrefix+requestID+viewerLLMDebugMetaSuffix,
		)
		legacyCommands[requestID] = pipe.GetRange(
			ctx,
			viewerLLMDebugKeyPrefix+requestID,
			0,
			viewerLLMInteractionBytesLimit,
		)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return enrichment, incomplete, err
	}

	for _, summaryIndex := range summaryIndexes {
		requestID := summaries[summaryIndex].RequestID
		var interactions []orchestration.LLMInteraction
		recordIncomplete := false
		serialized, listErr := interactionCommands[requestID].Result()
		if listErr == nil {
			if len(serialized) > viewerLLMInteractionLimit {
				incomplete = true
				recordIncomplete = true
			}
			if !recordIncomplete {
				serializedBytes := 0
				for _, raw := range serialized {
					if len(raw) > viewerLLMInteractionBytesLimit-serializedBytes {
						incomplete = true
						recordIncomplete = true
						break
					}
					serializedBytes += len(raw)
					var interaction orchestration.LLMInteraction
					if json.Unmarshal([]byte(raw), &interaction) != nil {
						incomplete = true
						recordIncomplete = true
						break
					}
					interactions = append(interactions, interaction)
				}
			}
		}
		if len(interactions) == 0 && !recordIncomplete {
			if legacy, legacyErr := legacyCommands[requestID].Bytes(); legacyErr == nil {
				if len(legacy) > viewerLLMInteractionBytesLimit {
					incomplete = true
					recordIncomplete = true
				} else if len(legacy) > 0 {
					var record orchestration.LLMDebugRecord
					if decodeViewerStoredJSON(
						legacy,
						&record,
						viewerLLMInteractionBytesLimit,
					) != nil {
						incomplete = true
						recordIncomplete = true
					} else {
						interactions = record.Interactions
						if len(interactions) > viewerLLMInteractionLimit {
							incomplete = true
							recordIncomplete = true
						}
					}
				}
			}
		}
		if recordIncomplete {
			// Do not publish partial duration/count values as exact enrichment.
			// Callers still receive DB 8 membership plus the incomplete signal.
			continue
		}
		if len(interactions) == 0 {
			if metaCount, metaErr := metaCommands[requestID].Result(); metaErr == nil &&
				metaCount > 0 {
				enrichment[requestID] = llmDebugEnrichment{
					HistoryStatus: "not_recorded",
				}
			}
			continue
		}

		deduped := orchestration.DedupeLLMInteractions(interactions)
		item := llmDebugEnrichment{
			CallCount:     len(deduped),
			HistoryStatus: "not_recorded",
		}
		for _, interaction := range deduped {
			item.TotalDurationMs += interaction.DurationMs
			if strings.Contains(interaction.Type, "conversation_history") {
				item.HistoryStatus = "recorded"
			}
		}
		enrichment[requestID] = item
		summaries[summaryIndex].LLMTotalDurationMs = item.TotalDurationMs
	}
	return enrichment, incomplete, nil
}

func decodeViewerStoredJSON(data []byte, target interface{}, maxBytes int) error {
	if len(data) == 0 {
		return fmt.Errorf("empty data")
	}
	payload := data
	switch data[0] {
	case 0:
		payload = data[1:]
	case 1:
		reader, err := gzip.NewReader(bytes.NewReader(data[1:]))
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		payload, err = io.ReadAll(io.LimitReader(reader, int64(maxBytes)+1))
		if err != nil {
			return err
		}
	}
	if len(payload) > maxBytes {
		return fmt.Errorf("decoded payload exceeds limit")
	}
	return json.Unmarshal(payload, target)
}
