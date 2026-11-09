package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// Every command issued by the lineage reader must belong to this read-only
// allowlist. This catches writes even when a missing/corrupt fixture looks fine
// in the resulting response.
type lineageReadOnlyHook struct {
	mu               sync.Mutex
	commands         []string
	getCounts        map[string]int
	violations       []string
	failIndexes      bool
	onIndex          func()
	getPipelines     int
	maxGetPipeline   int
	failGetPipeline  int
	afterGetPipeline func()
}

func (h *lineageReadOnlyHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *lineageReadOnlyHook) record(command redis.Cmder) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	name := command.Name()
	h.commands = append(h.commands, name)
	switch name {
	case "get", "zrange", "zrevrange", "zscore", "hello", "client", "ping":
	default:
		h.violations = append(h.violations, name)
		return fmt.Errorf("lineage attempted forbidden command %s", name)
	}
	if name == "get" {
		if h.getCounts == nil {
			h.getCounts = make(map[string]int)
		}
		h.getCounts[fmt.Sprint(command.Args()[1])]++
	}
	if name == "zrange" || name == "zrevrange" {
		if h.onIndex != nil {
			h.onIndex()
		}
		if h.failIndexes {
			return errors.New("backend unavailable with internal connection details")
		}
	}
	return nil
}

func (h *lineageReadOnlyHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if err := h.record(command); err != nil {
			return err
		}
		return next(ctx, command)
	}
}

func (h *lineageReadOnlyHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		gets := 0
		for _, command := range commands {
			if command.Name() == "get" {
				gets++
			}
			if err := h.record(command); err != nil {
				return err
			}
		}
		if gets > 0 {
			h.getPipelines++
			h.maxGetPipeline = max(h.maxGetPipeline, gets)
			if h.failGetPipeline == h.getPipelines {
				return errors.New("candidate response unavailable")
			}
		}
		err := next(ctx, commands)
		if gets > 0 && err == nil && h.afterGetPipeline != nil {
			h.afterGetPipeline()
		}
		return err
	}
}

func (h *lineageReadOnlyHook) assertReadOnly(t *testing.T) {
	t.Helper()
	if len(h.violations) > 0 {
		t.Fatalf("write commands attempted: %v", h.violations)
	}
	for key := range h.getCounts {
		if !strings.Contains(key, ":execution-debug:") || !strings.HasSuffix(key, ":record") {
			t.Fatalf("reader consulted non-execution record %q", key)
		}
	}
}

func indexLineageRecords(t *testing.T, client *redis.Client, records []StoredExecution) {
	t.Helper()
	for index, record := range records {
		storeViewerExecution(t, client, record)
		if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Score: float64(index), Member: record.RequestID}).Err(); err != nil {
			t.Fatal(err)
		}
		if conversation := record.Metadata[orchestration.MetadataConversationID]; conversation != "" {
			if err := client.ZAdd(t.Context(), viewerConversationIndexKey(conversation), redis.Z{Score: float64(index), Member: record.RequestID}).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func lineageIDs(response *ExecutionLineageResponse) []string {
	ids := make([]string, 0, len(response.Executions))
	for _, summary := range response.Executions {
		ids = append(ids, summary.RequestID)
	}
	sort.Strings(ids)
	return ids
}

func TestExecutionLineageFindsConcurrentGrandchildrenWithoutParentResults(t *testing.T) {
	for _, topology := range []string{"four", "six-failed-root"} {
		t.Run(topology, func(t *testing.T) {
			_, client := newConversationAPIRedis(t)
			metadata := map[string]string{orchestration.MetadataConversationID: "conversation-main"}
			records := []StoredExecution{
				{RequestID: "root", OriginalRequestID: "root", Metadata: metadata},
				{RequestID: "translation", OriginalRequestID: "root", ParentRequestID: "root", ParentStepID: "step-1", Metadata: metadata},
				{RequestID: "review-a", OriginalRequestID: "root", ParentRequestID: "translation", ParentStepID: "step-17", Metadata: metadata},
				{RequestID: "review-b", OriginalRequestID: "root", ParentRequestID: "translation", ParentStepID: "step-18", Metadata: metadata},
			}
			if topology == "six-failed-root" {
				records[0].Lifecycle = &orchestration.ExecutionLifecycleObservation{State: orchestration.RequestLifecycleFailed}
				records[0].Result = &ExecutionResult{Success: false, Steps: []StepResult{}}
				records = append(records,
					StoredExecution{RequestID: "translation-b", OriginalRequestID: "root", ParentRequestID: "root", ParentStepID: "step-2"},
					StoredExecution{RequestID: "translation-c", OriginalRequestID: "root", ParentRequestID: "root", ParentStepID: "step-3"})
			}
			want := make([]string, 0, len(records))
			for _, record := range records {
				want = append(want, record.RequestID)
			}
			sort.Strings(want)
			records = append(records, StoredExecution{RequestID: "unrelated-turn", OriginalRequestID: "unrelated-turn", Metadata: metadata, TraceID: "shared-trace"})
			indexLineageRecords(t, client, records)
			hook := &lineageReadOnlyHook{}
			client.AddHook(hook)
			response, err := loadExecutionLineageWithClient(t.Context(), client, "review-a")
			if err != nil || response.Partial || response.IndexIncomplete {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if got := lineageIDs(response); !reflect.DeepEqual(got, want) {
				t.Fatalf("family=%v want=%v", got, want)
			}
			for _, lookup := range response.ParentLookups {
				if lookup.Status != "available" {
					t.Fatalf("available ancestor classified as %+v", lookup)
				}
			}
			for key, reads := range hook.getCounts {
				if reads != 1 {
					t.Fatalf("record %q read %d times", key, reads)
				}
			}
			hook.assertReadOnly(t)
		})
	}
}

func TestExecutionLineagePreservesCrossConversationNavigationAndLegacyRoot(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	indexLineageRecords(t, client, []StoredExecution{
		{RequestID: "root", OriginalRequestID: "root", Metadata: map[string]string{orchestration.MetadataConversationID: "other"}},
		{RequestID: "child", ParentRequestID: "root", ParentStepID: "step-1", Metadata: map[string]string{orchestration.MetadataConversationID: "current"}},
		{RequestID: "legacy", OriginalRequestID: "root"},
		{RequestID: "grandchild", ParentRequestID: "child", ParentStepID: "step-1"},
	})
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "child")
	if err != nil || !reflect.DeepEqual(lineageIDs(response), []string{"child", "grandchild", "legacy", "root"}) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, summary := range response.Executions {
		if summary.RequestID == "root" && summary.ConversationID != "other" {
			t.Fatal("parent conversation changed")
		}
		if summary.RequestID == "grandchild" && summary.ConversationID != "" {
			t.Fatal("stateless descendant gained conversation membership")
		}
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageMissingCorruptAndStaleAreReadOnly(t *testing.T) {
	for _, state := range []string{"missing", "corrupt", "mismatched-id"} {
		t.Run(state, func(t *testing.T) {
			server, client := newConversationAPIRedis(t)
			indexLineageRecords(t, client, []StoredExecution{{RequestID: "child", ParentRequestID: "parent"}})
			switch state {
			case "corrupt":
				server.Set(viewerExecutionKeys.Record("parent"), "{broken")
			case "mismatched-id":
				server.Set(viewerExecutionKeys.Record("parent"), `{"request_id":"another-record"}`)
			}
			if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Member: "parent", Score: 3}).Err(); err != nil {
				t.Fatal(err)
			}
			hook := &lineageReadOnlyHook{}
			client.AddHook(hook)
			response, err := loadExecutionLineageWithClient(t.Context(), client, "child")
			if err != nil || len(response.ParentLookups) != 1 || !response.IndexIncomplete {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			wantStatus := "unknown"
			if state == "missing" {
				wantStatus = "unavailable"
			}
			if response.ParentLookups[0].Status != wantStatus {
				t.Fatalf("lookup=%+v want=%s", response.ParentLookups[0], wantStatus)
			}
			if state != "missing" && !response.Partial {
				t.Fatal("corrupt record reported complete")
			}
			hook.assertReadOnly(t)
		})
	}
}

func TestExecutionLineagePreservesSelectedAndAncestorsOnBackendFailureOrCancellation(t *testing.T) {
	for _, mode := range []string{"backend-error", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			_, client := newConversationAPIRedis(t)
			indexLineageRecords(t, client, []StoredExecution{{RequestID: "child", ParentRequestID: "parent"}, {RequestID: "parent"}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hook := &lineageReadOnlyHook{failIndexes: true}
			if mode == "cancelled" {
				hook.onIndex = cancel
			}
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(t.Context(), time.Now().Add(20*time.Millisecond))
				defer deadlineCancel()
				hook.onIndex = func() { <-ctx.Done() }
			}
			client.AddHook(hook)
			response, err := loadExecutionLineageWithClient(ctx, client, "child")
			if err != nil || !response.Partial || !reflect.DeepEqual(lineageIDs(response), []string{"child", "parent"}) {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			want := "backend_read"
			switch mode {
			case "cancelled":
				want = "cancelled"
			case "deadline":
				want = "lookup_deadline"
			}
			if !containsString(response.Reasons, want) {
				t.Fatalf("reasons=%v want=%q", response.Reasons, want)
			}
			hook.assertReadOnly(t)
		})
	}
}

func TestExecutionLineageSharesExactBudgetAcrossAncestorDepths(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	for index := 0; index <= lineageExactOwnerReadLimit; index++ {
		parent := ""
		if index < lineageExactOwnerReadLimit {
			parent = fmt.Sprintf("node-%04d", index+1)
		}
		storeViewerExecution(t, client, StoredExecution{RequestID: fmt.Sprintf("node-%04d", index), ParentRequestID: parent})
	}
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "node-0000")
	if err != nil || !response.Partial || !containsString(response.Reasons, "exact_hydration_limit") {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if len(response.Executions) != lineageExactOwnerReadLimit || len(hook.getCounts) != lineageExactOwnerReadLimit {
		t.Fatalf("executions=%d reads=%d", len(response.Executions), len(hook.getCounts))
	}
	limitedParent := fmt.Sprintf("node-%04d", lineageExactOwnerReadLimit)
	foundLimit := false
	for _, lookup := range response.ParentLookups {
		if lookup.ParentRequestID == limitedParent {
			foundLimit = true
			if lookup.Status != "unknown" || lookup.Reason != "exact_hydration_limit" {
				t.Fatalf("limited lookup=%+v", lookup)
			}
		}
	}
	if !foundLimit {
		t.Fatal("ancestor limit was not disclosed in the parent lookup")
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageTruncatedRecentAndConversationPages(t *testing.T) {
	server, client := newConversationAPIRedis(t)
	indexLineageRecords(t, client, []StoredExecution{{RequestID: "selected", Metadata: map[string]string{orchestration.MetadataConversationID: "current"}}})
	for index := 0; index <= lineageCandidateReadLimit; index++ {
		id := fmt.Sprintf("unrelated-%05d", index)
		server.Set(viewerExecutionKeys.Record(id), fmt.Sprintf(`{"request_id":%q}`, id))
		if err := client.ZAdd(t.Context(), viewerExecutionKeys.RecentIndex(), redis.Z{Member: id, Score: float64(index + 1)}).Err(); err != nil {
			t.Fatal(err)
		}
		if index <= lineageCandidateReadLimit {
			if err := client.ZAdd(t.Context(), viewerConversationIndexKey("current"), redis.Z{Member: id, Score: float64(index + 1)}).Err(); err != nil {
				t.Fatal(err)
			}
		}
	}
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "selected")
	if err != nil || !response.Partial || !response.IndexIncomplete || !reflect.DeepEqual(lineageIDs(response), []string{"selected"}) {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	for _, reason := range []string{"recent_index_limit", "conversation_index_limit"} {
		if !containsString(response.Reasons, reason) {
			t.Fatalf("reasons=%v missing=%s", response.Reasons, reason)
		}
	}
	if len(hook.getCounts) > lineageCandidateReadLimit*2+1 {
		t.Fatalf("ordinary hydration budget exceeded: %d", len(hook.getCounts))
	}
	if hook.maxGetPipeline > lineageHydrationBatchSize {
		t.Fatalf("candidate response batching exceeded: %d", hook.maxGetPipeline)
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageProjectionKeepsCompleteSummary(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Second)
	candidate := StoredExecution{
		RequestID: "candidate", OriginalRequestID: "root", ParentRequestID: "root", ParentStepID: "step-17",
		TraceID: "shared", AgentName: "reviewer", OriginalRequest: "review exact 中文", RecordRevision: 8,
		Lifecycle: &orchestration.ExecutionLifecycleObservation{State: orchestration.RequestLifecycleCompleted},
		CreatedAt: end, Metadata: map[string]string{orchestration.MetadataConversationID: "other", "exact": "a<b"},
		Checkpoint: &HITLCheckpoint{CheckpointID: "checkpoint-1"}, Interrupted: true,
		Result: &ExecutionResult{Success: false, TotalDuration: int64(3 * time.Second), Steps: []StepResult{
			{StepID: "step-1", StartTime: &start, EndTime: &end, Success: true, ResponseText: strings.Repeat("payload", 10000)},
			{StepID: "step-2", Success: false}, {StepID: "step-3", Skipped: true},
		}},
	}
	indexLineageRecords(t, client, []StoredExecution{{RequestID: "root", OriginalRequestID: "root"}, candidate})
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "root")
	if err != nil {
		t.Fatal(err)
	}
	var got ExecutionSummary
	for _, summary := range response.Executions {
		if summary.RequestID == candidate.RequestID {
			got = summary
		}
	}
	want := summarizeExecution(&candidate)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected summary=%+v want=%+v", got, want)
	}
	data, err := json.Marshal(response)
	if err != nil || strings.Contains(string(data), "payloadpayload") {
		t.Fatalf("lineage leaked full tool payload: err=%v", err)
	}
	for key, count := range hook.getCounts {
		if count != 1 {
			t.Fatalf("reread projected record %s: %d", key, count)
		}
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageRetainsReadFamilyAfterLaterBatchFailure(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	records := []StoredExecution{{RequestID: "root", OriginalRequestID: "root"}}
	for i := 0; i < lineageHydrationBatchSize*2; i++ {
		records = append(records, StoredExecution{RequestID: fmt.Sprintf("child-%03d", i), ParentRequestID: "root"})
	}
	indexLineageRecords(t, client, records)
	hook := &lineageReadOnlyHook{failGetPipeline: 3}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "root")
	if err != nil || !response.Partial || !containsString(response.Reasons, "backend_read") {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if len(response.Executions) != lineageHydrationBatchSize+1 {
		t.Fatalf("available family lost: %d", len(response.Executions))
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageDoesNotRetryFailedAncestorThroughCandidateBudget(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	indexLineageRecords(t, client, []StoredExecution{{RequestID: "child", ParentRequestID: "parent"}, {RequestID: "parent"}})
	hook := &lineageReadOnlyHook{failGetPipeline: 2}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, "child")
	if err != nil || !response.Partial || len(response.ParentLookups) != 1 || response.ParentLookups[0].Status != "unknown" || response.ParentLookups[0].Reason != "backend_read" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if count := hook.getCounts[viewerExecutionKeys.Record("parent")]; count != 1 {
		t.Fatalf("failed ancestor retried through candidate budget: reads=%d", count)
	}
	hook.assertReadOnly(t)
}

func TestExecutionReadCacheChargesDispatchedReadsWhenCancelledBeforeDecode(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	indexLineageRecords(t, client, []StoredExecution{{RequestID: "first"}, {RequestID: "second"}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hook := &lineageReadOnlyHook{afterGetPipeline: cancel}
	client.AddHook(hook)
	cache := newExecutionReadCache()
	if _, err := cache.load(ctx, client, []string{"first", "second"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if cache.hydrated != 2 || len(cache.failedReads) != 2 {
		t.Fatalf("attempt accounting: hydrated=%d failed=%d", cache.hydrated, len(cache.failedReads))
	}
	if _, err := cache.loadExactOwners(t.Context(), client, []string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	if cache.exactHydrated != 0 || hook.getPipelines != 1 {
		t.Fatalf("cancelled reads repeated through exact budget: exact=%d pipelines=%d", cache.exactHydrated, hook.getPipelines)
	}
	hook.assertReadOnly(t)
}

func TestExecutionLineageHTTPAndAdditiveProjections(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	execution := StoredExecution{RequestID: "child", ParentRequestID: "parent", ParentStepID: "step-17", Checkpoint: &HITLCheckpoint{ParentRequestID: "parent", ParentStepID: "step-17"}}
	storeViewerExecution(t, client, execution)
	previousClient, previousMock := viewerRedisClient, useMock
	viewerRedisClient, useMock = client, false
	t.Cleanup(func() { viewerRedisClient, useMock = previousClient, previousMock })
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	for _, tc := range []struct {
		method, id string
		status     int
	}{
		{http.MethodGet, "child", http.StatusOK},
		{http.MethodGet, "absent", http.StatusNotFound},
		{http.MethodPost, "child", http.StatusMethodNotAllowed},
	} {
		recorder := httptest.NewRecorder()
		handleExecution(recorder, httptest.NewRequest(tc.method, "/api/executions/"+tc.id+"/lineage", nil))
		if recorder.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.id, recorder.Code, recorder.Body.String())
		}
	}
	for _, value := range []interface{}{summarizeExecution(&execution), execution, execution.Checkpoint} {
		data, err := json.Marshal(value)
		if err != nil || !strings.Contains(string(data), `"parent_request_id":"parent"`) || !strings.Contains(string(data), `"parent_step_id":"step-17"`) {
			t.Fatalf("projection lost identity: %s err=%v", data, err)
		}
	}
	hook.assertReadOnly(t)
	// Unified details keep their existing payload, with only the additive pair.
	useMock = true
	unified := buildUnifiedViewWithContext(t.Context(), &execution)
	if unified.ParentRequestID != "parent" || unified.ParentStepID != "step-17" || unified.Checkpoint != execution.Checkpoint {
		t.Fatalf("unified projection lost identity/checkpoint: %+v", unified)
	}
}

func TestExecutionLineageSelectedReadFailuresAreErrors(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			server, client := newConversationAPIRedis(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "corrupt":
				server.Set(viewerExecutionKeys.Record("selected"), "not-json")
			case "cancelled":
				cancel()
			}
			hook := &lineageReadOnlyHook{}
			client.AddHook(hook)
			response, err := loadExecutionLineageWithClient(ctx, client, "selected")
			if response != nil || err == nil {
				t.Fatalf("selected failure returned successful family: response=%+v err=%v", response, err)
			}
			if mode == "missing" && !errors.Is(err, errLineageExecutionNotFound) {
				t.Fatalf("missing error=%v", err)
			}
			hook.assertReadOnly(t)
		})
	}
}

func TestExecutionLineageReportsMissingReverseMembershipWithoutRepair(t *testing.T) {
	_, client := newConversationAPIRedis(t)
	record := StoredExecution{RequestID: "selected", Metadata: map[string]string{orchestration.MetadataConversationID: "current"}}
	storeViewerExecution(t, client, record)
	hook := &lineageReadOnlyHook{}
	client.AddHook(hook)
	response, err := loadExecutionLineageWithClient(t.Context(), client, record.RequestID)
	if err != nil || !response.IndexIncomplete || response.Partial {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	hook.assertReadOnly(t)
}

func TestConnectedExecutionSummariesHandlesCyclesAndMissingRoots(t *testing.T) {
	all := map[string]ExecutionSummary{
		"a":         {RequestID: "a", ParentRequestID: "b", OriginalRequestID: "expired-root"},
		"b":         {RequestID: "b", ParentRequestID: "a"},
		"c":         {RequestID: "c", OriginalRequestID: "expired-root"},
		"unrelated": {RequestID: "unrelated"},
	}
	family := connectedExecutionSummaries(t.Context(), "a", all)
	if len(family) != 3 {
		t.Fatalf("family=%v", family)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func BenchmarkExecutionLineageBusyHistory(b *testing.B) {
	server, err := miniredis.Run()
	if err != nil {
		b.Fatal(err)
	}
	defer server.Close()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	keyspace, err := core.NewRedisKeyspace("lineage-benchmark")
	if err != nil {
		b.Fatal(err)
	}
	viewerExecutionKeys = orchestration.NewRedisExecutionDebugKeys(keyspace)
	ctx := context.Background()
	payload := strings.Repeat("synthetic payload ", 1024)
	var decodedBytes int64
	for index := 0; index < viewerGlobalExecutionScanLimit; index++ {
		id := fmt.Sprintf("request-%05d", index)
		execution := StoredExecution{RequestID: id, Result: &ExecutionResult{Steps: []StepResult{{ResponseText: payload}}}}
		if index == 1 {
			execution.ParentRequestID = "request-00000"
		}
		data, marshalErr := json.Marshal(execution)
		if marshalErr != nil {
			b.Fatal(marshalErr)
		}
		decodedBytes += int64(len(data))
		server.Set(viewerExecutionKeys.Record(id), string(data))
		if err := client.ZAdd(ctx, viewerExecutionKeys.RecentIndex(), redis.Z{Member: id, Score: float64(index)}).Err(); err != nil {
			b.Fatal(err)
		}
	}
	for _, mode := range []string{"existing-hydration", "lineage", "lineage-two-views"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(decodedBytes)
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			peak := before.HeapAlloc
			done, stopped := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(stopped)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						var current runtime.MemStats
						runtime.ReadMemStats(&current)
						if current.HeapAlloc > peak {
							peak = current.HeapAlloc
						}
					case <-done:
						return
					}
				}
			}()
			var responseBytes int
			b.ResetTimer()
			for range b.N {
				switch mode {
				case "lineage-two-views":
					errs := make(chan error, 2)
					for range 2 {
						go func() {
							_, err := loadExecutionLineageWithClient(ctx, client, "request-00001")
							errs <- err
						}()
					}
					for range 2 {
						if err := <-errs; err != nil {
							b.Fatal(err)
						}
					}
				case "lineage":
					response, err := loadExecutionLineageWithClient(ctx, client, "request-00001")
					if err != nil || len(response.Executions) != 2 {
						b.Fatalf("lineage err=%v", err)
					}
					data, err := json.Marshal(response)
					if err != nil {
						b.Fatal(err)
					}
					responseBytes = len(data)
				default:
					ids, err := client.ZRangeArgs(ctx, redis.ZRangeArgs{Key: viewerExecutionKeys.RecentIndex(), Start: 0, Stop: viewerGlobalExecutionScanLimit - 1, Rev: true}).Result()
					if err != nil {
						b.Fatal(err)
					}
					cache := newExecutionReadCache()
					if _, err := cache.load(ctx, client, ids); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.StopTimer()
			close(done)
			<-stopped
			b.ReportMetric(float64(peak), "peak-heap-B")
			b.ReportMetric(float64(before.HeapAlloc), "fixture-heap-B")
			b.ReportMetric(float64(peak-before.HeapAlloc), "additional-peak-heap-B")
			views := 1
			switch mode {
			case "lineage-two-views":
				views = 2
			}
			b.ReportMetric(float64(decodedBytes)*float64(views), "decoded-B/op")
			b.ReportMetric(float64(viewerGlobalExecutionScanLimit*views), "record-reads/op")
			if responseBytes > 0 {
				b.ReportMetric(float64(responseBytes), "response-B/op")
			}
		})
	}
}
