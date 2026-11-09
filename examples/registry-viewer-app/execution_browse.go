package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/orchestration"
)

const (
	viewerBrowseDefaultLimit       = 30
	viewerBrowseMaxLimit           = 100
	viewerBrowseScanLimit          = 200
	viewerBrowseIndexBatch         = 32
	viewerBrowseRecordBytes        = 16 * 1024 * 1024
	viewerBrowseRequestBytes       = 32 * 1024 * 1024
	viewerBrowseCursorMaxBytes     = 2048
	viewerBrowseResumeIDMaxBytes   = 512
	viewerConversationTurnPageSize = 5
)

// Browse cursors bound work to an index position, including position within a
// shared score. Index mutations may change pages; this is not an immutable
// snapshot or a claim of complete historical group membership.
type executionBrowseCursor struct {
	Version      int    `json:"v"`
	Fingerprint  string `json:"f"`
	Score        string `json:"s"`
	Member       string `json:"m"`
	ExactEmitted bool   `json:"e,omitempty"`
}

type executionBrowseQuery struct {
	Limit          int
	Search         string
	ExactRequestID string
	Status         string
	Agent          string
	MinScore       string
	MaxScore       string
	Ascending      bool
	Cursor         *executionBrowseCursor
	Fingerprint    string
	ConversationID string
	TopLevelOnly   bool
}

type executionBrowsePage struct {
	Summaries  []ExecutionSummary
	NextCursor string
	HasMore    bool
	Scanned    int
	Partial    bool
	Reasons    []string
}

type boundedExecutionListResponse struct {
	Executions  []ExecutionSummary `json:"executions"`
	Total       int                `json:"total"`
	HasMore     bool               `json:"has_more"`
	NextCursor  string             `json:"next_cursor,omitempty"`
	Scanned     int                `json:"scanned"`
	Partial     bool               `json:"partial"`
	Coverage    string             `json:"coverage"`
	Limitations []string           `json:"limitations,omitempty"`
	Query       string             `json:"query,omitempty"`
	Timestamp   time.Time          `json:"timestamp"`
}

func parseExecutionBrowseQuery(r *http.Request, conversationID string) (*executionBrowseQuery, error) {
	values := r.URL.Query()
	q := &executionBrowseQuery{Limit: viewerBrowseDefaultLimit, Search: strings.ToLower(strings.TrimSpace(values.Get("q"))), Status: values.Get("status"), Agent: values.Get("agent"), MinScore: "-inf", MaxScore: "+inf", ConversationID: conversationID, TopLevelOnly: conversationID != ""}
	if candidate := strings.TrimSpace(values.Get("q")); len(candidate) > 0 && len(candidate) <= 512 && !strings.ContainsAny(candidate, " \t\n\r") {
		q.ExactRequestID = candidate
	}
	if q.Status == "" {
		q.Status = "all"
	}
	switch q.Status {
	case "all", "success", "failed", "interrupted", "running", "cancelled":
	default:
		return nil, errors.New("unsupported status")
	}
	if sortField := values.Get("sort"); sortField != "" && sortField != "created_at" {
		return nil, errors.New("bounded browsing supports created_at ordering; open an execution for duration details")
	}
	if direction := values.Get("direction"); direction != "" && direction != "desc" && direction != "asc" {
		return nil, errors.New("unsupported direction")
	} else {
		q.Ascending = direction == "asc"
	}
	if raw, present := values["limit"]; present {
		if len(raw) != 1 {
			return nil, errors.New("limit must be specified once")
		}
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit <= 0 {
			return nil, errors.New("limit must be a positive integer")
		}
		q.Limit = min(limit, viewerBrowseMaxLimit)
	}
	if conversationID != "" {
		q.Limit = min(q.Limit, viewerConversationTurnPageSize)
	}
	for _, bound := range []struct {
		name        string
		destination *string
	}{{"from", &q.MinScore}, {"to", &q.MaxScore}} {
		if raw := values.Get(bound.name); raw != "" {
			instant, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, fmt.Errorf("%s must be an RFC3339 timestamp", bound.name)
			}
			*bound.destination = strconv.FormatFloat(float64(instant.UnixNano()), 'g', -1, 64)
		}
	}
	if q.MinScore != "-inf" && q.MaxScore != "+inf" {
		low, _ := strconv.ParseFloat(q.MinScore, 64)
		high, _ := strconv.ParseFloat(q.MaxScore, 64)
		if low > high {
			return nil, errors.New("from must not be after to")
		}
	}
	q.Fingerprint = executionBrowseFingerprint(q)
	if raw, present := values["cursor"]; present {
		if len(raw) != 1 || len(raw[0]) == 0 || len(raw[0]) > viewerBrowseCursorMaxBytes {
			return nil, errors.New("invalid browse cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw[0])
		if err != nil {
			return nil, errors.New("invalid browse cursor")
		}
		var cursor executionBrowseCursor
		if json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 2 || cursor.Fingerprint != q.Fingerprint || len(cursor.Member) > 512 || (cursor.ExactEmitted && (q.ExactRequestID == "" || q.ConversationID != "")) {
			return nil, errors.New("cursor does not match this browse query")
		}
		if cursor.Member == "" {
			// A direct exact lookup may fill the result or byte budget before
			// index traversal starts. Its continuation resumes at the start
			// without repeating the direct lookup, even if that lookup failed.
			if q.ExactRequestID == "" || q.ConversationID != "" || cursor.Score != "" {
				return nil, errors.New("invalid browse cursor position")
			}
		} else {
			score, err := strconv.ParseFloat(cursor.Score, 64)
			if err != nil || math.IsInf(score, 0) || math.IsNaN(score) {
				return nil, errors.New("invalid cursor score")
			}
		}
		q.Cursor = &cursor
	}
	return q, nil
}

func executionBrowseFingerprint(q *executionBrowseQuery) string {
	data, _ := json.Marshal([]interface{}{q.Search, q.ExactRequestID, q.Status, q.Agent, q.MinScore, q.MaxScore, q.Ascending, q.ConversationID, q.TopLevelOnly})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func executionMatchesBrowse(summary ExecutionSummary, q *executionBrowseQuery) bool {
	if q.ConversationID != "" && summary.ConversationID != q.ConversationID {
		return false
	}
	if q.TopLevelOnly && !isTopLevelExecution(summary) {
		return false
	}
	if q.Agent != "" && summary.AgentName != q.Agent {
		return false
	}
	if q.MinScore != "-inf" {
		low, _ := strconv.ParseFloat(q.MinScore, 64)
		if float64(summary.CreatedAt.UnixNano()) < low {
			return false
		}
	}
	if q.MaxScore != "+inf" {
		high, _ := strconv.ParseFloat(q.MaxScore, 64)
		if float64(summary.CreatedAt.UnixNano()) > high {
			return false
		}
	}
	state := ""
	if summary.Lifecycle != nil {
		state = summary.Lifecycle.State
	}
	switch q.Status {
	case "success":
		if state != orchestration.RequestLifecycleCompleted {
			return false
		}
	case "failed":
		if state != orchestration.RequestLifecycleFailed && state != orchestration.RequestLifecycleCancelled {
			return false
		}
	case "interrupted":
		if !summary.Interrupted {
			return false
		}
	case "running":
		if state != orchestration.RequestLifecycleRunning {
			return false
		}
	case "cancelled":
		if state != orchestration.RequestLifecycleCancelled {
			return false
		}
	}
	if q.Search == "" {
		return true
	}
	for _, field := range []string{summary.OriginalRequest, summary.RequestID, summary.OriginalRequestID, summary.AgentName, summary.ConversationID, summary.TraceID} {
		if strings.Contains(strings.ToLower(field), q.Search) {
			return true
		}
	}
	return false
}

// List text is an explicitly marked display projection. Matching always uses
// the complete stored request first, and raw/detail readers remain untouched.
func projectBrowseSummary(summary ExecutionSummary) ExecutionSummary {
	preview := executionOverviewPreview(summary.OriginalRequest)
	summary.RequestTextTruncated = preview != summary.OriginalRequest
	summary.OriginalRequest = preview
	// The resume marker distinguishes a successor from the original checkpoint
	// execution in existing lineage views. Preserve only that bounded identity;
	// arbitrary application metadata remains in detail/raw records.
	resumeID := summary.Metadata["resume_checkpoint_id"]
	summary.Metadata = nil
	if resumeID != "" && len(resumeID) <= viewerBrowseResumeIDMaxBytes {
		summary.Metadata = map[string]string{"resume_checkpoint_id": resumeID}
	}
	return summary
}

var errBrowseRecordTooLarge = errors.New("execution exceeds browse decode limit")
var errBrowseCursorStale = errors.New("browse cursor changed or expired; refresh the list")

// Rank seeking is logarithmic in index size, even if a million members share
// one score. Caller-controlled offsets never make Redis walk a tie bucket.
// An anchor that moved or disappeared is explicit stale pagination, not a
// reason to silently skip equally scored records.
func readBrowseIndexBatch(ctx context.Context, client redis.UniversalClient, index, minScore, maxScore string, ascending bool, position *executionBrowseCursor, count int) ([]redis.Z, error) {
	if count <= 0 {
		return nil, nil
	}
	var rows []redis.Z
	var err error
	if position == nil {
		bounds := &redis.ZRangeBy{Min: minScore, Max: maxScore, Count: int64(count)}
		if ascending {
			rows, err = client.ZRangeByScoreWithScores(ctx, index, bounds).Result()
		} else {
			rows, err = client.ZRevRangeByScoreWithScores(ctx, index, bounds).Result()
		}
	} else {
		pipe := client.Pipeline()
		var rank *redis.IntCmd
		if ascending {
			rank = pipe.ZRank(ctx, index, position.Member)
		} else {
			rank = pipe.ZRevRank(ctx, index, position.Member)
		}
		score := pipe.ZScore(ctx, index, position.Member)
		if _, err = pipe.Exec(ctx); err != nil {
			if errors.Is(err, redis.Nil) {
				return nil, errBrowseCursorStale
			}
			return nil, err
		}
		expected, parseErr := strconv.ParseFloat(position.Score, 64)
		if parseErr != nil || score.Val() != expected {
			return nil, errBrowseCursorStale
		}
		start := rank.Val() + 1
		if ascending {
			rows, err = client.ZRangeWithScores(ctx, index, start, start+int64(count)-1).Result()
		} else {
			rows, err = client.ZRevRangeWithScores(ctx, index, start, start+int64(count)-1).Result()
		}
	}
	if err != nil {
		return nil, err
	}
	low, high := math.Inf(-1), math.Inf(1)
	if minScore != "-inf" {
		low, _ = strconv.ParseFloat(minScore, 64)
	}
	if maxScore != "+inf" {
		high, _ = strconv.ParseFloat(maxScore, 64)
	}
	filtered := rows[:0]
	for _, row := range rows {
		if row.Score >= low && row.Score <= high {
			filtered = append(filtered, row)
		}
	}
	return filtered, nil
}

func writeBrowseError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, errBrowseCursorStale) {
		http.Error(w, errBrowseCursorStale.Error(), http.StatusConflict)
		return
	}
	http.Error(w, message, http.StatusServiceUnavailable)
}

func deserializeBrowseExecution(data []byte, maxBytes int) (*StoredExecution, int, error) {
	if len(data) == 0 {
		return nil, 0, redis.Nil
	}
	decoded := data
	if data[0] == 1 {
		reader, err := gzip.NewReader(bytes.NewReader(data[1:]))
		if err != nil {
			return nil, 0, err
		}
		defer reader.Close()
		decoded, err = io.ReadAll(io.LimitReader(reader, int64(maxBytes)+1))
		if err != nil {
			return nil, len(decoded), err
		}
	} else if data[0] == 0 {
		decoded = data[1:]
	}
	if len(decoded) > maxBytes {
		return nil, len(decoded), errBrowseRecordTooLarge
	}
	var execution StoredExecution
	if err := json.Unmarshal(decoded, &execution); err != nil {
		return nil, len(decoded), err
	}
	return &execution, len(decoded), nil
}

func loadExecutionBrowsePage(parent context.Context, client redis.UniversalClient, index string, q *executionBrowseQuery) (*executionBrowsePage, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	page := &executionBrowsePage{Summaries: []ExecutionSummary{}}
	minScore, maxScore := q.MinScore, q.MaxScore
	var position *executionBrowseCursor
	exactEmitted := false
	if q.Cursor != nil {
		exactEmitted = q.Cursor.ExactEmitted
		if q.Cursor.Member != "" {
			clone := *q.Cursor
			position = &clone
		}
	}
	readBytes, decodedBytes := 0, 0
	addReason := func(reason string) {
		page.Partial = true
		for _, existing := range page.Reasons {
			if existing == reason {
				return
			}
		}
		page.Reasons = append(page.Reasons, reason)
	}
	// Surface an exact hit even when it is absent from the advisory index,
	// then continue bounded matching for descendants and other matching records.
	// Carry its identity in pagination state so it is not emitted twice.
	if q.Cursor == nil && q.ExactRequestID != "" && index == viewerExecutionKeys.RecentIndex() {
		data, err := client.GetRange(ctx, viewerExecutionKeys.Record(q.ExactRequestID), 0, viewerBrowseRecordBytes).Bytes()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
		page.Scanned = 1
		readBytes += len(data)
		if len(data) > 0 {
			if len(data) > viewerBrowseRecordBytes {
				addReason("record_too_large")
			} else {
				execution, size, decodeErr := deserializeBrowseExecution(data, viewerBrowseRecordBytes)
				decodedBytes += size
				switch {
				case errors.Is(decodeErr, errBrowseRecordTooLarge):
					addReason("record_too_large")
				case decodeErr != nil:
					addReason("record_unreadable")
				case execution.RequestID != q.ExactRequestID:
					addReason("record_identity_mismatch")
				default:
					summary := summarizeExecution(execution)
					if executionMatchesBrowse(summary, q) {
						page.Summaries = append(page.Summaries, projectBrowseSummary(summary))
						exactEmitted = true
					}
				}
			}
		}
	}
	if len(page.Summaries) == q.Limit {
		rows, err := readBrowseIndexBatch(ctx, client, index, minScore, maxScore, q.Ascending, nil, 2)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Member != q.ExactRequestID {
				page.HasMore = true
				break
			}
		}
		if page.HasMore {
			data, _ := json.Marshal(executionBrowseCursor{Version: 2, Fingerprint: q.Fingerprint, ExactEmitted: exactEmitted})
			page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
		}
		return page, nil
	}
	consume := func(row redis.Z) {
		score := strconv.FormatFloat(row.Score, 'g', -1, 64)
		member, _ := row.Member.(string)
		position = &executionBrowseCursor{Version: 2, Fingerprint: q.Fingerprint, Score: score, Member: member, ExactEmitted: exactEmitted}
		page.Scanned++
	}
	for page.Scanned < viewerBrowseScanLimit && len(page.Summaries) < q.Limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batchSize := min(viewerBrowseIndexBatch, viewerBrowseScanLimit-page.Scanned)
		rows, err := readBrowseIndexBatch(ctx, client, index, minScore, maxScore, q.Ascending, position, batchSize+1)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			page.HasMore = false
			break
		}
		processCount := min(len(rows), batchSize)
		for rowIndex, row := range rows[:processCount] {
			page.HasMore = rowIndex+1 < len(rows)
			id, ok := row.Member.(string)
			if !ok {
				consume(row)
				addReason("invalid_index_member")
				continue
			}
			if exactEmitted && id == q.ExactRequestID {
				consume(row)
				continue
			}
			remaining := viewerBrowseRequestBytes - readBytes
			if remaining <= 0 || decodedBytes >= viewerBrowseRequestBytes {
				page.HasMore = true
				addReason("browse_byte_budget")
				goto finish
			}
			readLimit := min(viewerBrowseRecordBytes, remaining)
			data, readErr := client.GetRange(ctx, viewerExecutionKeys.Record(id), 0, int64(readLimit)).Bytes()
			if readErr != nil && !errors.Is(readErr, redis.Nil) {
				return nil, readErr
			}
			readBytes += len(data)
			if len(data) > readLimit {
				if readLimit < viewerBrowseRecordBytes {
					page.HasMore = true
					addReason("browse_byte_budget")
					goto finish
				}
				consume(row)
				addReason("record_too_large")
				continue
			}
			decodeLimit := min(viewerBrowseRecordBytes, viewerBrowseRequestBytes-decodedBytes)
			execution, size, decodeErr := deserializeBrowseExecution(data, decodeLimit)
			if errors.Is(decodeErr, errBrowseRecordTooLarge) && decodeLimit < viewerBrowseRecordBytes {
				page.HasMore = true
				addReason("browse_byte_budget")
				goto finish
			}
			decodedBytes += size
			consume(row)
			if errors.Is(decodeErr, redis.Nil) {
				continue
			}
			if decodeErr != nil {
				if errors.Is(decodeErr, errBrowseRecordTooLarge) {
					addReason("record_too_large")
				} else {
					addReason("record_unreadable")
				}
				continue
			}
			if execution.RequestID != id {
				addReason("record_identity_mismatch")
				continue
			}
			summary := summarizeExecution(execution)
			if executionMatchesBrowse(summary, q) {
				page.Summaries = append(page.Summaries, projectBrowseSummary(summary))
			}
			if len(page.Summaries) == q.Limit {
				goto finish
			}
		}
		if len(rows) <= batchSize {
			page.HasMore = false
			break
		}
	}
	if page.Scanned >= viewerBrowseScanLimit && page.HasMore {
		addReason("browse_scan_budget")
	}
finish:
	if page.HasMore && position == nil && q.ExactRequestID != "" && index == viewerExecutionKeys.RecentIndex() {
		position = &executionBrowseCursor{Version: 2, Fingerprint: q.Fingerprint, ExactEmitted: exactEmitted}
	}
	if page.HasMore && position != nil {
		data, _ := json.Marshal(position)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}

func handleBoundedExecutionList(w http.ResponseWriter, r *http.Request, searchRequired bool) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q, err := parseExecutionBrowseQuery(r, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if searchRequired && q.Search == "" {
		http.Error(w, "query parameter 'q' is required", http.StatusBadRequest)
		return
	}
	var page *executionBrowsePage
	if useMock {
		page = mockExecutionBrowsePage(q)
	} else {
		client, clientErr := getExecutionDebugClient()
		if clientErr != nil {
			http.Error(w, "execution browsing unavailable", http.StatusServiceUnavailable)
			return
		}
		page, err = loadExecutionBrowsePage(r.Context(), client, viewerExecutionKeys.RecentIndex(), q)
	}
	if err != nil {
		writeBrowseError(w, err, "execution browsing unavailable")
		return
	}
	writeJSON(w, boundedExecutionListResponse{Executions: page.Summaries, Total: len(page.Summaries), HasMore: page.HasMore, NextCursor: page.NextCursor, Scanned: page.Scanned, Partial: page.Partial, Coverage: "page", Limitations: page.Reasons, Query: q.Search, Timestamp: time.Now()})
}

func mockExecutionBrowsePage(q *executionBrowseQuery) *executionBrowsePage {
	page := &executionBrowsePage{Summaries: []ExecutionSummary{}}
	items := getMockExecutionSummaries()
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			if q.Ascending {
				return items[i].RequestID < items[j].RequestID
			}
			return items[i].RequestID > items[j].RequestID
		}
		if q.Ascending {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	var position *executionBrowseCursor
	anchorReached := q.Cursor == nil
	for _, summary := range items {
		score := strconv.FormatFloat(float64(summary.CreatedAt.UnixNano()), 'g', -1, 64)
		if !anchorReached {
			if summary.RequestID == q.Cursor.Member {
				anchorReached = true
			}
			continue
		}
		if len(page.Summaries) == q.Limit {
			page.HasMore = true
			break
		}
		position = &executionBrowseCursor{Version: 2, Fingerprint: q.Fingerprint, Score: score, Member: summary.RequestID}
		page.Scanned++
		if executionMatchesBrowse(summary, q) {
			page.Summaries = append(page.Summaries, projectBrowseSummary(summary))
		}
	}
	if page.HasMore && position != nil {
		encoded, _ := json.Marshal(position)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page
}

func executionBrowseCompatibilityQuery(limit int, cursor, search string) (*executionBrowseQuery, error) {
	if limit <= 0 {
		limit = viewerBrowseDefaultLimit
	}
	values := url.Values{"limit": {strconv.Itoa(limit)}, "q": {search}}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	return parseExecutionBrowseQuery(&http.Request{URL: &url.URL{RawQuery: values.Encode()}}, "")
}

func handleBoundedGroupedExecutionList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q, err := parseExecutionBrowseQuery(r, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var page *executionBrowsePage
	if useMock {
		page = mockExecutionBrowsePage(q)
	} else {
		client, clientErr := getExecutionDebugClient()
		if clientErr != nil {
			http.Error(w, "execution browsing unavailable", http.StatusServiceUnavailable)
			return
		}
		page, err = loadExecutionBrowsePage(r.Context(), client, viewerExecutionKeys.RecentIndex(), q)
	}
	if err != nil {
		writeBrowseError(w, err, "execution browsing unavailable")
		return
	}
	direction := "desc"
	if q.Ascending {
		direction = "asc"
	}
	groups := buildGroupedExecutionUnits(page.Summaries, nil, nil, &groupedExecutionQuery{Status: "all", Sort: "created_at", Direction: direction})
	for index := range groups {
		groups[index].CountsScope = "page"
		// Keep all matches from this bounded summary page. The browser pages
		// them five at a time; truncating here would make later matches vanish.
		if len(groups[index].Turns) > viewerConversationTurnPageSize {
			groups[index].HasMoreTurns = true
		}
	}
	writeJSON(w, &GroupedExecutionListResponse{Groups: nonNilSlice(groups), HasMore: page.HasMore, NextCursor: page.NextCursor, Scanned: page.Scanned, QueryFingerprint: q.Fingerprint, Partial: page.Partial, Coverage: "page", Limitations: page.Reasons, Timestamp: time.Now()})
}

func handleBoundedConversationTimeline(w http.ResponseWriter, r *http.Request, conversationID string) {
	q, err := parseExecutionBrowseQuery(r, conversationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var page *executionBrowsePage
	if useMock {
		page = mockExecutionBrowsePage(q)
	} else {
		client, clientErr := getExecutionDebugClient()
		if clientErr != nil {
			http.Error(w, "conversation browsing unavailable", http.StatusServiceUnavailable)
			return
		}
		page, err = loadExecutionBrowsePage(r.Context(), client, viewerConversationIndexKey(conversationID), q)
	}
	if err != nil {
		writeBrowseError(w, err, "conversation browsing unavailable")
		return
	}
	response := buildConversationTimelineResponse(conversationID, page.Summaries, nil, page.Partial, false, false)
	response.HasMore = page.HasMore
	response.NextCursor = page.NextCursor
	response.Scanned = page.Scanned
	response.Coverage = "page"
	response.Limitations = page.Reasons
	writeJSON(w, response)
}
