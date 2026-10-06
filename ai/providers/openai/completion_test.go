package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestCompletionFactsAcrossFactoryWrapperChainAndLegacy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"separate reason"},"finish_reason":"length"}]}`))
	}))
	defer server.Close()
	for _, fallback := range []bool{false, true} {
		client, err := ai.NewClient(ai.WithProvider("openai"), ai.WithAPIKey("fixture"), ai.WithBaseURL(server.URL), ai.WithMaxRetries(0), ai.WithReasoningContentFallback(fallback))
		if err != nil {
			t.Fatal(err)
		}
		chain, err := ai.NewChain(ai.ClientEntry("fixture", client))
		if err != nil {
			t.Fatal(err)
		}
		response, err := chain.GenerateResponse(context.Background(), "request", &core.AIOptions{Model: "gpt-4.1"})
		if err != nil {
			t.Fatal(err)
		}
		want, source := "", core.AIContentSourceFinal
		if fallback {
			want, source = "separate reason", core.AIContentSourceReasoningFallback
		}
		if response.Content != want || response.ReasoningContent != "separate reason" || response.FinishReason != "length" || response.ContentSource != source {
			t.Fatalf("factory/wrapper/chain lost provenance: %+v", response)
		}
	}
}

func TestReasoningDeliveryPreventsProviderRetryAndChainFailover(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"visible reason\"}}]}\n\n" +
			"data: {\"error\":{\"code\":503}}\n\n"))
	}))
	defer server.Close()
	client, err := ai.NewClient(ai.WithProvider("openai"), ai.WithAPIKey("fixture"), ai.WithBaseURL(server.URL), ai.WithMaxRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	chain, err := ai.NewChain(ai.ClientEntry("first", client), ai.ClientEntry("backup", client))
	if err != nil {
		t.Fatal(err)
	}
	var reasoning strings.Builder
	result, err := core.StreamAI(context.Background(), chain, core.NewAIRequestFromLegacy("request", "test", &core.AIOptions{Model: "gpt-4.1"}), func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			t.Fatalf("reasoning emitted as answer: %+v", chunk)
		}
		reasoning.WriteString(chunk.ReasoningContent)
		return nil
	})
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || requests.Load() != 1 || result == nil || result.Response.ReasoningContent != "visible reason" || reasoning.String() != "visible reason" {
		t.Fatalf("partial stream replayed or facts lost: requests=%d result=%+v error=%v", requests.Load(), result, err)
	}
}

func TestChainConstructorPropagatesCompletionCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"reasoning_content":"legacy answer"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "fixture")
	t.Setenv("OPENAI_BASE_URL", server.URL)
	chain, err := ai.NewChainClient(ai.WithProviderChain("openai"), ai.WithChainMaxRetries(0), ai.WithChainReasoningContentFallback(true))
	if err != nil {
		t.Fatal(err)
	}
	response, err := chain.GenerateResponse(context.Background(), "request", &core.AIOptions{Model: "gpt-4.1"})
	if err != nil || response.Content != "legacy answer" || response.ContentSource != core.AIContentSourceReasoningFallback {
		t.Fatalf("chain constructor dropped compatibility: %+v %v", response, err)
	}
}
