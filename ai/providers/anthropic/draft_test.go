package anthropic

import (
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

func directTestRequestProfile(model string) requestProfile {
	return requestProfile{
		fingerprintIdentity: directProfileIdentity,
		semanticModel:       model,
		wireModel:           model,
		modelField:          modelInBody,
		versionPlacement:    versionInHeader,
		version:             APIVersion,
	}
}

func TestAnthropicDraftValidate(t *testing.T) {
	validBody := func() map[string]interface{} {
		return map[string]interface{}{
			"model":      "claude-test",
			"messages":   []Message{{Role: "user", Content: "hello"}},
			"max_tokens": 100,
		}
	}
	tests := []struct {
		name    string
		body    map[string]interface{}
		stream  bool
		wantErr string
	}{
		{name: "valid sync", body: validBody()},
		{name: "valid int32 tokens", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": int32(1)}},
		{name: "valid int64 tokens", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": int64(1)}},
		{name: "valid stream", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": 1, "stream": true}, stream: true},
		{name: "wrong model", body: map[string]interface{}{"model": "other", "messages": true, "max_tokens": 1}, wantErr: "model invariant"},
		{name: "missing messages", body: map[string]interface{}{"model": "claude-test", "max_tokens": 1}, wantErr: "messages"},
		{name: "missing tokens", body: map[string]interface{}{"model": "claude-test", "messages": true}, wantErr: "max_tokens is required"},
		{name: "zero int tokens", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": 0}, wantErr: "positive"},
		{name: "zero int32 tokens", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": int32(0)}, wantErr: "positive"},
		{name: "zero int64 tokens", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": int64(0)}, wantErr: "positive"},
		{name: "wrong token type", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": 1.5}, wantErr: "unsupported type"},
		{name: "stream flag missing", body: validBody(), stream: true, wantErr: "streaming invariant"},
		{name: "stream flag false", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": 1, "stream": false}, stream: true, wantErr: "streaming invariant"},
		{name: "sync enables stream", body: map[string]interface{}{"model": "claude-test", "messages": true, "max_tokens": 1, "stream": true}, wantErr: "non-streaming"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := requestpolicy.NewDocument(requestpolicy.DocumentConfig{
				Info: requestpolicy.RequestInfo{
					Provider:      "anthropic",
					Surface:       "messages",
					ResolvedModel: "claude-test",
				},
				Body: test.body,
			})
			if err != nil {
				t.Fatalf("NewDocument returned error: %v", err)
			}
			draft := &anthropicDraft{
				Document: document, profile: directTestRequestProfile("claude-test"), stream: test.stream,
			}
			err = draft.Validate()
			if test.wantErr == "" && err != nil {
				t.Fatalf("Validate returned error: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("Validate error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}
