package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// SemanticScholarTool is a focused tool that provides academic paper search
// capabilities via the Semantic Scholar Academic Graph API.
// It demonstrates the passive tool pattern - can register but not discover.
type SemanticScholarTool struct {
	*core.BaseTool
	apiKey string
	client *S2Client
}

// NewSemanticScholarTool creates a new Semantic Scholar tool
func NewSemanticScholarTool() *SemanticScholarTool {
	apiKey := os.Getenv("S2_API_KEY")

	tool := &SemanticScholarTool{
		BaseTool: core.NewTool("semantic-scholar-tool"),
		apiKey:   apiKey,
		client:   NewS2Client(apiKey),
	}

	tool.registerCapabilities()
	return tool
}

// SearchPapersRequest represents the input for paper search
type SearchPapersRequest struct {
	Query         string `json:"query"`                    // Required: search query
	MaxResults    int    `json:"max_results,omitempty"`    // Optional: 1-100, default 10
	Year          string `json:"year,omitempty"`           // Optional: year range filter e.g. "2023-2026"
	FieldsOfStudy string `json:"fields_of_study,omitempty"` // Optional: field of study filter e.g. "Computer Science"
}

// PaperDetailsRequest represents the input for paper details
type PaperDetailsRequest struct {
	PaperID string `json:"paper_id"` // Required: 40-char hex, DOI:, ARXIV:, PMID:, CorpusId:
}

// AuthorRequest represents the input for author profile
type AuthorRequest struct {
	AuthorID string `json:"author_id"` // Required: Semantic Scholar numeric author ID (string)
}

// CitationsRequest represents the input for citations
type CitationsRequest struct {
	PaperID    string `json:"paper_id"`              // Required: paper identifier
	MaxResults int    `json:"max_results,omitempty"` // Optional: max citing papers, default 20
}

// PaperResult represents a single paper in search results
type PaperResult struct {
	PaperID         string   `json:"paper_id"`
	Title           string   `json:"title"`
	Authors         []Author `json:"authors"`
	Year            int      `json:"year"`
	CitationCount   int      `json:"citation_count"`
	Abstract        string   `json:"abstract,omitempty"`
	URL             string   `json:"url"`
	PublicationDate string   `json:"publication_date,omitempty"`
}

// Author represents a paper author
type Author struct {
	AuthorID string `json:"author_id"`
	Name     string `json:"name"`
}

// SearchPapersResponse represents the output for paper search
type SearchPapersResponse struct {
	Query      string        `json:"query"`
	Total      int           `json:"total"`
	Papers     []PaperResult `json:"papers"`
	Source     string        `json:"source"`
}

// PaperDetailsResponse represents detailed paper information
type PaperDetailsResponse struct {
	PaperID                   string        `json:"paper_id"`
	Title                     string        `json:"title"`
	Authors                   []Author      `json:"authors"`
	Year                      int           `json:"year"`
	Abstract                  string        `json:"abstract,omitempty"`
	URL                       string        `json:"url"`
	CitationCount             int           `json:"citation_count"`
	ReferenceCount            int           `json:"reference_count"`
	InfluentialCitationCount  int           `json:"influential_citation_count"`
	TLDR                      string        `json:"tldr,omitempty"`
	OpenAccessPDF             string        `json:"open_access_pdf,omitempty"`
	PublicationDate           string        `json:"publication_date,omitempty"`
	References                []PaperResult `json:"references,omitempty"`
	Citations                 []PaperResult `json:"citations,omitempty"`
	Source                    string        `json:"source"`
}

// AuthorResponse represents an author profile
type AuthorResponse struct {
	AuthorID     string        `json:"author_id"`
	Name         string        `json:"name"`
	Affiliations []string      `json:"affiliations,omitempty"`
	PaperCount   int           `json:"paper_count"`
	CitationCount int          `json:"citation_count"`
	HIndex       int           `json:"h_index"`
	Papers       []PaperResult `json:"papers,omitempty"`
	URL          string        `json:"url"`
	Source       string        `json:"source"`
}

// CitationsResponse represents citing papers
type CitationsResponse struct {
	PaperID   string        `json:"paper_id"`
	Total     int           `json:"total"`
	Citations []PaperResult `json:"citations"`
	Source    string        `json:"source"`
}

// registerCapabilities sets up all Semantic Scholar capabilities
func (s *SemanticScholarTool) registerCapabilities() {
	// Capability 1: Search Papers
	// Auto-generated endpoint: /api/capabilities/search_papers
	s.RegisterCapability(core.Capability{
		Name:        "search_papers",
		Description: "Searches academic papers on Semantic Scholar by keyword query.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleSearchPapers,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query that was executed"},
				{Name: "total", Type: "number", Description: "Total number of matching papers"},
				{Name: "papers", Type: "array", Description: "List of matching papers with paper_id, title, authors, year, citation_count, url"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "graph neural networks",
					Description: "Search query for academic papers",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Max results, 1-100, default 10",
				},
				{
					Name:        "year",
					Type:        "string",
					Example:     "2023-2026",
					Description: "Year range filter",
				},
				{
					Name:        "fields_of_study",
					Type:        "string",
					Example:     "Computer Science",
					Description: "Field of study filter",
				},
			},
		},
	})

	// Capability 2: Get Paper Details
	// Auto-generated endpoint: /api/capabilities/get_paper_details
	s.RegisterCapability(core.Capability{
		Name:        "get_paper_details",
		Description: "Gets detailed information about a specific academic paper including abstract, citations, and references.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleGetPaperDetails,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "paper_id", Type: "string", Description: "Semantic Scholar paper ID"},
				{Name: "title", Type: "string", Description: "Paper title"},
				{Name: "authors", Type: "array", Description: "List of authors with author_id and name"},
				{Name: "year", Type: "number", Description: "Publication year"},
				{Name: "url", Type: "string", Description: "Semantic Scholar URL"},
				{Name: "citation_count", Type: "number", Description: "Number of citations"},
				{Name: "reference_count", Type: "number", Description: "Number of references"},
				{Name: "influential_citation_count", Type: "number", Description: "Number of influential citations"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "abstract", Type: "string", Description: "Paper abstract"},
				{Name: "tldr", Type: "string", Description: "Auto-generated TL;DR summary"},
				{Name: "open_access_pdf", Type: "string", Description: "Open access PDF URL"},
				{Name: "publication_date", Type: "string", Description: "Publication date"},
				{Name: "references", Type: "array", Description: "List of referenced papers"},
				{Name: "citations", Type: "array", Description: "List of citing papers"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "paper_id",
					Type:        "string",
					Example:     "ARXIV:2301.07041",
					Description: "Paper ID: 40-char hex, DOI:, ARXIV:, PMID:, CorpusId:",
				},
			},
		},
	})

	// Capability 3: Get Author
	// Auto-generated endpoint: /api/capabilities/get_author
	s.RegisterCapability(core.Capability{
		Name:        "get_author",
		Description: "Gets an author's profile including affiliations, h-index, and recent papers.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleGetAuthor,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "author_id", Type: "string", Description: "Semantic Scholar author ID"},
				{Name: "name", Type: "string", Description: "Author name"},
				{Name: "paper_count", Type: "number", Description: "Total number of papers"},
				{Name: "citation_count", Type: "number", Description: "Total citation count"},
				{Name: "h_index", Type: "number", Description: "Author h-index"},
				{Name: "url", Type: "string", Description: "Semantic Scholar author URL"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "affiliations", Type: "array", Description: "Author affiliations"},
				{Name: "papers", Type: "array", Description: "Recent papers by this author"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "author_id",
					Type:        "string",
					Example:     "1741101",
					Description: "Semantic Scholar numeric author ID",
				},
			},
		},
	})

	// Capability 4: Get Citations
	// Auto-generated endpoint: /api/capabilities/get_citations
	s.RegisterCapability(core.Capability{
		Name:        "get_citations",
		Description: "Gets papers that cite a given paper.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleGetCitations,

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "paper_id", Type: "string", Description: "The paper ID that was queried"},
				{Name: "total", Type: "number", Description: "Total number of citing papers"},
				{Name: "citations", Type: "array", Description: "List of citing papers with paper_id, title, authors, year, citation_count, url"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "paper_id",
					Type:        "string",
					Example:     "ARXIV:2301.07041",
					Description: "Paper ID",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "20",
					Description: "Max citing papers, default 20",
				},
			},
		},
	})
}
