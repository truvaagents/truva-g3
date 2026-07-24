package orchestration

import (
	"context"
	"sync"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

type aiFingerprintCaptureKey struct{}

type aiFingerprintCapture struct {
	mu           sync.Mutex
	observed     bool
	stable       bool
	fingerprints map[string]struct{}
}

func withAIFingerprintCapture(ctx context.Context) (context.Context, *aiFingerprintCapture) {
	capture := &aiFingerprintCapture{stable: true, fingerprints: make(map[string]struct{})}
	return context.WithValue(ctx, aiFingerprintCaptureKey{}, capture), capture
}

func (capture *aiFingerprintCapture) matches(expected string) bool {
	if capture == nil {
		return true
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if !capture.observed || !capture.stable || len(capture.fingerprints) != 1 {
		return false
	}
	_, ok := capture.fingerprints[expected]
	return ok
}

// aiInvocation is the provider-neutral description of one orchestration LLM
// call. Options remains the compatibility input while call sites migrate to
// presence-aware generation parameters and declarative provider patches.
type aiInvocation struct {
	Purpose        string
	Prompt         string
	Options        *core.AIOptions
	Generation     core.AIGenerationOptions
	Patches        []core.AIProviderPatch
	DeferRecording bool
}

func newAIRequest(invocation aiInvocation) *core.AIRequest {
	request := core.NewAIRequestFromLegacy(invocation.Prompt, invocation.Purpose, invocation.Options)
	request.Generation = invocation.Generation
	request.Patches = append([]core.AIProviderPatch(nil), invocation.Patches...)
	return request
}

func invokeAI(
	ctx context.Context,
	client core.AIClient,
	invocation aiInvocation,
) (*core.AIResponse, *core.AIRequestReport, error) {
	if invocation.DeferRecording {
		ctx = telemetry.WithLLMCallRecordingDeferred(ctx)
	}
	result, err := core.GenerateAI(ctx, client, newAIRequest(invocation))
	if result == nil {
		captureAIRequestFingerprint(ctx, nil)
		return nil, nil, err
	}
	captureAIRequestFingerprint(ctx, result.RequestReport)
	recordAIRequestReport(ctx, result.RequestReport)
	return result.Response, result.RequestReport, err
}

func streamAI(
	ctx context.Context,
	client core.AIClient,
	invocation aiInvocation,
	callback core.StreamCallback,
) (*core.AIResponse, *core.AIRequestReport, error) {
	if invocation.DeferRecording {
		ctx = telemetry.WithLLMCallRecordingDeferred(ctx)
	}
	result, err := core.StreamAI(ctx, client, newAIRequest(invocation), callback)
	if result == nil {
		captureAIRequestFingerprint(ctx, nil)
		return nil, nil, err
	}
	captureAIRequestFingerprint(ctx, result.RequestReport)
	recordAIRequestReport(ctx, result.RequestReport)
	return result.Response, result.RequestReport, err
}

func captureAIRequestFingerprint(ctx context.Context, report *core.AIRequestReport) {
	capture, ok := ctx.Value(aiFingerprintCaptureKey{}).(*aiFingerprintCapture)
	if !ok || capture == nil {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.observed = true
	if report == nil || !report.Stable || report.Fingerprint == "" {
		capture.stable = false
		return
	}
	capture.fingerprints[report.Fingerprint] = struct{}{}
}

// fingerprintAI returns a cache namespace fragment and whether it is safe to
// use the affected AI-output cache. Legacy-only clients retain their existing
// namespace when the invocation uses only legacy-representable semantics.
func fingerprintAI(ctx context.Context, client core.AIClient, invocation aiInvocation) (string, bool) {
	request := newAIRequest(invocation)
	if fingerprinter, ok := client.(core.AIRequestFingerprinter); ok {
		fingerprint, stable := fingerprinter.RequestFingerprint(ctx, request)
		return fingerprint, stable && fingerprint != ""
	}
	if _, requestAware := client.(core.AIRequestClient); requestAware || !request.LegacyRepresentable() {
		return "", false
	}
	return "", true
}

func recordAIRequestReport(ctx context.Context, report *core.AIRequestReport) {
	if report == nil {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.String("ai.provider", report.Provider),
		attribute.String("ai.provider_alias", report.ProviderAlias),
		attribute.String("ai.surface", report.Surface),
		attribute.String("ai.request.operation", report.Operation),
		attribute.String("ai.purpose", report.Purpose),
		attribute.String("ai.requested_model", report.RequestedModel),
		attribute.String("ai.model", report.ResolvedModel),
		attribute.Bool("ai.request.policy_stable", report.Stable),
		attribute.Int("ai.request.adjustment_count", len(report.Adjustments)),
	}
	if report.Stable && report.Fingerprint != "" {
		attrs = append(attrs, attribute.String("ai.request.policy_fingerprint", report.Fingerprint))
	}
	telemetry.AddSpanEvent(ctx, "ai.request.prepared", attrs...)
}
