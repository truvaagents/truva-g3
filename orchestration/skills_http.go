package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultSkillDeleteVersionLimit = 100
	defaultSkillAdviceOutputTokens = 1024
	maxSkillAuditReasonBytes       = 1024
	maxSkillAuditActorBytes        = 256
	maxSkillIdempotencyKeyBytes    = 256
)

// SkillAdministrationLimits bound control-plane operations independently from
// authoring payload limits.
type SkillAdministrationLimits struct {
	MaxDeleteVersions              int `json:"max_delete_versions"`
	MaxAuthoringAdviceOutputTokens int `json:"max_authoring_advice_output_tokens"`
}

func DefaultSkillAdministrationLimits() SkillAdministrationLimits {
	return SkillAdministrationLimits{
		MaxDeleteVersions:              defaultSkillDeleteVersionLimit,
		MaxAuthoringAdviceOutputTokens: defaultSkillAdviceOutputTokens,
	}
}

func (limits SkillAdministrationLimits) Validate() error {
	if limits.MaxDeleteVersions <= 0 || limits.MaxAuthoringAdviceOutputTokens <= 0 {
		return fmt.Errorf("%w: skill administration limits must be positive", ErrInvalidSkillPackage)
	}
	return nil
}

// SkillAdminHandlerDependencies contains only provider-neutral capabilities.
// A host owns HTTP serving, middleware, authentication, and authorization.
type SkillAdminHandlerDependencies struct {
	Registry             SkillRegistry
	RevisionReader       SkillRevisionReader
	Administration       SkillAdministrationStore
	Deletions            SkillRevisionDeletionStore
	AuthoringLimits      SkillAuthoringLimits
	AdministrationLimits SkillAdministrationLimits
	ValidationRules      []SkillValidationRule
	Advisor              SkillAuthoringAdvisor
	Audit                SkillAuditSink
	AuditAttribution     SkillAuditAttributionProvider
	Logger               core.Logger
	Telemetry            core.Telemetry
}

type SkillAdminHandler struct {
	mux                  *http.ServeMux
	registry             SkillRegistry
	revisions            SkillRevisionReader
	administration       SkillAdministrationStore
	deletions            SkillRevisionDeletionStore
	validator            SkillPackageValidator
	advisor              SkillAuthoringAdvisor
	audit                SkillAuditSink
	auditAttribution     SkillAuditAttributionProvider
	authoringLimits      SkillAuthoringLimits
	administrationLimits SkillAdministrationLimits
	logger               core.Logger
	telemetry            core.Telemetry
}

type SkillValidationResponse struct {
	Normalized *SkillPackageInput    `json:"normalized,omitempty"`
	Validation SkillValidationResult `json:"validation"`
}

type SkillAnalysisResponse struct {
	Normalized *SkillPackageInput    `json:"normalized,omitempty"`
	Validation SkillValidationResult `json:"validation"`
	Advice     SkillAuthoringAdvice  `json:"advice"`
}

type SkillOperationWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SkillMutationResponse struct {
	Result        PutPublishedSkillResult `json:"result"`
	Validation    SkillValidationResult   `json:"validation"`
	AuditRecorded bool                    `json:"audit_recorded"`
	Warnings      []SkillOperationWarning `json:"warnings,omitempty"`
}

type SkillDeleteMutationResponse struct {
	Result        DeleteSkillVersionsResult `json:"result"`
	AuditRecorded bool                      `json:"audit_recorded"`
	Warnings      []SkillOperationWarning   `json:"warnings,omitempty"`
}

type skillHTTPError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewSkillAdminHandler(dependencies SkillAdminHandlerDependencies) (*SkillAdminHandler, error) {
	for _, dependency := range []struct {
		name  string
		value interface{}
	}{
		{"registry", dependencies.Registry},
		{"revision reader", dependencies.RevisionReader},
		{"administration store", dependencies.Administration},
		{"deletion store", dependencies.Deletions},
		{"advisor", dependencies.Advisor},
		{"audit sink", dependencies.Audit},
		{"audit attribution provider", dependencies.AuditAttribution},
		{"logger", dependencies.Logger},
		{"telemetry", dependencies.Telemetry},
	} {
		if dependency.value != nil && isNilBackendValue(dependency.value) {
			return nil, fmt.Errorf(
				"%w: skill administration %s dependency is typed nil",
				ErrInvalidSkillPackage,
				dependency.name,
			)
		}
	}
	authoringLimits := dependencies.AuthoringLimits
	if authoringLimits == (SkillAuthoringLimits{}) {
		authoringLimits = DefaultSkillAuthoringLimits()
	}
	validator, err := NewDefaultSkillPackageValidator(authoringLimits, dependencies.ValidationRules...)
	if err != nil {
		return nil, err
	}
	adminLimits := dependencies.AdministrationLimits
	if adminLimits == (SkillAdministrationLimits{}) {
		adminLimits = DefaultSkillAdministrationLimits()
	}
	if err := adminLimits.Validate(); err != nil {
		return nil, err
	}
	logger := dependencies.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	telemetryProvider := dependencies.Telemetry
	if telemetryProvider == nil {
		telemetryProvider = &core.NoOpTelemetry{}
	}
	handler := &SkillAdminHandler{
		registry: dependencies.Registry, revisions: dependencies.RevisionReader,
		administration: dependencies.Administration, deletions: dependencies.Deletions,
		validator: validator, advisor: dependencies.Advisor, audit: dependencies.Audit,
		auditAttribution: dependencies.AuditAttribution,
		authoringLimits:  authoringLimits, administrationLimits: adminLimits,
		logger: logger, telemetry: telemetryProvider,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/skills/schema", handler.getSchema)
	mux.HandleFunc("POST /api/v1/skills/{namespace}/{name}/validate", handler.validateSkill)
	if handler.registry != nil {
		mux.HandleFunc("GET /api/v1/skills", handler.listSkills)
	}
	if handler.advisor != nil {
		mux.HandleFunc("POST /api/v1/skills/{namespace}/{name}/analyze", handler.analyzeSkill)
	}
	if handler.revisions != nil {
		mux.HandleFunc("GET /api/v1/skills/{namespace}/{name}", handler.getPublished)
		mux.HandleFunc("GET /api/v1/skills/{namespace}/{name}/versions", handler.listVersions)
		mux.HandleFunc("GET /api/v1/skills/{namespace}/{name}/versions/{version}", handler.getVersion)
	}
	if handler.administration != nil && handler.audit != nil {
		mux.HandleFunc("PUT /api/v1/skills/{namespace}/{name}", handler.putPublished)
	}
	if handler.deletions != nil && handler.audit != nil {
		mux.HandleFunc("DELETE /api/v1/skills/{namespace}/{name}/versions", handler.deleteVersionRange)
		mux.HandleFunc("DELETE /api/v1/skills/{namespace}/{name}/versions/{version}", handler.deleteSingleVersion)
	}
	handler.mux = mux
	return handler, nil
}

func (handler *SkillAdminHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || handler.mux == nil {
		http.Error(writer, "skill administration unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx := request.Context()
	requestID := GetRequestID(ctx)
	if requestID == "" {
		requestID = "skills-admin-" + uuid.NewString()
		ctx = WithRequestID(ctx, requestID)
	}
	// WithRequestID is the framework's local correlation seam; baggage is the
	// propagation surface consumed by common span attributes and instrumented
	// AI clients. Keep both views identical even when host middleware supplied
	// only the local context value.
	ctx = telemetry.WithBaggage(ctx, "request_id", requestID)
	request = request.WithContext(ctx)
	operation := skillAdminOperation(request)
	ctx, span := handler.telemetry.StartSpan(request.Context(), "skills.admin."+operation)
	telemetry.SetCommonAttrsOn(ctx, span)
	span.SetAttribute("skills.operation", operation)
	request = request.WithContext(ctx)
	capture := &skillHTTPStatusWriter{ResponseWriter: writer, status: http.StatusOK}
	startedAt := time.Now()
	if !validSkillAdminQuery(request) {
		writeSkillHTTPError(capture, http.StatusBadRequest, "invalid_query", "Query parameters must use the documented names exactly once.")
	} else {
		handler.mux.ServeHTTP(capture, request)
	}
	duration := time.Since(startedAt)
	outcome := skillAdminHTTPOutcome(capture.status)
	span.SetAttribute("skills.outcome", outcome)
	span.SetAttribute("skills.duration_ms", duration.Milliseconds())
	span.End()
	labels := map[string]string{"module": telemetry.ModuleOrchestration, "operation": operation, "outcome": outcome}
	handler.telemetry.RecordMetric(skillAdminOperationTotalMetric, 1, labels)
	handler.telemetry.RecordMetric(skillAdminOperationDurationMetric, float64(duration.Milliseconds()), labels)
	handler.logger.DebugWithContext(ctx, "Skill administration request completed", map[string]interface{}{
		"operation": "skills_admin_" + operation, "request_id": GetRequestID(ctx),
		"status": outcome, "duration_ms": duration.Milliseconds(),
	})
}

func validSkillAdminQuery(request *http.Request) bool {
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	allowed := map[string]struct{}{}
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/v1/skills":
		allowed = map[string]struct{}{"namespace": {}, "domain": {}, "tag": {}, "limit": {}}
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/versions"):
		allowed = map[string]struct{}{"before": {}, "limit": {}}
	case request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, "/versions"):
		allowed = map[string]struct{}{"from": {}, "to": {}}
	}
	for key, values := range query {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}

func (handler *SkillAdminHandler) listSkills(writer http.ResponseWriter, request *http.Request) {
	if handler.registry == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill catalog access is not configured.")
		return
	}
	filter := SkillMetadataFilter{
		Namespace: request.URL.Query().Get("namespace"), Domain: request.URL.Query().Get("domain"),
		Tag: request.URL.Query().Get("tag"),
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_limit", "Limit must be a positive integer.")
			return
		}
		filter.Limit = limit
	}
	var result []SkillMetadata
	err := handler.withSkillStoreSpan(request.Context(), "list", func(ctx context.Context) error {
		var err error
		result, err = handler.registry.ListMetadata(ctx, filter)
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	writeSkillJSON(writer, http.StatusOK, map[string]interface{}{"skills": result})
}

func (handler *SkillAdminHandler) getSchema(writer http.ResponseWriter, _ *http.Request) {
	writeSkillJSON(writer, http.StatusOK, skillPackageJSONSchema(handler.authoringLimits))
}

func (handler *SkillAdminHandler) validateSkill(writer http.ResponseWriter, request *http.Request) {
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	input, ok := handler.decodePackage(writer, request)
	if !ok {
		return
	}
	validated, validation, err := handler.validatePackage(request.Context(), ref, input, "validate")
	response := SkillValidationResponse{Validation: validation}
	if err == nil {
		normalized := validated.Package
		response.Normalized = &normalized
	}
	if err != nil && !errors.Is(err, ErrInvalidSkillPackage) {
		handler.writeDomainError(writer, err, false)
		return
	}
	writeSkillJSON(writer, http.StatusOK, response)
}

func (handler *SkillAdminHandler) analyzeSkill(writer http.ResponseWriter, request *http.Request) {
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	input, ok := handler.decodePackage(writer, request)
	if !ok {
		return
	}
	validated, validation, err := handler.validatePackage(request.Context(), ref, input, "analyze")
	if err != nil {
		if errors.Is(err, ErrInvalidSkillPackage) {
			writeSkillJSON(writer, http.StatusOK, SkillAnalysisResponse{Validation: validation})
			return
		}
		handler.writeDomainError(writer, err, false)
		return
	}
	advice, err := handler.advisor.Analyze(request.Context(), SkillAuthoringAnalysisInput{
		Ref: ref, Package: validated.Package, Validation: validation,
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	normalized := validated.Package
	writeSkillJSON(writer, http.StatusOK, SkillAnalysisResponse{
		Normalized: &normalized, Validation: validation, Advice: advice,
	})
}

func (handler *SkillAdminHandler) putPublished(writer http.ResponseWriter, request *http.Request) {
	if handler.administration == nil || handler.audit == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill publication is not configured.")
		return
	}
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	input, ok := handler.decodePackage(writer, request)
	if !ok {
		return
	}
	validated, validation, err := handler.validatePackage(request.Context(), ref, input, "put_published")
	if err != nil {
		if errors.Is(err, ErrInvalidSkillPackage) {
			writeSkillJSON(writer, http.StatusUnprocessableEntity, SkillValidationResponse{Validation: validation})
			return
		}
		handler.writeDomainError(writer, err, false)
		return
	}
	requireAbsent := strings.TrimSpace(request.Header.Get("If-None-Match")) == "*"
	expected, hasExpected := parseSkillETag(request.Header.Get("If-Match"))
	if requireAbsent == hasExpected {
		writeSkillHTTPError(writer, http.StatusPreconditionRequired, "skill_precondition_required", "Supply exactly one of If-None-Match: * or a current If-Match ETag.")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey != "" && !validBoundedSkillText(idempotencyKey, maxSkillIdempotencyKeyBytes) {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must be bounded text.")
		return
	}
	var result PutPublishedSkillResult
	err = handler.withSkillStoreSpan(request.Context(), "put_published", func(ctx context.Context) error {
		var err error
		result, err = handler.administration.PutPublished(ctx, PutPublishedSkillInput{
			Ref: ref, Package: validated, RequireAbsent: requireAbsent,
			ExpectedRevisionToken: expected, IdempotencyKey: idempotencyKey,
		})
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, true)
		return
	}
	actor := handler.auditActor(request.Context())
	auditRecorded, warnings := handler.recordMutationAudit(request.Context(), SkillAuditEvent{
		EventID: uuid.NewString(), RequestID: GetRequestID(request.Context()), OccurredAt: time.Now().UTC(),
		Action: SkillAuditPutPublished, Outcome: result.Outcome, Ref: ref,
		Previous: result.Previous, Current: &result.Current.Ref, Actor: actor,
		Reason: validated.Package.ChangeReason,
	})
	writer.Header().Set("ETag", formatSkillETag(result.Current.RevisionToken))
	writer.Header().Set("Location", "/api/v1/skills/"+ref.Namespace+"/"+ref.Name)
	status := http.StatusOK
	if result.Outcome == SkillAuditCreated {
		status = http.StatusCreated
	}
	writeSkillJSON(writer, status, SkillMutationResponse{
		Result: result, Validation: validation, AuditRecorded: auditRecorded, Warnings: warnings,
	})
}

func (handler *SkillAdminHandler) getPublished(writer http.ResponseWriter, request *http.Request) {
	if handler.revisions == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill revision access is not configured.")
		return
	}
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	var representation SkillRevisionRepresentation
	err := handler.withSkillStoreSpan(request.Context(), "get_published", func(ctx context.Context) error {
		var err error
		representation, err = handler.revisions.GetPublished(ctx, ref)
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	writer.Header().Set("ETag", formatSkillETag(representation.Revision.RevisionToken))
	writeSkillJSON(writer, http.StatusOK, representation)
}

func (handler *SkillAdminHandler) listVersions(writer http.ResponseWriter, request *http.Request) {
	if handler.revisions == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill revision access is not configured.")
		return
	}
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	options := SkillVersionListOptions{}
	if raw := request.URL.Query().Get("before"); raw != "" {
		options.BeforeVersion, ok = parsePositiveSkillVersion(raw)
		if !ok {
			writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_before_version", "Before-version must be a positive integer.")
			return
		}
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		options.Limit, _ = strconv.Atoi(raw)
		if options.Limit <= 0 {
			writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_limit", "Limit must be a positive integer.")
			return
		}
	}
	var page SkillVersionPage
	err := handler.withSkillStoreSpan(request.Context(), "list_versions", func(ctx context.Context) error {
		var err error
		page, err = handler.revisions.ListVersions(ctx, ref, options)
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	writeSkillJSON(writer, http.StatusOK, page)
}

func (handler *SkillAdminHandler) getVersion(writer http.ResponseWriter, request *http.Request) {
	if handler.revisions == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill revision access is not configured.")
		return
	}
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	version, ok := parsePositiveSkillVersion(request.PathValue("version"))
	if !ok {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_version", "Version must be a positive integer.")
		return
	}
	var representation SkillRevisionRepresentation
	err := handler.withSkillStoreSpan(request.Context(), "get_version", func(ctx context.Context) error {
		var err error
		representation, err = handler.revisions.GetVersion(ctx, ref, version)
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	writeSkillJSON(writer, http.StatusOK, representation)
}

func (handler *SkillAdminHandler) deleteSingleVersion(writer http.ResponseWriter, request *http.Request) {
	version, ok := parsePositiveSkillVersion(request.PathValue("version"))
	if !ok {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_version", "Version must be a positive integer.")
		return
	}
	handler.deleteVersions(writer, request, version, version)
}

func (handler *SkillAdminHandler) deleteVersionRange(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	from, fromOK := parsePositiveSkillVersion(query.Get("from"))
	to, toOK := parsePositiveSkillVersion(query.Get("to"))
	if !fromOK || !toOK || from > to {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_version_range", "From and to must define a positive inclusive range.")
		return
	}
	handler.deleteVersions(writer, request, from, to)
}

func (handler *SkillAdminHandler) deleteVersions(writer http.ResponseWriter, request *http.Request, from, to uint64) {
	if handler.deletions == nil || handler.audit == nil {
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_route_unavailable", "Skill revision deletion is not configured.")
		return
	}
	// Compare the zero-based delta before adding one so a range ending at
	// math.MaxUint64 cannot wrap and bypass the configured inclusive limit.
	// MaxDeleteVersions is constructor-validated as positive, so converting the
	// non-negative Go int to uint64 is safe on every supported architecture.
	deleteVersionLimit := uint64(handler.administrationLimits.MaxDeleteVersions) // #nosec G115
	if to-from >= deleteVersionLimit {
		writeSkillHTTPError(writer, http.StatusBadRequest, "skill_delete_range_limit", "Deletion range exceeds the configured limit.")
		return
	}
	ref, ok := handler.pathRef(writer, request)
	if !ok {
		return
	}
	expected, ok := parseSkillETag(request.Header.Get("If-Match"))
	if !ok {
		writeSkillHTTPError(writer, http.StatusPreconditionRequired, "skill_precondition_required", "Supply the current If-Match ETag.")
		return
	}
	reason := strings.TrimSpace(request.Header.Get("X-Audit-Reason"))
	if !validBoundedSkillText(reason, maxSkillAuditReasonBytes) {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_audit_reason", "X-Audit-Reason is required and must be bounded text.")
		return
	}
	actor := handler.auditActor(request.Context())
	var result DeleteSkillVersionsResult
	err := handler.withSkillStoreSpan(request.Context(), "delete_versions", func(ctx context.Context) error {
		var err error
		result, err = handler.deletions.DeleteVersions(ctx, DeleteSkillVersionsInput{
			Ref: ref, FromVersion: from, ToVersion: to, ExpectedRevisionToken: expected,
			Reason: reason, Actor: actor,
		})
		return err
	})
	if err != nil {
		handler.writeDomainError(writer, err, false)
		return
	}
	auditRecorded, warnings := handler.recordMutationAudit(request.Context(), SkillAuditEvent{
		EventID: uuid.NewString(), RequestID: GetRequestID(request.Context()), OccurredAt: time.Now().UTC(),
		Action: SkillAuditDeleteVersions, Outcome: result.Outcome, Ref: ref,
		Previous: &result.PreviousPublished, Current: &result.CurrentPublished,
		DeletedVersions: result.DeletedVersions, AlreadyDeletedVersions: result.AlreadyDeletedVersions,
		Actor: actor, Reason: reason,
	})
	writer.Header().Set("ETag", formatSkillETag(expected))
	writeSkillJSON(writer, http.StatusOK, SkillDeleteMutationResponse{
		Result: result, AuditRecorded: auditRecorded, Warnings: warnings,
	})
}

func (handler *SkillAdminHandler) decodePackage(writer http.ResponseWriter, request *http.Request) (SkillPackageInput, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeSkillHTTPError(writer, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json.")
		return SkillPackageInput{}, false
	}
	limited := http.MaxBytesReader(writer, request.Body, int64(handler.authoringLimits.MaxPackageBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > handler.authoringLimits.MaxPackageBytes {
		writeSkillHTTPError(writer, http.StatusRequestEntityTooLarge, "skill_package_too_large", "Skill package exceeds the configured byte limit.")
		return SkillPackageInput{}, false
	}
	input, err := DecodeSkillPackageInputJSON(data)
	if err != nil {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_package_json", "Request body must be one complete valid skill package.")
		return SkillPackageInput{}, false
	}
	return input, true
}

func (handler *SkillAdminHandler) pathRef(writer http.ResponseWriter, request *http.Request) (SkillRef, bool) {
	ref := SkillRef{Namespace: request.PathValue("namespace"), Name: request.PathValue("name")}
	if !validSkillSlug(ref.Namespace, handler.authoringLimits.MaxNameChars) || !validSkillSlug(ref.Name, handler.authoringLimits.MaxNameChars) {
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_identity", "Namespace and name must be canonical lowercase slugs.")
		return SkillRef{}, false
	}
	return ref, true
}

func (handler *SkillAdminHandler) withSkillStoreSpan(ctx context.Context, operation string, call func(context.Context) error) error {
	startedAt := time.Now()
	ctx, span := handler.telemetry.StartSpan(ctx, "skills.store."+operation)
	telemetry.SetCommonAttrsOn(ctx, span)
	span.SetAttribute("skills.operation", operation)
	err := call(ctx)
	outcome := "success"
	if err != nil {
		outcome = "error"
		span.RecordError(errors.New("skill store operation failed"))
	}
	span.SetAttribute("skills.outcome", outcome)
	span.SetAttribute("skills.duration_ms", time.Since(startedAt).Milliseconds())
	span.End()
	return err
}

func (handler *SkillAdminHandler) recordAuthoringDiagnostics(operation string, result SkillValidationResult) {
	if handler == nil || handler.telemetry == nil {
		return
	}
	for _, diagnostics := range [][]SkillValidationDiagnostic{result.Errors, result.Warnings} {
		for _, diagnostic := range diagnostics {
			handler.telemetry.RecordMetric(skillAuthoringDiagnosticMetric, 1, map[string]string{
				"module": telemetry.ModuleOrchestration, "severity": string(diagnostic.Severity),
				"diagnostic_code": skillMetricDiagnosticCode(diagnostic.Code), "operation": operation,
			})
		}
	}
}

func (handler *SkillAdminHandler) validatePackage(
	ctx context.Context,
	ref SkillRef,
	input SkillPackageInput,
	operation string,
) (ValidatedSkillPackage, SkillValidationResult, error) {
	startedAt := time.Now()
	validated, result, err := handler.validator.Validate(ctx, ref, input)
	recordSkillValidationMetrics(handler.telemetry, "admin", time.Since(startedAt), err)
	handler.recordAuthoringDiagnostics(operation, result)
	return validated, result, err
}

func skillMetricDiagnosticCode(code string) string {
	switch code {
	case "change_reason_limit_exceeded", "change_reason_required", "combined_instructions_too_detailed", "custom_rule_failed",
		"custom_rule_limit_exceeded", "description_activation_unclear", "description_limit_exceeded",
		"description_required", "description_too_detailed", "display_name_required",
		"duplicate_resource_name", "duplicate_resource_scope", "illegal_control_character",
		"invalid_custom_rule_diagnostic", "invalid_domain", "invalid_name", "invalid_namespace",
		"invalid_resource_name", "invalid_resource_scope", "invalid_tag", "invalid_utf8",
		"manifest_byte_limit_exceeded", "manifest_token_limit_exceeded", "manifest_too_detailed",
		"negative_instruction_phrasing",
		"package_byte_limit_exceeded", "package_encoding_failed", "package_too_large",
		"planning_instructions_required", "prohibited_authority_override",
		"prohibited_executable_payload", "prohibited_secret", "reserved_prompt_tag",
		"resource_byte_limit_exceeded", "resource_content_required", "resource_count_high",
		"resource_count_limit_exceeded", "resource_description_required",
		"resource_load_when_ambiguous", "resource_load_when_required",
		"resource_token_limit_exceeded", "resource_too_detailed", "unsupported_resource_content_type":
		return code
	default:
		return "custom_rule_diagnostic"
	}
}

func (handler *SkillAdminHandler) recordMutationAudit(ctx context.Context, event SkillAuditEvent) (bool, []SkillOperationWarning) {
	event.TraceID = telemetry.GetTraceContext(ctx).TraceID
	if err := handler.audit.RecordSkillAudit(ctx, event); err != nil {
		handler.logger.WarnWithContext(ctx, "Skill mutation committed but audit delivery failed", map[string]interface{}{
			"operation": "skills_admin_audit", "request_id": GetRequestID(ctx),
			"error_type": "audit_write", "error": "skill audit delivery failed",
		})
		return false, []SkillOperationWarning{{
			Code: "skill_audit_not_recorded", Message: "The mutation committed, but audit delivery must be repaired.",
		}}
	}
	return true, nil
}

func (handler *SkillAdminHandler) auditActor(ctx context.Context) string {
	if handler.auditAttribution == nil {
		return ""
	}
	actor := strings.TrimSpace(handler.auditAttribution.SkillAuditActor(ctx))
	if actor == "" {
		return ""
	}
	if !validSkillAuditActor(actor) {
		handler.logger.WarnWithContext(ctx, "Skill audit attribution was omitted", map[string]interface{}{
			"operation": "skills_admin_audit_attribution", "request_id": GetRequestID(ctx),
			"status": "omitted", "reason": "invalid_attribution",
			"error_type": "invalid_audit_attribution",
		})
		telemetry.AddSpanEvent(ctx, "skills.audit_attribution.rejected",
			attribute.String("request_id", GetRequestID(ctx)),
			attribute.String("skills.reason", "invalid_attribution"),
		)
		return ""
	}
	return actor
}

func validSkillAuditActor(actor string) bool {
	if actor == "" || len(actor) > maxSkillAuditActorBytes || !utf8.ValidString(actor) {
		return false
	}
	for _, r := range actor {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

func (handler *SkillAdminHandler) writeDomainError(writer http.ResponseWriter, err error, publication bool) {
	switch {
	case errors.Is(err, ErrSkillPrecondition):
		writeSkillHTTPError(writer, http.StatusPreconditionFailed, "skill_precondition_failed", "The supplied representation precondition is stale or invalid.")
	case errors.Is(err, ErrSkillProtectedRevision):
		writeSkillHTTPError(writer, http.StatusConflict, "protected_skill_revisions", "The requested range intersects a protected revision.")
	case publication && errors.Is(err, ErrSkillConflict):
		writeSkillHTTPError(writer, http.StatusPreconditionFailed, "skill_precondition_failed", "The publication precondition failed.")
	case errors.Is(err, ErrSkillConflict):
		writeSkillHTTPError(writer, http.StatusConflict, "skill_conflict", "The skill operation conflicted with current state.")
	case errors.Is(err, ErrSkillNotFound), errors.Is(err, ErrSkillRevisionNotFound):
		writeSkillHTTPError(writer, http.StatusNotFound, "skill_not_found", "The requested skill or revision was not found.")
	case errors.Is(err, ErrSkillLimitExceeded):
		writeSkillHTTPError(writer, http.StatusBadRequest, "skill_limit_exceeded", "The skill operation exceeds a configured limit.")
	case errors.Is(err, ErrInvalidSkillPackage):
		writeSkillHTTPError(writer, http.StatusBadRequest, "invalid_skill_request", "The skill request is invalid.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeSkillHTTPError(writer, http.StatusGatewayTimeout, "skill_request_timeout", "The skill operation did not complete before cancellation.")
	default:
		writeSkillHTTPError(writer, http.StatusServiceUnavailable, "skill_backend_unavailable", "The skill operation is temporarily unavailable.")
	}
}

func skillAdminOperation(request *http.Request) string {
	path := request.URL.Path
	switch {
	case strings.HasSuffix(path, "/validate"):
		return "validate"
	case strings.HasSuffix(path, "/analyze"):
		return "analyze"
	case strings.Contains(path, "/versions") && request.Method == http.MethodDelete:
		return "delete_versions"
	case strings.HasSuffix(path, "/versions"):
		return "list_versions"
	case strings.Contains(path, "/versions/"):
		return "get_version"
	case request.Method == http.MethodPut:
		return "put_published"
	case path == "/api/v1/skills/schema":
		return "schema"
	case path == "/api/v1/skills":
		return "list"
	default:
		return "get_published"
	}
}

func skillAdminHTTPOutcome(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "success"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict, status == http.StatusPreconditionFailed:
		return "conflict"
	default:
		return "error"
	}
}

func formatSkillETag(token string) string {
	if token == "" {
		return ""
	}
	return strconv.Quote(token)
}

func parseSkillETag(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, ",") || strings.HasPrefix(value, "W/") {
		return "", false
	}
	decoded, err := strconv.Unquote(value)
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "", false
	}
	return decoded, true
}

func parsePositiveSkillVersion(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil && parsed > 0
}

func validBoundedSkillText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) && !containsIllegalSkillControl(value)
}

func writeSkillJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeSkillHTTPError(writer http.ResponseWriter, status int, code, message string) {
	writeSkillJSON(writer, status, skillHTTPError{Code: code, Message: message})
}

type skillHTTPStatusWriter struct {
	http.ResponseWriter
	status int
}

func (writer *skillHTTPStatusWriter) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func skillPackageJSONSchema(limits SkillAuthoringLimits) map[string]interface{} {
	return map[string]interface{}{
		"$schema":  "https://json-schema.org/draft/2020-12/schema",
		"$id":      "https://truvag3.dev/schemas/skill-package-v1.json",
		"$comment": "This schema describes the authoring wire shape and JSON-Schema-expressible limits. The deterministic validate endpoint is authoritative for normalization, semantic policy, byte limits, and estimated-token limits.",
		"title":    "TruvaG3 Skill Package", "type": "object", "additionalProperties": false,
		"required": []string{"display_name", "description", "planning_instructions", "change_reason"},
		"properties": map[string]interface{}{
			"display_name":          map[string]interface{}{"type": "string"},
			"description":           map[string]interface{}{"type": "string", "maxLength": limits.MaxDescriptionChars},
			"domains":               map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"tags":                  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"planning_instructions": map[string]interface{}{"type": "array", "minItems": 1, "items": map[string]interface{}{"type": "string"}},
			"response_instructions": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"tool_hints":            map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"resources":             map[string]interface{}{"type": "array", "maxItems": limits.MaxResources, "items": skillResourceJSONSchema()},
			"activation_examples": map[string]interface{}{
				"type": "object", "additionalProperties": false,
				"properties": map[string]interface{}{
					"should_activate":     map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"should_not_activate": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
			},
			"change_reason": map[string]interface{}{"type": "string", "minLength": 1, "maxLength": maxSkillAuditReasonBytes},
		},
	}
}

func skillResourceJSONSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object", "additionalProperties": false,
		"required": []string{"name", "description", "load_when", "content_type", "content"},
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"}, "description": map[string]interface{}{"type": "string"},
			"load_when": map[string]interface{}{"type": "string"},
			"applies_to": map[string]interface{}{"type": "array", "items": map[string]interface{}{
				"type": "string", "enum": []string{"planning", "continuation", "synthesis"},
			}},
			"required_when_selected": map[string]interface{}{"type": "boolean"},
			"content_type":           map[string]interface{}{"type": "string", "enum": []string{"text/plain", "text/markdown"}},
			"content":                map[string]interface{}{"type": "string"},
		},
	}
}
