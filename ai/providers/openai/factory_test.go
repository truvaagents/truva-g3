package openai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func TestFactory_Name(t *testing.T) {
	factory := &Factory{}
	if factory.Name() != "openai" {
		t.Errorf("expected name 'openai', got %q", factory.Name())
	}
}

func TestFactory_Description(t *testing.T) {
	factory := &Factory{}
	desc := factory.Description()
	if desc == "" {
		t.Error("expected non-empty description")
	}
	if desc != "Universal OpenAI-compatible provider (OpenAI, Groq, DeepSeek, Qwen, local models, etc.)" {
		t.Errorf("unexpected description: %q", desc)
	}
}

func TestFactoryInitializationLogDoesNotExposeEndpoint(t *testing.T) {
	const endpoint = "https://enterprise-endpoint-secret.example/v1"
	logger := &mockLogger{}
	factory := &Factory{}
	_, err := factory.CreateValidated(&ai.AIConfig{
		ProviderAlias: "openai",
		APIKey:        "test-key",
		BaseURL:       endpoint,
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("CreateValidated returned error: %v", err)
	}
	if len(logger.fields) == 0 {
		t.Fatal("provider initialization log was not captured")
	}
	fields := logger.fields[0]
	if _, exists := fields["base_url"]; exists {
		t.Fatalf("initialization fields contain base_url: %#v", fields)
	}
	if fields["custom_endpoint"] != true {
		t.Fatalf("custom_endpoint = %#v, want true", fields["custom_endpoint"])
	}
	if strings.Contains(fmt.Sprint(fields), "enterprise-endpoint-secret") {
		t.Fatalf("initialization fields leaked endpoint: %#v", fields)
	}
}

func TestFactoryPropagatesSSEEventLimit(t *testing.T) {
	created, err := (&Factory{}).CreateValidated(&ai.AIConfig{
		ProviderAlias:    "openai",
		APIKey:           "test-key",
		RetryDelay:       250 * time.Millisecond,
		SSEEventMaxBytes: 321,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := created.(*Client)
	if !ok {
		t.Fatalf("client type = %T", created)
	}
	if client.sseEventMaxBytes != 321 {
		t.Fatalf("SSE event limit = %d, want 321", client.sseEventMaxBytes)
	}
	if client.RetryDelay != 250*time.Millisecond {
		t.Fatalf("retry delay = %s, want 250ms", client.RetryDelay)
	}
}

func TestFactory_Priority(t *testing.T) {
	factory := &Factory{}
	// OpenAI factory should have default priority of 1000
	prio, _ := factory.DetectEnvironment()
	if prio != 0 && prio != 1000 {
		t.Errorf("expected priority 0 (no env) or 1000, got %d", prio)
	}
}

func TestFactory_DetectEnvironment(t *testing.T) {
	factory := &Factory{}

	tests := []struct {
		name      string
		setup     func()
		cleanup   func()
		wantPrio  int
		wantAvail bool
	}{
		{
			name: "with OPENAI_API_KEY",
			setup: func() {
				os.Setenv("OPENAI_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("OPENAI_API_KEY")
				os.Unsetenv("OPENAI_BASE_URL")
			},
			wantPrio:  1000,
			wantAvail: true,
		},
		{
			name: "with OPENROUTER_API_KEY",
			setup: func() {
				os.Setenv("OPENROUTER_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("OPENROUTER_API_KEY")
				os.Unsetenv("OPENROUTER_BASE_URL")
				os.Unsetenv("OPENAI_API_KEY")
			},
			wantPrio:  850,
			wantAvail: true,
		},
		{
			name: "with GROQ_API_KEY",
			setup: func() {
				os.Setenv("GROQ_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("GROQ_API_KEY")
				os.Unsetenv("OPENAI_BASE_URL")
				os.Unsetenv("OPENAI_API_KEY")
			},
			wantPrio:  700,
			wantAvail: true,
		},
		{
			name: "with DEEPSEEK_API_KEY",
			setup: func() {
				os.Setenv("DEEPSEEK_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("DEEPSEEK_API_KEY")
				os.Unsetenv("OPENAI_BASE_URL")
				os.Unsetenv("OPENAI_API_KEY")
			},
			wantPrio:  600,
			wantAvail: true,
		},
		{
			name: "with XAI_API_KEY",
			setup: func() {
				os.Setenv("XAI_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("XAI_API_KEY")
				os.Unsetenv("OPENAI_BASE_URL")
				os.Unsetenv("OPENAI_API_KEY")
			},
			wantPrio:  500,
			wantAvail: true,
		},
		{
			name: "with QWEN_API_KEY",
			setup: func() {
				os.Setenv("QWEN_API_KEY", "test-key")
			},
			cleanup: func() {
				os.Unsetenv("QWEN_API_KEY")
				os.Unsetenv("OPENAI_BASE_URL")
				os.Unsetenv("OPENAI_API_KEY")
			},
			wantPrio:  400,
			wantAvail: true,
		},
		{
			name:      "no environment variables",
			setup:     func() {},
			cleanup:   func() {},
			wantPrio:  0,
			wantAvail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env vars first
			os.Unsetenv("OPENAI_API_KEY")
			os.Unsetenv("OPENAI_BASE_URL")
			os.Unsetenv("OPENROUTER_API_KEY")
			os.Unsetenv("OPENROUTER_BASE_URL")
			os.Unsetenv("GROQ_API_KEY")
			os.Unsetenv("DEEPSEEK_API_KEY")
			os.Unsetenv("XAI_API_KEY")
			os.Unsetenv("MISTRAL_API_KEY")
			os.Unsetenv("QWEN_API_KEY")

			tt.setup()
			defer tt.cleanup()

			prio, avail := factory.DetectEnvironment()

			if prio != tt.wantPrio {
				t.Errorf("expected priority %d, got %d", tt.wantPrio, prio)
			}
			if avail != tt.wantAvail {
				t.Errorf("expected available %v, got %v", tt.wantAvail, avail)
			}
		})
	}
}

func TestFactory_DetectAvailableAliases(t *testing.T) {
	factory := &Factory{}

	t.Run("multiple env vars set", func(t *testing.T) {
		// Clear all first
		os.Unsetenv("OPENAI_API_KEY")
		os.Unsetenv("OPENROUTER_API_KEY")
		os.Unsetenv("GROQ_API_KEY")
		os.Unsetenv("DEEPSEEK_API_KEY")
		os.Unsetenv("XAI_API_KEY")
		os.Unsetenv("MISTRAL_API_KEY")
		os.Unsetenv("QWEN_API_KEY")
		os.Unsetenv("TOGETHER_API_KEY")

		os.Setenv("GROQ_API_KEY", "test-groq")
		os.Setenv("DEEPSEEK_API_KEY", "test-deepseek")
		defer func() {
			os.Unsetenv("GROQ_API_KEY")
			os.Unsetenv("DEEPSEEK_API_KEY")
		}()

		aliases := factory.DetectAvailableAliases()

		if len(aliases) != 2 {
			t.Fatalf("Expected 2 aliases, got %d: %+v", len(aliases), aliases)
		}

		// Groq should be first (priority 700 > DeepSeek 600)
		if aliases[0].Alias != "openai.groq" {
			t.Errorf("Expected first alias 'openai.groq', got %q", aliases[0].Alias)
		}
		if aliases[0].Priority != 700 {
			t.Errorf("Expected Groq priority 700, got %d", aliases[0].Priority)
		}
		if aliases[1].Alias != "openai.deepseek" {
			t.Errorf("Expected second alias 'openai.deepseek', got %q", aliases[1].Alias)
		}
	})

	t.Run("no env vars set", func(t *testing.T) {
		os.Unsetenv("OPENAI_API_KEY")
		os.Unsetenv("GROQ_API_KEY")
		os.Unsetenv("DEEPSEEK_API_KEY")
		os.Unsetenv("XAI_API_KEY")
		os.Unsetenv("MISTRAL_API_KEY")
		os.Unsetenv("QWEN_API_KEY")
		os.Unsetenv("TOGETHER_API_KEY")

		aliases := factory.DetectAvailableAliases()
		// May be non-empty if Ollama is running locally, but should not contain API-key-based providers
		for _, a := range aliases {
			if a.Alias != "openai.ollama" {
				t.Errorf("Unexpected alias without env vars: %q", a.Alias)
			}
		}
	})

	t.Run("all providers have correct provider name", func(t *testing.T) {
		os.Setenv("OPENAI_API_KEY", "test")
		os.Setenv("GROQ_API_KEY", "test")
		defer func() {
			os.Unsetenv("OPENAI_API_KEY")
			os.Unsetenv("GROQ_API_KEY")
		}()

		aliases := factory.DetectAvailableAliases()
		for _, a := range aliases {
			if a.ProviderName != "openai" {
				t.Errorf("Alias %q has ProviderName %q, expected 'openai'", a.Alias, a.ProviderName)
			}
		}
	})
}

func TestFactoryOpenRouterConfigurationPrecedenceAndValidation(t *testing.T) {
	factory := &Factory{}
	t.Setenv("OPENROUTER_API_KEY", "environment-key")
	t.Setenv("OPENROUTER_BASE_URL", "https://environment.openrouter.example/v1")

	created, err := factory.CreateValidated(&ai.AIConfig{
		ProviderAlias: "openai.openrouter",
		APIKey:        "explicit-key",
		BaseURL:       "https://explicit.openrouter.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	client := created.(*Client)
	if client.apiKey != "explicit-key" || client.baseURL != "https://explicit.openrouter.example/v1" {
		t.Fatalf("explicit OpenRouter config lost precedence: key=%q base=%q", client.apiKey, client.baseURL)
	}

	created, err = factory.CreateValidated(&ai.AIConfig{ProviderAlias: "openai.openrouter"})
	if err != nil {
		t.Fatal(err)
	}
	client = created.(*Client)
	if client.apiKey != "environment-key" || client.baseURL != "https://environment.openrouter.example/v1" {
		t.Fatalf("OpenRouter environment config not resolved: key=%q base=%q", client.apiKey, client.baseURL)
	}

	t.Setenv("OPENROUTER_BASE_URL", "://malformed")
	if _, err := factory.CreateValidated(&ai.AIConfig{ProviderAlias: "openai.openrouter"}); err == nil {
		t.Fatal("malformed OpenRouter environment base URL must fail construction")
	}
	if _, err := factory.CreateValidated(&ai.AIConfig{
		ProviderAlias: "openai.openrouter",
		APIKey:        "explicit-key",
		BaseURL:       "https://valid.example/v1?credential=forbidden",
	}); err == nil {
		t.Fatal("malformed explicit OpenRouter base URL must fail construction")
	}
}

func TestFactoryOpenRouterDefaultsAndOptionalDetection(t *testing.T) {
	factory := &Factory{}
	for _, key := range []string{
		"OPENAI_API_KEY", "GROQ_API_KEY", "DEEPSEEK_API_KEY", "XAI_API_KEY",
		"MISTRAL_API_KEY", "QWEN_API_KEY", "TOGETHER_API_KEY", "OPENROUTER_API_KEY",
		"OLLAMA_BASE_URL",
	} {
		t.Setenv(key, "")
	}
	created, err := factory.CreateValidated(&ai.AIConfig{
		ProviderAlias: "openai.openrouter",
		APIKey:        "explicit-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := created.(*Client).baseURL; got != "https://openrouter.ai/api/v1" {
		t.Fatalf("OpenRouter default base URL = %q", got)
	}
	for _, availability := range factory.DetectAvailableAliases() {
		if availability.Alias == "openai.openrouter" {
			t.Fatal("absent OpenRouter key must leave optional provider unavailable")
		}
	}

	t.Setenv("OPENROUTER_API_KEY", "test-key")
	aliases := factory.DetectAvailableAliases()
	count := 0
	for _, availability := range aliases {
		if availability.Alias == "openai.openrouter" {
			count++
			if availability.Priority != 850 || availability.ProviderName != "openai" {
				t.Fatalf("OpenRouter availability = %#v", availability)
			}
		}
	}
	if count != 1 {
		t.Fatalf("OpenRouter enumerated %d times, want once", count)
	}
}

func TestFactoryOpenRouterConcurrentConstructionIsCallLocal(t *testing.T) {
	factory := &Factory{}
	const clients = 24
	results := make(chan *Client, clients)
	errorsFound := make(chan error, clients)
	for index := 0; index < clients; index++ {
		index := index
		go func() {
			created, err := factory.CreateValidated(&ai.AIConfig{
				ProviderAlias: "openai.openrouter",
				APIKey:        fmt.Sprintf("key-%d", index),
				BaseURL:       fmt.Sprintf("https://router-%d.example/v1", index),
			})
			if err != nil {
				errorsFound <- err
				return
			}
			results <- created.(*Client)
		}()
	}
	seen := make(map[string]string, clients)
	for completed := 0; completed < clients; completed++ {
		select {
		case err := <-errorsFound:
			t.Fatal(err)
		case client := <-results:
			seen[client.apiKey] = client.baseURL
		}
	}
	for index := 0; index < clients; index++ {
		key := fmt.Sprintf("key-%d", index)
		if got := seen[key]; got != fmt.Sprintf("https://router-%d.example/v1", index) {
			t.Fatalf("client %q base URL = %q", key, got)
		}
	}
}

func TestFactory_Create(t *testing.T) {
	factory := &Factory{}

	tests := []struct {
		name   string
		config *ai.AIConfig
		verify func(*testing.T, *Client)
	}{
		{
			name: "with API key from config",
			config: &ai.AIConfig{
				APIKey: "config-key",
			},
			verify: func(t *testing.T, c *Client) {
				if c.apiKey != "config-key" {
					t.Errorf("expected API key 'config-key', got %q", c.apiKey)
				}
			},
		},
		{
			name: "with base URL from config",
			config: &ai.AIConfig{
				BaseURL: "https://custom.api.com/v1",
			},
			verify: func(t *testing.T, c *Client) {
				if c.baseURL != "https://custom.api.com/v1" {
					t.Errorf("expected base URL 'https://custom.api.com/v1', got %q", c.baseURL)
				}
			},
		},
		{
			name: "with timeout configuration",
			config: &ai.AIConfig{
				Timeout: 60 * time.Second,
			},
			verify: func(t *testing.T, c *Client) {
				if c.HTTPClient.Timeout != 60*time.Second {
					t.Errorf("expected timeout 60s, got %v", c.HTTPClient.Timeout)
				}
			},
		},
		{
			name: "with retry configuration",
			config: &ai.AIConfig{
				MaxRetries: 5,
			},
			verify: func(t *testing.T, c *Client) {
				if c.MaxRetries != 5 {
					t.Errorf("expected MaxRetries 5, got %d", c.MaxRetries)
				}
			},
		},
		{
			name: "with model configuration",
			config: &ai.AIConfig{
				Model: "gpt-4-turbo",
			},
			verify: func(t *testing.T, c *Client) {
				if c.DefaultModel != "gpt-4-turbo" {
					t.Errorf("expected model 'gpt-4-turbo', got %q", c.DefaultModel)
				}
			},
		},
		{
			name: "with temperature configuration",
			config: &ai.AIConfig{
				Temperature: 0.8,
			},
			verify: func(t *testing.T, c *Client) {
				if c.DefaultTemperature != 0.8 {
					t.Errorf("expected temperature 0.8, got %f", c.DefaultTemperature)
				}
			},
		},
		{
			name: "with max tokens configuration",
			config: &ai.AIConfig{
				MaxTokens: 2000,
			},
			verify: func(t *testing.T, c *Client) {
				if c.DefaultMaxTokens != 2000 {
					t.Errorf("expected max tokens 2000, got %d", c.DefaultMaxTokens)
				}
			},
		},
		{
			name:   "with API key from environment",
			config: &ai.AIConfig{},
			verify: func(t *testing.T, c *Client) {
				// Set env var for this test
				os.Setenv("OPENAI_API_KEY", "env-key")
				defer os.Unsetenv("OPENAI_API_KEY")

				// Recreate client to pick up env var
				newClient := factory.Create(&ai.AIConfig{})
				if openaiClient, ok := newClient.(*Client); ok {
					if openaiClient.apiKey != "env-key" {
						t.Errorf("expected API key from env 'env-key', got %q", openaiClient.apiKey)
					}
				}
			},
		},
		{
			name:   "with base URL from environment",
			config: &ai.AIConfig{},
			verify: func(t *testing.T, c *Client) {
				// Set env var for this test
				os.Setenv("OPENAI_BASE_URL", "https://env.api.com/v1")
				defer os.Unsetenv("OPENAI_BASE_URL")

				// Recreate client to pick up env var
				newClient := factory.Create(&ai.AIConfig{})
				if openaiClient, ok := newClient.(*Client); ok {
					if openaiClient.baseURL != "https://env.api.com/v1" {
						t.Errorf("expected base URL from env, got %q", openaiClient.baseURL)
					}
				}
			},
		},
		{
			// Note: this test passes &ai.AIConfig{} directly to the factory,
			// bypassing ai.NewClient. With the precedence chain in NewClient,
			// MaxRetries is normally resolved before reaching the factory; the
			// zero value here means "operator chose 0", which the factory now
			// honors via the >= 0 check. To assert the framework default of 3,
			// the test must either set MaxRetries: 3 explicitly or go through
			// ai.NewClient.
			name:   "default configuration",
			config: &ai.AIConfig{MaxRetries: 3},
			verify: func(t *testing.T, c *Client) {
				// DefaultModel is now "default" alias which gets resolved at request-time
				// This enables runtime model override via TRUVAG3_OPENAI_MODEL_DEFAULT env var
				if c.DefaultModel != "default" {
					t.Errorf("expected default model 'default' (alias), got %q", c.DefaultModel)
				}
				if c.DefaultMaxTokens != 1000 {
					t.Errorf("expected default max tokens 1000, got %d", c.DefaultMaxTokens)
				}
				if c.MaxRetries != 3 {
					t.Errorf("expected default max retries 3, got %d", c.MaxRetries)
				}
			},
		},
		{
			// Pins the >= 0 factory check that allows operators to disable
			// per-provider retries entirely (e.g. for cost-sensitive
			// deployments that avoid provider-local retries before chain failover
			// and per-provider retries just amplify token waste). Before
			// the >= 0 change, the factory used > 0 and silently fell back
			// to BaseClient's hard-coded default of 3, ignoring the explicit 0.
			name:   "explicit zero retries is honored",
			config: &ai.AIConfig{MaxRetries: 0},
			verify: func(t *testing.T, c *Client) {
				if c.MaxRetries != 0 {
					t.Errorf("explicit MaxRetries=0 must be honored as 'no retries', got %d", c.MaxRetries)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := factory.Create(tt.config)

			if client == nil {
				t.Fatal("expected non-nil client")
			}

			// Type assert to access internal fields for verification
			openaiClient, ok := client.(*Client)
			if !ok {
				t.Fatal("expected *Client type")
			}

			tt.verify(t, openaiClient)
		})
	}
}

func TestFactory_CreateWithHeaders(t *testing.T) {
	var customHeader string
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		customHeader = request.Header.Get("X-Custom-Header")
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"model":"gpt-4.1","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	factory := &Factory{}
	config := &ai.AIConfig{
		APIKey:  "framework-key",
		BaseURL: server.URL,
		Model:   "gpt-4.1",
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
			"Authorization":   "Bearer custom-token",
		},
	}
	client := factory.Create(config)
	_, err := client.GenerateResponse(t.Context(), "hello", &core.AIOptions{MaxTokens: 10})
	if err != nil {
		t.Fatalf("GenerateResponse returned error: %v", err)
	}
	if customHeader != "custom-value" {
		t.Fatalf("X-Custom-Header = %q", customHeader)
	}
	if authorization != "Bearer framework-key" {
		t.Fatalf("Authorization = %q, want provider-managed credential", authorization)
	}
}
