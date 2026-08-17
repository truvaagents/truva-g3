package orchestration

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSkillRefStringUsesNamespaceAndNameOnly(t *testing.T) {
	ref := SkillRef{Namespace: "travel", Name: "action-verification"}
	if got, want := ref.String(), "travel/action-verification"; got != want {
		t.Fatalf("SkillRef.String() = %q, want %q", got, want)
	}
}

func TestSkillBindingRefAndJSONRoundTrip(t *testing.T) {
	binding := SkillBinding{
		Namespace: "devops", Name: "pod-troubleshooting", Version: "published",
		Activation: SkillActivationAuto, Required: true,
	}
	if got, want := binding.Ref(), (SkillRef{Namespace: "devops", Name: "pod-troubleshooting"}); got != want {
		t.Fatalf("SkillBinding.Ref() = %#v, want %#v", got, want)
	}

	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded SkillBinding
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, binding) {
		t.Fatalf("round trip = %#v, want %#v", decoded, binding)
	}
}

func TestSkillPackageInputDoesNotAcceptServerAssignedFieldsByShape(t *testing.T) {
	typeFields := reflect.VisibleFields(reflect.TypeFor[SkillPackageInput]())
	for _, field := range typeFields {
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		switch jsonName {
		case "namespace", "name", "version", "status", "published_version", "manifest_hash", "resource_hash":
			t.Fatalf("SkillPackageInput unexpectedly exposes server-owned field %q", jsonName)
		}
	}
}

func TestRuntimeReferenceTypesContainNoContentBodies(t *testing.T) {
	assertNoJSONFields(t, reflect.TypeFor[SkillRef](), "content", "planning_instructions", "response_instructions")
	assertNoJSONFields(t, reflect.TypeFor[SkillVersionRef](), "content", "planning_instructions", "response_instructions")
	assertNoJSONFields(t, reflect.TypeFor[SkillResourceRef](), "content", "planning_instructions", "response_instructions")
	assertNoJSONFields(t, reflect.TypeFor[SkillCandidate](), "content", "planning_instructions", "response_instructions")
	assertNoJSONFields(t, reflect.TypeFor[SkillAuditEvent](), "content", "planning_instructions", "response_instructions", "package")
}

func TestRuntimeStateAndDebugGraphsContainNoBodies(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[SkillSnapshot](),
		reflect.TypeFor[SkillExecutionState](),
		reflect.TypeFor[SkillExecutionDebug](),
	} {
		assertTypeGraphHasNoJSONFields(t, typ,
			"content",
			"package",
			"manifest",
			"planning_instructions",
			"response_instructions",
			"tool_hints",
			"activation_examples",
			"change_reason",
		)
	}
}

func TestControlPlaneSecretsAndConcurrencyTokensAreNotJSON(t *testing.T) {
	ref := SkillRef{Namespace: "travel", Name: "weather-assessment"}
	values := []interface{}{
		PublishedSkillRevision{
			Ref:           SkillVersionRef{Ref: ref, Version: 1, ManifestHash: "sha256:manifest"},
			RevisionToken: "secret-etag",
		},
		PutPublishedSkillInput{
			Ref: ref, ExpectedRevisionToken: "secret-etag", IdempotencyKey: "secret-idempotency",
		},
		DeleteSkillVersionsInput{
			Ref: ref, FromVersion: 1, ToVersion: 1, ExpectedRevisionToken: "secret-etag",
		},
	}
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", value, err)
		}
		for _, forbidden := range []string{"secret-etag", "secret-idempotency", "revision_token", "idempotency_key"} {
			if strings.Contains(string(data), forbidden) {
				t.Errorf("json.Marshal(%T) contains %q: %s", value, forbidden, data)
			}
		}
	}
}

func TestSkillRuntimeDTOJSONRoundTrip(t *testing.T) {
	ref := SkillRef{Namespace: "travel", Name: "weather-assessment"}
	version := SkillVersionRef{Ref: ref, Version: 2, ManifestHash: "sha256:manifest"}
	state := SkillExecutionState{
		Pinned: &SkillSnapshot{
			EffectiveBindings: []SkillBinding{{
				Namespace: "travel", Name: "weather-assessment", Version: "published",
				Activation: SkillActivationAuto,
			}},
			Candidates:       []SkillCandidate{{Ref: ref, RequestedVersion: "published", Resolved: version, Status: SkillCandidateResolved}},
			CacheFingerprint: "sha256:cache",
		},
		ActiveSkills: []ActiveSkill{{
			Binding:  SkillBinding{Namespace: "travel", Name: "weather-assessment", Version: "published", Activation: SkillActivationAuto},
			Skill:    version,
			Selector: SkillDecisionDefaultAI,
			Reason:   "weather risk",
		}},
		Debug: SkillExecutionDebug{
			BindingSource:      SkillBindingsFromCode,
			BindingFingerprint: "sha256:bindings",
			BudgetFingerprint:  "sha256:budgets",
			CacheFingerprint:   "sha256:cache",
			Candidates: []SkillCandidateDebug{{
				Sequence: 1, Ref: ref, RequestedVersion: "published",
				DisplayName: "Weather Assessment", Description: "Assess travel weather.",
				Activation: SkillActivationAuto, Status: SkillCandidateResolved, Resolved: &version,
			}},
		},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded SkillExecutionState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Fatalf("round trip = %#v, want %#v", decoded, state)
	}
}

func TestSkillDTOZeroValuesMarshal(t *testing.T) {
	values := []interface{}{
		SkillRef{}, SkillVersionRef{}, SkillResourceRef{}, SkillBinding{},
		SkillPackageInput{}, SkillManifest{}, SkillResource{}, SkillCandidate{},
		SkillRevisionRepresentation{}, SkillValidationResult{}, SkillSnapshot{},
		SkillExecutionState{}, SkillExecutionDebug{}, SkillAuditEvent{},
	}
	for _, value := range values {
		if _, err := json.Marshal(value); err != nil {
			t.Errorf("json.Marshal(%T zero value) error = %v", value, err)
		}
	}
}

func TestSkillDomainErrorPreservesCategory(t *testing.T) {
	ref := SkillRef{Namespace: "travel", Name: "weather-assessment"}
	err := newSkillDomainError(ErrSkillIntegrity, "load resource", ref)
	if !errors.Is(err, ErrSkillIntegrity) {
		t.Fatalf("errors.Is(%v, ErrSkillIntegrity) = false", err)
	}
	var domainErr *SkillDomainError
	if !errors.As(err, &domainErr) {
		t.Fatalf("errors.As(%T, *SkillDomainError) = false", err)
	}
	if domainErr.Ref != ref || domainErr.Operation != "load resource" {
		t.Fatalf("SkillDomainError = %#v", domainErr)
	}
	if got := err.Error(); got != "skill load resource failed for travel/weather-assessment" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestSkillAuditEventOmitsRevisionTokenAndBodies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	event := SkillAuditEvent{
		EventID: "event-1", RequestID: "request-1", OccurredAt: now,
		Action: SkillAuditPutPublished, Outcome: SkillAuditCreated,
		Ref: SkillRef{Namespace: "travel", Name: "weather-assessment"},
		Current: &SkillVersionRef{
			Ref:     SkillRef{Namespace: "travel", Name: "weather-assessment"},
			Version: 1, ManifestHash: "sha256:abc",
		},
		Reason: "initial publication",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{"revision_token", "content", "planning_instructions", "idempotency_key"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("audit JSON contains forbidden field %q: %s", forbidden, data)
		}
	}
}

func TestSkillEnumWireValues(t *testing.T) {
	tests := map[string]string{
		"activation always":   string(SkillActivationAlways),
		"activation auto":     string(SkillActivationAuto),
		"activation explicit": string(SkillActivationExplicit),
		"candidate resolved":  string(SkillCandidateResolved),
		"candidate not found": string(SkillCandidateNotFound),
		"binding code":        string(SkillBindingsFromCode),
		"audit no-op":         string(SkillAuditSameContentNoOp),
		"resource planning":   string(SkillResourcePlanning),
		"initial boundary":    string(SkillBoundaryInitialPlanning),
		"published status":    string(SkillPublicationPublished),
		"revision retained":   string(SkillRevisionRetained),
		"validation warning":  string(SkillValidationWarning),
		"decision default AI": string(SkillDecisionDefaultAI),
		"domain warn":         string(SkillDomainCompatibilityWarn),
		"cache disabled":      string(SkillContentCacheDisabled),
	}
	wants := map[string]string{
		"activation always":   "always",
		"activation auto":     "auto",
		"activation explicit": "explicit",
		"candidate resolved":  "resolved",
		"candidate not found": "not_found",
		"binding code":        "code",
		"audit no-op":         "same_content_noop",
		"resource planning":   "planning",
		"initial boundary":    "initial_planning",
		"published status":    "published",
		"revision retained":   "retained",
		"validation warning":  "warning",
		"decision default AI": "default_ai",
		"domain warn":         "warn",
		"cache disabled":      "disabled",
	}
	for name, got := range tests {
		if got != wants[name] {
			t.Errorf("%s = %q, want %q", name, got, wants[name])
		}
	}
}

func TestSkillClosedEnumsRejectUnknownJSONValues(t *testing.T) {
	tests := []struct {
		name   string
		target interface{}
	}{
		{"activation", new(SkillActivation)},
		{"binding source", new(SkillBindingSource)},
		{"resource scope", new(SkillResourceScope)},
		{"domain compatibility mode", new(SkillDomainCompatibilityMode)},
		{"content cache mode", new(SkillContentCacheMode)},
		{"prompt boundary", new(SkillPromptBoundary)},
		{"publication status", new(SkillPublicationStatus)},
		{"candidate status", new(SkillCandidateStatus)},
		{"revision status", new(SkillRevisionStatus)},
		{"validation severity", new(SkillValidationSeverity)},
		{"audit action", new(SkillAuditAction)},
		{"audit outcome", new(SkillAuditOutcome)},
		{"decision source", new(SkillDecisionSource)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(`"unknown"`), test.target); !errors.Is(err, ErrInvalidSkillPackage) {
				t.Fatalf("json.Unmarshal() error = %v, want ErrInvalidSkillPackage", err)
			}
		})
	}
}

func TestFormatSkillVersion(t *testing.T) {
	ref := SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "baseline"}, Version: 7}
	if got, want := formatSkillVersion(ref), "travel/baseline@7"; got != want {
		t.Fatalf("formatSkillVersion() = %q, want %q", got, want)
	}
}

func assertNoJSONFields(t *testing.T, typ reflect.Type, forbidden ...string) {
	t.Helper()
	fields := make(map[string]struct{})
	for _, field := range reflect.VisibleFields(typ) {
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		fields[name] = struct{}{}
	}
	for _, name := range forbidden {
		if _, found := fields[name]; found {
			t.Errorf("%s contains forbidden JSON field %q", typ, name)
		}
	}
}

func assertTypeGraphHasNoJSONFields(t *testing.T, root reflect.Type, forbidden ...string) {
	t.Helper()
	forbiddenSet := make(map[string]struct{}, len(forbidden))
	for _, name := range forbidden {
		forbiddenSet[name] = struct{}{}
	}
	visited := make(map[reflect.Type]struct{})
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ.PkgPath() != reflect.TypeFor[SkillRef]().PkgPath() {
			return
		}
		if _, found := visited[typ]; found {
			return
		}
		visited[typ] = struct{}{}
		for _, field := range reflect.VisibleFields(typ) {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if _, found := forbiddenSet[name]; found {
				t.Errorf("%s reachable from %s contains forbidden JSON field %q", typ, root, name)
			}
			visit(field.Type)
		}
	}
	visit(root)
}
