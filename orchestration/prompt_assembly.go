package orchestration

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type promptKind string

const (
	promptInitialPlan      promptKind = "initial_plan"
	promptCorrection       promptKind = "correction"
	promptContinuationPlan promptKind = "continuation_plan"
	promptRegeneration     promptKind = "regeneration"
	promptSynthesis        promptKind = "synthesis"
	promptCapabilitySelect promptKind = "capability_selection"
	promptOther            promptKind = "other"
)

// promptDynamicValueKind identifies one framework-owned dynamic input before
// a renderer interpolates it. It is deliberately private and feature-neutral.
type promptDynamicValueKind string

const (
	promptValueRequest            promptDynamicValueKind = "request"
	promptValueCapabilityCatalog  promptDynamicValueKind = "capability_catalog"
	promptValuePhaseContext       promptDynamicValueKind = "phase_context"
	promptValueEnrichment         promptDynamicValueKind = "enrichment"
	promptValuePriorResult        promptDynamicValueKind = "prior_result"
	promptValueContinuationNote   promptDynamicValueKind = "continuation_note"
	promptValueValidationFeedback promptDynamicValueKind = "validation_feedback"
)

const (
	promptFieldRequest                        = "request"
	promptFieldCapabilityCatalog              = "capability_catalog"
	promptFieldContinuationNote               = "continuation_note"
	promptFieldValidationFeedback             = "validation_feedback"
	promptFieldPriorResultStepID              = "prior_result.step_id"
	promptFieldPriorResultAgentName           = "prior_result.agent_name"
	promptFieldPriorResultInstruction         = "prior_result.instruction"
	promptFieldPriorResultResponse            = "prior_result.response"
	promptFieldPriorResultError               = "prior_result.error"
	promptFieldPhaseContextPriorToolID        = "phase_context.prior_tool_id"
	promptFieldPhaseContextCompletedSummary   = "phase_context.completed_summary"
	promptFieldEnrichmentActivityCoordination = "enrichment.activity_coordination"
	promptFieldEnrichmentUserProfile          = "enrichment.user_profile"
	promptFieldEnrichmentRAGContext           = "enrichment.rag_context"
	promptFieldEnrichmentConversationHistory  = "enrichment.conversation_history"
)

type promptInputPreparer interface {
	PreparePromptValue(
		context.Context,
		promptKind,
		promptDynamicValueKind,
		string,
		string,
	) (string, error)
}

type promptInputPreparerContextKey struct{}

func withPromptInputPreparer(ctx context.Context, preparer promptInputPreparer) context.Context {
	if preparer == nil {
		return ctx
	}
	return context.WithValue(ctx, promptInputPreparerContextKey{}, preparer)
}

func preparePromptValue(
	ctx context.Context,
	kind promptKind,
	valueKind promptDynamicValueKind,
	field string,
	value string,
) (string, error) {
	preparer, _ := ctx.Value(promptInputPreparerContextKey{}).(promptInputPreparer)
	if preparer == nil {
		return value, nil
	}
	return preparer.PreparePromptValue(ctx, kind, valueKind, field, value)
}

func preparePromptInput(ctx context.Context, kind promptKind, source PromptInput) (PromptInput, error) {
	prepared := source
	prepared.Metadata = clonePromptMetadata(source.Metadata)
	var err error
	prepared.Request, err = preparePromptValue(ctx, kind, promptValueRequest, promptFieldRequest, source.Request)
	if err != nil {
		return PromptInput{}, err
	}
	prepared.CapabilityInfo, err = preparePromptValue(
		ctx, kind, promptValueCapabilityCatalog, promptFieldCapabilityCatalog, source.CapabilityInfo,
	)
	if err != nil {
		return PromptInput{}, err
	}
	prepared.Metadata, err = prepareKnownPromptEnrichments(ctx, kind, prepared.Metadata)
	if err != nil {
		return PromptInput{}, err
	}
	return prepared, nil
}

func prepareKnownPromptEnrichments(
	ctx context.Context,
	kind promptKind,
	source map[string]interface{},
) (map[string]interface{}, error) {
	prepared := clonePromptMetadata(source)
	known := []struct {
		key   string
		field string
	}{
		{core.EnrichmentActivityCoordination, promptFieldEnrichmentActivityCoordination},
		{core.EnrichmentUserProfile, promptFieldEnrichmentUserProfile},
		{core.EnrichmentRAGContext, promptFieldEnrichmentRAGContext},
		{core.EnrichmentConversationHistory, promptFieldEnrichmentConversationHistory},
	}
	for _, item := range known {
		value, found := prepared[item.key]
		if !found {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		var err error
		prepared[item.key], err = preparePromptValue(ctx, kind, promptValueEnrichment, item.field, text)
		if err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

func prepareSynthesisStepValues(
	ctx context.Context,
	agentName string,
	instruction string,
	content string,
	contentField string,
) (string, string, string, error) {
	preparedAgent, err := preparePromptValue(
		ctx, promptSynthesis, promptValuePriorResult,
		promptFieldPriorResultAgentName, agentName,
	)
	if err != nil {
		return "", "", "", err
	}
	preparedInstruction, err := preparePromptValue(
		ctx, promptSynthesis, promptValuePriorResult,
		promptFieldPriorResultInstruction, instruction,
	)
	if err != nil {
		return "", "", "", err
	}
	preparedContent, err := preparePromptValue(
		ctx, promptSynthesis, promptValuePriorResult, contentField, content,
	)
	if err != nil {
		return "", "", "", err
	}
	return preparedAgent, preparedInstruction, preparedContent, nil
}

func prepareValidationFeedback(
	ctx context.Context,
	kind promptKind,
	validationErr error,
) (string, error) {
	if validationErr == nil {
		return "", nil
	}
	return preparePromptValue(
		ctx,
		kind,
		promptValueValidationFeedback,
		promptFieldValidationFeedback,
		validationErr.Error(),
	)
}

func clonePromptMetadata(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

type promptRole uint8

const (
	promptRoleSystem promptRole = iota
	promptRoleUser
)

type promptSystemSource uint8

const (
	promptSystemFrameworkBuilder promptSystemSource = iota
	promptSystemCustomBuilder
	promptSystemAIOptionsOverride
)

type promptSection struct {
	Name string
	Body string
	Role promptRole
}

type promptAssembly struct {
	Kind            promptKind
	SystemBase      string
	SystemSource    promptSystemSource
	UserSections    []promptSection
	UserTail        []promptSection
	Generation      core.AIGenerationOptions
	ProviderPatches []core.AIProviderPatch
}

type promptFinalizer interface {
	Finalize(context.Context, promptAssembly) (promptAssembly, error)
}

type promptFinalizerFunc func(context.Context, promptAssembly) (promptAssembly, error)

func (f promptFinalizerFunc) Finalize(ctx context.Context, assembly promptAssembly) (promptAssembly, error) {
	return f(ctx, assembly)
}

var (
	ErrReservedRuntimeContext = errors.New("runtime_context is a framework-reserved prompt section")
	ErrInvalidPromptAssembly  = errors.New("invalid prompt assembly")
)

func promptKindForPurpose(purpose string) promptKind {
	switch purpose {
	case "planning":
		return promptInitialPlan
	case "plan-correction":
		return promptCorrection
	case "continuation-planning":
		return promptContinuationPlan
	case "synthesis":
		return promptSynthesis
	case "tiered-selection":
		return promptCapabilitySelect
	default:
		return promptOther
	}
}

func promptFinalizers(kind promptKind) []promptFinalizer {
	switch kind {
	case promptInitialPlan, promptContinuationPlan, promptRegeneration:
		return []promptFinalizer{
			promptFinalizerFunc(finalizeRuntimeContext),
			promptFinalizerFunc(finalizeSkillPrompt),
		}
	case promptSynthesis, promptCapabilitySelect:
		return []promptFinalizer{promptFinalizerFunc(finalizeSkillPrompt)}
	default:
		return nil
	}
}

func requiresFinalizedSystemPrompt(ctx context.Context, kind promptKind) bool {
	switch kind {
	case promptInitialPlan, promptContinuationPlan, promptRegeneration:
		return true
	case promptSynthesis:
		projection, ok := skillPromptProjectionFromContext(ctx)
		return ok && compileSkillInstructionEnvelope(projection, true) != ""
	default:
		return false
	}
}

func finalizePromptAssembly(ctx context.Context, assembly promptAssembly) (promptAssembly, error) {
	current := clonePromptAssembly(assembly)
	for _, finalizer := range promptFinalizers(current.Kind) {
		var err error
		current, err = finalizer.Finalize(ctx, current)
		if err != nil {
			return promptAssembly{}, err
		}
	}
	return current, nil
}

func finalizeRuntimeContext(_ context.Context, assembly promptAssembly) (promptAssembly, error) {
	hasOpen := strings.Contains(assembly.SystemBase, "<runtime_context>")
	hasClose := strings.Contains(assembly.SystemBase, "</runtime_context>")
	if hasOpen || hasClose {
		if assembly.SystemSource != promptSystemAIOptionsOverride &&
			strings.Count(assembly.SystemBase, "<runtime_context>") == 1 &&
			strings.Count(assembly.SystemBase, "</runtime_context>") == 1 &&
			hasCanonicalRuntimeContextSuffix(assembly.SystemBase) {
			return assembly, nil
		}
		return promptAssembly{}, ErrReservedRuntimeContext
	}
	assembly.SystemBase = appendRuntimeContext(assembly.SystemBase)
	return assembly, nil
}

func hasCanonicalRuntimeContextSuffix(value string) bool {
	const prefix = "\n\n<runtime_context>\nCurrent date (UTC): "
	const suffix = ". Resolve relative dates (today, tomorrow, next week, etc.) against this value.\n</runtime_context>"
	start := strings.LastIndex(value, prefix)
	if start < 0 || !strings.HasSuffix(value, suffix) {
		return false
	}
	dateStart := start + len(prefix)
	dateEnd := len(value) - len(suffix)
	if dateStart >= dateEnd {
		return false
	}
	_, err := time.Parse("2006-01-02", value[dateStart:dateEnd])
	return err == nil
}

func containsRuntimeContextTag(value string) bool {
	return strings.Contains(value, "<runtime_context>") || strings.Contains(value, "</runtime_context>")
}

func renderUserPrompt(assembly promptAssembly) string {
	var builder strings.Builder
	for _, section := range assembly.UserSections {
		builder.WriteString(section.Body)
	}
	for _, section := range assembly.UserTail {
		builder.WriteString(section.Body)
	}
	return builder.String()
}

func clonePromptAssembly(source promptAssembly) promptAssembly {
	copy := source
	copy.UserSections = append([]promptSection(nil), source.UserSections...)
	copy.UserTail = append([]promptSection(nil), source.UserTail...)
	copy.ProviderPatches = append([]core.AIProviderPatch(nil), source.ProviderPatches...)
	return copy
}
