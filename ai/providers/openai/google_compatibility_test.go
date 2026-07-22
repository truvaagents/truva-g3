package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

var (
	googleProjectIDPattern = regexp.MustCompile(
		`^(?:[a-z][a-z0-9-]{4,28}[a-z0-9]|[0-9]{6,30})$`,
	)
	googleLocationPattern = regexp.MustCompile(
		`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`,
	)
)

func googleOpenAIBaseURL(projectID, location string) (*url.URL, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	if !googleProjectIDPattern.MatchString(projectID) {
		return nil, errors.New("Google project ID is invalid")
	}
	if !googleLocationPattern.MatchString(location) {
		return nil, errors.New("Google location is invalid")
	}
	host := "aiplatform.googleapis.com"
	if location != "global" {
		host = location + "-aiplatform.googleapis.com"
	}
	return &url.URL{
		Scheme: "https", Host: host,
		Path: fmt.Sprintf(
			"/v1/projects/%s/locations/%s/endpoints/openapi",
			url.PathEscape(projectID), url.PathEscape(location),
		),
	}, nil
}

type googleRecordedRequest struct {
	url     string
	headers http.Header
	body    map[string]interface{}
}

type googleRecordingTransport struct {
	mu       sync.Mutex
	requests []googleRecordedRequest
}

func (transport *googleRecordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, googleRecordedRequest{
		url: request.URL.String(), headers: request.Header.Clone(), body: body,
	})
	call := len(transport.requests)
	transport.mu.Unlock()
	if call == 1 {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)),
			Request:    request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"model":"google/gemini-2.5-flash","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		)),
		Request: request,
	}, nil
}

func (transport *googleRecordingTransport) capturedRequests() []googleRecordedRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]googleRecordedRequest(nil), transport.requests...)
}

func TestGoogleOpenAIBaseURLUsesLocationDependentHost(t *testing.T) {
	tests := []struct {
		location string
		want     string
	}{
		{location: "global", want: "https://aiplatform.googleapis.com/v1/projects/acme-prod/locations/global/endpoints/openapi"},
		{location: "us-central1", want: "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-prod/locations/us-central1/endpoints/openapi"},
	}
	for _, test := range tests {
		t.Run(test.location, func(t *testing.T) {
			endpoint, err := googleOpenAIBaseURL("acme-prod", test.location)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.String() != test.want {
				t.Fatalf("endpoint = %q", endpoint.String())
			}
		})
	}
	for _, invalid := range []struct {
		project  string
		location string
	}{
		{project: "Bad_Project", location: "global"},
		{project: "acme-prod", location: "US-CENTRAL1"},
		{project: "acme-prod", location: "us.central1"},
	} {
		if _, err := googleOpenAIBaseURL(invalid.project, invalid.location); err == nil {
			t.Fatalf("invalid input accepted: %#v", invalid)
		}
	}
}

func TestGoogleHostedOpenAIUsesGenericPublicContract(t *testing.T) {
	const model = "google/gemini-2.5-flash"
	for _, location := range []string{"global", "us-central1"} {
		t.Run(location, func(t *testing.T) {
			baseURL, err := googleOpenAIBaseURL("acme-prod", location)
			if err != nil {
				t.Fatal(err)
			}
			transport := &googleRecordingTransport{}
			var tokenCalls atomic.Int64
			client, err := ai.NewRequestClient(
				ai.WithProvider("openai"),
				ai.WithBaseURL(baseURL.String()),
				ai.WithModel(model),
				ai.WithMaxRetries(1),
				ai.WithAuthHeader("Authorization", func(context.Context) (string, error) {
					return fmt.Sprintf("Bearer adc-token-%d", tokenCalls.Add(1)), nil
				}),
				ai.WithRequestRules(core.AIProviderPatch{
					Name: "google-reasoning-effort", Version: "1",
					Selector: core.AIProviderSelector{Provider: "openai", Model: "google/*"},
					Set:      map[string]interface{}{`/reasoning_effort`: "low"},
				}),
				ai.WithHTTPClient(&http.Client{Transport: transport}),
			)
			if err != nil {
				t.Fatalf("ai.NewRequestClient returned error: %v", err)
			}
			result, err := client.Generate(t.Context(), core.NewAIRequest("hello", "google-openai"))
			if err != nil {
				t.Fatalf("Generate returned error: %v", err)
			}
			if result == nil || result.Response == nil || result.Response.Content != "ok" {
				t.Fatalf("result = %#v", result)
			}
			requests := transport.capturedRequests()
			if len(requests) != 2 || tokenCalls.Load() != 2 {
				t.Fatalf("transport calls=%d token calls=%d", len(requests), tokenCalls.Load())
			}
			wantURL := strings.TrimRight(baseURL.String(), "/") + "/chat/completions"
			for index, request := range requests {
				if request.url != wantURL || request.headers.Get("Authorization") != fmt.Sprintf("Bearer adc-token-%d", index+1) {
					t.Fatalf("request %d = %#v", index+1, request)
				}
				if request.body["model"] != model || request.body["reasoning_effort"] != "low" ||
					request.body["max_tokens"] == nil || request.body["temperature"] == nil {
					t.Fatalf("body %d = %#v", index+1, request.body)
				}
				for _, absent := range []string{"reasoning", "max_completion_tokens"} {
					if _, present := request.body[absent]; present {
						t.Fatalf("body %d contains %q: %#v", index+1, absent, request.body)
					}
				}
			}
			if !reflect.DeepEqual(requests[0].body, requests[1].body) {
				t.Fatalf("retry body drift: %#v vs %#v", requests[0].body, requests[1].body)
			}
		})
	}
}
