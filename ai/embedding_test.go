package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEmbeddingClient_Defaults(t *testing.T) {
	client, _ := NewEmbeddingClient()
	assert.Equal(t, "http://localhost:11434/v1", client.baseURL)
	assert.Equal(t, "nomic-embed-text", client.model)
	assert.Empty(t, client.apiKey)
}

func TestNewEmbeddingClient_Options(t *testing.T) {
	client, _ := NewEmbeddingClient(
		WithEmbeddingBaseURL("http://custom:8080/v1"),
		WithEmbeddingModel("all-minilm"),
		WithEmbeddingAPIKey("test-key"),
	)
	assert.Equal(t, "http://custom:8080/v1", client.baseURL)
	assert.Equal(t, "all-minilm", client.model)
	assert.Equal(t, "test-key", client.apiKey)
}

func TestNewEmbeddingClient_EnvOverrides(t *testing.T) {
	os.Setenv("TRUVAG3_EMBEDDING_BASE_URL", "http://env-host:9090/v1")
	os.Setenv("TRUVAG3_EMBEDDING_MODEL", "env-model")
	os.Setenv("TRUVAG3_EMBEDDING_API_KEY", "env-key")
	defer func() {
		os.Unsetenv("TRUVAG3_EMBEDDING_BASE_URL")
		os.Unsetenv("TRUVAG3_EMBEDDING_MODEL")
		os.Unsetenv("TRUVAG3_EMBEDDING_API_KEY")
	}()

	client, _ := NewEmbeddingClient()
	assert.Equal(t, "http://env-host:9090/v1", client.baseURL)
	assert.Equal(t, "env-model", client.model)
	assert.Equal(t, "env-key", client.apiKey)
}

func TestNewEmbeddingClient_ExplicitOptionOverridesEnv(t *testing.T) {
	os.Setenv("TRUVAG3_EMBEDDING_MODEL", "env-model")
	defer os.Unsetenv("TRUVAG3_EMBEDDING_MODEL")

	client, _ := NewEmbeddingClient(WithEmbeddingModel("explicit-model"))
	assert.Equal(t, "explicit-model", client.model, "explicit option should override env var")
}

func TestGenerateEmbeddings_EmptyInput(t *testing.T) {
	client, _ := NewEmbeddingClient()
	resp, err := client.GenerateEmbeddings(context.Background(), []string{}, nil)
	require.NoError(t, err)
	assert.Empty(t, resp.Embeddings)
}

func TestGenerateEmbeddings_Success(t *testing.T) {
	// Mock OpenAI-compatible embedding server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Len(t, req.Input, 2)

		// Return mock response
		resp := embeddingResponse{
			Object: "list",
			Model:  "nomic-embed-text",
			Data: []embeddingData{
				{Object: "embedding", Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Object: "embedding", Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
			},
			Usage: embeddingUsage{PromptTokens: 10, TotalTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := NewEmbeddingClient(
		WithEmbeddingBaseURL(server.URL+"/v1"),
		WithEmbeddingAPIKey("test-key"),
	)

	resp, err := client.GenerateEmbeddings(context.Background(),
		[]string{"Hello world", "Test embedding"},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, resp.Embeddings, 2)
	assert.Equal(t, []float32{0.1, 0.2, 0.3}, resp.Embeddings[0])
	assert.Equal(t, []float32{0.4, 0.5, 0.6}, resp.Embeddings[1])
	assert.Equal(t, "nomic-embed-text", resp.Model)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
}

func TestGenerateEmbeddings_ModelOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "custom-model", req.Model) // Options override

		resp := embeddingResponse{
			Data: []embeddingData{{Embedding: []float32{0.1}, Index: 0}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client, _ := NewEmbeddingClient(WithEmbeddingBaseURL(server.URL + "/v1"))
	_, err := client.GenerateEmbeddings(context.Background(),
		[]string{"test"},
		&core.EmbeddingOptions{Model: "custom-model"},
	)
	assert.NoError(t, err)
}

func TestGenerateEmbeddings_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "model not found"}`))
	}))
	defer server.Close()

	client, _ := NewEmbeddingClient(WithEmbeddingBaseURL(server.URL + "/v1"))
	_, err := client.GenerateEmbeddings(context.Background(), []string{"test"}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "status 500")
}

func TestGenerateEmbeddings_ConnectionError(t *testing.T) {
	client, _ := NewEmbeddingClient(WithEmbeddingBaseURL("http://localhost:99999/v1"))
	_, err := client.GenerateEmbeddings(context.Background(), []string{"test"}, nil)
	assert.Error(t, err)
}

// Verify interface compliance at compile time
var _ core.EmbeddingClient = (*EmbeddingClient)(nil)
