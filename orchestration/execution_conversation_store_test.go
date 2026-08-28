package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

func newRedisExecutionConversationTestStore(
	t testing.TB,
	config ExecutionStoreConfig,
) (*miniredis.Miniredis, *RedisExecutionDebugStore) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	config = normalizeExecutionStoreConfig(config)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, &RedisExecutionDebugStore{
		client:         client,
		logger:         &core.NoOpLogger{},
		keyPrefix:      config.KeyPrefix,
		ttl:            config.TTL,
		errorTTL:       config.ErrorTTL,
		queryLimit:     config.ConversationQueryLimit,
		indexScanLimit: config.ConversationIndexScanLimit,
	}
}

var benchmarkConversationSummariesSink []ExecutionSummary

func executionWithConversation(
	requestID string,
	conversationID string,
	createdAt time.Time,
) *StoredExecution {
	execution := sampleExecution(requestID, true)
	execution.CreatedAt = createdAt
	execution.Metadata = map[string]string{
		MetadataConversationID: conversationID,
		"investigation":        "preserved",
	}
	return execution
}

func TestFilterUnseenConversationMembersDeduplicatesOverlappingScanBatches(
	t *testing.T,
) {
	seen := make(map[string]struct{})
	first := filterUnseenConversationMembers(
		[]string{"turn-4", "turn-3", "turn-2"},
		seen,
	)
	second := filterUnseenConversationMembers(
		[]string{"turn-2", "turn-1"},
		seen,
	)

	if got := strings.Join(first, ","); got != "turn-4,turn-3,turn-2" {
		t.Fatalf("first batch = %q", got)
	}
	if got := strings.Join(second, ","); got != "turn-1" {
		t.Fatalf("overlapping second batch = %q, want only unseen member", got)
	}
	if len(seen) != 4 {
		t.Fatalf("seen member count = %d, want 4", len(seen))
	}
}

func TestExecutionStoresSanitizeInvalidConversationWithoutMutatingCaller(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) (ExecutionStore, func() int)
	}{
		{
			name: "provider",
			store: func(*testing.T) (ExecutionStore, func() int) {
				provider := newMockStorageProvider()
				store := NewExecutionStoreWithProvider(
					provider,
					DefaultExecutionStoreConfig(),
					nil,
				)
				return store, func() int {
					count := 0
					for key := range provider.indexes {
						if strings.Contains(key, ":conversation:") {
							count++
						}
					}
					return count
				}
			},
		},
		{
			name: "direct redis",
			store: func(t *testing.T) (ExecutionStore, func() int) {
				mr, store := newRedisExecutionConversationTestStore(
					t,
					DefaultExecutionStoreConfig(),
				)
				return store, func() int {
					count := 0
					for _, key := range mr.Keys() {
						if strings.Contains(key, ":conversation:") {
							count++
						}
					}
					return count
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, conversationIndexCount := test.store(t)
			execution := executionWithConversation(
				"request-invalid-conversation",
				"invalid conversation",
				time.Now(),
			)
			callerMetadata := execution.Metadata

			if err := store.Store(context.Background(), execution); err != nil {
				t.Fatalf("Store: %v", err)
			}
			if execution.Metadata[MetadataConversationID] != "invalid conversation" {
				t.Fatalf("caller metadata was mutated: %v", execution.Metadata)
			}
			if execution.Metadata != nil &&
				fmt.Sprintf("%p", execution.Metadata) != fmt.Sprintf("%p", callerMetadata) {
				t.Fatal("caller metadata map identity changed")
			}

			stored, err := store.Get(context.Background(), execution.RequestID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if _, present := stored.Metadata[MetadataConversationID]; present {
				t.Fatalf("invalid conversation persisted: %v", stored.Metadata)
			}
			if stored.Metadata["investigation"] != "preserved" {
				t.Fatalf("unrelated metadata lost: %v", stored.Metadata)
			}
			if got := conversationIndexCount(); got != 0 {
				t.Fatalf("conversation index count = %d, want 0", got)
			}
		})
	}
}

func TestRedisExecutionUpdatePreservesStoredConversationIdentity(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(
		t,
		DefaultExecutionStoreConfig(),
	)
	original := executionWithConversation(
		"request-update-identity",
		"conversation-original",
		time.Now(),
	)
	if err := store.Store(context.Background(), original); err != nil {
		t.Fatalf("Store: %v", err)
	}

	update := executionWithConversation(
		"request-update-identity",
		"conversation-replacement",
		time.Now().Add(time.Minute),
	)
	if err := store.Update(context.Background(), update.RequestID, update); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := ExecutionConversationID(update); got != "conversation-replacement" {
		t.Fatalf("caller update was mutated: %q", got)
	}

	stored, err := store.Get(context.Background(), update.RequestID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := ExecutionConversationID(stored); got != "conversation-original" {
		t.Fatalf("stored conversation was rewritten: %q", got)
	}
}

func TestConversationExecutionListerOrderingIsolationAndHierarchy(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) ExecutionStore
	}{
		{
			name: "provider",
			store: func(*testing.T) ExecutionStore {
				return NewExecutionStoreWithProvider(
					newMockStorageProvider(),
					DefaultExecutionStoreConfig(),
					nil,
				)
			},
		},
		{
			name: "direct redis",
			store: func(t *testing.T) ExecutionStore {
				_, store := newRedisExecutionConversationTestStore(
					t,
					DefaultExecutionStoreConfig(),
				)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			lister, ok := store.(ConversationExecutionLister)
			if !ok {
				t.Fatalf("%T does not implement ConversationExecutionLister", store)
			}

			base := time.Now().Add(-time.Hour)
			executions := []*StoredExecution{
				executionWithConversation("turn-1", "conversation-a", base),
				executionWithConversation("turn-other", "conversation-b", base.Add(time.Minute)),
				executionWithConversation("turn-2", "conversation-a", base.Add(2*time.Minute)),
				executionWithConversation("turn-3", "conversation-a", base.Add(3*time.Minute)),
			}
			executions[1].OriginalRequestID = "turn-other"
			executions[2].OriginalRequestID = "turn-1"
			executions[3].OriginalRequestID = "turn-1"
			for _, execution := range executions {
				if err := store.Store(context.Background(), execution); err != nil {
					t.Fatalf("Store(%s): %v", execution.RequestID, err)
				}
			}

			summaries, err := lister.ListByConversationID(
				context.Background(),
				"conversation-a",
				10,
			)
			if err != nil {
				t.Fatalf("ListByConversationID: %v", err)
			}
			want := []string{"turn-1", "turn-2", "turn-3"}
			if len(summaries) != len(want) {
				t.Fatalf("summary count = %d, want %d", len(summaries), len(want))
			}
			for i, summary := range summaries {
				if summary.RequestID != want[i] {
					t.Fatalf("summary[%d] = %q, want %q", i, summary.RequestID, want[i])
				}
				if ExecutionSummaryConversationID(summary) != "conversation-a" {
					t.Fatalf("cross-contaminated summary: %+v", summary)
				}
				if i > 0 && summary.OriginalRequestID != "turn-1" {
					t.Fatalf("hierarchy lost: %+v", summary)
				}
			}
		})
	}
}

func TestConversationExecutionListerValidatesBeforeBackendAccess(t *testing.T) {
	provider := newMockStorageProvider()
	provider.listByScoreDescErr = errors.New("backend must not be called")
	providerStore := NewExecutionStoreWithProvider(
		provider,
		DefaultExecutionStoreConfig(),
		nil,
	).(ConversationExecutionLister)
	directStore := (&RedisExecutionDebugStore{}).ListByConversationID

	invalidIDs := []string{
		"",
		"invalid conversation",
		strings.Repeat("x", core.MaxConversationIDLength+1),
		"invalid,conversation",
	}
	for _, conversationID := range invalidIDs {
		t.Run(fmt.Sprintf("%q", conversationID), func(t *testing.T) {
			if _, err := providerStore.ListByConversationID(
				context.Background(),
				conversationID,
				10,
			); err == nil || strings.Contains(err.Error(), "backend must not be called") {
				t.Fatalf("provider validation error = %v", err)
			}
			if _, err := directStore(
				context.Background(),
				conversationID,
				10,
			); err == nil {
				t.Fatal("direct store accepted invalid conversation ID")
			}
		})
	}
}

func TestConversationExecutionListerClampsLimits(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T, config ExecutionStoreConfig) ExecutionStore
	}{
		{
			name: "provider",
			store: func(_ *testing.T, config ExecutionStoreConfig) ExecutionStore {
				return NewExecutionStoreWithProvider(newMockStorageProvider(), config, nil)
			},
		},
		{
			name: "direct redis",
			store: func(t *testing.T, config ExecutionStoreConfig) ExecutionStore {
				_, store := newRedisExecutionConversationTestStore(t, config)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultExecutionStoreConfig()
			config.ConversationQueryLimit = 2
			store := test.store(t, config)
			lister := store.(ConversationExecutionLister)
			base := time.Now().Add(-time.Hour)
			for i := 0; i < 4; i++ {
				execution := executionWithConversation(
					fmt.Sprintf("turn-%d", i),
					"conversation-limit",
					base.Add(time.Duration(i)*time.Minute),
				)
				if err := store.Store(context.Background(), execution); err != nil {
					t.Fatalf("Store: %v", err)
				}
			}

			for _, limit := range []int{0, 100} {
				summaries, err := lister.ListByConversationID(
					context.Background(),
					"conversation-limit",
					limit,
				)
				if err != nil {
					t.Fatalf("ListByConversationID(%d): %v", limit, err)
				}
				if len(summaries) != 2 {
					t.Fatalf("limit %d returned %d summaries, want 2", limit, len(summaries))
				}
			}
		})
	}
}

func TestConversationExecutionListerReturnsMostRecentWindowConsistently(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T, config ExecutionStoreConfig) ExecutionStore
	}{
		{
			name: "provider",
			store: func(_ *testing.T, config ExecutionStoreConfig) ExecutionStore {
				return NewExecutionStoreWithProvider(newMockStorageProvider(), config, nil)
			},
		},
		{
			name: "direct redis",
			store: func(t *testing.T, config ExecutionStoreConfig) ExecutionStore {
				_, store := newRedisExecutionConversationTestStore(t, config)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultExecutionStoreConfig()
			config.ConversationQueryLimit = 2
			config.ConversationIndexScanLimit = 3
			store := test.store(t, config)
			lister := store.(ConversationExecutionLister)
			base := time.Now().Add(-time.Hour)
			for i := 0; i < 4; i++ {
				if err := store.Store(
					context.Background(),
					executionWithConversation(
						fmt.Sprintf("turn-%d", i),
						"conversation-most-recent",
						base.Add(time.Duration(i)*time.Minute),
					),
				); err != nil {
					t.Fatalf("Store: %v", err)
				}
			}

			summaries, err := lister.ListByConversationID(
				context.Background(),
				"conversation-most-recent",
				2,
			)
			if err != nil {
				t.Fatalf("ListByConversationID: %v", err)
			}
			want := []string{"turn-2", "turn-3"}
			if len(summaries) != len(want) {
				t.Fatalf("summary count = %d, want %d", len(summaries), len(want))
			}
			for i, summary := range summaries {
				if summary.RequestID != want[i] {
					t.Fatalf("summary[%d] = %q, want %q", i, summary.RequestID, want[i])
				}
			}
		})
	}
}

func TestProviderConversationIndexStaleCleanupHonorsScanCeiling(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	config.ConversationQueryLimit = 2
	config.ConversationIndexScanLimit = 5
	store := NewExecutionStoreWithProvider(provider, config, nil)
	lister := store.(ConversationExecutionLister)
	conversationID := "conversation-stale"
	indexKey := executionConversationIndexKey(config.KeyPrefix, conversationID)
	base := time.Now().Add(-time.Hour)

	for i := 0; i < 2; i++ {
		execution := executionWithConversation(
			fmt.Sprintf("live-%d", i),
			conversationID,
			base.Add(time.Duration(i)*time.Minute),
		)
		if err := store.Store(context.Background(), execution); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := provider.AddToIndex(
			context.Background(),
			indexKey,
			float64(base.Add(time.Duration(10+i)*time.Minute).UnixNano()),
			fmt.Sprintf("stale-%d", i),
		); err != nil {
			t.Fatalf("AddToIndex: %v", err)
		}
	}

	summaries, err := lister.ListByConversationID(
		context.Background(),
		conversationID,
		2,
	)
	if err != nil {
		t.Fatalf("ListByConversationID: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("live summaries = %d, want 2", len(summaries))
	}
	for i := 0; i < 3; i++ {
		if _, present := provider.indexes[indexKey][fmt.Sprintf("stale-%d", i)]; present {
			t.Fatalf("stale-%d was not pruned", i)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := store.Get(context.Background(), fmt.Sprintf("live-%d", i)); err != nil {
			t.Fatalf("cleanup modified live execution: %v", err)
		}
	}

	config.ConversationIndexScanLimit = 2
	limitedProvider := newMockStorageProvider()
	limitedStore := NewExecutionStoreWithProvider(limitedProvider, config, nil)
	limitedLister := limitedStore.(ConversationExecutionLister)
	if err := limitedStore.Store(
		context.Background(),
		executionWithConversation("live-beyond-scan", conversationID, base),
	); err != nil {
		t.Fatalf("limited Store: %v", err)
	}
	limitedIndexKey := executionConversationIndexKey(config.KeyPrefix, conversationID)
	for i := 0; i < 3; i++ {
		_ = limitedProvider.AddToIndex(
			context.Background(),
			limitedIndexKey,
			float64(base.Add(time.Duration(10+i)*time.Minute).UnixNano()),
			fmt.Sprintf("limited-stale-%d", i),
		)
	}
	limited, err := limitedLister.ListByConversationID(
		context.Background(),
		conversationID,
		1,
	)
	if err != nil {
		t.Fatalf("limited ListByConversationID: %v", err)
	}
	if len(limited) != 0 {
		t.Fatalf("scan ceiling was bypassed: %+v", limited)
	}
	staleRemaining := 0
	for member := range limitedProvider.indexes[limitedIndexKey] {
		if strings.HasPrefix(member, "limited-stale-") {
			staleRemaining++
		}
	}
	if staleRemaining != 1 {
		t.Fatalf("cleanup exceeded scan ceiling: %d stale remain, want 1", staleRemaining)
	}
}

func TestDirectConversationIndexStaleCleanupHonorsScanCeiling(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	config.ConversationQueryLimit = 2
	config.ConversationIndexScanLimit = 5
	mr, store := newRedisExecutionConversationTestStore(t, config)
	conversationID := "conversation-direct-stale"
	indexKey := store.conversationIndexKey(conversationID)
	base := time.Now().Add(-time.Hour)

	for i := 0; i < 2; i++ {
		execution := executionWithConversation(
			fmt.Sprintf("direct-live-%d", i),
			conversationID,
			base.Add(time.Duration(i)*time.Minute),
		)
		if err := store.Store(context.Background(), execution); err != nil {
			t.Fatalf("Store: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := store.client.ZAdd(context.Background(), indexKey, redis.Z{
			Score:  float64(base.Add(time.Duration(10+i) * time.Minute).UnixNano()),
			Member: fmt.Sprintf("direct-stale-%d", i),
		}).Err(); err != nil {
			t.Fatalf("seed stale member: %v", err)
		}
	}

	summaries, err := store.ListByConversationID(
		context.Background(),
		conversationID,
		2,
	)
	if err != nil {
		t.Fatalf("ListByConversationID: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("live summaries = %d, want 2", len(summaries))
	}
	for i := 0; i < 3; i++ {
		if _, err := store.client.ZScore(
			context.Background(),
			indexKey,
			fmt.Sprintf("direct-stale-%d", i),
		).Result(); err != redis.Nil {
			t.Fatalf("direct-stale-%d was not pruned", i)
		}
	}
	for i := 0; i < 2; i++ {
		if !mr.Exists(store.recordKey(fmt.Sprintf("direct-live-%d", i))) {
			t.Fatalf("cleanup removed direct-live-%d record", i)
		}
	}

	config.ConversationIndexScanLimit = 2
	_, limitedStore := newRedisExecutionConversationTestStore(t, config)
	limitedConversationID := "conversation-direct-scan-limit"
	limitedIndexKey := limitedStore.conversationIndexKey(limitedConversationID)
	if err := limitedStore.Store(
		context.Background(),
		executionWithConversation(
			"direct-live-beyond-scan",
			limitedConversationID,
			base,
		),
	); err != nil {
		t.Fatalf("limited Store: %v", err)
	}
	for i := 0; i < 3; i++ {
		_ = limitedStore.client.ZAdd(context.Background(), limitedIndexKey, redis.Z{
			Score:  float64(base.Add(time.Duration(10+i) * time.Minute).UnixNano()),
			Member: fmt.Sprintf("direct-limited-stale-%d", i),
		}).Err()
	}
	limited, err := limitedStore.ListByConversationID(
		context.Background(),
		limitedConversationID,
		1,
	)
	if err != nil {
		t.Fatalf("limited ListByConversationID: %v", err)
	}
	if len(limited) != 0 {
		t.Fatalf("scan ceiling was bypassed: %+v", limited)
	}
	staleRemaining := 0
	for i := 0; i < 3; i++ {
		if _, err := limitedStore.client.ZScore(
			context.Background(),
			limitedIndexKey,
			fmt.Sprintf("direct-limited-stale-%d", i),
		).Result(); err == nil {
			staleRemaining++
		}
	}
	if staleRemaining != 1 {
		t.Fatalf("cleanup exceeded direct scan ceiling: %d stale remain, want 1", staleRemaining)
	}
}

type conversationCleanupFailProvider struct {
	*mockStorageProvider
	err error
}

func (p *conversationCleanupFailProvider) RemoveFromIndex(
	context.Context,
	string,
	...string,
) error {
	return p.err
}

type conversationZRemFailureHook struct {
	err error
}

func (conversationZRemFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h conversationZRemFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "zrem" {
			return h.err
		}
		return next(ctx, cmd)
	}
}

func (conversationZRemFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestConversationIndexCleanupFailureIsNonFatalAndSanitized(t *testing.T) {
	conversationID := "conversation-cleanup-secret"
	rawErr := errors.New(
		"redis://user:password@host/8?conversation=" + conversationID,
	)

	t.Run("provider", func(t *testing.T) {
		provider := &conversationCleanupFailProvider{
			mockStorageProvider: newMockStorageProvider(),
			err:                 rawErr,
		}
		logger := &recordingLogger{}
		store := NewExecutionStoreWithProvider(
			provider,
			DefaultExecutionStoreConfig(),
			logger,
		).(ConversationExecutionLister)
		indexKey := executionConversationIndexKey(
			DefaultExecutionKeyPrefix,
			conversationID,
		)
		if err := provider.AddToIndex(
			context.Background(),
			indexKey,
			1,
			"stale-provider",
		); err != nil {
			t.Fatalf("seed stale member: %v", err)
		}

		if _, err := store.ListByConversationID(
			context.Background(),
			conversationID,
			1,
		); err != nil {
			t.Fatalf("cleanup failure became fatal: %v", err)
		}
		assertConversationCleanupWarning(t, logger, conversationID)
	})

	t.Run("direct redis", func(t *testing.T) {
		_, store := newRedisExecutionConversationTestStore(
			t,
			DefaultExecutionStoreConfig(),
		)
		logger := &recordingLogger{}
		store.logger = logger
		store.client.AddHook(conversationZRemFailureHook{err: rawErr})
		indexKey := store.conversationIndexKey(conversationID)
		if err := store.client.ZAdd(
			context.Background(),
			indexKey,
			redis.Z{Score: 1, Member: "stale-direct"},
		).Err(); err != nil {
			t.Fatalf("seed stale member: %v", err)
		}

		if _, err := store.ListByConversationID(
			context.Background(),
			conversationID,
			1,
		); err != nil {
			t.Fatalf("cleanup failure became fatal: %v", err)
		}
		assertConversationCleanupWarning(t, logger, conversationID)
	})
}

func assertConversationCleanupWarning(
	t *testing.T,
	logger *recordingLogger,
	rawConversationID string,
) {
	t.Helper()
	if len(logger.warns) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warns))
	}
	fields := logger.warns[0].fields
	if fields["operation"] != "execution_store_conversation_index_cleanup" ||
		fields["error_type"] != "index_write" ||
		fields["error"] != "execution store backend write failed" {
		t.Fatalf("warning fields = %v", fields)
	}
	if strings.Contains(fmt.Sprint(fields), rawConversationID) ||
		strings.Contains(fmt.Sprint(fields), "password") {
		t.Fatalf("warning leaked sensitive fields: %v", fields)
	}
}

type indexTTLTestProvider struct {
	*mockStorageProvider
	indexTTLs map[string]time.Duration
	ttlCalls  []string
	err       error
}

func newIndexTTLTestProvider() *indexTTLTestProvider {
	return &indexTTLTestProvider{
		mockStorageProvider: newMockStorageProvider(),
		indexTTLs:           make(map[string]time.Duration),
	}
}

func (p *indexTTLTestProvider) ExtendIndexTTL(
	_ context.Context,
	indexKey string,
	minTTL time.Duration,
) error {
	p.ttlCalls = append(p.ttlCalls, indexKey)
	if p.err != nil {
		return p.err
	}
	if p.indexTTLs[indexKey] < minTTL {
		p.indexTTLs[indexKey] = minTTL
	}
	return nil
}

func TestProviderConversationIndexTTLDoesNotDowngrade(t *testing.T) {
	provider := newIndexTTLTestProvider()
	config := DefaultExecutionStoreConfig()
	store := NewExecutionStoreWithProvider(provider, config, nil)
	conversationID := "conversation-provider-ttl"
	indexKey := executionConversationIndexKey(config.KeyPrefix, conversationID)

	failed := executionWithConversation("failed", conversationID, time.Now())
	failed.Result.Success = false
	if err := store.Store(context.Background(), failed); err != nil {
		t.Fatalf("failed Store: %v", err)
	}
	if got := provider.indexTTLs[indexKey]; got != config.ErrorTTL {
		t.Fatalf("error index TTL = %v, want %v", got, config.ErrorTTL)
	}

	success := executionWithConversation("success", conversationID, time.Now().Add(time.Minute))
	if err := store.Store(context.Background(), success); err != nil {
		t.Fatalf("success Store: %v", err)
	}
	if got := provider.indexTTLs[indexKey]; got != config.ErrorTTL {
		t.Fatalf("index TTL downgraded to %v", got)
	}
	if err := store.ExtendTTL(context.Background(), "success", 14*24*time.Hour); err != nil {
		t.Fatalf("ExtendTTL: %v", err)
	}
	if got := provider.indexTTLs[indexKey]; got != 14*24*time.Hour {
		t.Fatalf("extended index TTL = %v", got)
	}
}

func TestProviderWithoutIndexTTLManagerRemainsUsableWithoutWarning(t *testing.T) {
	provider := newMockStorageProvider()
	logger := &recordingLogger{}
	store := NewExecutionStoreWithProvider(
		provider,
		DefaultExecutionStoreConfig(),
		logger,
	)
	if err := store.Store(
		context.Background(),
		executionWithConversation(
			"request-no-ttl-capability",
			"conversation-no-ttl-capability",
			time.Now(),
		),
	); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(logger.warns) != 0 {
		t.Fatalf("missing optional capability emitted warning: %+v", logger.warns)
	}
}

func TestProviderConversationIndexTTLFailureIsNonFatalAndSanitized(t *testing.T) {
	rawConversationID := "conversation-provider-ttl-secret"
	provider := newIndexTTLTestProvider()
	provider.err = errors.New(
		"redis://user:password@host/8?conversation=" + rawConversationID,
	)
	logger := &recordingLogger{}
	store := NewExecutionStoreWithProvider(
		provider,
		DefaultExecutionStoreConfig(),
		logger,
	)
	if err := store.Store(
		context.Background(),
		executionWithConversation("request-ttl-error", rawConversationID, time.Now()),
	); err != nil {
		t.Fatalf("TTL failure became fatal: %v", err)
	}
	if len(logger.warns) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warns))
	}
	fields := logger.warns[0].fields
	if fields["operation"] != "execution_store_conversation_index_ttl" ||
		fields["request_id"] != "request-ttl-error" ||
		fields["error_type"] != "ttl_update" ||
		fields["error"] != "execution store backend write failed" {
		t.Fatalf("warning fields = %v", fields)
	}
	if strings.Contains(fmt.Sprint(fields), rawConversationID) ||
		strings.Contains(fmt.Sprint(fields), "password") {
		t.Fatalf("warning leaked sensitive fields: %v", fields)
	}
}

func TestDirectRedisConversationIndexTTLDoesNotDowngrade(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	mr, store := newRedisExecutionConversationTestStore(t, config)
	conversationID := "conversation-direct-ttl"
	indexKey := executionConversationIndexKey(config.KeyPrefix, conversationID)

	failed := executionWithConversation("failed", conversationID, time.Now())
	failed.Result.Success = false
	if err := store.Store(context.Background(), failed); err != nil {
		t.Fatalf("failed Store: %v", err)
	}
	if got := mr.TTL(indexKey); got != config.ErrorTTL {
		t.Fatalf("error index TTL = %v, want %v", got, config.ErrorTTL)
	}

	success := executionWithConversation("success", conversationID, time.Now().Add(time.Minute))
	if err := store.Store(context.Background(), success); err != nil {
		t.Fatalf("success Store: %v", err)
	}
	if got := mr.TTL(indexKey); got != config.ErrorTTL {
		t.Fatalf("index TTL downgraded to %v", got)
	}

	if err := store.ExtendTTL(context.Background(), "success", time.Hour); err != nil {
		t.Fatalf("short ExtendTTL: %v", err)
	}
	if got := mr.TTL(indexKey); got != config.ErrorTTL {
		t.Fatalf("short extension downgraded index TTL to %v", got)
	}
	if err := store.ExtendTTL(context.Background(), "success", 14*24*time.Hour); err != nil {
		t.Fatalf("long ExtendTTL: %v", err)
	}
	if got := mr.TTL(indexKey); got != 14*24*time.Hour {
		t.Fatalf("long extension index TTL = %v", got)
	}
	if got := mr.TTL(store.recordKey("success")); got != 14*24*time.Hour {
		t.Fatalf("record TTL = %v", got)
	}
	if got := mr.TTL(store.traceKey(success.TraceID)); got != 14*24*time.Hour {
		t.Fatalf("trace TTL = %v", got)
	}
}

func TestDirectRedisIndexTTLHelperHandlesMissingAndPersistentKeys(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(
		t,
		DefaultExecutionStoreConfig(),
	)
	ctx := context.Background()

	if exists, err := extendRedisKeyMinimumTTL(
		ctx,
		store.client,
		"missing-index",
		time.Hour,
	); err != nil || exists {
		t.Fatalf("missing index: %v", err)
	}

	persistentKey := "persistent-index"
	if err := store.client.ZAdd(
		ctx,
		persistentKey,
		redis.Z{Score: 1, Member: "member"},
	).Err(); err != nil {
		t.Fatalf("seed persistent index: %v", err)
	}
	if exists, err := extendRedisKeyMinimumTTL(
		ctx,
		store.client,
		persistentKey,
		time.Hour,
	); err != nil || !exists {
		t.Fatalf("preserve persistent index: %v", err)
	}
	if got, err := store.client.TTL(ctx, persistentKey).Result(); err != nil ||
		got != redisTTLPersistent {
		t.Fatalf("persistent TTL = (%v, %v), want Redis persistent sentinel", got, err)
	}

}

func TestExecutionStoreCanonicalKeysMatchAcrossImplementations(t *testing.T) {
	for _, prefix := range []string{
		"tenant:execution:debug",
		"tenant:execution:debug:",
		"tenant:execution:debug::",
	} {
		config := DefaultExecutionStoreConfig()
		config.KeyPrefix = prefix
		providerStore := NewExecutionStoreWithProvider(
			newMockStorageProvider(),
			config,
			nil,
		).(*executionStoreImpl)
		directStore := &RedisExecutionDebugStore{keyPrefix: prefix}
		conversationID := "conversation-key"

		keys := [][2]string{
			{providerStore.recordKey("request"), directStore.recordKey("request")},
			{providerStore.indexKey(), directStore.indexKey()},
			{providerStore.traceKey("trace"), directStore.traceKey("trace")},
			{
				providerStore.conversationIndexKey(conversationID),
				directStore.conversationIndexKey(conversationID),
			},
		}
		for _, pair := range keys {
			if pair[0] != pair[1] {
				t.Fatalf("prefix %q key mismatch: %q != %q", prefix, pair[0], pair[1])
			}
			if strings.Contains(pair[0], "debug::") {
				t.Fatalf("prefix %q produced double separator: %q", prefix, pair[0])
			}
		}
		if strings.Contains(
			providerStore.conversationIndexKey(conversationID),
			conversationID,
		) {
			t.Fatal("conversation index key retained the raw conversation ID")
		}
	}
}

func TestExecutionStoreTraceLookupIsLastWriter(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) ExecutionStore
	}{
		{
			name: "provider",
			store: func(*testing.T) ExecutionStore {
				return NewExecutionStoreWithProvider(
					newMockStorageProvider(),
					DefaultExecutionStoreConfig(),
					nil,
				)
			},
		},
		{
			name: "direct redis",
			store: func(t *testing.T) ExecutionStore {
				_, store := newRedisExecutionConversationTestStore(
					t,
					DefaultExecutionStoreConfig(),
				)
				return store
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := test.store(t)
			first := sampleExecution("trace-first", true)
			first.TraceID = "shared-trace"
			second := sampleExecution("trace-second", true)
			second.TraceID = "shared-trace"
			if err := store.Store(context.Background(), first); err != nil {
				t.Fatalf("first Store: %v", err)
			}
			if err := store.Store(context.Background(), second); err != nil {
				t.Fatalf("second Store: %v", err)
			}
			got, err := store.GetByTraceID(context.Background(), "shared-trace")
			if err != nil {
				t.Fatalf("GetByTraceID: %v", err)
			}
			if got.RequestID != "trace-second" {
				t.Fatalf("trace lookup = %q, want last writer", got.RequestID)
			}
		})
	}
}

func TestNoOpExecutionStoreDoesNotAdvertiseConversationListing(t *testing.T) {
	if _, ok := interface{}(NewNoOpExecutionStore()).(ConversationExecutionLister); ok {
		t.Fatal("NoOpExecutionStore unexpectedly implements ConversationExecutionLister")
	}
}

func TestExecutionStoreConversationLimitConfiguration(t *testing.T) {
	defaults := DefaultExecutionStoreConfig()
	if defaults.ConversationQueryLimit != defaultConversationQueryLimit ||
		defaults.ConversationIndexScanLimit != defaultConversationIndexScanLimit {
		t.Fatalf("conversation defaults = %+v", defaults)
	}

	t.Setenv("TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT", "17")
	t.Setenv("TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT", "29")
	fromEnvironment := DefaultConfig().ExecutionStore
	if fromEnvironment.ConversationQueryLimit != 17 ||
		fromEnvironment.ConversationIndexScanLimit != 29 {
		t.Fatalf("environment config = %+v", fromEnvironment)
	}

	t.Setenv("TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT", "0")
	t.Setenv("TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT", "invalid")
	invalidEnvironment := DefaultConfig().ExecutionStore
	if invalidEnvironment.ConversationQueryLimit != defaultConversationQueryLimit ||
		invalidEnvironment.ConversationIndexScanLimit != defaultConversationIndexScanLimit {
		t.Fatalf("invalid environment changed defaults: %+v", invalidEnvironment)
	}

	normalized := normalizeExecutionStoreConfig(ExecutionStoreConfig{})
	if normalized.ConversationQueryLimit != defaultConversationQueryLimit ||
		normalized.ConversationIndexScanLimit != defaultConversationIndexScanLimit ||
		normalized.KeyPrefix != DefaultExecutionKeyPrefix ||
		normalized.TTL != defaults.TTL ||
		normalized.ErrorTTL != defaults.ErrorTTL {
		t.Fatalf("zero-value normalization = %+v", normalized)
	}
}

func TestRedisExecutionStoreWithConfigUsesExplicitLimits(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	t.Setenv("TRUVAG3_EXECUTION_DEBUG_CONVERSATION_QUERY_LIMIT", "99")
	t.Setenv("TRUVAG3_EXECUTION_DEBUG_INDEX_SCAN_LIMIT", "199")
	config := DefaultExecutionStoreConfig()
	config.KeyPrefix = "explicit:execution"
	config.ConversationQueryLimit = 3
	config.ConversationIndexScanLimit = 7
	store, err := NewRedisExecutionDebugStoreWithConfig(
		config,
		WithExecutionDebugRedisURL("redis://"+mr.Addr()),
	)
	if err != nil {
		t.Fatalf("NewRedisExecutionDebugStoreWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if store.queryLimit != 3 || store.indexScanLimit != 7 {
		t.Fatalf(
			"explicit limits = (%d, %d)",
			store.queryLimit,
			store.indexScanLimit,
		)
	}
	if store.keyPrefix != "explicit:execution:" {
		t.Fatalf("normalized prefix = %q", store.keyPrefix)
	}
}

type conversationIndexFailProvider struct {
	*mockStorageProvider
	err error
}

func (p *conversationIndexFailProvider) AddToIndex(
	ctx context.Context,
	key string,
	score float64,
	member string,
) error {
	if strings.Contains(key, ":conversation:") {
		return p.err
	}
	return p.mockStorageProvider.AddToIndex(ctx, key, score, member)
}

func TestProviderConversationIndexFailureIsNonFatalAndSanitized(t *testing.T) {
	rawConversationID := "conversation-secret"
	provider := &conversationIndexFailProvider{
		mockStorageProvider: newMockStorageProvider(),
		err: errors.New(
			"redis://user:password@host/8?conversation=" + rawConversationID,
		),
	}
	logger := &recordingLogger{}
	store := NewExecutionStoreWithProvider(
		provider,
		DefaultExecutionStoreConfig(),
		logger,
	)
	if err := store.Store(
		context.Background(),
		executionWithConversation("request-index-error", rawConversationID, time.Now()),
	); err != nil {
		t.Fatalf("conversation index failure became fatal: %v", err)
	}
	if len(logger.warns) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warns))
	}
	fields := logger.warns[0].fields
	if fields["operation"] != "execution_store_conversation_index" ||
		fields["request_id"] != "request-index-error" ||
		fields["error_type"] != "index_write" {
		t.Fatalf("warning fields = %v", fields)
	}
	if got := fmt.Sprint(fields["error"]); got != "execution store backend write failed" {
		t.Fatalf("sanitized error = %q", got)
	}
	if strings.Contains(fmt.Sprint(fields), rawConversationID) ||
		strings.Contains(fmt.Sprint(fields), "password") {
		t.Fatalf("warning leaked sensitive fields: %v", fields)
	}
}

type conversationZAddFailureHook struct {
	err error
}

func (conversationZAddFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h conversationZAddFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "eval" || cmd.Name() == "evalsha" {
			for _, arg := range cmd.Args() {
				if strings.Contains(fmt.Sprint(arg), ":conversation:") {
					return h.err
				}
			}
		}
		return next(ctx, cmd)
	}
}

func (conversationZAddFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestDirectConversationIndexFailureIsNonFatalNilSafeAndSanitized(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	_, store := newRedisExecutionConversationTestStore(t, config)
	rawConversationID := "conversation-direct-secret"
	store.client.AddHook(conversationZAddFailureHook{
		err: errors.New(
			"redis://user:password@host/8?conversation=" + rawConversationID,
		),
	})
	logger := &recordingLogger{}
	store.logger = logger

	execution := executionWithConversation(
		"request-direct-index-error",
		rawConversationID,
		time.Now(),
	)
	if err := store.Store(context.Background(), execution); err != nil {
		t.Fatalf("conversation index failure became fatal: %v", err)
	}
	if _, err := store.Get(context.Background(), execution.RequestID); err != nil {
		t.Fatalf("main execution record was lost: %v", err)
	}
	if len(logger.warns) != 1 {
		t.Fatalf("warnings = %d, want 1", len(logger.warns))
	}
	fields := logger.warns[0].fields
	if fields["operation"] != "execution_store_conversation_index" ||
		fields["request_id"] != execution.RequestID ||
		fields["error_type"] != "index_write" ||
		fields["error"] != "execution store backend write failed" {
		t.Fatalf("warning fields = %v", fields)
	}
	if strings.Contains(fmt.Sprint(fields), rawConversationID) ||
		strings.Contains(fmt.Sprint(fields), "password") {
		t.Fatalf("warning leaked sensitive fields: %v", fields)
	}

	store.logger = nil
	execution.RequestID = "request-direct-index-error-nil-logger"
	if err := store.Store(context.Background(), execution); err != nil {
		t.Fatalf("nil logger made optional index failure fatal: %v", err)
	}
}

type benchmarkCountingStorageProvider struct {
	*mockStorageProvider
	commandCount  uint64
	preserveStale bool
}

func (p *benchmarkCountingStorageProvider) Get(
	ctx context.Context,
	key string,
) (string, error) {
	p.commandCount++
	return p.mockStorageProvider.Get(ctx, key)
}

func (p *benchmarkCountingStorageProvider) ListByScoreDesc(
	ctx context.Context,
	key string,
	min string,
	max string,
	offset int64,
	count int64,
) ([]string, error) {
	p.commandCount++
	return p.mockStorageProvider.ListByScoreDesc(
		ctx,
		key,
		min,
		max,
		offset,
		count,
	)
}

func (p *benchmarkCountingStorageProvider) RemoveFromIndex(
	ctx context.Context,
	key string,
	members ...string,
) error {
	p.commandCount++
	if p.preserveStale {
		return nil
	}
	return p.mockStorageProvider.RemoveFromIndex(ctx, key, members...)
}

type benchmarkRedisCommandHook struct {
	commandCount  atomic.Uint64
	preserveStale bool
}

func (*benchmarkRedisCommandHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *benchmarkRedisCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.commandCount.Add(1)
		h.redirectStaleCleanup(cmd)
		return next(ctx, cmd)
	}
}

func (h *benchmarkRedisCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.commandCount.Add(uint64(len(cmds)))
		for _, cmd := range cmds {
			h.redirectStaleCleanup(cmd)
		}
		return next(ctx, cmds)
	}
}

func (h *benchmarkRedisCommandHook) redirectStaleCleanup(cmd redis.Cmder) {
	if !h.preserveStale || cmd.Name() != "zrem" {
		return
	}
	args := cmd.Args()
	if len(args) > 1 {
		args[1] = fmt.Sprint(args[1]) + ":benchmark-noop"
	}
}

func BenchmarkConversationExecutionLookup(b *testing.B) {
	b.Run("provider/query_limit", benchmarkProviderConversationQueryLimit)
	b.Run("provider/stale_scan_ceiling", benchmarkProviderConversationStaleScanCeiling)
	b.Run("direct_redis/query_limit", benchmarkDirectConversationQueryLimit)
	b.Run("direct_redis/stale_scan_ceiling", benchmarkDirectConversationStaleScanCeiling)
}

func benchmarkProviderConversationQueryLimit(b *testing.B) {
	config := DefaultExecutionStoreConfig()
	provider := &benchmarkCountingStorageProvider{
		mockStorageProvider: newMockStorageProvider(),
	}
	store := NewExecutionStoreWithProvider(provider, config, nil)
	lister := store.(ConversationExecutionLister)
	conversationID := "conversation-provider-query-benchmark"
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < config.ConversationQueryLimit; i++ {
		if err := store.Store(
			context.Background(),
			executionWithConversation(
				fmt.Sprintf("provider-live-%04d", i),
				conversationID,
				base.Add(time.Duration(i)*time.Second),
			),
		); err != nil {
			b.Fatal(err)
		}
	}
	provider.commandCount = 0

	b.ReportAllocs()
	b.ReportMetric(float64(config.ConversationQueryLimit), "records/op")
	b.ResetTimer()
	var got []ExecutionSummary
	for i := 0; i < b.N; i++ {
		var err error
		got, err = lister.ListByConversationID(
			context.Background(),
			conversationID,
			config.ConversationQueryLimit,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchmarkConversationSummariesSink = got
	b.ReportMetric(float64(provider.commandCount)/float64(b.N), "provider_cmds/op")
}

func benchmarkProviderConversationStaleScanCeiling(b *testing.B) {
	config := DefaultExecutionStoreConfig()
	provider := &benchmarkCountingStorageProvider{
		mockStorageProvider: newMockStorageProvider(),
		preserveStale:       true,
	}
	store := NewExecutionStoreWithProvider(provider, config, nil)
	lister := store.(ConversationExecutionLister)
	conversationID := "conversation-provider-stale-benchmark"
	indexKey := executionConversationIndexKey(config.KeyPrefix, conversationID)
	for i := 0; i < config.ConversationIndexScanLimit; i++ {
		if err := provider.AddToIndex(
			context.Background(),
			indexKey,
			float64(i),
			fmt.Sprintf("provider-stale-%04d", i),
		); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ReportMetric(float64(config.ConversationIndexScanLimit), "records_scanned/op")
	b.ResetTimer()
	var got []ExecutionSummary
	for i := 0; i < b.N; i++ {
		var err error
		got, err = lister.ListByConversationID(
			context.Background(),
			conversationID,
			config.ConversationQueryLimit,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchmarkConversationSummariesSink = got
	b.ReportMetric(float64(provider.commandCount)/float64(b.N), "provider_cmds/op")
}

func benchmarkDirectConversationQueryLimit(b *testing.B) {
	config := DefaultExecutionStoreConfig()
	_, store := newRedisExecutionConversationTestStore(b, config)
	conversationID := "conversation-direct-query-benchmark"
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < config.ConversationQueryLimit; i++ {
		if err := store.Store(
			context.Background(),
			executionWithConversation(
				fmt.Sprintf("direct-live-%04d", i),
				conversationID,
				base.Add(time.Duration(i)*time.Second),
			),
		); err != nil {
			b.Fatal(err)
		}
	}
	hook := &benchmarkRedisCommandHook{}
	store.client.AddHook(hook)

	b.ReportAllocs()
	b.ReportMetric(float64(config.ConversationQueryLimit), "records/op")
	b.ResetTimer()
	var got []ExecutionSummary
	for i := 0; i < b.N; i++ {
		var err error
		got, err = store.ListByConversationID(
			context.Background(),
			conversationID,
			config.ConversationQueryLimit,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchmarkConversationSummariesSink = got
	b.ReportMetric(float64(hook.commandCount.Load())/float64(b.N), "redis_cmds/op")
}

func benchmarkDirectConversationStaleScanCeiling(b *testing.B) {
	config := DefaultExecutionStoreConfig()
	_, store := newRedisExecutionConversationTestStore(b, config)
	conversationID := "conversation-direct-stale-benchmark"
	indexKey := store.conversationIndexKey(conversationID)
	members := make([]redis.Z, 0, config.ConversationIndexScanLimit)
	for i := 0; i < config.ConversationIndexScanLimit; i++ {
		members = append(members, redis.Z{
			Score:  float64(i),
			Member: fmt.Sprintf("direct-stale-%04d", i),
		})
	}
	if err := store.client.ZAdd(context.Background(), indexKey, members...).Err(); err != nil {
		b.Fatal(err)
	}
	hook := &benchmarkRedisCommandHook{preserveStale: true}
	store.client.AddHook(hook)

	b.ReportAllocs()
	b.ReportMetric(float64(config.ConversationIndexScanLimit), "records_scanned/op")
	b.ResetTimer()
	var got []ExecutionSummary
	for i := 0; i < b.N; i++ {
		var err error
		got, err = store.ListByConversationID(
			context.Background(),
			conversationID,
			config.ConversationQueryLimit,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
	benchmarkConversationSummariesSink = got
	b.ReportMetric(float64(hook.commandCount.Load())/float64(b.N), "redis_cmds/op")
}

type conversationTTLFailureHook struct {
	conversationKey string
	err             error
}

func (conversationTTLFailureHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h conversationTTLFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "ttl" &&
			len(cmd.Args()) > 1 &&
			fmt.Sprint(cmd.Args()[1]) == h.conversationKey {
			return h.err
		}
		return next(ctx, cmd)
	}
}

func (conversationTTLFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestDirectConversationIndexStoreDoesNotUseRacyTTLProbe(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	mr, store := newRedisExecutionConversationTestStore(t, config)
	conversationID := "conversation-direct-atomic-ttl"
	conversationKey := store.conversationIndexKey(conversationID)
	store.client.AddHook(conversationTTLFailureHook{
		conversationKey: conversationKey,
		err:             errors.New("standalone TTL probe must not run"),
	})
	logger := &recordingLogger{}
	store.logger = logger

	if err := store.Store(
		context.Background(),
		executionWithConversation("request-direct-atomic-ttl", conversationID, time.Now()),
	); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(logger.warns) != 0 {
		t.Fatalf("unexpected warnings = %+v", logger.warns)
	}
	if got := mr.TTL(conversationKey); got != config.TTL {
		t.Fatalf("conversation index TTL = %v, want %v", got, config.TTL)
	}
	if _, err := store.client.ZScore(
		context.Background(),
		conversationKey,
		"request-direct-atomic-ttl",
	).Result(); err != nil {
		t.Fatalf("conversation member missing after atomic upsert: %v", err)
	}
}
