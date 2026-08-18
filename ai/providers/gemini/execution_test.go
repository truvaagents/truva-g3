package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestRequestAwareGenerateReturnsReportAndDetailedUsage(t *testing.T) {
	client := NewClient("static-key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	var capturedHeader string
	var capturedURL string
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		capturedHeader = request.Header.Get("x-goog-api-key")
		capturedURL = request.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"candidates":[{"content":{"role":"model","parts":[{"text":"internal","thought":true},{"text":"done"}]},"finishReason":"STOP","index":0}],
				"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":21,"cachedContentTokenCount":3,"thoughtsTokenCount":4},
				"modelVersion":"gemini-3.7-flash-20260813"
			}`)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "answer")
	request.Generation.Model = "gemini-3.7-flash"
	request.Generation.MaxTokens = core.SetAIParameter(128)

	result, err := client.Generate(t.Context(), request)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result == nil || result.Response == nil || result.RequestReport == nil || result.UsageDetails == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.Response.Content != "done" || result.Response.Model != "gemini-3.7-flash-20260813" ||
		result.Response.Usage.PromptTokens != 11 || result.Response.Usage.CompletionTokens != 7 || result.Response.Usage.TotalTokens != 21 ||
		result.UsageDetails.CachedInputTokens != 3 || result.UsageDetails.ReasoningTokens != 4 {
		t.Fatalf("normalized response = %#v", result)
	}
	if result.RequestReport.EffectiveTemperature.Mode != core.AIParameterOmit ||
		result.RequestReport.EffectiveMaxTokens.Mode != core.AIParameterSet || result.RequestReport.EffectiveMaxTokens.Value != 128 {
		t.Fatalf("request report = %#v", result.RequestReport)
	}
	if capturedHeader != "static-key" || strings.Contains(capturedURL, "static-key") || strings.Contains(capturedURL, "key=") {
		t.Fatalf("credential placement header=%q URL=%q", capturedHeader, capturedURL)
	}
}

func TestRequestAwareStreamReturnsCallbackErrorsWithPartialResultAndReport(t *testing.T) {
	callbackErr := errors.New("application stopped stream")
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"one\"}]},\"index\":0}]}\n\n" +
					"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"two\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":2,\"totalTokenCount\":4}}\n\n",
			)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	result, err := client.Stream(t.Context(), request, func(chunk core.StreamChunk) error {
		if chunk.Content == "one" {
			return callbackErr
		}
		return nil
	})
	if !errors.Is(err, callbackErr) || result == nil || result.Response == nil || result.RequestReport == nil {
		t.Fatalf("Stream = %#v, %v", result, err)
	}
	if result.Response.Content != "one" || result.RequestReport.Operation != "stream" {
		t.Fatalf("partial stream result = %#v", result)
	}
}

func TestRequestAwareStreamSendsOneFinalChunkAndUsesTerminalUsageSnapshot(t *testing.T) {
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"internal\",\"thought\":true},{\"text\":\"one\"}]},\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n" +
					"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"two\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3,\"totalTokenCount\":9,\"cachedContentTokenCount\":2,\"thoughtsTokenCount\":1}}\n\n",
			)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	var chunks []core.StreamChunk
	result, err := client.Stream(t.Context(), request, func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(chunks) != 3 || !chunks[0].Delta || !chunks[1].Delta || chunks[2].Delta ||
		chunks[2].FinishReason != "STOP" || chunks[2].Usage == nil {
		t.Fatalf("chunks = %#v", chunks)
	}
	if result.Response.Content != "onetwo" || result.Response.Usage.PromptTokens != 5 ||
		result.Response.Usage.CompletionTokens != 3 || result.Response.Usage.TotalTokens != 9 ||
		result.UsageDetails.CachedInputTokens != 2 || result.UsageDetails.ReasoningTokens != 1 {
		t.Fatalf("terminal stream result = %#v", result)
	}
}

func TestRequestAwareStreamDecodeFailureReturnsPartialSentinel(t *testing.T) {
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]},\"index\":0}]}\n\n" +
					"data: not-json\n\n",
			)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	result, err := client.Stream(t.Context(), request, func(core.StreamChunk) error { return nil })
	if !errors.Is(err, core.ErrStreamPartiallyCompleted) || result == nil || result.Response == nil || result.Response.Content != "partial" || result.RequestReport == nil {
		t.Fatalf("partial decode = %#v, %v", result, err)
	}
}

func TestRequestAwareStreamCancellationPreservesPartialResultAndReport(t *testing.T) {
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"one\"}]},\"index\":0}]}\n\n" +
					"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"two\"}]},\"finishReason\":\"STOP\",\"index\":0}]}\n\n",
			)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	ctx, cancel := context.WithCancel(context.Background())
	result, err := client.Stream(ctx, request, func(chunk core.StreamChunk) error {
		if chunk.Content == "one" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, core.ErrStreamPartiallyCompleted) {
		t.Fatalf("canceled stream error = %v", err)
	}
	if result == nil || result.Response == nil || result.Response.Content != "one" || result.RequestReport == nil {
		t.Fatalf("canceled stream result = %#v", result)
	}
}

func TestRequestAwareStreamCancellationBeforeTransportPreservesReport(t *testing.T) {
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	transportCalls := 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	result, err := client.Stream(ctx, request, func(core.StreamChunk) error { return nil })
	if !errors.Is(err, context.Canceled) || result == nil || result.RequestReport == nil {
		t.Fatalf("pre-transport cancellation = %#v, %v", result, err)
	}
	if transportCalls > 1 {
		t.Fatalf("transport calls after pre-cancellation = %d", transportCalls)
	}
}

func TestRequestAwareStreamFinalCallbackErrorReturnsCompleteResult(t *testing.T) {
	callbackErr := errors.New("reject final chunk")
	client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: geminiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"complete\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n",
			)),
			Request: request,
		}, nil
	})}
	request := core.NewAIRequest("hello", "")
	request.Generation.Model = "gemini-2.5-flash"
	finalCallbacks := 0
	result, err := client.Stream(t.Context(), request, func(chunk core.StreamChunk) error {
		if !chunk.Delta {
			finalCallbacks++
			return callbackErr
		}
		return nil
	})
	if !errors.Is(err, callbackErr) || finalCallbacks != 1 {
		t.Fatalf("final callback error = %v, callbacks=%d", err, finalCallbacks)
	}
	if result == nil || result.Response == nil || result.Response.Content != "complete" ||
		result.Response.Usage.TotalTokens != 3 || result.RequestReport == nil {
		t.Fatalf("complete result after final callback error = %#v", result)
	}
}

func TestRequestAwareGenerateErrorsPreservePreparedReport(t *testing.T) {
	transportErr := errors.New("transport unavailable")
	tests := []struct {
		name      string
		transport geminiRoundTripFunc
		want      string
	}{
		{
			name: "transport",
			transport: func(*http.Request) (*http.Response, error) {
				return nil, transportErr
			},
			want: "transport request failed",
		},
		{
			name: "provider status",
			transport: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid request"}}`)),
					Request:    request,
				}, nil
			},
			want: "invalid request",
		},
		{
			name: "decode",
			transport: func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`not-json`)),
					Request:    request,
				}, nil
			},
			want: "decode Gemini response",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("key", "https://gemini.example/v1beta", &core.NoOpLogger{})
			client.MaxRetries = 0
			client.HTTPClient = &http.Client{Transport: test.transport}
			request := core.NewAIRequest("hello", "")
			request.Generation.Model = "gemini-2.5-flash"
			result, err := client.Generate(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Generate error = %v; want %q", err, test.want)
			}
			if result == nil || result.RequestReport == nil || !result.RequestReport.Stable {
				t.Fatalf("error result = %#v", result)
			}
		})
	}
}
