package requestpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/truvaagents/truva-g3/core"
)

// Engine is an immutable, concurrency-safe request-policy snapshot.
type Engine struct {
	builtIns   []compiledPatch
	appRules   []compiledPatch
	middleware []RequestMiddleware
	mode       CompatibilityMode
}

// NewEngine validates and snapshots request rules before they can be used by
// concurrent provider calls.
func NewEngine(config Config) (*Engine, error) {
	if !config.Mode.Valid() {
		return nil, fmt.Errorf("unsupported compatibility mode %d", config.Mode)
	}
	builtIns, err := cloneAndValidatePatches(config.BuiltIns)
	if err != nil {
		return nil, fmt.Errorf("validate built-in request rules: %w", err)
	}
	appRules, err := cloneAndValidatePatches(config.AppRules)
	if err != nil {
		return nil, fmt.Errorf("validate application request rules: %w", err)
	}
	middleware := append([]RequestMiddleware(nil), config.Middleware...)
	for index, item := range middleware {
		if isNilMiddleware(item) {
			return nil, fmt.Errorf("request middleware %d is nil", index)
		}
		if strings.TrimSpace(item.Name()) == "" {
			return nil, fmt.Errorf("request middleware %d has no name", index)
		}
		if strings.TrimSpace(item.Version()) == "" {
			return nil, fmt.Errorf("request middleware %q has no version", item.Name())
		}
	}
	return &Engine{
		builtIns:   builtIns,
		appRules:   appRules,
		middleware: middleware,
		mode:       config.Mode,
	}, nil
}

// Apply executes the deterministic policy pipeline against one call-local
// draft. It may return a sanitized partial report with an error.
func (e *Engine) Apply(ctx context.Context, draft Draft, perRequest []core.AIProviderPatch) (*core.AIRequestReport, error) {
	if e == nil {
		return nil, errors.New("request policy engine is nil")
	}
	if ctx == nil {
		return nil, errors.New("request policy context is nil")
	}
	if isNilDraft(draft) {
		return nil, errors.New("request policy draft is nil")
	}
	requestRules, err := cloneAndValidatePatches(perRequest)
	if err != nil {
		return reportForInfo(draft.Info()), &PolicyError{Stage: "request-patch-validation", Err: err}
	}

	editor := newTrackingEditor(draft)
	editor.fingerprint = append(editor.fingerprint, fmt.Sprintf("compatibility-mode=%d", e.mode))
	for _, rule := range e.builtIns {
		if !matches(rule.patch.Selector, rule.modelPattern, draft.Info()) {
			continue
		}
		editor.addFingerprintPatch("built-in-rule", rule.patch)
		if err := editor.applyPatch(rule.patch, "built-in-rule"); err != nil {
			return editor.report(), err
		}
	}
	for _, rule := range e.appRules {
		if !matches(rule.patch.Selector, rule.modelPattern, draft.Info()) {
			continue
		}
		editor.addFingerprintPatch("app-rule", rule.patch)
		if err := editor.applyPatch(rule.patch, "app-rule"); err != nil {
			return editor.report(), err
		}
	}
	for _, middleware := range e.middleware {
		editor.addFingerprintMiddleware(middleware)
		if err := editor.applyMiddleware(ctx, middleware); err != nil {
			return editor.report(), err
		}
	}
	for _, patch := range requestRules {
		if !matches(patch.patch.Selector, patch.modelPattern, draft.Info()) {
			continue
		}
		editor.addFingerprintPatch("request-patch", patch.patch)
		if err := editor.applyPatch(patch.patch, "request-patch"); err != nil {
			return editor.report(), err
		}
	}
	if err := editor.validateCompatibility(e.mode); err != nil {
		return editor.report(), err
	}
	if err := draft.Validate(); err != nil {
		return editor.report(), &PolicyError{Stage: "draft-validation", Err: err}
	}
	return editor.finalReport(), nil
}

func isNilDraft(draft Draft) bool {
	if draft == nil {
		return true
	}
	value := reflect.ValueOf(draft)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func isNilMiddleware(middleware RequestMiddleware) bool {
	if middleware == nil {
		return true
	}
	value := reflect.ValueOf(middleware)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

type trackingEditor struct {
	draft             Draft
	adjustments       []core.AIRequestAdjustment
	currentSource     string
	currentRule       string
	currentReason     string
	strictConflict    map[string]string
	fingerprint       []string
	fingerprintStable bool
}

func newTrackingEditor(draft Draft) *trackingEditor {
	return &trackingEditor{
		draft:             draft,
		strictConflict:    make(map[string]string),
		fingerprintStable: true,
	}
}

func (e *trackingEditor) Info() RequestInfo { return e.draft.Info() }

func (e *trackingEditor) Get(path string) (interface{}, bool) {
	value, exists := e.draft.Get(path)
	if !exists {
		return nil, false
	}
	cloned, err := CloneJSONValue(value)
	if err != nil {
		// Opaque legacy leaves are intentionally not exposed by reference to
		// middleware. Providers can still pass them to their existing encoder.
		return nil, false
	}
	return cloned, true
}

func (e *trackingEditor) Set(path string, value interface{}) error {
	cloned, err := CloneJSONValue(value)
	if err != nil {
		return err
	}
	before, existed := e.draft.Get(path)
	changed := !existed || !reflect.DeepEqual(before, cloned)
	if detector, ok := e.draft.(setChangeDetector); ok {
		changed = detector.WouldSetChange(path, cloned)
	}
	if err := e.draft.Set(path, cloned); err != nil {
		return err
	}
	e.trackCompatibility(path, changed)
	if changed {
		e.record(path, "set")
	}
	return nil
}

func (e *trackingEditor) Remove(path string) error {
	_, existed := e.draft.Get(path)
	if err := e.draft.Remove(path); err != nil {
		return err
	}
	e.trackCompatibility(path, existed)
	if existed {
		e.record(path, "remove")
	}
	return nil
}

func (e *trackingEditor) SetHeader(name, value string) error {
	before, existed := e.readHeader(name)
	if err := e.draft.SetHeader(name, value); err != nil {
		return err
	}
	if !existed || before != value {
		e.record("header:"+strings.ToLower(name), "set")
	}
	return nil
}

func (e *trackingEditor) RemoveHeader(name string) error {
	_, existed := e.readHeader(name)
	if err := e.draft.RemoveHeader(name); err != nil {
		return err
	}
	if existed {
		e.record("header:"+strings.ToLower(name), "remove")
	}
	return nil
}

func (e *trackingEditor) readHeader(name string) (string, bool) {
	reader, ok := e.draft.(headerReader)
	if !ok {
		return "", false
	}
	return reader.Header(name)
}

func (e *trackingEditor) applyPatch(patch core.AIProviderPatch, source string) error {
	e.currentSource = source
	e.currentRule = patch.Name + "@" + patch.Version
	e.currentReason = patch.Name
	defer e.clearCurrent()

	for _, path := range sortedMapKeys(patch.Set) {
		if err := e.Set(path, patch.Set[path]); err != nil {
			return &PolicyError{Stage: "mutation", Rule: e.currentRule, Path: path, Err: err}
		}
	}
	for _, path := range patch.Remove {
		if err := e.Remove(path); err != nil {
			return &PolicyError{Stage: "mutation", Rule: e.currentRule, Path: path, Err: err}
		}
	}
	for _, name := range sortedMapKeys(patch.SetHeaders) {
		if err := e.SetHeader(name, patch.SetHeaders[name]); err != nil {
			return &PolicyError{Stage: "mutation", Rule: e.currentRule, Path: "header:" + strings.ToLower(name), Err: err}
		}
	}
	for _, name := range patch.RemoveHeaders {
		if err := e.RemoveHeader(name); err != nil {
			return &PolicyError{Stage: "mutation", Rule: e.currentRule, Path: "header:" + strings.ToLower(name), Err: err}
		}
	}
	return nil
}

func (e *trackingEditor) applyMiddleware(ctx context.Context, middleware RequestMiddleware) error {
	e.currentSource = "middleware"
	e.currentRule = middleware.Name() + "@" + middleware.Version()
	e.currentReason = middleware.Name()
	defer e.clearCurrent()
	if err := middleware.Apply(ctx, e); err != nil {
		return &PolicyError{Stage: "middleware", Rule: e.currentRule, Err: err}
	}
	return nil
}

func (e *trackingEditor) clearCurrent() {
	e.currentSource = ""
	e.currentRule = ""
	e.currentReason = ""
}

func (e *trackingEditor) record(path, action string) {
	e.adjustments = append(e.adjustments, core.AIRequestAdjustment{
		Source: e.currentSource,
		Rule:   e.currentRule,
		Path:   path,
		Action: action,
		Reason: e.currentReason,
	})
}

func (e *trackingEditor) trackCompatibility(path string, changed bool) {
	if e.currentSource == "built-in-rule" && changed {
		if explicit, ok := e.draft.(explicitIntentDraft); ok && explicit.HasExplicitIntent(path) {
			e.strictConflict[path] = e.currentRule
		}
		return
	}
	if e.currentSource == "app-rule" || e.currentSource == "middleware" || e.currentSource == "request-patch" {
		delete(e.strictConflict, path)
	}
}

func (e *trackingEditor) validateCompatibility(mode CompatibilityMode) error {
	if mode != CompatibilityStrict || len(e.strictConflict) == 0 {
		return nil
	}
	paths := sortedMapKeys(e.strictConflict)
	path := paths[0]
	return &PolicyError{
		Stage: "compatibility",
		Rule:  e.strictConflict[path],
		Path:  path,
		Err:   errors.New("built-in compatibility changed explicit request intent without application acknowledgment"),
	}
}

func (e *trackingEditor) addFingerprintPatch(source string, patch core.AIProviderPatch) {
	parts := []string{
		"patch", source, patch.Name, patch.Version,
		selectorFingerprint(patch.Selector),
		"set=" + strings.Join(sortedMapKeys(patch.Set), ","),
		"remove=" + strings.Join(patch.Remove, ","),
		"set-headers=" + strings.Join(lowerSortedKeys(patch.SetHeaders), ","),
		"remove-headers=" + strings.Join(lowerSorted(patch.RemoveHeaders), ","),
	}
	e.fingerprint = append(e.fingerprint, strings.Join(parts, "|"))
}

func (e *trackingEditor) addFingerprintMiddleware(middleware RequestMiddleware) {
	e.fingerprint = append(e.fingerprint, "middleware|"+middleware.Name()+"|"+middleware.Version())
	stable, ok := middleware.(StableRequestMiddleware)
	if !ok || !stable.StablePolicyFingerprint() {
		e.fingerprintStable = false
	}
}

func (e *trackingEditor) report() *core.AIRequestReport {
	report := reportForInfo(e.draft.Info())
	report.Adjustments = append([]core.AIRequestAdjustment(nil), e.adjustments...)
	return report
}

func (e *trackingEditor) finalReport() *core.AIRequestReport {
	report := e.report()
	identity := ""
	if provider, ok := e.draft.(fingerprintDraft); ok {
		identity = strings.TrimSpace(provider.PolicyFingerprintIdentity())
	}
	if identity == "" || !e.fingerprintStable {
		report.Stable = false
		return report
	}
	info := e.draft.Info()
	components := []string{
		"provider=" + strings.ToLower(info.Provider),
		"alias=" + strings.ToLower(info.ProviderAlias),
		"surface=" + strings.ToLower(info.Surface),
		"operation=" + strings.ToLower(info.Operation),
		"purpose=" + info.Purpose,
		"requested-model=" + strings.ToLower(info.RequestedModel),
		"resolved-model=" + strings.ToLower(info.ResolvedModel),
		"adapter=" + identity,
	}
	components = append(components, e.fingerprint...)
	sum := sha256.Sum256([]byte(strings.Join(components, "\n")))
	report.Fingerprint = hex.EncodeToString(sum[:])
	report.Stable = true
	return report
}

func reportForInfo(info RequestInfo) *core.AIRequestReport {
	return &core.AIRequestReport{
		Provider:       info.Provider,
		ProviderAlias:  info.ProviderAlias,
		Surface:        info.Surface,
		Operation:      info.Operation,
		Purpose:        info.Purpose,
		RequestedModel: info.RequestedModel,
		ResolvedModel:  info.ResolvedModel,
	}
}

func selectorFingerprint(selector core.AIProviderSelector) string {
	return strings.Join([]string{
		strings.ToLower(selector.Provider),
		strings.ToLower(selector.ProviderAlias),
		strings.ToLower(selector.Surface),
		strings.ToLower(selector.Model),
		strings.ToLower(selector.Operation),
		selector.Purpose,
		fmt.Sprint(selector.AllProviders),
	}, "/")
}

func lowerSortedKeys[V any](source map[string]V) []string {
	keys := sortedMapKeys(source)
	for index := range keys {
		keys[index] = strings.ToLower(keys[index])
	}
	sort.Strings(keys)
	return keys
}

func lowerSorted(values []string) []string {
	clone := append([]string(nil), values...)
	for index := range clone {
		clone[index] = strings.ToLower(clone[index])
	}
	sort.Strings(clone)
	return clone
}
