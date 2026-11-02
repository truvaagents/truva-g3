package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// ExecutionLineageResponse contains retained relationship evidence, not an
// exhaustive history or relatives' execution/debug payloads.
type ExecutionLineageResponse struct {
	RequestID       string             `json:"request_id"`
	Executions      []ExecutionSummary `json:"executions"`
	ParentLookups   []ParentLookup     `json:"parent_lookups,omitempty"`
	Partial         bool               `json:"partial"`
	IndexIncomplete bool               `json:"index_incomplete"`
	Reasons         []string           `json:"reasons,omitempty"`
}

// ParentLookup distinguishes confirmed absence from an inconclusive read.
type ParentLookup struct {
	RequestID       string `json:"request_id"`
	ParentRequestID string `json:"parent_request_id"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
}

var errLineageExecutionNotFound = errors.New("selected execution not found")

// This is wire batching, not a read/coverage allowance: every candidate still
// uses the existing shared ordinary budget. Releasing response buffers between
// batches keeps a cold lookup from retaining thousands of unrelated payloads.
const lineageHydrationBatchSize = 128

func handleExecutionLineage(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if useMock {
		selected := getMockExecution(requestID)
		if selected == nil {
			http.Error(w, "execution not found", http.StatusNotFound)
			return
		}
		all := make(map[string]ExecutionSummary)
		for _, summary := range getMockExecutionSummaries() {
			all[summary.RequestID] = summary
		}
		all[requestID] = summarizeExecution(selected)
		family := connectedExecutionSummaries(r.Context(), requestID, all)
		response := &ExecutionLineageResponse{RequestID: requestID, Executions: sortedLineageSummaries(family)}
		for _, summary := range response.Executions {
			if summary.ParentRequestID == "" {
				continue
			}
			status, reason := "unknown", "not_indexed"
			if _, ok := family[summary.ParentRequestID]; ok {
				status, reason = "available", ""
			}
			response.ParentLookups = append(response.ParentLookups, ParentLookup{summary.RequestID, summary.ParentRequestID, status, reason})
		}
		writeJSON(w, response)
		return
	}
	response, err := loadExecutionLineage(r.Context(), requestID)
	if err != nil {
		if errors.Is(err, errLineageExecutionNotFound) {
			http.Error(w, "execution not found", http.StatusNotFound)
			return
		}
		if viewerLogger != nil {
			viewerLogger.WarnWithContext(r.Context(), "Execution lineage unavailable", map[string]interface{}{
				"operation": "viewer_execution_lineage", "request_id": core.GetRequestID(r.Context()),
				"status": "error", "error_type": "backend_read", "error": "selected execution could not be read",
			})
		}
		http.Error(w, "execution lineage unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, response)
}

func loadExecutionLineage(parent context.Context, requestID string) (*ExecutionLineageResponse, error) {
	client, err := getExecutionDebugClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	return loadExecutionLineageWithClient(ctx, client, requestID)
}

// loadExecutionLineageWithClient deliberately composes read primitives only.
// Existing list/timeline helpers may prune indexes or enrich debug data, so
// those high-level readers are not used here.
func loadExecutionLineageWithClient(ctx context.Context, client redis.UniversalClient, requestID string) (*ExecutionLineageResponse, error) {
	cache := newExecutionReadCache()
	projected := make(map[string]ExecutionSummary)
	summaryFor := func(id string, execution *StoredExecution) ExecutionSummary {
		if summary, ok := projected[id]; ok {
			return summary
		}
		return summarizeExecution(execution)
	}
	if _, err := cache.loadExactOwners(ctx, client, []string{requestID}); err != nil {
		return nil, err
	}
	if _, missing := cache.missing[requestID]; missing {
		return nil, errLineageExecutionNotFound
	}
	selected := cache.records[requestID]
	if selected == nil || selected.RequestID != requestID {
		return nil, errors.New("selected execution is unreadable")
	}
	response := &ExecutionLineageResponse{RequestID: requestID}
	reasons := make(map[string]struct{})
	addReason := func(reason string) {
		response.Partial = true
		reasons[reason] = struct{}{}
	}
	readFailure := func(err error) string {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return "cancelled"
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "lookup_deadline"
		}
		return "backend_read"
	}
	known := map[string]ExecutionSummary{requestID: summarizeExecution(selected)}
	visitedAncestors := make(map[string]struct{})
	loadAncestors := func() {
		for ctx.Err() == nil {
			var ids []string
			for _, id := range unresolvedExecutionOwnerIDs(known) {
				if _, done := visitedAncestors[id]; !done {
					visitedAncestors[id] = struct{}{}
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				return
			}
			limited, err := cache.loadExactOwners(ctx, client, ids)
			if limited && err == nil {
				if cache.exactHydrated >= viewerExactOwnerHydrationLimit {
					addReason("exact_hydration_limit")
				} else {
					addReason("record_unreadable")
				}
			}
			if err != nil {
				addReason(readFailure(err))
			}
			for _, id := range ids {
				if execution := cache.records[id]; execution != nil {
					if execution.RequestID != id {
						cache.unreadable[id] = struct{}{}
						delete(cache.records, id)
						addReason("record_unreadable")
						continue
					}
					known[id] = summaryFor(id, execution)
				}
			}
			if err != nil || limited {
				return
			}
		}
	}
	loadAncestors()
	var globalIDs, reverseIDs []string
	if ctx.Err() == nil {
		rows, err := client.ZRevRangeWithScores(ctx, viewerExecutionKeys.RecentIndex(), 0, viewerGlobalExecutionScanLimit).Result()
		if err != nil {
			addReason(readFailure(err))
		} else {
			if len(rows) > viewerGlobalExecutionScanLimit {
				rows = rows[:viewerGlobalExecutionScanLimit]
				addReason("recent_index_limit")
			}
			for _, row := range rows {
				if id, ok := row.Member.(string); ok && id != "" {
					globalIDs = append(globalIDs, id)
				} else {
					response.IndexIncomplete = true
				}
			}
		}
	}
	conversationID := selected.Metadata[orchestration.MetadataConversationID]
	if conversationID != "" && core.ValidateConversationID(conversationID) == core.ConversationIDValidationNone && ctx.Err() == nil {
		ids, err := client.ZRange(ctx, viewerConversationIndexKey(conversationID), 0, viewerConversationMemberReadLimit).Result()
		if err != nil {
			addReason(readFailure(err))
		} else {
			if len(ids) > viewerConversationMemberReadLimit {
				ids = ids[:viewerConversationMemberReadLimit]
				addReason("conversation_index_limit")
			}
			reverseIDs = ids
		}
	}
	if ctx.Err() == nil {
		candidates := append(append([]string(nil), globalIDs...), reverseIDs...)
		cache.projectLoaded = func(id string, execution *StoredExecution) *StoredExecution {
			if execution.RequestID == id {
				projected[id] = summarizeExecution(execution)
			}
			// Presence is retained to avoid rereads. Only the summary map is
			// used for presentation; this marker is never a full-record result.
			return &StoredExecution{
				RequestID: execution.RequestID, OriginalRequestID: execution.OriginalRequestID,
				ParentRequestID: execution.ParentRequestID, ParentStepID: execution.ParentStepID,
				Metadata: execution.Metadata,
			}
		}
		for start := 0; start < len(candidates) && ctx.Err() == nil; start += lineageHydrationBatchSize {
			end := min(start+lineageHydrationBatchSize, len(candidates))
			limited, err := cache.load(ctx, client, candidates[start:end])
			if limited && err == nil {
				if cache.hydrated >= viewerExecutionHydrationLimit {
					addReason("candidate_hydration_limit")
				} else {
					addReason("record_unreadable")
				}
			}
			if err != nil {
				addReason(readFailure(err))
				break
			}
		}
	}
	for id, execution := range cache.records {
		if ctx.Err() != nil {
			break
		}
		if execution.RequestID != id {
			cache.unreadable[id] = struct{}{}
			delete(cache.records, id)
			response.IndexIncomplete = true
			addReason("record_unreadable")
		}
	}
	for _, id := range append(append([]string(nil), globalIDs...), reverseIDs...) {
		if _, missing := cache.missing[id]; missing {
			response.IndexIncomplete = true
		}
		if _, unreadable := cache.unreadable[id]; unreadable {
			response.IndexIncomplete = true
		}
	}
	for _, id := range reverseIDs {
		if execution := cache.records[id]; execution != nil && execution.Metadata[orchestration.MetadataConversationID] != conversationID {
			response.IndexIncomplete = true
		}
	}
	if len(cache.unreadable) > 0 {
		addReason("record_unreadable")
	}
	// A fixed point includes descendants regardless of candidate ordering. Each
	// newly connected record may expose an ancestor outside the candidate page.
	for ctx.Err() == nil {
		all := make(map[string]ExecutionSummary, len(cache.records))
		for id, execution := range cache.records {
			if ctx.Err() != nil {
				break
			}
			all[id] = summaryFor(id, execution)
		}
		family := connectedExecutionSummaries(ctx, requestID, all)
		for id, summary := range known {
			family[id] = summary
		}
		before := len(cache.records)
		known = family
		loadAncestors()
		if len(cache.records) == before {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		addReason(readFailure(err))
	}
	if conversationID != "" && core.ValidateConversationID(conversationID) == core.ConversationIDValidationNone && ctx.Err() == nil {
		reverseSet := make(map[string]struct{}, len(reverseIDs))
		for _, id := range reverseIDs {
			reverseSet[id] = struct{}{}
		}
		var checks []string
		for id, summary := range known {
			if summary.ConversationID == conversationID {
				if _, inspected := reverseSet[id]; !inspected {
					checks = append(checks, id)
				}
			}
		}
		sort.Strings(checks)
		missing, err := findMissingConversationIndexMembers(ctx, client, map[string][]string{conversationID: checks})
		if err != nil {
			addReason(readFailure(err))
		} else if len(missing[conversationID]) > 0 {
			response.IndexIncomplete = true
		}
	}
	response.Executions = sortedLineageSummaries(known)
	for _, summary := range response.Executions {
		if summary.ParentRequestID == "" {
			continue
		}
		lookup := ParentLookup{RequestID: summary.RequestID, ParentRequestID: summary.ParentRequestID, Status: "unknown"}
		switch {
		case cache.records[summary.ParentRequestID] != nil:
			lookup.Status = "available"
		case containsLineageID(cache.missing, summary.ParentRequestID):
			lookup.Status, lookup.Reason = "unavailable", "not_found"
		case containsLineageID(cache.unreadable, summary.ParentRequestID):
			lookup.Reason = "record_unreadable"
		case ctx.Err() != nil:
			lookup.Reason = readFailure(ctx.Err())
		case cache.exactHydrated >= viewerExactOwnerHydrationLimit:
			lookup.Reason = "exact_hydration_limit"
		default:
			lookup.Reason = "backend_read"
		}
		response.ParentLookups = append(response.ParentLookups, lookup)
	}
	for reason := range reasons {
		response.Reasons = append(response.Reasons, reason)
	}
	sort.Strings(response.Reasons)
	return response, nil
}

func containsLineageID(ids map[string]struct{}, id string) bool {
	_, ok := ids[id]
	return ok
}

func connectedExecutionSummaries(ctx context.Context, selectedID string, all map[string]ExecutionSummary) map[string]ExecutionSummary {
	family := make(map[string]ExecutionSummary)
	if _, ok := all[selectedID]; !ok {
		return family
	}
	children := make(map[string][]string)
	rootMembers := make(map[string][]string)
	for id, summary := range all {
		if ctx.Err() != nil {
			return family
		}
		if summary.ParentRequestID != "" {
			children[summary.ParentRequestID] = append(children[summary.ParentRequestID], id)
		}
		root := summary.OriginalRequestID
		if root == "" {
			root = id
		}
		rootMembers[root] = append(rootMembers[root], id)
	}
	queue := []string{selectedID}
	queued := map[string]struct{}{selectedID: {}}
	visitedRoots := make(map[string]struct{})
	enqueue := func(id string) {
		if _, exists := all[id]; !exists {
			return
		}
		if _, seen := queued[id]; seen {
			return
		}
		queued[id] = struct{}{}
		queue = append(queue, id)
	}
	for len(queue) > 0 && ctx.Err() == nil {
		id := queue[0]
		queue = queue[1:]
		summary := all[id]
		family[id] = summary
		enqueue(summary.ParentRequestID)
		for _, child := range children[id] {
			enqueue(child)
		}
		root := summary.OriginalRequestID
		if root == "" {
			root = id
		}
		if _, seen := visitedRoots[root]; !seen {
			visitedRoots[root] = struct{}{}
			enqueue(root)
			for _, related := range rootMembers[root] {
				enqueue(related)
			}
		}
	}
	return family
}

func sortedLineageSummaries(summaries map[string]ExecutionSummary) []ExecutionSummary {
	result := make([]ExecutionSummary, 0, len(summaries))
	for _, summary := range summaries {
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.CreatedAt.IsZero() != right.CreatedAt.IsZero() {
			return !left.CreatedAt.IsZero()
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.RequestID < right.RequestID
	})
	return result
}
