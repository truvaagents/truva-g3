package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/orchestration"
)

type browseCommandObserver struct {
	bodies    atomic.Int64
	llmBodies atomic.Int64
}

func (*browseCommandObserver) DialHook(next redis.DialHook) redis.DialHook { return next }
func (o *browseCommandObserver) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "getrange" {
			o.bodies.Add(1)
		}
		if cmd.Name() == "lrange" {
			o.llmBodies.Add(1)
		}
		return next(ctx, cmd)
	}
}
func (*browseCommandObserver) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func browseQueryForTest(t *testing.T, raw string, conversationID string) *executionBrowseQuery {
	t.Helper()
	q, err := parseExecutionBrowseQuery(httptest.NewRequest("GET", "/api/executions?"+raw, nil), conversationID)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func seedBrowseExecution(t *testing.T, client *redis.Client, id string, score float64, request, conversation string) {
	t.Helper()
	execution := StoredExecution{RequestID: id, OriginalRequest: request, CreatedAt: time.Unix(1700000000, 0), Metadata: map[string]string{"conversation_id": conversation}}
	storeViewerExecution(t, client, execution)
	if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: score, Member: id}).Err(); err != nil {
		t.Fatal(err)
	}
	if conversation != "" {
		if err := client.ZAdd(t.Context(), viewerConversationIndexKey(conversation), redis.Z{Score: score, Member: id}).Err(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConversationTitleActivityTimeIsIndependentOfSnapshotOrder(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	laterStart := start.Add(time.Minute)
	hookStart := start.Add(-time.Second)
	first := StoredExecution{RequestID: "first-query", OriginalRequest: "First question", CreatedAt: start.Add(10 * time.Minute), Metadata: map[string]string{"conversation_id": "conversation-title"}, Plan: &RoutingPlan{CreatedAt: &start}, PipelineHooks: []orchestration.PipelineHookExecution{{StartedAt: hookStart}}}
	second := StoredExecution{RequestID: "second-query", OriginalRequest: "Second question", CreatedAt: start.Add(2 * time.Minute), Metadata: map[string]string{"conversation_id": "conversation-title"}, Plan: &RoutingPlan{CreatedAt: &laterStart}}
	for _, execution := range []StoredExecution{first, second} {
		storeViewerExecution(t, client, execution)
		if err := client.ZAdd(t.Context(), viewerConversationIndexKey("conversation-title"), redis.Z{Score: float64(execution.CreatedAt.UnixNano()), Member: execution.RequestID}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerConversationIndexKey("conversation-title"), browseQueryForTest(t, "direction=asc&limit=5", "conversation-title"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 2 || page.Summaries[0].RequestID != second.RequestID {
		t.Fatalf("snapshot ordering changed: %+v", page.Summaries)
	}
	if observed := page.Summaries[1].FirstObservedAt; observed == nil || !observed.Equal(hookStart) {
		t.Fatalf("first query activity time = %v, want %v", observed, hookStart)
	}
	if observed := page.Summaries[0].FirstObservedAt; observed == nil || !observed.Equal(laterStart) {
		t.Fatalf("second query activity time = %v, want %v", observed, laterStart)
	}
	if unknown := summarizeExecution(&StoredExecution{CreatedAt: start}); unknown.FirstObservedAt != nil {
		t.Fatal("snapshot time must not become request-start evidence")
	}
}

func TestGroupedBrowseKeepsEveryMatchInBoundedPage(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	previousClient, previousMock := viewerRedisClient, useMock
	viewerRedisClient, useMock = client, false
	t.Cleanup(func() { viewerRedisClient, useMock = previousClient, previousMock })
	for i := 0; i < 37; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("matching-turn-%02d", i), float64(i), "matching request", "busy-conversation")
	}
	observer := &browseCommandObserver{}
	client.AddHook(observer)
	recorder := httptest.NewRecorder()
	handleBoundedGroupedExecutionList(recorder, httptest.NewRequest("GET", "/api/executions?group_conversations=true&q=matching&limit=30", nil))
	if recorder.Code != 200 {
		t.Fatalf("grouped browse status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var page GroupedExecutionListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	// Five visible turns are a browser paging boundary, not permission for the
	// API to discard matches from the bounded thirty-summary browse page.
	if len(page.Groups) != 1 || len(page.Groups[0].Turns) != 30 || page.Groups[0].MatchingTurnCount != 30 || !page.Groups[0].HasMoreTurns || page.Groups[0].CountsScope != "page" || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("matching turns dropped or coverage overstated: %+v", page)
	}
	// Text search also performs one bounded direct-ID lookup before scanning.
	if observer.bodies.Load() != 31 || observer.llmBodies.Load() != 0 {
		t.Fatalf("grouping expanded reads: records=%d llm=%d", observer.bodies.Load(), observer.llmBodies.Load())
	}
}

func TestExecutionBrowsePaginatesEqualScoresWithoutLLMPayloads(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for i := 0; i < 7; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("request-%d", i), 100, "request", "")
	}
	observer := &browseCommandObserver{}
	client.AddHook(observer)
	for _, direction := range []string{"asc", "desc"} {
		seen := map[string]bool{}
		cursor := ""
		for pageNumber := 0; pageNumber < 5; pageNumber++ {
			params := url.Values{"limit": {"2"}, "direction": {direction}}
			if cursor != "" {
				params.Set("cursor", cursor)
			}
			page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, params.Encode(), ""))
			if err != nil {
				t.Fatal(err)
			}
			if page.Scanned > 2 || len(page.Summaries) > 2 {
				t.Fatalf("page not bounded: %+v", page)
			}
			for _, summary := range page.Summaries {
				if seen[summary.RequestID] {
					t.Fatalf("duplicate equal-score entry %q", summary.RequestID)
				}
				seen[summary.RequestID] = true
			}
			if !page.HasMore {
				break
			}
			if page.NextCursor == "" {
				t.Fatal("missing continuation")
			}
			cursor = page.NextCursor
		}
		if len(seen) != 7 {
			t.Fatalf("%s lost tied entries: %v", direction, seen)
		}
	}
	if observer.llmBodies.Load() != 0 {
		t.Fatal("list queried LLM interaction bodies")
	}
}

func TestExecutionBrowseFilteredScanCanContinuePastEmptyPage(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for i := 0; i < 210; i++ {
		request := "unrelated"
		if i == 0 {
			request = "needle in older request"
		}
		seedBrowseExecution(t, client, fmt.Sprintf("request-%03d", i), float64(i), request, "")
	}
	query := browseQueryForTest(t, "q=needle&limit=30", "")
	first, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Summaries) != 0 || first.Scanned != viewerBrowseScanLimit || !first.HasMore || first.NextCursor == "" || !first.Partial {
		t.Fatalf("empty bounded search cannot continue: %+v", first)
	}
	query = browseQueryForTest(t, "q=needle&limit=30&cursor="+url.QueryEscape(first.NextCursor), "")
	second, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Summaries) != 1 || second.Summaries[0].RequestID != "request-000" || second.HasMore {
		t.Fatalf("older match lost: %+v", second)
	}
}

func TestExecutionBrowseMissingKeysProgressAndExactIDsBypassIndex(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for i := 0; i < 40; i++ {
		if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: 100, Member: fmt.Sprintf("stale-%d", i)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	seedBrowseExecution(t, client, "retained", 1, "retained request", "")
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "limit=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 || page.Scanned != 41 || page.HasMore {
		t.Fatalf("stale entries blocked retained data: %+v", page)
	}
	if err := client.ZRem(t.Context(), viewerExecutionKeys.RecentIndex(), "retained").Err(); err != nil {
		t.Fatal(err)
	}
	page, err = loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=retained", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 || page.Scanned != 41 || page.HasMore {
		t.Fatalf("exact retained ID depends on recent index: %+v", page)
	}
}

func TestExecutionBrowseExactIDIncludesFamilyAcrossPages(t *testing.T) {
	for _, direction := range []string{"asc", "desc"} {
		for _, limit := range []int{1, 2, 30} {
			t.Run(fmt.Sprintf("%s-limit-%d", direction, limit), func(t *testing.T) {
				_, client := newConversationAPIRedis(t)
				seedBrowseExecution(t, client, "root", 3, "parent request", "")
				for i := 0; i < 6; i++ {
					id := fmt.Sprintf("delegation-%d", i)
					seedBrowseExecution(t, client, id, float64(i), "child request", "")
					storeViewerExecution(t, client, StoredExecution{RequestID: id, OriginalRequestID: "root", CreatedAt: time.Unix(1700000000, 0)})
				}
				observer := &browseCommandObserver{}
				client.AddHook(observer)
				params := url.Values{"q": {"root"}, "limit": {strconv.Itoa(limit)}, "direction": {direction}}
				seen := map[string]bool{}
				for pageNumber := 0; ; pageNumber++ {
					if pageNumber > 7 {
						t.Fatal("family pagination did not finish")
					}
					page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, params.Encode(), ""))
					if err != nil {
						t.Fatal(err)
					}
					if len(page.Summaries) > limit || page.Scanned > viewerBrowseScanLimit {
						t.Fatalf("unbounded family page: %+v", page)
					}
					if pageNumber == 0 && (len(page.Summaries) == 0 || page.Summaries[0].RequestID != "root") {
						t.Fatalf("direct exact hit missing: %+v", page)
					}
					for _, summary := range page.Summaries {
						if seen[summary.RequestID] {
							t.Fatalf("duplicate execution across pages: %s", summary.RequestID)
						}
						seen[summary.RequestID] = true
					}
					if !page.HasMore {
						break
					}
					if page.NextCursor == "" {
						t.Fatal("family page has no continuation")
					}
					params.Set("cursor", page.NextCursor)
				}
				if len(seen) != 7 || observer.bodies.Load() != 7 {
					t.Fatalf("family omitted or duplicate body hydration: seen=%v reads=%d", seen, observer.bodies.Load())
				}
			})
		}
	}
}

func TestExecutionBrowseExactIDWithoutIndex(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	storeViewerExecution(t, client, StoredExecution{RequestID: "unindexed", OriginalRequest: "retained directly"})
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=unindexed&limit=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 || page.Summaries[0].RequestID != "unindexed" || page.Scanned != 1 || page.HasMore || page.NextCursor != "" {
		t.Fatalf("unindexed exact hit lost: %+v", page)
	}
}

func TestExecutionBrowseExactIDKeepsScanBudget(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	seedBrowseExecution(t, client, "root", 0, "parent", "")
	seedBrowseExecution(t, client, "child", -1, "child", "")
	storeViewerExecution(t, client, StoredExecution{RequestID: "child", OriginalRequestID: "root"})
	for i := 1; i <= viewerBrowseScanLimit+5; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("unrelated-%d", i), float64(i), "unrelated", "")
	}
	first, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Summaries) != 1 || first.Summaries[0].RequestID != "root" || first.Scanned != viewerBrowseScanLimit || !first.HasMore || !first.Partial {
		t.Fatalf("exact lookup bypassed bounded family search: %+v", first)
	}
	second, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root&cursor="+url.QueryEscape(first.NextCursor), ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Summaries) != 1 || second.Summaries[0].RequestID != "child" || second.HasMore {
		t.Fatalf("older family member lost or exact hit duplicated: %+v", second)
	}
}

func TestExecutionBrowseExactIDCountsDecompressedBytes(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for i, id := range []string{"root", "child-a", "child-b"} {
		record, err := json.Marshal(StoredExecution{RequestID: id, OriginalRequestID: "root", Metadata: map[string]string{"padding": strings.Repeat("x", viewerBrowseRequestBytes/3)}})
		if err != nil {
			t.Fatal(err)
		}
		var compressed bytes.Buffer
		compressed.WriteByte(1)
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(record); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := client.Set(t.Context(), viewerExecutionKeys.Record(id), compressed.Bytes(), time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: float64(i), Member: id}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	first, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Summaries) != 2 || !first.HasMore || !first.Partial || !slices.Contains(first.Reasons, "browse_byte_budget") {
		t.Fatalf("exact decode did not count against request budget: %+v", first)
	}
	second, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root&cursor="+url.QueryEscape(first.NextCursor), ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Summaries) != 1 || second.Summaries[0].RequestID != "child-a" || second.HasMore {
		t.Fatalf("deferred family member lost: %+v", second)
	}
}

func TestExecutionBrowseExactByteLimitBeforeFirstIndexAnchor(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for _, item := range []struct {
		id   string
		size int
	}{{"root", viewerBrowseRecordBytes + 10}, {"child", viewerBrowseRecordBytes}} {
		prefix := fmt.Sprintf(`{"request_id":%q,"original_request_id":"root","metadata":{"padding":"`, item.id)
		const suffix = `"}}`
		record := prefix + strings.Repeat("x", item.size-len(prefix)-len(suffix)) + suffix
		var compressed bytes.Buffer
		compressed.WriteByte(1)
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := client.Set(t.Context(), viewerExecutionKeys.Record(item.id), compressed.Bytes(), time.Hour).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: 1, Member: "child"}).Err(); err != nil {
		t.Fatal(err)
	}
	first, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Summaries) != 0 || !first.HasMore || first.NextCursor == "" || !slices.Contains(first.Reasons, "browse_byte_budget") {
		t.Fatalf("byte budget before first index anchor lost continuation: %+v", first)
	}
	second, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root&cursor="+url.QueryEscape(first.NextCursor), ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Summaries) != 1 || second.Summaries[0].RequestID != "child" || second.HasMore {
		t.Fatalf("deferred first indexed record lost: %+v", second)
	}
}

func TestExecutionBrowseInvalidExactHitDoesNotHideFamily(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	if err := client.Set(t.Context(), viewerExecutionKeys.Record("root"), "not JSON", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	seedBrowseExecution(t, client, "child", 1, "child", "")
	storeViewerExecution(t, client, StoredExecution{RequestID: "child", OriginalRequestID: "root"})
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=root", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 || page.Summaries[0].RequestID != "child" || !page.Partial || !slices.Contains(page.Reasons, "record_unreadable") {
		t.Fatalf("unreadable exact hit hid valid family: %+v", page)
	}
}

func TestExecutionBrowseConversationReadsOnlyFiveTurns(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for i := 0; i < 12; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("turn-%02d", i), float64(i), "user request", "conversation-one")
	}
	for i := 0; i < 25; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("unrelated-%02d", i), float64(i), "unrelated", "conversation-other")
	}
	observer := &browseCommandObserver{}
	client.AddHook(observer)
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerConversationIndexKey("conversation-one"), browseQueryForTest(t, "limit=100", "conversation-one"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 5 || page.Scanned != 5 || observer.bodies.Load() != 5 || !page.HasMore {
		t.Fatalf("conversation hydrated outside requested five-turn page: %+v, bodies=%d", page, observer.bodies.Load())
	}
}

func TestExecutionBrowseBoundedDecompressionAndCancellation(t *testing.T) {
	var compressed bytes.Buffer
	compressed.WriteByte(1)
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_, size, err := deserializeBrowseExecution(compressed.Bytes(), 64)
	if !errors.Is(err, errBrowseRecordTooLarge) || size != 65 {
		t.Fatalf("decompression was not bounded: size=%d err=%v", size, err)
	}
	_, client := newConversationAPIRedis(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = loadExecutionBrowsePage(ctx, client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "", ""))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestExecutionBrowseQueryLimitsAndCursorBinding(t *testing.T) {
	if q := browseQueryForTest(t, "", ""); q.Limit != 30 {
		t.Fatalf("default limit=%d", q.Limit)
	}
	if q := browseQueryForTest(t, "limit=999", ""); q.Limit != 100 {
		t.Fatalf("max limit=%d", q.Limit)
	}
	for _, raw := range []string{"limit=-1", "status=unknown", "sort=total_duration_ms", "from=bad", "from=2026-11-07T00%3A00%3A00Z&to=2026-11-06T00%3A00%3A00Z"} {
		if _, err := parseExecutionBrowseQuery(httptest.NewRequest("GET", "/api/executions?"+raw, nil), ""); err == nil {
			t.Errorf("accepted invalid query %s", raw)
		}
	}
	_, client := newConversationAPIRedis(t)
	for i := 0; i < 3; i++ {
		seedBrowseExecution(t, client, fmt.Sprintf("request-%d", i), 100, "query", "")
	}
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "limit=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	_, err = parseExecutionBrowseQuery(httptest.NewRequest("GET", "/api/executions?agent=another-agent&cursor="+url.QueryEscape(page.NextCursor), nil), "")
	if err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("cursor accepted unrelated filters: %v", err)
	}
}

func TestExecutionBrowseExactHitCursorValidation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		query        string
		conversation string
		emitted      bool
		score        string
		valid        bool
	}{
		{name: "exact hit before traversal", query: "q=root", emitted: true, valid: true},
		{name: "ordinary query", emitted: true},
		{name: "conversation", query: "q=root", conversation: "conversation-one", emitted: true},
		{name: "exact lookup exhausted byte budget", query: "q=root", valid: true},
		{name: "score without anchor", query: "q=root", emitted: true, score: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := browseQueryForTest(t, tc.query, tc.conversation)
			data, err := json.Marshal(executionBrowseCursor{Version: 2, Fingerprint: q.Fingerprint, ExactEmitted: tc.emitted, Score: tc.score})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/api/executions?"+tc.query+"&cursor="+base64.RawURLEncoding.EncodeToString(data), nil)
			if _, err := parseExecutionBrowseQuery(r, tc.conversation); (err == nil) != tc.valid {
				t.Fatalf("cursor validity=%v want=%v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestExecutionBrowseChangedCursorRequiresRefresh(t *testing.T) {
	for _, change := range []string{"missing", "moved"} {
		t.Run(change, func(t *testing.T) {
			_, client := newConversationAPIRedis(t)
			for i := 0; i < 3; i++ {
				seedBrowseExecution(t, client, fmt.Sprintf("request-%d", i), 100, "query", "")
			}
			first, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "limit=1", ""))
			if err != nil {
				t.Fatal(err)
			}
			q := browseQueryForTest(t, "limit=1&cursor="+url.QueryEscape(first.NextCursor), "")
			if change == "missing" {
				err = client.ZRem(t.Context(), viewerExecutionKeys.RecentIndex(), q.Cursor.Member).Err()
			} else {
				err = client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: 101, Member: q.Cursor.Member}).Err()
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), q)
			if !errors.Is(err, errBrowseCursorStale) {
				t.Fatalf("changed anchor silently skipped data: %v", err)
			}
			response := httptest.NewRecorder()
			writeBrowseError(response, err, "unavailable")
			if response.Code != 409 || !strings.Contains(response.Body.String(), "refresh") {
				t.Fatalf("missing explicit refresh response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestExecutionBrowseSearchesFullRequestBeforeDisplayPreview(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	request := strings.Repeat("é", 1000) + " needle-after-preview"
	seedBrowseExecution(t, client, "request-with-long-text", 100, request, "")
	metadataMarker := strings.Repeat("large-application-metadata", 100)
	storeViewerExecution(t, client, StoredExecution{RequestID: "request-with-long-text", OriginalRequest: request, CreatedAt: time.Unix(1700000000, 0), Metadata: map[string]string{"application_data": metadataMarker}})
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "q=needle-after-preview", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 || !page.Summaries[0].RequestTextTruncated || strings.Contains(page.Summaries[0].OriginalRequest, "needle-after-preview") || page.Summaries[0].OriginalRequest != executionOverviewPreview(request) {
		t.Fatalf("fulltext matching or marked preview failed: %+v", page)
	}
	if len(page.Summaries[0].Metadata) != 0 {
		t.Fatal("browse retained arbitrary application metadata")
	}
	raw, err := client.Get(t.Context(), viewerExecutionKeys.Record("request-with-long-text")).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	execution, err := deserializeExecution(raw)
	if err != nil || execution.OriginalRequest != request || execution.Metadata["application_data"] != metadataMarker {
		t.Fatalf("browse changed raw evidence: %v", err)
	}
}

func TestExecutionBrowseLifecycleFilterKeepsUnknownSeparate(t *testing.T) {
	for _, success := range []bool{false, true} {
		summary := ExecutionSummary{Success: success}
		for _, status := range []string{"all", "success", "failed", "running", "cancelled"} {
			if got := executionMatchesBrowse(summary, browseQueryForTest(t, "status="+status, "")); got != (status == "all") {
				t.Errorf("unknown lifecycle aggregateSuccess=%v matched %s=%v", success, status, got)
			}
		}
	}
	summary := ExecutionSummary{Success: false, FailedSteps: 1, Lifecycle: &orchestration.ExecutionLifecycleObservation{State: orchestration.RequestLifecycleCompleted}}
	if !executionMatchesBrowse(summary, browseQueryForTest(t, "status=success", "")) || executionMatchesBrowse(summary, browseQueryForTest(t, "status=failed", "")) {
		t.Fatal("aggregate failed step replaced completed request lifecycle")
	}
}

func TestExecutionBrowsePreservesOnlyBoundedResumeIdentity(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	seedBrowseExecution(t, client, "resume-request", 100, "Resume the approved work", "")
	opaque := strings.Repeat("application-only-", 1000)
	storeViewerExecution(t, client, StoredExecution{RequestID: "resume-request", OriginalRequestID: "original-request", OriginalRequest: "Resume the approved work", CreatedAt: time.Unix(1700000000, 0), Metadata: map[string]string{"resume_checkpoint_id": "cp-reviewed-resume", "application_data": opaque}})
	page, err := loadExecutionBrowsePage(t.Context(), client, viewerExecutionKeys.RecentIndex(), browseQueryForTest(t, "limit=1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Summaries) != 1 {
		t.Fatalf("missing resume: %+v", page)
	}
	summary := page.Summaries[0]
	if len(summary.Metadata) != 1 || summary.Metadata["resume_checkpoint_id"] != "cp-reviewed-resume" || summary.HITLCheckpointID != "cp-reviewed-resume" || summary.OriginalRequestID != "original-request" {
		t.Fatalf("resume identity lost or extra metadata retained: %+v", summary)
	}
	raw, err := client.Get(t.Context(), viewerExecutionKeys.Record("resume-request")).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	record, err := deserializeExecution(raw)
	if err != nil {
		t.Fatal(err)
	}
	summary.Metadata["resume_checkpoint_id"] = "changed-display-copy"
	if record.Metadata["resume_checkpoint_id"] != "cp-reviewed-resume" || record.Metadata["application_data"] != opaque {
		t.Fatal("summary projection modified authoritative metadata")
	}
	for _, value := range []string{"", strings.Repeat("c", viewerBrowseResumeIDMaxBytes+1)} {
		projected := projectBrowseSummary(ExecutionSummary{Metadata: map[string]string{"resume_checkpoint_id": value, "application_data": opaque}})
		if len(projected.Metadata) != 0 {
			t.Fatal("unbounded or absent resume marker retained")
		}
	}
}
