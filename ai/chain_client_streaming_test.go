package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

// ================================
// Streaming Unit Tests for Chain Client
// ================================

// streamingMockAIClient implements both core.AIClient and core.StreamingAIClient
type streamingMockAIClient struct {
	name              string
	shouldFail        bool
	failWith          error
	failAfterChunks   int // If > 0, fail after this many chunks (partial completion)
	supportsStreaming bool
	callCount         int
	streamCallCount   int
	response          string
}

func (m *streamingMockAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	m.callCount++
	if m.shouldFail {
		return nil, m.failWith
	}
	return &core.AIResponse{
		Content: m.response,
		Model:   m.name,
	}, nil
}

func (m *streamingMockAIClient) StreamResponse(ctx context.Context, prompt string, opts *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	m.streamCallCount++
	if m.shouldFail {
		return nil, m.failWith
	}

	content := m.response
	if content == "" {
		content = "Mock streaming response from " + m.name
	}

	chunkSize := 10
	var fullContent strings.Builder
	chunkIndex := 0

	for i := 0; i < len(content); i += chunkSize {
		// Check for partial completion simulation
		if m.failAfterChunks > 0 && chunkIndex >= m.failAfterChunks {
			return &core.AIResponse{
				Content: fullContent.String(),
				Model:   m.name,
			}, core.ErrStreamPartiallyCompleted
		}

		end := i + chunkSize
		if end > len(content) {
			end = len(content)
		}

		chunk := core.StreamChunk{
			Content: content[i:end],
			Delta:   true,
			Index:   chunkIndex,
			Model:   m.name,
		}
		fullContent.WriteString(content[i:end])
		chunkIndex++

		if err := callback(chunk); err != nil {
			return &core.AIResponse{
				Content: fullContent.String(),
				Model:   m.name,
			}, nil
		}
	}

	// Send final chunk
	finalChunk := core.StreamChunk{
		Delta:        false,
		Index:        chunkIndex,
		FinishReason: "stop",
		Model:        m.name,
	}
	_ = callback(finalChunk)

	return &core.AIResponse{
		Content: fullContent.String(),
		Model:   m.name,
	}, nil
}

func (m *streamingMockAIClient) SupportsStreaming() bool {
	return m.supportsStreaming
}

// TestChainClient_StreamResponse_FirstProviderSucceeds tests normal success case
func TestChainClient_StreamResponse_FirstProviderSucceeds(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		response:          "Response from provider 1",
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		response:          "Response from provider 2",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	var chunks []core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Verify first provider was used
	if provider1.streamCallCount != 1 {
		t.Errorf("Expected provider1 to be called once, got %d", provider1.streamCallCount)
	}
	if provider2.streamCallCount != 0 {
		t.Errorf("Expected provider2 to not be called, got %d", provider2.streamCallCount)
	}

	// Verify response
	if resp.Content != "Response from provider 1" {
		t.Errorf("Expected content from provider1, got %q", resp.Content)
	}

	// Verify chunks were received
	if len(chunks) == 0 {
		t.Error("Expected chunks to be delivered")
	}
}

// TestChainClient_StreamResponse_FailoverToSecond tests failover before streaming starts
func TestChainClient_StreamResponse_FailoverToSecond(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		shouldFail:        true,
		failWith:          errors.New("connection failed"),
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		response:          "Response from provider 2",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Both providers should have been tried
	if provider1.streamCallCount != 1 {
		t.Errorf("Expected provider1 to be called once, got %d", provider1.streamCallCount)
	}
	if provider2.streamCallCount != 1 {
		t.Errorf("Expected provider2 to be called once, got %d", provider2.streamCallCount)
	}

	// Response should be from provider2
	if resp.Content != "Response from provider 2" {
		t.Errorf("Expected content from provider2, got %q", resp.Content)
	}
}

// TestChainClient_StreamResponse_PartialCompletionNoFailover tests that partial completion doesn't failover
func TestChainClient_StreamResponse_PartialCompletionNoFailover(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		response:          "This is a long response that will be cut short",
		failAfterChunks:   2, // Fail after 2 chunks
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		response:          "Response from provider 2",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	var chunks []core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)

	// Should return partial response with ErrStreamPartiallyCompleted
	if err != core.ErrStreamPartiallyCompleted {
		t.Errorf("Expected ErrStreamPartiallyCompleted, got %v", err)
	}

	// Should NOT failover to second provider (streaming already started)
	if provider2.streamCallCount != 0 {
		t.Errorf("Expected provider2 to NOT be called after partial completion, got %d", provider2.streamCallCount)
	}

	// Response should have partial content
	if resp == nil {
		t.Fatal("Expected response with partial content")
	}
	if resp.Content == "" {
		t.Error("Expected partial content in response")
	}
}

// TestChainClient_StreamResponse_SkipsNonStreamingProvider tests that non-streaming providers are skipped
func TestChainClient_StreamResponse_SkipsNonStreamingProvider(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: false, // Doesn't support streaming
		response:          "Response from provider 1",
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		response:          "Response from provider 2",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Provider1 should be skipped (doesn't support streaming)
	if provider1.streamCallCount != 0 {
		t.Errorf("Expected provider1 (non-streaming) to be skipped, got %d calls", provider1.streamCallCount)
	}

	// Provider2 should be used
	if provider2.streamCallCount != 1 {
		t.Errorf("Expected provider2 to be called once, got %d", provider2.streamCallCount)
	}

	// Response should be from provider2
	if resp.Content != "Response from provider 2" {
		t.Errorf("Expected content from provider2, got %q", resp.Content)
	}
}

// TestChainClient_StreamResponse_AllProvidersFail tests when all providers fail
func TestChainClient_StreamResponse_AllProvidersFail(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		shouldFail:        true,
		failWith:          errors.New("provider1 failed"),
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		shouldFail:        true,
		failWith:          errors.New("provider2 failed"),
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)

	// Should fail
	if err == nil {
		t.Error("Expected error when all providers fail")
	}
	if resp != nil {
		t.Error("Expected nil response when all providers fail")
	}

	// All providers should have been tried
	if provider1.streamCallCount != 1 {
		t.Errorf("Expected provider1 to be called once, got %d", provider1.streamCallCount)
	}
	if provider2.streamCallCount != 1 {
		t.Errorf("Expected provider2 to be called once, got %d", provider2.streamCallCount)
	}

	// Error should mention all providers failed
	if !strings.Contains(err.Error(), "all") {
		t.Errorf("Expected error to mention all providers failed, got: %v", err)
	}
}

// TestChainClient_StreamResponse_NoStreamingProviders tests when no providers support streaming
func TestChainClient_StreamResponse_NoStreamingProviders(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: false,
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: false,
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	_, err := client.StreamResponse(context.Background(), "test", nil, callback)

	// Should fail - no streaming providers
	if err == nil {
		t.Error("Expected error when no providers support streaming")
	}
}

// TestChainClient_SupportsStreaming_WithStreamingProvider tests SupportsStreaming returns true
func TestChainClient_SupportsStreaming_WithStreamingProvider(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: false,
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
	}

	if !client.SupportsStreaming() {
		t.Error("Expected SupportsStreaming() to return true when at least one provider supports streaming")
	}
}

// TestChainClient_SupportsStreaming_NoStreamingProviders tests SupportsStreaming returns false
func TestChainClient_SupportsStreaming_NoStreamingProviders(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: false,
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: false,
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
	}

	if client.SupportsStreaming() {
		t.Error("Expected SupportsStreaming() to return false when no providers support streaming")
	}
}

// TestChainClient_StreamResponse_NilTelemetry verifies nil telemetry doesn't panic
func TestChainClient_StreamResponse_NilTelemetry(t *testing.T) {
	provider := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		response:          "Test response",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider},
		providerAliases: []string{"provider1"},
		logger:          &core.NoOpLogger{},
		telemetry:       nil, // Explicitly nil
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	// Should not panic
	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}
}

// TestChainClient_StreamResponse_OptionsCloned verifies options are cloned per provider
func TestChainClient_StreamResponse_OptionsCloned(t *testing.T) {
	provider1 := &streamingMockAIClient{
		name:              "provider1",
		supportsStreaming: true,
		shouldFail:        true,
		failWith:          errors.New("first provider failed"),
	}
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
		response:          "Response from provider 2",
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
		telemetry:       &core.NoOpTelemetry{},
	}

	originalOptions := &core.AIOptions{
		Model:       "smart",
		Temperature: 0.7,
	}

	callback := func(chunk core.StreamChunk) error { return nil }

	_, err := client.StreamResponse(context.Background(), "test", originalOptions, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Original options should be unchanged
	if originalOptions.Model != "smart" {
		t.Errorf("Original Model was mutated: expected 'smart', got %q", originalOptions.Model)
	}
	if originalOptions.Temperature != 0.7 {
		t.Errorf("Original Temperature was mutated: expected 0.7, got %v", originalOptions.Temperature)
	}
}

// nonStreamingAIClient implements only core.AIClient (no streaming)
type nonStreamingAIClient struct {
	name     string
	response string
}

func (m *nonStreamingAIClient) GenerateResponse(ctx context.Context, prompt string, opts *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{
		Content: m.response,
		Model:   m.name,
	}, nil
}

// TestChainClient_SupportsStreaming_MixedClients tests with mixed client types
func TestChainClient_SupportsStreaming_MixedClients(t *testing.T) {
	provider1 := &nonStreamingAIClient{name: "provider1"} // Does NOT implement StreamingAIClient
	provider2 := &streamingMockAIClient{
		name:              "provider2",
		supportsStreaming: true,
	}

	client := &ChainClient{
		providers:       []core.AIClient{provider1, provider2},
		providerAliases: []string{"provider1", "provider2"},
		logger:          &core.NoOpLogger{},
	}

	// Should return true because provider2 supports streaming
	if !client.SupportsStreaming() {
		t.Error("Expected SupportsStreaming() to return true with mixed clients")
	}
}
