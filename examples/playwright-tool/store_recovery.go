package main

import (
	"context"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

const testStoreRetryInterval = 30 * time.Second

type testStoreFactory func() (*TestStore, error)

// testStoreRecovery is a one-shot, optional startup repair job. Framework owns
// its goroutine and cancellation; the application owns the installed client.
type testStoreRecovery struct {
	tool        *PlaywrightTool
	factory     testStoreFactory
	interval    time.Duration
	wait        func(context.Context, time.Duration) bool
	onFailure   func(error)
	onRecovered func()
}

var _ core.Runnable = (*testStoreRecovery)(nil)

func (j *testStoreRecovery) Start(ctx context.Context) error {
	interval := j.interval
	if interval <= 0 {
		interval = testStoreRetryInterval
	}
	wait := j.wait
	if wait == nil {
		wait = waitForTestStoreRetry
	}
	if j.tool.testStore() != nil {
		return nil
	}
	for ctx.Err() == nil {
		if !wait(ctx, interval) || ctx.Err() != nil {
			return nil
		}
		// NewTestStore uses the shared bounded startup PING, but has no
		// cancellation parameter. This synchronous attempt may outlast the
		// framework's runnable drain. Never detach a second factory goroutine;
		// the ownership guard also rejects results after application cleanup.
		store, err := j.factory()
		if ctx.Err() != nil {
			_ = store.Close()
			return nil
		}
		if err != nil {
			_ = store.Close()
			if j.onFailure != nil {
				j.onFailure(err)
			}
			continue
		}
		if !j.tool.installTestStore(ctx, store) {
			_ = store.Close()
			return nil
		}
		if j.onRecovered != nil {
			j.onRecovered()
		}
		return nil
	}
	return nil
}

func waitForTestStoreRetry(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
