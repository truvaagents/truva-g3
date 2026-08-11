package requestpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

type testDraft struct {
	*Document
	explicit map[string]struct{}
	identity string
}

func (d *testDraft) HasExplicitIntent(path string) bool {
	_, ok := d.explicit[path]
	return ok
}

func (d *testDraft) PolicyFingerprintIdentity() string { return d.identity }

type generationTestDraft struct {
	*testDraft
	temperaturePath string
	maxTokensPath   string
}

func (d *generationTestDraft) EffectiveGenerationPaths() (string, string) {
	return d.temperaturePath, d.maxTokensPath
}

type testMiddleware struct {
	name    string
	version string
	apply   func(context.Context, RequestEditor) error
}

func (m *testMiddleware) Name() string    { return m.name }
func (m *testMiddleware) Version() string { return m.version }
func (m *testMiddleware) Apply(ctx context.Context, editor RequestEditor) error {
	return m.apply(ctx, editor)
}

type stableTestMiddleware struct {
	RequestMiddleware
	stable bool
}

func (m *stableTestMiddleware) StablePolicyFingerprint() bool { return m.stable }

func TestEngine_ApplyPrecedenceReportingAndFingerprint(t *testing.T) {
	selector := core.AIProviderSelector{Provider: "anthropic", Model: "claude-*", Operation: "generate"}
	builtIn := patch("built-in", selector, map[string]interface{}{"/value": "built-in"})
	appRule := patch("app", selector, map[string]interface{}{"/value": "app"})
	middleware := &stableTestMiddleware{
		RequestMiddleware: &testMiddleware{name: "tenant-policy", version: "3", apply: func(_ context.Context, editor RequestEditor) error {
			value, ok := editor.Get("/nested")
			if !ok {
				return errors.New("nested value missing")
			}
			value.(map[string]interface{})["caller-mutation"] = true
			return editor.Set("/value", "middleware")
		}},
		stable: true,
	}
	engine, err := NewEngine(Config{BuiltIns: []core.AIProviderPatch{builtIn}, AppRules: []core.AIProviderPatch{appRule}, Middleware: []RequestMiddleware{middleware}})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	requestPatch := patch("request", selector, map[string]interface{}{"/value": "request"})
	draft := newTestDraft(t, map[string]interface{}{"value": "initial", "nested": map[string]interface{}{"safe": true}}, nil)

	report, err := engine.Apply(t.Context(), draft, []core.AIProviderPatch{requestPatch})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got, _ := draft.Get("/value"); got != "request" {
		t.Fatalf("final value = %#v, want request", got)
	}
	if _, leaked := draft.Get("/nested/caller-mutation"); leaked {
		t.Fatal("middleware mutated a Get result without using the editor")
	}
	wantSources := []string{"built-in-rule", "app-rule", "middleware", "request-patch"}
	gotSources := make([]string, 0, len(report.Adjustments))
	for _, adjustment := range report.Adjustments {
		gotSources = append(gotSources, adjustment.Source)
		if adjustment.Path != "/value" || adjustment.Action != "set" {
			t.Fatalf("unexpected adjustment: %#v", adjustment)
		}
	}
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("adjustment sources = %#v, want %#v", gotSources, wantSources)
	}
	if !report.Stable || len(report.Fingerprint) != 64 {
		t.Fatalf("stable fingerprint missing: %#v", report)
	}

	secondDraft := newTestDraft(t, map[string]interface{}{"value": "different", "nested": map[string]interface{}{"safe": true}}, nil)
	second, err := engine.Apply(t.Context(), secondDraft, []core.AIProviderPatch{requestPatch})
	if err != nil {
		t.Fatalf("second Apply returned error: %v", err)
	}
	if second.Fingerprint != report.Fingerprint {
		t.Fatalf("fingerprint changed with draft values: %q != %q", second.Fingerprint, report.Fingerprint)
	}

	changedValue := patch("request", selector, map[string]interface{}{"/value": "secret-different"})
	third, err := engine.Apply(t.Context(), newTestDraft(t, map[string]interface{}{"value": "initial", "nested": map[string]interface{}{"safe": true}}, nil), []core.AIProviderPatch{changedValue})
	if err != nil {
		t.Fatalf("third Apply returned error: %v", err)
	}
	if third.Fingerprint != report.Fingerprint {
		t.Fatal("fingerprint included an arbitrary patch value")
	}
}

func TestEngine_ReportsEffectiveGenerationValues(t *testing.T) {
	engine, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	draft := &generationTestDraft{
		testDraft:       newTestDraft(t, map[string]interface{}{"temperature": json.Number("0.25"), "max_tokens": float64(128)}, nil),
		temperaturePath: "/temperature",
		maxTokensPath:   "/max_tokens",
	}

	report, err := engine.Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if report.EffectiveTemperature.Mode != core.AIParameterSet || report.EffectiveTemperature.Value != 0.25 {
		t.Fatalf("effective temperature = %#v", report.EffectiveTemperature)
	}
	if report.EffectiveMaxTokens.Mode != core.AIParameterSet || report.EffectiveMaxTokens.Value != 128 {
		t.Fatalf("effective max tokens = %#v", report.EffectiveMaxTokens)
	}
}

func TestEffectiveGenerationReportPresenceAndInvalidValues(t *testing.T) {
	tests := []struct {
		name            string
		draft           Draft
		wantTemperature core.AIParameterMode
		wantMaxTokens   core.AIParameterMode
	}{
		{
			name:            "provider does not expose paths",
			draft:           newTestDraft(t, map[string]interface{}{}, nil),
			wantTemperature: core.AIParameterInherit,
			wantMaxTokens:   core.AIParameterInherit,
		},
		{
			name: "reported fields absent",
			draft: &generationTestDraft{
				testDraft:       newTestDraft(t, map[string]interface{}{}, nil),
				temperaturePath: "/temperature",
				maxTokensPath:   "/max_tokens",
			},
			wantTemperature: core.AIParameterOmit,
			wantMaxTokens:   core.AIParameterOmit,
		},
		{
			name: "reported fields cannot be represented",
			draft: &generationTestDraft{
				testDraft:       newTestDraft(t, map[string]interface{}{"temperature": "warm", "max_tokens": 1.5}, nil),
				temperaturePath: "/temperature",
				maxTokensPath:   "/max_tokens",
			},
			wantTemperature: core.AIParameterInherit,
			wantMaxTokens:   core.AIParameterInherit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			temperature, maxTokens := effectiveGenerationReport(test.draft)
			if temperature.Mode != test.wantTemperature || maxTokens.Mode != test.wantMaxTokens {
				t.Fatalf("effective generation = (%#v, %#v), want modes (%v, %v)", temperature, maxTokens, test.wantTemperature, test.wantMaxTokens)
			}
		})
	}
}

func TestReportFloat32(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  float32
		ok    bool
	}{
		{name: "float32", value: float32(0.25), want: 0.25, ok: true},
		{name: "float64", value: float64(-0.5), want: -0.5, ok: true},
		{name: "signed integer", value: int16(-2), want: -2, ok: true},
		{name: "unsigned integer", value: uint32(3), want: 3, ok: true},
		{name: "json number", value: json.Number("0.75"), want: 0.75, ok: true},
		{name: "invalid json number", value: json.Number("invalid"), ok: false},
		{name: "nil", value: nil, ok: false},
		{name: "unsupported type", value: "0.25", ok: false},
		{name: "nan", value: math.NaN(), ok: false},
		{name: "positive infinity", value: math.Inf(1), ok: false},
		{name: "negative infinity", value: math.Inf(-1), ok: false},
		{name: "overflow", value: math.MaxFloat64, ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := reportFloat32(test.value)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("reportFloat32(%#v) = (%v, %t), want (%v, %t)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestReportInt(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  int
		ok    bool
	}{
		{name: "signed integer", value: int16(-2), want: -2, ok: true},
		{name: "unsigned integer", value: uint32(3), want: 3, ok: true},
		{name: "integral float32", value: float32(4), want: 4, ok: true},
		{name: "integral float64", value: float64(5), want: 5, ok: true},
		{name: "json integer", value: json.Number("6"), want: 6, ok: true},
		{name: "fractional json number", value: json.Number("6.5"), ok: false},
		{name: "invalid json number", value: json.Number("invalid"), ok: false},
		{name: "nil", value: nil, ok: false},
		{name: "unsupported type", value: "6", ok: false},
		{name: "fractional float", value: 6.5, ok: false},
		{name: "unsigned overflow", value: uint64(math.MaxUint64), ok: false},
		{name: "positive infinity", value: math.Inf(1), ok: false},
		{name: "negative infinity", value: math.Inf(-1), ok: false},
		{name: "nan", value: math.NaN(), ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := reportInt(test.value)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("reportInt(%#v) = (%d, %t), want (%d, %t)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestEngine_StrictCompatibilityRequiresApplicationAcknowledgment(t *testing.T) {
	selector := core.AIProviderSelector{Provider: "anthropic"}
	builtIn := core.AIProviderPatch{Name: "compatibility", Version: "1", Selector: selector, Remove: []string{"/temperature"}}
	strict, err := NewEngine(Config{BuiltIns: []core.AIProviderPatch{builtIn}, Mode: CompatibilityStrict})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	draft := newTestDraft(t, map[string]interface{}{"temperature": 0.2}, []string{"/temperature"})
	report, err := strict.Apply(t.Context(), draft, nil)
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) || policyErr.Stage != "compatibility" {
		t.Fatalf("strict Apply error = %v, want compatibility PolicyError", err)
	}
	if report == nil || len(report.Adjustments) != 1 {
		t.Fatalf("partial report = %#v", report)
	}

	acknowledgment := core.AIProviderPatch{Name: "acknowledge", Version: "1", Selector: selector, Remove: []string{"/temperature"}}
	strictWithAck, err := NewEngine(Config{BuiltIns: []core.AIProviderPatch{builtIn}, AppRules: []core.AIProviderPatch{acknowledgment}, Mode: CompatibilityStrict})
	if err != nil {
		t.Fatalf("NewEngine with acknowledgment returned error: %v", err)
	}
	if _, err := strictWithAck.Apply(t.Context(), newTestDraft(t, map[string]interface{}{"temperature": 0.2}, []string{"/temperature"}), nil); err != nil {
		t.Fatalf("acknowledged strict Apply returned error: %v", err)
	}
}

func TestEngine_HeaderPrecedenceAndMiddlewareIdentity(t *testing.T) {
	selector := core.AIProviderSelector{AllProviders: true}
	builtIn := core.AIProviderPatch{Name: "built-in", Version: "1", Selector: selector, SetHeaders: map[string]string{"X-Layer": "built-in"}}
	appRule := core.AIProviderPatch{Name: "app", Version: "1", Selector: selector, SetHeaders: map[string]string{"x-layer": "app"}}
	middleware := &testMiddleware{name: "headers", version: "1", apply: func(_ context.Context, editor RequestEditor) error {
		if editor.Info().Provider != "anthropic" {
			return errors.New("request identity unavailable")
		}
		if err := editor.SetHeader("X-Layer", "middleware"); err != nil {
			return err
		}
		return editor.RemoveHeader("X-Remove")
	}}
	engine, err := NewEngine(Config{
		BuiltIns:   []core.AIProviderPatch{builtIn},
		AppRules:   []core.AIProviderPatch{appRule},
		Middleware: []RequestMiddleware{middleware},
	})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	draft := newTestDraftWithConfig(t, DocumentConfig{
		Info:    testInfo(),
		Body:    map[string]interface{}{},
		Headers: map[string]string{"x-remove": "present"},
	}, nil)
	request := core.AIProviderPatch{Name: "request", Version: "1", Selector: selector, SetHeaders: map[string]string{"X-Layer": "request"}}
	report, err := engine.Apply(t.Context(), draft, []core.AIProviderPatch{request})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if got, _ := draft.Header("x-layer"); got != "request" {
		t.Fatalf("final header = %q, want request", got)
	}
	if _, exists := draft.Header("x-remove"); exists {
		t.Fatal("middleware header removal was not applied")
	}
	if len(report.Adjustments) != 5 {
		t.Fatalf("header adjustments = %#v", report.Adjustments)
	}
}

func TestEngine_ReportsCaseFoldNormalizationAsAChange(t *testing.T) {
	rule := patch("normalize", core.AIProviderSelector{AllProviders: true}, map[string]interface{}{"/temperature": 0.2})
	engine, err := NewEngine(Config{AppRules: []core.AIProviderPatch{rule}})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	document := mustDocument(t, DocumentConfig{
		Info: testInfo(),
		Body: map[string]interface{}{
			"temperature": 0.2,
			"Temperature": 0.9,
		},
		CaseInsensitivePaths: []string{"/temperature"},
	})
	draft := &testDraft{Document: document, identity: "test-adapter-v1"}
	report, err := engine.Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(report.Adjustments) != 1 || report.Adjustments[0].Path != "/temperature" {
		t.Fatalf("case-fold normalization adjustments = %#v", report.Adjustments)
	}
	if len(draft.Body()) != 1 {
		t.Fatalf("duplicate casing survived normalization: %#v", draft.Body())
	}
}

func TestEngine_InputValidationAndCompatibilityModeFingerprint(t *testing.T) {
	if _, err := NewEngine(Config{Mode: CompatibilityMode(99)}); err == nil {
		t.Fatal("unknown compatibility mode unexpectedly accepted")
	}
	var nilMiddleware *testMiddleware
	if _, err := NewEngine(Config{Middleware: []RequestMiddleware{nilMiddleware}}); err == nil {
		t.Fatal("typed-nil middleware unexpectedly accepted")
	}
	if _, err := NewEngine(Config{Middleware: []RequestMiddleware{&testMiddleware{version: "1"}}}); err == nil {
		t.Fatal("unnamed middleware unexpectedly accepted")
	}
	if _, err := NewEngine(Config{Middleware: []RequestMiddleware{&testMiddleware{name: "name"}}}); err == nil {
		t.Fatal("unversioned middleware unexpectedly accepted")
	}

	compatible, err := NewEngine(Config{Mode: CompatibilityCompatible})
	if err != nil {
		t.Fatalf("compatible engine: %v", err)
	}
	strict, err := NewEngine(Config{Mode: CompatibilityStrict})
	if err != nil {
		t.Fatalf("strict engine: %v", err)
	}
	compatibleReport, err := compatible.Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil)
	if err != nil {
		t.Fatalf("compatible Apply: %v", err)
	}
	strictReport, err := strict.Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil)
	if err != nil {
		t.Fatalf("strict Apply: %v", err)
	}
	if compatibleReport.Fingerprint == strictReport.Fingerprint {
		t.Fatal("compatibility mode was omitted from policy fingerprint")
	}
	if _, err := compatible.Apply(nil, newTestDraft(t, map[string]interface{}{}, nil), nil); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
	var nilDraft *testDraft
	if _, err := compatible.Apply(t.Context(), nilDraft, nil); err == nil {
		t.Fatal("typed-nil draft unexpectedly accepted")
	}
	if _, err := (*Engine)(nil).Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil); err == nil {
		t.Fatal("nil engine unexpectedly accepted")
	}
}

func TestEngine_ValidationAndProtectedFailures(t *testing.T) {
	tests := []struct {
		name  string
		patch core.AIProviderPatch
		want  string
	}{
		{name: "missing identity", patch: core.AIProviderPatch{Selector: core.AIProviderSelector{Provider: "anthropic"}}, want: "name"},
		{name: "unscoped", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{Operation: "generate"}}, want: "requires provider"},
		{name: "ambiguous body", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/same": true}, Remove: []string{"/same"}}, want: "both set and remove"},
		{name: "ambiguous header", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, SetHeaders: map[string]string{"X-Test": "one"}, RemoveHeaders: []string{"x-test"}}, want: "both set and remove"},
		{name: "invalid pointer", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Remove: []string{"root"}}, want: "must begin"},
		{name: "append token", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Set: map[string]interface{}{"/items/-": true}}, want: "append token"},
		{name: "invalid header", patch: core.AIProviderPatch{Name: "rule", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, SetHeaders: map[string]string{"bad header": "value"}}, want: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewEngine(Config{AppRules: []core.AIProviderPatch{test.patch}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewEngine error = %v, want substring %q", err, test.want)
			}
		})
	}

	tooLong := strings.Repeat("x", maxModelSelectorBytes+1)
	_, err := NewEngine(Config{AppRules: []core.AIProviderPatch{{Name: "long", Version: "1", Selector: core.AIProviderSelector{Model: tooLong}}}})
	if err == nil || !strings.Contains(err.Error(), "256") {
		t.Fatalf("long model selector error = %v", err)
	}

	engine, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("empty engine: %v", err)
	}
	protected := newTestDraftWithConfig(t, DocumentConfig{
		Info:           testInfo(),
		Body:           map[string]interface{}{"model": "claude-test"},
		ProtectedPaths: []string{"/model"},
	}, nil)
	requestPatch := patch("override", core.AIProviderSelector{Provider: "anthropic"}, map[string]interface{}{"/model": "stolen"})
	report, err := engine.Apply(t.Context(), protected, []core.AIProviderPatch{requestPatch})
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected mutation error = %v", err)
	}
	if report == nil || report.Provider != "anthropic" {
		t.Fatalf("protected failure report = %#v", report)
	}
}

func TestEngine_SelectorBoundaryMissingRemoveAndConfigIsolation(t *testing.T) {
	value := map[string]interface{}{"nested": "original"}
	rule := patch("bounded", core.AIProviderSelector{Provider: "anthropic", Model: "claude-sonnet-5-*"}, map[string]interface{}{"/value": value})
	engine, err := NewEngine(Config{AppRules: []core.AIProviderPatch{rule}})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	value["nested"] = "caller-mutated"
	matched := newTestDraft(t, map[string]interface{}{}, nil)
	matched.info.ResolvedModel = "claude-sonnet-5-20260701"
	report, err := engine.Apply(t.Context(), matched, nil)
	if err != nil {
		t.Fatalf("matched Apply: %v", err)
	}
	got, _ := matched.Get("/value/nested")
	if got != "original" {
		t.Fatalf("engine config was not isolated: %#v", got)
	}
	if len(report.Adjustments) != 1 {
		t.Fatalf("matched adjustment count = %d", len(report.Adjustments))
	}

	notMatched := newTestDraft(t, map[string]interface{}{}, nil)
	notMatched.info.ResolvedModel = "claude-sonnet-50"
	report, err = engine.Apply(t.Context(), notMatched, nil)
	if err != nil {
		t.Fatalf("non-matching Apply: %v", err)
	}
	if _, exists := notMatched.Get("/value"); exists || len(report.Adjustments) != 0 {
		t.Fatalf("model glob crossed family boundary: body=%#v report=%#v", notMatched.Body(), report)
	}

	removeMissing, err := NewEngine(Config{AppRules: []core.AIProviderPatch{{Name: "remove", Version: "1", Selector: core.AIProviderSelector{AllProviders: true}, Remove: []string{"/missing"}}}})
	if err != nil {
		t.Fatalf("remove engine: %v", err)
	}
	report, err = removeMissing.Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil)
	if err != nil || len(report.Adjustments) != 0 {
		t.Fatalf("missing remove = (%#v, %v), want no adjustment", report, err)
	}
}

func TestEngine_UnstableWithoutAdapterIdentityAndMiddlewareError(t *testing.T) {
	engine, err := NewEngine(Config{})
	if err != nil {
		t.Fatalf("NewEngine returned error: %v", err)
	}
	draft := newTestDraft(t, map[string]interface{}{}, nil)
	draft.identity = ""
	report, err := engine.Apply(t.Context(), draft, nil)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if report.Stable || report.Fingerprint != "" {
		t.Fatalf("report unexpectedly stable: %#v", report)
	}

	failing := &testMiddleware{name: "failure", version: "1", apply: func(context.Context, RequestEditor) error { return errors.New("denied") }}
	engine, err = NewEngine(Config{Middleware: []RequestMiddleware{failing}})
	if err != nil {
		t.Fatalf("middleware engine: %v", err)
	}
	report, err = engine.Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil)
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) || policyErr.Stage != "middleware" || report == nil {
		t.Fatalf("middleware failure = (%#v, %v)", report, err)
	}
	if !errors.Is(policyErr, policyErr.Err) {
		t.Fatal("PolicyError did not unwrap its cause")
	}
}

func TestEngine_MiddlewareFingerprintStabilityIsExplicit(t *testing.T) {
	base := &testMiddleware{
		name:    "dynamic-policy",
		version: "1",
		apply:   func(context.Context, RequestEditor) error { return nil },
	}
	tests := []struct {
		name       string
		middleware RequestMiddleware
		wantStable bool
	}{
		{name: "undeclared is unstable", middleware: base},
		{
			name: "explicitly unstable",
			middleware: &stableTestMiddleware{
				RequestMiddleware: base,
				stable:            false,
			},
		},
		{
			name: "explicitly stable",
			middleware: &stableTestMiddleware{
				RequestMiddleware: base,
				stable:            true,
			},
			wantStable: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, err := NewEngine(Config{Middleware: []RequestMiddleware{test.middleware}})
			if err != nil {
				t.Fatalf("NewEngine returned error: %v", err)
			}
			report, err := engine.Apply(t.Context(), newTestDraft(t, map[string]interface{}{}, nil), nil)
			if err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}
			if report.Stable != test.wantStable {
				t.Fatalf("report.Stable = %t, want %t", report.Stable, test.wantStable)
			}
			if test.wantStable && len(report.Fingerprint) != 64 {
				t.Fatalf("stable fingerprint = %q, want SHA-256 hex", report.Fingerprint)
			}
			if !test.wantStable && report.Fingerprint != "" {
				t.Fatalf("unstable report exposed fingerprint %q", report.Fingerprint)
			}
		})
	}
}

func patch(name string, selector core.AIProviderSelector, set map[string]interface{}) core.AIProviderPatch {
	return core.AIProviderPatch{Name: name, Version: "1", Selector: selector, Set: set}
}

func testInfo() RequestInfo {
	return RequestInfo{
		Provider:       "anthropic",
		ProviderAlias:  "anthropic.primary",
		Surface:        "messages",
		Operation:      "generate",
		Purpose:        "test-purpose",
		RequestedModel: "default",
		ResolvedModel:  "claude-test",
	}
}

func newTestDraft(t *testing.T, body map[string]interface{}, explicit []string) *testDraft {
	return newTestDraftWithConfig(t, DocumentConfig{Info: testInfo(), Body: body}, explicit)
}

func newTestDraftWithConfig(t *testing.T, config DocumentConfig, explicit []string) *testDraft {
	t.Helper()
	document := mustDocument(t, config)
	paths := make(map[string]struct{}, len(explicit))
	for _, path := range explicit {
		paths[path] = struct{}{}
	}
	return &testDraft{Document: document, explicit: paths, identity: "test-adapter-v1"}
}
