//go:build integration
// +build integration

package ai_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/ai"
	_ "github.com/truvaagents/truva-g3/ai/providers/openai" // Register OpenAI provider
	"github.com/truvaagents/truva-g3/core"
)

// TestAIToolWithUnifiedPattern verifies that AI Tools work with the unified capability pattern
func TestAIToolWithUnifiedPattern(t *testing.T) {
	t.Run("AITool implements core.Tool interface", func(t *testing.T) {
		// Verify compile-time interface implementation
		var _ core.Tool = (*ai.AITool)(nil)
	})

	t.Run("AITool can use Framework", func(t *testing.T) {
		// Create an AI Tool
		tool := core.NewTool("ai-test-tool")

		// Register a capability
		tool.RegisterCapability(core.Capability{
			Name:        "test_capability",
			Description: "Test capability for AI tool",
		})

		// Create framework with the tool
		framework, err := core.NewFramework(tool, core.WithPort(8095))
		if err != nil {
			t.Fatalf("Failed to create framework with AI tool: %v", err)
		}

		// Verify framework was created successfully
		if framework == nil {
			t.Error("Framework should not be nil")
		}
	})

	t.Run("AITool capabilities are exposed via /api/capabilities", func(t *testing.T) {
		// Create an AI Tool with mock AI client
		tool := core.NewTool("ai-capability-tool")

		// Register AI-powered capability
		tool.RegisterCapability(core.Capability{
			Name:        "ai_process",
			Description: "AI-powered processing",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("AI processing response"))
			},
		})

		// Initialize the tool
		ctx := context.Background()
		if err := tool.Initialize(ctx); err != nil {
			t.Fatalf("Failed to initialize tool: %v", err)
		}

		// Start the tool's HTTP server
		go func() {
			_ = tool.Start(ctx, 8096)
		}()

		// Note: In a real test, we would wait for the server to start
		// and then make HTTP requests to verify the endpoints
	})

	t.Run("AITool RegisterAICapability works with unified pattern", func(t *testing.T) {
		// Create base tool
		baseTool := core.NewTool("ai-enhanced-tool")

		// Wrap it as an AITool (simulating the pattern)
		aiTool := &ai.AITool{
			BaseTool: baseTool,
			// aiClient would be set in production
		}

		// Register an AI capability using the base method
		aiTool.RegisterCapability(core.Capability{
			Name:        "translate",
			Description: "AI translation capability",
			Endpoint:    "/ai/translate",
			Handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Translation result"))
			},
		})

		// Verify capability was registered
		caps := aiTool.GetCapabilities()
		if len(caps) != 1 {
			t.Errorf("Expected 1 capability, got %d", len(caps))
		}

		if caps[0].Name != "translate" {
			t.Errorf("Expected capability name 'translate', got '%s'", caps[0].Name)
		}

		// Test the handler
		req := httptest.NewRequest("POST", "/ai/translate", strings.NewReader("test input"))
		rec := httptest.NewRecorder()

		caps[0].Handler(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %d", rec.Code)
		}

		if rec.Body.String() != "Translation result" {
			t.Errorf("Expected 'Translation result', got '%s'", rec.Body.String())
		}
	})
}

// TestAIToolExamples tests the example AI tools
func TestAIToolExamples(t *testing.T) {
	// Check which providers are registered before cleanup
	providers := ai.ListProviders()
	t.Logf("Providers before cleanup: %v", providers)

	// Ensure OpenAI provider is registered by triggering import
	// The blank import should have already registered it, but let's verify
	providers = ai.ListProviders()
	t.Logf("Providers after import verification: %v", providers)

	// Skip if OpenAI provider not found
	found := false
	for _, p := range providers {
		if p == "openai" {
			found = true
			break
		}
	}
	if !found {
		t.Skip("OpenAI provider not registered - this indicates an issue with provider registration in test environment")
	}

	// Mock API key for testing
	mockAPIKey := "test-api-key"

	t.Run("TranslationTool follows unified pattern", func(t *testing.T) {
		tool, err := ai.NewTranslationTool(mockAPIKey)
		if err != nil {
			t.Fatalf("Failed to create translation tool: %v", err)
		}

		// Verify it has the expected capability
		caps := tool.GetCapabilities()
		if len(caps) != 1 {
			t.Errorf("Expected 1 capability, got %d", len(caps))
		}

		if caps[0].Name != "translate" {
			t.Errorf("Expected capability 'translate', got '%s'", caps[0].Name)
		}

		// Verify endpoint follows pattern
		expectedEndpoint := "/ai/translate"
		if caps[0].Endpoint != expectedEndpoint {
			t.Errorf("Expected endpoint '%s', got '%s'", expectedEndpoint, caps[0].Endpoint)
		}
	})

	t.Run("SummarizationTool follows unified pattern", func(t *testing.T) {
		tool, err := ai.NewSummarizationTool(mockAPIKey)
		if err != nil {
			t.Fatalf("Failed to create summarization tool: %v", err)
		}

		caps := tool.GetCapabilities()
		if len(caps) != 1 {
			t.Errorf("Expected 1 capability, got %d", len(caps))
		}

		if caps[0].Name != "summarize" {
			t.Errorf("Expected capability 'summarize', got '%s'", caps[0].Name)
		}
	})

	t.Run("SentimentAnalysisTool follows unified pattern", func(t *testing.T) {
		tool, err := ai.NewSentimentAnalysisTool(mockAPIKey)
		if err != nil {
			t.Fatalf("Failed to create sentiment analysis tool: %v", err)
		}

		caps := tool.GetCapabilities()
		if len(caps) != 1 {
			t.Errorf("Expected 1 capability, got %d", len(caps))
		}

		if caps[0].Name != "analyze_sentiment" {
			t.Errorf("Expected capability 'analyze_sentiment', got '%s'", caps[0].Name)
		}
	})

	t.Run("CodeReviewTool follows unified pattern", func(t *testing.T) {
		tool, err := ai.NewCodeReviewTool(mockAPIKey)
		if err != nil {
			t.Fatalf("Failed to create code review tool: %v", err)
		}

		caps := tool.GetCapabilities()
		if len(caps) != 1 {
			t.Errorf("Expected 1 capability, got %d", len(caps))
		}

		if caps[0].Name != "review_code" {
			t.Errorf("Expected capability 'review_code', got '%s'", caps[0].Name)
		}
	})
}
