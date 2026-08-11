package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRedactSensitiveText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "assignment", input: "request failed: api_key=top-secret status=401", want: "request failed: api_key=[REDACTED] status=401"},
		{name: "quoted assignment", input: `api-key: "top secret"`, want: `api-key: "[REDACTED]"`},
		{name: "json", input: `{"error":"denied","api_key":"top-secret"}`, want: `{"error":"denied","api_key":"[REDACTED]"}`},
		{name: "url query", input: "https://api.example.test/data?appid=top-secret&units=metric", want: "https://api.example.test/data?appid=[REDACTED]&units=metric"},
		{name: "url generic key", input: "https://api.example.test/data?key=top-secret", want: "https://api.example.test/data?key=[REDACTED]"},
		{name: "authorization", input: "Authorization: Bearer top-secret", want: "Authorization: [REDACTED]"},
		{name: "standalone bearer", input: "upstream sent Bearer top-secret", want: "upstream sent Bearer [REDACTED]"},
		{name: "url user info", input: "redis://operator:top-secret@redis.example.test:6379", want: "redis://[REDACTED]@redis.example.test:6379"},
		{name: "ordinary prose", input: "the cache key is stable and the token count is 42", want: "the cache key is stable and the token count is 42"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactSensitiveText(test.input); got != test.want {
				t.Fatalf("RedactSensitiveText() = %q, want %q", got, test.want)
			}
			if strings.Contains(RedactSensitiveText(test.input), "top-secret") || strings.Contains(RedactSensitiveText(test.input), "top secret") {
				t.Fatal("redacted text retained the credential")
			}
		})
	}
}

func TestRedactSensitiveTextPreservesJSON(t *testing.T) {
	t.Parallel()

	redacted := RedactSensitiveText(`{"error":"provider rejected api_key=top-secret","api_key":"another-secret"}`)
	var decoded map[string]string
	if err := json.Unmarshal([]byte(redacted), &decoded); err != nil {
		t.Fatalf("redacted JSON is invalid: %v", err)
	}
	if decoded["api_key"] != redactedSensitiveValue || strings.Contains(decoded["error"], "top-secret") {
		t.Fatalf("credential was not redacted: %#v", decoded)
	}
}

func TestRedactSensitiveErrorPreservesCauseAndSanitizesMessage(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial redis://operator:top-secret@redis.example.test:6379: connection refused")
	redacted := RedactSensitiveError(cause)
	if redacted == nil {
		t.Fatal("RedactSensitiveError() returned nil")
	}
	if strings.Contains(redacted.Error(), "top-secret") {
		t.Fatalf("redacted error retained the credential: %q", redacted.Error())
	}
	if !strings.Contains(redacted.Error(), "redis://[REDACTED]@redis.example.test:6379") {
		t.Fatalf("redacted error lost useful diagnostic context: %q", redacted.Error())
	}
	if !errors.Is(redacted, cause) {
		t.Fatal("redacted error does not preserve the original cause")
	}
	if RedactSensitiveError(nil) != nil {
		t.Fatal("RedactSensitiveError(nil) must return nil")
	}
}
