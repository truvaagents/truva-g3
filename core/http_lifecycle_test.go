package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func lifecycleTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func TestHTTPComponentsJoinDrain(t *testing.T) {
	for _, kind := range []string{"agent", "tool"} {
		for _, expires := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timeout=%t", kind, expires), func(t *testing.T) {
				config := DefaultConfig()
				config.Name = "lifecycle-test"
				config.Discovery.Enabled = false
				config.HTTP.ShutdownTimeout = 100 * time.Millisecond
				var start func(context.Context, int) error
				var stop func(context.Context) error
				var register func(string, http.HandlerFunc) error
				if kind == "agent" {
					a := NewBaseAgentWithConfig(config)
					start, stop, register = a.Start, a.Stop, a.HandleFunc
				} else {
					a := NewToolWithConfig(config)
					start, stop = a.Start, a.Shutdown
					register = func(path string, handler http.HandlerFunc) error {
						a.mux.HandleFunc(path, handler)
						return nil
					}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
				t.Cleanup(releaseHandler)
				require.NoError(t, register("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
				require.NoError(t, register("/hold", func(w http.ResponseWriter, _ *http.Request) {
					close(entered)
					<-release
					_, _ = w.Write([]byte("drained"))
				}))
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				port := lifecycleTestPort(t)
				done := make(chan error, 1)
				go func() { done <- start(ctx, port) }()
				client := &http.Client{Timeout: 3 * time.Second}
				t.Cleanup(client.CloseIdleConnections)
				url := fmt.Sprintf("http://127.0.0.1:%d", port)
				require.Eventually(t, func() bool {
					response, err := client.Get(url + "/ready")
					if err != nil {
						return false
					}
					_ = response.Body.Close()
					return response.StatusCode == http.StatusOK
				}, 3*time.Second, 5*time.Millisecond)
				responseDone := make(chan error, 1)
				go func() {
					response, err := client.Get(url + "/hold")
					if err == nil {
						_, err = io.ReadAll(response.Body)
						_ = response.Body.Close()
					}
					responseDone <- err
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("handler was not entered")
				}
				explicitStop := make(chan error, 1)
				go func() { explicitStop <- stop(context.Background()) }()
				cancel()
				select {
				case err := <-done:
					t.Fatalf("Start returned before draining: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				if !expires {
					releaseHandler()
				}
				var runErr error
				select {
				case runErr = <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("Start did not finish bounded drain")
				}
				if expires {
					require.ErrorIs(t, runErr, context.DeadlineExceeded)
					require.ErrorIs(t, <-explicitStop, context.DeadlineExceeded)
					require.ErrorIs(t, stop(context.Background()), context.DeadlineExceeded)
				} else {
					require.NoError(t, runErr)
					require.NoError(t, <-explicitStop)
					require.NoError(t, stop(context.Background()))
				}
				releaseHandler()
				require.NoError(t, <-responseDone)
			})
		}
	}
}

func TestHTTPLifecycleStartupFailureAndConcurrentStop(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	server := &http.Server{Addr: listener.Addr().String(), ReadHeaderTimeout: time.Second}
	var calls atomic.Int32
	lifecycle := &httpLifecycle{server: server, timeout: time.Second, shutdown: func(ctx context.Context) error {
		calls.Add(1)
		return server.Shutdown(ctx)
	}}
	err = lifecycle.serve(context.Background())
	require.Error(t, err)
	var networkErr *net.OpError
	require.ErrorAs(t, err, &networkErr)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { _ = lifecycle.stop(context.Background()) })
	}
	wg.Wait()
	require.EqualValues(t, 1, calls.Load())
}

func TestHTTPLifecycleLaterCallerCanCancelItsWait(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			shutdownErr := errors.New("shutdown failed")
			var calls atomic.Int32
			lifecycle := &httpLifecycle{timeout: 3 * time.Second, shutdown: func(ctx context.Context) error {
				calls.Add(1)
				close(entered)
				select {
				case <-release:
					return shutdownErr
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			ownerDone := make(chan error, 1)
			go func() { ownerDone <- lifecycle.stop(context.Background()) }()
			<-entered
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			} else {
				cancel()
			}
			defer cancel()
			waiterDone := make(chan error, 1)
			go func() { waiterDone <- lifecycle.stop(ctx) }()
			select {
			case err := <-waiterDone:
				require.ErrorIs(t, err, ctx.Err())
			case <-time.After(time.Second):
				t.Fatal("later caller ignored its cancellation/deadline")
			}
			select {
			case err := <-ownerDone:
				t.Fatalf("later caller interrupted shared drain: %v", err)
			default:
			}
			unblock()
			require.ErrorIs(t, <-ownerDone, shutdownErr)
			// A completed operation retains its result, even for cancelled callers.
			require.ErrorIs(t, lifecycle.stop(ctx), shutdownErr)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestHTTPLifecycleServeJoinsDrainAfterWaiterCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	lifecycle := &httpLifecycle{server: server, timeout: 3 * time.Second, shutdown: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return server.Shutdown(ctx)
	}}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- lifecycle.serve(parent) }()
	<-entered // Parent cancellation owns the drain before the explicit caller.
	waiter, cancelWaiter := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWaiter()
	require.ErrorIs(t, lifecycle.stop(waiter), context.DeadlineExceeded)
	select {
	case err := <-serveDone:
		t.Fatalf("serve returned before shared drain finished: %v", err)
	default:
	}
	unblock()
	require.NoError(t, <-serveDone)
	require.NoError(t, lifecycle.stop(context.Background()))
}

func TestHTTPLifecycleFirstCallerOnlyLimitsItsWait(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%t", deadline), func(t *testing.T) {
			type correlationKey struct{}
			base := context.WithValue(context.Background(), correlationKey{}, "shutdown-owner")
			ctx, cancel := context.WithCancel(base)
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(base, 20*time.Millisecond)
			} else {
				cancel()
			}
			defer cancel()
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var calls atomic.Int32
			lifecycle := &httpLifecycle{timeout: 3 * time.Second, shutdown: func(drain context.Context) error {
				calls.Add(1)
				entered <- drain
				select {
				case <-release:
					return nil
				case <-drain.Done():
					return drain.Err()
				}
			}}
			waiter := make(chan error, 1)
			go func() { waiter <- lifecycle.stop(ctx) }()
			var drain context.Context
			select {
			case drain = <-entered:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not start")
			}
			select {
			case err := <-waiter:
				require.ErrorIs(t, err, ctx.Err())
			case <-time.After(time.Second):
				t.Fatal("first caller ignored its own wait deadline")
			}
			require.NoError(t, drain.Err(), "caller cancellation must leave the shared drain alive")
			require.Equal(t, "shutdown-owner", drain.Value(correlationKey{}))
			drainDeadline, bounded := drain.Deadline()
			require.True(t, bounded)
			require.Greater(t, time.Until(drainDeadline), time.Second)
			unblock()
			require.NoError(t, lifecycle.stop(context.Background()))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestHTTPComponentsCancelledStopPreservesDrain(t *testing.T) {
	for _, tool := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool=%t", tool), func(t *testing.T) {
			config := DefaultConfig()
			config.Name = "cancelled-stop"
			config.Discovery.Enabled = false
			config.HTTP.ShutdownTimeout = 2 * time.Second
			var start func(context.Context, int) error
			var stop func(context.Context) error
			var mux *http.ServeMux
			if tool {
				component := NewToolWithConfig(config)
				start, stop, mux = component.Start, component.Shutdown, component.mux
			} else {
				component := NewBaseAgentWithConfig(config)
				start, stop, mux = component.Start, component.Stop, component.mux
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			mux.HandleFunc("/hold", func(w http.ResponseWriter, _ *http.Request) {
				close(entered)
				<-release
				_, _ = w.Write([]byte("finished"))
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startDone := make(chan error, 1)
			port := lifecycleTestPort(t)
			go func() { startDone <- start(ctx, port) }()
			client := &http.Client{Timeout: 3 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			url := fmt.Sprintf("http://127.0.0.1:%d", port)
			require.Eventually(t, func() bool {
				response, err := client.Get(url + "/ready")
				if err != nil {
					return false
				}
				_ = response.Body.Close()
				return response.StatusCode == http.StatusOK
			}, 3*time.Second, 5*time.Millisecond)
			requestDone := make(chan error, 1)
			go func() {
				response, err := client.Get(url + "/hold")
				if err == nil {
					var body []byte
					body, err = io.ReadAll(response.Body)
					_ = response.Body.Close()
					if err == nil && (response.StatusCode != http.StatusOK || string(body) != "finished") {
						err = fmt.Errorf("unexpected drained response: status=%d body=%q", response.StatusCode, body)
					}
				}
				requestDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not start")
			}
			cancel()
			require.ErrorIs(t, stop(ctx), context.Canceled)
			select {
			case err := <-startDone:
				t.Fatalf("Start returned while a handler was still active: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			require.NoError(t, <-requestDone)
			select {
			case err := <-startDone:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("Start did not join shutdown")
			}
		})
	}
}

func TestHTTPLifecycleRetainsContextWithoutCancellation(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "correlation"))
	cancel()
	var observed context.Context
	server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
	lifecycle := &httpLifecycle{server: server, timeout: time.Second, shutdown: func(ctx context.Context) error {
		observed = ctx
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return server.Shutdown(ctx)
	}}
	require.NoError(t, lifecycle.serve(ctx))
	require.Equal(t, "correlation", observed.Value(key{}))
	_, bounded := observed.Deadline()
	require.True(t, bounded)
}

func TestHTTPShutdownTimeoutConfiguration(t *testing.T) {
	value, err := httpShutdownTimeout(0)
	require.NoError(t, err)
	require.Equal(t, defaultHTTPShutdownTimeout, value)
	for _, raw := range []string{"", "bad", "0s", "-1s"} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			t.Setenv("TRUVAG3_HTTP_SHUTDOWN_TIMEOUT", raw)
			config := DefaultConfig()
			require.ErrorIs(t, config.LoadFromEnv(), ErrInvalidConfiguration)
		})
	}
	t.Setenv("TRUVAG3_HTTP_SHUTDOWN_TIMEOUT", "2s")
	config := DefaultConfig()
	require.NoError(t, config.LoadFromEnv())
	require.Equal(t, 2*time.Second, config.HTTP.ShutdownTimeout)
	config.HTTP.ShutdownTimeout = -time.Second
	require.ErrorIs(t, config.Validate(), ErrInvalidConfiguration)
	require.ErrorIs(t, NewBaseAgentWithConfig(config).Start(context.Background(), 0), ErrInvalidConfiguration)
	require.ErrorIs(t, NewToolWithConfig(config).Start(context.Background(), 0), ErrInvalidConfiguration)
}

func TestHTTPComponentsPreCancelledStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, NewBaseAgent("cancelled").Start(ctx, 0), context.Canceled)
	require.ErrorIs(t, NewTool("cancelled").Start(ctx, 0), context.Canceled)
}

func TestRunCancelsRunnablesOnHTTPStartupFailure(t *testing.T) {
	startupErr := errors.New("listener unavailable")
	component := &fakeHTTPComponent{startErr: startupErr}
	framework := &Framework{component: component, config: &Config{Port: 0}}
	runnable := newFakeRunnable()
	framework.RegisterRunnable(runnable)
	parent := context.Background()
	require.ErrorIs(t, framework.Run(parent), startupErr)
	select {
	case <-runnable.exitedCh:
	default:
		t.Fatal("runnable was not joined after startup failure")
	}
	require.NoError(t, parent.Err())
}

type lifecycleMetrics struct {
	mockMetricsRegistry
	shutdowns       int
	durations       int
	lifecycleLabels []map[string]string
}

type lifecycleDiscovery struct{ Discovery }

func (lifecycleDiscovery) Unregister(context.Context, string) error { return ErrDiscoveryUnavailable }

func (m *lifecycleMetrics) Counter(name string, labels ...string) {
	if name == "agent.lifecycle" {
		fields := make(map[string]string)
		for i := 0; i+1 < len(labels); i += 2 {
			fields[labels[i]] = labels[i+1]
		}
		m.lifecycleLabels = append(m.lifecycleLabels, fields)
		if fields["event"] == "shutdown" {
			m.shutdowns++
		}
	}
}

func TestAgentInitializeLifecycleModuleLabel(t *testing.T) {
	previous := globalMetricsRegistry
	metrics := &lifecycleMetrics{}
	globalMetricsRegistry = metrics
	t.Cleanup(func() { globalMetricsRegistry = previous })
	config := DefaultConfig()
	config.Name = "lifecycle-initialize"
	config.Discovery.Enabled = false
	agent := NewBaseAgentWithConfig(config)
	require.NoError(t, agent.Initialize(t.Context()))
	require.Equal(t, []map[string]string{{
		"module": "core", "agent_name": config.Name,
		"event": "initialized", "discovery_enabled": "false",
	}}, metrics.lifecycleLabels)
}

func (m *lifecycleMetrics) Histogram(name string, _ float64, _ ...string) {
	if name == "agent.shutdown.duration_ms" {
		m.durations++
	}
}

func TestAgentStopEmitsOneLifecycleObservation(t *testing.T) {
	previous := globalMetricsRegistry
	metrics := &lifecycleMetrics{}
	globalMetricsRegistry = metrics
	t.Cleanup(func() { globalMetricsRegistry = previous })
	agent := NewBaseAgent("lifecycle-metrics")
	agent.Discovery = lifecycleDiscovery{}
	agent.Config.Discovery.Enabled = true
	logger := &captureLogger{}
	agent.Logger = logger
	server := &http.Server{ReadHeaderTimeout: time.Second}
	agent.lifecycle = &httpLifecycle{server: server, timeout: time.Second, shutdown: func(ctx context.Context) error {
		return agent.shutdownServer(ctx, server)
	}}
	require.NoError(t, agent.Stop(context.Background()))
	require.NoError(t, agent.Stop(context.Background()))
	require.Equal(t, 1, metrics.shutdowns)
	require.Equal(t, 1, metrics.durations)
	require.Equal(t, []map[string]string{{
		"module": "core", "agent_name": agent.Name,
		"event": "shutdown", "status": "success",
	}}, metrics.lifecycleLabels)
	require.Len(t, logger.entries, 1)
	require.Equal(t, "unregister", logger.entries[0].fields["operation"])
	require.ErrorIs(t, logger.entries[0].fields["error"].(error), ErrDiscoveryUnavailable)
}
