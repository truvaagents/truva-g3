// Package providers defines the interface for search providers and result types.
package providers

import (
	"context"
)

// SearchProvider defines the interface for web search providers.
// Implementations include Tavily (production) and Mock (testing).
type SearchProvider interface {
	// Name returns the provider name (e.g., "tavily", "mock")
	Name() string

	// Search performs a web search and returns results.
	// Parameters:
	//   - ctx: Context for cancellation and tracing
	//   - query: Search query string
	//   - maxResults: Maximum number of results to return (1-10)
	//   - searchType: Type of search ("web" or "news")
	// Returns:
	//   - []SearchResult: Array of search results
	//   - error: Any error that occurred during the search
	Search(ctx context.Context, query string, maxResults int, searchType string) ([]SearchResult, error)
}

// SearchResult represents a single search result from any provider.
// This is the common format used across all providers.
type SearchResult struct {
	Title       string  `json:"title"`                  // Page title
	Snippet     string  `json:"snippet"`                // Content snippet/summary
	URL         string  `json:"url"`                    // Full URL to the page
	DisplayURL  string  `json:"display_url,omitempty"`  // Human-readable URL
	PublishedAt string  `json:"published_at,omitempty"` // Publication date (for news)
	Score       float64 `json:"score,omitempty"`        // Relevance score (0-1, provider-specific)
}
