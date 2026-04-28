package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// PubMedTool wraps the NCBI E-utilities API for biomedical literature search.
// Follows the passive tool pattern -- registers capabilities but does not discover other tools.
type PubMedTool struct {
	*core.BaseTool
	apiKey   string // NCBI_API_KEY (optional, increases rate from 3 to 10 req/sec)
	toolName string // NCBI_TOOL_NAME (required by NCBI policy)
	email    string // NCBI_EMAIL (required by NCBI policy)
	client   *PubMedClient
}

// --- search_articles ---

type SearchArticlesRequest struct {
	Query      string `json:"query"`                // PubMed search query (supports MeSH terms)
	MaxResults int    `json:"max_results,omitempty"` // 1-100, default 10
	Sort       string `json:"sort,omitempty"`        // "relevance" (default) or "date"
}

type SearchArticlesResponse struct {
	Query      string           `json:"query"`
	TotalCount int              `json:"total_count"` // Total matching articles (from esearch count)
	Articles   []ArticleSummary `json:"articles"`
	Source     string           `json:"source"` // "NCBI PubMed E-utilities"
}

type ArticleSummary struct {
	PMID        string   `json:"pmid"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors"`        // Flattened from authors[].name
	Journal     string   `json:"journal"`        // fulljournalname
	PubDate     string   `json:"pub_date"`       // Normalized from inconsistent formats
	DOI         string   `json:"doi,omitempty"`  // Extracted from articleids where idtype=="doi"
	PMCRefCount int      `json:"pmc_ref_count"`  // Citation count in PMC (one of few actual ints)
	HasAbstract bool     `json:"has_abstract"`   // Derived from attributes containing "Has Abstract"
}

// --- get_article_details ---

type GetArticleDetailsRequest struct {
	PMIDs string `json:"pmids"` // Comma-separated PubMed IDs (e.g., "38000000,37999999")
}

type GetArticleDetailsResponse struct {
	Articles []ArticleDetail `json:"articles"`
	Source   string          `json:"source"`
}

type ArticleDetail struct {
	PMID        string      `json:"pmid"`
	Title       string      `json:"title"`
	Authors     []Author    `json:"authors"`
	Journal     string      `json:"journal"`          // fulljournalname
	PubDate     string      `json:"pub_date"`
	DOI         string      `json:"doi,omitempty"`
	Volume      string      `json:"volume,omitempty"`
	Issue       string      `json:"issue,omitempty"`
	Pages       string      `json:"pages,omitempty"`
	PMCRefCount int         `json:"pmc_ref_count"`
	HasAbstract bool        `json:"has_abstract"`
	ArticleIDs  []ArticleID `json:"article_ids"` // All identifiers (doi, pmc, pubmed, etc.)
}

type Author struct {
	Name     string `json:"name"`
	AuthType string `json:"auth_type,omitempty"` // "Author", "Investigator", etc.
}

type ArticleID struct {
	IDType string `json:"id_type"` // "doi", "pmc", "pubmed", "pii", etc.
	Value  string `json:"value"`
}

// --- get_citations ---

type GetCitationsRequest struct {
	PMID string `json:"pmid"` // Single PubMed ID
}

type GetCitationsResponse struct {
	PMID          string           `json:"pmid"`
	CitationCount int              `json:"citation_count"`
	Citations     []ArticleSummary `json:"citations"` // Citing articles (fetched via esummary)
	Source        string           `json:"source"`
}

func NewPubMedTool() *PubMedTool {
	apiKey := os.Getenv("NCBI_API_KEY")
	toolName := os.Getenv("NCBI_TOOL_NAME")
	email := os.Getenv("NCBI_EMAIL")

	tool := &PubMedTool{
		BaseTool: core.NewTool("pubmed-tool"),
		apiKey:   apiKey,
		toolName: toolName,
		email:    email,
		client:   NewPubMedClient(apiKey, toolName, email),
	}

	tool.registerCapabilities()
	return tool
}

func (t *PubMedTool) registerCapabilities() {

	// Capability 1: search_articles
	// Auto-generated endpoint: /api/capabilities/search_articles
	t.RegisterCapability(core.Capability{
		Name: "search_articles",
		Description: "Searches PubMed for biomedical articles by keyword or MeSH term.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchArticles,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query that was executed"},
				{Name: "total_count", Type: "number", Description: "Total number of matching articles"},
				{Name: "articles", Type: "array", Description: "List of article summaries with pmid, title, authors, journal, pub_date, pmc_ref_count, has_abstract"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "diabetes type 2 treatment",
					Description: "PubMed search query, supports MeSH terms",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Max results to return, 1-100",
				},
				{
					Name:        "sort",
					Type:        "string",
					Example:     "relevance",
					Description: "Sort order: relevance or date",
				},
			},
		},
	})

	// Capability 2: get_article_details
	// Auto-generated endpoint: /api/capabilities/get_article_details
	t.RegisterCapability(core.Capability{
		Name: "get_article_details",
		Description: "Retrieves detailed metadata for specific PubMed articles by their PMIDs.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetArticleDetails,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "articles", Type: "array", Description: "List of detailed articles with pmid, title, authors, journal, pub_date, pmc_ref_count, has_abstract, article_ids"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "pmids",
					Type:        "string",
					Example:     "38000000,37999999",
					Description: "Comma-separated PubMed IDs",
				},
			},
		},
	})

	// Capability 3: get_citations
	// Auto-generated endpoint: /api/capabilities/get_citations
	t.RegisterCapability(core.Capability{
		Name: "get_citations",
		Description: "Finds articles that cite a given PubMed article.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetCitations,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "pmid", Type: "string", Description: "The PubMed ID that was queried"},
				{Name: "citation_count", Type: "number", Description: "Number of citing articles"},
				{Name: "citations", Type: "array", Description: "List of citing article summaries with pmid, title, authors, journal, pub_date"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "pmid",
					Type:        "string",
					Example:     "38000000",
					Description: "Single PubMed ID to find citing articles",
				},
			},
		},
	})
}
