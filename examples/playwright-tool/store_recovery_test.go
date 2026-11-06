package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/truvaagents/truva-g3/core"
)

type closeTrackingRedis struct {
	redis.UniversalClient
	closes atomic.Int32
	err    error
}

func (c *closeTrackingRedis) Close() error {
	c.closes.Add(1)
	if c.UniversalClient != nil {
		return c.UniversalClient.Close()
	}
	return c.err
}

func trackedTestStore() (*TestStore, *closeTrackingRedis) {
	client := &closeTrackingRedis{}
	return &TestStore{rdb: client}, client
}

func immediateTestStoreRetry(context.Context, time.Duration) bool { return true }

func TestTestStoreRecoveryAfterTransientFailures(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, -time.Second, time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			tool := &PlaywrightTool{}
			store, client := trackedTestStore()
			t.Cleanup(func() { _ = tool.closeTestStore() })
			attempts, failures, recoveries := 0, 0, 0
			var waits []time.Duration
			job := &testStoreRecovery{
				tool: tool, interval: interval,
				wait: func(_ context.Context, duration time.Duration) bool {
					waits = append(waits, duration)
					return true
				},
				factory: func() (*TestStore, error) {
					attempts++
					if attempts < 3 {
						return nil, errors.New("temporarily unavailable")
					}
					return store, nil
				},
				onFailure:   func(error) { failures++ },
				onRecovered: func() { recoveries++ },
			}
			if err := job.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			if attempts != 3 || failures != 2 || recoveries != 1 || len(waits) != 3 {
				t.Fatalf("attempts/failures/recoveries/waits = %d/%d/%d/%d", attempts, failures, recoveries, len(waits))
			}
			wantInterval := interval
			if wantInterval <= 0 {
				wantInterval = 30 * time.Second
			}
			for _, got := range waits {
				if got != wantInterval {
					t.Fatalf("retry delay = %s, want %s", got, wantInterval)
				}
			}
			if tool.testStore() != store || client.closes.Load() != 0 {
				t.Fatal("successful client was not retained")
			}
		})
	}
}

func TestTestStoreRecoverySkipsExistingStore(t *testing.T) {
	t.Parallel()
	tool := &PlaywrightTool{}
	store, _ := trackedTestStore()
	if !tool.installTestStore(context.Background(), store) {
		t.Fatal("initial installation failed")
	}
	defer tool.closeTestStore()
	job := &testStoreRecovery{
		tool: tool,
		wait: func(context.Context, time.Duration) bool {
			t.Fatal("initial success must not wait/retry")
			return false
		},
	}
	if err := job.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTestStoreRecoveryCancellationBeforeFactory(t *testing.T) {
	t.Parallel()
	for _, cancelBeforeWait := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_start", false: "ready_tick"}[cancelBeforeWait], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelBeforeWait {
				cancel()
			}
			job := &testStoreRecovery{
				tool: &PlaywrightTool{},
				wait: func(context.Context, time.Duration) bool { cancel(); return true },
				factory: func() (*TestStore, error) {
					t.Fatal("factory called after cancellation")
					return nil, nil
				},
			}
			if err := job.Start(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestStoreRetryWaitCancels(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- waitForTestStoreRetry(ctx, time.Hour) }()
	cancel()
	select {
	case retried := <-done:
		if retried {
			t.Fatal("cancelled wait requested a retry")
		}
	case <-time.After(time.Second):
		t.Fatal("retry wait ignored cancellation")
	}
}

func TestTestStoreRecoveryClosesLateCandidate(t *testing.T) {
	t.Parallel()
	for _, cancelFactory := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancelled_factory", false: "stopped_owner"}[cancelFactory], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tool := &PlaywrightTool{}
			store, client := trackedTestStore()
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var recoveries atomic.Int32
			job := &testStoreRecovery{
				tool: tool, wait: immediateTestStoreRetry,
				factory: func() (*TestStore, error) {
					close(started)
					<-release
					return store, nil
				},
				onRecovered: func() { recoveries.Add(1) },
			}
			go func() { done <- job.Start(ctx) }()
			<-started
			if cancelFactory {
				cancel()
			}
			if err := tool.closeTestStore(); err != nil {
				t.Fatal(err)
			}
			// The shared constructor cannot be interrupted: cancellation does
			// not pretend to join it. Cleanup still closes the publication gate.
			select {
			case <-done:
				t.Fatal("synchronous factory returned before release")
			default:
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery did not finish after factory returned")
			}
			if tool.testStore() != nil || client.closes.Load() != 1 || recoveries.Load() != 0 {
				t.Fatalf("late store published or leaked: closes=%d recoveries=%d", client.closes.Load(), recoveries.Load())
			}
		})
	}
}

func TestTestStoreCloseIsNilSafeAndIdempotent(t *testing.T) {
	t.Parallel()
	var absent *TestStore
	if err := absent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (&TestStore{}).Close(); err != nil {
		t.Fatal(err)
	}
	tool := &PlaywrightTool{}
	store, client := trackedTestStore()
	wantErr := errors.New("close failure")
	client.err = wantErr
	if !tool.installTestStore(context.Background(), store) {
		t.Fatal("installation failed")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := tool.closeTestStore(); !errors.Is(err, wantErr) {
				t.Errorf("close error = %v, want %v", err, wantErr)
			}
		})
	}
	wg.Wait()
	if err := store.Close(); !errors.Is(err, wantErr) || client.closes.Load() != 1 {
		t.Fatalf("client close calls=%d error=%v", client.closes.Load(), err)
	}
	rejected, rejectedClient := trackedTestStore()
	if tool.installTestStore(context.Background(), rejected) {
		t.Fatal("stopped ownership reopened")
	}
	_ = rejected.Close()
	if rejectedClient.closes.Load() != 1 {
		t.Fatal("rejected candidate not closed")
	}
}

func TestTestStoreConcurrentInstallReadAndClose(t *testing.T) {
	t.Parallel()
	for range 100 {
		tool := &PlaywrightTool{}
		store, client := trackedTestStore()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			if !tool.installTestStore(context.Background(), store) {
				_ = store.Close()
			}
		})
		wg.Go(func() { <-start; _ = tool.closeTestStore() })
		wg.Go(func() {
			<-start
			for range 100 {
				_ = tool.testStore()
			}
		})
		close(start)
		wg.Wait()
		if tool.testStore() != nil || client.closes.Load() != 1 {
			t.Fatalf("concurrent cleanup leaked/reinstalled client: closes=%d", client.closes.Load())
		}
	}
}

type storeInstallingBody struct {
	io.Reader
	once    sync.Once
	install func()
}

func (b *storeInstallingBody) Read(p []byte) (int, error) {
	b.once.Do(b.install)
	return b.Reader.Read(p)
}

func TestRecoveredStoreQueriesAndOperationSnapshot(t *testing.T) {
	t.Parallel()
	server := miniredis.RunT(t)
	connection := core.DefaultRedisConnectionConfig()
	connection.Addrs = []string{server.Addr()}
	keyspace, err := core.NewRedisKeyspace("recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewTestStore(connection, keyspace)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.IndexRun(ctx, RunMetadata{
		RunID: "recovered-run", Site: "example.test", Status: "passed", Timestamp: "2026-11-04T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveScriptRef(ctx, "example.test", ScriptMetadata{Name: "smoke", Version: 1}); err != nil {
		t.Fatal(err)
	}
	tool := &PlaywrightTool{BaseTool: core.NewTool("store-recovery-test")}
	defer tool.closeTestStore()
	for _, handler := range []http.HandlerFunc{tool.handleGetResults, tool.handleLookupScripts} {
		rw := httptest.NewRecorder()
		handler(rw, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"hostname":"example.test"}`)))
		if rw.Code != http.StatusServiceUnavailable || !strings.Contains(rw.Body.String(), "STORE_UNAVAILABLE") {
			t.Fatalf("nil store changed unavailable response: %d %s", rw.Code, rw.Body.String())
		}
	}
	job := &testStoreRecovery{tool: tool, wait: immediateTestStoreRetry, factory: func() (*TestStore, error) { return store, nil }}
	// Recovery during decoding cannot change the snapshot of this operation.
	body := &storeInstallingBody{Reader: strings.NewReader(`{}`), install: func() {
		if err := job.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}}
	rw := httptest.NewRecorder()
	tool.handleGetResults(rw, httptest.NewRequest(http.MethodPost, "/", body))
	if rw.Code != http.StatusServiceUnavailable || tool.testStore() != store {
		t.Fatalf("operation snapshot changed midway: status=%d", rw.Code)
	}
	rw = httptest.NewRecorder()
	tool.handleGetResults(rw, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"site":"example.test"}`)))
	var response struct {
		Success bool          `json:"success"`
		Data    []RunMetadata `json:"data"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rw.Code != http.StatusOK || !response.Success || len(response.Data) != 1 || response.Data[0].RunID != "recovered-run" {
		t.Fatalf("recovered run query failed: %d %s", rw.Code, rw.Body.String())
	}
	rw = httptest.NewRecorder()
	tool.handleLookupScripts(rw, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"hostname":"example.test"}`)))
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"name":"smoke"`) {
		t.Fatalf("recovered script lookup failed: %d %s", rw.Code, rw.Body.String())
	}
}
