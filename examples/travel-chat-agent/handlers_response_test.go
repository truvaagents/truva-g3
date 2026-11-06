package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type responseFailureLogger struct {
	core.NoOpLogger
	ctx    context.Context
	fields map[string]interface{}
	errors int
}

func (l *responseFailureLogger) ErrorWithContext(ctx context.Context, _ string, fields map[string]interface{}) {
	l.ctx, l.fields = ctx, fields
	l.errors++
}

type failingResponseWriter struct {
	*httptest.ResponseRecorder
}

func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("private response transport detail")
}

func TestResponseHelpersPreserveWireContractAndRequestContext(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation string
		status    int
		failWrite bool
		invoke    func(*TravelChatAgent, http.ResponseWriter, *http.Request)
		want      map[string]interface{}
	}{
		{
			name: "JSON success", operation: "write_json", status: http.StatusCreated,
			invoke: func(agent *TravelChatAgent, w http.ResponseWriter, r *http.Request) {
				agent.writeJSON(w, r, http.StatusCreated, map[string]interface{}{"text": "Keep *pointers*, <tags>, and 日本語\n"})
			},
			want: map[string]interface{}{"text": "Keep *pointers*, <tags>, and 日本語\n"},
		},
		{
			name: "error success", operation: "write_error", status: http.StatusBadRequest,
			invoke: func(agent *TravelChatAgent, w http.ResponseWriter, r *http.Request) {
				agent.writeError(w, r, http.StatusBadRequest, "Invalid request", errors.New("original application details"))
			},
			want: map[string]interface{}{"error": "Invalid request", "status": float64(http.StatusBadRequest), "success": false, "details": "original application details"},
		},
		{
			name: "JSON write failure", operation: "write_json", status: http.StatusCreated, failWrite: true,
			invoke: func(agent *TravelChatAgent, w http.ResponseWriter, r *http.Request) {
				agent.writeJSON(w, r, http.StatusCreated, map[string]interface{}{"text": "private response data"})
			},
		},
		{
			name: "error write failure", operation: "write_error", status: http.StatusBadRequest, failWrite: true,
			invoke: func(agent *TravelChatAgent, w http.ResponseWriter, r *http.Request) {
				agent.writeError(w, r, http.StatusBadRequest, "Invalid request", errors.New("private application details"))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := &responseFailureLogger{}
			base := core.NewBaseAgent("response-test")
			base.Logger = logger
			agent := &TravelChatAgent{BaseAgent: base}
			request := httptest.NewRequest(http.MethodGet, "/chat/session", nil)
			request = request.WithContext(core.WithRequestID(request.Context(), "response-request"))
			recorder := httptest.NewRecorder()
			var writer http.ResponseWriter = recorder
			if tc.failWrite {
				writer = &failingResponseWriter{recorder}
			}
			tc.invoke(agent, writer, request)
			if recorder.Code != tc.status || recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Fatalf("response contract changed: status=%d headers=%v", recorder.Code, recorder.Header())
			}
			if tc.failWrite {
				if logger.errors != 1 || logger.ctx != request.Context() || core.GetRequestID(logger.ctx) != "response-request" {
					t.Fatalf("encoding failure lost request context: errors=%d context=%v", logger.errors, logger.ctx)
				}
				if logger.fields["operation"] != tc.operation || logger.fields["status"] != "failure" || logger.fields["error_type"] != "encoding" || logger.fields["error"] != "response encoding failed" {
					t.Fatalf("unexpected diagnostic fields: %v", logger.fields)
				}
				encoded, err := json.Marshal(logger.fields)
				if err != nil || strings.Contains(string(encoded), "private") {
					t.Fatalf("diagnostic contains payload/transport details: %s", encoded)
				}
				return
			}
			if logger.errors != 0 {
				t.Fatal("successful response logged an error")
			}
			var got map[string]interface{}
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(tc.want)
			gotJSON, _ := json.Marshal(got)
			if string(wantJSON) != string(gotJSON) {
				t.Fatalf("response payload changed: got %s want %s", gotJSON, wantJSON)
			}
		})
	}
}
