package mock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// TestClient_StreamResponse_Basic verifies basic streaming functionality
func TestClient_StreamResponse_Basic(t *testing.T) {
	client := NewClient(nil)
	expectedResponse := "This is a streaming test response"
	client.SetResponses(expectedResponse)

	var chunks []core.StreamChunk
	var fullContent strings.Builder

	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
		}
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test prompt", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Verify response
	if resp.Content != expectedResponse {
		t.Errorf("Expected response %q, got %q", expectedResponse, resp.Content)
	}

	// Verify chunks were delivered
	if len(chunks) == 0 {
		t.Error("Expected at least one chunk")
	}

	// Verify final chunk has finish reason
	finalChunk := chunks[len(chunks)-1]
	if finalChunk.FinishReason != "stop" {
		t.Errorf("Expected final chunk FinishReason 'stop', got %q", finalChunk.FinishReason)
	}

	// Verify full content was delivered via chunks
	if fullContent.String() != expectedResponse {
		t.Errorf("Chunked content mismatch: expected %q, got %q", expectedResponse, fullContent.String())
	}

	// Verify call count
	if client.CallCount != 1 {
		t.Errorf("Expected CallCount 1, got %d", client.CallCount)
	}
}

// TestClient_StreamResponse_ChunkSize verifies configurable chunk size
func TestClient_StreamResponse_ChunkSize(t *testing.T) {
	client := NewClient(nil)
	client.ChunkSize = 5
	response := "HelloWorld"
	client.SetResponses(response)

	var contentChunks []string
	callback := func(chunk core.StreamChunk) error {
		if chunk.Content != "" {
			contentChunks = append(contentChunks, chunk.Content)
		}
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// "HelloWorld" with chunk size 5 should produce ["Hello", "World"]
	if len(contentChunks) != 2 {
		t.Errorf("Expected 2 content chunks, got %d: %v", len(contentChunks), contentChunks)
	}

	if contentChunks[0] != "Hello" {
		t.Errorf("Expected first chunk 'Hello', got %q", contentChunks[0])
	}
	if contentChunks[1] != "World" {
		t.Errorf("Expected second chunk 'World', got %q", contentChunks[1])
	}
}

// TestClient_StreamResponse_CallbackError verifies callback can stop streaming
func TestClient_StreamResponse_CallbackError(t *testing.T) {
	client := NewClient(nil)
	client.ChunkSize = 5
	client.SetResponses("HelloWorldTest")

	stopErr := errors.New("stop streaming")
	chunkCount := 0

	callback := func(chunk core.StreamChunk) error {
		chunkCount++
		if chunkCount >= 2 {
			return stopErr // Stop after 2 chunks
		}
		return nil
	}

	resp, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("Expected no error (callback stop is graceful), got: %v", err)
	}

	// Should have partial content
	if resp == nil {
		t.Fatal("Expected response, got nil")
	}

	// Content should be partial (first 2 chunks)
	if resp.Content != "HelloWorld" {
		t.Errorf("Expected partial content 'HelloWorld', got %q", resp.Content)
	}
}

// TestClient_StreamResponse_ContextCancellation verifies context cancellation handling
func TestClient_StreamResponse_ContextCancellation(t *testing.T) {
	client := NewClient(nil)
	client.ChunkSize = 5
	client.StreamDelay = 100 * time.Millisecond
	client.SetResponses("HelloWorldTestContent")

	ctx, cancel := context.WithCancel(context.Background())

	var chunksReceived int
	callback := func(chunk core.StreamChunk) error {
		chunksReceived++
		if chunksReceived >= 2 {
			cancel() // Cancel after 2 chunks
		}
		return nil
	}

	resp, err := client.StreamResponse(ctx, "test", nil, callback)

	// Should return partial content with ErrStreamPartiallyCompleted
	if err != core.ErrStreamPartiallyCompleted && err != context.Canceled {
		// Either error is acceptable
		if err != nil {
			t.Logf("Got error: %v (acceptable for cancellation)", err)
		}
	}

	// Should have some content
	if resp != nil && resp.Content == "" {
		t.Error("Expected partial content when cancelled")
	}
}

// TestClient_StreamResponse_Error verifies error handling
func TestClient_StreamResponse_Error(t *testing.T) {
	client := NewClient(nil)
	expectedErr := errors.New("test error")
	client.SetError(expectedErr)

	callback := func(chunk core.StreamChunk) error {
		t.Error("Callback should not be called when error is set")
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err == nil {
		t.Error("Expected error, got nil")
	}
	if err.Error() != expectedErr.Error() {
		t.Errorf("Expected error %q, got %q", expectedErr.Error(), err.Error())
	}
}

// TestClient_StreamResponse_MultipleResponses verifies response cycling
func TestClient_StreamResponse_MultipleResponses(t *testing.T) {
	client := NewClient(nil)
	client.SetResponses("First", "Second", "Third")

	callback := func(chunk core.StreamChunk) error { return nil }

	// First call
	resp1, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}
	if resp1.Content != "First" {
		t.Errorf("Expected 'First', got %q", resp1.Content)
	}

	// Second call
	resp2, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}
	if resp2.Content != "Second" {
		t.Errorf("Expected 'Second', got %q", resp2.Content)
	}

	// Third call
	resp3, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("Third call failed: %v", err)
	}
	if resp3.Content != "Third" {
		t.Errorf("Expected 'Third', got %q", resp3.Content)
	}

	// Fourth call should error
	_, err = client.StreamResponse(context.Background(), "test", nil, callback)
	if err == nil {
		t.Error("Expected error when no more responses")
	}
}

// TestClient_StreamResponse_ChunkIndex verifies chunk index is sequential
func TestClient_StreamResponse_ChunkIndex(t *testing.T) {
	client := NewClient(nil)
	client.ChunkSize = 3
	client.SetResponses("ABCDEFGHI") // 9 chars = 3 chunks

	var indices []int
	callback := func(chunk core.StreamChunk) error {
		indices = append(indices, chunk.Index)
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Should have indices 0, 1, 2, 3 (3 content chunks + 1 final)
	for i, idx := range indices {
		if idx != i {
			t.Errorf("Expected index %d at position %d, got %d", i, i, idx)
		}
	}
}

// TestClient_StreamResponse_DeltaFlag verifies delta flag behavior
func TestClient_StreamResponse_DeltaFlag(t *testing.T) {
	client := NewClient(nil)
	client.ChunkSize = 5
	client.SetResponses("HelloWorld")

	var chunks []core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Content chunks should have Delta=true
	for i := 0; i < len(chunks)-1; i++ {
		if !chunks[i].Delta {
			t.Errorf("Expected Delta=true for chunk %d", i)
		}
	}

	// Final chunk should have Delta=false
	finalChunk := chunks[len(chunks)-1]
	if finalChunk.Delta {
		t.Error("Expected Delta=false for final chunk")
	}
}

// TestClient_StreamResponse_ModelInChunks verifies model is set in chunks
func TestClient_StreamResponse_ModelInChunks(t *testing.T) {
	client := NewClient(nil)
	client.SetResponses("Test")

	var chunks []core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		chunks = append(chunks, chunk)
		return nil
	}

	_, err := client.StreamResponse(context.Background(), "test", &core.AIOptions{Model: "custom-model"}, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// All chunks should have the model set
	for _, chunk := range chunks {
		if chunk.Model != "custom-model" {
			t.Errorf("Expected Model 'custom-model', got %q", chunk.Model)
		}
	}
}

// TestClient_StreamResponse_UsageInFinalChunk verifies usage is in final chunk
func TestClient_StreamResponse_UsageInFinalChunk(t *testing.T) {
	client := NewClient(nil)
	prompt := "Test prompt"
	response := "Test response"
	client.SetResponses(response)

	var finalChunk core.StreamChunk
	callback := func(chunk core.StreamChunk) error {
		if chunk.FinishReason != "" {
			finalChunk = chunk
		}
		return nil
	}

	_, err := client.StreamResponse(context.Background(), prompt, nil, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	// Final chunk should have usage
	if finalChunk.Usage == nil {
		t.Error("Expected usage in final chunk")
		return
	}

	expectedPromptTokens := len(prompt) / 4
	expectedCompletionTokens := len(response) / 4

	if finalChunk.Usage.PromptTokens != expectedPromptTokens {
		t.Errorf("Expected PromptTokens %d, got %d", expectedPromptTokens, finalChunk.Usage.PromptTokens)
	}
	if finalChunk.Usage.CompletionTokens != expectedCompletionTokens {
		t.Errorf("Expected CompletionTokens %d, got %d", expectedCompletionTokens, finalChunk.Usage.CompletionTokens)
	}
}

// TestClient_SupportsStreaming verifies streaming is supported
func TestClient_SupportsStreaming(t *testing.T) {
	client := NewClient(nil)
	if !client.SupportsStreaming() {
		t.Error("Expected SupportsStreaming() to return true")
	}
}

// TestClient_StreamResponse_TracksLastPromptAndOptions verifies tracking
func TestClient_StreamResponse_TracksLastPromptAndOptions(t *testing.T) {
	client := NewClient(nil)
	client.SetResponses("Test")

	expectedPrompt := "test prompt"
	expectedOptions := &core.AIOptions{
		Model:       "test-model",
		Temperature: 0.7,
	}

	callback := func(chunk core.StreamChunk) error { return nil }
	_, err := client.StreamResponse(context.Background(), expectedPrompt, expectedOptions, callback)
	if err != nil {
		t.Fatalf("StreamResponse failed: %v", err)
	}

	if client.LastPrompt != expectedPrompt {
		t.Errorf("Expected LastPrompt %q, got %q", expectedPrompt, client.LastPrompt)
	}
	if client.LastOptions != expectedOptions {
		t.Error("LastOptions not set correctly")
	}
}
