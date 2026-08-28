package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

const foundationTerminalSingleStepPlan = `{
  "plan_id":"terminal-phase",
  "original_request":"request",
  "mode":"autonomous",
  "steps":[{
    "step_id":"step-1",
    "agent_name":"test-agent",
    "namespace":"default",
    "instruction":"run the test capability",
    "depends_on":[],
    "metadata":{"capability":"test_capability","parameters":{}}
  }],
  "terminal":true
}`

func foundationNonTerminalSingleStepPlan(planID, stepID string) string {
	return fmt.Sprintf(`{
  "plan_id":%q,
  "original_request":"request",
  "mode":"autonomous",
  "steps":[{
    "step_id":%q,
    "agent_name":"test-agent",
    "namespace":"default",
    "instruction":"run the test capability",
    "depends_on":[],
    "metadata":{"capability":"test_capability","parameters":{}}
  }],
  "terminal":false
}`, planID, stepID)
}

type terminalRecordStore struct {
	*NoOpExecutionStore

	mu                    sync.Mutex
	records               []*StoredExecution
	hookFinished          func() bool
	terminalBeforeHookEnd bool
}

func (store *terminalRecordStore) Store(_ context.Context, record *StoredExecution) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records = append(store.records, record)
	if storedExecutionHasResultTrim(record) && store.hookFinished != nil && !store.hookFinished() {
		store.terminalBeforeHookEnd = true
	}
	return nil
}

func (store *terminalRecordStore) snapshot() ([]*StoredExecution, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]*StoredExecution(nil), store.records...), store.terminalBeforeHookEnd
}

func storedExecutionHasResultTrim(record *StoredExecution) bool {
	if record == nil || record.Result == nil {
		return false
	}
	for _, step := range record.Result.Steps {
		if step.Metadata != nil && step.Metadata["result_trim"] != nil {
			return true
		}
	}
	return false
}

type terminalMetadataProcessor struct{}

func (terminalMetadataProcessor) ProcessForPrompt(
	ctx context.Context,
	result string,
	_ int,
	_ ResultProcessorContext,
) string {
	CaptureResultTrimMetadata(ctx, ResultTrimMetadata{
		Method: "terminal-order-test", OriginalBytes: len(result), TrimmedBytes: len(result),
	})
	return result
}

type terminalHook struct {
	mu       sync.Mutex
	finished bool
}

func (*terminalHook) Name() string { return "terminal-record-order" }

func (hook *terminalHook) AfterSynthesis(
	_ context.Context,
	_ *core.PipelineContext,
	response string,
) (string, error) {
	hook.mu.Lock()
	hook.finished = true
	hook.mu.Unlock()
	return "governed: " + response, nil
}

func (hook *terminalHook) Finished() bool {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	return hook.finished
}

type terminalStreamingClient struct {
	streamErr error
}

func (*terminalStreamingClient) GenerateResponse(
	_ context.Context,
	_ string,
	_ *core.AIOptions,
) (*core.AIResponse, error) {
	return &core.AIResponse{Content: foundationTerminalSingleStepPlan, Model: "planner", Provider: "test"}, nil
}

func (client *terminalStreamingClient) StreamResponse(
	_ context.Context,
	_ string,
	_ *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	if client.streamErr != nil && !errors.Is(client.streamErr, core.ErrStreamPartiallyCompleted) {
		return nil, client.streamErr
	}
	if err := callback(core.StreamChunk{Content: "streamed response", FinishReason: "stop"}); err != nil {
		return nil, err
	}
	return &core.AIResponse{
		Content: "streamed response", Model: "synthesizer", Provider: "test",
	}, client.streamErr
}

func (*terminalStreamingClient) SupportsStreaming() bool { return true }

func TestRequestDeliveryModesPersistTerminalPostSynthesisView(t *testing.T) {
	tests := []struct {
		name          string
		streaming     bool
		client        core.AIClient
		wantError     bool
		wantHookCalls bool
	}{
		{
			name: "buffered success", client: &promptCapturingAIClient{
				responses: []string{foundationTerminalSingleStepPlan, "buffered response"},
			}, wantHookCalls: true,
		},
		{
			name: "simulated streaming success", streaming: true, client: &promptCapturingAIClient{
				responses: []string{foundationTerminalSingleStepPlan, "simulated response"},
			}, wantHookCalls: true,
		},
		{
			name: "native streaming success", streaming: true,
			client: &terminalStreamingClient{}, wantHookCalls: true,
		},
		{
			name: "native streaming partial", streaming: true,
			client: &terminalStreamingClient{streamErr: core.ErrStreamPartiallyCompleted},
		},
		{
			name: "native streaming failure", streaming: true,
			client: &terminalStreamingClient{streamErr: errors.New("stream failed")}, wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			orchestrator := setupTestOrchestrator(t, test.client)
			if err := orchestrator.discovery.Register(t.Context(), &core.ServiceRegistration{
				ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
				Type: core.ComponentTypeTool,
			}); err != nil {
				t.Fatal(err)
			}
			orchestrator.catalog.agents = map[string]*AgentInfo{
				"test-agent": {
					Registration: &core.ServiceRegistration{
						ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
						Type: core.ComponentTypeTool,
					},
					Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
				},
			}
			transport := NewMockRoundTripper()
			transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"ok"}`)
			orchestrator.executor.httpClient = &http.Client{Transport: transport}

			processor := terminalMetadataProcessor{}
			orchestrator.resultProcessor = processor
			orchestrator.synthesizer.resultProcessor = processor
			hook := &terminalHook{}
			orchestrator.pipelineHooks = []core.PipelineHook{hook}
			store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore(), hookFinished: hook.Finished}
			orchestrator.executionStore = store

			var requestErr error
			if test.streaming {
				_, requestErr = orchestrator.ProcessRequestStreaming(t.Context(), "request", nil, func(core.StreamChunk) error {
					return nil
				})
			} else {
				_, requestErr = orchestrator.ProcessRequest(t.Context(), "request", nil)
			}
			if (requestErr != nil) != test.wantError {
				t.Fatalf("request error = %v, wantError=%v", requestErr, test.wantError)
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := orchestrator.Shutdown(shutdownCtx); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}

			records, terminalBeforeHookEnd := store.snapshot()
			terminalCount := 0
			var terminalRecord *StoredExecution
			for _, record := range records {
				if storedExecutionHasResultTrim(record) {
					terminalCount++
					terminalRecord = record
				}
			}
			if terminalCount != 1 {
				t.Fatalf("terminal post-synthesis records = %d, all records = %d", terminalCount, len(records))
			}
			if test.wantHookCalls && terminalBeforeHookEnd {
				t.Fatal("terminal execution record was stored before AfterSynthesis completed")
			}
			if hook.Finished() != test.wantHookCalls {
				t.Fatalf("AfterSynthesis finished = %v, want %v", hook.Finished(), test.wantHookCalls)
			}
			if test.wantHookCalls {
				if terminalRecord == nil || terminalRecord.FinalResponse == nil ||
					!strings.HasPrefix(*terminalRecord.FinalResponse, "governed: ") {
					t.Fatalf("terminal final response = %#v, want governed post-hook output", terminalRecord)
				}
				if terminalRecord.FinalResponseSource != FinalResponseSourceAfterSynthesisHooks {
					t.Errorf("final response source = %q", terminalRecord.FinalResponseSource)
				}
			} else if terminalRecord != nil && terminalRecord.FinalResponse != nil {
				t.Errorf("partial/failed synthesis stored authoritative final response: %q", *terminalRecord.FinalResponse)
			}
		})
	}
}

func TestExecutionRecorder_MaxPhasePathHasBoundedWriteChain(t *testing.T) {
	const maxPhases = 2
	client := &promptCapturingAIClient{responses: []string{
		foundationNonTerminalSingleStepPlan("phase-1", "step-1"),
		foundationNonTerminalSingleStepPlan("phase-2", "step-2"),
		"bounded synthesis",
	}}
	orchestrator := setupTestOrchestrator(t, client)
	orchestrator.config.IterativePlanning.MaxPhases = maxPhases
	orchestrator.config.IterativePlanning.MaxTotalSteps = 10
	if err := orchestrator.discovery.Register(t.Context(), &core.ServiceRegistration{
		ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
		Type: core.ComponentTypeTool,
	}); err != nil {
		t.Fatal(err)
	}
	orchestrator.catalog.agents = map[string]*AgentInfo{
		"test-agent": {
			Registration: &core.ServiceRegistration{
				ID: "test-agent", Name: "test-agent", Address: "localhost", Port: 8080,
				Type: core.ComponentTypeTool,
			},
			Capabilities: []EnhancedCapability{{Name: "test_capability", Endpoint: "/process"}},
		},
	}
	transport := NewMockRoundTripper()
	transport.SetResponse("http://localhost:8080/process", http.StatusOK, `{"result":"ok"}`)
	orchestrator.executor.httpClient = &http.Client{Transport: transport}
	store := &terminalRecordStore{NoOpExecutionStore: NewNoOpExecutionStore()}
	orchestrator.executionStore = store

	if _, err := orchestrator.ProcessRequest(t.Context(), "request", nil); err != nil {
		t.Fatalf("ProcessRequest() error = %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := orchestrator.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	records, _ := store.snapshot()
	// A max-phase path can write once after each non-terminal phase, once for
	// the phase-loop final view, and once for the terminal synthesis view.
	if want := maxPhases + 2; len(records) != want {
		t.Fatalf("execution record writes = %d, want bounded maximum %d", len(records), want)
	}
}
