package providers

import (
	"context"
	"strings"
)

// MockProvider provides fake search results for testing and development.
// Use this when no API key is configured or for local testing.
type MockProvider struct{}

// NewMockProvider creates a new mock search provider.
func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

// Name returns the provider name.
func (m *MockProvider) Name() string {
	return "mock"
}

// Search returns mock search results based on the query.
// Results are contextual to common query patterns (beach, travel, news, etc.)
func (m *MockProvider) Search(ctx context.Context, query string, maxResults int, searchType string) ([]SearchResult, error) {
	queryLower := strings.ToLower(query)

	// Generate contextual mock results based on query
	var results []SearchResult

	if strings.Contains(queryLower, "beach") || strings.Contains(queryLower, "destination") {
		results = getBeachResults()
	} else if strings.Contains(queryLower, "news") || searchType == "news" || strings.Contains(queryLower, "ai") || strings.Contains(queryLower, "developments") {
		results = getNewsResults()
	} else if strings.Contains(queryLower, "weather") {
		results = getWeatherResults()
	} else if strings.Contains(queryLower, "travel") || strings.Contains(queryLower, "vacation") {
		results = getTravelResults()
	} else {
		results = getGenericResults(query)
	}

	// Limit results to maxResults
	if maxResults > 0 && maxResults < len(results) {
		results = results[:maxResults]
	}

	return results, nil
}

func getBeachResults() []SearchResult {
	return []SearchResult{
		{
			Title:   "10 Best Caribbean Beach Destinations for 2026",
			Snippet: "Discover the top Caribbean beach destinations including Turks and Caicos, Aruba, and the Bahamas. Perfect for family vacations with crystal-clear waters and white sand beaches.",
			URL:     "https://example.com/caribbean-beaches",
			Score:   0.95,
		},
		{
			Title:   "Turks and Caicos: Ultimate Beach Paradise",
			Snippet: "Grace Bay Beach in Turks and Caicos consistently ranks as one of the world's best beaches. Enjoy snorkeling, diving, and luxury resorts.",
			URL:     "https://example.com/turks-caicos-guide",
			Score:   0.92,
		},
		{
			Title:   "Aruba: One Happy Island Beach Guide",
			Snippet: "Aruba's Eagle Beach and Palm Beach offer perfect conditions year-round. Known for its consistent weather and family-friendly atmosphere.",
			URL:     "https://example.com/aruba-beaches",
			Score:   0.89,
		},
		{
			Title:   "Bahamas Beach Vacation Planning",
			Snippet: "From Nassau to the Exumas, explore the best beaches in the Bahamas. Swimming pigs, clear waters, and island hopping adventures await.",
			URL:     "https://example.com/bahamas-vacation",
			Score:   0.87,
		},
		{
			Title:   "Family-Friendly Caribbean Resorts 2026",
			Snippet: "The best all-inclusive resorts for families in the Caribbean. Kids clubs, water parks, and safe beaches for your next vacation.",
			URL:     "https://example.com/family-caribbean",
			Score:   0.84,
		},
	}
}

func getNewsResults() []SearchResult {
	return []SearchResult{
		{
			Title:       "OpenAI Announces GPT-5 with Enhanced Reasoning Capabilities",
			Snippet:     "OpenAI has unveiled GPT-5, featuring breakthrough improvements in mathematical reasoning and reduced hallucinations. The model shows significant advances in complex problem-solving.",
			URL:         "https://example.com/news/gpt5-announcement",
			PublishedAt: "2026-01-30T10:30:00Z",
			Score:       0.96,
		},
		{
			Title:       "Google DeepMind's Gemini 2.0 Sets New Benchmarks",
			Snippet:     "DeepMind's latest multimodal AI system achieves state-of-the-art results across vision, language, and code generation tasks.",
			URL:         "https://example.com/news/gemini-2-launch",
			PublishedAt: "2026-01-28T14:15:00Z",
			Score:       0.93,
		},
		{
			Title:       "AI Regulation: EU AI Act Implementation Begins",
			Snippet:     "The European Union begins enforcement of the AI Act, requiring companies to classify AI systems by risk level and implement appropriate safeguards.",
			URL:         "https://example.com/news/eu-ai-act",
			PublishedAt: "2026-01-25T09:00:00Z",
			Score:       0.90,
		},
		{
			Title:       "Anthropic Releases Claude 4 with Improved Safety",
			Snippet:     "Anthropic's Claude 4 introduces constitutional AI improvements with better factual accuracy and reduced harmful outputs.",
			URL:         "https://example.com/news/claude-4-release",
			PublishedAt: "2026-01-22T16:45:00Z",
			Score:       0.88,
		},
		{
			Title:       "AI Healthcare: FDA Approves First Autonomous Diagnostic System",
			Snippet:     "The FDA has approved an AI system for independent medical diagnosis, marking a significant milestone in healthcare automation.",
			URL:         "https://example.com/news/ai-healthcare-fda",
			PublishedAt: "2026-01-20T11:00:00Z",
			Score:       0.85,
		},
	}
}

func getWeatherResults() []SearchResult {
	return []SearchResult{
		{
			Title:   "Weather.com - Local Weather Forecast",
			Snippet: "Get accurate weather forecasts for your location. Hourly, 10-day, and monthly forecasts with radar maps.",
			URL:     "https://weather.com",
			Score:   0.95,
		},
		{
			Title:   "Understanding Weather Patterns and Climate",
			Snippet: "Learn about weather systems, climate patterns, and how meteorologists predict the weather.",
			URL:     "https://example.com/weather-patterns",
			Score:   0.82,
		},
	}
}

func getTravelResults() []SearchResult {
	return []SearchResult{
		{
			Title:   "Best Travel Destinations 2026",
			Snippet: "Discover the top travel destinations for 2026, from emerging hotspots to classic favorites. Expert recommendations for every type of traveler.",
			URL:     "https://example.com/travel-2026",
			Score:   0.94,
		},
		{
			Title:   "Budget Travel Tips and Tricks",
			Snippet: "Save money on your next trip with these expert budget travel tips. Find deals on flights, hotels, and activities.",
			URL:     "https://example.com/budget-travel",
			Score:   0.88,
		},
		{
			Title:   "Solo Travel Guide for Beginners",
			Snippet: "Everything you need to know about traveling solo. Safety tips, destination recommendations, and how to meet people on the road.",
			URL:     "https://example.com/solo-travel",
			Score:   0.85,
		},
	}
}

func getGenericResults(query string) []SearchResult {
	return []SearchResult{
		{
			Title:   "Search results for: " + query,
			Snippet: "This is a mock search result. In production, real results from Tavily Search API would appear here. Configure SEARCH_API_KEY for real results.",
			URL:     "https://example.com/search?q=" + strings.ReplaceAll(query, " ", "+"),
			Score:   0.90,
		},
		{
			Title:   "Related Information",
			Snippet: "Additional information related to your search query. Mock provider returns sample data for testing.",
			URL:     "https://example.com/related",
			Score:   0.75,
		},
		{
			Title:   "Getting Started Guide",
			Snippet: "To enable real web search, get a free API key from tavily.com and set SEARCH_API_KEY in your environment.",
			URL:     "https://tavily.com",
			Score:   0.70,
		},
	}
}
