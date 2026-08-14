package orchestration

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Stable skill-domain error categories. Implementations wrap these values so
// callers can classify failures without inspecting provider-specific text.
var (
	ErrInvalidSkillPackage   = errors.New("invalid skill package")
	ErrSkillNotFound         = errors.New("skill not found")
	ErrSkillRevisionNotFound = errors.New("skill revision not found")
	ErrSkillIntegrity        = errors.New("skill integrity verification failed")
	ErrSkillUnavailable      = errors.New("skill unavailable")
	ErrSkillLimitExceeded    = errors.New("skill limit exceeded")
	ErrSkillConflict         = errors.New("skill publication conflict")
	// ErrSkillPrecondition and ErrSkillProtectedRevision retain the broad
	// ErrSkillConflict category while allowing provider-neutral HTTP mapping.
	ErrSkillPrecondition      = fmt.Errorf("skill precondition failed: %w", ErrSkillConflict)
	ErrSkillProtectedRevision = fmt.Errorf("protected skill revision: %w", ErrSkillConflict)
)

// SkillRef is the canonical provider-neutral identity of a skill. Namespace
// and Name are validated and normalized before a value becomes authoritative.
// Domains and tags never participate in identity.
type SkillRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// String returns the canonical namespace/name representation. It does not
// validate or normalize a value; construction boundaries retain that duty.
func (ref SkillRef) String() string {
	return ref.Namespace + "/" + ref.Name
}

// SkillVersionRef identifies one immutable published manifest.
type SkillVersionRef struct {
	Ref          SkillRef `json:"ref"`
	Version      uint64   `json:"version"`
	ManifestHash string   `json:"manifest_hash"`
}

// SkillResourceRef identifies one immutable resource listed by an immutable
// skill manifest. ExpectedHash is copied from that manifest's resource index.
type SkillResourceRef struct {
	Skill        SkillVersionRef `json:"skill"`
	Name         string          `json:"name"`
	ExpectedHash string          `json:"expected_hash"`
}

// SkillActivation controls when an available binding becomes active.
type SkillActivation string

const (
	SkillActivationAlways   SkillActivation = "always"
	SkillActivationAuto     SkillActivation = "auto"
	SkillActivationExplicit SkillActivation = "explicit"
)

// SkillBinding is the normalized developer-owned eligibility policy for one
// canonical skill. Version is "published" or a positive decimal revision.
// Required controls availability; it never forces activation.
type SkillBinding struct {
	Namespace  string          `json:"namespace"`
	Name       string          `json:"name"`
	Version    string          `json:"version,omitempty"`
	Activation SkillActivation `json:"activation"`
	Required   bool            `json:"required,omitempty"`
}

// Ref returns the binding's canonical identity fields without validation.
func (binding SkillBinding) Ref() SkillRef {
	return SkillRef{Namespace: binding.Namespace, Name: binding.Name}
}

// SkillBindingSource records which complete binding list became effective.
type SkillBindingSource string

const (
	SkillBindingsFromCode        SkillBindingSource = "code"
	SkillBindingsFromEnvironment SkillBindingSource = "environment"
	SkillBindingsFromCheckpoint  SkillBindingSource = "checkpoint"
)

// SkillDomainCompatibilityMode controls only the comparison between an
// explicitly bound candidate's domains and the agent domain. It never performs
// discovery or substitution.
type SkillDomainCompatibilityMode string

const (
	SkillDomainCompatibilityOff     SkillDomainCompatibilityMode = "off"
	SkillDomainCompatibilityWarn    SkillDomainCompatibilityMode = "warn"
	SkillDomainCompatibilityEnforce SkillDomainCompatibilityMode = "enforce"
)

// SkillContentCacheMode selects the included local immutable-body cache or
// verified direct registry reads.
type SkillContentCacheMode string

const (
	SkillContentCacheLocal    SkillContentCacheMode = "local"
	SkillContentCacheDisabled SkillContentCacheMode = "disabled"
)

// SkillRuntimeLimits are effective execution-time count, token, and registry
// read ceilings. Values are provider-neutral and validated as a set.
type SkillRuntimeLimits struct {
	MaxBindings                int           `json:"max_bindings"`
	MaxAutoCandidates          int           `json:"max_auto_candidates"`
	CatalogTokenBudget         int           `json:"catalog_token_budget"`
	MaxResourceCandidates      int           `json:"max_resource_candidates"`
	ResourceCatalogTokenBudget int           `json:"resource_catalog_token_budget"`
	MaxActiveSkills            int           `json:"max_active_skills"`
	TotalTokenBudget           int           `json:"total_token_budget"`
	MainTokenBudget            int           `json:"main_token_budget"`
	ResourceTokenBudget        int           `json:"resource_token_budget"`
	MaxResourcesPerPhase       int           `json:"max_resources_per_phase"`
	MaxResourcesPerExecution   int           `json:"max_resources_per_execution"`
	ResolutionMaxTokens        int           `json:"resolution_max_tokens"`
	RegistryReadTimeout        time.Duration `json:"registry_read_timeout"`
	SynthesisTokenBudget       int           `json:"synthesis_token_budget"`
	EffectiveInputTokenBudget  int           `json:"effective_input_token_budget,omitempty"`
}

// SkillContentCacheConfig configures only process-local immutable-body cache
// storage. It cannot weaken integrity verification or alias resolution.
type SkillContentCacheConfig struct {
	Mode     SkillContentCacheMode `json:"mode"`
	MaxBytes int                   `json:"max_bytes"`
}

// SkillConfig is the complete developer-owned eligibility and runtime policy
// configuration. Bindings are never merged with registry or runtime state.
type SkillConfig struct {
	Enabled                 bool                         `json:"enabled"`
	Bindings                []SkillBinding               `json:"bindings,omitempty"`
	DomainCompatibilityMode SkillDomainCompatibilityMode `json:"domain_compatibility_mode"`
	Cache                   SkillContentCacheConfig      `json:"cache"`
	Limits                  SkillRuntimeLimits           `json:"limits"`
	RuntimePolicyID         string                       `json:"runtime_policy_id,omitempty"`
	bindingSource           SkillBindingSource           `json:"-"`
}

// SkillPromptGuidance contains bounded additive selector guidance. It is not a
// complete prompt replacement and raw file paths are not retained here.
type SkillPromptGuidance struct {
	Activation string `json:"-"`
	Resource   string `json:"-"`
}

// SkillResourceScope is an authored, typed applicability guard for a resource.
type SkillResourceScope string

const (
	SkillResourcePlanning     SkillResourceScope = "planning"
	SkillResourceContinuation SkillResourceScope = "continuation"
	SkillResourceSynthesis    SkillResourceScope = "synthesis"
)

// SkillPromptBoundary is a runtime projection/debug lifecycle boundary. It is
// deliberately distinct from the authored resource applicability enum.
type SkillPromptBoundary string

const (
	SkillBoundaryInitialPlanning SkillPromptBoundary = "initial_planning"
	SkillBoundaryContinuation    SkillPromptBoundary = "continuation"
	SkillBoundaryRegeneration    SkillPromptBoundary = "regeneration"
	SkillBoundarySynthesis       SkillPromptBoundary = "synthesis"
	SkillBoundaryResume          SkillPromptBoundary = "resume"
)

// SkillActivationExamples are evaluation inputs. They are retained with the
// immutable revision but never included in ordinary runtime prompts.
type SkillActivationExamples struct {
	ShouldActivate    []string `json:"should_activate,omitempty"`
	ShouldNotActivate []string `json:"should_not_activate,omitempty"`
}

// SkillResourceInput is the text-only authoring representation of one
// independently loadable resource. Hashes and versions are server assigned.
type SkillResourceInput struct {
	Name                 string               `json:"name"`
	Description          string               `json:"description"`
	LoadWhen             string               `json:"load_when"`
	AppliesTo            []SkillResourceScope `json:"applies_to,omitempty"`
	RequiredWhenSelected bool                 `json:"required_when_selected,omitempty"`
	ContentType          string               `json:"content_type"`
	Content              string               `json:"content"`
}

// SkillPackageInput is the complete provider-neutral authoring payload. The
// HTTP path supplies namespace/name and the server assigns the revision.
type SkillPackageInput struct {
	DisplayName          string                  `json:"display_name"`
	Description          string                  `json:"description"`
	Domains              []string                `json:"domains,omitempty"`
	Tags                 []string                `json:"tags,omitempty"`
	PlanningInstructions []string                `json:"planning_instructions"`
	ResponseInstructions []string                `json:"response_instructions,omitempty"`
	ToolHints            []string                `json:"tool_hints,omitempty"`
	Resources            []SkillResourceInput    `json:"resources,omitempty"`
	ActivationExamples   SkillActivationExamples `json:"activation_examples,omitempty"`
	ChangeReason         string                  `json:"change_reason"`
}

// ValidatedSkillPackage can be produced only after deterministic framework
// normalization and validation. The package still contains authoring bodies;
// it is a control-plane value and must never enter runtime snapshots or debug.
type ValidatedSkillPackage struct {
	Package SkillPackageInput
}

// SkillResourceMetadata is the body-free manifest index for one resource.
type SkillResourceMetadata struct {
	Name                 string               `json:"name"`
	Description          string               `json:"description"`
	LoadWhen             string               `json:"load_when"`
	AppliesTo            []SkillResourceScope `json:"applies_to,omitempty"`
	RequiredWhenSelected bool                 `json:"required_when_selected,omitempty"`
	ContentType          string               `json:"content_type"`
	ResourceHash         string               `json:"resource_hash"`
}

// SkillMetadata is the bounded catalog representation used by list and
// candidate-selection operations. It contains no instructions or resources.
type SkillPublicationStatus string

const (
	SkillPublicationPublished SkillPublicationStatus = "published"
)

type SkillMetadata struct {
	Ref              SkillRef               `json:"ref"`
	DisplayName      string                 `json:"display_name"`
	Description      string                 `json:"description"`
	Domains          []string               `json:"domains,omitempty"`
	Tags             []string               `json:"tags,omitempty"`
	PublishedVersion uint64                 `json:"published_version"`
	Status           SkillPublicationStatus `json:"status"`
}

// SkillManifest is the immutable body-bearing main record loaded only after
// activation. Resource bodies remain separate.
type SkillManifest struct {
	Ref                  SkillVersionRef         `json:"ref"`
	DisplayName          string                  `json:"display_name"`
	Description          string                  `json:"description"`
	Domains              []string                `json:"domains,omitempty"`
	Tags                 []string                `json:"tags,omitempty"`
	PlanningInstructions []string                `json:"planning_instructions"`
	ResponseInstructions []string                `json:"response_instructions,omitempty"`
	ToolHints            []string                `json:"tool_hints,omitempty"`
	Resources            []SkillResourceMetadata `json:"resources,omitempty"`
}

// SkillResource is one immutable separately loadable resource body.
type SkillResource struct {
	Ref         SkillResourceRef `json:"ref"`
	ContentType string           `json:"content_type"`
	Content     string           `json:"content"`
}

// SkillCandidateStatus is the closed per-binding result classification from
// authoritative batch resolution.
type SkillCandidateStatus string

const (
	SkillCandidateResolved       SkillCandidateStatus = "resolved"
	SkillCandidateNotFound       SkillCandidateStatus = "not_found"
	SkillCandidateDeleted        SkillCandidateStatus = "deleted"
	SkillCandidateInvalidVersion SkillCandidateStatus = "invalid_version"
	SkillCandidateUnavailable    SkillCandidateStatus = "unavailable"
)

// SkillCandidateRequest resolves "published" or one exact immutable revision.
type SkillCandidateRequest struct {
	Ref              SkillRef `json:"ref"`
	RequestedVersion string   `json:"requested_version"`
}

// SkillCandidate is a body-free result for exactly one candidate request.
type SkillCandidate struct {
	Ref              SkillRef             `json:"ref"`
	RequestedVersion string               `json:"requested_version"`
	Resolved         SkillVersionRef      `json:"resolved,omitempty"`
	Metadata         SkillMetadata        `json:"metadata,omitempty"`
	Status           SkillCandidateStatus `json:"status"`
}

// SkillDomainCompatibilityOutcome is the body-free result of comparing one
// explicitly bound resolved candidate with the configured agent domain.
type SkillDomainCompatibilityOutcome struct {
	Ref     SkillRef `json:"ref"`
	Outcome string   `json:"outcome"`
}

// SkillMetadataFilter contains independent catalog filters. Empty fields mean
// no restriction; implementations must still apply their bounded page limit.
type SkillMetadataFilter struct {
	Namespace string `json:"namespace,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Tag       string `json:"tag,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// SkillCatalogSummary is the exact-version, body-free selector view. It is
// derived from one resolved candidate and never carries instructions or
// resource bodies.
type SkillCatalogSummary struct {
	Ref         SkillVersionRef `json:"ref"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	Domains     []string        `json:"domains,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
}

// SkillResourceCatalogEntry keys one body-free resource index entry to the
// active immutable skill revision from which it was loaded.
type SkillResourceCatalogEntry struct {
	Skill    SkillVersionRef       `json:"skill"`
	Resource SkillResourceMetadata `json:"resource"`
}

// SkillRevisionStatus is the bounded history state returned by control-plane
// version listing. It is deliberately separate from runtime publication
// status: retained and deleted revisions are never alternate operational
// states and cannot become published through this value.
type SkillRevisionStatus string

const (
	SkillRevisionRetained SkillRevisionStatus = "retained"
	SkillRevisionDeleted  SkillRevisionStatus = "deleted"
)

// SkillRevisionSummary is a body-free immutable-history entry. Deleted
// revisions retain only bounded tombstone evidence; authoring and resource
// bodies are never present.
type SkillRevisionSummary struct {
	Ref       SkillVersionRef     `json:"ref"`
	Status    SkillRevisionStatus `json:"status"`
	DeletedAt *time.Time          `json:"deleted_at,omitempty"`
	Reason    string              `json:"reason,omitempty"`
	Actor     string              `json:"actor,omitempty"`
}

// SkillVersionListOptions requests a bounded descending immutable-history
// page. BeforeVersion is exclusive; zero starts at the newest revision.
type SkillVersionListOptions struct {
	BeforeVersion uint64 `json:"before_version,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// SkillVersionPage contains body-free immutable revision summaries.
// NextBeforeVersion is zero when no later page exists.
type SkillVersionPage struct {
	Versions          []SkillRevisionSummary `json:"versions"`
	NextBeforeVersion uint64                 `json:"next_before_version,omitempty"`
}

// PublishedSkillRevision is the immutable public revision summary. The opaque
// RevisionToken is transported as an HTTP ETag and never appears in JSON.
type PublishedSkillRevision struct {
	Ref           SkillVersionRef `json:"ref"`
	Metadata      SkillMetadata   `json:"metadata"`
	RevisionToken string          `json:"-"`
}

// SkillRevisionRepresentation is the complete normalized, resubmittable
// control-plane representation. It must never be used for candidate resolution.
type SkillRevisionRepresentation struct {
	Revision PublishedSkillRevision `json:"revision"`
	Package  SkillPackageInput      `json:"package"`
	Manifest SkillManifest          `json:"manifest"`
}

// PutPublishedSkillInput is the complete provider-neutral atomic publication
// command. Package has already passed mandatory deterministic validation.
// RequireAbsent represents first-create intent; ExpectedRevisionToken is the
// opaque update precondition. At most one is set.
type PutPublishedSkillInput struct {
	Ref                   SkillRef              `json:"ref"`
	Package               ValidatedSkillPackage `json:"package"`
	RequireAbsent         bool                  `json:"require_absent,omitempty"`
	ExpectedRevisionToken string                `json:"-"`
	IdempotencyKey        string                `json:"-"`
}

// PutPublishedSkillResult contains only the atomic facts produced by the
// administration store. Validation warnings and audit-delivery disposition
// belong to the hosting service, not this result.
type PutPublishedSkillResult struct {
	Outcome  SkillAuditOutcome      `json:"outcome"`
	Previous *SkillVersionRef       `json:"previous,omitempty"`
	Current  PublishedSkillRevision `json:"current"`
}

// DeleteSkillVersionsInput describes one normalized inclusive range. A single
// revision is represented by equal FromVersion and ToVersion values.
type DeleteSkillVersionsInput struct {
	Ref                   SkillRef `json:"ref"`
	FromVersion           uint64   `json:"from_version"`
	ToVersion             uint64   `json:"to_version"`
	ExpectedRevisionToken string   `json:"-"`
	Reason                string   `json:"reason"`
	Actor                 string   `json:"actor,omitempty"`
}

// DeleteSkillVersionsResult contains the complete atomic deletion facts used
// to construct body-free audit evidence. Published remains unchanged.
type DeleteSkillVersionsResult struct {
	Outcome                SkillAuditOutcome `json:"outcome"`
	Ref                    SkillRef          `json:"ref"`
	PreviousPublished      SkillVersionRef   `json:"previous_published"`
	CurrentPublished       SkillVersionRef   `json:"current_published"`
	DeletedVersions        []uint64          `json:"deleted_versions"`
	AlreadyDeletedVersions []uint64          `json:"already_deleted_versions"`
}

// SkillValidationSeverity is a closed validation-diagnostic classification.
type SkillValidationSeverity string

const (
	SkillValidationError   SkillValidationSeverity = "error"
	SkillValidationWarning SkillValidationSeverity = "warning"
)

// SkillValidationDiagnostic is a bounded machine-readable authoring finding.
// Path is a JSON Pointer into SkillPackageInput.
type SkillValidationDiagnostic struct {
	Code     string                  `json:"code"`
	Path     string                  `json:"path"`
	Message  string                  `json:"message"`
	Severity SkillValidationSeverity `json:"severity"`
}

// SkillTokenEstimator identifies the deterministic estimator used for
// authoring and admission evidence.
type SkillTokenEstimator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SkillValidationMetrics reports deterministic normalized authoring size.
type SkillValidationMetrics struct {
	ManifestBytes  int                 `json:"manifest_bytes"`
	ResourceBytes  int                 `json:"resource_bytes"`
	PackageBytes   int                 `json:"package_bytes"`
	ManifestTokens int                 `json:"manifest_tokens"`
	ResourceTokens int                 `json:"resource_tokens"`
	ResourceCount  int                 `json:"resource_count"`
	TokenEstimator SkillTokenEstimator `json:"token_estimator"`
}

// SkillValidationResult separates hard errors from advisory warnings.
type SkillValidationResult struct {
	Valid    bool                        `json:"valid"`
	Errors   []SkillValidationDiagnostic `json:"errors"`
	Warnings []SkillValidationDiagnostic `json:"warnings"`
	Metrics  SkillValidationMetrics      `json:"metrics"`
}

// SkillAuthoringAnalysisInput is the body-bearing control-plane input passed
// only to an optional non-mutating authoring advisor.
type SkillAuthoringAnalysisInput struct {
	Ref        SkillRef              `json:"ref"`
	Package    SkillPackageInput     `json:"package"`
	Validation SkillValidationResult `json:"validation"`
}

// SkillAuthoringFinding is one bounded advisory finding. It cannot change the
// authoritative deterministic validation result.
type SkillAuthoringFinding struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// SkillJSONPatchOperation is the bounded JSON Patch subset returned by the
// optional authoring advisor. The advisor implementation validates operations,
// paths, and aggregate output bounds before returning them. Advice remains
// non-mutating; accepted values are revalidated through ordinary publication.
type SkillJSONPatchOperation struct {
	Operation string      `json:"op"`
	Path      string      `json:"path"`
	Value     interface{} `json:"value,omitempty"`
}

// SkillAuthoringAdvice is advisory and non-mutating. ProposedPatch must be
// explicitly accepted and resubmitted through ordinary validation.
type SkillAuthoringAdvice struct {
	Summary       string                    `json:"summary"`
	Findings      []SkillAuthoringFinding   `json:"findings"`
	ProposedPatch []SkillJSONPatchOperation `json:"proposed_patch,omitempty"`
}

// SkillAuditAction is a closed administrative mutation classification.
type SkillAuditAction string

const (
	SkillAuditPutPublished   SkillAuditAction = "put_published"
	SkillAuditDeleteVersions SkillAuditAction = "delete_versions"
)

// SkillAuditOutcome is a closed mutation outcome shared by store results and
// body-free audit evidence.
type SkillAuditOutcome string

const (
	SkillAuditCreated          SkillAuditOutcome = "created"
	SkillAuditUpdated          SkillAuditOutcome = "updated"
	SkillAuditSameContentNoOp  SkillAuditOutcome = "same_content_noop"
	SkillAuditIdempotentReplay SkillAuditOutcome = "idempotent_replay"
	SkillAuditDeleted          SkillAuditOutcome = "deleted"
	SkillAuditDeleteNoOp       SkillAuditOutcome = "delete_noop"
)

// SkillAuditEvent is bounded mutation evidence. It never contains an
// authoring payload, body, ETag, idempotency key, provider key, or credential.
type SkillAuditEvent struct {
	EventID                string            `json:"event_id"`
	RequestID              string            `json:"request_id"`
	TraceID                string            `json:"trace_id,omitempty"`
	OccurredAt             time.Time         `json:"occurred_at"`
	Action                 SkillAuditAction  `json:"action"`
	Outcome                SkillAuditOutcome `json:"outcome"`
	Ref                    SkillRef          `json:"ref"`
	Previous               *SkillVersionRef  `json:"previous,omitempty"`
	Current                *SkillVersionRef  `json:"current,omitempty"`
	DeletedVersions        []uint64          `json:"deleted_versions,omitempty"`
	AlreadyDeletedVersions []uint64          `json:"already_deleted_versions,omitempty"`
	Actor                  string            `json:"actor,omitempty"`
	Reason                 string            `json:"reason"`
}

// SkillCacheContext is the feature-owned request cache projection. Only its
// opaque Fingerprint crosses the generic core pipeline cache-vary boundary.
type SkillCacheContext struct {
	Fingerprint           string `json:"fingerprint"`
	ResponseCacheEligible bool   `json:"response_cache_eligible"`
}

// SkillResourceRequest is a trusted, body-free host request for one authored
// resource. Runtime validation still requires an effective binding and an
// active pinned manifest entry.
type SkillResourceRequest struct {
	Skill SkillRef `json:"skill"`
	Name  string   `json:"name"`
}

// SkillDecisionSource records the bounded mechanism responsible for an
// activation or resource-selection decision.
type SkillDecisionSource string

const (
	SkillDecisionAlways       SkillDecisionSource = "always"
	SkillDecisionTrusted      SkillDecisionSource = "trusted"
	SkillDecisionCustomPolicy SkillDecisionSource = "custom_policy"
	SkillDecisionDefaultAI    SkillDecisionSource = "default_ai"
)

// SkillSelectionReason is bounded explanatory text retained only in runtime
// state and debug evidence. It is never a metric label.
type SkillSelectionReason string

// SkillDiagnostic is body-free execution evidence for one bounded runtime
// condition. Detail contains sanitized, bounded framework text only.
type SkillDiagnostic struct {
	Code        string              `json:"code"`
	Boundary    SkillPromptBoundary `json:"boundary,omitempty"`
	PhaseNumber int                 `json:"phase_number,omitempty"`
	Skill       *SkillRef           `json:"skill,omitempty"`
	Resource    string              `json:"resource,omitempty"`
	Action      string              `json:"action,omitempty"`
	Detail      string              `json:"detail,omitempty"`
}

// SkillDebugProvenance captures immutable request-start configuration and
// runtime policy identity without content bodies.
type SkillDebugProvenance struct {
	BindingSource      SkillBindingSource      `json:"binding_source"`
	BindingFingerprint string                  `json:"binding_fingerprint"`
	BudgetFingerprint  string                  `json:"budget_fingerprint"`
	RuntimePolicy      SkillRuntimePolicyDebug `json:"runtime_policy"`
}

// SkillSnapshot is the immutable, body-free request-start candidate set.
type SkillSnapshot struct {
	EffectiveBindings          []SkillBinding                    `json:"effective_bindings"`
	Candidates                 []SkillCandidate                  `json:"candidates"`
	TrustedExplicitActivations []SkillRef                        `json:"trusted_explicit_activations,omitempty"`
	TrustedResourceRequests    []SkillResourceRequest            `json:"trusted_resource_requests,omitempty"`
	ExpectedCapabilities       []string                          `json:"expected_capabilities,omitempty"`
	DomainOutcomes             []SkillDomainCompatibilityOutcome `json:"domain_outcomes,omitempty"`
	CacheFingerprint           string                            `json:"cache_fingerprint"`
	DebugProvenance            SkillDebugProvenance              `json:"debug_provenance"`
}

// ActiveSkill is one immutable activation record. It contains exact identity,
// policy, source, and reason but no manifest body.
type ActiveSkill struct {
	Binding  SkillBinding         `json:"binding"`
	Skill    SkillVersionRef      `json:"skill"`
	Selector SkillDecisionSource  `json:"selector"`
	Reason   SkillSelectionReason `json:"reason"`
}

// SkillResourceSelection is one body-free resource-selection history entry.
type SkillResourceSelection struct {
	Resource             SkillResourceRef     `json:"resource"`
	Boundary             SkillPromptBoundary  `json:"boundary"`
	PhaseNumber          int                  `json:"phase_number"`
	Selector             SkillDecisionSource  `json:"selector"`
	Reason               SkillSelectionReason `json:"reason"`
	RequiredWhenSelected bool                 `json:"required_when_selected"`
}

// SkillExecutionState evolves copy-on-write across orchestration boundaries.
// Pinned is immutable after request-start construction and every field remains
// body-free.
type SkillExecutionState struct {
	Pinned             *SkillSnapshot           `json:"pinned,omitempty"`
	ActiveSkills       []ActiveSkill            `json:"active_skills,omitempty"`
	UnavailableContent []SkillVersionRef        `json:"unavailable_content,omitempty"`
	ResourceSelections []SkillResourceSelection `json:"resource_selections,omitempty"`
	Diagnostics        []SkillDiagnostic        `json:"diagnostics,omitempty"`
	Debug              SkillExecutionDebug      `json:"debug"`
}

// SkillRuntimePolicyDebug records configuration and framework-policy
// provenance. These values are debug fields, never high-cardinality labels.
type SkillRuntimePolicyDebug struct {
	ActivationSelectorPolicyVersion string `json:"activation_selector_policy_version"`
	ResourceSelectorPolicyVersion   string `json:"resource_selector_policy_version"`
	TokenCounterPolicyVersion       string `json:"token_counter_policy_version"`
	CapabilityHintPolicyVersion     string `json:"capability_hint_policy_version"`
	BudgetPolicyVersion             string `json:"budget_policy_version"`
	ProjectionCompilerVersion       string `json:"projection_compiler_version"`
	InputEncoderVersion             string `json:"input_encoder_version"`
	RuntimePolicyID                 string `json:"runtime_policy_id,omitempty"`
	ActivationGuidanceFingerprint   string `json:"activation_guidance_fingerprint"`
	ResourceGuidanceFingerprint     string `json:"resource_guidance_fingerprint"`
	DomainCompatibilityMode         string `json:"domain_compatibility_mode"`
	ResponseCacheEligible           bool   `json:"response_cache_eligible"`
}

// SkillCandidateDebug records one normalized binding and its authoritative
// request-start resolution outcome. DisplayName and Description are the
// already-bounded catalog metadata captured with the pinned revision so
// historical debug views never join against mutable current metadata.
type SkillCandidateDebug struct {
	Sequence         int                  `json:"sequence"`
	Ref              SkillRef             `json:"ref"`
	DisplayName      string               `json:"display_name,omitempty"`
	Description      string               `json:"description,omitempty"`
	RequestedVersion string               `json:"requested_version"`
	Activation       SkillActivation      `json:"activation"`
	Required         bool                 `json:"required"`
	Status           SkillCandidateStatus `json:"status"`
	Resolved         *SkillVersionRef     `json:"resolved,omitempty"`
}

// SkillActivationDebug records one body-free boundary activation decision.
type SkillActivationDebug struct {
	Sequence    int                  `json:"sequence"`
	Boundary    SkillPromptBoundary  `json:"boundary"`
	PhaseNumber int                  `json:"phase_number"`
	Skill       SkillVersionRef      `json:"skill"`
	Activation  SkillActivation      `json:"activation"`
	Required    bool                 `json:"required"`
	Selector    SkillDecisionSource  `json:"selector"`
	Decision    string               `json:"decision"`
	Admission   string               `json:"admission"`
	Reason      SkillSelectionReason `json:"reason"`
}

// SkillResourceSelectionDebug records eligibility, selection, and admission
// for one body-free exact resource reference.
type SkillResourceSelectionDebug struct {
	Sequence             int                  `json:"sequence"`
	Boundary             SkillPromptBoundary  `json:"boundary"`
	PhaseNumber          int                  `json:"phase_number"`
	Resource             SkillResourceRef     `json:"resource"`
	Eligibility          string               `json:"eligibility"`
	Selector             SkillDecisionSource  `json:"selector"`
	Decision             string               `json:"decision"`
	Admission            string               `json:"admission"`
	RequiredWhenSelected bool                 `json:"required_when_selected"`
	Reason               SkillSelectionReason `json:"reason"`
}

// SkillContentLoadDebug records integrity/cache/load disposition without
// retaining the loaded instruction or resource body.
type SkillContentLoadDebug struct {
	Sequence       int                 `json:"sequence"`
	Boundary       SkillPromptBoundary `json:"boundary"`
	PhaseNumber    int                 `json:"phase_number"`
	ContentKind    string              `json:"content_kind"`
	Skill          SkillVersionRef     `json:"skill"`
	ResourceName   string              `json:"resource_name,omitempty"`
	ExpectedHash   string              `json:"expected_hash"`
	ObservedHash   string              `json:"observed_hash,omitempty"`
	CacheOutcome   string              `json:"cache_outcome"`
	Source         string              `json:"source"`
	Attempt        int                 `json:"attempt"`
	Outcome        string              `json:"outcome"`
	RetryOutcome   string              `json:"retry_outcome,omitempty"`
	DiagnosticCode string              `json:"diagnostic_code,omitempty"`
	ByteEstimate   int                 `json:"byte_estimate,omitempty"`
	TokenEstimate  int                 `json:"token_estimate,omitempty"`
	DurationMs     int64               `json:"duration_ms"`
}

// SkillProjectionDebug records the bounded body-free composition evidence for
// one model-facing prompt boundary.
type SkillProjectionDebug struct {
	Sequence              int                 `json:"sequence"`
	Boundary              SkillPromptBoundary `json:"boundary"`
	PhaseNumber           int                 `json:"phase_number"`
	PromptKind            string              `json:"prompt_kind"`
	SkillRefs             []SkillVersionRef   `json:"skill_refs,omitempty"`
	ResourceRefs          []SkillResourceRef  `json:"resource_refs,omitempty"`
	MainInstructionTokens int                 `json:"main_instruction_tokens"`
	ResourceTokens        int                 `json:"resource_tokens"`
	TotalTokens           int                 `json:"total_tokens"`
	PolicyVersion         string              `json:"policy_version"`
	CompilerVersion       string              `json:"compiler_version"`
	Outcome               string              `json:"outcome"`
}

// SkillExecutionDebug is the authoritative body-free reconstruction record
// embedded in StoredExecution. It is bounded by runtime configuration.
type SkillExecutionDebug struct {
	BindingSource      SkillBindingSource            `json:"binding_source"`
	BindingFingerprint string                        `json:"binding_fingerprint"`
	BudgetFingerprint  string                        `json:"budget_fingerprint"`
	CacheFingerprint   string                        `json:"cache_fingerprint"`
	RuntimePolicy      SkillRuntimePolicyDebug       `json:"runtime_policy"`
	Candidates         []SkillCandidateDebug         `json:"candidates,omitempty"`
	Activations        []SkillActivationDebug        `json:"activations,omitempty"`
	ResourceSelections []SkillResourceSelectionDebug `json:"resource_selections,omitempty"`
	ContentLoads       []SkillContentLoadDebug       `json:"content_loads,omitempty"`
	Projections        []SkillProjectionDebug        `json:"projections,omitempty"`
	Diagnostics        []SkillDiagnostic             `json:"diagnostics,omitempty"`
}

// SkillDomainError retains a stable skill identity and operation while
// preserving an errors.Is-compatible category. It deliberately carries no
// provider error text, content body, or other arbitrary detail string.
type SkillDomainError struct {
	Category  error
	Operation string
	Ref       SkillRef
}

func (err *SkillDomainError) Error() string {
	if err == nil {
		return ""
	}
	message := "skill operation failed"
	if err.Operation != "" {
		message = "skill " + err.Operation + " failed"
	}
	if err.Ref.Namespace != "" || err.Ref.Name != "" {
		message += " for " + err.Ref.String()
	}
	return message
}

func (err *SkillDomainError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Category
}

func newSkillDomainError(category error, operation string, ref SkillRef) error {
	if category == nil {
		category = ErrSkillUnavailable
	}
	return &SkillDomainError{
		Category: category, Operation: operation, Ref: ref,
	}
}

// newRequiredSkillContentError preserves integrity classification while also
// exposing the required-content availability failure expected by callers.
func newRequiredSkillContentError(loadErr error, operation string, ref SkillRef) error {
	category := error(ErrSkillUnavailable)
	if errors.Is(loadErr, ErrSkillIntegrity) {
		category = errors.Join(ErrSkillUnavailable, ErrSkillIntegrity)
	}
	return newSkillDomainError(category, operation, ref)
}

func formatSkillVersion(ref SkillVersionRef) string {
	return fmt.Sprintf("%s@%d", ref.Ref.String(), ref.Version)
}

func (value *SkillActivation) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "activation",
		SkillActivationAlways, SkillActivationAuto, SkillActivationExplicit)
}

func (value *SkillBindingSource) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "binding source",
		SkillBindingsFromCode, SkillBindingsFromEnvironment, SkillBindingsFromCheckpoint)
}

func (value *SkillResourceScope) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "resource scope",
		SkillResourcePlanning, SkillResourceContinuation, SkillResourceSynthesis)
}

func (value *SkillDomainCompatibilityMode) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "domain compatibility mode",
		SkillDomainCompatibilityOff, SkillDomainCompatibilityWarn, SkillDomainCompatibilityEnforce)
}

func (value *SkillContentCacheMode) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "skill content cache mode",
		SkillContentCacheLocal, SkillContentCacheDisabled)
}

func (value *SkillPromptBoundary) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "prompt boundary",
		SkillBoundaryInitialPlanning, SkillBoundaryContinuation, SkillBoundaryRegeneration,
		SkillBoundarySynthesis, SkillBoundaryResume)
}

func (value *SkillPublicationStatus) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "publication status", SkillPublicationPublished)
}

func (value *SkillCandidateStatus) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "candidate status",
		SkillCandidateResolved, SkillCandidateNotFound, SkillCandidateDeleted,
		SkillCandidateInvalidVersion, SkillCandidateUnavailable)
}

func (value *SkillRevisionStatus) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "revision status",
		SkillRevisionRetained, SkillRevisionDeleted)
}

func (value *SkillValidationSeverity) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "validation severity",
		SkillValidationError, SkillValidationWarning)
}

func (value *SkillAuditAction) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "audit action",
		SkillAuditPutPublished, SkillAuditDeleteVersions)
}

func (value *SkillAuditOutcome) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "audit outcome",
		SkillAuditCreated, SkillAuditUpdated, SkillAuditSameContentNoOp,
		SkillAuditIdempotentReplay, SkillAuditDeleted, SkillAuditDeleteNoOp)
}

func (value *SkillDecisionSource) UnmarshalJSON(data []byte) error {
	return unmarshalClosedSkillEnum(data, value, "decision source",
		SkillDecisionAlways, SkillDecisionTrusted, SkillDecisionCustomPolicy,
		SkillDecisionDefaultAI)
}

func unmarshalClosedSkillEnum[T ~string](
	data []byte,
	target *T,
	name string,
	allowed ...T,
) error {
	if target == nil {
		return fmt.Errorf("%w: %s target is nil", ErrInvalidSkillPackage, name)
	}
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: %s must be a string", ErrInvalidSkillPackage, name)
	}
	candidate := T(decoded)
	if candidate == "" {
		*target = candidate
		return nil
	}
	for _, expected := range allowed {
		if candidate == expected {
			*target = candidate
			return nil
		}
	}
	return fmt.Errorf("%w: invalid %s", ErrInvalidSkillPackage, name)
}
