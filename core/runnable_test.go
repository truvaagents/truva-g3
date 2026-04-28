package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Test fakes ---

// fakeRunnable is a controllable Runnable implementation for tests.
type fakeRunnable struct {
	mu          sync.Mutex
	startCalled int32
	startErr    error
	blockUntil  func(ctx context.Context) error // optional custom block behaviour
	startedCh   chan struct{}                   // closed when Start is invoked
	exitedCh    chan struct{}                   // closed when Start returns
}

func newFakeRunnable() *fakeRunnable {
	return &fakeRunnable{
		startedCh: make(chan struct{}),
		exitedCh:  make(chan struct{}),
	}
}

func (f *fakeRunnable) Start(ctx context.Context) error {
	atomic.AddInt32(&f.startCalled, 1)
	close(f.startedCh)
	defer close(f.exitedCh)

	if f.blockUntil != nil {
		return f.blockUntil(ctx)
	}
	if f.startErr != nil {
		return f.startErr
	}
	// Default: block until ctx done
	<-ctx.Done()
	return nil
}

// fakeHTTPComponent satisfies HTTPComponent for tests without spinning up a real server.
// Implements the loggerHaver interface so getComponentLogger can extract the logger.
type fakeHTTPComponent struct {
	id           string
	name         string
	logger       Logger
	initErr      error
	startErr     error
	startCalled  int32
	startBlocked time.Duration // how long Start blocks before returning (simulates HTTP server)
}

func (f *fakeHTTPComponent) Initialize(ctx context.Context) error { return f.initErr }
func (f *fakeHTTPComponent) GetID() string                        { return f.id }
func (f *fakeHTTPComponent) GetName() string {
	if f.name == "" {
		return "fake-component"
	}
	return f.name
}
func (f *fakeHTTPComponent) GetCapabilities() []Capability { return nil }
func (f *fakeHTTPComponent) GetType() ComponentType        { return ComponentTypeAgent }
func (f *fakeHTTPComponent) Start(ctx context.Context, port int) error {
	atomic.AddInt32(&f.startCalled, 1)
	if f.startErr != nil {
		return f.startErr
	}
	if f.startBlocked > 0 {
		select {
		case <-time.After(f.startBlocked):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeHTTPComponent) RegisterCapability(cap Capability) {}

// GetLogger implements the loggerHaver interface used by getComponentLogger
// for custom HTTPComponent implementations (not BaseAgent/BaseTool).
func (f *fakeHTTPComponent) GetLogger() Logger { return f.logger }

// --- RegisterRunnable tests ---

func TestRegisterRunnable_AddsToSlice(t *testing.T) {
	f := &Framework{component: &fakeHTTPComponent{}}
	r1 := newFakeRunnable()
	r2 := newFakeRunnable()

	f.RegisterRunnable(r1)
	f.RegisterRunnable(r2)

	assert.Len(t, f.runnables, 2, "both runnables should be registered")
}

func TestRegisterRunnable_LogsRegistration(t *testing.T) {
	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component}

	f.RegisterRunnable(newFakeRunnable())

	require.NotEmpty(t, logger.entries, "RegisterRunnable should emit a log")
	entry := logger.entries[0]
	assert.Equal(t, "Runnable registered with framework", entry.msg)
	assert.Equal(t, "framework_register_runnable", entry.fields["operation"])
	assert.Equal(t, 1, entry.fields["total_count"])
}

// --- Run() with runnables tests ---

func TestRun_StartsRegisteredRunnables(t *testing.T) {
	component := &fakeHTTPComponent{}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r1 := newFakeRunnable()
	r2 := newFakeRunnable()
	f.RegisterRunnable(r1)
	f.RegisterRunnable(r2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	// Wait for both runnables to actually start
	select {
	case <-r1.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("r1 did not start within 2s")
	}
	select {
	case <-r2.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("r2 did not start within 2s")
	}

	cancel()
	<-done

	assert.Equal(t, int32(1), atomic.LoadInt32(&r1.startCalled))
	assert.Equal(t, int32(1), atomic.LoadInt32(&r2.startCalled))
}

func TestRun_DrainsRunnablesOnCtxCancel(t *testing.T) {
	component := &fakeHTTPComponent{}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-r.startedCh
	cancel()

	// Runnable should exit cleanly via ctx
	select {
	case <-r.exitedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runnable did not exit within 2s of ctx cancel")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return within 2s")
	}
}

func TestRun_DrainTimeoutFires_WhenRunnableIgnoresCtx(t *testing.T) {
	t.Setenv("TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT", "100ms")

	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component, config: &Config{Port: 0}}

	// Stubborn runnable that ignores ctx and blocks forever
	stubborn := newFakeRunnable()
	stubborn.blockUntil = func(ctx context.Context) error {
		time.Sleep(10 * time.Second)
		return nil
	}
	f.RegisterRunnable(stubborn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-stubborn.startedCh
	cancel()

	// Run should return after drain timeout (~100ms)
	start := time.Now()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after drain timeout")
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 1*time.Second, "Run should return shortly after drain timeout")

	// Verify drain timeout warning was logged
	foundTimeout := false
	for _, e := range logger.entries {
		if e.fields["error_type"] == "runnable_drain_timeout" {
			foundTimeout = true
			break
		}
	}
	assert.True(t, foundTimeout, "drain timeout warning should be logged")
}

func TestRun_LogsRunnableExitError(t *testing.T) {
	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component, config: &Config{Port: 0}}

	failing := newFakeRunnable()
	failing.startErr = errors.New("simulated runtime error")
	f.RegisterRunnable(failing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-failing.exitedCh
	cancel()
	<-done

	// Verify error log
	foundError := false
	for _, e := range logger.entries {
		if e.fields["error_type"] == "runnable_exit" {
			foundError = true
			assert.Contains(t, e.fields["error"], "simulated runtime error")
			break
		}
	}
	assert.True(t, foundError, "runnable exit error should be logged")
}

func TestRun_LogsCleanRunnableExit(t *testing.T) {
	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-r.startedCh
	cancel()
	<-done

	// Verify clean exit log
	foundClean := false
	for _, e := range logger.entries {
		if e.fields["operation"] == "framework_runnable_exit" && e.level == "info" {
			foundClean = true
			break
		}
	}
	assert.True(t, foundClean, "clean runnable exit should be logged at INFO")
}

func TestRun_LogsLifecycleEvents(t *testing.T) {
	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-r.startedCh
	cancel()
	<-done

	// Verify all lifecycle events were logged
	expectedOps := map[string]bool{
		"framework_register_runnable": false,
		"framework_runnable_start":    false,
		"framework_runnable_exit":     false,
		"framework_runnable_drain":    false,
	}
	for _, e := range logger.entries {
		if op, ok := e.fields["operation"].(string); ok {
			if _, expected := expectedOps[op]; expected {
				expectedOps[op] = true
			}
		}
	}
	for op, found := range expectedOps {
		assert.True(t, found, "lifecycle log %q should be emitted", op)
	}
}

func TestRun_NoRunnables_WorksAsBefore(t *testing.T) {
	component := &fakeHTTPComponent{}
	f := &Framework{component: component, config: &Config{Port: 0}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	// Give Run a moment to call Start on the component
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return when no runnables are registered")
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&component.startCalled), "component.Start should be called even with no runnables")
}

func TestRun_InitializeError_ReturnsImmediately(t *testing.T) {
	component := &fakeHTTPComponent{initErr: errors.New("init failed")}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	err := f.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
	assert.Equal(t, int32(0), atomic.LoadInt32(&r.startCalled), "runnables should not start when Initialize fails")
}

// --- Drain timeout env var tests ---

func TestRun_DefaultDrainTimeout_WhenEnvUnset(t *testing.T) {
	t.Setenv("TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT", "")

	component := &fakeHTTPComponent{}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-r.startedCh
	cancel()
	<-done
	// Just verifying it doesn't panic and Runnable exits cleanly via ctx.
}

func TestRun_DrainTimeoutEnvVar_Override(t *testing.T) {
	t.Setenv("TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT", "50ms")

	logger := &captureLogger{}
	component := &fakeHTTPComponent{logger: logger}
	f := &Framework{component: component, config: &Config{Port: 0}}

	stubborn := newFakeRunnable()
	stubborn.blockUntil = func(ctx context.Context) error {
		time.Sleep(5 * time.Second)
		return nil
	}
	f.RegisterRunnable(stubborn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-stubborn.startedCh
	cancel()

	start := time.Now()
	<-done
	elapsed := time.Since(start)

	// Should return well before 5s (the runnable's block) — within ~500ms
	// of cancel() because drain timeout is 50ms.
	assert.Less(t, elapsed, 1*time.Second, "drain should respect 50ms env var override")

	// Verify the log captured the override value
	for _, e := range logger.entries {
		if e.fields["error_type"] == "runnable_drain_timeout" {
			assert.Equal(t, "50ms", e.fields["drain_timeout"])
			break
		}
	}
}

func TestRun_DrainTimeoutEnvVar_InvalidIgnored(t *testing.T) {
	t.Setenv("TRUVAG3_FRAMEWORK_RUNNABLE_DRAIN_TIMEOUT", "not-a-duration")

	component := &fakeHTTPComponent{}
	f := &Framework{component: component, config: &Config{Port: 0}}

	r := newFakeRunnable()
	f.RegisterRunnable(r)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	<-r.startedCh
	cancel()

	// Should not panic, should use default timeout
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return — invalid env var may have caused issue")
	}
}

// --- getComponentLogger tests ---

func TestGetComponentLogger_BaseAgent(t *testing.T) {
	logger := &NoOpLogger{}
	base := &BaseAgent{Logger: logger}

	got := getComponentLogger(base)
	assert.Equal(t, Logger(logger), got)
}

func TestGetComponentLogger_BaseTool(t *testing.T) {
	logger := &NoOpLogger{}
	base := &BaseTool{Logger: logger}

	got := getComponentLogger(base)
	assert.Equal(t, Logger(logger), got)
}

func TestGetComponentLogger_LoggerHaverInterface(t *testing.T) {
	expected := &NoOpLogger{}
	custom := &customComponentWithLogger{logger: expected}

	got := getComponentLogger(custom)
	assert.Equal(t, Logger(expected), got)
}

func TestGetComponentLogger_NilComponent(t *testing.T) {
	got := getComponentLogger(nil)
	assert.Nil(t, got)
}

func TestGetComponentLogger_UnknownComponent(t *testing.T) {
	component := &fakeHTTPComponent{}
	got := getComponentLogger(component)
	assert.Nil(t, got, "unknown component types should return nil logger")
}

// --- Helpers ---

// captureLogger records all log calls for assertion.
type captureLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

type logEntry struct {
	level  string
	msg    string
	fields map[string]interface{}
}

func (c *captureLogger) Info(msg string, fields map[string]interface{}) {
	c.add("info", msg, fields)
}
func (c *captureLogger) Error(msg string, fields map[string]interface{}) {
	c.add("error", msg, fields)
}
func (c *captureLogger) Warn(msg string, fields map[string]interface{}) {
	c.add("warn", msg, fields)
}
func (c *captureLogger) Debug(msg string, fields map[string]interface{}) {
	c.add("debug", msg, fields)
}
func (c *captureLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	c.add("info", msg, fields)
}
func (c *captureLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	c.add("error", msg, fields)
}
func (c *captureLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	c.add("warn", msg, fields)
}
func (c *captureLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	c.add("debug", msg, fields)
}
func (c *captureLogger) add(level, msg string, fields map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copy fields to prevent caller mutation
	copied := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		copied[k] = v
	}
	c.entries = append(c.entries, logEntry{level: level, msg: msg, fields: copied})
}

// customComponentWithLogger implements HTTPComponent with a GetLogger() method.
type customComponentWithLogger struct {
	logger Logger
}

func (c *customComponentWithLogger) Initialize(ctx context.Context) error { return nil }
func (c *customComponentWithLogger) GetID() string                        { return "custom" }
func (c *customComponentWithLogger) GetName() string                      { return "custom" }
func (c *customComponentWithLogger) GetCapabilities() []Capability        { return nil }
func (c *customComponentWithLogger) GetType() ComponentType               { return ComponentTypeAgent }
func (c *customComponentWithLogger) Start(ctx context.Context, port int) error {
	<-ctx.Done()
	return nil
}
func (c *customComponentWithLogger) RegisterCapability(cap Capability) {}
func (c *customComponentWithLogger) GetLogger() Logger                 { return c.logger }
