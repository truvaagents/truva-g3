package main

import (
	"net/http"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewsTool provides news search capabilities using GNews.io API
type NewsTool struct {
	*core.BaseTool
	httpClient *http.Client
	apiKey     string
}

// NewsRequest represents the input for news search
type NewsRequest struct {
	Query      string `json:"query"`                 // Search query (e.g., "Tokyo travel")
	MaxResults int    `json:"max_results,omitempty"` // 1-10, default 5
	Language   string `json:"language,omitempty"`    // e.g., "en", default "en"
}

// NewsResponse represents the news search result
type NewsResponse struct {
	TotalArticles int       `json:"total_articles"`
	Articles      []Article `json:"articles"`
}

// Article represents a single news article
type Article struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Source      string `json:"source"`
	PublishedAt string `json:"published_at"`
	ImageURL    string `json:"image_url,omitempty"`
}

// GNewsResponse represents the GNews.io API response
type GNewsResponse struct {
	TotalArticles int `json:"totalArticles"`
	Articles      []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Image       string `json:"image"`
		PublishedAt string `json:"publishedAt"`
		Source      struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"source"`
	} `json:"articles"`
}

const (
	ErrCodeAPIKeyMissing      = "API_KEY_MISSING"
	ErrCodeRateLimitExceeded  = "RATE_LIMIT_EXCEEDED"
	ErrCodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	ErrCodeInvalidRequest     = "INVALID_REQUEST"
)

const GNewsBaseURL = "https://gnews.io/api/v4"

func NewNewsTool() *NewsTool {
	transport := &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	}

	tool := &NewsTool{
		BaseTool: core.NewTool("news-tool"),
		httpClient: &http.Client{
			Transport: otelhttp.NewTransport(transport),
			Timeout:   30 * time.Second,
		},
		apiKey: os.Getenv("GNEWS_API_KEY"),
	}

	tool.registerCapabilities()
	return tool
}

func (n *NewsTool) registerCapabilities() {
	n.RegisterCapability(core.Capability{
		Name:        "search_news",
		Description: "Searches for news articles related to a query.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     n.handleSearchNews,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: "Tokyo travel", Description: "Search query for news articles"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "max_results", Type: "number", Example: "5", Description: "Number of articles (1-10)"},
				{Name: "language", Type: "string", Example: "en", Description: "Language code (en, de, fr, etc.)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "total_articles", Type: "number", Description: "Total number of articles matching the query"},
				{Name: "articles", Type: "array", Description: "Array of articles, each with title, description, url, source, published_at, and optional image_url"},
			},
		},
	})
}
