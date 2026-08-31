package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type failureCountingCircuitBreaker struct {
	mu       sync.Mutex
	failures int
	open     bool
}

func (b *failureCountingCircuitBreaker) Execute(
	ctx context.Context,
	operation func() error,
) error {
	b.mu.Lock()
	if b.open {
		b.mu.Unlock()
		return errors.New("test circuit breaker is open")
	}
	b.mu.Unlock()

	err := operation()
	if err != nil {
		b.mu.Lock()
		b.failures++
		b.open = true
		b.mu.Unlock()
	}
	return err
}

func (b *failureCountingCircuitBreaker) ExecuteWithTimeout(
	ctx context.Context,
	_ time.Duration,
	operation func() error,
) error {
	return b.Execute(ctx, operation)
}

func (b *failureCountingCircuitBreaker) GetState() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return "open"
	}
	return "closed"
}

func (b *failureCountingCircuitBreaker) GetMetrics() map[string]interface{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]interface{}{"failure": b.failures}
}

func (b *failureCountingCircuitBreaker) Reset() {
	b.mu.Lock()
	b.failures = 0
	b.open = false
	b.mu.Unlock()
}

func (b *failureCountingCircuitBreaker) CanExecute() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.open
}

type failRedisScriptForKeyOnceHook struct {
	mu     sync.Mutex
	key    string
	err    error
	failed bool
}

func (h *failRedisScriptForKeyOnceHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *failRedisScriptForKeyOnceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		name := command.Name()
		if name == "eval" || name == "evalsha" {
			h.mu.Lock()
			if !h.failed {
				for _, argument := range command.Args() {
					if key, ok := argument.(string); ok && key == h.key {
						h.failed = true
						h.mu.Unlock()
						return h.err
					}
				}
			}
			h.mu.Unlock()
		}
		return next(ctx, command)
	}
}

func (h *failRedisScriptForKeyOnceHook) ProcessPipelineHook(
	next redis.ProcessPipelineHook,
) redis.ProcessPipelineHook {
	return next
}

type retentionLinkFailingProvider struct {
	*mockStorageProvider
	failKey string
	err     error
}

func (p *retentionLinkFailingProvider) SetKeyWithMinimumTTL(
	ctx context.Context,
	key string,
	value string,
	minimumTTL time.Duration,
) error {
	if key == p.failKey {
		return p.err
	}
	return p.mockStorageProvider.SetKeyWithMinimumTTL(ctx, key, value, minimumTTL)
}

func TestExecutionRetentionTTLAt(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		execution *StoredExecution
		normalTTL time.Duration
		errorTTL  time.Duration
		want      time.Duration
	}{
		{
			name:      "success",
			execution: &StoredExecution{Result: &ExecutionResult{Success: true}},
			normalTTL: 24 * time.Hour,
			errorTTL:  7 * 24 * time.Hour,
			want:      24 * time.Hour,
		},
		{
			name:      "failure",
			execution: &StoredExecution{Result: &ExecutionResult{Success: false}},
			normalTTL: 24 * time.Hour,
			errorTTL:  7 * 24 * time.Hour,
			want:      7 * 24 * time.Hour,
		},
		{
			name: "interrupted uses longer configured retention",
			execution: &StoredExecution{
				Interrupted: true,
				Result:      &ExecutionResult{Success: false},
			},
			normalTTL: 24 * time.Hour,
			errorTTL:  7 * 24 * time.Hour,
			want:      7 * 24 * time.Hour,
		},
		{
			name: "checkpoint window is authoritative minimum",
			execution: &StoredExecution{
				Interrupted: true,
				Checkpoint:  &ExecutionCheckpoint{ExpiresAt: now.Add(10 * 24 * time.Hour)},
			},
			normalTTL: 24 * time.Hour,
			errorTTL:  7 * 24 * time.Hour,
			want:      10 * 24 * time.Hour,
		},
		{
			name: "expired checkpoint cannot reduce retention",
			execution: &StoredExecution{
				Interrupted: true,
				Checkpoint:  &ExecutionCheckpoint{ExpiresAt: now.Add(-time.Hour)},
			},
			normalTTL: 24 * time.Hour,
			errorTTL:  time.Hour,
			want:      24 * time.Hour,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := executionRetentionTTLAt(
				test.execution,
				test.normalTTL,
				test.errorTTL,
				now,
			); got != test.want {
				t.Fatalf("retention = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDirectRedisFailedChildPromotesRootEvidence(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	config.TTL = time.Hour
	config.ErrorTTL = 7 * time.Hour
	mr, store := newRedisExecutionConversationTestStore(t, config)
	ctx := context.Background()
	now := time.Now()

	root := sampleExecution("lineage-root", true)
	root.CreatedAt = now
	if err := store.Store(ctx, root); err != nil {
		t.Fatalf("store root: %v", err)
	}
	child := sampleExecution("lineage-child", false)
	child.CreatedAt = now.Add(time.Minute)
	child.OriginalRequestID = root.RequestID
	if err := store.Store(ctx, child); err != nil {
		t.Fatalf("store child: %v", err)
	}

	for label, key := range map[string]string{
		"root record":  root.RequestID,
		"root trace":   store.traceKey(root.TraceID),
		"child record": child.RequestID,
	} {
		if label != "root trace" {
			key = store.recordKey(key)
		}
		if got := mr.TTL(key); got != config.ErrorTTL {
			t.Fatalf("%s TTL = %v, want %v", label, got, config.ErrorTTL)
		}
	}
}

func TestZeroValueExecutionStoreConfigUsesRetentionDefaults(t *testing.T) {
	defaults := DefaultExecutionStoreConfig()
	ctx := context.Background()

	provider := newMockStorageProvider()
	providerStore := NewExecutionStoreWithProvider(
		provider,
		ExecutionStoreConfig{},
		nil,
	)
	providerExecution := sampleExecution("zero-config-provider", true)
	if err := providerStore.Store(ctx, providerExecution); err != nil {
		t.Fatalf("provider Store with zero config: %v", err)
	}
	providerKey := DefaultExecutionKeyPrefix + providerExecution.RequestID
	provider.mu.RLock()
	providerTTL := provider.ttls[providerKey]
	provider.mu.RUnlock()
	if providerTTL != defaults.TTL {
		t.Fatalf("provider zero-config TTL = %v, want %v", providerTTL, defaults.TTL)
	}

	mr, redisStore := newRedisExecutionConversationTestStore(
		t,
		ExecutionStoreConfig{},
	)
	redisExecution := sampleExecution("zero-config-redis", true)
	if err := redisStore.Store(ctx, redisExecution); err != nil {
		t.Fatalf("Redis Store with zero config: %v", err)
	}
	if got := mr.TTL(redisStore.recordKey(redisExecution.RequestID)); got != defaults.TTL {
		t.Fatalf("Redis zero-config TTL = %v, want %v", got, defaults.TTL)
	}
}

func TestRedisExecutionStoreOptionsNormalizeNonPositiveTTLs(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisExecutionDebugStoreWithClient(
		client,
		DefaultExecutionStoreConfig(),
		WithExecutionDebugTTL(0),
		WithExecutionDebugErrorTTL(-time.Second),
	)
	if err != nil {
		t.Fatalf("NewRedisExecutionDebugStoreWithClient: %v", err)
	}
	defaults := DefaultExecutionStoreConfig()
	if store.ttl != defaults.TTL || store.errorTTL != defaults.ErrorTTL {
		t.Fatalf(
			"normalized TTLs = (%v, %v), want (%v, %v)",
			store.ttl,
			store.errorTTL,
			defaults.TTL,
			defaults.ErrorTTL,
		)
	}

	ctx := context.Background()
	for _, test := range []struct {
		requestID string
		success   bool
		wantTTL   time.Duration
	}{
		{requestID: "zero-option-success", success: true, wantTTL: defaults.TTL},
		{requestID: "zero-option-failure", success: false, wantTTL: defaults.ErrorTTL},
	} {
		execution := sampleExecution(test.requestID, test.success)
		if err := store.Store(ctx, execution); err != nil {
			t.Fatalf("Store(%s): %v", test.requestID, err)
		}
		if got := mr.TTL(store.recordKey(test.requestID)); got != test.wantTTL {
			t.Fatalf("%s TTL = %v, want %v", test.requestID, got, test.wantTTL)
		}
	}
}

func TestDirectRedisExtendChildPromotesCompleteRetentionChain(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	config.TTL = time.Hour
	config.ErrorTTL = 7 * time.Hour
	mr, store := newRedisExecutionConversationTestStore(t, config)
	ctx := context.Background()
	const conversationID = "conversation-retention-chain"

	root := sampleExecution("retention-chain-root", true)
	root.Metadata = map[string]string{MetadataConversationID: conversationID}
	child := sampleExecution("retention-chain-child", true)
	child.OriginalRequestID = root.RequestID
	child.Metadata = map[string]string{MetadataConversationID: conversationID}
	grandchild := sampleExecution("retention-chain-grandchild", true)
	grandchild.OriginalRequestID = child.RequestID
	grandchild.Metadata = map[string]string{MetadataConversationID: conversationID}
	for _, execution := range []*StoredExecution{root, child, grandchild} {
		if err := store.Store(ctx, execution); err != nil {
			t.Fatalf("Store(%s): %v", execution.RequestID, err)
		}
	}

	const promotedTTL = 14 * 24 * time.Hour
	if err := store.ExtendTTL(ctx, grandchild.RequestID, promotedTTL); err != nil {
		t.Fatalf("ExtendTTL(grandchild): %v", err)
	}
	for _, execution := range []*StoredExecution{root, child, grandchild} {
		for label, key := range map[string]string{
			"record":         store.recordKey(execution.RequestID),
			"retention link": store.retentionLinkKey(execution.RequestID),
			"trace mapping":  store.traceKey(execution.TraceID),
		} {
			if got := mr.TTL(key); got != promotedTTL {
				t.Fatalf("%s %s TTL = %v, want %v", execution.RequestID, label, got, promotedTTL)
			}
		}
	}
	if got := mr.TTL(store.conversationIndexKey(conversationID)); got != promotedTTL {
		t.Fatalf("conversation index TTL = %v, want %v", got, promotedTTL)
	}
}

func TestProviderExtendChildPromotesCompleteRetentionChain(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	config.TTL = time.Hour
	store := NewExecutionStoreWithProvider(provider, config, nil)
	ctx := context.Background()

	root := sampleExecution("provider-chain-root", true)
	child := sampleExecution("provider-chain-child", true)
	child.OriginalRequestID = root.RequestID
	grandchild := sampleExecution("provider-chain-grandchild", true)
	grandchild.OriginalRequestID = child.RequestID
	for _, execution := range []*StoredExecution{root, child, grandchild} {
		if err := store.Store(ctx, execution); err != nil {
			t.Fatalf("Store(%s): %v", execution.RequestID, err)
		}
	}

	const promotedTTL = 14 * 24 * time.Hour
	if err := store.ExtendTTL(ctx, grandchild.RequestID, promotedTTL); err != nil {
		t.Fatalf("ExtendTTL(grandchild): %v", err)
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	for _, execution := range []*StoredExecution{root, child, grandchild} {
		for label, key := range map[string]string{
			"record":         normalizeExecutionKeyPrefix(config.KeyPrefix) + execution.RequestID,
			"retention link": executionRetentionLinkKey(config.KeyPrefix, execution.RequestID),
			"trace mapping":  normalizeExecutionKeyPrefix(config.KeyPrefix) + "trace:" + execution.TraceID,
		} {
			if got := provider.ttls[key]; got != promotedTTL {
				t.Fatalf("%s %s TTL = %v, want %v", execution.RequestID, label, got, promotedTTL)
			}
		}
	}
}

func TestDirectRedisExtendTTLUsesRetentionLinkAndCircuitBreaker(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
	ctx := context.Background()
	execution := sampleExecution("retention-link-extension", true)
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.client.Set(
		ctx,
		store.recordKey(execution.RequestID),
		"not-a-valid-execution-payload",
		time.Hour,
	).Err(); err != nil {
		t.Fatalf("corrupt full record fixture: %v", err)
	}

	breakerCalls := 0
	store.circuitBreaker = &mockCircuitBreaker{executeFunc: func(
		_ context.Context,
		operation func() error,
	) error {
		breakerCalls++
		return operation()
	}}
	if err := store.ExtendTTL(ctx, execution.RequestID, 14*24*time.Hour); err != nil {
		t.Fatalf("ExtendTTL: %v", err)
	}
	if breakerCalls != 1 {
		t.Fatalf("circuit breaker calls = %d, want 1", breakerCalls)
	}
}

func TestDirectRedisMissingExecutionDoesNotTripCircuitBreaker(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
	breaker := &failureCountingCircuitBreaker{}
	store.circuitBreaker = breaker
	ctx := context.Background()

	err := store.ExtendTTL(ctx, "expired-or-never-recorded", 24*time.Hour)
	if !errors.Is(err, ErrExecutionRecordNotFound) {
		t.Fatalf("ExtendTTL error = %v, want typed not-found", err)
	}
	if breaker.GetState() != "closed" {
		t.Fatalf("expected absence opened circuit breaker")
	}
	if failures := breaker.GetMetrics()["failure"]; failures != 0 {
		t.Fatalf("expected absence counted as breaker failure: %v", failures)
	}

	execution := sampleExecution("store-after-expected-absence", true)
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store after expected absence: %v", err)
	}
}

func TestExtendTTLSucceedsWhenOnlyAncestorIsMissing(t *testing.T) {
	const promotedTTL = 14 * 24 * time.Hour
	ctx := context.Background()

	t.Run("direct Redis", func(t *testing.T) {
		mr, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
		child := sampleExecution("redis-child-with-expired-ancestor", true)
		child.OriginalRequestID = "redis-expired-ancestor"
		if err := store.Store(ctx, child); err != nil {
			t.Fatalf("Store child: %v", err)
		}
		if err := store.ExtendTTL(ctx, child.RequestID, promotedTTL); err != nil {
			t.Fatalf("ExtendTTL child with missing ancestor: %v", err)
		}
		if got := mr.TTL(store.recordKey(child.RequestID)); got != promotedTTL {
			t.Fatalf("child TTL = %v, want %v", got, promotedTTL)
		}
	})

	t.Run("provider", func(t *testing.T) {
		provider := newMockStorageProvider()
		config := DefaultExecutionStoreConfig()
		storeAPI := NewExecutionStoreWithProvider(provider, config, nil)
		store, ok := storeAPI.(*executionStoreImpl)
		if !ok {
			t.Fatalf("provider constructor returned %T, want *executionStoreImpl", storeAPI)
		}
		child := sampleExecution("provider-child-with-expired-ancestor", true)
		child.OriginalRequestID = "provider-expired-ancestor"
		if err := store.Store(ctx, child); err != nil {
			t.Fatalf("Store child: %v", err)
		}
		if err := store.ExtendTTL(ctx, child.RequestID, promotedTTL); err != nil {
			t.Fatalf("ExtendTTL child with missing ancestor: %v", err)
		}
		provider.mu.RLock()
		got := provider.ttls[store.recordKey(child.RequestID)]
		provider.mu.RUnlock()
		if got != promotedTTL {
			t.Fatalf("child TTL = %v, want %v", got, promotedTTL)
		}
		if err := store.ExtendTTL(ctx, "missing-target", promotedTTL); !errors.Is(err, ErrExecutionRecordNotFound) {
			t.Fatalf("missing target error = %v, want typed not-found", err)
		}
	})
}

func TestDirectRedisUpdateKeepsRecordAndRetentionLinkCoupled(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
	ctx := context.Background()
	execution := sampleExecution("atomic-update", true)
	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store: %v", err)
	}
	linkKey := store.retentionLinkKey(execution.RequestID)
	beforeLink, err := store.client.Get(ctx, linkKey).Result()
	if err != nil {
		t.Fatalf("read original retention link: %v", err)
	}

	store.client.AddHook(&failRedisScriptForKeyOnceHook{
		key: linkKey,
		err: errors.New("injected coupled update failure"),
	})
	store.circuitBreaker = &mockCircuitBreaker{executeFunc: func(
		_ context.Context,
		operation func() error,
	) error {
		return operation()
	}}
	updated := *execution
	updated.OriginalRequest = "updated request"
	updated.TraceID = "updated-trace"
	updated.OriginalRequestID = "updated-root"
	if err := store.Update(ctx, execution.RequestID, &updated); err == nil {
		t.Fatal("Update succeeded despite injected coupled write failure")
	}

	after, err := store.Get(ctx, execution.RequestID)
	if err != nil {
		t.Fatalf("Get after failed Update: %v", err)
	}
	if after.OriginalRequest != execution.OriginalRequest ||
		after.TraceID != execution.TraceID ||
		after.OriginalRequestID != execution.OriginalRequestID {
		t.Fatalf("authoritative record changed after failed coupled update: %#v", after)
	}
	afterLink, err := store.client.Get(ctx, linkKey).Result()
	if err != nil {
		t.Fatalf("read retention link after failed Update: %v", err)
	}
	if afterLink != beforeLink {
		t.Fatalf("retention link changed after failed coupled update: %q != %q", afterLink, beforeLink)
	}
}

func TestProviderStoreContinuesWhenRetentionLinkWriteFails(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	execution := sampleExecution("provider-sidecar-write-failure", true)
	baseProvider := newMockStorageProvider()
	provider := &retentionLinkFailingProvider{
		mockStorageProvider: baseProvider,
		failKey:             executionRetentionLinkKey(config.KeyPrefix, execution.RequestID),
		err:                 errors.New("injected retention-link failure"),
	}
	warningCount := 0
	logger := &mockLogger{warnFunc: func(_ string, fields map[string]interface{}) {
		if fields["operation"] == "execution_store_retention_link" {
			warningCount++
		}
	}}
	storeAPI := NewExecutionStoreWithProvider(provider, config, logger)
	store, ok := storeAPI.(*executionStoreImpl)
	if !ok {
		t.Fatalf("provider constructor returned %T, want *executionStoreImpl", storeAPI)
	}
	ctx := context.Background()

	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store returned an error after the primary write succeeded: %v", err)
	}
	if warningCount != 1 {
		t.Fatalf("retention-link warnings = %d, want 1", warningCount)
	}
	if _, err := store.Get(ctx, execution.RequestID); err != nil {
		t.Fatalf("Get persisted execution: %v", err)
	}

	baseProvider.mu.RLock()
	_, linkExists := baseProvider.data[provider.failKey]
	_, indexed := baseProvider.indexes[store.indexKey()][execution.RequestID]
	baseProvider.mu.RUnlock()
	if linkExists {
		t.Fatalf("failed retention link unexpectedly exists")
	}
	if !indexed {
		t.Fatalf("primary execution was not indexed after sidecar failure")
	}

	const promotedTTL = 14 * 24 * time.Hour
	if err := store.ExtendTTL(ctx, execution.RequestID, promotedTTL); err != nil {
		t.Fatalf("fallback ExtendTTL without retention link: %v", err)
	}
	baseProvider.mu.RLock()
	retention := baseProvider.ttls[store.recordKey(execution.RequestID)]
	baseProvider.mu.RUnlock()
	if retention != promotedTTL {
		t.Fatalf("fallback retention = %v, want %v", retention, promotedTTL)
	}
}

func TestDirectRedisStoreContinuesWhenRetentionLinkWriteFails(t *testing.T) {
	mr, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
	execution := sampleExecution("redis-sidecar-write-failure", true)
	linkKey := store.retentionLinkKey(execution.RequestID)
	store.client.AddHook(&failRedisScriptForKeyOnceHook{
		key: linkKey,
		err: errors.New("injected retention-link failure"),
	})
	warningCount := 0
	store.logger = &mockLogger{warnFunc: func(_ string, fields map[string]interface{}) {
		if fields["operation"] == "execution_store_retention_link" {
			warningCount++
		}
	}}
	ctx := context.Background()

	if err := store.Store(ctx, execution); err != nil {
		t.Fatalf("Store returned an error after the primary write succeeded: %v", err)
	}
	if warningCount != 1 {
		t.Fatalf("retention-link warnings = %d, want 1", warningCount)
	}
	if _, err := store.Get(ctx, execution.RequestID); err != nil {
		t.Fatalf("Get persisted execution: %v", err)
	}
	if exists, err := store.client.Exists(ctx, linkKey).Result(); err != nil || exists != 0 {
		t.Fatalf("retention link existence = (%d, %v), want absent", exists, err)
	}
	if err := store.client.ZScore(ctx, store.indexKey(), execution.RequestID).Err(); err != nil {
		t.Fatalf("primary execution was not indexed after sidecar failure: %v", err)
	}

	const promotedTTL = 14 * 24 * time.Hour
	if err := store.ExtendTTL(ctx, execution.RequestID, promotedTTL); err != nil {
		t.Fatalf("fallback ExtendTTL without retention link: %v", err)
	}
	if retention := mr.TTL(store.recordKey(execution.RequestID)); retention != promotedTTL {
		t.Fatalf("fallback retention = %v, want %v", retention, promotedTTL)
	}
}

func TestProviderBackedFailedChildPromotesRootAndRewritesPreserveIt(t *testing.T) {
	provider := newMockStorageProvider()
	config := DefaultExecutionStoreConfig()
	config.TTL = time.Hour
	config.ErrorTTL = 7 * time.Hour
	store := NewExecutionStoreWithProvider(provider, config, nil)
	ctx := context.Background()

	root := sampleExecution("provider-root", true)
	if err := store.Store(ctx, root); err != nil {
		t.Fatalf("store root: %v", err)
	}
	child := sampleExecution("provider-child", false)
	child.OriginalRequestID = root.RequestID
	if err := store.Store(ctx, child); err != nil {
		t.Fatalf("store child: %v", err)
	}
	rootKey := normalizeExecutionKeyPrefix(config.KeyPrefix) + root.RequestID
	provider.mu.RLock()
	retention := provider.ttls[rootKey]
	provider.mu.RUnlock()
	if retention != config.ErrorTTL {
		t.Fatalf("promoted provider root TTL = %v, want %v", retention, config.ErrorTTL)
	}

	if err := store.ExtendTTL(ctx, root.RequestID, 14*24*time.Hour); err != nil {
		t.Fatalf("ExtendTTL: %v", err)
	}
	if err := store.SetMetadata(ctx, root.RequestID, "audit", "preserved"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	provider.mu.RLock()
	retention = provider.ttls[rootKey]
	provider.mu.RUnlock()
	if retention != 14*24*time.Hour {
		t.Fatalf("provider metadata rewrite TTL = %v, want 14d", retention)
	}
}

func TestDirectRedisContentRewritesPreservePromotedTTL(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	config.TTL = time.Hour
	config.ErrorTTL = 7 * time.Hour
	mr, store := newRedisExecutionConversationTestStore(t, config)
	ctx := context.Background()

	root := sampleExecution("rewrite-root", true)
	if err := store.Store(ctx, root); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := store.ExtendTTL(ctx, root.RequestID, 14*24*time.Hour); err != nil {
		t.Fatalf("ExtendTTL: %v", err)
	}

	root.OriginalRequest = "updated"
	if err := store.Update(ctx, root.RequestID, root); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := store.SetMetadata(ctx, root.RequestID, "audit", "retained"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	if got := mr.TTL(store.recordKey(root.RequestID)); got != 14*24*time.Hour {
		t.Fatalf("record TTL after rewrites = %v, want 14d", got)
	}

	if err := store.client.Persist(ctx, store.recordKey(root.RequestID)).Err(); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := store.SetMetadata(ctx, root.RequestID, "persistent", "true"); err != nil {
		t.Fatalf("SetMetadata persistent: %v", err)
	}
	if got := mr.TTL(store.recordKey(root.RequestID)); got != 0 {
		t.Fatalf("persistent record gained TTL %v", got)
	}
}

func TestDirectRedisMissingRootIsNotCreated(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	_, store := newRedisExecutionConversationTestStore(t, config)
	logger := &TestLogger{}
	store.logger = logger
	ctx := context.Background()
	child := sampleExecution("child-with-missing-root", false)
	child.OriginalRequestID = "missing-root"
	if err := store.Store(ctx, child); err != nil {
		t.Fatalf("child Store: %v", err)
	}
	if exists, err := store.client.Exists(ctx, store.recordKey("missing-root")).Result(); err != nil || exists != 0 {
		t.Fatalf("missing root existence = (%d, %v), want absent", exists, err)
	}
	if logs := logger.GetLogsByOperation("execution_store_lineage_retention"); len(logs) != 0 {
		t.Fatalf("expected missing root emitted warnings: %#v", logs)
	}
	if _, err := store.Get(ctx, "missing-root"); !errors.Is(err, ErrExecutionRecordNotFound) {
		t.Fatalf("missing Get error = %v, want typed not-found", err)
	}
}

func TestAtomicMinimumTTLConcurrentRequestsKeepLongest(t *testing.T) {
	config := DefaultExecutionStoreConfig()
	_, store := newRedisExecutionConversationTestStore(t, config)
	ctx := context.Background()
	key := "concurrent-retention"
	if err := store.client.Set(ctx, key, "value", time.Minute).Err(); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	var wg sync.WaitGroup
	for index := 0; index < 40; index++ {
		requested := time.Hour
		if index%2 == 0 {
			requested = 14 * 24 * time.Hour
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := extendRedisKeyMinimumTTL(ctx, store.client, key, requested); err != nil {
				t.Errorf("extend minimum TTL: %v", err)
			}
		}()
	}
	wg.Wait()
	if ttl, err := store.client.TTL(ctx, key).Result(); err != nil || ttl < 14*24*time.Hour-time.Second {
		t.Fatalf("final TTL = (%v, %v), want at least 14d", ttl, err)
	}
}

func TestRedisLLMRetentionIsNonDowngradingAndTypedWhenMissing(t *testing.T) {
	mr, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()
	requestID := "llm-retention"
	if err := store.RecordInteraction(ctx, requestID, LLMInteraction{Success: true}); err != nil {
		t.Fatalf("RecordInteraction: %v", err)
	}
	if err := store.ExtendTTL(ctx, requestID, 7*24*time.Hour); err != nil {
		t.Fatalf("ExtendTTL: %v", err)
	}
	if err := store.RecordInteraction(ctx, requestID, LLMInteraction{Success: true}); err != nil {
		t.Fatalf("second RecordInteraction: %v", err)
	}
	for _, key := range []string{
		store.recordPrefix() + requestID + llmDebugMetaSuffix,
		store.recordPrefix() + requestID + llmDebugInterSuffix,
	} {
		if got := mr.TTL(key); got != 7*24*time.Hour {
			t.Fatalf("%s TTL = %v, want 7d", key, got)
		}
	}
	legacyID := "legacy-llm-retention"
	legacyData, err := store.serialize(&LLMDebugRecord{
		RequestID: legacyID,
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("serialize legacy record: %v", err)
	}
	legacyKey := store.recordPrefix() + legacyID
	if err := store.client.Set(ctx, legacyKey, legacyData, time.Hour).Err(); err != nil {
		t.Fatalf("seed legacy record: %v", err)
	}
	if err := store.ExtendTTL(ctx, legacyID, 7*24*time.Hour); err != nil {
		t.Fatalf("extend legacy record: %v", err)
	}
	if got := mr.TTL(legacyKey); got != 7*24*time.Hour {
		t.Fatalf("legacy record TTL = %v, want 7d", got)
	}

	missingID := "llm-record-does-not-exist"
	err = store.ExtendTTL(ctx, missingID, time.Hour)
	if !errors.Is(err, ErrLLMDebugRecordNotFound) {
		t.Fatalf("missing ExtendTTL error = %v, want typed not-found", err)
	}
	keys, scanErr := store.client.Keys(ctx, "*"+missingID+"*").Result()
	if scanErr != nil || len(keys) != 0 {
		t.Fatalf("missing extension created keys = (%v, %v)", keys, scanErr)
	}
}

func TestRedisLLMRetentionFloorRepairsShorterExistingKeys(t *testing.T) {
	mr, store := setupRedisLLMDebugTestStore(t)
	ctx := context.Background()
	const requestID = "llm-retention-floor-repair"
	const promotedTTL = 14 * 24 * time.Hour
	if err := store.PreserveRetention(ctx, requestID, promotedTTL); err != nil {
		t.Fatalf("PreserveRetention: %v", err)
	}
	if err := store.RecordInteraction(
		ctx,
		requestID,
		LLMInteraction{Type: "late_writer", Success: true},
	); err != nil {
		t.Fatalf("RecordInteraction: %v", err)
	}
	keys := []string{
		store.recordPrefix() + requestID + llmDebugMetaSuffix,
		store.recordPrefix() + requestID + llmDebugInterSuffix,
	}
	for _, key := range keys {
		mr.SetTTL(key, time.Hour)
	}
	if err := store.PreserveRetention(ctx, requestID, time.Hour); err != nil {
		t.Fatalf("shorter PreserveRetention: %v", err)
	}
	for _, key := range keys {
		if got := mr.TTL(key); got != promotedTTL {
			t.Fatalf("%s repaired TTL = %v, want %v", key, got, promotedTTL)
		}
	}
}

func TestRedisSetWithMinimumTTLPreservesPersistentKey(t *testing.T) {
	_, store := newRedisExecutionConversationTestStore(t, DefaultExecutionStoreConfig())
	ctx := context.Background()
	key := "persistent-rewrite"
	if err := store.client.Set(ctx, key, "before", 0).Err(); err != nil {
		t.Fatalf("seed persistent key: %v", err)
	}
	if err := setRedisValueWithMinimumTTL(ctx, store.client, key, "after", time.Hour); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if ttl, err := store.client.TTL(ctx, key).Result(); err != nil || ttl != redisTTLPersistent {
		t.Fatalf("persistent TTL = (%v, %v)", ttl, err)
	}
	if value, err := store.client.Get(ctx, key).Result(); err != nil || value != "after" {
		t.Fatalf("rewritten value = (%q, %v)", value, err)
	}
}
