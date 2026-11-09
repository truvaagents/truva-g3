package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type blockedViewerWriter struct {
	*httptest.ResponseRecorder
	started chan<- struct{}
	release <-chan struct{}
}

func (writer *blockedViewerWriter) Write(body []byte) (int, error) {
	writer.started <- struct{}{}
	<-writer.release
	return writer.ResponseRecorder.Write(body)
}

func TestViewerReadBudgetIncludesBufferedResponseWrites(t *testing.T) {
	started := make(chan struct{}, viewerConcurrentDataReads)
	release := make(chan struct{})
	var workers sync.WaitGroup
	handler := apiMiddleware(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"request_id":"bounded-response"}`))
	})
	for range viewerConcurrentDataReads {
		workers.Add(1)
		go func() {
			defer workers.Done()
			writer := &blockedViewerWriter{httptest.NewRecorder(), started, release}
			handler(writer, httptest.NewRequest(http.MethodGet, "/api/executions", nil))
		}()
	}
	for range viewerConcurrentDataReads {
		<-started
	}
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/api/executions", nil))
	if response.Code != http.StatusTooManyRequests {
		t.Errorf("buffered writes released read slots early: %d", response.Code)
	}
	close(release)
	workers.Wait()
}

func TestViewerReadBudgetBoundsConcurrentInspectionWithoutBlockingHealth(t *testing.T) {
	started := make(chan struct{}, viewerConcurrentDataReads)
	release := make(chan struct{})
	var workers sync.WaitGroup
	handler := withViewerDataReadBudget(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/executions" && r.Method == http.MethodGet {
			started <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusOK)
	})
	for range viewerConcurrentDataReads {
		workers.Add(1)
		go func() {
			defer workers.Done()
			handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/executions", nil))
		}()
	}
	for range viewerConcurrentDataReads {
		<-started
	}
	blocked := httptest.NewRecorder()
	handler(blocked, httptest.NewRequest(http.MethodGet, "/api/executions/selected/unified?section=overview", nil))
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") != "1" || blocked.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("saturated inspection must be visibly retryable: status=%d headers=%v", blocked.Code, blocked.Header())
	}
	protected := httptest.NewRecorder()
	protected.Header().Set("Cache-Control", "private, no-store")
	handler(protected, httptest.NewRequest(http.MethodGet, "/api/executions", nil))
	if protected.Code != http.StatusTooManyRequests || protected.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("read admission changed protected response caching: %d %v", protected.Code, protected.Header())
	}
	for _, path := range []string{"/api/health", "/api/readiness", "/api/services"} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s blocked by inspection reads: %d", path, response.Code)
		}
	}
	mutation := httptest.NewRecorder()
	handler(mutation, httptest.NewRequest(http.MethodPost, "/api/hitl/command", nil))
	if mutation.Code != http.StatusOK {
		t.Errorf("mutation consumed read budget: %d", mutation.Code)
	}
	close(release)
	workers.Wait()
	recovered := httptest.NewRecorder()
	handler(recovered, httptest.NewRequest(http.MethodGet, "/api/executions/selected/unified?section=overview", nil))
	if recovered.Code != http.StatusOK {
		t.Errorf("finished reads did not release their slots: %d", recovered.Code)
	}
}

func TestViewerReadRoutesRejectUnsupportedMethodsWhenSaturated(t *testing.T) {
	previousMock := useMock
	useMock = true
	t.Cleanup(func() { useMock = previousMock })
	for range viewerConcurrentDataReads {
		viewerDataReadSlots <- struct{}{}
	}
	defer func() {
		for range viewerConcurrentDataReads {
			<-viewerDataReadSlots
		}
	}()
	// Use the actual route handlers: some legacy detail handlers accept POST
	// themselves, so the admission boundary must reject it before they run.
	for _, route := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/api/executions", handleExecutionList},
		{"/api/executions/search?q=example", handleExecutionSearch},
		{"/api/executions/example/unified?section=overview", handleExecution},
		{"/api/executions/example/lineage", handleExecution},
		{"/api/llm-debug", handleLLMDebugList},
		{"/api/llm-debug/example", handleLLMDebugRecord},
		{"/api/conversations?conversation_id=example", handleConversationTimeline},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			t.Run(method+" "+route.path, func(t *testing.T) {
				response := httptest.NewRecorder()
				apiMiddleware(route.handler)(response, httptest.NewRequest(method, route.path, nil))
				if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
					t.Fatalf("unsupported method reached read handler: status=%d headers=%v", response.Code, response.Header())
				}
			})
		}
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := httptest.NewRecorder()
			apiMiddleware(route.handler)(response, httptest.NewRequest(method, route.path, nil))
			if response.Code != http.StatusTooManyRequests {
				t.Fatalf("%s %s bypassed saturated read budget: %d", method, route.path, response.Code)
			}
		}
	}
}
