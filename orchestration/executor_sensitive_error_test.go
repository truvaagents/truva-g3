package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestSanitizeExecutionErrorPreservesCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("provider failed authorization=Bearer live-provider-key")
	got := sanitizeExecutionError(cause)
	if got == nil {
		t.Fatal("sanitizeExecutionError() returned nil")
	}
	if strings.Contains(got.Error(), "live-provider-key") {
		t.Fatalf("sanitized execution error retained the credential: %q", got.Error())
	}
	if !errors.Is(got, cause) {
		t.Fatal("sanitized execution error does not preserve its cause")
	}
}

func TestSmartExecutorRedactsComponentErrorBeforeLifecyclePropagation(t *testing.T) {
	t.Parallel()

	const secret = "live-provider-key"
	catalog := &AgentCatalog{agents: map[string]*AgentInfo{
		"tool-1": {
			Registration: &core.ServiceRegistration{
				ID: "tool-1", Name: "test-tool", Address: "localhost", Port: 8080,
				Type: core.ComponentTypeTool,
			},
			Capabilities: []EnhancedCapability{{Name: "test_cap", Endpoint: "/api/test"}},
		},
	}}

	executor := NewSmartExecutor(catalog)
	executor.SetMaxAttempts(1)
	mockRT := NewMockRoundTripper()
	mockRT.SetResponse(
		"http://localhost:8080/api/test",
		http.StatusBadGateway,
		"provider rejected api_key="+secret,
	)
	executor.httpClient = &http.Client{Transport: mockRT}

	var loggedFields []map[string]interface{}
	logger := &mockLogger{
		debugFunc: func(_ string, fields map[string]interface{}) {
			loggedFields = append(loggedFields, fields)
		},
		errorFunc: func(_ string, fields map[string]interface{}) {
			loggedFields = append(loggedFields, fields)
		},
	}
	executor.SetLogger(logger)

	result := executor.executeStep(context.Background(), RoutingStep{
		StepID: "step-1", AgentName: "test-tool",
		Metadata: map[string]interface{}{
			"capability": "test_cap",
			"parameters": map[string]interface{}{},
		},
	})

	if result.Success {
		t.Fatal("expected the component call to fail")
	}
	if strings.Contains(result.Error, secret) {
		t.Fatal("step state retained the component credential")
	}
	if !strings.Contains(result.Error, "api_key=[REDACTED]") {
		t.Fatalf("step state lost the useful redaction marker: %q", result.Error)
	}
	if strings.Contains(fmt.Sprint(loggedFields), secret) {
		t.Fatal("structured executor logs retained the component credential")
	}
}
