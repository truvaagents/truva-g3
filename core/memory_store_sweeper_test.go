package core

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// componentAwareMockLogger captures records AND records WithComponent calls.
// Used to verify that NewMemoryStoreSweeper wraps the logger with
// "framework/core" per docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md §14.
type componentAwareMockLogger struct {
	mu                 sync.Mutex
	component          string
	withComponentCalls []string
	entries            []LogEntry
	parent             *componentAwareMockLogger // points to the original; set on children
}

func newComponentAwareMockLogger() *componentAwareMockLogger {
	return &componentAwareMockLogger{}
}

func (m *componentAwareMockLogger) WithComponent(name string) Logger {
	m.mu.Lock()
	m.withComponentCalls = append(m.withComponentCalls, name)
	m.mu.Unlock()
	// Return a child logger that records back to this parent so a single
	// instance can capture both the wrapping calls and the post-wrap log
	// records.
	child := &componentAwareMockLogger{component: name, parent: m}
	return child
}

func (m *componentAwareMockLogger) record(level, msg string, fields map[string]interface{}) {
	root := m
	if m.parent != nil {
		root = m.parent
	}
	root.mu.Lock()
	root.entries = append(root.entries, LogEntry{Level: level, Message: msg, Fields: fields})
	root.mu.Unlock()
}

func (m *componentAwareMockLogger) Debug(msg string, fields map[string]interface{}) {
	m.record("debug", msg, fields)
}
func (m *componentAwareMockLogger) Info(msg string, fields map[string]interface{}) {
	m.record("info", msg, fields)
}
func (m *componentAwareMockLogger) Warn(msg string, fields map[string]interface{}) {
	m.record("warn", msg, fields)
}
func (m *componentAwareMockLogger) Error(msg string, fields map[string]interface{}) {
	m.record("error", msg, fields)
}
func (m *componentAwareMockLogger) DebugWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	m.record("debug", msg, fields)
}
func (m *componentAwareMockLogger) InfoWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	m.record("info", msg, fields)
}
func (m *componentAwareMockLogger) WarnWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	m.record("warn", msg, fields)
}
func (m *componentAwareMockLogger) ErrorWithContext(_ context.Context, msg string, fields map[string]interface{}) {
	m.record("error", msg, fields)
}

func (m *componentAwareMockLogger) snapshot() (calls []string, entries []LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls = append([]string(nil), m.withComponentCalls...)
	entries = append([]LogEntry(nil), m.entries...)
	return
}

// recordingTelemetry mocks core.Telemetry, recording every StartSpan call.
type recordingTelemetry struct {
	mu     sync.Mutex
	starts []string // span names, in order
	spans  []*recordingSpan
}

func (r *recordingTelemetry) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, name)
	s := &recordingSpan{name: name, attrs: map[string]interface{}{}}
	r.spans = append(r.spans, s)
	return ctx, s
}

func (r *recordingTelemetry) RecordMetric(_ string, _ float64, _ map[string]string) {}

func (r *recordingTelemetry) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

type recordingSpan struct {
	name  string
	attrs map[string]interface{}
	ended bool
}

func (s *recordingSpan) End()                                       { s.ended = true }
func (s *recordingSpan) SetAttribute(key string, value interface{}) { s.attrs[key] = value }
func (s *recordingSpan) RecordError(_ error)                        {}

// failingOption is an Option that always returns an error — exercises the
// constructor's option-validation error path.
func failingOption(msg string) MemoryStoreSweeperOption {
	return func(s *MemoryStoreSweeper) error {
		return &simpleErr{msg: msg}
	}
}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// findEntry returns the first LogEntry whose Message matches msgSubstring
// (case-sensitive substring match). Returns nil if not found.
func findEntry(entries []LogEntry, msgSubstring string) *LogEntry {
	for i := range entries {
		if strings.Contains(entries[i].Message, msgSubstring) {
			return &entries[i]
		}
	}
	return nil
}

// ---- NewMemoryStoreSweeper construction ----

func TestNewMemoryStoreSweeper_NilLoggerDefaultsToNoOp(t *testing.T) {
	store := NewMemoryStore()
	sweeper, err := NewMemoryStoreSweeper(store, time.Minute, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sweeper == nil {
		t.Fatal("expected non-nil sweeper")
	}
	if sweeper.logger == nil {
		t.Fatal("expected logger to be defaulted to NoOpLogger, got nil")
	}
	if _, ok := sweeper.logger.(*NoOpLogger); !ok {
		t.Fatalf("expected *NoOpLogger when nil passed, got %T", sweeper.logger)
	}
}

func TestNewMemoryStoreSweeper_WrapsComponentAwareLoggerToFrameworkCore(t *testing.T) {
	mockLogger := newComponentAwareMockLogger()
	store := NewMemoryStore()
	_, err := NewMemoryStoreSweeper(store, time.Minute, mockLogger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls, _ := mockLogger.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one WithComponent call, got %d: %v", len(calls), calls)
	}
	if calls[0] != "framework/core" {
		t.Errorf("WithComponent called with %q, want %q", calls[0], "framework/core")
	}
}

func TestNewMemoryStoreSweeper_PropagatesOptionError(t *testing.T) {
	store := NewMemoryStore()
	_, err := NewMemoryStoreSweeper(store, time.Minute, &NoOpLogger{}, failingOption("boom"))
	if err == nil {
		t.Fatal("expected error from failing option, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error to wrap 'boom', got: %v", err)
	}
	if !strings.Contains(err.Error(), "memory store sweeper option") {
		t.Errorf("expected error message to mention 'memory store sweeper option', got: %v", err)
	}
}

func TestNewMemoryStoreSweeper_TelemetryOptionStored(t *testing.T) {
	store := NewMemoryStore()
	tel := &recordingTelemetry{}
	sweeper, err := NewMemoryStoreSweeper(store, time.Minute, &NoOpLogger{}, WithMemoryStoreSweeperTelemetry(tel))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sweeper.telemetry != tel {
		t.Errorf("telemetry was not stored on sweeper")
	}
}

// ---- Start: deletion behavior ----

func TestMemoryStoreSweeper_StartDeletesExpiredEntries(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Populate entries with very short TTLs so they're expired by sweep time.
	for i := 0; i < 5; i++ {
		_ = store.Set(ctx, fmt.Sprintf("k%d", i), "v", 5*time.Millisecond)
	}
	// Wait for entries to expire.
	time.Sleep(20 * time.Millisecond)

	sweeper, err := NewMemoryStoreSweeper(store, 10*time.Millisecond, &NoOpLogger{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	// Allow at least 2 ticks to fire so we know the sweep ran.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned non-nil error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within drain timeout after ctx cancel")
	}

	// Verify the underlying map is empty.
	store.mu.RLock()
	mapLen := len(store.store)
	store.mu.RUnlock()
	if mapLen != 0 {
		t.Errorf("expected underlying map length 0 after sweep, got %d", mapLen)
	}
}

// ---- Start: ctx cancellation contract ----

func TestMemoryStoreSweeper_HonorsCtxCancellation(t *testing.T) {
	store := NewMemoryStore()
	sweeper, _ := NewMemoryStoreSweeper(store, time.Hour, &NoOpLogger{}) // long interval — ticker should never fire

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	// Give Start a moment to begin, then cancel.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned non-nil error on ctx cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within drain timeout after ctx cancel")
	}
}

// ---- Start: no-op cases (interval ≤ 0, nil store) ----

func TestMemoryStoreSweeper_NoOp_ZeroInterval(t *testing.T) {
	store := NewMemoryStore()
	sweeper, _ := NewMemoryStoreSweeper(store, 0, &NoOpLogger{})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	startTime := time.Now()
	go func() { done <- sweeper.Start(runCtx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned non-nil error: %v", err)
		}
		// Should have returned promptly after ctx cancel — no ticker work.
		if elapsed := time.Since(startTime); elapsed > 500*time.Millisecond {
			t.Errorf("zero-interval sweeper took too long to return: %v", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within drain timeout")
	}
}

func TestMemoryStoreSweeper_NoOp_NegativeInterval(t *testing.T) {
	store := NewMemoryStore()
	sweeper, _ := NewMemoryStoreSweeper(store, -1*time.Second, &NoOpLogger{})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned non-nil error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within drain timeout")
	}
}

func TestMemoryStoreSweeper_NoOp_NilStore(t *testing.T) {
	sweeper, _ := NewMemoryStoreSweeper(nil, time.Millisecond, &NoOpLogger{})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned non-nil error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within drain timeout")
	}
}

// ---- Logging compliance per docs/observability/LOGGING_IMPLEMENTATION_GUIDE.md §16 ----

func TestMemoryStoreSweeper_LoggingCompliance(t *testing.T) {
	mockLogger := newComponentAwareMockLogger()
	store := NewMemoryStore()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = store.Set(ctx, fmt.Sprintf("k%d", i), "v", 5*time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	sweeper, _ := NewMemoryStoreSweeper(store, 10*time.Millisecond, mockLogger)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	_, entries := mockLogger.snapshot()

	// Lifecycle "started" log
	startedEntry := findEntry(entries, "Memory sweeper started")
	if startedEntry == nil {
		t.Fatal("expected 'Memory sweeper started' log entry")
	}
	if op, _ := startedEntry.Fields["operation"].(string); op != "memory_sweeper" {
		t.Errorf("started log: operation = %q, want %q", op, "memory_sweeper")
	}

	// Per-pass log with sweep_id, deleted_count, duration_ms, status, operation
	passEntry := findEntry(entries, "Memory sweep pass completed")
	if passEntry == nil {
		t.Fatal("expected 'Memory sweep pass completed' log entry")
	}
	if op, _ := passEntry.Fields["operation"].(string); op != "memory_sweep_pass" {
		t.Errorf("pass log: operation = %q, want %q", op, "memory_sweep_pass")
	}
	if sweepID, _ := passEntry.Fields["sweep_id"].(string); !strings.HasPrefix(sweepID, "sweep-") {
		t.Errorf("pass log: sweep_id = %q, want prefix 'sweep-'", sweepID)
	}
	if _, ok := passEntry.Fields["deleted_count"]; !ok {
		t.Error("pass log: missing deleted_count field")
	}
	if _, ok := passEntry.Fields["duration_ms"]; !ok {
		t.Error("pass log: missing duration_ms field")
	}
	if status, _ := passEntry.Fields["status"].(string); status != "success" {
		t.Errorf("pass log: status = %q, want %q", status, "success")
	}

	// Shutdown log
	stoppingEntry := findEntry(entries, "Memory sweeper stopping (context cancelled)")
	if stoppingEntry == nil {
		t.Error("expected 'Memory sweeper stopping (context cancelled)' log entry")
	}
}

// ---- Span creation when telemetry is set ----

func TestMemoryStoreSweeper_CreatesSpanPerPass_WhenTelemetrySet(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_ = store.Set(ctx, "key1", "v", 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	tel := &recordingTelemetry{}
	sweeper, _ := NewMemoryStoreSweeper(store, 10*time.Millisecond, &NoOpLogger{}, WithMemoryStoreSweeperTelemetry(tel))

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	// Allow at least two ticks.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if tel.startCount() < 1 {
		t.Fatalf("expected at least one StartSpan call, got %d", tel.startCount())
	}
	for _, name := range tel.starts {
		if name != "memory.sweep_pass" {
			t.Errorf("unexpected span name: %q", name)
		}
	}
	// Verify attributes set on at least one span.
	tel.mu.Lock()
	defer tel.mu.Unlock()
	hasAttrs := false
	for _, span := range tel.spans {
		if _, ok := span.attrs["sweep_id"]; ok {
			hasAttrs = true
			if _, ok := span.attrs["interval"]; !ok {
				t.Error("span missing 'interval' attribute")
			}
			if _, ok := span.attrs["deleted_count"]; !ok {
				t.Error("span missing 'deleted_count' attribute")
			}
			if _, ok := span.attrs["duration_ms"]; !ok {
				t.Error("span missing 'duration_ms' attribute")
			}
			if !span.ended {
				t.Error("span End() was not called")
			}
			break
		}
	}
	if !hasAttrs {
		t.Error("no span had 'sweep_id' attribute")
	}
}

func TestMemoryStoreSweeper_NoSpan_WhenTelemetryNil(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_ = store.Set(ctx, "key1", "v", 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	tel := &recordingTelemetry{}
	// Pass tel into the test mock but DO NOT supply WithMemoryStoreSweeperTelemetry.
	// Sweeper should not invoke tel.StartSpan even though we have an instance handy.
	sweeper, _ := NewMemoryStoreSweeper(store, 10*time.Millisecond, &NoOpLogger{})

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweeper.Start(runCtx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if tel.startCount() != 0 {
		t.Errorf("expected 0 StartSpan calls when telemetry option not provided, got %d", tel.startCount())
	}
}

// ---- NewMemoryStore lifecycle ----

func TestNewMemoryStore_DoesNotStartGoroutine(t *testing.T) {
	// runtime.NumGoroutine is noisy; we just check that NewMemoryStore alone
	// doesn't spawn a goroutine. Other tests / packages may have running
	// goroutines, so we assert delta == 0 after a brief settle.
	runtime.GC()
	time.Sleep(5 * time.Millisecond)
	before := runtime.NumGoroutine()

	stores := make([]*MemoryStore, 0, 10)
	for i := 0; i < 10; i++ {
		stores = append(stores, NewMemoryStore())
	}

	runtime.GC()
	time.Sleep(5 * time.Millisecond)
	after := runtime.NumGoroutine()

	// Allow up to 2 transient goroutines for unrelated runtime activity.
	if after-before > 2 {
		t.Errorf("constructing 10 MemoryStores added %d goroutines (before=%d, after=%d); expected ≤ 2 transient",
			after-before, before, after)
	}

	// Stores should all be functional.
	if len(stores) != 10 {
		t.Errorf("unexpected store count")
	}
}
