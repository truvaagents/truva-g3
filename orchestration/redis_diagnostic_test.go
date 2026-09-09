package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestClassifyRedisDiagnosticUsesFixedVocabulary(t *testing.T) {
	const backendText = "redis://user:unit-test-secret@private.invalid/0 payload=private"
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "none"},
		{"canceled", context.Canceled, "canceled"},
		{"wrapped canceled", fmt.Errorf("%s: %w", backendText, context.Canceled), "canceled"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"wrapped deadline", fmt.Errorf("%s: %w", backendText, context.DeadlineExceeded), "timeout"},
		{"network timeout", &net.DNSError{Err: backendText, IsTimeout: true}, "timeout"},
		{"wrapped network timeout", fmt.Errorf("%s: %w", backendText, &net.DNSError{IsTimeout: true}), "timeout"},
		{"network failure", &net.DNSError{Err: backendText}, "redis_backend_failure"},
		{"redis nil", redis.Nil, "redis_backend_failure"},
		{"backend text", errors.New(backendText), "redis_backend_failure"},
		{"cancellation takes precedence", errors.Join(context.DeadlineExceeded, context.Canceled), "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyRedisDiagnostic(test.err); got != test.want {
				t.Fatalf("classification = %q, want %q", got, test.want)
			}
		})
	}
}
