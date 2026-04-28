package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// OpenFDATool is a focused tool that provides FDA data capabilities via the OpenFDA API.
// It demonstrates the passive tool pattern -- can register but not discover.
type OpenFDATool struct {
	*core.BaseTool
	apiKey string
	client *FDAClient
}

// NewOpenFDATool creates a new OpenFDA tool
func NewOpenFDATool() *OpenFDATool {
	apiKey := os.Getenv("OPENFDA_API_KEY")

	tool := &OpenFDATool{
		BaseTool: core.NewTool("openfda-tool"),
		apiKey:   apiKey,
		client:   NewFDAClient(apiKey),
	}

	tool.registerCapabilities()
	return tool
}

// SearchAdverseEventsRequest represents the input for drug adverse event searches
type SearchAdverseEventsRequest struct {
	DrugName string `json:"drug_name"`         // Required: Drug brand name or generic name
	Serious  *bool  `json:"serious,omitempty"`  // Optional: Filter to serious events only
	Limit    int    `json:"limit,omitempty"`    // Optional: Max results, 1-100
}

// SearchDrugLabelsRequest represents the input for drug label searches
type SearchDrugLabelsRequest struct {
	Query string `json:"query"`          // Required: Drug name or active ingredient
	Limit int    `json:"limit,omitempty"` // Optional: Max results, 1-100
}

// SearchDrugRecallsRequest represents the input for drug recall searches
type SearchDrugRecallsRequest struct {
	DrugName       string `json:"drug_name,omitempty"`       // Optional: Drug name to filter recalls
	Classification string `json:"classification,omitempty"`  // Optional: Recall severity (e.g., "Class I")
	Status         string `json:"status,omitempty"`          // Optional: Recall status (e.g., "Ongoing")
	Limit          int    `json:"limit,omitempty"`           // Optional: Max results, 1-100
}

// SearchDeviceEventsRequest represents the input for device adverse event searches
type SearchDeviceEventsRequest struct {
	DeviceName string `json:"device_name"`      // Required: Medical device name
	Limit      int    `json:"limit,omitempty"`   // Optional: Max results, 1-100
}

// AdverseEventResponse represents a single drug adverse event report
type AdverseEventResponse struct {
	SafetyReportID  string   `json:"safety_report_id"`
	ReceiveDate     string   `json:"receive_date"`       // YYYYMMDD format
	Serious         string   `json:"serious"`            // "1" or "2" (string, NOT bool)
	Reactions       []string `json:"reactions"`           // patient.reaction[].reactionmeddrapt
	DrugNames       []string `json:"drug_names"`          // patient.drug[].medicinalproduct
	PatientSex      string   `json:"patient_sex"`         // "1"=male, "2"=female, "0"=unknown (string)
	PatientOnsetAge string   `json:"patient_onset_age,omitempty"` // Age as string
	Source          string   `json:"source"`
}

// SearchAdverseEventsResponse wraps multiple adverse event results
type SearchAdverseEventsResponse struct {
	DrugName string                 `json:"drug_name"`
	Total    int                    `json:"total"`         // meta.results.total (real int)
	Events   []AdverseEventResponse `json:"events"`
	Source   string                 `json:"source"`
}

// DrugLabelResponse represents a single FDA-approved drug label
type DrugLabelResponse struct {
	BrandName       string   `json:"brand_name,omitempty"`
	GenericName     string   `json:"generic_name,omitempty"`
	Manufacturer    string   `json:"manufacturer,omitempty"`
	Purpose         string   `json:"purpose,omitempty"`          // Truncated to 500 chars
	Warnings        string   `json:"warnings,omitempty"`         // Truncated to 500 chars
	Indications     string   `json:"indications_and_usage,omitempty"` // Truncated to 500 chars
	DosageAndAdmin  string   `json:"dosage_and_administration,omitempty"` // Truncated to 500 chars
	ActiveIngredient string  `json:"active_ingredient,omitempty"`
	Route           []string `json:"route,omitempty"`
	Source          string   `json:"source"`
}

// SearchDrugLabelsResponse wraps multiple drug label results
type SearchDrugLabelsResponse struct {
	Query  string              `json:"query"`
	Total  int                 `json:"total"`
	Labels []DrugLabelResponse `json:"labels"`
	Source string              `json:"source"`
}

// DrugRecallResponse represents a single drug enforcement/recall report
type DrugRecallResponse struct {
	RecallNumber       string `json:"recall_number"`
	ReasonForRecall    string `json:"reason_for_recall"`
	Classification     string `json:"classification"`      // "Class I", "Class II", "Class III"
	Status             string `json:"status"`               // "Ongoing", "Terminated", etc.
	ProductDescription string `json:"product_description"`
	RecallingFirm      string `json:"recalling_firm"`
	City               string `json:"city,omitempty"`
	State              string `json:"state,omitempty"`
	ReportDate         string `json:"report_date"`          // YYYYMMDD format
	Source             string `json:"source"`
}

// SearchDrugRecallsResponse wraps multiple drug recall results
type SearchDrugRecallsResponse struct {
	DrugName string               `json:"drug_name,omitempty"`
	Total    int                  `json:"total"`
	Recalls  []DrugRecallResponse `json:"recalls"`
	Source   string               `json:"source"`
}

// DeviceEventResponse represents a single medical device adverse event
type DeviceEventResponse struct {
	ReportNumber     string   `json:"report_number"`
	DateReceived     string   `json:"date_received"`     // YYYYMMDD format
	EventType        string   `json:"event_type"`
	DeviceName       string   `json:"device_name"`
	Manufacturer     string   `json:"manufacturer"`
	BrandName        string   `json:"brand_name,omitempty"`
	ProductCode      string   `json:"product_code,omitempty"`
	PatientOutcome   []string `json:"patient_outcome,omitempty"`
	EventDescription string   `json:"event_description,omitempty"` // Truncated to 500 chars
	Source           string   `json:"source"`
}

// SearchDeviceEventsResponse wraps multiple device event results
type SearchDeviceEventsResponse struct {
	DeviceName string                `json:"device_name"`
	Total      int                   `json:"total"`
	Events     []DeviceEventResponse `json:"events"`
	Source     string                `json:"source"`
}

func (t *OpenFDATool) registerCapabilities() {
	// Capability 1: Search Adverse Events (Drug FAERS)
	// Auto-generated endpoint: /api/capabilities/search_adverse_events
	t.RegisterCapability(core.Capability{
		Name: "search_adverse_events",
		Description: "Searches FDA drug adverse event reports (FAERS database) by drug name.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchAdverseEvents,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "drug_name", Type: "string", Description: "The drug name that was searched"},
				{Name: "total", Type: "number", Description: "Total number of adverse event reports"},
				{Name: "events", Type: "array", Description: "List of adverse event reports with safety_report_id, receive_date, serious, reactions, drug_names, patient_sex, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "drug_name",
					Type:        "string",
					Example:     "aspirin",
					Description: "Drug brand name (searches FDA brand_name field, e.g. aspirin, Tylenol, Lipitor)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "serious",
					Type:        "boolean",
					Example:     "true",
					Description: "Filter to serious events only",
				},
				{
					Name:        "limit",
					Type:        "number",
					Example:     "10",
					Description: "Max results, 1-100",
				},
			},
		},
	})

	// Capability 2: Search Drug Labels
	// Auto-generated endpoint: /api/capabilities/search_drug_labels
	t.RegisterCapability(core.Capability{
		Name: "search_drug_labels",
		Description: "Searches FDA-approved drug labeling including dosage, warnings, and indications.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchDrugLabels,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "The drug query that was searched"},
				{Name: "total", Type: "number", Description: "Total number of matching labels"},
				{Name: "labels", Type: "array", Description: "List of drug labels with brand_name, generic_name, manufacturer, purpose, warnings, indications_and_usage, dosage_and_administration, active_ingredient, route, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "aspirin",
					Description: "Drug brand name (searches FDA brand_name field, e.g. aspirin, Tylenol, Lipitor)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "limit",
					Type:        "number",
					Example:     "5",
					Description: "Max results, 1-100",
				},
			},
		},
	})

	// Capability 3: Search Drug Recalls
	// Auto-generated endpoint: /api/capabilities/search_drug_recalls
	t.RegisterCapability(core.Capability{
		Name: "search_drug_recalls",
		Description: "Searches FDA drug enforcement and recall reports.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchDrugRecalls,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "total", Type: "number", Description: "Total number of matching recall reports"},
				{Name: "recalls", Type: "array", Description: "List of drug recall reports with recall_number, reason_for_recall, classification, status, product_description, recalling_firm, report_date, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "drug_name", Type: "string", Description: "The drug name filter that was applied, if specified"},
			},
		},
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "drug_name",
					Type:        "string",
					Example:     "metformin",
					Description: "Drug name to filter recalls",
				},
				{
					Name:        "classification",
					Type:        "string",
					Example:     "Class I",
					Description: "Recall severity: Class I (most severe), Class II, or Class III",
				},
				{
					Name:        "status",
					Type:        "string",
					Example:     "Ongoing",
					Description: "Recall status: Ongoing, Terminated, etc.",
				},
				{
					Name:        "limit",
					Type:        "number",
					Example:     "10",
					Description: "Max results, 1-100",
				},
			},
		},
	})

	// Capability 4: Search Device Events
	// Auto-generated endpoint: /api/capabilities/search_device_events
	t.RegisterCapability(core.Capability{
		Name: "search_device_events",
		Description: "Searches FDA medical device adverse event reports (MAUDE database).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchDeviceEvents,
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "device_name", Type: "string", Description: "The device name that was searched"},
				{Name: "total", Type: "number", Description: "Total number of device event reports"},
				{Name: "events", Type: "array", Description: "List of device event reports with report_number, date_received, event_type, device_name, manufacturer, source"},
				{Name: "source", Type: "string", Description: "Data source identifier"},
			},
		},
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "device_name",
					Type:        "string",
					Example:     "pacemaker",
					Description: "Medical device name",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "limit",
					Type:        "number",
					Example:     "10",
					Description: "Max results, 1-100",
				},
			},
		},
	})
}
