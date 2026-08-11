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
	promptOther            promptKind = "other"
)

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
	default:
		return promptOther
	}
}

func promptFinalizers(kind promptKind) []promptFinalizer {
	switch kind {
	case promptInitialPlan, promptContinuationPlan, promptRegeneration:
		return []promptFinalizer{promptFinalizerFunc(finalizeRuntimeContext)}
	default:
		return nil
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
