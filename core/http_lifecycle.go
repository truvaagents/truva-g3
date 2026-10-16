package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const defaultHTTPShutdownTimeout = 10 * time.Second

// httpLifecycle owns one server generation. Its cancellation waiter is joined
// before serve returns; it is not an independent background job. Explicit and
// context-driven shutdown share one operation. Callers can cancel their own
// wait without interrupting that operation or letting serve return early.
type httpLifecycle struct {
	server   *http.Server
	timeout  time.Duration
	shutdown func(context.Context) error
	once     sync.Once
	done     chan struct{}
	err      error
}

func httpShutdownTimeout(configured time.Duration) (time.Duration, error) {
	if configured < 0 {
		return 0, fmt.Errorf("HTTP shutdown timeout must be positive (zero selects the default): %w", ErrInvalidConfiguration)
	}
	if configured == 0 {
		return defaultHTTPShutdownTimeout, nil
	}
	return configured, nil
}

func (l *httpLifecycle) stop(ctx context.Context) error {
	l.once.Do(func() {
		l.done = make(chan struct{})
		// Preserve correlation, but give the shared drain its configured budget
		// regardless of which caller initiates it. serve joins this operation.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.timeout)
		go func() {
			defer cancel()
			l.err = l.shutdown(shutdownCtx)
			close(l.done)
		}()
	})
	// Prefer an already-completed result, including when this caller's
	// context has also expired. Closing done publishes err to all waiters.
	select {
	case <-l.done:
		return l.err
	default:
	}
	select {
	case <-l.done:
		return l.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *httpLifecycle) serve(ctx context.Context) error {
	serveDone := make(chan struct{})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		select {
		case <-ctx.Done():
		case <-serveDone:
		}
		// Keep lifecycle correlation, but give shutdown its own bounded budget.
		// No application-owned clients or telemetry providers are closed here.
		_ = l.stop(context.WithoutCancel(ctx))
	}()

	err := l.server.ListenAndServe()
	close(serveDone)
	<-drained
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, l.err)
}
