package openaiwire_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

func TestCompletionProvenanceBuffered(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		codec, err := openaiwire.NewConfiguredCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningContentFallback: fallback})
		if err != nil {
			t.Fatal(err)
		}
		for _, final := range []string{"", "answer *exact*"} {
			for _, finish := range []string{"", "stop", "length", "content_filter", "provider-specific"} {
				body := `{"choices":[{"message":{"content":"` + final + `","reasoning_content":"reason *exact*"},"finish_reason":"` + finish + `"}]}`
				result, err := codec.Decode(strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				want, source := final, core.AIContentSourceFinal
				if fallback && final == "" {
					want, source = "reason *exact*", core.AIContentSourceReasoningFallback
				}
				if result.Response.Content != want || result.Response.ReasoningContent != "reason *exact*" || result.Response.FinishReason != finish || result.Response.ContentSource != source {
					t.Fatalf("fallback=%v final=%q finish=%q: %+v", fallback, final, finish, result.Response)
				}
			}
		}
	}
}

func TestCompletionPolicyChangesFingerprintNotRequest(t *testing.T) {
	var drafts []*openaiwire.Draft
	for _, fallback := range []bool{false, true} {
		codec, err := openaiwire.NewConfiguredCodec(openaiwire.Config{SurfaceVersion: "custom-surface", ReasoningContentFallback: fallback})
		if err != nil {
			t.Fatal(err)
		}
		draft, err := codec.BuildDraft(core.NewAIRequest("request", "test"), "gpt-4.1", false)
		if err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, draft)
	}
	if !reflect.DeepEqual(drafts[0].Body(), drafts[1].Body()) || drafts[0].PolicyFingerprintIdentity() == drafts[1].PolicyFingerprintIdentity() {
		t.Fatal("decoding policy must vary cache identity without changing the provider request")
	}
}

func TestCompletionStreamKeepsBothChannelsAndCompatibility(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		codec, err := openaiwire.NewConfiguredCodec(openaiwire.Config{SurfaceVersion: openaiwire.DefaultSurfaceVersion, ReasoningContentFallback: fallback})
		if err != nil {
			t.Fatal(err)
		}
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"r1\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"answer\",\"reasoning_content\":\"r2\"},\"finish_reason\":\"length\"}]}\n\n"
		var chunks []core.StreamChunk
		result, err := codec.DecodeStream(strings.NewReader(stream), func(c core.StreamChunk) error { chunks = append(chunks, c); return nil })
		if err != nil {
			t.Fatal(err)
		}
		want, source := "answer", core.AIContentSourceFinal
		if fallback {
			want, source = "r1answer", core.AIContentSourceMixed
		}
		if result.Response.Content != want || result.Response.ReasoningContent != "r1r2" || result.Response.FinishReason != "length" || result.Response.ContentSource != source || len(chunks) != 3 {
			t.Fatalf("fallback=%v result=%+v chunks=%+v", fallback, result.Response, chunks)
		}
		if chunks[1].ReasoningContent != "r2" || chunks[1].Content != "answer" {
			t.Fatal("simultaneous channels lost", chunks)
		}
	}
}

func TestReasoningOnlyPartialStreamCannotBeReplayed(t *testing.T) {
	codec := mustCodec(t)
	for _, ending := range []string{"", "data: invalid\n\n", "data: {\"error\":{\"code\":503}}\n\n", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"error\"}]}\n\n"} {
		stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n" + ending
		result, err := codec.DecodeStream(strings.NewReader(stream), func(c core.StreamChunk) error {
			if c.Content != "" || c.ReasoningContent != "thinking" {
				t.Fatalf("wrong channel: %+v", c)
			}
			return nil
		})
		if !errors.Is(err, core.ErrStreamPartiallyCompleted) || result == nil || result.Response.Content != "" || result.Response.ReasoningContent != "thinking" {
			t.Fatalf("partial reasoning lost or replayable: %+v %v", result, err)
		}
	}
}
