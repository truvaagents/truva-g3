package orchestration

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// PrecedenceAudit captures per-interaction observability metadata about
// how the <context_precedence> rule interacted with the enrichments
// supplied to a planning- or synthesis-style prompt. Attached to the
// corresponding LLMInteraction so the registry viewer (or any downstream
// analyzer) can verify that the planner honoured the precedence rule —
// the compliance signal we can't get from raw prompt text alone.
//
// Field groups:
//
//  1. Always-available (cheap; populated from prompt/response text by
//     DerivePrecedenceAudit with no extractor configured):
//     DirectiveEmitted, ProfilePresent, HistoryPresent, PromptKind.
//
//  2. Opt-in (populated only when a PrecedenceEntityExtractor is wired on
//     the orchestrator via AIOrchestrator.SetPrecedenceEntityExtractor):
//     ProfileContextEntities, ConversationEntities, RequestEntities,
//     PlanTargetEntities, Compliance, AuditorVersion.
//
// Entity extraction is opt-in because named-entity recognition is
// domain-specific and the framework stays domain-agnostic per
// FRAMEWORK_DESIGN_PRINCIPLES.md §Framework is domain-agnostic. Agents
// that need compliance detection plug in their own extractor.
//
// UI extensibility: fields are all scalar, list-of-string, or labelled
// strings. Adding a registry-viewer card that renders them is purely
// additive JS — the JSON shape is stable.
type PrecedenceAudit struct {
	// DirectiveEmitted records whether writeContextPrecedence emitted the
	// <context_precedence> block on this prompt. False when the gating
	// helper ran but had no conflict-eligible enrichments.
	DirectiveEmitted bool `json:"directive_emitted"`

	// ProfilePresent is true when the prompt carried a <user_profile>
	// block at the time of recording.
	ProfilePresent bool `json:"profile_present"`

	// HistoryPresent is true when the prompt carried a
	// <conversation_history> block at the time of recording.
	HistoryPresent bool `json:"history_present"`

	// PromptKind identifies which prompt site produced this interaction
	// (PromptKindPlanning / PromptKindContinuation / …). Stable label so
	// dashboards and viewer filters can aggregate across traces.
	PromptKind string `json:"prompt_kind,omitempty"`

	// ProfileContextEntities lists entities the extractor found in the
	// <user_profile> Context block. Nil when no extractor is configured.
	ProfileContextEntities []string `json:"profile_context_entities,omitempty"`

	// ConversationEntities lists entities the extractor found in the
	// <conversation_history> block.
	ConversationEntities []string `json:"conversation_entities,omitempty"`

	// RequestEntities lists entities the extractor found in the
	// <user_request> block.
	RequestEntities []string `json:"request_entities,omitempty"`

	// PlanTargetEntities lists entities the extractor found in the LLM
	// response (for planning sites this is the emitted plan; for
	// synthesis sites it is the synthesized answer).
	PlanTargetEntities []string `json:"plan_target_entities,omitempty"`

	// Compliance is a heuristic label derived from the entity sets:
	//   PrecedenceComplianceCompliant          — plan entities appear in
	//     the live turn (conversation ∪ request) or nowhere suspicious.
	//   PrecedenceComplianceAnchoredOnProfile  — the plan includes an
	//     entity that only appears in <user_profile> Context, not in the
	//     live turn. This is the Switzerland-vs-Italy regression signal.
	//   PrecedenceComplianceInconclusive       — not enough entity data
	//     to judge (no plan entities extracted).
	// Empty when no extractor is configured.
	Compliance string `json:"compliance,omitempty"`

	// AuditorVersion is the version string reported by the entity
	// extractor that produced this record. Lets the UI hide/demote
	// records from older extractor revisions during schema evolution.
	AuditorVersion string `json:"auditor_version,omitempty"`
}

// Compliance labels for PrecedenceAudit.Compliance.
const (
	// PrecedenceComplianceCompliant indicates the plan's named entities
	// all appear in the live turn (conversation_history ∪ user_request),
	// or no profile-only entity leaked into the plan.
	PrecedenceComplianceCompliant = "compliant"

	// PrecedenceComplianceAnchoredOnProfile indicates the plan contains
	// at least one entity that appears only in <user_profile> Context
	// and not in the live turn — the canonical failure mode the
	// <context_precedence> rule is meant to prevent.
	PrecedenceComplianceAnchoredOnProfile = "anchored_on_profile"

	// PrecedenceComplianceInconclusive indicates the audit lacked enough
	// entity data to judge (e.g., the extractor returned no entities
	// from the response).
	PrecedenceComplianceInconclusive = "inconclusive"
)

// PrecedenceSection identifies which source section a piece of text
// came from when the extractor is invoked. Implementations can apply
// section-aware parsing (e.g., JSON-aware for PlanResponse, bullet-list
// parsing for ProfileContext).
type PrecedenceSection string

const (
	PrecedenceSectionProfileContext PrecedenceSection = "profile_context"
	PrecedenceSectionConversation   PrecedenceSection = "conversation_history"
	PrecedenceSectionRequest        PrecedenceSection = "request"
	PrecedenceSectionPlanResponse   PrecedenceSection = "plan_response"
)

// PrecedenceEntityExtractor pulls named entities from a prompt section
// for context_precedence auditing. The framework ships no default
// implementation — named-entity recognition is domain-specific and the
// framework stays domain-agnostic. Agents wire their own extractor via
// AIOrchestrator.SetPrecedenceEntityExtractor.
//
// Contract:
//   - Must be safe for concurrent use (called from debug-recording
//     goroutines).
//   - Should be fast (~ms). Expensive extractors (e.g., LLM-based) must
//     wrap themselves in their own async mechanism and return a cached
//     result on the hot path.
//   - Version() returns a string that changes when the extractor's
//     logic changes materially; the UI uses it to decide whether to
//     surface old records.
type PrecedenceEntityExtractor interface {
	ExtractEntities(ctx context.Context, section PrecedenceSection, text string) []string
	Version() string
}

// Precompiled regexes for finding the four source sections in a
// recorded prompt. The framework emits these tags itself, so the
// structure is stable across prompt revisions.
var (
	userProfileTagRE          = regexp.MustCompile(`(?s)<user_profile>(.*?)</user_profile>`)
	userProfileContextBlockRE = regexp.MustCompile(`(?s)Context:\s*\n(.*?)(?:\n\n|\n</user_profile>|\z)`)
	conversationHistoryRE     = regexp.MustCompile(`(?s)<conversation_history>(.*?)</conversation_history>`)
	userRequestRE             = regexp.MustCompile(`(?s)<user_request>(.*?)</user_request>`)
)

// DerivePrecedenceAudit builds a PrecedenceAudit from a recorded
// LLMInteraction. Returns nil when the prompt has no conflict-eligible
// enrichments (<user_profile> or <conversation_history>) — those
// interactions have nothing to audit and we keep the LLMInteraction
// record lean.
//
// If extractor is nil, only the cheap structural fields (DirectiveEmitted,
// ProfilePresent, HistoryPresent, PromptKind) are populated. When
// extractor is non-nil, entity lists and Compliance are filled in too.
//
// Safe to call on every interaction; the self-gating on tag presence
// keeps unrelated interactions (hook LLM calls, micro-resolution, etc.)
// from carrying an audit record they don't need.
func DerivePrecedenceAudit(ctx context.Context, i LLMInteraction, extractor PrecedenceEntityExtractor) *PrecedenceAudit {
	profileMatch := userProfileTagRE.FindStringSubmatch(i.Prompt)
	historyMatch := conversationHistoryRE.FindStringSubmatch(i.Prompt)
	hasProfile := len(profileMatch) > 1
	hasHistory := len(historyMatch) > 1

	if !hasProfile && !hasHistory {
		return nil
	}

	audit := &PrecedenceAudit{
		DirectiveEmitted: strings.Contains(i.Prompt, "<context_precedence>"),
		ProfilePresent:   hasProfile,
		HistoryPresent:   hasHistory,
		PromptKind:       promptKindForInteractionType(i.Type),
	}

	if extractor == nil {
		return audit
	}

	audit.AuditorVersion = extractor.Version()

	if hasProfile {
		if ctxMatch := userProfileContextBlockRE.FindStringSubmatch(profileMatch[1]); len(ctxMatch) > 1 {
			audit.ProfileContextEntities = extractor.ExtractEntities(ctx, PrecedenceSectionProfileContext, ctxMatch[1])
		}
	}
	if hasHistory {
		audit.ConversationEntities = extractor.ExtractEntities(ctx, PrecedenceSectionConversation, historyMatch[1])
	}
	requestDecodeFailed := false
	if reqMatch := userRequestRE.FindStringSubmatch(i.Prompt); len(reqMatch) > 1 {
		requestText := removePromptSectionFraming(reqMatch[1])
		if skillPromptRequestEncodingActive(ctx) {
			var decoded string
			if err := json.Unmarshal([]byte(requestText), &decoded); err != nil {
				requestDecodeFailed = true
				telemetry.AddSpanEvent(ctx, "orchestrator.context_precedence.request_decode_failed",
					attribute.String("prompt_kind", audit.PromptKind),
					attribute.String("section", string(PrecedenceSectionRequest)),
					attribute.String("encoder_version", skillInputEncoderPolicyVersion),
				)
			} else {
				requestText = decoded
			}
		}
		if !requestDecodeFailed {
			audit.RequestEntities = extractor.ExtractEntities(ctx, PrecedenceSectionRequest, requestText)
		}
	}
	if i.Response != "" {
		audit.PlanTargetEntities = extractor.ExtractEntities(ctx, PrecedenceSectionPlanResponse, i.Response)
	}

	if requestDecodeFailed {
		audit.Compliance = PrecedenceComplianceInconclusive
	} else {
		audit.Compliance = derivePrecedenceCompliance(audit)
	}
	return audit
}

// removePromptSectionFraming removes only the line feeds inserted by the
// framework immediately inside a prompt section. Caller-owned leading and
// trailing whitespace remains part of the semantic value.
func removePromptSectionFraming(value string) string {
	value = strings.TrimPrefix(value, "\n")
	value = strings.TrimSuffix(value, "\n")
	return value
}

func skillPromptRequestEncodingActive(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	preparer, _ := ctx.Value(promptInputPreparerContextKey{}).(promptInputPreparer)
	switch preparer.(type) {
	case skillPromptInputPreparer, *skillPromptInputPreparer:
		return true
	default:
		return false
	}
}

// promptKindForInteractionType maps recorded LLMInteraction.Type values
// to the stable PromptKind labels emitted on context_precedence
// telemetry. Lets downstream consumers filter on a single canonical
// label regardless of which synthesis/planning variant produced the
// interaction.
func promptKindForInteractionType(t string) string {
	switch t {
	case "plan_generation":
		return PromptKindPlanning
	case "continuation_plan_generation", "continuation_plan_regeneration":
		return PromptKindContinuation
	case "synthesis", "synthesis_streaming":
		return PromptKindSynthesis
	default:
		return t
	}
}

// derivePrecedenceCompliance classifies the audit's compliance label
// from the entity sets. Case-insensitive comparison so "Switzerland"
// and "switzerland" are treated as the same entity.
func derivePrecedenceCompliance(a *PrecedenceAudit) string {
	if len(a.PlanTargetEntities) == 0 {
		return PrecedenceComplianceInconclusive
	}

	live := make(map[string]struct{})
	for _, e := range a.ConversationEntities {
		live[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}
	for _, e := range a.RequestEntities {
		live[strings.ToLower(strings.TrimSpace(e))] = struct{}{}
	}

	profileOnly := make(map[string]struct{})
	for _, e := range a.ProfileContextEntities {
		key := strings.ToLower(strings.TrimSpace(e))
		if _, inLive := live[key]; !inLive {
			profileOnly[key] = struct{}{}
		}
	}

	for _, e := range a.PlanTargetEntities {
		key := strings.ToLower(strings.TrimSpace(e))
		if _, inProfileOnly := profileOnly[key]; inProfileOnly {
			return PrecedenceComplianceAnchoredOnProfile
		}
	}
	return PrecedenceComplianceCompliant
}
