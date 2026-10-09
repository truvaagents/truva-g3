package openai

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/truvaagents/truva-g3/ai/internal/wirevalue"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// This is provider capability data, not a global budget. Keep unknown models
// unknown and exact routed IDs separate from native families. Source reviewed
// 2026-10-08: native model docs and OpenRouter's Luna/Sol endpoint metadata.
type modelBudgetContract struct {
	outputLimit              int
	reasoningActiveByDefault bool
}

func reviewedModelBudget(provider, model string) modelBudgetContract {
	if provider == "openai" {
		switch model {
		case "gpt-5.6-sol":
			return modelBudgetContract{outputLimit: 128000}
		case "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol":
			return modelBudgetContract{outputLimit: 128000, reasoningActiveByDefault: true}
		}
	}
	if provider == openRouterProviderAlias {
		switch model {
		case "openai/gpt-5.6-luna":
			return modelBudgetContract{outputLimit: 128000}
		case "openai/gpt-6-luna", "openai/gpt-6.1-sol":
			return modelBudgetContract{outputLimit: 128000, reasoningActiveByDefault: true}
		}
	}
	return modelBudgetContract{}
}

func reviewedOutputLimit(provider, model string) int {
	return reviewedModelBudget(provider, model).outputLimit
}

// Embedding the concrete wire draft preserves explicit-intent/report interfaces
// while delegating model-specific validation to the existing contract wrapper.
type outputBudgetDraft struct {
	*openaiwire.Draft
	contract requestpolicy.Draft
	limit    int
}

func (d *outputBudgetDraft) PolicyFingerprintIdentity() string {
	identity := d.Draft.PolicyFingerprintIdentity()
	if contract, ok := d.contract.(interface{ PolicyFingerprintIdentity() string }); ok {
		identity = contract.PolicyFingerprintIdentity()
	}
	return identity + "|output-ceiling-v1=" + strconv.Itoa(d.limit)
}

func (d *outputBudgetDraft) Validate() error {
	if err := d.contract.Validate(); err != nil {
		return err
	}
	_, path := d.EffectiveGenerationPaths()
	if value, present := d.Get(path); present {
		number, err := budgetInteger(value)
		if err != nil || number > d.limit {
			return &requestpolicy.PolicyError{Stage: "output-capacity", Path: path,
				Err: fmt.Errorf("output budget exceeds reviewed model capacity of %d tokens", d.limit)}
		}
	}
	return nil
}

func budgetInteger(value interface{}) (int, error) {
	// The wire draft uses Go ints; application policy may supply JSON numbers.
	if n, valid := wirevalue.Int(value); valid && n > 0 {
		return n, nil
	}
	return 0, errors.New("output budget must be a positive representable integer")
}

func outputCeilingPolicy(engine *requestpolicy.Engine, draft *openaiwire.Draft, contract requestpolicy.Draft, limit int) (*requestpolicy.Engine, requestpolicy.Draft, error) {
	if limit == 0 {
		return engine, contract, nil
	}
	bounded := &outputBudgetDraft{Draft: draft, contract: contract, limit: limit}
	_, path := draft.EffectiveGenerationPaths()
	value, present := draft.Get(path)
	if !present {
		return engine, bounded, nil
	}
	n, err := budgetInteger(value)
	if err != nil {
		return nil, nil, err
	}
	if n <= limit {
		return engine, bounded, nil
	}
	derived, err := engine.WithBuiltInRules(core.AIProviderPatch{
		Name: "model-output-ceiling", Version: "1",
		Selector: core.AIProviderSelector{ProviderAlias: draft.Info().ProviderAlias, Surface: draft.Info().Surface},
		Set:      map[string]interface{}{path: limit},
	})
	return derived, bounded, err
}
