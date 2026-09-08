package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type redisSingleCommandFailureHook struct {
	mu          sync.Mutex
	command     string
	keyContains string
	enabled     bool
	attempts    int
}

func (*redisSingleCommandFailureHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (hook *redisSingleCommandFailureHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		hook.mu.Lock()
		matches := hook.enabled && command.Name() == hook.command &&
			(hook.keyContains == "" || containsRedisCommandArgument(command, hook.keyContains))
		if matches {
			hook.attempts++
		}
		hook.mu.Unlock()
		if matches {
			return errors.New("injected Redis command failure")
		}
		return next(ctx, command)
	}
}

func (*redisSingleCommandFailureHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func containsRedisCommandArgument(command redis.Cmder, wanted string) bool {
	for _, argument := range command.Args()[1:] {
		if value, ok := argument.(string); ok && value == wanted {
			return true
		}
	}
	return false
}

func TestExecutionListRecentDoesNotPruneOnTransientRecordReadFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("projection-read-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRedisExecutionDebugStoreWithClient(
		client,
		DefaultExecutionStoreConfig(),
		WithExecutionDebugKeyspace(keyspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution := sampleExecution("execution-live", true)
	execution.CreatedAt = time.Now()
	if err := store.Store(t.Context(), execution); err != nil {
		t.Fatal(err)
	}

	hook := &redisSingleCommandFailureHook{
		command: "get", keyContains: store.recordKey(execution.RequestID), enabled: true,
	}
	client.AddHook(hook)
	if _, err := store.ListRecent(t.Context(), 10); err == nil {
		t.Fatal("ListRecent succeeded despite transient record read failure")
	}
	if _, err := client.ZScore(t.Context(), store.indexKey(), execution.RequestID).Result(); err != nil {
		t.Fatalf("transient read failure pruned a live execution index member: %v", err)
	}
}

func TestLLMDebugListRecentDoesNotPruneOnTransientRecordReadFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("projection-read-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRedisLLMDebugStoreWithClient(client, WithDebugKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	requestID := "llm-live"
	if err := store.RecordInteraction(t.Context(), requestID, LLMInteraction{Type: "agent_llm_call", Success: true}); err != nil {
		t.Fatal(err)
	}

	hook := &redisSingleCommandFailureHook{
		command: "type", keyContains: store.metaKey(requestID), enabled: true,
	}
	client.AddHook(hook)
	if _, err := store.ListRecent(t.Context(), 10); err == nil {
		t.Fatal("ListRecent succeeded despite transient record read failure")
	}
	if _, err := client.ZScore(t.Context(), store.indexKey(), requestID).Result(); err != nil {
		t.Fatalf("transient read failure pruned a live LLM index member: %v", err)
	}
}

func TestRedisLLMDebugStoreIndexFailureDoesNotDuplicateAndLaterWriteRepairs(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("projection-write-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRedisLLMDebugStoreWithClient(client, WithDebugKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	logger := &recordingLogger{}
	store.logger = logger
	hook := &redisSingleCommandFailureHook{command: "zadd", enabled: true}
	client.AddHook(hook)
	requestID := "llm-index-repair"
	interaction := LLMInteraction{Type: "agent_llm_call", Success: true}

	if err := store.RecordInteraction(t.Context(), requestID, interaction); err != nil {
		t.Fatalf("advisory index failure became fatal: %v", err)
	}
	if got := client.LLen(t.Context(), store.interactionsKey(requestID)).Val(); got != 1 {
		t.Fatalf("authoritative interaction count = %d, want 1", got)
	}
	hook.mu.Lock()
	attempts := hook.attempts
	hook.enabled = false
	hook.mu.Unlock()
	if attempts != layer1MaxRetries {
		t.Fatalf("index attempts = %d, want %d", attempts, layer1MaxRetries)
	}
	if len(logger.warns) != 1 {
		t.Fatalf("index warnings = %d, want 1", len(logger.warns))
	}
	fields := logger.warns[0].fields
	if fields["operation"] != "llm_debug_recent_index" ||
		fields["request_id"] != requestID ||
		fields["error"] != "redis LLM debug index update failed" ||
		fields["error_type"] != "index_write" ||
		fields["failure_class"] != "redis_backend_failure" {
		t.Fatalf("index warning fields = %#v", fields)
	}

	if err := store.RecordInteraction(t.Context(), requestID, interaction); err != nil {
		t.Fatal(err)
	}
	if got := client.LLen(t.Context(), store.interactionsKey(requestID)).Val(); got != 2 {
		t.Fatalf("authoritative interaction count after repair = %d, want 2", got)
	}
	if score, err := client.ZScore(t.Context(), store.indexKey(), requestID).Result(); err != nil || score == 0 {
		t.Fatalf("later write did not repair recent index: score=%v err=%v", score, err)
	}
}

func TestRedisDebugStoreRetryLogsAreBoundedAndRequestCorrelated(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("retry-observation")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("execution", func(t *testing.T) {
		logger := &TestLogger{}
		store, err := NewRedisExecutionDebugStoreWithClient(
			client,
			DefaultExecutionStoreConfig(),
			WithExecutionDebugKeyspace(keyspace),
			WithExecutionDebugLogger(logger),
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := core.WithRequestID(t.Context(), "execution-retry-request")
		if err := store.executeWithRetry(ctx, func() error {
			return errors.New("redis://user:secret@execution.invalid/0")
		}); err == nil {
			t.Fatal("retry unexpectedly succeeded")
		}
		assertBoundedRedisRetryLogs(t, logger, "execution_store_retry", "execution-retry-request", "redis execution debug operation failed")
	})

	t.Run("llm", func(t *testing.T) {
		logger := &TestLogger{}
		store, err := NewRedisLLMDebugStoreWithClient(
			client,
			WithDebugKeyspace(keyspace),
			WithDebugLogger(logger),
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := core.WithRequestID(t.Context(), "llm-retry-request")
		if err := store.executeWithRetry(ctx, func() error {
			return errors.New("redis://user:secret@llm.invalid/0")
		}); err == nil {
			t.Fatal("retry unexpectedly succeeded")
		}
		assertBoundedRedisRetryLogs(t, logger, "llm_debug_store_retry", "llm-retry-request", "redis LLM debug operation failed")
	})
}

func assertBoundedRedisRetryLogs(t *testing.T, logger *TestLogger, operation, requestID, message string) {
	t.Helper()
	logs := logger.GetLogsByOperation(operation)
	if len(logs) != 3 {
		t.Fatalf("%s logs = %d, want 3", operation, len(logs))
	}
	for _, entry := range logs {
		fields := entry.Fields
		if fields["request_id"] != requestID || fields["error"] != message ||
			fields["error_type"] != "backend" || fields["failure_class"] != "redis_backend_failure" {
			t.Fatalf("%s fields = %#v", operation, fields)
		}
		encoded := fmt.Sprint(fields)
		if strings.Contains(encoded, "secret") || strings.Contains(encoded, "redis://") {
			t.Fatalf("%s exposed Redis error text: %s", operation, encoded)
		}
	}
}
