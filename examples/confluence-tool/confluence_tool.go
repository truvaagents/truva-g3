package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// ConfluenceTool wraps the Confluence REST API v2 and exposes page capabilities.
// This is a WRITE-CAPABLE tool: create_page and update_page mutate data.
type ConfluenceTool struct {
	*core.BaseTool
	client *ConfluenceClient
}

// ---------------------------------------------------------------------------
// Request types
// ---------------------------------------------------------------------------

// CreatePageRequest represents the input for creating a new Confluence page.
type CreatePageRequest struct {
	SpaceID  string `json:"space_id"`            // Confluence space ID
	Title    string `json:"title"`               // Page title
	Content  string `json:"content,omitempty"`   // Page body as markdown-like text
	ParentID string `json:"parent_id,omitempty"` // Optional parent page ID
}

// SearchPagesRequest represents the input for searching Confluence pages via CQL.
type SearchPagesRequest struct {
	Query    string `json:"query"`               // Text search or raw CQL
	SpaceKey string `json:"space_key,omitempty"` // Restrict to a specific space
	Limit    int    `json:"limit,omitempty"`     // Max results (default 10, max 250)
}

// GetPageRequest represents the input for retrieving a single Confluence page.
type GetPageRequest struct {
	PageID      string `json:"page_id"`                // Confluence page ID
	IncludeBody bool   `json:"include_body,omitempty"` // Whether to include page content
}

// ListSpacesRequest represents the input for listing Confluence spaces.
type ListSpacesRequest struct {
	Limit int `json:"limit,omitempty"` // Max results (default 25, max 250)
}

// UpdatePageRequest represents the input for updating a Confluence page.
type UpdatePageRequest struct {
	PageID  string `json:"page_id"`           // Confluence page ID
	Title   string `json:"title,omitempty"`   // New title (keeps existing if empty)
	Content string `json:"content,omitempty"` // New body as markdown-like text
}

// ---------------------------------------------------------------------------
// Response types
// ---------------------------------------------------------------------------

// CreatePageResponse represents the output after creating a page.
type CreatePageResponse struct {
	PageID    string `json:"page_id"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	SpaceID   string `json:"space_id"`
	Version   int    `json:"version"`
	CreatedAt string `json:"created_at,omitempty"`
	Source    string `json:"source"`
}

// SearchPagesResponse represents the output for page search results.
type SearchPagesResponse struct {
	Query   string             `json:"query"`
	Results []SearchPageResult `json:"results"`
	Total   int                `json:"total"`
	Source  string             `json:"source"`
}

// SearchPageResult represents a single page in search results.
type SearchPageResult struct {
	PageID    string `json:"page_id"`
	Title     string `json:"title"`
	SpaceKey  string `json:"space_key"`
	SpaceName string `json:"space_name"`
	URL       string `json:"url"`
	Excerpt   string `json:"excerpt,omitempty"`
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// GetPageResponse represents the output for a single page retrieval.
type GetPageResponse struct {
	PageID    string `json:"page_id"`
	Title     string `json:"title"`
	SpaceID   string `json:"space_id"`
	URL       string `json:"url"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Source    string `json:"source"`
}

// ListSpacesResponse represents the output for listing spaces.
type ListSpacesResponse struct {
	Spaces []SpaceInfo `json:"spaces"`
	Total  int         `json:"total"`
	Source string      `json:"source"`
}

// SpaceInfo represents a single Confluence space.
type SpaceInfo struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	HomepageID  string `json:"homepage_id,omitempty"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// UpdatePageResponse represents the output after updating a page.
type UpdatePageResponse struct {
	PageID  string `json:"page_id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Version int    `json:"version"`
	Source  string `json:"source"`
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewConfluenceTool creates and initializes the Confluence tool.
func NewConfluenceTool() *ConfluenceTool {
	baseURL := os.Getenv("CONFLUENCE_BASE_URL")
	email := os.Getenv("CONFLUENCE_USER_EMAIL")
	apiToken := os.Getenv("CONFLUENCE_API_TOKEN")

	tool := &ConfluenceTool{
		BaseTool: core.NewTool("confluence-tool"),
		client:   NewConfluenceClient(baseURL, email, apiToken),
	}

	tool.registerCapabilities()
	return tool
}

// ---------------------------------------------------------------------------
// Capability Registration
// ---------------------------------------------------------------------------

// registerCapabilities sets up all Confluence-related capabilities.
func (t *ConfluenceTool) registerCapabilities() {
	// Capability 1: Create Page (WRITE)
	// Auto-generated endpoint: /api/capabilities/create_page
	// Schema endpoint: /api/capabilities/create_page/schema
	t.RegisterCapability(core.Capability{
		Name:        "create_page",
		Description: "Creates a new page in a Confluence space. Use list_spaces first to get the numeric space ID.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCreatePage,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "space_id",
					Type:        "string",
					Example:     "360452",
					Description: "Numeric space ID (e.g. \"360452\"), NOT the space key or name. Get this from list_spaces.",
				},
				{
					Name:        "title",
					Type:        "string",
					Example:     "Post-Mortem: Stock Tool Outage 2026-03-05",
					Description: "Page title",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "content",
					Type:        "string",
					Example:     "## Summary\nThe stock-market-tool experienced a 15-minute outage...\n\n## Timeline\n- 14:00 Alert triggered\n- 14:15 Service restored",
					Description: "Page body as markdown-like text (headings, bullets, paragraphs)",
				},
				{
					Name:        "parent_id",
					Type:        "string",
					Example:     "123456",
					Description: "Parent page ID for nested page creation",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "page_id", Type: "string", Description: "The ID of the created page"},
				{Name: "url", Type: "string", Description: "Full URL to the created page"},
				{Name: "title", Type: "string", Description: "Title of the created page"},
				{Name: "space_id", Type: "string", Description: "Space ID the page was created in"},
				{Name: "version", Type: "number", Description: "Page version number"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "created_at", Type: "string", Description: "ISO timestamp of page creation"},
			},
		},
	})

	// Capability 2: Search Pages (READ)
	// Auto-generated endpoint: /api/capabilities/search_pages
	// Schema endpoint: /api/capabilities/search_pages/schema
	t.RegisterCapability(core.Capability{
		Name:        "search_pages",
		Description: "Searches Confluence pages by text or CQL query.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchPages,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "post-mortem",
					Description: "Text to search in page titles and content, or raw CQL (e.g. type=page AND title~\"outage\")",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "space_key",
					Type:        "string",
					Example:     "OPS",
					Description: "Restrict search to a specific Confluence space by key",
				},
				{
					Name:        "limit",
					Type:        "number",
					Example:     "10",
					Description: "Maximum number of results (default 10, max 250)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The search query that was executed"},
				{Name: "results", Type: "array", Description: "Array of matching pages with page_id, title, space_key, space_name, url, version"},
				{Name: "total", Type: "number", Description: "Total number of matching results"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 3: Get Page (READ)
	// Auto-generated endpoint: /api/capabilities/get_page
	// Schema endpoint: /api/capabilities/get_page/schema
	t.RegisterCapability(core.Capability{
		Name:        "get_page",
		Description: "Retrieves a Confluence page by ID, optionally including body content.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetPage,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "page_id",
					Type:        "string",
					Example:     "123456",
					Description: "Confluence page ID",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "include_body",
					Type:        "boolean",
					Example:     "true",
					Description: "Whether to include the full page content (default false)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "page_id", Type: "string", Description: "The Confluence page ID"},
				{Name: "title", Type: "string", Description: "Page title"},
				{Name: "space_id", Type: "string", Description: "Space ID the page belongs to"},
				{Name: "url", Type: "string", Description: "Full URL to the page"},
				{Name: "version", Type: "number", Description: "Page version number"},
				{Name: "status", Type: "string", Description: "Page status (e.g. current, draft)"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "content", Type: "string", Description: "Page body content (when include_body is true)"},
				{Name: "created_at", Type: "string", Description: "ISO timestamp of page creation"},
			},
		},
	})

	// Capability 4: Update Page (WRITE)
	// Auto-generated endpoint: /api/capabilities/update_page
	// Schema endpoint: /api/capabilities/update_page/schema
	t.RegisterCapability(core.Capability{
		Name:        "update_page",
		Description: "Updates a Confluence page title and/or content.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleUpdatePage,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "page_id",
					Type:        "string",
					Example:     "123456",
					Description: "Confluence page ID to update",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "title",
					Type:        "string",
					Example:     "Post-Mortem: Stock Tool Outage (Resolved)",
					Description: "New page title (keeps existing if empty)",
				},
				{
					Name:        "content",
					Type:        "string",
					Example:     "## Resolution\nRoot cause identified as API rate limiting...",
					Description: "New page body as markdown-like text",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "page_id", Type: "string", Description: "The updated page ID"},
				{Name: "url", Type: "string", Description: "Full URL to the updated page"},
				{Name: "title", Type: "string", Description: "Current page title after update"},
				{Name: "version", Type: "number", Description: "New page version number"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 5: List Spaces (READ)
	// Auto-generated endpoint: /api/capabilities/list_spaces
	// Schema endpoint: /api/capabilities/list_spaces/schema
	t.RegisterCapability(core.Capability{
		Name:        "list_spaces",
		Description: "Lists Confluence spaces with their numeric IDs (needed by create_page) and keys.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleListSpaces,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "limit",
					Type:        "number",
					Example:     "25",
					Description: "Maximum number of spaces to return (default 25, max 250)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "spaces", Type: "array", Description: "Array of space objects with id, key, name, type, status"},
				{Name: "total", Type: "number", Description: "Total number of spaces returned"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})
}
