package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
	"github.com/truvaagents/truva-g3/telemetry"
)

type metadataReadCommands struct {
	mu       sync.Mutex
	commands []string
}

func (*metadataReadCommands) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *metadataReadCommands) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		hook.mu.Lock()
		hook.commands = append(hook.commands, command.Name())
		hook.mu.Unlock()
		return next(ctx, command)
	}
}

func (hook *metadataReadCommands) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		hook.mu.Lock()
		for _, command := range commands {
			hook.commands = append(hook.commands, command.Name())
		}
		hook.mu.Unlock()
		return next(ctx, commands)
	}
}

func setupLLMMetadataTest(t *testing.T) *redis.Client {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousClient, previousKeys, previousMock := viewerRedisClient, viewerLLMKeys, useMock
	viewerRedisClient, useMock = client, false
	keyspace, err := core.NewRedisKeyspace("llm-header-tests")
	if err != nil {
		t.Fatal(err)
	}
	viewerLLMKeys = telemetry.NewRedisLLMDebugKeys(keyspace)
	t.Cleanup(func() {
		viewerRedisClient, viewerLLMKeys, useMock = previousClient, previousKeys, previousMock
		_ = client.Close()
	})
	return client
}

func TestLLMMetadataBrowsePaginatesScoreTiesWithoutPayloadReads(t *testing.T) {
	client := setupLLMMetadataTest(t)
	valid := make(map[string]bool)
	for index := 0; index < 70; index++ {
		id := fmt.Sprintf("request-%03d", index)
		if err := client.ZAdd(t.Context(), viewerLLMKeys.RecentIndex(), redis.Z{Score: 100, Member: id}).Err(); err != nil {
			t.Fatal(err)
		}
		if index%7 == 0 {
			continue // expired metadata entries must not stall pagination
		}
		valid[id] = true
		if err := client.HSet(t.Context(), viewerLLMKeys.Meta(id), "request_id", id, "original_request_id", "root", "created_at", "1794000000", "originating_agent", "example-agent").Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.RPush(t.Context(), viewerLLMKeys.Interactions(id), strings.Repeat("INTERACTION_BODY", 1000)).Err(); err != nil {
			t.Fatal(err)
		}
	}
	hook := &metadataReadCommands{}
	client.AddHook(hook)
	seen := make(map[string]bool)
	var cursor *executionBrowseCursor
	for pageNumber := 0; pageNumber < 20; pageNumber++ {
		page, err := loadLLMMetadataPage(t.Context(), client, 7, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Records) > 7 {
			t.Fatalf("page grew beyond requested size: %d", len(page.Records))
		}
		encoded, err := json.Marshal(page)
		if err != nil || strings.Contains(string(encoded), "INTERACTION_BODY") || strings.Contains(string(encoded), "interaction_count") || strings.Contains(string(encoded), "total_tokens") || strings.Contains(string(encoded), "has_errors") {
			t.Fatalf("metadata list included payloads or fabricated totals: %s (%v)", encoded, err)
		}
		for _, record := range page.Records {
			if seen[record.RequestID] || !valid[record.RequestID] || !record.SummaryPending {
				t.Fatalf("invalid/repeated header: %+v", record)
			}
			seen[record.RequestID] = true
		}
		if !page.HasMore {
			break
		}
		_, cursor, err = parseLLMMetadataQuery(httptest.NewRequest(http.MethodGet, "/api/llm-debug?limit=7&cursor="+page.NextCursor, nil))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != len(valid) {
		t.Fatalf("pagination missed retained headers: %d/%d", len(seen), len(valid))
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	for _, command := range hook.commands {
		if command == "get" || command == "lrange" || command == "llen" || command == "hgetall" {
			t.Fatalf("metadata list performed full-record/body read: %s", command)
		}
	}
}

func TestLLMMetadataBrowseEmptyBoundedPageCanContinue(t *testing.T) {
	client := setupLLMMetadataTest(t)
	for index := 0; index < 250; index++ {
		if err := client.ZAdd(t.Context(), viewerLLMKeys.RecentIndex(), redis.Z{Score: float64(index + 1), Member: fmt.Sprintf("expired-%03d", index)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.ZAdd(t.Context(), viewerLLMKeys.RecentIndex(), redis.Z{Score: 0, Member: "retained"}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(t.Context(), viewerLLMKeys.Meta("retained"), "request_id", "retained").Err(); err != nil {
		t.Fatal(err)
	}
	first, err := loadLLMMetadataPage(t.Context(), client, 30, nil)
	if err != nil || len(first.Records) != 0 || !first.HasMore || first.NextCursor == "" || first.Scanned != viewerLLMHeaderScanLimit {
		t.Fatalf("bounded empty page lost continuation: %+v err=%v", first, err)
	}
	_, cursor, err := parseLLMMetadataQuery(httptest.NewRequest(http.MethodGet, "/api/llm-debug?cursor="+first.NextCursor, nil))
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadLLMMetadataPage(t.Context(), client, 30, cursor)
	if err != nil || len(second.Records) != 1 || second.Records[0].RequestID != "retained" || second.HasMore {
		t.Fatalf("older retained record was unreachable: %+v err=%v", second, err)
	}
}

func TestLLMMetadataBrowseRejectsExecutionCursor(t *testing.T) {
	encoded, err := json.Marshal(executionBrowseCursor{Version: 2, Fingerprint: "execution-query", Score: "100", Member: "request-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = parseLLMMetadataQuery(httptest.NewRequest(http.MethodGet, "/api/llm-debug?cursor="+base64.RawURLEncoding.EncodeToString(encoded), nil))
	if err == nil {
		t.Fatal("cross-endpoint cursor was accepted")
	}
}

func seedLLMSummaryRecord(t *testing.T, client *redis.Client, id string, interactions ...orchestration.LLMInteraction) {
	t.Helper()
	if err := client.HSet(t.Context(), viewerLLMKeys.Meta(id), "request_id", id, "created_at", "100", "updated_at", "100").Err(); err != nil {
		t.Fatal(err)
	}
	for _, interaction := range interactions {
		raw, err := json.Marshal(interaction)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.RPush(t.Context(), viewerLLMKeys.Interactions(id), raw).Err(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLLMRowSummaryDedupesCachesAndObservesSameSecondWrites(t *testing.T) {
	client := setupLLMMetadataTest(t)
	typed := orchestration.LLMInteraction{Type: "plan_generation", Prompt: "kept only in raw storage", Response: "answer", DurationMs: 9, TotalTokens: 50, Success: true}
	shadow := typed
	shadow.Type = "agent_llm_call"
	failed := orchestration.LLMInteraction{Type: "synthesis", Prompt: "another prompt", TotalTokens: 7, Success: false}
	seedLLMSummaryRecord(t, client, "request", shadow, typed, failed)
	rawBefore := client.LRange(t.Context(), viewerLLMKeys.Interactions("request"), 0, -1).Val()
	reader := newLLMSummaryReader()
	first := reader.load(t.Context(), client, "request")
	if first.State != "ready" || first.InteractionCount != 2 || first.TotalTokens != 57 || !first.HasErrors {
		t.Fatalf("wrong deduplicated facts: %+v", first)
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "answer") {
		t.Fatalf("summary leaked payloads: %s, %v", encoded, err)
	}
	hook := &metadataReadCommands{}
	client.AddHook(hook)
	second := reader.load(t.Context(), client, "request")
	if second.State != "ready" || *second.LLMSummaryValues != *first.LLMSummaryValues {
		t.Fatalf("cache changed facts: %+v", second)
	}
	hook.mu.Lock()
	for _, command := range hook.commands {
		if command == "eval" || command == "evalsha" || command == "lrange" || command == "lindex" {
			t.Errorf("cache hit read interaction bodies: %s", command)
		}
	}
	hook.mu.Unlock()
	if got := client.LRange(t.Context(), viewerLLMKeys.Interactions("request"), 0, -1).Val(); !reflect.DeepEqual(got, rawBefore) {
		t.Fatal("summary read modified raw records")
	}
	// updated_at and created_at remain identical; the list length must bust cache.
	seedLLMSummaryRecord(t, client, "request", orchestration.LLMInteraction{Type: "synthesis", TotalTokens: 13, Success: true})
	third := reader.load(t.Context(), client, "request")
	if third.State != "ready" || third.InteractionCount != 3 || third.TotalTokens != 70 {
		t.Fatalf("same-second append left stale totals: %+v", third)
	}
	if err := client.Del(t.Context(), viewerLLMKeys.Meta("request"), viewerLLMKeys.Interactions("request")).Err(); err != nil {
		t.Fatal(err)
	}
	if got := reader.load(t.Context(), client, "request"); got.State != "unavailable" || got.Reason != "record_missing" || got.LLMSummaryValues != nil {
		t.Fatalf("cache hid expiry: %+v", got)
	}
}

func TestLLMRowSummaryRefusesPartialOrMalformedTotals(t *testing.T) {
	client := setupLLMMetadataTest(t)
	seedLLMSummaryRecord(t, client, "changed", orchestration.LLMInteraction{Type: "planning", Success: true})
	version, err := readLLMSummaryVersion(t.Context(), client, "changed")
	if err != nil {
		t.Fatal(err)
	}
	seedLLMSummaryRecord(t, client, "changed", orchestration.LLMInteraction{Type: "synthesis", Success: true})
	if _, reason := summarizeLLMRows(t.Context(), client, "changed", version); reason != "record_changed" {
		t.Fatalf("accepted a changing snapshot: %s", reason)
	}
	for name, raw := range map[string]string{"broken": "{", "null": "null", "oversized": strings.Repeat("X", viewerLLMSummaryRecordBytes+1)} {
		t.Run(name, func(t *testing.T) {
			seedLLMSummaryRecord(t, client, name)
			if err := client.RPush(t.Context(), viewerLLMKeys.Interactions(name), raw).Err(); err != nil {
				t.Fatal(err)
			}
			row := newLLMSummaryReader().load(t.Context(), client, name)
			if row.State != "unavailable" || row.LLMSummaryValues != nil {
				t.Fatalf("fabricated totals: %+v", row)
			}
			want := "record_invalid"
			if name == "oversized" {
				want = "record_too_large"
			}
			if row.Reason != want {
				t.Fatalf("reason = %q, want %q", row.Reason, want)
			}
		})
	}
	version.Rows = viewerLLMSummaryRows + 1
	if _, reason := summarizeLLMRows(t.Context(), client, "changed", version); reason != "record_too_large" {
		t.Fatalf("unbounded row scan: %s", reason)
	}
}

func TestLLMSummaryCacheIsBoundedAndExpires(t *testing.T) {
	reader := newLLMSummaryReader()
	version := llmSummaryVersion{RequestID: "record"}
	for index := 0; index <= viewerLLMSummaryCacheEntries; index++ {
		reader.remember(strconv.Itoa(index), version, LLMSummaryValues{TotalTokens: index})
	}
	if len(reader.entries) != viewerLLMSummaryCacheEntries || reader.lru.Len() != viewerLLMSummaryCacheEntries {
		t.Fatal("cache is not bounded")
	}
	if _, ok := reader.cached("0", version); ok {
		t.Fatal("oldest entry was not evicted")
	}
	entry := reader.entries["1"].Value.(llmSummaryCacheEntry)
	entry.expires = time.Now().Add(-time.Second)
	reader.entries["1"].Value = entry
	if _, ok := reader.cached("1", version); ok {
		t.Fatal("expired summary reused")
	}
}

func TestLLMRowSummaryHandlerBoundsAndCompactResponse(t *testing.T) {
	client := setupLLMMetadataTest(t)
	previous := viewerLLMSummaryReader
	viewerLLMSummaryReader = newLLMSummaryReader()
	t.Cleanup(func() { viewerLLMSummaryReader = previous })
	seedLLMSummaryRecord(t, client, "record", orchestration.LLMInteraction{Type: "planning", Prompt: "private prompt", TotalTokens: 11, Success: true})
	for _, query := range []string{"", "?request_id=", "?request_id=x&request_id=x", "?request_id=" + strings.Repeat("x", 513), "?request_id=1&request_id=2&request_id=3&request_id=4&request_id=5&request_id=6"} {
		w := httptest.NewRecorder()
		handleLLMRowSummaries(w, httptest.NewRequest(http.MethodGet, "/api/llm-debug/summaries"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid query %q: %d", query, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handleLLMRowSummaries(w, httptest.NewRequest(http.MethodGet, "/api/llm-debug/summaries?request_id=record&request_id=missing", nil))
	var result struct {
		Records []llmRowSummary `json:"records"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Records) != 2 || result.Records[0].State != "ready" || result.Records[1].State != "unavailable" {
		t.Fatalf("wrong batch response: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private prompt") || len(w.Body.Bytes()) > 512 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response must be compact and refreshed through API: %s", w.Body.String())
	}
}

// Hold the cold read long enough to exercise shared ownership and cancellation.
type summaryReadGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	reads   atomic.Int32
}

func (*summaryReadGate) DialHook(next redis.DialHook) redis.DialHook { return next }
func (*summaryReadGate) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (gate *summaryReadGate) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if command.Name() == "evalsha" {
			gate.reads.Add(1)
			gate.once.Do(func() { close(gate.entered) })
			select {
			case <-gate.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, command)
	}
}

func TestLLMSummarySharesColdWorkAcrossCancelledWaiters(t *testing.T) {
	client := setupLLMMetadataTest(t)
	seedLLMSummaryRecord(t, client, "shared", orchestration.LLMInteraction{Type: "planning", TotalTokens: 19, Success: true})
	if err := client.Expire(t.Context(), viewerLLMKeys.Meta("shared"), time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Expire(t.Context(), viewerLLMKeys.Interactions("shared"), time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	beforeMetaTTL := client.PTTL(t.Context(), viewerLLMKeys.Meta("shared")).Val()
	beforeRowsTTL := client.PTTL(t.Context(), viewerLLMKeys.Interactions("shared")).Val()
	if err := readLLMSummaryChunk.Load(t.Context(), client).Err(); err != nil {
		t.Fatal(err)
	}
	gate := &summaryReadGate{entered: make(chan struct{}), release: make(chan struct{})}
	client.AddHook(gate)
	reader := newLLMSummaryReader()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan llmRowSummary, 1)
	go func() { first <- reader.load(ctx, client, "shared") }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("cold read never started")
	}
	cancel()
	if row := <-first; row.State != "unavailable" {
		t.Fatalf("cancelled caller did not stop waiting: %+v", row)
	}
	results := make(chan llmRowSummary, 8)
	for range 8 {
		go func() { results <- reader.load(t.Context(), client, "shared") }()
	}
	close(gate.release)
	for range 8 {
		row := <-results
		if row.State != "ready" || row.TotalTokens != 19 {
			t.Fatalf("cancelled leader poisoned shared result: %+v", row)
		}
	}
	if gate.reads.Load() != 1 {
		t.Fatalf("cold work not coalesced: %d payload reads", gate.reads.Load())
	}
	if client.PTTL(t.Context(), viewerLLMKeys.Meta("shared")).Val() != beforeMetaTTL || client.PTTL(t.Context(), viewerLLMKeys.Interactions("shared")).Val() != beforeRowsTTL {
		t.Fatal("summary changed record retention")
	}
}
