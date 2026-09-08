package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type stalledStartupPingClient struct {
	redis.UniversalClient
	release  chan struct{}
	finished chan struct{}
}

func (c *stalledStartupPingClient) Ping(ctx context.Context) *redis.StatusCmd {
	<-c.release // Deliberately ignores ctx, like a client without ContextTimeoutEnabled.
	defer close(c.finished)
	return redis.NewStatusResult("PONG", nil)
}
func TestBorrowedClientStartupDeadlineDoesNotRequireContextTimeoutEnabled(t *testing.T) {
	server := miniredis.RunT(t)
	borrowed := redis.NewClient(&redis.Options{Addr: server.Addr(), ContextTimeoutEnabled: false, ReadTimeout: 8 * time.Second})
	defer borrowed.Close()
	client := &stalledStartupPingClient{UniversalClient: borrowed, release: make(chan struct{}), finished: make(chan struct{})}
	defer func() { close(client.release); <-client.finished }()
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			keys, err := core.NewRedisKeyspace("startup-test")
			if err != nil {
				return err
			}
			_, err = NewRedisLLMCallRecorderWithClient(client, keys)
			return err
		}()
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("startup error = %v, want deadline exceeded", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("constructor exceeded its five-second startup deadline")
	}
	if borrowed.Options().ContextTimeoutEnabled || borrowed.Options().ReadTimeout != 8*time.Second {
		t.Fatal("constructor changed borrowed client options")
	}
	if err := borrowed.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("constructor closed borrowed client: %v", err)
	}
}
