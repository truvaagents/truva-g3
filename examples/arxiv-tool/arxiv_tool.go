package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// ArxivTool is a focused tool that provides academic paper search capabilities via the arXiv API.
// It demonstrates the passive tool pattern with XML-to-JSON conversion.
// arXiv returns Atom 1.0 XML exclusively -- this tool handles the XML parsing
// and converts results to JSON for the standard TruvaG3 ToolResponse wrapper.
type ArxivTool struct {
	*core.BaseTool
	client *ArxivClient
}

// SearchPapersRequest represents the input for paper search requests
type SearchPapersRequest struct {
	Query      string `json:"query"`                 // Required: search query (e.g., "transformer attention mechanism")
	Category   string `json:"category,omitempty"`    // Optional: arXiv category filter (e.g., "cs.AI")
	MaxResults int    `json:"max_results,omitempty"` // Optional: max results 1-30000, default 10
	SortBy     string `json:"sort_by,omitempty"`     // Optional: relevance, lastUpdatedDate, submittedDate
}

// GetPaperRequest represents the input for getting a specific paper by ID
type GetPaperRequest struct {
	ArxivID string `json:"arxiv_id"` // Required: arXiv paper ID (e.g., "2301.07041" or "2301.07041v2")
}

// RecentPapersRequest represents the input for getting recent papers in a category
type RecentPapersRequest struct {
	Category   string `json:"category"`              // Required: arXiv category (e.g., "cs.AI")
	MaxResults int    `json:"max_results,omitempty"` // Optional: max results, default 10
}

// PaperResult represents a single arXiv paper in the JSON response
type PaperResult struct {
	ArxivID         string   `json:"arxiv_id"`              // e.g., "2301.07041v2"
	Title           string   `json:"title"`                 // Paper title (whitespace-normalized)
	Authors         []string `json:"authors"`               // List of author names
	Abstract        string   `json:"abstract"`              // Paper abstract (whitespace-normalized)
	Categories      []string `json:"categories"`            // All category terms (e.g., ["cs.AI", "cs.CL"])
	PrimaryCategory string   `json:"primary_category"`      // Primary category term
	PublishedDate   string   `json:"published_date"`        // ISO 8601 date string
	UpdatedDate     string   `json:"updated_date"`          // ISO 8601 date string
	PDFURL          string   `json:"pdf_url"`               // Direct PDF link
	AbsURL          string   `json:"abs_url"`               // Abstract page URL
	Comment         string   `json:"comment,omitempty"`     // Optional: author comment
	JournalRef      string   `json:"journal_ref,omitempty"` // Optional: journal reference
	DOI             string   `json:"doi,omitempty"`         // Optional: DOI
}

// SearchPapersResponse represents the output for paper search
type SearchPapersResponse struct {
	Query        string        `json:"query"`
	TotalResults int           `json:"total_results"`
	Papers       []PaperResult `json:"papers"`
	Source       string        `json:"source"`
}

// GetPaperResponse represents the output for a specific paper lookup
type GetPaperResponse struct {
	Paper  PaperResult `json:"paper"`
	Source string      `json:"source"`
}

// RecentPapersResponse represents the output for recent papers
type RecentPapersResponse struct {
	Category     string        `json:"category"`
	TotalResults int           `json:"total_results"`
	Papers       []PaperResult `json:"papers"`
	Source       string        `json:"source"`
}

// NewArxivTool creates a new arXiv paper search tool
func NewArxivTool() *ArxivTool {
	tool := &ArxivTool{
		BaseTool: core.NewTool("arxiv-tool"),
		client:   NewArxivClient(),
	}

	tool.registerCapabilities()
	return tool
}

func (a *ArxivTool) registerCapabilities() {
	// Capability 1: Search Papers
	// Auto-generated endpoint: /api/capabilities/search_papers
	a.RegisterCapability(core.Capability{
		Name:        "search_papers",
		Description: "Searches arXiv preprints by query with optional category filter and sorting.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleSearchPapers,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query that was executed"},
				{Name: "total_results", Type: "number", Description: "Total number of matching papers"},
				{Name: "papers", Type: "array", Description: "List of matching papers with arxiv_id, title, authors, abstract, categories, primary_category, published_date, updated_date, pdf_url, abs_url"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "transformer attention mechanism",
					Description: "Search query for arXiv papers (spaces are treated as OR; use AND between terms for conjunction)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "category",
					Type:        "string",
					Example:     "cs.AI",
					Description: "arXiv category filter (e.g., cs.AI, cs.CL, math.CO, physics.hep-th)",
				},
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Maximum number of results (1-30000, default 10)",
				},
				{
					Name:        "sort_by",
					Type:        "string",
					Example:     "relevance",
					Description: "Sort order: relevance, lastUpdatedDate, or submittedDate",
				},
			},
		},
	})

	// Capability 2: Get Paper
	// Auto-generated endpoint: /api/capabilities/get_paper
	a.RegisterCapability(core.Capability{
		Name:        "get_paper",
		Description: "Gets detailed information for a specific arXiv paper by its ID.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleGetPaper,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "paper", Type: "object", Description: "Paper details with arxiv_id, title, authors, abstract, categories, primary_category, published_date, updated_date, pdf_url, abs_url"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "arxiv_id",
					Type:        "string",
					Example:     "2301.07041",
					Description: "arXiv paper ID, optionally with version suffix (e.g., 2301.07041v2)",
				},
			},
		},
	})

	// Capability 3: Recent Papers
	// Auto-generated endpoint: /api/capabilities/recent_papers
	a.RegisterCapability(core.Capability{
		Name:        "recent_papers",
		Description: "Gets the most recently submitted papers in an arXiv category.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleRecentPapers,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "category", Type: "string", Description: "The arXiv category that was queried"},
				{Name: "total_results", Type: "number", Description: "Total number of recent papers returned"},
				{Name: "papers", Type: "array", Description: "List of recent papers with arxiv_id, title, authors, abstract, categories, primary_category, published_date, updated_date, pdf_url, abs_url"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "category",
					Type:        "string",
					Example:     "cs.AI",
					Description: "arXiv category for recent papers (e.g., cs.AI, cs.LG, physics.hep-th)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Maximum number of results (default 10)",
				},
			},
		},
	})
}
