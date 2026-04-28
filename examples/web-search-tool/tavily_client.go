package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/examples/web-search-tool/providers"
	"github.com/truvaagents/truva-g3/telemetry"
)

// TavilyClient handles API communication with Tavily Search.
// Uses TracedHTTPClient for client-side span visibility in Jaeger.
// Implements providers.SearchProvider interface.
type TavilyClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewTavilyClient creates a new Tavily Search API client with distributed tracing.
func NewTavilyClient(apiKey string) *TavilyClient {
	// Use telemetry.NewTracedHTTPClientWithTransport for automatic trace propagation
	// This injects traceparent headers into outgoing requests
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
	})
	tracedClient.Timeout = 30 * time.Second

	return &TavilyClient{
		apiKey:     apiKey,
		baseURL:    "https://api.tavily.com",
		httpClient: tracedClient,
	}
}

// Name returns the provider name.
func (t *TavilyClient) Name() string {
	return "tavily"
}

// Search performs a web search via Tavily API.
func (t *TavilyClient) Search(ctx context.Context, query string, maxResults int, searchType string) ([]providers.SearchResult, error) {
	if t.apiKey == "" {
		return nil, fmt.Errorf("Tavily API key not configured")
	}

	// Build the request body (Tavily uses POST with JSON body)
	reqBody := TavilySearchRequest{
		Query:       query,
		MaxResults:  maxResults,
		SearchDepth: "basic", // 1 credit per request
	}

	// Map searchType to Tavily's topic parameter
	if searchType == "news" {
		reqBody.Topic = "news"
	} else {
		reqBody.Topic = "general"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create POST request with context for trace propagation
	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+"/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers - Tavily uses Bearer token authentication
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	// Execute - TracedHTTPClient will create a client-side span
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for error details
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var tavilyResp TavilySearchResponse
	if err := json.Unmarshal(respBody, &tavilyResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Convert to common format
	results := make([]providers.SearchResult, 0, len(tavilyResp.Results))
	for _, r := range tavilyResp.Results {
		results = append(results, providers.SearchResult{
			Title:       r.Title,
			Snippet:     r.Content,
			URL:         r.URL,
			Score:       r.Score,
			PublishedAt: r.PublishedDate, // Populated for news results
		})
	}

	return results, nil
}

// TavilySearchRequest represents the request body for Tavily Search API.
// Verified against live API - January 2026
type TavilySearchRequest struct {
	Query         string `json:"query"`                    // Required: search query
	MaxResults    int    `json:"max_results,omitempty"`    // Optional: 1-10 (default 5)
	SearchDepth   string `json:"search_depth,omitempty"`   // "basic" (1 credit) or "advanced" (2 credits)
	Topic         string `json:"topic,omitempty"`          // "general" or "news"
	IncludeAnswer bool   `json:"include_answer,omitempty"` // If true, returns AI-generated answer
	IncludeImages bool   `json:"include_images,omitempty"` // If true, returns related images
}

// TavilySearchResponse represents the response from Tavily Search API.
// Verified against live API - January 2026
type TavilySearchResponse struct {
	Query             string   `json:"query"`
	Answer            string   `json:"answer,omitempty"`              // AI-generated answer (if include_answer=true)
	FollowUpQuestions []string `json:"follow_up_questions,omitempty"` // Suggested follow-up questions
	Images            []string `json:"images,omitempty"`              // Image URLs (if include_images=true)
	ResponseTime      float64  `json:"response_time"`                 // API latency in seconds
	RequestID         string   `json:"request_id"`                    // Unique request identifier
	Results           []struct {
		Title         string  `json:"title"`
		URL           string  `json:"url"`
		Content       string  `json:"content"`                  // Snippet/summary
		Score         float64 `json:"score"`                    // Relevance score (0-1)
		RawContent    string  `json:"raw_content,omitempty"`    // Full page content (if requested)
		PublishedDate string  `json:"published_date,omitempty"` // For news results (RFC 2822 format)
	} `json:"results"`
}
