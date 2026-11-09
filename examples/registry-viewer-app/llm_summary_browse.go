package main

import (
	"container/list"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/orchestration"
	"golang.org/x/sync/singleflight"
)

// These bounds apply only to the Viewer's optional list-summary projection.
// Explicit detail reads continue to use the existing debug-record endpoint.
const (
	viewerLLMSummaryBatchSize    = 5
	viewerLLMSummaryReaders      = 2
	viewerLLMSummaryCacheEntries = 256
	viewerLLMSummaryCacheTTL     = time.Minute
	viewerLLMSummaryRecordBytes  = 16 * 1024 * 1024
	viewerLLMSummaryRows         = 512
	viewerLLMSummaryChunkRows    = 8
)

// LLMSummaryValues contains only derived list facts, never interaction bodies.
type LLMSummaryValues struct {
	InteractionCount int  `json:"interaction_count"`
	TotalTokens      int  `json:"total_tokens"`
	HasErrors        bool `json:"has_errors"`
}

type llmRowSummary struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	*LLMSummaryValues
}

// Both writers append atomically, but updated_at alone has only second
// precision. The list length detects additional writes within the same second.
type llmSummaryVersion struct {
	RequestID string
	CreatedAt string
	UpdatedAt string
	Rows      int64
}

type llmSummaryCacheEntry struct {
	key     string
	version llmSummaryVersion
	values  LLMSummaryValues
	expires time.Time
}

type llmSummaryReader struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     *list.List
	flights singleflight.Group
	slots   chan struct{}
}

func newLLMSummaryReader() *llmSummaryReader {
	return &llmSummaryReader{entries: make(map[string]*list.Element), lru: list.New(), slots: make(chan struct{}, viewerLLMSummaryReaders)}
}

var viewerLLMSummaryReader = newLLMSummaryReader()

func (reader *llmSummaryReader) cached(key string, version llmSummaryVersion) (LLMSummaryValues, bool) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if element := reader.entries[key]; element != nil {
		entry := element.Value.(llmSummaryCacheEntry)
		if entry.version == version && time.Now().Before(entry.expires) {
			reader.lru.MoveToFront(element)
			return entry.values, true
		}
		reader.lru.Remove(element)
		delete(reader.entries, key)
	}
	return LLMSummaryValues{}, false
}

func (reader *llmSummaryReader) remember(key string, version llmSummaryVersion, values LLMSummaryValues) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if old := reader.entries[key]; old != nil {
		reader.lru.Remove(old)
	}
	reader.entries[key] = reader.lru.PushFront(llmSummaryCacheEntry{key: key, version: version, values: values, expires: time.Now().Add(viewerLLMSummaryCacheTTL)})
	for reader.lru.Len() > viewerLLMSummaryCacheEntries {
		oldest := reader.lru.Back()
		delete(reader.entries, oldest.Value.(llmSummaryCacheEntry).key)
		reader.lru.Remove(oldest)
	}
}

func readLLMSummaryVersion(ctx context.Context, client redis.UniversalClient, id string) (llmSummaryVersion, error) {
	pipe := client.Pipeline()
	meta := pipe.HMGet(ctx, viewerLLMKeys.Meta(id), "request_id", "created_at", "updated_at")
	rows := pipe.LLen(ctx, viewerLLMKeys.Interactions(id))
	if _, err := pipe.Exec(ctx); err != nil {
		return llmSummaryVersion{}, err
	}
	values := meta.Val()
	if len(values) != 3 || values[0] != id {
		return llmSummaryVersion{}, orchestration.ErrLLMDebugRecordNotFound
	}
	created, _ := values[1].(string)
	updated, _ := values[2].(string)
	return llmSummaryVersion{RequestID: id, CreatedAt: created, UpdatedAt: updated, Rows: rows.Val()}, nil
}

// Read-only, bounded projection over the existing list. Checking string lengths
// in Redis keeps oversized rows off the wire and out of the Viewer heap. The
// script neither changes records nor refreshes their TTLs, and its single key
// works with Redis Cluster. A chunk is never accepted as a complete summary.
var readLLMSummaryChunk = redis.NewScript(`
local rows = {}
local bytes = 0
local remaining = tonumber(ARGV[3])
for index = tonumber(ARGV[1]), tonumber(ARGV[2]) do
    local row = redis.call("LINDEX", KEYS[1], index)
    if not row then return {-1, 0, {}} end
    if #row > remaining - bytes then return {0, 0, {}} end
    bytes = bytes + #row
    rows[#rows + 1] = row
end
return {1, bytes, rows}
`)

func summarizeLLMRows(ctx context.Context, client redis.UniversalClient, id string, version llmSummaryVersion) (LLMSummaryValues, string) {
	if version.Rows > viewerLLMSummaryRows {
		return LLMSummaryValues{}, "record_too_large"
	}
	interactions := make([]orchestration.LLMInteraction, 0, int(version.Rows))
	bytesRead := 0
	for start := int64(0); start < version.Rows; start += viewerLLMSummaryChunkRows {
		end := min(start+viewerLLMSummaryChunkRows, version.Rows) - 1
		chunk, err := readLLMSummaryChunk.Run(ctx, client, []string{viewerLLMKeys.Interactions(id)}, start, end, viewerLLMSummaryRecordBytes-bytesRead).Slice()
		if err != nil {
			return LLMSummaryValues{}, "read_unavailable"
		}
		if len(chunk) != 3 || chunk[0] != int64(1) {
			if len(chunk) == 3 && chunk[0] == int64(0) {
				return LLMSummaryValues{}, "record_too_large"
			}
			return LLMSummaryValues{}, "record_changed"
		}
		rows, ok := chunk[2].([]interface{})
		if !ok || int64(len(rows)) != end-start+1 {
			return LLMSummaryValues{}, "record_invalid"
		}
		for _, raw := range rows {
			text, ok := raw.(string)
			if !ok {
				return LLMSummaryValues{}, "record_invalid"
			}
			bytesRead += len(text)
			var interaction orchestration.LLMInteraction
			if json.Unmarshal([]byte(text), &interaction) != nil || strings.TrimSpace(text) == "null" {
				return LLMSummaryValues{}, "record_invalid"
			}
			interactions = append(interactions, interaction)
		}
	}
	current, err := readLLMSummaryVersion(ctx, client, id)
	if err != nil || current != version {
		return LLMSummaryValues{}, "record_changed"
	}
	values := LLMSummaryValues{}
	for _, interaction := range orchestration.DedupeLLMInteractions(interactions) {
		values.InteractionCount++
		values.TotalTokens += interaction.TotalTokens
		values.HasErrors = values.HasErrors || !interaction.Success
	}
	return values, ""
}

func (reader *llmSummaryReader) load(ctx context.Context, client redis.UniversalClient, id string) llmRowSummary {
	unavailable := llmRowSummary{RequestID: id, State: "unavailable", Reason: "read_unavailable"}
	version, err := readLLMSummaryVersion(ctx, client, id)
	if err != nil {
		if errors.Is(err, orchestration.ErrLLMDebugRecordNotFound) {
			unavailable.Reason = "record_missing"
		}
		return unavailable
	}
	key := viewerLLMKeys.Meta(id)
	if values, ok := reader.cached(key, version); ok {
		return llmRowSummary{RequestID: id, State: "ready", LLMSummaryValues: &values}
	}
	stamp, _ := json.Marshal(version)
	result := reader.flights.DoChan(key+string(stamp), func() (interface{}, error) {
		// One caller leaving must not cancel work shared with other operators.
		// Detached work retains correlation and has its own fixed read deadline.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), viewerDetailReadTimeout)
		defer cancel()
		select {
		case reader.slots <- struct{}{}:
			defer func() { <-reader.slots }()
		case <-work.Done():
			return unavailable, nil
		}
		if values, ok := reader.cached(key, version); ok {
			return llmRowSummary{RequestID: id, State: "ready", LLMSummaryValues: &values}, nil
		}
		values, reason := summarizeLLMRows(work, client, id, version)
		if reason != "" {
			return llmRowSummary{RequestID: id, State: "unavailable", Reason: reason}, nil
		}
		reader.remember(key, version, values)
		return llmRowSummary{RequestID: id, State: "ready", LLMSummaryValues: &values}, nil
	})
	select {
	case <-ctx.Done():
		return unavailable
	case result := <-result:
		return result.Val.(llmRowSummary)
	}
}

func handleLLMRowSummaries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ids := r.URL.Query()["request_id"]
	if len(ids) == 0 || len(ids) > viewerLLMSummaryBatchSize {
		http.Error(w, "specify between one and five request_id values", http.StatusBadRequest)
		return
	}
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" || len(id) > 512 || seen[id] {
			http.Error(w, "request_id values must be nonempty, distinct and at most 512 bytes", http.StatusBadRequest)
			return
		}
		seen[id] = true
	}
	// Browser responses are refreshed through the normal API boundary. Only
	// compact, version-checked derived values live in the process cache.
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), viewerDetailReadTimeout)
	defer cancel()
	var client redis.UniversalClient
	if !useMock {
		var err error
		client, err = getLLMDebugReadClient()
		if err != nil {
			http.Error(w, "LLM summaries unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	rows := make([]llmRowSummary, 0, len(ids))
	for _, id := range ids {
		if useMock {
			row := llmRowSummary{RequestID: id, State: "unavailable", Reason: "record_missing"}
			for _, summary := range getMockLLMDebugSummaries() {
				if summary.RequestID == id {
					row = llmRowSummary{RequestID: id, State: "ready", LLMSummaryValues: &LLMSummaryValues{InteractionCount: summary.InteractionCount, TotalTokens: summary.TotalTokens, HasErrors: summary.HasErrors}}
					break
				}
			}
			rows = append(rows, row)
		} else {
			rows = append(rows, viewerLLMSummaryReader.load(ctx, client, id))
		}
	}
	writeJSON(w, map[string]interface{}{"records": rows})
}

const (
	viewerLLMHeaderPageSize    = 30
	viewerLLMHeaderMaxPageSize = 100
	viewerLLMHeaderScanLimit   = 200
	viewerLLMHeaderBatchSize   = 32
	viewerLLMHeaderCursorScope = "viewer-llm-headers-desc-v1"
)

// Counts and outcomes are deliberately absent: existing Redis metadata cannot
// prove deduplicated totals without reading the full interaction record.
type llmRecordHeader struct {
	RequestID         string     `json:"request_id"`
	OriginalRequestID string     `json:"original_request_id,omitempty"`
	TraceID           string     `json:"trace_id,omitempty"`
	CreatedAt         *time.Time `json:"created_at,omitempty"`
	OriginatingAgent  string     `json:"originating_agent,omitempty"`
	SummaryPending    bool       `json:"summary_pending"`
}

type llmMetadataPage struct {
	Records        []llmRecordHeader `json:"records"`
	Total          int               `json:"total"`
	HasMore        bool              `json:"has_more"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	Scanned        int               `json:"scanned"`
	Coverage       string            `json:"coverage"`
	SummaryPending bool              `json:"summary_pending"`
	Timestamp      time.Time         `json:"timestamp"`
}

func handleLLMMetadataList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit, cursor, err := parseLLMMetadataQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if useMock {
		response := mockLLMMetadataPage(limit, cursor)
		writeJSON(w, response)
		return
	}
	client, err := getLLMDebugReadClient()
	if err != nil {
		http.Error(w, "LLM record metadata unavailable", http.StatusServiceUnavailable)
		return
	}
	response, err := loadLLMMetadataPage(r.Context(), client, limit, cursor)
	if err != nil {
		writeBrowseError(w, err, "LLM record metadata unavailable")
		return
	}
	writeJSON(w, response)
}

func parseLLMMetadataQuery(r *http.Request) (int, *executionBrowseCursor, error) {
	limit := viewerLLMHeaderPageSize
	if values, present := r.URL.Query()["limit"]; present {
		if len(values) != 1 {
			return 0, nil, errors.New("limit must be specified once")
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed <= 0 {
			return 0, nil, errors.New("limit must be a positive integer")
		}
		limit = min(parsed, viewerLLMHeaderMaxPageSize)
	}
	values, present := r.URL.Query()["cursor"]
	if !present {
		return limit, nil, nil
	}
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > viewerBrowseCursorMaxBytes {
		return 0, nil, errors.New("invalid LLM metadata cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(values[0])
	if err != nil {
		return 0, nil, errors.New("invalid LLM metadata cursor")
	}
	var cursor executionBrowseCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 2 || cursor.Fingerprint != viewerLLMHeaderCursorScope || cursor.Member == "" || len(cursor.Member) > 512 {
		return 0, nil, errors.New("cursor does not match LLM metadata browsing")
	}
	score, err := strconv.ParseFloat(cursor.Score, 64)
	if err != nil || math.IsInf(score, 0) || math.IsNaN(score) {
		return 0, nil, errors.New("invalid LLM metadata cursor score")
	}
	return limit, &cursor, nil
}

func loadLLMMetadataPage(parent context.Context, client redis.UniversalClient, limit int, cursor *executionBrowseCursor) (*llmMetadataPage, error) {
	ctx, cancel := context.WithTimeout(parent, viewerDetailReadTimeout)
	defer cancel()
	page := &llmMetadataPage{Records: []llmRecordHeader{}, Coverage: "page", SummaryPending: true, Timestamp: time.Now()}
	var position *executionBrowseCursor
	if cursor != nil {
		copied := *cursor
		position = &copied
	}
	for page.Scanned < viewerLLMHeaderScanLimit && len(page.Records) < limit {
		batchSize := min(viewerLLMHeaderBatchSize, viewerLLMHeaderScanLimit-page.Scanned)
		rows, err := readBrowseIndexBatch(ctx, client, viewerLLMKeys.RecentIndex(), "-inf", "+inf", false, position, batchSize+1)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			page.HasMore = false
			break
		}
		processCount := min(len(rows), batchSize)
		pipe := client.Pipeline()
		commands := make([]*redis.SliceCmd, processCount)
		for index, row := range rows[:processCount] {
			id, _ := row.Member.(string)
			// Fixed metadata fields avoid downloading arbitrary user notes or any
			// interaction body. LLEN would count physical paired rows, not calls.
			commands[index] = pipe.HMGet(ctx, viewerLLMKeys.Meta(id), "request_id", "original_request_id", "trace_id", "created_at", "originating_agent")
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
		for index, row := range rows[:processCount] {
			page.Scanned++
			page.HasMore = index+1 < len(rows)
			score := strconv.FormatFloat(row.Score, 'g', -1, 64)
			id, _ := row.Member.(string)
			position = &executionBrowseCursor{Version: 2, Fingerprint: viewerLLMHeaderCursorScope, Score: score, Member: id}
			values, err := commands[index].Result()
			if err != nil {
				return nil, err
			}
			if header, present := llmHeaderFromMetadata(id, values); present {
				page.Records = append(page.Records, header)
			}
			if len(page.Records) == limit {
				break
			}
		}
	}
	page.Total = len(page.Records)
	if page.HasMore && position != nil {
		encoded, err := json.Marshal(position)
		if err != nil {
			return nil, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func llmHeaderFromMetadata(id string, values []interface{}) (llmRecordHeader, bool) {
	value := func(index int) string {
		if index >= len(values) {
			return ""
		}
		result, _ := values[index].(string)
		return result
	}
	if id == "" || value(0) != id {
		return llmRecordHeader{}, false
	}
	header := llmRecordHeader{RequestID: id, OriginalRequestID: value(1), TraceID: value(2), OriginatingAgent: value(4), SummaryPending: true}
	if seconds, err := strconv.ParseInt(value(3), 10, 64); err == nil {
		created := time.Unix(seconds, 0).UTC()
		header.CreatedAt = &created
	} else if created, err := time.Parse(time.RFC3339Nano, value(3)); err == nil {
		header.CreatedAt = &created
	}
	return header, true
}

func mockLLMMetadataPage(limit int, cursor *executionBrowseCursor) *llmMetadataPage {
	page := &llmMetadataPage{Records: []llmRecordHeader{}, Coverage: "page", SummaryPending: true, Timestamp: time.Now()}
	rows := getMockLLMDebugSummaries()
	start := 0
	if cursor != nil {
		start = len(rows)
		for index, row := range rows {
			if row.RequestID == cursor.Member {
				start = index + 1
				break
			}
		}
	}
	end := min(start+limit, len(rows))
	for _, row := range rows[start:end] {
		created := row.CreatedAt
		page.Records = append(page.Records, llmRecordHeader{RequestID: row.RequestID, OriginalRequestID: row.OriginalRequestID, TraceID: row.TraceID, CreatedAt: &created, OriginatingAgent: row.OriginatingAgent, SummaryPending: true})
	}
	page.Scanned, page.Total = len(page.Records), len(page.Records)
	page.HasMore = end < len(rows)
	if page.HasMore {
		encoded, _ := json.Marshal(executionBrowseCursor{Version: 2, Fingerprint: viewerLLMHeaderCursorScope, Score: "0", Member: rows[end-1].RequestID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page
}
