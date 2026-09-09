package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type redisStoreComponentLogger struct {
	core.NoOpLogger
	component string
}

func (logger *redisStoreComponentLogger) WithComponent(component string) core.Logger {
	logger.component = component
	return logger
}

func TestDirectRedisStoresScopeComponentAwareLoggers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("logger-scope")
	if err != nil {
		t.Fatal(err)
	}

	executionLogger := &redisStoreComponentLogger{}
	execution, err := NewRedisExecutionDebugStoreWithClient(
		client,
		DefaultExecutionStoreConfig(),
		WithExecutionDebugKeyspace(keyspace),
		WithExecutionDebugLogger(executionLogger),
	)
	if err != nil {
		t.Fatal(err)
	}
	if execution.logger != executionLogger || executionLogger.component != "framework/orchestration" {
		t.Fatalf("execution logger = %#v, component = %q", execution.logger, executionLogger.component)
	}

	llmLogger := &redisStoreComponentLogger{}
	llm, err := NewRedisLLMDebugStoreWithClient(
		client,
		WithDebugKeyspace(keyspace),
		WithDebugLogger(llmLogger),
	)
	if err != nil {
		t.Fatal(err)
	}
	if llm.logger != llmLogger || llmLogger.component != "framework/orchestration" {
		t.Fatalf("LLM logger = %#v, component = %q", llm.logger, llmLogger.component)
	}

	scheduleLogger := &redisStoreComponentLogger{}
	schedules, err := NewRedisScheduleStore(client, &RedisScheduleStoreConfig{
		Keyspace: &keyspace,
		Logger:   scheduleLogger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if schedules.logger != scheduleLogger || scheduleLogger.component != "framework/orchestration" {
		t.Fatalf("schedule logger = %#v, component = %q", schedules.logger, scheduleLogger.component)
	}
}

func TestRedisConstructorsPreserveCredentialSafeConnectionCause(t *testing.T) {
	tests := []struct {
		name      string
		construct func(string) error
	}{
		{
			name: "HITL command",
			construct: func(redisURL string) error {
				_, err := NewRedisCommandStore(WithCommandStoreRedisURL(redisURL))
				return err
			},
		},
		{
			name: "HITL checkpoint",
			construct: func(redisURL string) error {
				_, err := NewRedisCheckpointStore(WithCheckpointRedisURL(redisURL))
				return err
			},
		},
		{
			name: "execution debug",
			construct: func(redisURL string) error {
				_, err := NewRedisExecutionDebugStoreWithConfig(
					DefaultExecutionStoreConfig(),
					WithExecutionDebugRedisURL(redisURL),
				)
				return err
			},
		},
		{
			name: "LLM debug",
			construct: func(redisURL string) error {
				_, err := NewRedisLLMDebugStore(WithDebugRedisURL(redisURL))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			server.SetError("ERR backend rejected api_key=top-secret")

			err := test.construct("redis://" + server.Addr())
			if err == nil {
				t.Fatal("constructor succeeded despite Redis error")
			}
			if strings.Contains(err.Error(), "top-secret") ||
				strings.Contains(err.Error(), "api_key") ||
				!strings.Contains(err.Error(), "startup check failed") {
				t.Fatalf("constructor error was not bounded: %q", err.Error())
			}
			var redisError redis.Error
			if !errors.As(err, &redisError) {
				t.Fatalf("constructor error does not preserve Redis cause: %T: %v", err, err)
			}
		})
	}
}

func TestInjectedRedisAdaptersDoNotCloseApplicationClient(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("ownership-test")
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewRedisExecutionDebugStoreWithClient(client, DefaultExecutionStoreConfig(), WithExecutionDebugKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	llm, err := NewRedisLLMDebugStoreWithClient(client, WithDebugKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewRedisCheckpointStoreWithClient(client, WithCheckpointKeyspace(keyspace, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewRedisCommandStoreWithClient(client, WithCommandStoreKeyspace(keyspace, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, closer := range []interface{ Close() error }{execution, llm, checkpoint, commands} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("injected client was closed: %v", err)
	}
}

func TestClusterAdaptersAcceptTypedKeyspaces(t *testing.T) {
	client := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:1"}})
	t.Cleanup(func() { _ = client.Close() })
	keyspace, err := core.NewRedisKeyspace("cluster-prefix-test")
	if err != nil {
		t.Fatal(err)
	}

	checkpoint, err := NewRedisCheckpointStoreWithClient(client, WithCheckpointKeyspace(keyspace, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	commands, err := NewRedisCommandStoreWithClient(client, WithCommandStoreKeyspace(keyspace, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewRedisExecutionDebugStoreWithClient(
		client,
		DefaultExecutionStoreConfig(),
		WithExecutionDebugKeyspace(keyspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	llm, err := NewRedisLLMDebugStoreWithClient(client, WithDebugKeyspace(keyspace))
	if err != nil {
		t.Fatal(err)
	}
	schedules, err := NewRedisScheduleStore(client, &RedisScheduleStoreConfig{Keyspace: &keyspace})
	if err != nil {
		t.Fatal(err)
	}
	for _, closer := range []interface{ Close() error }{checkpoint, commands, execution, llm} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	_ = schedules
}

func TestOwningRedisDebugStoresUseEnvironmentKeyspace(t *testing.T) {
	server := miniredis.RunT(t)
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "debug-deployment")
	keyspace, err := core.NewRedisKeyspace("debug-deployment")
	if err != nil {
		t.Fatal(err)
	}

	execution, err := NewRedisExecutionDebugStore(
		WithExecutionDebugRedisURL("redis://" + server.Addr()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Close() })
	if got, want := execution.keys.Record("request-1"), keyspace.Tagged("execution-debug", "request-1", "record"); got != want {
		t.Fatalf("execution key = %q, want %q", got, want)
	}

	llm, err := NewRedisLLMDebugStore(
		WithDebugRedisURL("redis://" + server.Addr()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = llm.Close() })
	if got, want := llm.metaKey("request-1"), keyspace.Tagged("llm-debug", "request-1", "meta"); got != want {
		t.Fatalf("LLM debug key = %q, want %q", got, want)
	}
}

func TestOwningRedisDebugStoresRejectInvalidEnvironmentKeyspace(t *testing.T) {
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "invalid{namespace")
	tests := []struct {
		name      string
		construct func() error
	}{
		{
			name: "execution debug",
			construct: func() error {
				_, err := NewRedisExecutionDebugStore()
				return err
			},
		},
		{
			name: "LLM debug",
			construct: func() error {
				_, err := NewRedisLLMDebugStore()
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.construct()
			if !errors.Is(err, core.ErrInvalidConfiguration) {
				t.Fatalf("constructor error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func TestExplicitRedisDebugKeyspaceOverridesEnvironment(t *testing.T) {
	server := miniredis.RunT(t)
	t.Setenv("TRUVAG3_REDIS_NAMESPACE", "invalid{namespace")
	keyspace, err := core.NewRedisKeyspace("explicit-deployment")
	if err != nil {
		t.Fatal(err)
	}

	execution, err := NewRedisExecutionDebugStore(
		WithExecutionDebugRedisURL("redis://"+server.Addr()),
		WithExecutionDebugKeyspace(keyspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Close() })

	llm, err := NewRedisLLMDebugStore(
		WithDebugRedisURL("redis://"+server.Addr()),
		WithDebugKeyspace(keyspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = llm.Close() })
}
