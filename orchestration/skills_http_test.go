package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type skillHTTPBackend struct {
	published   SkillRevisionRepresentation
	putInput    PutPublishedSkillInput
	deleteInput DeleteSkillVersionsInput
	putErr      error
	deleteErr   error
	putCalls    int
	deleteCalls int
}

func (backend *skillHTTPBackend) ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error) {
	if backend.published.Revision.Ref.Version == 0 {
		return []SkillMetadata{}, nil
	}
	return []SkillMetadata{backend.published.Revision.Metadata}, nil
}
func (*skillHTTPBackend) ResolveCandidates(context.Context, []SkillCandidateRequest) ([]SkillCandidate, error) {
	return nil, nil
}
func (*skillHTTPBackend) GetManifest(context.Context, SkillVersionRef) (SkillManifest, error) {
	return SkillManifest{}, nil
}
func (*skillHTTPBackend) GetResource(context.Context, SkillResourceRef) (SkillResource, error) {
	return SkillResource{}, nil
}
func (backend *skillHTTPBackend) GetPublished(context.Context, SkillRef) (SkillRevisionRepresentation, error) {
	if backend.published.Revision.Ref.Version == 0 {
		return SkillRevisionRepresentation{}, ErrSkillNotFound
	}
	return backend.published, nil
}
func (backend *skillHTTPBackend) GetVersion(_ context.Context, _ SkillRef, version uint64) (SkillRevisionRepresentation, error) {
	if backend.published.Revision.Ref.Version != version {
		return SkillRevisionRepresentation{}, ErrSkillRevisionNotFound
	}
	return backend.published, nil
}
func (backend *skillHTTPBackend) ListVersions(context.Context, SkillRef, SkillVersionListOptions) (SkillVersionPage, error) {
	return SkillVersionPage{Versions: []SkillRevisionSummary{{Ref: backend.published.Revision.Ref, Status: SkillRevisionRetained}}}, nil
}
func (backend *skillHTTPBackend) PutPublished(_ context.Context, input PutPublishedSkillInput) (PutPublishedSkillResult, error) {
	backend.putCalls++
	backend.putInput = input
	if backend.putErr != nil {
		return PutPublishedSkillResult{}, backend.putErr
	}
	ref := SkillVersionRef{Ref: input.Ref, Version: 1, ManifestHash: "sha256:" + strings.Repeat("a", 64)}
	result := PutPublishedSkillResult{
		Outcome: SkillAuditCreated,
		Current: PublishedSkillRevision{
			Ref: ref, Metadata: SkillMetadata{Ref: input.Ref, PublishedVersion: 1, Status: SkillPublicationPublished},
			RevisionToken: "token-1",
		},
	}
	backend.published = SkillRevisionRepresentation{
		Revision: result.Current, Package: input.Package.Package, Manifest: SkillManifest{Ref: ref},
	}
	return result, nil
}
func (backend *skillHTTPBackend) DeleteVersions(_ context.Context, input DeleteSkillVersionsInput) (DeleteSkillVersionsResult, error) {
	backend.deleteCalls++
	backend.deleteInput = input
	if backend.deleteErr != nil {
		return DeleteSkillVersionsResult{}, backend.deleteErr
	}
	return DeleteSkillVersionsResult{
		Outcome: SkillAuditDeleted, Ref: input.Ref,
		PreviousPublished: backend.published.Revision.Ref, CurrentPublished: backend.published.Revision.Ref,
		DeletedVersions: []uint64{input.FromVersion},
	}, nil
}

type skillHTTPAudit struct {
	events []SkillAuditEvent
	err    error
}

type skillHTTPAdvisor struct {
	input SkillAuthoringAnalysisInput
	calls int
}

type skillHTTPAuditAttribution struct {
	actor string
	calls int
}

func (attribution *skillHTTPAuditAttribution) SkillAuditActor(context.Context) string {
	attribution.calls++
	if attribution.actor != "" {
		return attribution.actor
	}
	return "operator"
}

func TestNewSkillAdminHandlerRejectsTypedNilCapabilities(t *testing.T) {
	var backend *skillHTTPBackend
	var advisor *skillHTTPAdvisor
	var audit *skillHTTPAudit
	var attribution *skillHTTPAuditAttribution
	var logger *core.NoOpLogger
	var telemetryProvider *skillHTTPSpanTelemetry
	tests := []struct {
		name         string
		dependencies SkillAdminHandlerDependencies
	}{
		{"registry", SkillAdminHandlerDependencies{Registry: backend}},
		{"revision reader", SkillAdminHandlerDependencies{RevisionReader: backend}},
		{"administration", SkillAdminHandlerDependencies{Administration: backend}},
		{"deletions", SkillAdminHandlerDependencies{Deletions: backend}},
		{"advisor", SkillAdminHandlerDependencies{Advisor: advisor}},
		{"audit", SkillAdminHandlerDependencies{Audit: audit}},
		{"audit attribution", SkillAdminHandlerDependencies{AuditAttribution: attribution}},
		{"logger", SkillAdminHandlerDependencies{Logger: logger}},
		{"telemetry", SkillAdminHandlerDependencies{Telemetry: telemetryProvider}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSkillAdminHandler(test.dependencies); !errors.Is(err, ErrInvalidSkillPackage) {
				t.Fatalf("NewSkillAdminHandler(typed-nil %s) error = %v", test.name, err)
			}
		})
	}
}

type skillHTTPSpanTelemetry struct {
	span *mockSpan
	ctx  context.Context
}

func (telemetry *skillHTTPSpanTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	telemetry.span = &mockSpan{name: name}
	telemetry.ctx = ctx
	return ctx, telemetry.span
}

func (*skillHTTPSpanTelemetry) RecordMetric(string, float64, map[string]string) {}

func (advisor *skillHTTPAdvisor) Analyze(
	_ context.Context,
	input SkillAuthoringAnalysisInput,
) (SkillAuthoringAdvice, error) {
	advisor.calls++
	advisor.input = input
	return SkillAuthoringAdvice{
		Summary: "The package is ready for an evaluation run.",
		Findings: []SkillAuthoringFinding{{
			Code: "evaluation_recommended", Path: "/description", Message: "Measure activation recall.",
		}},
	}, nil
}

func (audit *skillHTTPAudit) RecordSkillAudit(_ context.Context, event SkillAuditEvent) error {
	audit.events = append(audit.events, event)
	return audit.err
}

func TestSkillAdminHandlerValidateAndSchemaAreProviderNeutral(t *testing.T) {
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	schemaRequest := httptest.NewRequest(http.MethodGet, "/api/v1/skills/schema", nil)
	schemaResponse := httptest.NewRecorder()
	handler.ServeHTTP(schemaResponse, schemaRequest)
	if schemaResponse.Code != http.StatusOK ||
		!strings.Contains(schemaResponse.Body.String(), `"additionalProperties":false`) ||
		!strings.Contains(schemaResponse.Body.String(), `deterministic validate endpoint is authoritative`) {
		t.Fatalf("schema response = %d %s", schemaResponse.Code, schemaResponse.Body.String())
	}

	payload := mustSkillHTTPJSON(t, validSkillHTTPPackage())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/skills/travel/weather/validate", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"valid":true`) ||
		!strings.Contains(response.Body.String(), `"normalized"`) {
		t.Fatalf("validate response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/skills/travel/weather/validate", strings.NewReader(`{"unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field response = %d %s", response.Code, response.Body.String())
	}
}

func TestSkillAdminHandlerMountsOnlyInjectedCapabilities(t *testing.T) {
	backend := &skillHTTPBackend{}
	audit := &skillHTTPAudit{}
	tests := []struct {
		name         string
		dependencies SkillAdminHandlerDependencies
		present      []string
		absent       []string
	}{
		{
			name: "provider neutral routes only",
			present: []string{
				http.MethodGet + " /api/v1/skills/schema",
				http.MethodPost + " /api/v1/skills/travel/weather/validate",
			},
			absent: []string{
				http.MethodGet + " /api/v1/skills",
				http.MethodGet + " /api/v1/skills/travel/weather",
				http.MethodPut + " /api/v1/skills/travel/weather",
				http.MethodDelete + " /api/v1/skills/travel/weather/versions/1",
			},
		},
		{
			name:         "read capabilities",
			dependencies: SkillAdminHandlerDependencies{Registry: backend, RevisionReader: backend},
			present: []string{
				http.MethodGet + " /api/v1/skills",
				http.MethodGet + " /api/v1/skills/travel/weather",
				http.MethodGet + " /api/v1/skills/travel/weather/versions",
				http.MethodGet + " /api/v1/skills/travel/weather/versions/1",
			},
			absent: []string{
				http.MethodPut + " /api/v1/skills/travel/weather",
				http.MethodDelete + " /api/v1/skills/travel/weather/versions/1",
			},
		},
		{
			name: "mutation capabilities",
			dependencies: SkillAdminHandlerDependencies{
				Administration: backend, Deletions: backend, Audit: audit,
			},
			present: []string{
				http.MethodPut + " /api/v1/skills/travel/weather",
				http.MethodDelete + " /api/v1/skills/travel/weather/versions",
				http.MethodDelete + " /api/v1/skills/travel/weather/versions/1",
			},
		},
		{
			name:         "mutation store without required audit sink",
			dependencies: SkillAdminHandlerDependencies{Administration: backend, Deletions: backend},
			absent: []string{
				http.MethodPut + " /api/v1/skills/travel/weather",
				http.MethodDelete + " /api/v1/skills/travel/weather/versions/1",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := NewSkillAdminHandler(test.dependencies)
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range test.present {
				method, path, _ := strings.Cut(route, " ")
				request := httptest.NewRequest(method, path, nil)
				_, pattern := handler.mux.Handler(request)
				if pattern == "" {
					t.Errorf("route %s is not mounted", route)
				}
			}
			for _, route := range test.absent {
				method, path, _ := strings.Cut(route, " ")
				request := httptest.NewRequest(method, path, nil)
				_, pattern := handler.mux.Handler(request)
				if pattern != "" {
					t.Errorf("route %s unexpectedly matched %q", route, pattern)
				}
			}
		})
	}
}

func TestSkillAdminOperationDistinguishesPublishedAndExactVersionReads(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{method: http.MethodGet, path: "/api/v1/skills/travel/weather", want: "get_published"},
		{method: http.MethodGet, path: "/api/v1/skills/travel/weather/versions", want: "list_versions"},
		{method: http.MethodGet, path: "/api/v1/skills/travel/weather/versions/7", want: "get_version"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := skillAdminOperation(request); got != test.want {
			t.Errorf("skillAdminOperation(%s %s) = %q, want %q", test.method, test.path, got, test.want)
		}
	}
}

func TestSkillAdminStoreSpanRecordsBoundedOutcomeAndDuration(t *testing.T) {
	capture := &skillHTTPSpanTelemetry{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{Telemetry: capture})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.withSkillStoreSpan(t.Context(), "get_published", func(context.Context) error {
		return errors.New("provider detail")
	}); err == nil {
		t.Fatal("withSkillStoreSpan() error = nil")
	}
	if capture.span == nil || !capture.span.ended || capture.span.name != "skills.store.get_published" ||
		capture.span.attributes["skills.outcome"] != "error" || capture.span.attributes["skills.duration_ms"] == nil ||
		len(capture.span.errors) != 1 || capture.span.errors[0].Error() != "skill store operation failed" {
		t.Fatalf("store span = %#v", capture.span)
	}
}

func TestSkillAdminPropagatesExistingRequestIDThroughBaggage(t *testing.T) {
	capture := &skillHTTPSpanTelemetry{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{Telemetry: capture})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/skills/schema", nil)
	request = request.WithContext(WithRequestID(request.Context(), "existing-admin-request"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schema response = %d %s", response.Code, response.Body.String())
	}
	if got := telemetry.GetBaggage(capture.ctx)["request_id"]; got != "existing-admin-request" {
		t.Fatalf("request_id baggage = %q", got)
	}
}

func TestSkillAdminMetricsUseExactBoundedLabels(t *testing.T) {
	capture := &mockTelemetry{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{Telemetry: capture})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/skills/schema", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schema response = %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(
		http.MethodPost, "/api/v1/skills/travel/weather/validate", strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("validation response = %d %s", response.Code, response.Body.String())
	}
	foundValidationStage := false
	for _, record := range capture.metricRecords {
		if record.name == skillOperationTotalMetric && record.labels["stage"] == "config_validation" {
			foundValidationStage = record.labels["boundary"] == "admin" && record.labels["outcome"] == "error"
		}
	}
	if !foundValidationStage {
		t.Fatalf("admin validation stage metrics = %#v", capture.metricRecords)
	}
	assertSkillMetricRecordsUseExactLabels(t, capture.metricRecords)
}

func TestSkillAdminHandlerPublicationPreconditionAuditAndETag(t *testing.T) {
	backend := &skillHTTPBackend{}
	audit := &skillHTTPAudit{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Registry: backend, RevisionReader: backend, Administration: backend, Deletions: backend, Audit: audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := mustSkillHTTPJSON(t, validSkillHTTPPackage())
	request := httptest.NewRequest(http.MethodPut, "/api/v1/skills/travel/weather", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("Idempotency-Key", "request-retry-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"token-1"` ||
		backend.putCalls != 1 || !backend.putInput.RequireAbsent || len(audit.events) != 1 {
		t.Fatalf("PUT response=%d etag=%q body=%s input=%#v audits=%#v", response.Code, response.Header().Get("ETag"), response.Body.String(), backend.putInput, audit.events)
	}
	if audit.events[0].RequestID == "" || audit.events[0].Reason != validSkillHTTPPackage().ChangeReason {
		t.Fatalf("audit event = %#v", audit.events[0])
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/skills/travel/weather", nil)
	getResponse := httptest.NewRecorder()
	handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK || getResponse.Header().Get("ETag") != `"token-1"` ||
		strings.Contains(getResponse.Body.String(), "token-1") {
		t.Fatalf("GET response=%d etag=%q body=%s", getResponse.Code, getResponse.Header().Get("ETag"), getResponse.Body.String())
	}
}

func TestSkillAdminHandlerRejectsUnboundedIdempotencyKeyBeforeStore(t *testing.T) {
	backend := &skillHTTPBackend{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Administration: backend, Audit: &skillHTTPAudit{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/skills/travel/weather",
		strings.NewReader(mustSkillHTTPJSON(t, validSkillHTTPPackage())),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("Idempotency-Key", strings.Repeat("x", maxSkillIdempotencyKeyBytes+1))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || backend.putCalls != 0 ||
		!strings.Contains(response.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("response = %d %s; put calls = %d", response.Code, response.Body.String(), backend.putCalls)
	}
}

func TestSkillAdminHandlerAuditFailureDoesNotRollBackMutation(t *testing.T) {
	backend := &skillHTTPBackend{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Administration: backend, Audit: &skillHTTPAudit{err: errors.New("secret backend detail")},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/skills/travel/weather", strings.NewReader(mustSkillHTTPJSON(t, validSkillHTTPPackage())))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", "*")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || backend.putCalls != 1 ||
		!strings.Contains(response.Body.String(), `"audit_recorded":false`) || strings.Contains(response.Body.String(), "secret backend detail") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestSkillAdminHandlerOmitsInvalidAuditAttributionWithDiagnostic(t *testing.T) {
	backend := &skillHTTPBackend{}
	audit := &skillHTTPAudit{}
	attribution := &skillHTTPAuditAttribution{actor: "operator\nforged"}
	warnCalls := 0
	logger := &mockLogger{warnFunc: func(_ string, fields map[string]interface{}) {
		warnCalls++
		if fields["operation"] != "skills_admin_audit_attribution" ||
			fields["reason"] != "invalid_attribution" ||
			fields["error_type"] != "invalid_audit_attribution" {
			t.Fatalf("audit-attribution diagnostic fields = %#v", fields)
		}
		if strings.Contains(mustSkillHTTPJSON(t, fields), "forged") {
			t.Fatalf("audit-attribution diagnostic exposed rejected value: %#v", fields)
		}
	}}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Administration: backend, Audit: audit, AuditAttribution: attribution, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/skills/travel/weather",
		strings.NewReader(mustSkillHTTPJSON(t, validSkillHTTPPackage())),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-None-Match", "*")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(audit.events) != 1 ||
		audit.events[0].Actor != "" || attribution.calls != 1 || warnCalls != 1 {
		t.Fatalf(
			"response=%d audits=%#v attribution_calls=%d warning_calls=%d",
			response.Code, audit.events, attribution.calls, warnCalls,
		)
	}
}

func TestSkillAdminHandlerDeleteLimitsAndConflictMapping(t *testing.T) {
	backend := &skillHTTPBackend{published: SkillRevisionRepresentation{Revision: PublishedSkillRevision{
		Ref: SkillVersionRef{Ref: SkillRef{Namespace: "travel", Name: "weather"}, Version: 6}, RevisionToken: "token-6",
	}}}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Deletions: backend, Audit: &skillHTTPAudit{},
		AdministrationLimits: SkillAdministrationLimits{MaxDeleteVersions: 3, MaxAuthoringAdviceOutputTokens: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/skills/travel/weather/versions?from=1&to=4", nil)
	request.Header.Set("If-Match", `"token-6"`)
	request.Header.Set("X-Audit-Reason", "retention")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || backend.deleteCalls != 0 {
		t.Fatalf("range-limit response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/travel/weather/versions?from=1&to=18446744073709551615", nil)
	request.Header.Set("If-Match", `"token-6"`)
	request.Header.Set("X-Audit-Reason", "retention")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || backend.deleteCalls != 0 ||
		!strings.Contains(response.Body.String(), "skill_delete_range_limit") {
		t.Fatalf("overflowing range response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/travel/weather/versions?from=1&from=2&to=3", nil)
	request.Header.Set("If-Match", `"token-6"`)
	request.Header.Set("X-Audit-Reason", "retention")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || backend.deleteCalls != 0 ||
		!strings.Contains(response.Body.String(), "invalid_query") {
		t.Fatalf("ambiguous range response = %d %s", response.Code, response.Body.String())
	}

	backend.deleteErr = ErrSkillProtectedRevision
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/travel/weather/versions/4", nil)
	request.Header.Set("If-Match", `"token-6"`)
	request.Header.Set("X-Audit-Reason", "retention")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "protected_skill_revisions") {
		t.Fatalf("protected response = %d %s", response.Code, response.Body.String())
	}

	backend.deleteErr = ErrSkillPrecondition
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/skills/travel/weather/versions/4", nil)
	request.Header.Set("If-Match", `"token-6"`)
	request.Header.Set("X-Audit-Reason", "retention")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("precondition response = %d %s", response.Code, response.Body.String())
	}
}

func TestSkillAdminHandlerRejectsMalformedAndUndocumentedQueryParameters(t *testing.T) {
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rawQuery := range []string{"unexpected=value", "limit=%zz"} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/skills/schema", nil)
		request.URL.RawQuery = rawQuery
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_query") {
			t.Fatalf("query %q response = %d %s", rawQuery, response.Code, response.Body.String())
		}
	}
}

func TestSkillAdminHandlerAnalyzeRouteIsAbsentWithoutAdvisor(t *testing.T) {
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/skills/travel/weather/analyze", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("analysis route = %d, want 404", response.Code)
	}
}

func TestSkillAdminHandlerCatalogHistoryAndAnalysisRoutes(t *testing.T) {
	ref := SkillRef{Namespace: "travel", Name: "weather"}
	backend := &skillHTTPBackend{published: SkillRevisionRepresentation{
		Revision: PublishedSkillRevision{
			Ref: SkillVersionRef{Ref: ref, Version: 1, ManifestHash: "sha256:" + strings.Repeat("a", 64)},
			Metadata: SkillMetadata{
				Ref: ref, DisplayName: "Weather Assessment", Description: "Assess travel weather.",
				Domains: []string{"travel"}, Tags: []string{"weather"}, PublishedVersion: 1,
				Status: SkillPublicationPublished,
			},
			RevisionToken: "token-1",
		},
		Package: validSkillHTTPPackage(),
	}}
	advisor := &skillHTTPAdvisor{}
	handler, err := NewSkillAdminHandler(SkillAdminHandlerDependencies{
		Registry: backend, RevisionReader: backend, Advisor: advisor,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/skills?namespace=travel&domain=travel&tag=weather&limit=5", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Weather Assessment") {
		t.Fatalf("catalog response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/skills?limit=invalid", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_limit") {
		t.Fatalf("invalid catalog limit response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/skills/travel/weather/versions?before=2&limit=1", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":1`) {
		t.Fatalf("history response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/skills/travel/weather/versions/1", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"change_reason":"Initial publication"`) {
		t.Fatalf("version response = %d %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/skills/travel/weather/analyze",
		strings.NewReader(mustSkillHTTPJSON(t, validSkillHTTPPackage())),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || advisor.calls != 1 || advisor.input.Ref != ref ||
		!strings.Contains(response.Body.String(), "evaluation_recommended") {
		t.Fatalf("analysis response = %d %s; advisor=%#v", response.Code, response.Body.String(), advisor)
	}
}

func validSkillHTTPPackage() SkillPackageInput {
	return SkillPackageInput{
		DisplayName: "Weather Assessment",
		Description: "Assess travel weather. Use when a request asks about weather risk.",
		Domains:     []string{"travel"}, Tags: []string{"weather"},
		PlanningInstructions: []string{"Retrieve the relevant forecast before assessing travel risk."},
		ResponseInstructions: []string{"State uncertainty."},
		Resources: []SkillResourceInput{{
			Name: "forecast", Description: "Forecast interpretation guidance.",
			LoadWhen: "A future forecast determines travel risk.", AppliesTo: []SkillResourceScope{SkillResourcePlanning},
			ContentType: "text/markdown", Content: "Use forecast confidence and horizon.",
		}},
		ChangeReason: "Initial publication",
	}
}

func mustSkillHTTPJSON(t *testing.T, value interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSkillMetricDiagnosticCodeRecognizesPromptQualityWarning(t *testing.T) {
	if got := skillMetricDiagnosticCode("negative_instruction_phrasing"); got != "negative_instruction_phrasing" {
		t.Fatalf("skillMetricDiagnosticCode() = %q", got)
	}
}
