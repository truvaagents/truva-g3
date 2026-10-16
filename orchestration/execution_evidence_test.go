package orchestration

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestLastSuccessfulStep(t *testing.T) {
	base := StepResult{StepID: "original", AgentName: "tool", Capability: "read", Namespace: "one", Success: true,
		Parameters: map[string]any{"id": " exact ", "count": 1}, Response: `{"success":true,"data":{"v":1}}`}
	steps := []StepResult{base, base, base, base, base, base}
	steps[1].StepID, steps[1].Response = "latest", "invalid JSON"
	steps[2].Success = false
	steps[3].Skipped = true
	steps[4].Capability = "other"
	steps[5].AgentName = "other"
	filter := StepResultFilter{AgentName: "tool", Capability: "read", Namespace: "one", Parameters: map[string]any{"id": " exact "}}
	index, found := LastSuccessfulStep(steps, filter)
	require.True(t, found)
	require.Equal(t, 1, index)
	_, err := SuccessfulToolData(steps[index])
	require.Error(t, err, "invalid selected payload must not fall back to the older valid result")
	for _, mismatch := range []StepResultFilter{
		{Namespace: "other"},
		{Parameters: map[string]any{"id": "exact"}},
		{Parameters: map[string]any{"count": float64(1)}},
		{Parameters: map[string]any{"missing": nil}},
	} {
		index, found = LastSuccessfulStep(steps, mismatch)
		require.False(t, found)
		require.Equal(t, -1, index)
	}
	index, found = LastSuccessfulStep(nil, StepResultFilter{})
	require.Equal(t, -1, index)
	require.False(t, found)
	index, found = LastSuccessfulStep(steps, StepResultFilter{})
	require.True(t, found)
	require.Equal(t, len(steps)-1, index)
	require.Equal(t, " exact ", steps[0].Parameters["id"])
}

func TestSuccessfulToolData(t *testing.T) {
	for _, raw := range []string{
		`{"success":false,"data":{}}`, `{"data":{}}`, `{"success":true}`, `{"success":true,"data":null}`,
		`{"success":true,"data":{},"error":{"code":"FAILED"}}`,
		`{"success":true,"data":{},"extra":1}`,
		`{"success":true,"Success":false,"data":{}}`,
		`{"success":true,"success":false,"data":{}}`,
		`{"success":true,"data":{"id":"one","id":"two"}}`,
		`{"success":true,"data":[{"id":1,"id":2}]}`,
		`{"success":true,"data":{}} {}`, `{"success":true,`, `null`, `[]`,
		`{"success":"true","data":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			data, err := SuccessfulToolData(StepResult{Success: true, Response: raw})
			require.Error(t, err)
			require.Nil(t, data)
		})
	}
	for _, step := range []StepResult{{Success: false}, {Success: true, Skipped: true}} {
		_, err := SuccessfulToolData(step)
		require.Error(t, err)
	}
	for _, payload := range []string{`false`, `0`, `""`, `[]`, `{}`, `{"arbitrary":{"n":1e999}, "code":"*x = [1, 2, ]\n"}`} {
		response := `{"success":true,"data":` + payload + `,"error":null}`
		step := StepResult{Success: true, Response: response}
		data, err := SuccessfulToolData(step)
		require.NoError(t, err)
		require.Equal(t, payload, string(data))
		data[0] = 'X'
		require.Equal(t, response, step.Response)
	}
}

func TestExecutionRequestID(t *testing.T) {
	for _, tt := range []struct {
		name, response string
		metadata       map[string]any
		want           string
		invalid        bool
	}{
		{name: "missing"},
		{name: "response", response: "child", want: "child"},
		{name: "execution", metadata: map[string]any{MetadataKeyExecutionRequestID: "child"}, want: "child"},
		{name: "matching", response: "child", metadata: map[string]any{MetadataKeyExecutionRequestID: "child"}, want: "child"},
		{name: "conflict", response: "parent", metadata: map[string]any{MetadataKeyExecutionRequestID: "child"}, invalid: true},
		{name: "wrong type", response: "child", metadata: map[string]any{MetadataKeyExecutionRequestID: 3}, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ExecutionRequestID(&OrchestratorResponse{RequestID: tt.response}, &ExecutionResult{Metadata: tt.metadata})
			require.Equal(t, tt.want, id)
			require.Equal(t, tt.invalid, err != nil)
		})
	}
	id, err := ExecutionRequestID(nil, nil)
	require.NoError(t, err)
	require.Empty(t, id)
}

func TestSuccessfulToolDataUsesFinalDispatch(t *testing.T) {
	for _, tt := range []struct {
		name       string
		dispatches []StepDispatch
		want       string
		invalid    bool
	}{
		{name: "legacy response", want: `{"source":"normalized"}`},
		{name: "raw final dispatch", dispatches: []StepDispatch{
			{Success: false, Response: "earlier failure"},
			{Success: true, Response: `{"success":true,"data": {"id":9007199254740993, "note":"a<b"}}`},
		}, want: `{"id":9007199254740993, "note":"a<b"}`},
		{name: "failed final dispatch", dispatches: []StepDispatch{
			{Success: true, Response: `{"success":true,"data":{}}`}, {Success: false},
		}, invalid: true},
		{name: "empty final response", dispatches: []StepDispatch{{Success: true}}, invalid: true},
		{name: "invalid final response", dispatches: []StepDispatch{
			{Success: true, Response: `{"success":true,"data":{}}`}, {Success: true, Response: "invalid JSON"},
		}, invalid: true},
		{name: "duplicate in final response", dispatches: []StepDispatch{
			{Success: true, Response: `{"success":true,"data":{"id":1,"id":2}}`},
		}, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			step := StepResult{Success: true, Response: `{"success":true,"data":{"source":"normalized"}}`, Dispatches: tt.dispatches}
			before, err := json.Marshal(step)
			require.NoError(t, err)
			data, err := SuccessfulToolData(step)
			if tt.invalid {
				require.Error(t, err)
				require.Nil(t, data)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, string(data))
				data[0] = 'X'
			}
			after, err := json.Marshal(step)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestSuccessfulToolDataExecutorRecording(t *testing.T) {
	for _, duplicates := range []bool{false, true} {
		name, payload := "exact bytes", `{"order_id":9007199254740993, "note":"a<b", "code":"*x = [1, 2, ]\\n"}`
		if duplicates {
			name, payload = "duplicate keys", `{"order_id":9007199254740993,"k":1,"k":2}`
		}
		t.Run(name, func(t *testing.T) {
			raw := `{"success":true,"data":` + payload + `}`
			catalog := &AgentCatalog{agents: map[string]*AgentInfo{"target": {
				Registration: &core.ServiceRegistration{ID: "target", Name: "target", Address: "localhost", Port: 8080},
				Capabilities: []EnhancedCapability{{Name: "read", Endpoint: "/read"}},
			}}}
			executor := NewSmartExecutor(catalog)
			transport := NewMockRoundTripper()
			transport.SetResponse("http://localhost:8080/read", http.StatusOK, raw)
			executor.httpClient = &http.Client{Transport: transport}
			step := executor.executeStep(t.Context(), RoutingStep{StepID: "step-1", AgentName: "target", Metadata: map[string]any{
				"capability": "read", "parameters": map[string]any{},
			}})
			require.True(t, step.Success, step.Error)
			require.Len(t, step.Dispatches, 1)
			require.Equal(t, raw, step.Dispatches[0].Response)
			// The legacy Response is normalized. Round-trip the stored step to
			// exercise the helper against evidence an execution store can return.
			require.NotEqual(t, raw, step.Response)
			stored, err := json.Marshal(step)
			require.NoError(t, err)
			var recorded StepResult
			require.NoError(t, json.Unmarshal(stored, &recorded))
			data, err := SuccessfulToolData(recorded)
			if duplicates {
				require.ErrorContains(t, err, "duplicate")
				require.Nil(t, data)
			} else {
				require.NoError(t, err)
				require.Equal(t, payload, string(data))
			}
		})
	}
}

func TestSharedJSONValidationPreservesSkillContract(t *testing.T) {
	for _, raw := range []string{`{"a":1}`, `{"a":1,"a":2}`, `{} {}`, `{"a":[1,`, `1e999`} {
		// Skill decoding retains float64 token semantics and its own EOF error.
		err := rejectDuplicateSkillJSONFields([]byte(raw))
		if raw == `{"a":1}` {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
	_, err := SuccessfulToolData(StepResult{Success: true, Response: `{"success":true,"data":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`})
	require.Error(t, err)
}

func FuzzSuccessfulToolDataPreservesPayload(f *testing.F) {
	f.Add("*ptr = [1, 2, ]\n```json\n", "{{step-1.response.value}}")
	f.Add("\x00\t café", "name")
	f.Fuzz(func(t *testing.T, value, name string) {
		payload, err := json.Marshal(map[string]string{name: value})
		require.NoError(t, err)
		step := StepResult{Success: true, Response: `{"success":true,"data":` + string(payload) + `}`}
		before := step
		data, err := SuccessfulToolData(step)
		require.NoError(t, err)
		require.Equal(t, string(payload), string(data))
		require.True(t, reflect.DeepEqual(before, step))
	})
}
