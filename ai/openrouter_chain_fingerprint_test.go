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
