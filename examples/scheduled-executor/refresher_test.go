package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

// fakeCatalog implements refreshableCatalog for testing.
type fakeCatalog struct {
	mu         sync.Mutex
	refreshN   int
	refreshErr error
	agents     map[string]*orchestration.AgentInfo
}

func (f *fakeCatalog) Refresh(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshN++
	return f.refreshErr
}

func (f *fakeCatalog) GetAgentsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.agents)
}

func TestCatalogRefresher_TicksAndRefreshes(t *testing.T) {
	cat := &fakeCatalog{agents: map[string]*orchestration.AgentInfo{
		"a": {Registration: &core.ServiceInfo{Name: "a"}},
	}}

	r := &catalogRefresher{
		catalog:  cat,
		logger:   &core.NoOpLogger{},
		interval: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	<-ctx.Done()
	err := <-done
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	cat.mu.Lock()
	n := cat.refreshN
	cat.mu.Unlock()
	if n < 2 {
		t.Errorf("expected at least 2 refresh calls in 200ms with 50ms interval, got %d", n)
	}
}

func TestCatalogRefresher_RefreshError_ContinuesTicking(t *testing.T) {
	cat := &fakeCatalog{
		refreshErr: errors.New("redis down"),
		agents:     map[string]*orchestration.AgentInfo{},
	}

	r := &catalogRefresher{
		catalog:  cat,
		logger:   &core.NoOpLogger{},
		interval: 50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	<-ctx.Done()
	err := <-done
	if err != nil {
		t.Fatalf("Start should return nil on ctx cancel, got: %v", err)
	}

	cat.mu.Lock()
	n := cat.refreshN
	cat.mu.Unlock()
	if n < 2 {
		t.Errorf("expected at least 2 refresh attempts despite errors, got %d", n)
	}
}

func TestCatalogRefresher_GracefulShutdown(t *testing.T) {
	cat := &fakeCatalog{agents: map[string]*orchestration.AgentInfo{}}

	r := &catalogRefresher{
		catalog:  cat,
		logger:   &core.NoOpLogger{},
		interval: 10 * time.Second, // long interval — exits via ctx cancel, not tick
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start should return nil on cancel, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s after ctx cancel")
	}
}
