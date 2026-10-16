package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// Initialize owns process-wide singletons. Exercise its real logging in an
// isolated process instead of replacing globals shared by other telemetry tests.
func TestInitializeOperationLogs(t *testing.T) {
	const modeKey = "TRUVAG3_TEST_INITIALIZE_LOG_MODE"
	if mode := os.Getenv(modeKey); mode != "" {
		t.Setenv("TRUVAG3_LOG_FORMAT", "json")
		t.Setenv("TRUVAG3_LOG_LEVEL", "DEBUG")
		unsetInstanceEnv(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		config := UseProfile(ProfileDevelopment)
		config.ServiceName = "initialization-log-test"
		config.Endpoint = server.URL
		if mode == "failure" {
			config.ServiceInstanceID = " invalid "
		}
		err := Initialize(config)
		if (err != nil) != (mode == "failure") {
			t.Fatalf("Initialize error=%v, mode=%s", err, mode)
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInitializeOperationLogs$")
			cmd.Env = append(os.Environ(), modeKey+"="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child failed: %v\n%s", err, output)
			}
			want := map[string]string{
				"Telemetry initialization starting": "telemetry_initialize",
			}
			if mode == "success" {
				want["Telemetry system initialized successfully"] = "telemetry_initialize"
				want["Registered module metrics"] = "telemetry_register_module"
			} else {
				want["Telemetry initialization failed"] = "telemetry_initialize"
			}
			seen := map[string]bool{}
			for _, line := range bytes.Split(output, []byte("\n")) {
				var record map[string]interface{}
				if json.Unmarshal(line, &record) != nil {
					continue
				}
				message, _ := record["message"].(string)
				if operation, ok := want[message]; ok {
					if record["operation"] != operation {
						t.Errorf("%s: operation=%v, want %s", message, record["operation"], operation)
					}
					seen[message] = true
				}
			}
			for message := range want {
				if !seen[message] {
					t.Errorf("missing log %q in %s", message, output)
				}
			}
		})
	}
}

func TestResolveServiceInstanceID(t *testing.T) {
	for _, tc := range []struct {
		name, explicit, env, want string
		present, invalid          bool
	}{
		{name: "typed", explicit: "replica-a", env: "replica-b", present: true, want: "replica-a"},
		{name: "environment", env: "replica-b", present: true, want: "replica-b"},
		{name: "explicit beats invalid environment", explicit: "replica-a", present: true, want: "replica-a"},
		{name: "empty environment", present: true, invalid: true},
		{name: "whitespace", explicit: " replica-a", invalid: true},
		{name: "control", explicit: "replica\n", invalid: true},
		{name: "invalid utf8", explicit: string([]byte{0xff}), invalid: true},
		{name: "preserve unicode", explicit: "instance-é", want: "instance-é"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := resolveServiceInstanceID(tc.explicit, func(key string) (string, bool) {
				if key != "TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID" {
					t.Fatalf("unexpected environment key %q", key)
				}
				return tc.env, tc.present
			})
			if (err != nil) != tc.invalid || id != tc.want {
				t.Fatalf("identity=%q error=%v, want %q invalid=%v", id, err, tc.want, tc.invalid)
			}
		})
	}
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	for range 100 {
		id, err := resolveServiceInstanceID("", func(string) (string, bool) { return "", false })
		if err != nil || !pattern.MatchString(id) || seen[id] {
			t.Fatalf("generated identity=%q error=%v duplicate=%v", id, err, seen[id])
		}
		seen[id] = true
	}
}

// Capture real OTLP protobuf exports, not merely a resource-construction helper.
// Three independent providers for the same service reproduce replica topology.
func TestOTelReplicaIdentityExports(t *testing.T) {
	unsetInstanceEnv(t)
	oldTracer, oldMeter, oldPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(oldTracer)
		otel.SetMeterProvider(oldMeter)
		otel.SetTextMapPropagator(oldPropagator)
	})
	var mu sync.Mutex
	counts := map[string]int64{}
	traces := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/metrics":
			var req collectormetric.ExportMetricsServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				t.Error(err)
			}
			for _, rm := range req.ResourceMetrics {
				id := exportedInstanceID(t, rm.Resource)
				for _, scope := range rm.ScopeMetrics {
					for _, m := range scope.Metrics {
						if m.Name == "identity.calls_total" {
							for _, point := range m.GetSum().GetDataPoints() {
								counts[id] = point.GetAsInt()
							}
						}
					}
				}
			}
		case "/v1/traces":
			var req collectortrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(data, &req); err != nil {
				t.Error(err)
			}
			for _, rs := range req.ResourceSpans {
				traces[exportedInstanceID(t, rs.Resource)] = true
			}
		default:
			t.Errorf("unexpected export path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	newProvider := func(options ...OTelProviderOption) *OTelProvider {
		p, err := NewOTelProvider("replicated-service", "agent", server.URL, options...)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Shutdown(ctx); err != nil {
				t.Error(err)
			}
		})
		return p
	}
	flush := func(p *OTelProvider) {
		if err := p.metricProvider.ForceFlush(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := p.traceProvider.ForceFlush(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	assertTotals := func(instances int, total int64) {
		mu.Lock()
		defer mu.Unlock()
		var sum int64
		for id, count := range counts {
			sum += count
			if id == "" || !traces[id] {
				t.Errorf("metric identity %q missing from traces", id)
			}
		}
		if len(counts) != instances || len(traces) != instances || sum != total {
			t.Fatalf("counts=%v trace instances=%d, want %d instances totaling %d", counts, len(traces), instances, total)
		}
	}
	var providers []*OTelProvider
	for _, count := range []float64{9, 16, 0} {
		p := newProvider()
		providers = append(providers, p)
		p.RecordMetric("identity.calls_total", count, nil)
		_, span := p.tracer.Start(t.Context(), "identity-check")
		span.End()
		flush(p)
	}
	assertTotals(3, 25)
	// Alternate export order with no new work: identity and totals stay stable.
	for round := range 3 {
		for i := range providers {
			flush(providers[(round+i)%len(providers)])
		}
		assertTotals(3, 25)
	}
	providers[0].RecordMetric("identity.calls_total", 2, nil)
	flush(providers[0])
	assertTotals(3, 27)
	// A replacement SDK has a fresh identity; it cannot overwrite its predecessor.
	replacement := newProvider()
	replacement.RecordMetric("identity.calls_total", 3, nil)
	_, span := replacement.tracer.Start(t.Context(), "replacement")
	span.End()
	flush(replacement)
	assertTotals(4, 30)

	// The supported code override passes through registry construction to exports.
	t.Setenv("TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID", "environment-instance")
	config := Config{ServiceName: "replicated-service", Endpoint: server.URL}.
		WithOverrides(Config{ServiceInstanceID: "configured-instance"})
	r, err := newRegistry(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.limiter.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.provider.Shutdown(ctx)
	})
	r.provider.RecordMetric("identity.calls_total", 1, nil)
	flush(r.provider)
	mu.Lock()
	if counts["configured-instance"] != 1 {
		t.Errorf("typed instance ID did not reach export: %v", counts)
	}
	mu.Unlock()
	p := newProvider()
	p.RecordMetric("identity.calls_total", 1, nil)
	flush(p)
	mu.Lock()
	if counts["environment-instance"] != 1 {
		t.Errorf("environment instance ID did not reach export: %v", counts)
	}
	mu.Unlock()
}

func exportedInstanceID(t *testing.T, resource *resourcepb.Resource) string {
	t.Helper()
	var id, name string
	for _, attr := range resource.GetAttributes() {
		switch attr.Key {
		case "service.instance.id":
			id = attr.Value.GetStringValue()
		case "service.name":
			name = attr.Value.GetStringValue()
		}
	}
	if id == "" || name != "replicated-service" {
		t.Errorf("invalid exported resource: instance=%q service=%q", id, name)
	}
	return id
}

func unsetInstanceEnv(t *testing.T) {
	t.Helper()
	const key = "TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID"
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestServiceInstanceIDInvalidConstruction(t *testing.T) {
	t.Setenv("TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID", "")
	for _, options := range [][]OTelProviderOption{nil, {nil}, {WithServiceInstanceID("bad\nidentity")}} {
		p, err := NewOTelProvider("service", "agent", "localhost:4318", options...)
		if err == nil || p != nil {
			t.Fatalf("invalid identity/options returned provider=%v error=%v", p, err)
		}
	}
}

func TestInitializePreservesIdentityConfigurationError(t *testing.T) {
	t.Setenv("TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID", "")
	previous := globalRegistry.Load()
	globalRegistry.Store(nil)
	initOnce = sync.Once{}
	initErr = nil
	t.Cleanup(func() {
		globalRegistry.Store(previous)
		initOnce = sync.Once{}
		initErr = nil
	})
	var callers sync.WaitGroup
	errors := make([]error, 10)
	for i := range errors {
		callers.Go(func() {
			errors[i] = Initialize(Config{ServiceName: "invalid-identity"})
		})
	}
	callers.Wait()
	for _, err := range errors {
		if err == nil || !strings.Contains(err.Error(), "instance ID") || GetRegistry() != nil {
			t.Fatalf("configuration failure lost: error=%v registry=%v", err, GetRegistry())
		}
	}
	// Correcting settings does not silently restart a once-owned lifecycle.
	t.Setenv("TRUVAG3_TELEMETRY_SERVICE_INSTANCE_ID", "now-valid")
	if err := Initialize(Config{ServiceName: "valid-identity"}); err != errors[0] || GetRegistry() != nil {
		t.Fatalf("initialization result changed: error=%v registry=%v", err, GetRegistry())
	}
}
