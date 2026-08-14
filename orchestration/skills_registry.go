package orchestration

import "context"

// SkillRegistry is the provider-neutral runtime read contract. Candidate
// resolution is intentionally batched; manifest and resource reads are exact
// immutable loads.
type SkillRegistry interface {
	ListMetadata(context.Context, SkillMetadataFilter) ([]SkillMetadata, error)
	ResolveCandidates(context.Context, []SkillCandidateRequest) ([]SkillCandidate, error)
	GetManifest(context.Context, SkillVersionRef) (SkillManifest, error)
	GetResource(context.Context, SkillResourceRef) (SkillResource, error)
}

// SkillRevisionReader is an optional control-plane read contract. It is never
// used by ordinary agent execution.
type SkillRevisionReader interface {
	GetPublished(context.Context, SkillRef) (SkillRevisionRepresentation, error)
	GetVersion(context.Context, SkillRef, uint64) (SkillRevisionRepresentation, error)
	ListVersions(context.Context, SkillRef, SkillVersionListOptions) (SkillVersionPage, error)
}

// SkillAuthoringAdvisor is optional, bounded, advisory, and non-mutating.
type SkillAuthoringAdvisor interface {
	Analyze(context.Context, SkillAuthoringAnalysisInput) (SkillAuthoringAdvice, error)
}

// SkillAdministrationStore performs one provider-atomic publication command.
type SkillAdministrationStore interface {
	PutPublished(context.Context, PutPublishedSkillInput) (PutPublishedSkillResult, error)
}

// SkillRevisionDeletionStore performs one provider-atomic guarded deletion.
type SkillRevisionDeletionStore interface {
	DeleteVersions(context.Context, DeleteSkillVersionsInput) (DeleteSkillVersionsResult, error)
}

// SkillAuditSink durably accepts body-free administration events. Implementers
// must make duplicate EventID delivery idempotent.
type SkillAuditSink interface {
	RecordSkillAudit(context.Context, SkillAuditEvent) error
}

// SkillAuditAttributionProvider supplies optional bounded audit attribution.
// It is metadata only and is never interpreted as authentication or authority.
type SkillAuditAttributionProvider interface {
	SkillAuditActor(context.Context) string
}

// SkillActivationPolicyInput contains only request-local data and body-free
// catalog summaries for remaining resolved auto candidates.
type SkillActivationPolicyInput struct {
	Request     string                 `json:"request"`
	Enrichments map[string]interface{} `json:"enrichments,omitempty"`
	Candidates  []SkillCatalogSummary  `json:"candidates"`
}

// SkillActivationPolicyDecision is a validated three-way refinement: include,
// exclude, or leave a candidate undecided for the default resolver.
type SkillActivationPolicyDecision struct {
	Include []SkillRef `json:"include,omitempty"`
	Exclude []SkillRef `json:"exclude,omitempty"`
}

// SkillActivationPolicy is an optional deterministic refinement. Runtime code
// revalidates every returned identity against the input candidate set.
type SkillActivationPolicy interface {
	Evaluate(context.Context, SkillActivationPolicyInput) (SkillActivationPolicyDecision, error)
}

// SkillResolutionInput is the body-free selector input for initial activation.
type SkillResolutionInput struct {
	Request    string                `json:"request"`
	Boundary   SkillPromptBoundary   `json:"boundary"`
	Candidates []SkillCatalogSummary `json:"candidates"`
	Context    SkillSelectorContext  `json:"context,omitempty"`
}

// SkillPriorResultSummary is a bounded execution-result projection for skill
// selection. It never contains a skill instruction or resource body.
type SkillPriorResultSummary struct {
	StepID  string `json:"step_id"`
	Agent   string `json:"agent"`
	Success bool   `json:"success"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SkillSelectorContext carries bounded lifecycle facts that can change
// activation or resource relevance at a later boundary. It never contains a
// skill instruction or resource body; result and enrichment text is truncated.
type SkillSelectorContext struct {
	Objective               string                    `json:"objective,omitempty"`
	ExpectedCapabilities    []string                  `json:"expected_capabilities,omitempty"`
	PriorResults            []SkillPriorResultSummary `json:"prior_results,omitempty"`
	ExecutedStepIDs         []string                  `json:"executed_step_ids,omitempty"`
	Enrichments             map[string]string         `json:"enrichments,omitempty"`
	PriorResourceSelections []SkillResourceSelection  `json:"prior_resource_selections,omitempty"`
}

// SkillContinuationInput is the body-free selector input for later execution
// boundaries. PreviouslyActive values remain pinned exact revisions.
type SkillContinuationInput struct {
	Request          string                `json:"request"`
	Boundary         SkillPromptBoundary   `json:"boundary"`
	PhaseNumber      int                   `json:"phase_number"`
	Candidates       []SkillCatalogSummary `json:"candidates"`
	PreviouslyActive []ActiveSkill         `json:"previously_active,omitempty"`
	Context          SkillSelectorContext  `json:"context,omitempty"`
}

// SkillActivationDecision contains only validated identity choices and bounded
// explanations. The runtime owns final admission and content loading.
type SkillActivationDecision struct {
	Activate []SkillRef                      `json:"activate,omitempty"`
	Reasons  map[string]SkillSelectionReason `json:"reasons,omitempty"`
}

// SkillResolver performs included activation selection without knowing the
// registry provider or loading instruction/resource bodies.
type SkillResolver interface {
	ResolveInitial(context.Context, SkillResolutionInput) (SkillActivationDecision, error)
	ResolveContinuation(context.Context, SkillContinuationInput) (SkillActivationDecision, error)
}

// SkillResourceResolutionInput contains one body-free eligible resource index
// for a lifecycle boundary.
type SkillResourceResolutionInput struct {
	Request      string                      `json:"request"`
	Boundary     SkillPromptBoundary         `json:"boundary"`
	PhaseNumber  int                         `json:"phase_number"`
	ActiveSkills []ActiveSkill               `json:"active_skills"`
	Resources    []SkillResourceCatalogEntry `json:"resources"`
	Context      SkillSelectorContext        `json:"context,omitempty"`
}

// SkillResourceDecision contains selected resource names and bounded reasons;
// exact resource references are constructed and revalidated by runtime code.
type SkillResourceDecision struct {
	Select  []SkillResourceRequest          `json:"select,omitempty"`
	Reasons map[string]SkillSelectionReason `json:"reasons,omitempty"`
}

// SkillResourceResolver selects among a body-free resource index.
type SkillResourceResolver interface {
	Resolve(context.Context, SkillResourceResolutionInput) (SkillResourceDecision, error)
}

// SkillContentCache stores verified immutable bodies independently from the
// authoritative registry. A cache implementation never makes content
// authoritative and cannot bypass runtime integrity verification.
type SkillContentCache interface {
	GetManifest(context.Context, SkillVersionRef) (SkillManifest, bool, error)
	PutManifest(context.Context, SkillVersionRef, SkillManifest) error
	RemoveManifest(context.Context, SkillVersionRef) error
	GetResource(context.Context, SkillResourceRef) (SkillResource, bool, error)
	PutResource(context.Context, SkillResourceRef, SkillResource) error
	RemoveResource(context.Context, SkillResourceRef) error
}
