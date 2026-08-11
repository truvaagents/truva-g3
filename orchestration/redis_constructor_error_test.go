package orchestration

import (
	"errors"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
)

func TestRedisDebugConstructorsPreserveSanitizedConnectionCause(t *testing.T) {
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
				!strings.Contains(err.Error(), "api_key=[REDACTED]") {
				t.Fatalf("constructor error was not sanitized: %q", err.Error())
			}
			var redisError redis.Error
			if !errors.As(err, &redisError) {
				t.Fatalf("constructor error does not preserve Redis cause: %T: %v", err, err)
			}
		})
	}
}
