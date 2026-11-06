package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync/atomic"

	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

var errTestSSEWrite = errors.New("test SSE write failure")
var errTestSSEFlush = errors.New("test SSE flush failure")

type manualSSEHeartbeatTicker struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualSSEHeartbeatTicker() *manualSSEHeartbeatTicker {
	return &manualSSEHeartbeatTicker{ch: make(chan time.Time, 256), stopped: make(chan struct{})}
}

func (t *manualSSEHeartbeatTicker) C() <-chan time.Time { return t.ch }
func (t *manualSSEHeartbeatTicker) Stop() {
	t.once.Do(func() { close(t.stopped) })
}

type sseTestResponseWriter struct {
	mu           sync.Mutex
	header       http.Header
	body         bytes.Buffer
	writes       int
	flushes      int
	writeErrorAt int
	flushErrorAt int
	failEvent    string
}

func newSSETestResponseWriter() *sseTestResponseWriter {
	return &sseTestResponseWriter{header: make(http.Header)}
}

func (w *sseTestResponseWriter) Header() http.Header { return w.header }
func (*sseTestResponseWriter) WriteHeader(int)       {}
func (w *sseTestResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if (w.writeErrorAt > 0 && w.writes >= w.writeErrorAt) || (w.failEvent != "" && strings.HasPrefix(string(data), "event: "+w.failEvent+"\n")) {
		return 0, errTestSSEWrite
	}
	return w.body.Write(data)
}
func (w *sseTestResponseWriter) Flush() { _ = w.FlushError() }
func (w *sseTestResponseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushes++
	if w.flushErrorAt > 0 && w.flushes >= w.flushErrorAt {
		return errTestSSEFlush
	}
	return nil
}
func (w *sseTestResponseWriter) snapshot() (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String(), w.flushes
}

func waitForSSECondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSE condition")
		}
		runtime.Gosched()
	}
}

type parsedSSEEvent struct {
	typeName string
	data     string
}

// parseLikeChatUI follows the browser pages' event/data-only loop. Comment
// frames are deliberately ignored by falling through both branches.
func parseLikeChatUI(stream string) []parsedSSEEvent {
	var events []parsedSSEEvent
	var eventType string
	for _, line := range strings.Split(stream, "\n") {
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
		} else if strings.HasPrefix(line, "data: ") && eventType != "" {
			events = append(events, parsedSSEEvent{typeName: eventType, data: strings.TrimPrefix(line, "data: ")})
			eventType = ""
		}
	}
	return events
}

func TestSSEHeartbeatSerializesFramesAndRemainsAComment(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := newSSETestResponseWriter()
	callback := newManagedSSECallback(writer, writer, ctx, cancel)
	ticker := newManualSSEHeartbeatTicker()
	if err := callback.startHeartbeat(time.Hour, func(time.Duration) sseHeartbeatTicker { return ticker }); err != nil {
		t.Fatal(err)
	}

	const eventCount = 40
	const tickCount = 40
	var senders sync.WaitGroup
	for i := 0; i < eventCount; i++ {
		senders.Add(1)
		go func(value int) {
			defer senders.Done()
			callback.SendChunk(string(rune('a' + value%26)))
		}(i)
	}
	for i := 0; i < tickCount; i++ {
		ticker.ch <- time.Now()
	}
	senders.Wait()
	waitForSSECondition(t, func() bool {
		body, _ := writer.snapshot()
		return strings.Count(body, ": heartbeat\n\n") == tickCount+1 && strings.Count(body, "event: chunk\n") == eventCount
	})
	callback.StopHeartbeat()

	body, flushes := writer.snapshot()
	for _, frame := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if frame == ": heartbeat" {
			continue
		}
		if !strings.HasPrefix(frame, "event: chunk\ndata: {") || !strings.HasSuffix(frame, "}") {
			t.Fatalf("interleaved or malformed SSE frame: %q", frame)
		}
	}
	events := parseLikeChatUI(body)
	if len(events) != eventCount {
		t.Fatalf("UI-style parser received %d events, want %d", len(events), eventCount)
	}
	for _, event := range events {
		if event.typeName != "chunk" || event.data == "" {
			t.Fatalf("unexpected parsed event: %#v", event)
		}
	}
	if flushes != eventCount+tickCount+1 {
		t.Fatalf("flushes = %d, want %d", flushes, eventCount+tickCount+1)
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("heartbeat ticker was not stopped and joined")
	}
}

func TestSSEHeartbeatStopsOnCancellationAndCompletion(t *testing.T) {
	for _, test := range []struct {
		name       string
		stopStream func(*testing.T, context.CancelFunc, *SSECallback)
	}{
		{name: "request cancellation", stopStream: func(t *testing.T, cancel context.CancelFunc, callback *SSECallback) {
			cancel()
			// Cancellation must stop the worker without StopHeartbeat doing it.
			select {
			case <-callback.heartbeatDone:
			case <-time.After(time.Second):
				t.Fatal("heartbeat ignored cancellation")
			}
			callback.StopHeartbeat()
		}},
		{name: "handler completion", stopStream: func(_ *testing.T, _ context.CancelFunc, callback *SSECallback) {
			callback.StopHeartbeat()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := newSSETestResponseWriter()
			callback := newManagedSSECallback(writer, writer, ctx, cancel)
			ticker := newManualSSEHeartbeatTicker()
			if err := callback.startHeartbeat(time.Hour, func(time.Duration) sseHeartbeatTicker { return ticker }); err != nil {
				t.Fatal(err)
			}
			test.stopStream(t, cancel, callback)
			select {
			case <-ticker.stopped:
			default:
				t.Fatal("ticker still running after stream ended")
			}
			before, _ := writer.snapshot()
			ticker.ch <- time.Now()
			runtime.Gosched()
			after, _ := writer.snapshot()
			if after != before {
				t.Fatal("heartbeat wrote after stream cleanup")
			}
		})
	}
}

func TestSSEHeartbeatUsesTwentySecondProductionInterval(t *testing.T) {
	if sseHeartbeatInterval != 20*time.Second {
		t.Fatalf("heartbeat interval = %s", sseHeartbeatInterval)
	}
}

func TestSSEHeartbeatAllFailurePointsPreserveCause(t *testing.T) {
	for _, test := range []struct {
		name             string
		writeAt, flushAt int
		want             error
	}{
		{"initial write", 1, 0, errTestSSEWrite},
		{"initial flush", 0, 1, errTestSSEFlush},
		{"periodic write", 2, 0, errTestSSEWrite},
		{"periodic flush", 0, 2, errTestSSEFlush},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := newSSETestResponseWriter()
			writer.writeErrorAt, writer.flushErrorAt = test.writeAt, test.flushAt
			callback := newManagedSSECallback(writer, writer, ctx, cancel)
			ticker := newManualSSEHeartbeatTicker()
			factoryCalled := false
			err := callback.startHeartbeat(time.Hour, func(time.Duration) sseHeartbeatTicker {
				factoryCalled = true
				return ticker
			})
			if test.writeAt == 1 || test.flushAt == 1 {
				if !errors.Is(err, test.want) || factoryCalled {
					t.Fatalf("initial error = %v, factory called = %v", err, factoryCalled)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				ticker.ch <- time.Now()
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("delivery failure did not cancel processing")
			}
			callback.StopHeartbeat()
			before, _ := writer.snapshot()
			callback.SendChunk("must not be sent")
			callback.sendEvent("bad", make(chan int))
			after, _ := writer.snapshot()
			if before != after || !errors.Is(callback.Err(), test.want) {
				t.Fatalf("failed writer changed body or cause: %v", callback.Err())
			}
		})
	}
}

func TestSSEHeartbeatConcurrentStartStopJoinsInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := newSSETestResponseWriter()
	callback := newManagedSSECallback(writer, writer, ctx, cancel)
	ticker := newManualSSEHeartbeatTicker()
	entered, release := make(chan struct{}), make(chan struct{})
	var factories atomic.Int32
	started := make(chan error, 1)
	go func() {
		started <- callback.startHeartbeat(time.Hour, func(time.Duration) sseHeartbeatTicker {
			factories.Add(1)
			close(entered)
			<-release
			return ticker
		})
	}()
	<-entered
	stopped := make(chan struct{})
	go func() { callback.StopHeartbeat(); close(stopped) }()
	select {
	case <-stopped:
		close(release)
		<-started
		callback.StopHeartbeat()
		t.Fatal("cleanup returned before initialization was published")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not join initialization")
	}
	select {
	case <-ticker.stopped:
	default:
		t.Fatal("cleanup missed the newly initialized ticker")
	}
	var callers sync.WaitGroup
	for range 20 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_ = callback.startHeartbeat(time.Hour, func(time.Duration) sseHeartbeatTicker {
				factories.Add(1)
				return newManualSSEHeartbeatTicker()
			})
			callback.StopHeartbeat()
		}()
	}
	callers.Wait()
	if got := factories.Load(); got != 1 {
		t.Fatalf("created %d tickers", got)
	}
	before, _ := writer.snapshot()
	ticker.ch <- time.Now()
	callback.StopHeartbeat()
	after, _ := writer.snapshot()
	if before != after {
		t.Fatal("heartbeat restarted after cleanup")
	}
}

func TestSSEMarshalFailureCancelsWithoutWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := newSSETestResponseWriter()
	callback := newManagedSSECallback(writer, writer, ctx, cancel)
	callback.sendEvent("bad", make(chan int))
	var unsupported *json.UnsupportedTypeError
	if !errors.As(callback.Err(), &unsupported) || ctx.Err() == nil {
		t.Fatalf("marshal failure = %v, context = %v", callback.Err(), ctx.Err())
	}
	if body, _ := writer.snapshot(); body != "" {
		t.Fatalf("wrote invalid event: %q", body)
	}
}

func TestForwardStreamChunkChecksPhaseAndContentDelivery(t *testing.T) {
	for _, chunk := range []core.StreamChunk{
		{Content: "answer"},
		{Content: "phase progress", Metadata: map[string]interface{}{"type": "phase_complete", "phase": 2}},
	} {
		writer := newSSETestResponseWriter()
		writer.flushErrorAt = 1
		callback := NewSSECallback(writer, writer)
		if err := forwardStreamChunk(t.Context(), callback, chunk); !errors.Is(err, errTestSSEFlush) {
			t.Fatalf("delivery error = %v for %+v", err, chunk)
		}
		body, _ := writer.snapshot()
		if chunk.Metadata != nil && (strings.Contains(body, "event: chunk") || !strings.Contains(body, "event: status")) {
			t.Fatalf("phase progress became response content: %s", body)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	writer := newSSETestResponseWriter()
	// An unmanaged callback has no context/error, so ctx.Err() must win.
	if err := forwardStreamChunk(ctx, NewSSECallback(writer, writer), core.StreamChunk{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil callback error hid context cancellation: %v", err)
	}
}

func TestSSEHandlerValidationDoesNotStartHeartbeat(t *testing.T) {
	agent := &TravelChatAgent{BaseAgent: core.NewBaseAgent("sse-validation-test")}
	for _, test := range []struct{ method, body, code string }{
		{http.MethodOptions, "", ""},
		{http.MethodGet, "", "method_not_allowed"},
		{http.MethodPost, "{", "invalid_request"},
		{http.MethodPost, `{"message":""}`, "validation_error"},
	} {
		response := httptest.NewRecorder()
		NewSSEHandler(agent).ServeHTTP(response, httptest.NewRequest(test.method, "/chat/stream", strings.NewReader(test.body)))
		if strings.Contains(response.Body.String(), ": heartbeat") {
			t.Fatal("validation started a heartbeat")
		}
		if test.code != "" && !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("missing %s error", test.code)
		}
	}
}
