package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// AdvisoryTool provides US State Department travel advisory capabilities
// Free API, no authentication required, in-memory caching
type AdvisoryTool struct {
	*core.BaseTool
	client *StateGovClient
}

// GetAdvisoryRequest represents the input for getting a specific travel advisory
type GetAdvisoryRequest struct {
	Country string `json:"country"` // Full country name (e.g., "Thailand")
}

// GetAdvisoryResponse represents the output for a travel advisory
type GetAdvisoryResponse struct {
	Country     string `json:"country"`
	ISOCode     string `json:"iso_code"`
	Level       int    `json:"level"`      // 1-4
	LevelText   string `json:"level_text"` // e.g., "Exercise Increased Caution"
	Description string `json:"description"`
	LastUpdated string `json:"last_updated"`
	Source      string `json:"source"`
}

// ListAdvisoriesRequest represents the input for listing advisories
type ListAdvisoriesRequest struct {
	Level int `json:"level,omitempty"` // Filter by risk level (1-4)
}

// ListAdvisoriesResponse represents the output for listing advisories
type ListAdvisoriesResponse struct {
	Advisories []AdvisorySummary `json:"advisories"`
	Count      int               `json:"count"`
	Level      int               `json:"level,omitempty"` // If filtered
	Source     string            `json:"source"`
}

// AdvisorySummary represents a brief advisory entry
type AdvisorySummary struct {
	Country     string `json:"country"`
	ISOCode     string `json:"iso_code"`
	Level       int    `json:"level"`
	LevelText   string `json:"level_text"`
	LastUpdated string `json:"last_updated"`
}

// NewAdvisoryTool creates a new travel advisory tool
func NewAdvisoryTool() *AdvisoryTool {
	tool := &AdvisoryTool{
		BaseTool: core.NewTool("travel-advisory-tool"),
		client:   NewStateGovClient(),
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all advisory-related capabilities
func (a *AdvisoryTool) registerCapabilities() {
	// Capability 1: Get Travel Advisory
	a.RegisterCapability(core.Capability{
		Name:        "get_travel_advisory",
		Description: "Gets the official US State Department travel safety advisory for a specific country.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleGetAdvisory,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "country",
					Type:        "string",
					Example:     "Thailand",
					Description: "Full country name for travel advisory lookup",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "country", Type: "string", Description: "Full country name"},
				{Name: "iso_code", Type: "string", Description: "ISO country code"},
				{Name: "level", Type: "number", Description: "Advisory risk level (1-4)"},
				{Name: "level_text", Type: "string", Description: "Human-readable risk level description"},
				{Name: "description", Type: "string", Description: "Detailed advisory description"},
				{Name: "last_updated", Type: "string", Description: "Date the advisory was last updated"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
	})

	// Capability 2: List Advisories
	a.RegisterCapability(core.Capability{
		Name:        "list_advisories",
		Description: "Lists all country travel advisories, optionally filtered by risk level.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     a.handleListAdvisories,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "level",
					Type:        "integer",
					Example:     "4",
					Description: "Filter by risk level: 1=Normal, 2=Increased Caution, 3=Reconsider, 4=Do Not Travel",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "advisories", Type: "array", Description: "Array of advisory summary objects with country, iso_code, level, level_text, last_updated"},
				{Name: "count", Type: "number", Description: "Total number of advisories returned"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "level", Type: "number", Description: "Risk level filter applied, if any"},
			},
		},
	})
}
