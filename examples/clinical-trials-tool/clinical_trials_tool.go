package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// ClinicalTrialsTool provides clinical trial search capabilities via ClinicalTrials.gov v2 API
// It follows the passive tool pattern - registers with the mesh but does not discover other tools
type ClinicalTrialsTool struct {
	*core.BaseTool
	client *ClinicalTrialsClient
}

// SearchTrialsRequest represents the input for searching clinical trials
type SearchTrialsRequest struct {
	Condition    string `json:"condition"`              // Required: disease or condition (e.g., "lung cancer")
	Intervention string `json:"intervention,omitempty"` // Optional: drug or treatment (e.g., "pembrolizumab")
	Phase        string `json:"phase,omitempty"`        // Optional: PHASE1, PHASE2, PHASE3, PHASE4, EARLY_PHASE1
	Status       string `json:"status,omitempty"`       // Optional: RECRUITING, COMPLETED, ACTIVE_NOT_RECRUITING, etc.
	MaxResults   int    `json:"max_results,omitempty"`  // Optional: 1-1000, default 10
}

// GetTrialRequest represents the input for retrieving a specific trial
type GetTrialRequest struct {
	NCTID string `json:"nct_id"` // Required: ClinicalTrials.gov NCT identifier (e.g., "NCT04280705")
}

// SearchByLocationRequest represents the input for location-based trial search
type SearchByLocationRequest struct {
	Condition  string `json:"condition"`             // Required: disease or condition
	Country    string `json:"country"`               // Required: country name (e.g., "Japan")
	City       string `json:"city,omitempty"`        // Optional: city name (e.g., "Tokyo")
	Status     string `json:"status,omitempty"`      // Optional: trial status filter
	MaxResults int    `json:"max_results,omitempty"` // Optional: 1-1000, default 10
}

// ClinicalTrial represents a flattened clinical trial
// Flattened from deeply nested API response:
//
//	protocolSection.identificationModule  -> nct_id, title, sponsor
//	protocolSection.statusModule          -> status, start_date, completion_date
//	protocolSection.designModule          -> phase, enrollment
//	protocolSection.conditionsModule      -> conditions
//	protocolSection.armsInterventionsModule -> interventions
//	protocolSection.contactsLocationsModule -> locations
type ClinicalTrial struct {
	NCTID          string   `json:"nct_id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	Phase          string   `json:"phase"` // Joined from phases array (e.g., "PHASE2, PHASE3")
	Conditions     []string `json:"conditions"`
	Interventions  []string `json:"interventions"`
	Locations      []string `json:"locations"`  // Formatted as "Facility, City, Country"
	StartDate      string   `json:"start_date"` // API returns "YYYY-MM" or "YYYY-MM-DD" inconsistently
	CompletionDate string   `json:"completion_date"`
	Enrollment     int      `json:"enrollment"` // From enrollmentInfo.count (integer)
	Sponsor        string   `json:"sponsor"`
	Source         string   `json:"source"`
}

// SearchTrialsResponse represents the output for trial search
type SearchTrialsResponse struct {
	Condition  string          `json:"condition"`
	Trials     []ClinicalTrial `json:"trials"`
	TotalCount int             `json:"total_count"`
	Source     string          `json:"source"`
}

// GetTrialResponse represents the output for a single trial lookup
type GetTrialResponse struct {
	Trial  ClinicalTrial `json:"trial"`
	Source string        `json:"source"`
}

// SearchByLocationResponse represents the output for location-based search
type SearchByLocationResponse struct {
	Condition string          `json:"condition"`
	Country   string          `json:"country"`
	City      string          `json:"city,omitempty"`
	Trials    []ClinicalTrial `json:"trials"`
	Source    string          `json:"source"`
}

func NewClinicalTrialsTool() *ClinicalTrialsTool {
	tool := &ClinicalTrialsTool{
		BaseTool: core.NewTool("clinical-trials-tool"),
		client:   NewClinicalTrialsClient(),
	}
	tool.registerCapabilities()
	return tool
}

func (t *ClinicalTrialsTool) registerCapabilities() {
	// Capability 1: search_trials
	t.RegisterCapability(core.Capability{
		Name:        "search_trials",
		Description: "Searches clinical trials on ClinicalTrials.gov by condition, intervention, phase, and status.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchTrials,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "condition", Type: "string", Description: "The condition that was searched"},
				{Name: "trials", Type: "array", Description: "List of clinical trials with nct_id, title, status, phase, conditions, interventions, locations, start_date, completion_date, enrollment, sponsor, source"},
				{Name: "total_count", Type: "number", Description: "Total number of matching trials"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "condition",
					Type:        "string",
					Example:     "lung cancer",
					Description: "Disease or condition to search for",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "intervention",
					Type:        "string",
					Example:     "pembrolizumab",
					Description: "Drug or treatment name",
				},
				{
					Name:        "phase",
					Type:        "string",
					Example:     "PHASE3",
					Description: "Trial phase: PHASE1, PHASE2, PHASE3, PHASE4, EARLY_PHASE1",
				},
				{
					Name:        "status",
					Type:        "string",
					Example:     "RECRUITING",
					Description: "Trial status: RECRUITING, COMPLETED, ACTIVE_NOT_RECRUITING, etc.",
				},
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Maximum number of results to return (1-1000, default 10)",
				},
			},
		},
	})

	// Capability 2: get_trial
	t.RegisterCapability(core.Capability{
		Name:        "get_trial",
		Description: "Retrieves detailed information for a specific clinical trial by NCT identifier.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetTrial,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "trial", Type: "object", Description: "Clinical trial details with nct_id, title, status, phase, conditions, interventions, locations, start_date, completion_date, enrollment, sponsor, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "nct_id",
					Type:        "string",
					Example:     "NCT04280705",
					Description: "ClinicalTrials.gov NCT identifier",
				},
			},
		},
	})

	// Capability 3: search_by_location
	t.RegisterCapability(core.Capability{
		Name:        "search_by_location",
		Description: "Finds clinical trials near a geographic location by country and optionally city.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchByLocation,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "condition", Type: "string", Description: "The condition that was searched"},
				{Name: "country", Type: "string", Description: "The country that was searched"},
				{Name: "trials", Type: "array", Description: "List of clinical trials with nct_id, title, status, phase, conditions, interventions, locations, start_date, completion_date, enrollment, sponsor, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "city", Type: "string", Description: "The city that was searched, if specified"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "condition",
					Type:        "string",
					Example:     "lung cancer",
					Description: "Disease or condition to search for",
				},
				{
					Name:        "country",
					Type:        "string",
					Example:     "Japan",
					Description: "Country name for location filtering",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "city",
					Type:        "string",
					Example:     "Tokyo",
					Description: "City name for more specific location filtering",
				},
				{
					Name:        "status",
					Type:        "string",
					Example:     "RECRUITING",
					Description: "Trial status filter: RECRUITING, COMPLETED, etc.",
				},
				{
					Name:        "max_results",
					Type:        "number",
					Example:     "10",
					Description: "Maximum number of results to return (1-1000, default 10)",
				},
			},
		},
	})
}
