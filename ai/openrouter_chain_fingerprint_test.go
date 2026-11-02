package ai_test

import (
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers/openai"
	"github.com/truvaagents/truva-g3/core"
)

func TestOpenRouterRouterEntryMakesWholeChainFingerprintUnstable(t *testing.T) {
	stable := openai.NewClient("key", "", "openai", &core.NoOpLogger{})
	router := openai.NewClient("key", "", "openai.openrouter", &core.NoOpLogger{})
	router.DefaultModel = "openrouter/auto"
	request := core.NewAIRequest("prompt", "planning")

	for _, entries := range [][]ai.ChainEntry{
		{ai.ClientEntry("openai", stable), ai.ClientEntry("openrouter", router)},
		{ai.ClientEntry("openrouter", router), ai.ClientEntry("openai", stable)},
	} {
		chain, err := ai.NewChain(entries...)
		if err != nil {
			t.Fatal(err)
		}
		if fingerprint, cacheStable := chain.RequestFingerprint(t.Context(), request); cacheStable || fingerprint != "" {
			t.Fatalf("fingerprint=%q stable=%t", fingerprint, cacheStable)
		}
	}

	stableChain, err := ai.NewChain(ai.ClientEntry("openai", stable))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint, cacheStable := stableChain.RequestFingerprint(t.Context(), request); !cacheStable || len(fingerprint) != 64 {
		t.Fatalf("stable control fingerprint=%q stable=%t", fingerprint, cacheStable)
	}
}

func TestOpenRouterConcreteDefaultsKeepChainFingerprintStable(t *testing.T) {
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_DEFAULT", "")
	t.Setenv("TRUVAG3_OPENROUTER_MODEL_SMART", "")
	for _, alias := range []string{"default", "smart"} {
		t.Run(alias, func(t *testing.T) {
			client := openai.NewClient("key", "", "openai.openrouter", &core.NoOpLogger{})
			client.DefaultModel = alias
			chain, err := ai.NewChain(ai.ClientEntry("openrouter", client))
			if err != nil {
				t.Fatal(err)
			}
			request := core.NewAIRequest("prompt", "planning")
			before, stable := chain.RequestFingerprint(t.Context(), request)
			if !stable || len(before) != 64 {
				t.Fatalf("concrete alias fingerprint=%q stable=%t", before, stable)
			}
			request.Generation.Model = "openrouter/auto"
			if fingerprint, cacheStable := chain.RequestFingerprint(t.Context(), request); cacheStable || fingerprint != "" {
				t.Fatalf("router fingerprint=%q stable=%t", fingerprint, cacheStable)
			}
		})
	}
}
