//go:build lineageintegration

package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const lineageVerificationSource = `{"success":true,"data":{"content_text":"Network **中文** <router> & café"}}`
const lineageVerificationReasoning = "synthetic reasoning **中文** <unchanged>"

type lineageVerificationCall struct {
	RequestID, Purpose, Prompt, Answer string
	Phase, Plan                        string
}

// Responses depend only on role and purpose. Concurrent reviewers do not
// consume a mutable response queue, and this client has no provider or network.
type lineageVerificationAI struct {
	role  string
	mu    sync.Mutex
	calls []lineageVerificationCall
}

func (c *lineageVerificationAI) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	result, err := c.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "synthesis", options))
	if err != nil {
		return nil, err
	}
	return result.Response, nil
}

func (c *lineageVerificationAI) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	answer := "deterministic " + c.role + " response **中文** <unchanged>"
	switch request.Purpose {
	case "planning":
		steps := []RoutingStep{}
		add := func(id, target, capability, query string) {
			steps = append(steps, RoutingStep{StepID: id, AgentName: "lineage-verification-" + target,
				Namespace: "default", Instruction: query, Metadata: map[string]interface{}{
					"capability": capability, "parameters": map[string]interface{}{"query": query},
				}})
		}
		switch c.role {
		case "root":
			add("step-1", "child", "invoke", "delegate exact **中文** <input>")
		case "failed-root":
			add("step-1", "fanout-child", "invoke", "delegate independent reviews")
			add("step-2", "leaf-child", "invoke", "inspect candidate two")
			add("step-3", "leaf-child", "invoke", "inspect candidate three")
		case "child", "fanout-child":
			add("step-17", "reviewer", "invoke", "review candidate one")
			add("step-18", "reviewer", "invoke", "review candidate two")
		case "reviewer", "leaf-child":
			add("step-1", "source", "read_source", "read exact source")
		default:
			return nil, fmt.Errorf("unknown deterministic role %q", c.role)
		}
		terminal := true
		plan := RoutingPlan{PlanID: "lineage-verification-" + c.role + "-plan",
			Mode: ModeAutonomous, Terminal: &terminal, Steps: steps}
		encoded, err := json.Marshal(plan)
		if err != nil {
			return nil, err
		}
		if c.role == "root" {
			// Exercise the observed 2000-byte telemetry preview boundary. The
			// complete JSON stays valid and is preserved in the debug store.
			for padding := range 3 {
				plan.Steps[0].Instruction = strings.Repeat("a", padding) + strings.Repeat("文", 1000)
				encoded, err = json.Marshal(plan)
				if err != nil {
					return nil, err
				}
				if !utf8.Valid(encoded[:2000]) {
					break
				}
			}
		}
		answer = string(encoded)
	case "synthesis":
	default:
		return nil, fmt.Errorf("unexpected deterministic AI purpose %q", request.Purpose)
	}
	bag := telemetry.GetBaggage(ctx)
	c.mu.Lock()
	c.calls = append(c.calls, lineageVerificationCall{
		RequestID: core.GetRequestID(ctx), Purpose: request.Purpose, Prompt: request.Prompt,
		Answer: answer, Phase: bag["phase_number"], Plan: bag["plan_id"],
	})
	c.mu.Unlock()
	return &core.AIResult{
		Response: &core.AIResponse{Content: answer, ReasoningContent: lineageVerificationReasoning,
			FinishReason: "stop", ContentSource: core.AIContentSourceFinal,
			Provider: "deterministic-test", Model: "deterministic-" + c.role,
			Usage: core.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}},
		RequestReport: &core.AIRequestReport{Provider: "deterministic-test", Purpose: request.Purpose,
			ResolvedModel: "deterministic-" + c.role, Fingerprint: "lineage-verification/v1", Stable: true},
	}, nil
}

type lineageVerificationTelemetry struct{ tracer trace.Tracer }
type lineageVerificationSpan struct{ trace.Span }

func (p lineageVerificationTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	ctx, span := p.tracer.Start(ctx, name)
	return ctx, lineageVerificationSpan{span}
}
func (lineageVerificationTelemetry) RecordMetric(string, float64, map[string]string) {}
func (s lineageVerificationSpan) SetAttribute(key string, value interface{}) {
	s.SetAttributes(attribute.String(key, fmt.Sprint(value)))
}
func (s lineageVerificationSpan) RecordError(err error) { s.Span.RecordError(err) }
func (s lineageVerificationSpan) End()                  { s.Span.End() }

func lineageVerificationObservability(t *testing.T, kind bool) (core.Telemetry, *tracetest.SpanRecorder) {
	t.Helper()
	oldTracer, oldMeter, oldPropagator := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(oldTracer)
		if kind {
			otel.SetMeterProvider(oldMeter)
		}
		otel.SetTextMapPropagator(oldPropagator)
	})
	recorder := tracetest.NewSpanRecorder()
	if kind {
		endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if endpoint == "" {
			endpoint = os.Getenv("OTEL_ENDPOINT")
		}
		require.NotEmpty(t, endpoint, "Kind verification requires the existing OTLP/HTTP collector endpoint")
		provider, err := telemetry.NewOTelProvider("lineage-verification", "agent", endpoint)
		require.NoError(t, err)
		otel.GetTracerProvider().(*sdktrace.TracerProvider).RegisterSpanProcessor(recorder)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, provider.Shutdown(ctx))
		})
		return provider, recorder
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	return lineageVerificationTelemetry{tracer: provider.Tracer("lineage-verification")}, recorder
}

func lineageVerificationStores(t *testing.T, kind bool) (ExecutionStore, LLMDebugStore) {
	t.Helper()
	config := DefaultExecutionStoreConfig()
	config.TTL, config.ErrorTTL = 30*time.Minute, 30*time.Minute
	if !kind {
		return NewExecutionStoreWithProvider(newMockStorageProvider(), config, nil), NewMemoryLLMDebugStore()
	}
	execution, err := NewRedisExecutionDebugStoreWithConfig(config,
		WithExecutionDebugTTL(30*time.Minute), WithExecutionDebugErrorTTL(30*time.Minute))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, execution.Close()) })
	debug, err := NewRedisLLMDebugStore(WithDebugTTL(30*time.Minute), WithDebugErrorTTL(30*time.Minute))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, debug.Close()) })
	return execution, debug
}

func lineageVerificationRegistration(t *testing.T, name, serverURL, capability string, kind core.CapabilityType) *AgentInfo {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)
	host, portText, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	component := core.ComponentTypeAgent
	if kind == core.CapabilityTool {
		component = core.ComponentTypeTool
	}
	return &AgentInfo{Registration: &core.ServiceInfo{ID: name, Name: name, Address: host, Port: port, Type: component},
		Capabilities: []EnhancedCapability{{Name: capability, Type: kind, Endpoint: "/api/capabilities/" + capability,
			Parameters: []Parameter{{Name: "query", Type: "string", Required: true}}}}}
}

// Manual local command:
// go test -tags=lineageintegration ./orchestration -run '^TestDelegatedExecutionLineageHTTPIntegration$' -v
//
// TRUVAG3_LINEAGE_KIND_VERIFY=1 additionally uses the existing REDIS_URL (or
// structured Redis connection env), TRUVAG3_REDIS_NAMESPACE, and
// OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_ENDPOINT). No real AI provider is
// constructed. Synthetic records expire after 30 minutes; unrelated records
// are neither queried broadly nor deleted.
func TestDelegatedExecutionLineageHTTPIntegration(t *testing.T) {
	kind := os.Getenv("TRUVAG3_LINEAGE_KIND_VERIFY") == "1"
	provider, spans := lineageVerificationObservability(t, kind)
	store, debug := lineageVerificationStores(t, kind)
	var sourceCalls atomic.Int32
	var sourceMu sync.Mutex
	var sourceParents []core.IncomingCall
	source := httptest.NewServer(telemetry.TracingMiddleware("lineage-verification-source")(
		core.IncomingCallMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := core.ExtractRequestContext(r.Context(), r)
			call, present := core.IncomingCallFromContext(ctx)
			if !present || core.GetRequestID(ctx) != call.RequestID {
				http.Error(w, "missing source caller", http.StatusBadRequest)
				return
			}
			sourceMu.Lock()
			sourceParents = append(sourceParents, call)
			sourceMu.Unlock()
			sourceCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(lineageVerificationSource))
		}))))
	t.Cleanup(source.Close)

	type completed struct{ role, id string }
	completedRuns := make(chan completed, 4)
	var reviewersStarted atomic.Int32
	bothReviewers := make(chan struct{})
	clients := map[string]*lineageVerificationAI{}
	var orchestrators []*AIOrchestrator
	newServer := func(role string, target *AgentInfo) *httptest.Server {
		client := &lineageVerificationAI{role: role}
		clients[role] = client
		config := NewDefaultOrchestratorConfig()
		config.Name = "lineage-verification-" + role
		config.RequestIDPrefix = config.Name
		config.EnableHybridResolution, config.PlanRefinementEnabled = false, false
		config.CacheEnabled = false
		config.ExecutionOptions.MaxConcurrency = 2
		config.ExecutionOptions.TotalTimeout = 30 * time.Second
		config.ExecutionOptions.StepTimeout = 20 * time.Second
		config.ExecutionStore.TTL, config.ExecutionStore.ErrorTTL = 30*time.Minute, 30*time.Minute
		config.PlanAIOptions = &AIOptionsOverride{Model: StringPtr("deterministic-" + role), MaxTokens: IntPtr(512)}
		config.SynthesisAIOptions = &AIOptionsOverride{Model: StringPtr("deterministic-" + role), MaxTokens: IntPtr(512)}
		discovery := NewMockDiscovery()
		require.NoError(t, discovery.Register(t.Context(), target.Registration))
		o := NewAIOrchestrator(config, discovery, client)
		o.catalog.agents = map[string]*AgentInfo{target.Registration.Name: target}
		o.SetCapabilityProvider(&testCapabilityProvider{
			formattedInfo: "Agent: " + target.Registration.Name + "\n Capability: " + target.Capabilities[0].Name,
			agentNames:    []string{target.Registration.Name},
		})
		o.SetTelemetry(provider)
		o.SetLogger(&core.NoOpLogger{})
		o.SetExecutionStore(store)
		o.SetLLMDebugStore(debug)
		o.executor.httpClient = telemetry.NewTracedHTTPClient(nil)
		orchestrators = append(orchestrators, o)
		t.Cleanup(func() { shutdownRequiredHookFixture(t, o) })
		server := httptest.NewServer(telemetry.TracingMiddleware(config.Name)(core.IncomingCallMiddleware(nil)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if role == "reviewer" {
					if reviewersStarted.Add(1) == 2 {
						close(bothReviewers)
					}
					select {
					case <-bothReviewers:
					case <-r.Context().Done():
						http.Error(w, "concurrent review was cancelled", http.StatusGatewayTimeout)
						return
					}
				}
				var input struct {
					Query string `json:"query"`
					Data  *struct {
						Query string `json:"query"`
					} `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					http.Error(w, "invalid synthetic request", http.StatusBadRequest)
					return
				}
				// Direct fixture ingress uses query; framework agent dispatch
				// sends the authoritative query inside its data envelope.
				query := input.Query
				if input.Data != nil {
					query = input.Data.Query
				}
				if query == "" {
					http.Error(w, "missing synthetic query", http.StatusBadRequest)
					return
				}
				ctx := core.ExtractRequestContext(r.Context(), r)
				response, execution, err := o.ProcessRequestWithExecution(ctx, query, nil)
				if err != nil || response == nil || execution == nil || !execution.Success {
					http.Error(w, fmt.Sprintf("synthetic execution failed: %v", err), http.StatusInternalServerError)
					return
				}
				completedRuns <- completed{role: role, id: response.RequestID}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: map[string]interface{}{
					"request_id": response.RequestID, "response": response.Response,
				}})
			}))))
		t.Cleanup(server.Close)
		return server
	}
	reviewer := newServer("reviewer", lineageVerificationRegistration(t, "lineage-verification-source", source.URL, "read_source", core.CapabilityTool))
	child := newServer("child", lineageVerificationRegistration(t, "lineage-verification-reviewer", reviewer.URL, "invoke", core.CapabilityOrchestrator))
	root := newServer("root", lineageVerificationRegistration(t, "lineage-verification-child", child.URL, "invoke", core.CapabilityOrchestrator))

	conversationID := fmt.Sprintf("lineage-verification-%d", time.Now().UnixNano())
	caller := telemetry.WithBaggage(t.Context(), "request_id", "outside-caller", "phase_number", "4", "plan_id", "outside-plan")
	caller = core.WithPlanID(core.WithPhaseNumber(core.WithRequestID(caller, "outside-caller"), 4), "outside-plan")
	caller, topSpan := otel.Tracer("lineage-verification").Start(caller, "lineage-verification.manual-request")
	traceID := trace.SpanContextFromContext(caller).TraceID().String()
	req, err := http.NewRequestWithContext(caller, http.MethodPost, root.URL+"/api/capabilities/invoke",
		bytes.NewBufferString(`{"query":"verify exact **中文** <request>"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TruvaG3-Conversation-ID", conversationID)
	httpClient := telemetry.NewTracedHTTPClient(nil)
	httpClient.Timeout = 30 * time.Second
	t.Cleanup(httpClient.CloseIdleConnections)
	response, err := httpClient.Do(req)
	require.NoError(t, err)
	var envelope core.ToolResponse
	decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
	_ = response.Body.Close()
	topSpan.End()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, decodeErr)
	require.True(t, envelope.Success)
	for _, o := range orchestrators {
		o.executionWg.Wait()
	}
	require.EqualValues(t, 2, sourceCalls.Load())
	require.EqualValues(t, 2, reviewersStarted.Load())
	require.Equal(t, "outside-caller", core.GetRequestID(caller))
	require.Equal(t, 4, core.GetPhaseNumber(caller))
	require.Equal(t, "outside-plan", telemetry.GetBaggage(caller)["plan_id"])

	byRole := map[string][]*StoredExecution{}
	completedIDs := map[string]string{}
	for range 4 {
		run := <-completedRuns
		require.NotContains(t, completedIDs, run.id, "concurrent HTTP executions reused request identity: role=%s previous=%s", run.role, completedIDs[run.id])
		completedIDs[run.id] = run.role
		record, err := store.Get(t.Context(), run.id)
		require.NoError(t, err)
		require.NotNil(t, record)
		require.Equal(t, traceID, record.TraceID)
		require.Equal(t, conversationID, ExecutionConversationID(record))
		require.Equal(t, RequestLifecycleCompleted, record.Lifecycle.State)
		require.True(t, record.Result.Success)
		byRole[run.role] = append(byRole[run.role], record)
	}
	require.Len(t, byRole["root"], 1)
	require.Len(t, byRole["child"], 1)
	require.Len(t, byRole["reviewer"], 2)
	rootRecord, childRecord := byRole["root"][0], byRole["child"][0]
	require.Equal(t, "verify exact **中文** <request>", rootRecord.OriginalRequest)
	require.Equal(t, "delegate exact **中文** <input>", childRecord.OriginalRequest)
	require.Empty(t, rootRecord.ParentRequestID)
	require.Equal(t, rootRecord.RequestID, rootRecord.OriginalRequestID)
	require.Equal(t, rootRecord.RequestID, childRecord.ParentRequestID)
	require.Equal(t, "step-1", childRecord.ParentStepID)
	parentSteps := map[string]bool{}
	reviewerIDs := map[string]bool{}
	for _, record := range byRole["reviewer"] {
		reviewerIDs[record.RequestID] = true
		require.Equal(t, childRecord.RequestID, record.ParentRequestID)
		require.Equal(t, rootRecord.RequestID, record.OriginalRequestID)
		require.Equal(t, map[string]string{
			"step-17": "review candidate one", "step-18": "review candidate two",
		}[record.ParentStepID], record.OriginalRequest, "review must preserve its own delegated query")
		parentSteps[record.ParentStepID] = true
		require.Len(t, record.Result.Steps, 1)
		require.Len(t, record.Result.Steps[0].Dispatches, 1)
		require.Equal(t, lineageVerificationSource, record.Result.Steps[0].Dispatches[0].Response)
	}
	require.Equal(t, map[string]bool{"step-17": true, "step-18": true}, parentSteps)
	sourceMu.Lock()
	for _, origin := range sourceParents {
		require.Contains(t, reviewerIDs, origin.RequestID)
		require.Equal(t, "step-1", origin.StepID)
		require.Equal(t, rootRecord.RequestID, origin.OriginalRequestID)
	}
	sourceMu.Unlock()

	for role, client := range clients {
		client.mu.Lock()
		calls := append([]lineageVerificationCall(nil), client.calls...)
		client.mu.Unlock()
		require.Len(t, calls, 2*len(byRole[role]))
		for _, record := range byRole[role] {
			llm, err := debug.GetRecord(t.Context(), record.RequestID)
			require.NoError(t, err)
			require.Len(t, llm.Interactions, 2)
			matchedCalls := 0
			for _, call := range calls {
				if call.RequestID != record.RequestID {
					continue
				}
				matchedCalls++
				if call.Purpose == "planning" {
					require.Empty(t, call.Phase, "initial planning inherited a caller phase")
					require.Empty(t, call.Plan, "initial planning inherited a caller plan")
					if role == "root" {
						require.Greater(t, len(call.Answer), 2000)
						require.True(t, utf8.ValidString(call.Answer))
						require.False(t, utf8.ValidString(call.Answer[:2000]), "fixture must reproduce the old preview failure")
					}
				}
				found := false
				for _, interaction := range llm.Interactions {
					if interaction.Prompt == call.Prompt && interaction.Response == call.Answer {
						found = true
						require.Equal(t, lineageVerificationReasoning, interaction.ReasoningContent)
						require.Equal(t, "stop", interaction.FinishReason)
						require.Equal(t, core.AIContentSourceFinal, interaction.ContentSource)
						require.Equal(t, "deterministic-test", interaction.Provider)
					}
				}
				require.True(t, found, "actual call absent or payload changed in its own debug record")
			}
			require.Equal(t, 2, matchedCalls, "AI calls were assigned to a different request")
		}
	}
	spanByID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	requestSpans := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range spans.Ended() {
		lineageVerificationUTF8Attributes(t, span.Attributes())
		for _, event := range span.Events() {
			lineageVerificationUTF8Attributes(t, event.Attributes)
		}
		spanByID[span.SpanContext().SpanID()] = span
		if span.Name() == "orchestrator.process_request" {
			for _, attr := range span.Attributes() {
				if attr.Key == "request_id" {
					requestSpans[attr.Value.AsString()] = span
				}
			}
		}
	}
	require.Len(t, requestSpans, 4)
	for _, records := range byRole {
		for _, record := range records {
			span := requestSpans[record.RequestID]
			require.NotNil(t, span)
			require.Equal(t, traceID, span.SpanContext().TraceID().String())
			attributes := map[string]string{}
			for _, attr := range span.Attributes() {
				attributes[string(attr.Key)] = attr.Value.AsString()
			}
			require.Equal(t, record.OriginalRequestID, attributes["original_request_id"])
			require.Equal(t, record.ParentRequestID, attributes["parent_request_id"])
			require.Equal(t, record.ParentStepID, attributes["parent_step_id"])
			if record.ParentRequestID == "" {
				continue
			}
			ancestor := spanByID[span.Parent().SpanID()]
			for ancestor != nil && ancestor.Name() != "orchestrator.process_request" {
				ancestor = spanByID[ancestor.Parent().SpanID()]
			}
			require.NotNil(t, ancestor, "trace ancestry lost the immediate caller request")
			require.Equal(t, requestSpans[record.ParentRequestID].SpanContext().SpanID(), ancestor.SpanContext().SpanID())
		}
	}
	t.Logf("lineage-verification provider=deterministic-test kind=%t trace_id=%s root_request_id=%s child_request_id=%s reviewer_request_ids=%s,%s conversation_id=%s",
		kind, traceID, rootRecord.RequestID, childRecord.RequestID,
		byRole["reviewer"][0].RequestID, byRole["reviewer"][1].RequestID, conversationID)
}

var _ core.AIRequestClient = (*lineageVerificationAI)(nil)

// A real phase deadline supplies the failure. Each child finishes its own
// managed execution but withholds its HTTP response, so the failed root cannot
// use returned child results to reconstruct the family. No root record or step
// result is manufactured by the test.
func TestDelegatedExecutionLineageFailedRootHTTPIntegration(t *testing.T) {
	kind := os.Getenv("TRUVAG3_LINEAGE_KIND_VERIFY") == "1"
	provider, spans := lineageVerificationObservability(t, kind)
	store, debug := lineageVerificationStores(t, kind)
	var sourceCalls atomic.Int32
	source := httptest.NewServer(telemetry.TracingMiddleware("lineage-verification-source")(
		core.IncomingCallMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			sourceCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(lineageVerificationSource))
		}))))
	t.Cleanup(source.Close)
	type outcome struct {
		role, id string
		err      error
	}
	outcomes := make(chan outcome, 6)
	var orchestrators []*AIOrchestrator
	newServer := func(role string, targets ...*AgentInfo) *httptest.Server {
		client := &lineageVerificationAI{role: role}
		config := NewDefaultOrchestratorConfig()
		config.Name, config.RequestIDPrefix = "lineage-verification-"+role, "lineage-verification-"+role
		config.EnableHybridResolution, config.PlanRefinementEnabled, config.CacheEnabled = false, false, false
		config.ExecutionOptions.MaxConcurrency = 3
		config.ExecutionOptions.TotalTimeout, config.ExecutionOptions.StepTimeout = 30*time.Second, 20*time.Second
		config.ExecutionStore.TTL, config.ExecutionStore.ErrorTTL = 30*time.Minute, 30*time.Minute
		config.PlanAIOptions = &AIOptionsOverride{Model: StringPtr("deterministic-" + role), MaxTokens: IntPtr(512)}
		config.SynthesisAIOptions = &AIOptionsOverride{Model: StringPtr("deterministic-" + role), MaxTokens: IntPtr(512)}
		if role == "failed-root" {
			config.IterativePlanning.PhaseTimeout = 2 * time.Second
			// The harness raises a phase deadline to HITL+step allowance for
			// delegation. Keep that existing allowance coherent with this
			// short manual fixture so the real phase deadline fires first.
			config.HITL.DefaultTimeout = time.Second
			config.ExecutionOptions.StepTimeout = time.Second
		}
		discovery := NewMockDiscovery()
		catalog := map[string]*AgentInfo{}
		names := []string{}
		formatted := ""
		for _, target := range targets {
			require.NoError(t, discovery.Register(t.Context(), target.Registration))
			catalog[target.Registration.Name] = target
			names = append(names, target.Registration.Name)
			formatted += "Agent: " + target.Registration.Name + "\n Capability: " + target.Capabilities[0].Name + "\n"
		}
		o := NewAIOrchestrator(config, discovery, client)
		o.catalog.agents = catalog
		o.SetCapabilityProvider(&testCapabilityProvider{formattedInfo: formatted, agentNames: names})
		o.SetTelemetry(provider)
		o.SetLogger(&core.NoOpLogger{})
		o.SetExecutionStore(store)
		o.SetLLMDebugStore(debug)
		o.executor.httpClient = telemetry.NewTracedHTTPClient(nil)
		orchestrators = append(orchestrators, o)
		t.Cleanup(func() { shutdownRequiredHookFixture(t, o) })
		server := httptest.NewServer(telemetry.TracingMiddleware(config.Name)(core.IncomingCallMiddleware(nil)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Query string `json:"query"`
					Data  *struct {
						Query string `json:"query"`
					} `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					http.Error(w, "invalid synthetic request", http.StatusBadRequest)
					return
				}
				query := input.Query
				if input.Data != nil {
					query = input.Data.Query
				}
				if query == "" {
					http.Error(w, "missing synthetic query", http.StatusBadRequest)
					return
				}
				ctx := core.ExtractRequestContext(r.Context(), r)
				response, _, err := o.ProcessRequestWithExecution(ctx, query, nil)
				id := ""
				if response != nil {
					id = response.RequestID
				} else {
					// This is the actual generated ID observed during the real
					// planning call, not a fabricated root execution snapshot.
					client.mu.Lock()
					if len(client.calls) != 0 {
						id = client.calls[0].RequestID
					}
					client.mu.Unlock()
				}
				outcomes <- outcome{role: role, id: id, err: err}
				if err != nil {
					http.Error(w, "synthetic root phase deadline", http.StatusGatewayTimeout)
					return
				}
				if role == "leaf-child" || role == "fanout-child" {
					// Persisted child work survives even though its response
					// never reaches the parent before the parent's deadline.
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: map[string]interface{}{
					"request_id": response.RequestID, "response": response.Response,
				}})
			}))))
		t.Cleanup(server.Close)
		return server
	}
	sourceInfo := lineageVerificationRegistration(t, "lineage-verification-source", source.URL, "read_source", core.CapabilityTool)
	reviewer := newServer("reviewer", sourceInfo)
	fanout := newServer("fanout-child", lineageVerificationRegistration(t, "lineage-verification-reviewer", reviewer.URL, "invoke", core.CapabilityOrchestrator))
	leaf := newServer("leaf-child", sourceInfo)
	root := newServer("failed-root",
		lineageVerificationRegistration(t, "lineage-verification-fanout-child", fanout.URL, "invoke", core.CapabilityOrchestrator),
		lineageVerificationRegistration(t, "lineage-verification-leaf-child", leaf.URL, "invoke", core.CapabilityOrchestrator))
	conversationID := fmt.Sprintf("lineage-verification-failed-%d", time.Now().UnixNano())
	caller, span := otel.Tracer("lineage-verification").Start(t.Context(), "lineage-verification.failed-root-request")
	traceID := trace.SpanContextFromContext(caller).TraceID().String()
	req, err := http.NewRequestWithContext(caller, http.MethodPost, root.URL+"/api/capabilities/invoke", bytes.NewBufferString(`{"query":"verify failed root lineage"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TruvaG3-Conversation-ID", conversationID)
	client := telemetry.NewTracedHTTPClient(nil)
	client.Timeout = 10 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Do(req)
	require.NoError(t, err)
	_ = response.Body.Close()
	span.End()
	require.Equal(t, http.StatusGatewayTimeout, response.StatusCode)
	for _, o := range orchestrators {
		o.executionWg.Wait()
	}
	byRole := map[string][]*StoredExecution{}
	for range 6 {
		select {
		case result := <-outcomes:
			require.NotEmpty(t, result.id)
			if result.role == "failed-root" {
				require.ErrorIs(t, result.err, context.DeadlineExceeded)
			} else {
				require.NoError(t, result.err)
			}
			record, err := store.Get(t.Context(), result.id)
			require.NoError(t, err)
			require.Equal(t, traceID, record.TraceID)
			require.Equal(t, conversationID, ExecutionConversationID(record))
			byRole[result.role] = append(byRole[result.role], record)
		case <-time.After(time.Second):
			t.Fatal("expected six actual HTTP execution outcomes")
		}
	}
	require.Len(t, byRole["failed-root"], 1)
	require.Len(t, byRole["fanout-child"], 1)
	require.Len(t, byRole["leaf-child"], 2)
	require.Len(t, byRole["reviewer"], 2)
	rootRecord := byRole["failed-root"][0]
	require.Equal(t, "verify failed root lineage", rootRecord.OriginalRequest)
	require.Equal(t, RequestLifecycleFailed, rootRecord.Lifecycle.State)
	require.NotNil(t, rootRecord.Result)
	require.False(t, rootRecord.Result.Success)
	require.Len(t, rootRecord.Result.Steps, 3, "preserve the three actual failed parent dispatches")
	failedParentSteps := map[string]bool{}
	for _, step := range rootRecord.Result.Steps {
		failedParentSteps[step.StepID] = true
		require.False(t, step.Success)
		require.False(t, step.Skipped, "each parent step made an actual HTTP dispatch")
		require.Empty(t, step.Response, "withheld child responses must not become synthetic successful parent evidence")
		require.NotEmpty(t, step.Error)
		require.Equal(t, 1, step.DispatchCount)
		require.Len(t, step.Dispatches, 1)
		require.Equal(t, 1, step.Dispatches[0].Number)
		require.False(t, step.Dispatches[0].Success)
		require.Empty(t, step.Dispatches[0].Response)
		require.NotEmpty(t, step.Dispatches[0].Error, "retain the original HTTP dispatch failure")
		require.Contains(t, step.Dispatches[0].Error, "context deadline exceeded")
	}
	require.Equal(t, map[string]bool{"step-1": true, "step-2": true, "step-3": true}, failedParentSteps)
	require.Empty(t, rootRecord.FinalResponse)
	require.EqualValues(t, 4, sourceCalls.Load())
	children := append([]*StoredExecution{byRole["fanout-child"][0]}, byRole["leaf-child"]...)
	childSteps := map[string]bool{}
	for _, child := range children {
		require.Equal(t, rootRecord.RequestID, child.ParentRequestID)
		require.Equal(t, map[string]string{
			"step-1": "delegate independent reviews", "step-2": "inspect candidate two", "step-3": "inspect candidate three",
		}[child.ParentStepID], child.OriginalRequest, "child must preserve its own delegated query")
		childSteps[child.ParentStepID] = true
	}
	require.Equal(t, map[string]bool{"step-1": true, "step-2": true, "step-3": true}, childSteps)
	for _, reviewer := range byRole["reviewer"] {
		require.Equal(t, byRole["fanout-child"][0].RequestID, reviewer.ParentRequestID)
		require.Equal(t, map[string]string{
			"step-17": "review candidate one", "step-18": "review candidate two",
		}[reviewer.ParentStepID], reviewer.OriginalRequest, "review must preserve its own delegated query")
	}
	for role, records := range byRole {
		if role == "failed-root" {
			continue
		}
		for _, record := range records {
			require.Equal(t, RequestLifecycleCompleted, record.Lifecycle.State)
			require.True(t, record.Result.Success)
			require.Equal(t, rootRecord.RequestID, record.OriginalRequestID)
			llm, err := debug.GetRecord(t.Context(), record.RequestID)
			require.NoError(t, err)
			require.Len(t, llm.Interactions, 2)
		}
	}
	requestSpans := 0
	for _, ended := range spans.Ended() {
		if ended.Name() == "orchestrator.process_request" {
			requestSpans++
			require.Equal(t, traceID, ended.SpanContext().TraceID().String())
		}
	}
	require.Equal(t, 6, requestSpans)
	t.Logf("lineage-verification failed-root provider=deterministic-test kind=%t trace_id=%s root_request_id=%s child_records=3 reviewer_records=2 root_step_results=3 conversation_id=%s",
		kind, traceID, rootRecord.RequestID, conversationID)
}

func lineageVerificationUTF8Attributes(t *testing.T, attributes []attribute.KeyValue) {
	t.Helper()
	for _, field := range attributes {
		require.True(t, utf8.ValidString(string(field.Key)))
		switch field.Value.Type() {
		case attribute.STRING:
			require.Truef(t, utf8.ValidString(field.Value.AsString()), "invalid UTF-8 in span/event attribute %q", field.Key)
		case attribute.STRINGSLICE:
			for _, value := range field.Value.AsStringSlice() {
				require.Truef(t, utf8.ValidString(value), "invalid UTF-8 in span/event attribute %q", field.Key)
			}
		}
	}
}

// This no-model probe exercises both real Redis writers when the existing Kind
// opt-in is enabled. It leaves only two explicitly synthetic, TTL-owned LLM
// records and exports a main/background trace pair for each writer order.
func TestLLMDebugTraceProvenanceIntegration(t *testing.T) {
	testLLMDebugTraceProvenanceIntegration(t, false)
}

func TestLLMDebugSaturatedRootIntegration(t *testing.T) {
	testLLMDebugTraceProvenanceIntegration(t, true)
}

func testLLMDebugTraceProvenanceIntegration(t *testing.T, saturated bool) {
	kind := os.Getenv("TRUVAG3_LINEAGE_KIND_VERIFY") == "1"
	observability, spans := lineageVerificationObservability(t, kind)
	keyspace := defaultRedisKeyspace()
	var store *RedisLLMDebugStore
	if kind {
		var err error
		keyspace, err = redisKeyspaceFromEnvironment()
		require.NoError(t, err)
		store, err = NewRedisLLMDebugStore(WithDebugTTL(30*time.Minute), WithDebugErrorTTL(30*time.Minute))
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
	} else {
		_, store = setupRedisLLMDebugTestStore(t)
		store.ttl, store.errorTTL = 30*time.Minute, 30*time.Minute
	}
	recorder, err := telemetry.NewRedisLLMCallRecorderWithClient(store.client, keyspace,
		telemetry.WithRecorderTTL(30*time.Minute), telemetry.WithRecorderErrorTTL(30*time.Minute))
	require.NoError(t, err)
	const payload = "synthetic no-AI **中文** <unchanged>\n{\"exact\":true}"
	for index, order := range [][]string{{"typed", "telemetry"}, {"telemetry", "typed"}} {
		requestID := fmt.Sprintf("lineage-verification-llm-trace-%d-%d", time.Now().UnixNano(), index)
		rootID := requestID
		base := context.Background()
		if saturated {
			rootID = requestID + strings.Repeat("r", core.MaxIncomingCallIDLength-len(requestID))
			base = telemetry.WithoutBaggageMember(correlationCapacityContext(t, 18, 500), "original_request_id")
			base = incomingExecutionContext(t, base, core.IncomingCall{RequestID: "parent", StepID: "step-1", OriginalRequestID: rootID})
		}
		mainCtx, mainSpan := observability.StartSpan(base, "lineage-verification.llm-debug-main")
		if saturated {
			o := &AIOrchestrator{logger: &core.NoOpLogger{}}
			var correlation requestCorrelation
			mainCtx, correlation = o.beginExecutionCorrelation(mainCtx, requestID)
			require.Equal(t, rootID, correlation.OriginalRequestID)
			require.Equal(t, rootID, core.GetOriginalRequestID(mainCtx))
			require.Empty(t, telemetry.GetBaggage(mainCtx)["original_request_id"])
		} else {
			mainCtx = core.WithRequestID(mainCtx, requestID)
			mainCtx = telemetry.WithBaggage(mainCtx, "request_id", requestID)
			mainCtx = telemetry.WithBaggage(mainCtx, "original_request_id", requestID)
		}
		mainCtx = telemetry.WithBaggage(mainCtx, "agent_name", "lineage-verification")
		mainTrace := telemetry.GetTraceContext(mainCtx)
		require.NotEmpty(t, mainTrace.TraceID)
		mainSpan.SetAttribute("request_id", requestID)
		mainSpan.SetAttribute("provider", "deterministic-test")
		backgroundCtx, endBackground := telemetry.StartLinkedSpan(
			telemetry.CopyBaggage(context.Background(), mainCtx),
			"lineage-verification.llm-debug-background", mainTrace.TraceID, mainTrace.SpanID,
			map[string]string{"request_id": requestID, "provider": "deterministic-test"},
		)
		backgroundTrace := telemetry.GetTraceContext(backgroundCtx).TraceID
		if saturated {
			// Mirror the request-owned typed copy at the detached hook boundary;
			// the default unit suite separately exercises that actual hook.
			backgroundCtx = core.WithRequestID(backgroundCtx, requestID)
			backgroundCtx = core.WithOriginalRequestID(backgroundCtx, core.GetOriginalRequestID(mainCtx))
			require.Empty(t, telemetry.GetBaggage(backgroundCtx)["original_request_id"])
		}
		require.NotEmpty(t, backgroundTrace)
		require.NotEqual(t, mainTrace.TraceID, backgroundTrace)
		for call, writer := range order {
			ctx := mainCtx
			if call > 0 {
				ctx = backgroundCtx
			}
			if writer == "typed" {
				err = store.RecordInteraction(ctx, requestID, LLMInteraction{
					Type: "deterministic_trace_verification", Prompt: payload, Response: payload,
					ReasoningContent: payload, Provider: "deterministic-test", Model: "no-model-call",
					Timestamp: time.Now(), Success: true,
				})
			} else {
				err = recorder.RecordLLMCall(ctx, requestID, telemetry.LLMCallRecord{
					CallType: "agent_llm_call", Prompt: payload, Response: payload,
					ReasoningContent: payload, Provider: "deterministic-test", Model: "no-model-call",
					Timestamp: time.Now(), Success: true,
				})
			}
			require.NoError(t, err)
		}
		endBackground()
		mainSpan.End()
		record, err := store.GetRecord(t.Context(), requestID)
		require.NoError(t, err)
		require.Equal(t, requestID, record.RequestID)
		require.Equal(t, rootID, record.OriginalRequestID)
		require.Equal(t, mainTrace.TraceID, record.TraceID)
		require.Len(t, record.Interactions, 2)
		require.Equal(t, mainTrace.TraceID, record.Interactions[0].TraceID)
		require.Equal(t, backgroundTrace, record.Interactions[1].TraceID)
		for _, interaction := range record.Interactions {
			require.Equal(t, payload, interaction.Prompt)
			require.Equal(t, payload, interaction.Response)
			require.Equal(t, payload, interaction.ReasoningContent)
		}
		for _, key := range []string{store.metaKey(requestID), store.interactionsKey(requestID)} {
			ttl, err := store.client.PTTL(t.Context(), key).Result()
			require.NoError(t, err)
			require.Greater(t, ttl, 29*time.Minute)
			require.LessOrEqual(t, ttl, 30*time.Minute)
		}
		linked := false
		for _, span := range spans.Ended() {
			if span.SpanContext().TraceID().String() != backgroundTrace {
				continue
			}
			for _, link := range span.Links() {
				if link.SpanContext.TraceID().String() == mainTrace.TraceID && link.SpanContext.SpanID().String() == mainTrace.SpanID {
					linked = true
				}
			}
		}
		require.True(t, linked, "background call must retain its link to the main trace")
		t.Logf("lineage-verification llm-debug-traces provider=deterministic-test kind=%t saturated_root=%t root_bytes=%d request_id=%s first_writer=%s aggregate_trace_id=%s background_trace_id=%s interactions=2",
			kind, saturated, len(rootID), requestID, order[0], record.TraceID, backgroundTrace)
	}
}
