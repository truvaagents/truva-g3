package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type shutdownExporter struct {
	calls atomic.Int32
	err   error
}

func (*shutdownExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (e *shutdownExporter) Shutdown(context.Context) error {
	e.calls.Add(1)
	return e.err
}

func installShutdownRegistry(t *testing.T, exportErr error) (*Registry, *shutdownExporter) {
	t.Helper()
	previous := globalRegistry.Load()
	exporter := &shutdownExporter{err: exportErr}
	metrics := NewMetricInstruments("shutdown-test")
	metrics.meter = noop.NewMeterProvider().Meter("shutdown-test")
	provider := &OTelProvider{
		metrics:       metrics,
		traceProvider: sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter)),
	}
	r := &Registry{provider: provider, metrics: metrics, startTime: time.Now(), limiter: NewCardinalityLimiter(nil)}
	globalRegistry.Store(r)
	EnableFrameworkIntegration(nil)
	t.Cleanup(func() {
		r.limiter.Stop()
		_ = provider.Shutdown(context.Background())
		globalRegistry.Store(previous)
		core.SetMetricsRegistry(nil)
		if previous != nil {
			EnableFrameworkIntegration(previous.logger)
		}
	})
	return r, exporter
}

func assertShutdownNoOps(t *testing.T) {
	t.Helper()
	if GetRegistry() != nil || GetTelemetryProvider() != nil || GetHealth().Initialized {
		t.Fatal("shutdown must expose a real nil registry/provider and uninitialized health")
	}
	Counter("shutdown.counter")
	Gauge("shutdown.gauge", 1)
	TrackGoroutines("shutdown.goroutines", 1)
	EmitWithContext(t.Context(), "shutdown.context", 1)
	if GetInternalMetrics().Emitted != 0 {
		t.Fatal("post-shutdown emission was not a no-op")
	}
	ResetInternalMetrics()
}

func TestRegistryShutdownUninitialized(t *testing.T) {
	previous := globalRegistry.Load()
	globalRegistry.Store(nil)
	t.Cleanup(func() { globalRegistry.Store(previous) })
	if err := Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertShutdownNoOps(t)
}

func TestRegistryShutdownClearsPointer(t *testing.T) {
	_, exporter := installShutdownRegistry(t, nil)
	for range 2 {
		if err := Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertShutdownNoOps(t)
		if core.GetGlobalMetricsRegistry() != nil {
			t.Fatal("framework metrics registry remains registered")
		}
	}
	if exporter.calls.Load() != 1 {
		t.Fatalf("provider shutdown calls = %d, want 1", exporter.calls.Load())
	}
}

func TestRegistryShutdownConcurrentReads(t *testing.T) {
	_, exporter := installShutdownRegistry(t, nil)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 50 {
				Counter("shutdown.concurrent")
				Gauge("shutdown.concurrent_gauge", 1)
				TrackGoroutines("shutdown.concurrent_goroutines", 1)
				_ = GetHealth()
				_ = GetInternalMetrics()
				_ = GetTelemetryProvider()
			}
		})
		wg.Go(func() {
			if err := Shutdown(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertShutdownNoOps(t)
	if exporter.calls.Load() != 1 {
		t.Fatalf("provider shutdown calls = %d, want 1", exporter.calls.Load())
	}
}

func TestRegistryShutdownPreservesProviderFailure(t *testing.T) {
	r, exporter := installShutdownRegistry(t, errors.New("test exporter shutdown failure"))
	err := Shutdown(t.Context())
	if err == nil || !strings.Contains(err.Error(), "test exporter shutdown failure") {
		t.Fatalf("provider failure lost: %v", err)
	}
	// Preserve the existing error-path contract; this slice fixes nil storage,
	// not provider retry/error caching or failure-path lifecycle semantics.
	if GetRegistry() != r || exporter.calls.Load() != 1 {
		t.Fatal("provider failure unexpectedly cleared registry or repeated shutdown")
	}
}
