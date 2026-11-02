package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIncomingCallHelpersPreserveCurrentContext(t *testing.T) {
	t.Parallel()
	type unrelatedKey struct{}
	base, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	base = context.WithValue(base, unrelatedKey{}, "trace-and-application-value")
	base = WithRequestID(base, "current-request")
	base = WithStepID(base, "current-step")
	base = WithOriginalRequestID(base, "current-root")
	base = WithPlanID(base, "current-plan")
	base = WithPhaseNumber(base, 3)
	base = WithConversationID(base, "conversation")
	call := IncomingCall{RequestID: "caller:!#$%&'()*+-./09:<=>?@AZ[]^_az{|}~", StepID: "step-7", OriginalRequestID: "root"}
	ctx, err := WithIncomingCall(base, call)
	require.NoError(t, err)
	call.RequestID = "changed-after-copy"
	stored, present := IncomingCallFromContext(ctx)
	require.True(t, present)
	require.NotEqual(t, call.RequestID, stored.RequestID)
	stored.RequestID = "changed-after-read"
	storedAgain, present := IncomingCallFromContext(ctx)
	require.True(t, present)
	require.NotEqual(t, stored.RequestID, storedAgain.RequestID)
	cleared := WithoutIncomingCall(ctx)
	_, present = IncomingCallFromContext(cleared)
	require.False(t, present)
	for _, preserved := range []context.Context{ctx, cleared} {
		require.Equal(t, "current-request", GetRequestID(preserved))
		require.Equal(t, "current-step", GetStepID(preserved))
		require.Equal(t, "current-root", GetOriginalRequestID(preserved))
		require.Equal(t, "current-plan", GetPlanID(preserved))
		require.Equal(t, 3, GetPhaseNumber(preserved))
		require.Equal(t, "conversation", GetConversationID(preserved))
		require.Equal(t, "trace-and-application-value", preserved.Value(unrelatedKey{}))
		deadline, ok := preserved.Deadline()
		require.True(t, ok)
		originalDeadline, _ := base.Deadline()
		require.Equal(t, originalDeadline, deadline)
		require.Equal(t, base.Done(), preserved.Done())
	}
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.ErrorIs(t, cleared.Err(), context.Canceled)
	_, present = IncomingCallFromContext(base)
	require.False(t, present, "adding facts to a child must leave its parent unchanged")
}

func TestIncomingCallValidationAndNilContext(t *testing.T) {
	t.Parallel()
	_, present := IncomingCallFromContext(nil)
	require.False(t, present)
	_, present = IncomingCallFromContext(WithoutIncomingCall(nil))
	require.False(t, present)
	inherited, err := WithIncomingCall(nil, IncomingCall{RequestID: "older-request"})
	require.NoError(t, err)
	for _, field := range []string{"request_id", "step_id", "original_request_id"} {
		for _, invalid := range []string{"contains space", "comma,value", "semicolon;value", `quote"value`, `back\slash`, "é", string([]byte{0xff}), strings.Repeat("x", 513)} {
			t.Run(fmt.Sprintf("%s/%q", field, invalid), func(t *testing.T) {
				call := IncomingCall{RequestID: "new-request"}
				switch field {
				case "request_id":
					call.RequestID = invalid
				case "step_id":
					call.StepID = invalid
				case "original_request_id":
					call.OriginalRequestID = invalid
				}
				ctx, validationErr := WithIncomingCall(inherited, call)
				require.Error(t, validationErr)
				require.NotContains(t, validationErr.Error(), invalid)
				require.Less(t, len(validationErr.Error()), 128)
				_, found := IncomingCallFromContext(ctx)
				require.False(t, found, "invalid input must shadow an older caller")
			})
		}
	}
	ctx, err := WithIncomingCall(inherited, IncomingCall{StepID: "orphan"})
	require.Error(t, err)
	_, present = IncomingCallFromContext(ctx)
	require.False(t, present)
	maximum := strings.Repeat("x", MaxIncomingCallIDLength)
	ctx, err = WithIncomingCall(nil, IncomingCall{RequestID: maximum, StepID: maximum, OriginalRequestID: maximum})
	require.NoError(t, err)
	call, present := IncomingCallFromContext(ctx)
	require.True(t, present)
	require.Equal(t, maximum, call.RequestID)
	require.Equal(t, maximum, call.StepID)
	require.Equal(t, maximum, call.OriginalRequestID)
}

func TestIncomingCallIDCharacterContract(t *testing.T) {
	t.Parallel()
	for value := 0; value <= 255; value++ {
		id := string([]byte{byte(value)})
		ctx, err := WithIncomingCall(nil, IncomingCall{RequestID: id})
		_, present := IncomingCallFromContext(ctx)
		allowed := value == 0x21 || (value >= 0x23 && value <= 0x2b) ||
			(value >= 0x2d && value <= 0x3a) || (value >= 0x3c && value <= 0x5b) ||
			(value >= 0x5d && value <= 0x7e)
		require.Equalf(t, allowed, err == nil, "byte 0x%02x", value)
		require.Equalf(t, allowed, present, "byte 0x%02x", value)
	}
}

func TestIncomingCallMiddlewareEnvelope(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		headers http.Header
		want    IncomingCall
		reason  string
	}{
		{name: "absent"},
		{name: "caller only", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}}, want: IncomingCall{RequestID: "caller"}},
		{name: "complete", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}, "X-Truvag3-Step-Id": {"s"}, "X-Truvag3-Original-Request-Id": {"root"}}, want: IncomingCall{RequestID: "caller", StepID: "s", OriginalRequestID: "root"}},
		{name: "identical repeats", headers: http.Header{"X-Truvag3-Request-Id": {"caller", "caller"}, "x-truvag3-request-id": {"caller"}}, want: IncomingCall{RequestID: "caller"}},
		{name: "conflicting repeats", headers: http.Header{"X-Truvag3-Request-Id": {"caller", "other"}}, reason: "conflicting_values"},
		{name: "case conflicting repeats", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}, "x-truvag3-request-id": {"other"}}, reason: "conflicting_values"},
		{name: "conflicting steps", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}, "X-Truvag3-Step-Id": {"s", "other"}}, reason: "conflicting_values"},
		{name: "conflicting roots", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}, "X-Truvag3-Original-Request-Id": {"root", "other"}}, reason: "conflicting_values"},
		{name: "empty request", headers: http.Header{"X-Truvag3-Request-Id": {""}}, reason: "empty"},
		{name: "nil values", headers: http.Header{"X-Truvag3-Request-Id": nil}, reason: "empty"},
		{name: "empty step", headers: http.Header{"X-Truvag3-Request-Id": {"caller"}, "X-Truvag3-Step-Id": {""}}, reason: "empty"},
		{name: "orphan step", headers: http.Header{"X-Truvag3-Step-Id": {"s"}}, reason: "missing"},
		{name: "orphan root", headers: http.Header{"X-Truvag3-Original-Request-Id": {"root"}}, reason: "missing"},
		{name: "invalid value", headers: http.Header{"X-Truvag3-Request-Id": {"REJECTED secret"}}, reason: "invalid_character"},
		{name: "joined values", headers: http.Header{"X-Truvag3-Request-Id": {"caller,caller"}}, reason: "invalid_character"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, err := WithIncomingCall(WithRequestID(context.Background(), "current"), IncomingCall{RequestID: "stale"})
			require.NoError(t, err)
			logger := newComponentAwareMockLogger()
			called := false
			body := strings.NewReader("exact **application** body")
			req := httptest.NewRequest(http.MethodPost, "/", body).WithContext(base)
			req.Header = test.headers
			recorder := httptest.NewRecorder()
			IncomingCallMiddleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				call, present := IncomingCallFromContext(r.Context())
				require.Equal(t, test.want, call)
				require.Equal(t, test.want.RequestID != "", present)
				require.Equal(t, "current", GetRequestID(r.Context()))
				require.Same(t, recorder, w, "middleware must preserve the response writer")
				require.Implements(t, (*http.Flusher)(nil), w)
				data, readErr := io.ReadAll(r.Body)
				require.NoError(t, readErr)
				require.Equal(t, "exact **application** body", string(data))
				w.WriteHeader(http.StatusAccepted)
				_, writeErr := w.Write([]byte("data: streamed\n\n"))
				require.NoError(t, writeErr)
				w.(http.Flusher).Flush()
			})).ServeHTTP(recorder, req)
			require.True(t, called)
			require.Equal(t, http.StatusAccepted, recorder.Code)
			require.Equal(t, "data: streamed\n\n", recorder.Body.String())
			require.True(t, recorder.Flushed)
			components, entries := logger.snapshot()
			require.Equal(t, []string{"framework/core"}, components)
			if test.reason == "" {
				require.Empty(t, entries)
				return
			}
			require.Len(t, entries, 1)
			require.Equal(t, "warn", entries[0].Level)
			require.Equal(t, "incoming_call_context", entries[0].Fields["operation"])
			require.Equal(t, "current", entries[0].Fields["request_id"])
			require.Equal(t, "omitted", entries[0].Fields["status"])
			require.Equal(t, "invalid_correlation", entries[0].Fields["error_type"])
			require.Equal(t, test.reason, entries[0].Fields["reason"])
			serialized, err := json.Marshal(entries)
			require.NoError(t, err)
			require.NotContains(t, string(serialized), "REJECTED secret")
		})
	}
}

func TestIncomingCallMiddlewareNilLogger(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-TruvaG3-Step-ID", "orphan")
	called := false
	IncomingCallMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, present := IncomingCallFromContext(r.Context())
		require.False(t, present)
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, called)
}

func TestHTTPComponentsCaptureIncomingCall(t *testing.T) {
	for _, kind := range []string{"agent", "tool"} {
		t.Run(kind, func(t *testing.T) {
			type traceKey struct{}
			config := DefaultConfig()
			config.Name = "incoming-call-test"
			config.Discovery.Enabled = false
			config.HTTP.ShutdownTimeout = time.Second
			config.HTTP.Middleware = []func(http.Handler) http.Handler{func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, err := WithIncomingCall(r.Context(), IncomingCall{RequestID: "stale"})
					require.NoError(t, err)
					ctx = WithRequestID(ctx, "current-from-custom-middleware")
					ctx = context.WithValue(ctx, traceKey{}, "established-trace")
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			}}
			handler := func(w http.ResponseWriter, r *http.Request) {
				call, _ := IncomingCallFromContext(r.Context())
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"incoming": call,
					"request":  GetRequestID(r.Context()),
					"trace":    r.Context().Value(traceKey{}),
					"flusher":  implementsFlusher(w),
				})
			}
			var start func(context.Context, int) error
			if kind == "agent" {
				agent := NewBaseAgentWithConfig(config)
				start = agent.Start
				agent.RegisterCapability(Capability{Name: "inspect", Handler: handler})
				require.NoError(t, agent.HandleFunc("/custom", handler))
			} else {
				tool := NewToolWithConfig(config)
				start = tool.Start
				tool.RegisterCapability(Capability{Name: "inspect", Handler: handler})
				tool.mux.HandleFunc("/custom", handler)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			port := lifecycleTestPort(t)
			go func() { done <- start(ctx, port) }()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(3 * time.Second):
					t.Error("HTTP component did not stop")
				}
			})
			client := &http.Client{Timeout: time.Second}
			t.Cleanup(client.CloseIdleConnections)
			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			require.Eventually(t, func() bool {
				response, err := client.Get(baseURL + "/custom")
				if err != nil {
					return false
				}
				_ = response.Body.Close()
				return response.StatusCode == http.StatusOK
			}, 3*time.Second, 5*time.Millisecond)
			for _, path := range []string{"/custom", "/api/capabilities/inspect"} {
				for _, incoming := range []string{"caller", ""} {
					req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+path, nil)
					require.NoError(t, err)
					if incoming != "" {
						req.Header.Set("X-TruvaG3-Request-ID", incoming)
						req.Header.Set("X-TruvaG3-Step-ID", "s")
					}
					response, err := client.Do(req)
					require.NoError(t, err)
					var facts struct {
						Incoming IncomingCall
						Request  string
						Trace    string
						Flusher  bool
					}
					err = json.NewDecoder(response.Body).Decode(&facts)
					_ = response.Body.Close()
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, response.StatusCode)
					require.Equal(t, incoming, facts.Incoming.RequestID)
					require.Equal(t, "current-from-custom-middleware", facts.Request)
					require.Equal(t, "established-trace", facts.Trace)
					require.True(t, facts.Flusher)
				}
			}
		})
	}
}

func implementsFlusher(w http.ResponseWriter) bool {
	_, ok := w.(http.Flusher)
	return ok
}
