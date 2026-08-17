package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type skillOTelTestTelemetry struct{ tracer trace.Tracer }

func (telemetry skillOTelTestTelemetry) StartSpan(
	ctx context.Context,
	name string,
) (context.Context, core.Span) {
	ctx, span := telemetry.tracer.Start(ctx, name)
	return ctx, &skillOTelTestSpan{span: span}
}

func (skillOTelTestTelemetry) RecordMetric(string, float64, map[string]string) {}

type skillOTelTestSpan struct{ span trace.Span }

func (span *skillOTelTestSpan) End()                        { span.span.End() }
func (*skillOTelTestSpan) SetAttribute(string, interface{}) {}
func (span *skillOTelTestSpan) RecordError(err error)       { span.span.RecordError(err) }

type skillEvidenceAIClient struct{}

func (skillEvidenceAIClient) GenerateResponse(context.Context, string, *core.AIOptions) (*core.AIResponse, error) {
	return nil, errors.New("legacy path must not be used")
}

func (skillEvidenceAIClient) Generate(_ context.Context, request *core.AIRequest) (*core.AIResult, error) {
	return &core.AIResult{
		Response: &core.AIResponse{
			Content: `{"selected_skills":[{"namespace":"travel","name":"weather","reason":"Weather was requested."}]}`,
			Model:   "effective-model", Provider: "test-provider",
		},
		RequestReport: &core.AIRequestReport{
			Provider: "test-provider", Purpose: request.Purpose,
			RequestedModel: "requested-model", ResolvedModel: "effective-model",
			EffectiveTemperature: core.SetAIParameter(float32(0.02)),
			EffectiveMaxTokens:   core.SetAIParameter(321),
			Adjustments:          []core.AIRequestAdjustment{{Source: "test", Rule: "normalize", Path: "temperature", Action: "replace", Reason: "test"}},
			Fingerprint:          "sha256:test", Stable: true,
		},
	}, nil
}

type deadlineSkillRegistry struct{}

func (deadlineSkillRegistry) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	return nil, nil
}
func (deadlineSkillRegistry) ResolveCandidates(context.Context, []SkillCandidateRequest) ([]SkillCandidate, error) {
	return nil, nil
}
func (deadlineSkillRegistry) GetManifest(ctx context.Context, _ SkillVersionRef) (SkillManifest, error) {
	<-ctx.Done()
	return SkillManifest{}, ctx.Err()
}
func (deadlineSkillRegistry) GetResource(ctx context.Context, _ SkillResourceRef) (SkillResource, error) {
	<-ctx.Done()
	return SkillResource{}, ctx.Err()
}

func TestSkillRuntimeEmitsBoundedSpanAndMetricTopology(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	resourceRequest := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "forecast", Description: "Forecast detail", LoadWhen: "A forecast is requested.",
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain",
		Content: "Use the latest forecast.",
	})
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	capture := &mockTelemetry{}
	runtime.telemetry = capture
	ctx, err := WithTrustedSkillResourceRequests(t.Context(), resourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithRequestID(ctx, "request-skills")
	state, _, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = runtime.prepareInitialBoundary(ctx, state, "forecast", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Debug.ContentLoads) != 2 {
		t.Fatalf("content loads = %#v", state.Debug.ContentLoads)
	}
	for _, load := range state.Debug.ContentLoads {
		if load.ByteEstimate <= 0 || load.TokenEstimate <= 0 || load.DurationMs < 0 {
			t.Fatalf("content load evidence = %#v", load)
		}
	}
	wantSpans := []string{
		"orchestrator.skills.pin_candidates",
		"skills.registry.resolve_candidates",
		"orchestrator.skills.activate",
		"skills.registry.load_manifest",
		"orchestrator.skills.resolve_resources",
		"skills.registry.load_resource",
	}
	for _, want := range wantSpans {
		if !containsSkillString(capture.spans, want) {
			t.Fatalf("spans = %v; missing %s", capture.spans, want)
		}
	}
	if capture.metrics[skillOperationTotalMetric] != 1 ||
		capture.metrics[skillOperationDurationMetric] < 0 ||
		capture.metrics[skillPromptTokensMetric] <= 0 {
		t.Fatalf("skill metrics = %#v", capture.metrics)
	}
	assertSkillMetricRecordsUseExactLabels(t, capture.metricRecords)
}

func TestSkillResponseCacheDecisionEmitsBoundedSkillMetric(t *testing.T) {
	capture := &mockTelemetry{}
	hook := &decisionHook{
		name: "cache",
		decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			return &core.PipelineShortCircuitDecision{
				ShortCircuit:  &core.PipelineShortCircuit{Response: "cached"},
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{reservedSkillCacheDimension: "sha256:current"},
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}, telemetry: capture}
	decision, err := orchestrator.runBeforePlanningHooks(
		t.Context(),
		&core.PipelineContext{},
		newPipelineGate(map[string]string{reservedSkillCacheDimension: "sha256:current"}, false),
		reservedSkillCacheDimension,
	)
	if err != nil || decision == nil {
		t.Fatalf("cache decision = %#v, %v", decision, err)
	}

	var records []mockMetricRecord
	for _, record := range capture.metricRecords {
		if record.name == skillOperationTotalMetric || record.name == skillOperationDurationMetric {
			records = append(records, record)
		}
	}
	if len(records) != 2 {
		t.Fatalf("skill response-cache metrics = %#v", records)
	}
	want := map[string]string{
		"module": telemetry.ModuleOrchestration, "stage": "response_cache",
		"boundary": "request_start", "outcome": "accepted",
	}
	for _, record := range records {
		if !reflect.DeepEqual(record.labels, want) {
			t.Fatalf("%s labels = %#v, want %#v", record.name, record.labels, want)
		}
	}
	assertSkillMetricRecordsUseExactLabels(t, records)
}

func TestNoSkillAuthoritativeShortCircuitDoesNotEmitSkillMetric(t *testing.T) {
	capture := &mockTelemetry{}
	hook := &decisionHook{
		name: "policy",
		decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			return &core.PipelineShortCircuitDecision{
				ShortCircuit: &core.PipelineShortCircuit{Response: "denied"},
				Kind:         core.PipelineShortCircuitAuthoritative,
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}, telemetry: capture}
	decision, err := orchestrator.runBeforePlanningHooks(
		t.Context(), &core.PipelineContext{}, newPipelineGate(nil, false), reservedSkillCacheDimension,
	)
	if err != nil || decision == nil {
		t.Fatalf("authoritative decision = %#v, %v", decision, err)
	}
	for _, record := range capture.metricRecords {
		if record.name == skillOperationTotalMetric || record.name == skillOperationDurationMetric {
			t.Fatalf("no-skill request emitted skill metric: %#v", record)
		}
	}
}

func TestDisabledSkillsRejectingSkillCacheEntryEmitsSkillMetric(t *testing.T) {
	capture := &mockTelemetry{}
	hook := &decisionHook{
		name: "cache",
		decision: func(core.PipelineGate) (*core.PipelineShortCircuitDecision, error) {
			return &core.PipelineShortCircuitDecision{
				ShortCircuit:  &core.PipelineShortCircuit{Response: "stale"},
				Kind:          core.PipelineShortCircuitCache,
				CachedAgainst: map[string]string{reservedSkillCacheDimension: "sha256:old"},
			}, nil
		},
	}
	orchestrator := &AIOrchestrator{pipelineHooks: []core.PipelineHook{hook}, telemetry: capture}
	decision, err := orchestrator.runBeforePlanningHooks(
		t.Context(), &core.PipelineContext{}, newPipelineGate(nil, false), reservedSkillCacheDimension,
	)
	if err != nil || decision != nil {
		t.Fatalf("stale skill cache decision = %#v, %v", decision, err)
	}
	found := false
	for _, record := range capture.metricRecords {
		if record.name == skillOperationTotalMetric && record.labels["stage"] == "response_cache" {
			found = record.labels["outcome"] == "rejected"
		}
	}
	if !found {
		t.Fatalf("missing rejected skill response-cache metric: %#v", capture.metricRecords)
	}
}

func TestSkillStartupConfigValidationEmitsBoundedOutcome(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	for _, test := range []struct {
		name        string
		configure   func(*OrchestratorConfig)
		wantOutcome string
		wantError   bool
	}{
		{
			name: "success",
			configure: func(config *OrchestratorConfig) {
				config.Skills.Enabled = true
				config.Skills.Bindings = []SkillBinding{binding}
				config.SkillRegistry = activationRegistryForBindings(t, []SkillBinding{binding})
			},
			wantOutcome: "success",
		},
		{
			name: "invalid",
			configure: func(config *OrchestratorConfig) {
				binding.Required = true
				config.Skills.Bindings = []SkillBinding{binding}
			},
			wantOutcome: "error",
			wantError:   true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := NewDefaultOrchestratorConfig()
			test.configure(config)
			capture := &mockTelemetry{}
			orchestrator, err := CreateResolvedOrchestrator(config, OrchestratorDependencies{
				Discovery: NewMockDiscovery(), AIClient: NewMockAIClient(), Telemetry: capture,
			})
			if test.wantError != (err != nil) {
				t.Fatalf("CreateResolvedOrchestrator() error = %v, want error %v", err, test.wantError)
			}
			if orchestrator != nil {
				t.Cleanup(func() { _ = orchestrator.Shutdown(t.Context()) })
			}
			var records []mockMetricRecord
			for _, record := range capture.metricRecords {
				if record.name == skillOperationTotalMetric || record.name == skillOperationDurationMetric {
					records = append(records, record)
				}
			}
			if len(records) != 2 {
				t.Fatalf("config-validation metrics = %#v", records)
			}
			want := map[string]string{
				"module": telemetry.ModuleOrchestration, "stage": "config_validation",
				"boundary": "startup", "outcome": test.wantOutcome,
			}
			for _, record := range records {
				if !reflect.DeepEqual(record.labels, want) {
					t.Fatalf("%s labels = %#v, want %#v", record.name, record.labels, want)
				}
			}
			assertSkillMetricRecordsUseExactLabels(t, records)
		})
	}
}

func TestSkillConfigResolutionFailuresEmitStartupOutcome(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *mockTelemetry) error
	}{
		{
			name: "strict skill environment",
			run: func(_ *testing.T, capture *mockTelemetry) error {
				_, err := createOrchestratorFromEnvironment(
					OrchestratorDependencies{Telemetry: capture},
					lookupFromMap(map[string]string{"TRUVAG3_SKILLS_ENABLED": "not-a-boolean"}),
				)
				return err
			},
		},
		{
			name: "invalid skill code option",
			run: func(_ *testing.T, capture *mockTelemetry) error {
				_, err := CreateOrchestratorWithOptions(
					OrchestratorDependencies{Telemetry: capture},
					WithSkills(SkillConfig{DomainCompatibilityMode: "invalid"}),
				)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &mockTelemetry{}
			if err := test.run(t, capture); err == nil {
				t.Fatal("configuration resolution succeeded, want error")
			}
			found := false
			for _, record := range capture.metricRecords {
				if record.name == skillOperationTotalMetric &&
					record.labels["stage"] == "config_validation" &&
					record.labels["boundary"] == "startup" &&
					record.labels["outcome"] == "error" {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing startup config-validation failure metric: %#v", capture.metricRecords)
			}
		})
	}
}

func TestSkillContentFailureMetricReflectsRequiredPolicy(t *testing.T) {
	for _, test := range []struct {
		name       string
		required   bool
		wantAction string
	}{
		{name: "optional omission", wantAction: "omitted"},
		{name: "required failure", required: true, wantAction: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture := &mockTelemetry{}
			runtime := &skillRuntime{
				registry: deadlineSkillRegistry{}, telemetry: capture,
				config: SkillConfig{Limits: SkillRuntimeLimits{RegistryReadTimeout: time.Millisecond}},
			}
			ref := SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 1}
			_, _, err := runtime.loadSkillManifest(
				t.Context(), ref, SkillBoundaryInitialPlanning, 1, test.required,
			)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("loadSkillManifest() error = %v", err)
			}
			var integrity []mockMetricRecord
			for _, record := range capture.metricRecords {
				if record.name == skillIntegrityMetric {
					integrity = append(integrity, record)
				}
			}
			if len(integrity) != 1 || integrity[0].labels["action"] != test.wantAction {
				t.Fatalf("integrity metrics = %#v, want action %q", integrity, test.wantAction)
			}
			assertSkillMetricRecordsUseExactLabels(t, integrity)
		})
	}
}

func TestSkillContentLoadDebugRecordsIntegrityRereadOutcome(t *testing.T) {
	for _, test := range []struct {
		name        string
		recover     bool
		required    bool
		wantOutcome string
		wantRetry   string
		wantError   bool
	}{
		{name: "recovered", recover: true, wantOutcome: "verified", wantRetry: "recovered"},
		{name: "persistent optional", wantOutcome: "omitted", wantRetry: "persistent"},
		{name: "persistent required", required: true, wantOutcome: "failed", wantRetry: "persistent", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, _ := cacheTestSkillContent(t, test.name)
			corrupt := cloneSkillManifest(manifest)
			corrupt.PlanningInstructions[0] += " corrupted"
			responses := []SkillManifest{corrupt, corrupt}
			if test.recover {
				responses[1] = manifest
			}
			upstream := &cacheTestSkillRegistry{manifest: manifest, manifestResponses: responses}
			registry, err := NewImmutableCachedSkillRegistry(upstream, nil)
			if err != nil {
				t.Fatal(err)
			}
			binding := SkillBinding{
				Namespace: manifest.Ref.Ref.Namespace, Name: manifest.Ref.Ref.Name,
				Version: "published", Activation: SkillActivationAlways, Required: test.required,
			}
			config := NewDefaultOrchestratorConfig()
			config.Skills.Enabled = true
			config.Skills.Bindings = []SkillBinding{binding}
			runtime, err := newSkillRuntime(config, registry, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, _, err := runtime.PinCandidates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, state, err = runtime.prepareInitialBoundary(t.Context(), state, "weather", nil, 1)
			if test.wantError != (err != nil) {
				t.Fatalf("prepareInitialBoundary() error = %v, want error %v", err, test.wantError)
			}
			if test.required && (!errors.Is(err, ErrSkillUnavailable) || !errors.Is(err, ErrSkillIntegrity)) {
				t.Fatalf("required integrity error = %v, want unavailable and integrity classifications", err)
			}
			if len(state.Debug.ContentLoads) != 1 {
				t.Fatalf("content loads = %#v", state.Debug.ContentLoads)
			}
			load := state.Debug.ContentLoads[0]
			if load.Attempt != 2 || load.RetryOutcome != test.wantRetry ||
				load.Outcome != test.wantOutcome || load.ObservedHash == "" ||
				load.ObservedHash == load.ExpectedHash {
				t.Fatalf("integrity content-load debug = %#v", load)
			}
			if test.recover && load.DiagnosticCode != "" {
				t.Fatalf("recovered load diagnostic = %q", load.DiagnosticCode)
			}
			if !test.recover && load.DiagnosticCode != "skill_manifest_hash_mismatch" {
				t.Fatalf("persistent load diagnostic = %q", load.DiagnosticCode)
			}
		})
	}
}

func TestSkillRuntimeTraceKeepsActivationAndResourceResolutionAsSiblings(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAlways,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	resourceRequest := addActivationTestResource(t, registry, binding.Ref(), SkillResourceInput{
		Name: "forecast", Description: "Forecast detail", LoadWhen: "A forecast is requested.",
		AppliesTo: []SkillResourceScope{SkillResourcePlanning}, ContentType: "text/plain",
		Content: "Use the latest forecast.",
	})
	runtime, _ := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	tracer := provider.Tracer("skills-topology-test")
	runtime.telemetry = skillOTelTestTelemetry{tracer: tracer}

	ctx, err := WithTrustedSkillResourceRequests(t.Context(), resourceRequest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, requestSpan := tracer.Start(ctx, "orchestrator.phase.1")
	state, _, err := runtime.PinCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = runtime.prepareInitialBoundary(ctx, state, "forecast", nil, 1); err != nil {
		t.Fatal(err)
	}
	requestSpan.End()

	spans := make(map[string]sdktrace.ReadOnlySpan)
	for _, span := range recorder.Ended() {
		spans[span.Name()] = span
	}
	for _, name := range []string{
		"orchestrator.phase.1", "orchestrator.skills.activate",
		"skills.registry.load_manifest", "orchestrator.skills.resolve_resources",
		"skills.registry.load_resource",
	} {
		if spans[name] == nil {
			t.Fatalf("ended spans missing %q: %#v", name, spans)
		}
	}
	phaseID := spans["orchestrator.phase.1"].SpanContext().SpanID()
	activationID := spans["orchestrator.skills.activate"].SpanContext().SpanID()
	resourceResolutionID := spans["orchestrator.skills.resolve_resources"].SpanContext().SpanID()
	if got := spans["orchestrator.skills.activate"].Parent().SpanID(); got != phaseID {
		t.Fatalf("activation parent = %s, want phase %s", got, phaseID)
	}
	if got := spans["orchestrator.skills.resolve_resources"].Parent().SpanID(); got != phaseID {
		t.Fatalf("resource-resolution parent = %s, want phase %s", got, phaseID)
	}
	if got := spans["skills.registry.load_manifest"].Parent().SpanID(); got != activationID {
		t.Fatalf("manifest-read parent = %s, want activation %s", got, activationID)
	}
	if got := spans["skills.registry.load_resource"].Parent().SpanID(); got != resourceResolutionID {
		t.Fatalf("resource-read parent = %s, want resource resolution %s", got, resourceResolutionID)
	}
}

func TestSkillRuntimeOmitsActivationSpanWithoutEligibleCandidates(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationExplicit,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, nil, nil)
	capture := &mockTelemetry{}
	runtime.telemetry = capture
	if _, _, err := runtime.prepareInitialBoundary(t.Context(), state, "forecast", nil, 1); err != nil {
		t.Fatal(err)
	}
	if containsSkillString(capture.spans, "orchestrator.skills.activate") {
		t.Fatalf("activation span emitted without an eligible candidate: %v", capture.spans)
	}
}

func TestSkillSelectorUsesScopedPurposeAndCommonDebugEvidence(t *testing.T) {
	binding := SkillBinding{
		Namespace: "travel", Name: "weather", Version: "published",
		Activation: SkillActivationAuto,
	}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	client := &activationTestAIClient{responses: []string{
		`{"selected_skills":[{"namespace":"travel","name":"weather","reason":"Weather was requested."}]}`,
	}}
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, client, nil)
	var interaction LLMInteraction
	var purpose string
	runtime.debugRecorder = func(ctx context.Context, recorded LLMInteraction) {
		interaction = recorded
		purpose = telemetry.GetBaggage(ctx)["ai.purpose"]
	}
	_, _, err := runtime.prepareInitialBoundary(t.Context(), state, "weather", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if purpose != skillActivationAIPurpose || interaction.Type != skillActivationAIPurpose ||
		interaction.Category != "llm" || !interaction.Success ||
		interaction.SystemPrompt == "" || interaction.Prompt == "" || interaction.Attempt != 1 {
		t.Fatalf("skill selector evidence = %#v; purpose = %q", interaction, purpose)
	}
}

func TestSkillSelectorDebugCapturesEffectiveRequestEvidence(t *testing.T) {
	binding := SkillBinding{Namespace: "travel", Name: "weather", Version: "published", Activation: SkillActivationAuto}
	registry := activationRegistryForBindings(t, []SkillBinding{binding})
	runtime, state := activationRuntimeAndState(t, []SkillBinding{binding}, registry, skillEvidenceAIClient{}, func(config *OrchestratorConfig) {
		config.SkillActivationAIOptions = &AIOptionsOverride{Model: StringPtr("requested-model")}
	})
	metricCapture := &mockTelemetry{}
	runtime.telemetry = metricCapture
	var interaction LLMInteraction
	runtime.debugRecorder = func(_ context.Context, recorded LLMInteraction) { interaction = recorded }
	_, _, err := runtime.prepareInitialBoundary(t.Context(), state, "weather", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if interaction.RequestedModel != "requested-model" || interaction.EffectiveModel != "effective-model" ||
		interaction.Model != "effective-model" || interaction.Temperature != 0.02 || interaction.MaxTokens != 321 ||
		len(interaction.Adjustments) != 1 || interaction.PolicyFingerprint != "sha256:test" || !interaction.PolicyStable {
		t.Fatalf("effective selector evidence = %#v", interaction)
	}
	assertSkillMetricRecordsUseExactLabels(t, metricCapture.metricRecords)
}

func TestSkillManifestAndResourceReadsUseIndependentConfiguredTimeouts(t *testing.T) {
	runtime := &skillRuntime{
		registry: deadlineSkillRegistry{},
		config:   SkillConfig{Limits: SkillRuntimeLimits{RegistryReadTimeout: 10 * time.Millisecond}},
	}
	manifestRef := SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 1}
	if _, evidence, err := runtime.loadSkillManifest(t.Context(), manifestRef, SkillBoundaryInitialPlanning, 1, false); !errors.Is(err, context.DeadlineExceeded) || evidence.DurationMs < 1 {
		t.Fatalf("manifest read evidence=%#v error=%v", evidence, err)
	}
	resourceRef := SkillResourceRef{Skill: manifestRef, Name: "forecast"}
	if _, evidence, err := runtime.loadSkillResource(t.Context(), resourceRef, SkillBoundaryInitialPlanning, 1, false); !errors.Is(err, context.DeadlineExceeded) || evidence.DurationMs < 1 {
		t.Fatalf("resource read evidence=%#v error=%v", evidence, err)
	}
}

func TestSkillRegistryReadSpansRepresentUpstreamMissesNotImmutableCacheHits(t *testing.T) {
	manifest, resource := cacheTestSkillContent(t, "registry-span")
	upstream := &cacheTestSkillRegistry{manifest: manifest, resource: resource}
	cache, err := NewByteLRUSkillContentCache(1024 * 1024)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewImmutableCachedSkillRegistry(upstream, cache)
	if err != nil {
		t.Fatal(err)
	}
	capture := &mockTelemetry{}
	runtime := &skillRuntime{
		registry: registry,
		config: SkillConfig{Limits: SkillRuntimeLimits{
			RegistryReadTimeout: time.Second,
		}},
		telemetry: capture,
	}

	for range 2 {
		if _, _, err := runtime.loadSkillManifest(
			t.Context(), manifest.Ref, SkillBoundaryInitialPlanning, 1, false,
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runtime.loadSkillResource(
			t.Context(), resource.Ref, SkillBoundaryInitialPlanning, 1, false,
		); err != nil {
			t.Fatal(err)
		}
	}

	if got := countSkillString(capture.spans, "skills.registry.load_manifest"); got != 1 {
		t.Fatalf("manifest registry spans = %d, want one cache-miss span; spans = %v", got, capture.spans)
	}
	if got := countSkillString(capture.spans, "skills.registry.load_resource"); got != 1 {
		t.Fatalf("resource registry spans = %d, want one cache-miss span; spans = %v", got, capture.spans)
	}
	if upstream.manifestCalls != 1 || upstream.resourceCalls != 1 {
		t.Fatalf("upstream reads = %d/%d, want one each", upstream.manifestCalls, upstream.resourceCalls)
	}
}

func TestSkillFailureLogContainsNoProviderErrorOrContent(t *testing.T) {
	runtime := &skillRuntime{logger: &mockLogger{warnFunc: func(_ string, fields map[string]interface{}) {
		if fields["error"] == "secret provider URL and body" {
			t.Fatal("raw provider error reached structured log")
		}
		if fields["operation"] != "skills_registry_load_manifest" || fields["request_id"] != "request-skills" {
			t.Fatalf("log fields = %#v", fields)
		}
	}}}
	ctx := WithRequestID(t.Context(), "request-skills")
	_, observation := runtime.startSkillRegistryOperation(
		ctx, "load_manifest", SkillBoundaryInitialPlanning, 1,
	)
	observation.Finish("", errors.New("secret provider URL and body"))
}

func assertSkillMetricRecordsUseExactLabels(t *testing.T, records []mockMetricRecord) {
	t.Helper()
	labelKeys := map[string]map[string]struct{}{
		skillOperationTotalMetric:    stringSet("module", "stage", "boundary", "outcome"),
		skillOperationDurationMetric: stringSet("module", "stage", "boundary", "outcome"),
		skillCandidateBatchMetric:    stringSet("module", "boundary", "outcome"),
		skillSelectorTokensMetric:    stringSet("module", "selector", "token_kind", "outcome"),
		skillContentCacheMetric:      stringSet("module", "content_kind", "outcome"),
		skillPromptTokensMetric:      stringSet("module", "prompt_kind", "boundary"),
		skillIntegrityMetric:         stringSet("module", "content_kind", "source", "retry_outcome", "action"),
		skillAuthoringDiagnosticMetric: stringSet(
			"module", "severity", "diagnostic_code", "operation",
		),
		skillAdminOperationTotalMetric:    stringSet("module", "operation", "outcome"),
		skillAdminOperationDurationMetric: stringSet("module", "operation", "outcome"),
	}
	allowedValues := map[string]map[string]struct{}{
		"module": stringSet(telemetry.ModuleOrchestration),
		"stage": stringSet(
			"config_validation", "pin_candidates", "response_cache", "activation",
			"manifest_load", "resource_selection", "resource_load", "projection",
		),
		"boundary": stringSet(
			"startup", "request_start", "initial_planning", "continuation",
			"regeneration", "synthesis", "resume", "admin",
		),
		"outcome": stringSet(
			"success", "error", "fallback", "omitted", "accepted", "rejected",
			"authoritative", "hit", "miss", "evicted", "noop", "conflict",
			"denied", "not_found", "deleted", "already_deleted",
		),
		"selector":     stringSet("activation", "resource", "authoring"),
		"token_kind":   stringSet("prompt", "completion", "total"),
		"content_kind": stringSet("manifest", "resource"),
		"source":       stringSet("cache", "registry", "authoring_input"),
		"retry_outcome": stringSet(
			"not_attempted", "recovered", "persistent",
		),
		"action":   stringSet("used", "omitted", "failed", "evicted"),
		"severity": stringSet("error", "warning"),
		"operation": stringSet(
			"schema", "validate", "analyze", "list", "get_published", "get_version",
			"list_versions", "put_published", "delete_versions",
		),
		"prompt_kind": stringSet(
			"capability_selection", "planning", "continuation", "regeneration",
			"synthesis", skillActivationAIPurpose, skillResourceAIPurpose, skillAuthoringAIPurpose,
		),
	}
	for _, record := range records {
		expectedKeys, found := labelKeys[record.name]
		if !found {
			continue
		}
		actualKeys := make(map[string]struct{}, len(record.labels))
		for key, value := range record.labels {
			actualKeys[key] = struct{}{}
			if key == "diagnostic_code" {
				if skillMetricDiagnosticCode(value) != value {
					t.Fatalf("%s has unbounded diagnostic_code %q", record.name, value)
				}
				continue
			}
			if allowed, constrained := allowedValues[key]; constrained {
				if _, ok := allowed[value]; !ok {
					t.Fatalf("%s label %s has unbounded value %q", record.name, key, value)
				}
			}
		}
		if !reflect.DeepEqual(actualKeys, expectedKeys) {
			t.Fatalf("%s label keys = %#v, want %#v", record.name, actualKeys, expectedKeys)
		}
	}
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func TestStoreExecutionAsyncPersistsBoundedBodyFreeSkillDebug(t *testing.T) {
	orchestrator, capture := storeExecutionAsyncFixture(t)
	state := SkillExecutionState{Debug: SkillExecutionDebug{
		Candidates: []SkillCandidateDebug{{
			Ref:              SkillRef{Namespace: "travel", Name: "weather"},
			RequestedVersion: "published", Activation: SkillActivationAuto,
			Status: SkillCandidateResolved,
		}},
		ContentLoads: []SkillContentLoadDebug{{
			ContentKind: "resource",
			Skill: SkillVersionRef{
				Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 3,
				ManifestHash: "sha256:" + strings.Repeat("a", 64),
			},
			ResourceName: "forecast", ExpectedHash: "sha256:" + strings.Repeat("b", 64),
			Boundary: SkillBoundaryInitialPlanning, Outcome: "loaded",
		}},
		Projections: []SkillProjectionDebug{{
			Boundary: SkillBoundaryInitialPlanning,
			SkillRefs: []SkillVersionRef{{
				Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 3,
				ManifestHash: "sha256:" + strings.Repeat("a", 64),
			}},
			ResourceRefs: []SkillResourceRef{{
				Skill: SkillVersionRef{
					Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 3,
					ManifestHash: "sha256:" + strings.Repeat("a", 64),
				},
				Name: "forecast", ExpectedHash: "sha256:" + strings.Repeat("b", 64),
			}},
			TotalTokens: 80, Outcome: "projected",
		}},
		CacheFingerprint: "sha256:" + strings.Repeat("c", 64),
	}}
	holder := newSkillExecutionStateHolder(state, SkillCacheContext{})
	ctx := withSkillExecutionHolder(t.Context(), holder)
	orchestrator.storeExecutionAsync(ctx, "request", "request-with-skills", nil, nil, nil)
	orchestrator.executionWg.Wait()

	stored := lastStored(capture)
	if stored == nil || stored.Skills == nil || len(stored.Skills.ContentLoads) != 1 ||
		len(stored.Skills.Projections) != 1 {
		t.Fatalf("stored skill debug = %#v", stored)
	}
	encoded, err := json.Marshal(stored.Skills)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"planning_instructions", "response_instructions", `"content"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("stored skill debug contains body field %q: %s", forbidden, encoded)
		}
	}
}

func containsSkillString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func countSkillString(values []string, wanted string) int {
	count := 0
	for _, value := range values {
		if value == wanted {
			count++
		}
	}
	return count
}
