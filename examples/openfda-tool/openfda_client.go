package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	fdaBaseURL = "https://api.fda.gov"
)

// FDAClient handles API communication with the OpenFDA API
type FDAClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewFDAClient creates a new FDA API client with traced HTTP client
// for distributed tracing visibility into external API calls.
func NewFDAClient(apiKey string) *FDAClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second // 30s timeout (FDA can be slow)

	return &FDAClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// FDAMetaResults represents the meta.results block (real integers, not strings)
type FDAMetaResults struct {
	Skip  int `json:"skip"`
	Limit int `json:"limit"`
	Total int `json:"total"`
}

// FDAMeta represents the top-level meta block
type FDAMeta struct {
	Results FDAMetaResults `json:"results"`
}

// --- Drug Adverse Events (FAERS) ---

// FDAAdverseEventResponse is the top-level response from /drug/event.json
type FDAAdverseEventResponse struct {
	Meta    FDAMeta           `json:"meta"`
	Results []FDAAdverseEvent `json:"results"`
}

// FDAAdverseEvent represents a single adverse event from the raw API
type FDAAdverseEvent struct {
	SafetyReportID string     `json:"safetyreportid"`
	ReceiveDate    string     `json:"receivedate"`  // YYYYMMDD
	Serious        string     `json:"serious"`      // "1" or "2" (STRING)
	Patient        FDAPatient `json:"patient"`
}

// FDAPatient represents the patient sub-object
type FDAPatient struct {
	PatientSex      string           `json:"patientsex"`                // "1", "2", "0" (STRING)
	PatientOnsetAge string           `json:"patientonsetage,omitempty"` // Age as STRING
	Reaction        []FDAReaction    `json:"reaction"`
	Drug            []FDAPatientDrug `json:"drug"`
}

// FDAReaction represents a single reaction
type FDAReaction struct {
	ReactionMedDRAPT string `json:"reactionmeddrapt"`
}

// FDAPatientDrug represents a drug in the patient record
type FDAPatientDrug struct {
	MedicinalProduct string          `json:"medicinalproduct"`
	OpenFDA          *FDAOpenFDADrug `json:"openfda,omitempty"`
}

// FDAOpenFDADrug represents the openfda sub-object in drug endpoints
// Values are arrays of strings in drug endpoints
type FDAOpenFDADrug struct {
	BrandName   []string `json:"brand_name,omitempty"`
	GenericName []string `json:"generic_name,omitempty"`
}

// --- Drug Labels ---

// FDADrugLabelResponse is the top-level response from /drug/label.json
type FDADrugLabelResponse struct {
	Meta    FDAMeta        `json:"meta"`
	Results []FDADrugLabel `json:"results"`
}

// FDADrugLabel represents a single drug label from the raw API
// Note: All section fields are arrays of long strings
type FDADrugLabel struct {
	OpenFDA                 *FDAOpenFDALabel `json:"openfda,omitempty"`
	Purpose                 []string         `json:"purpose,omitempty"`
	Warnings                []string         `json:"warnings,omitempty"`
	IndicationsAndUsage     []string         `json:"indications_and_usage,omitempty"`
	DosageAndAdministration []string         `json:"dosage_and_administration,omitempty"`
	ActiveIngredient        []string         `json:"active_ingredient,omitempty"`
}

// FDAOpenFDALabel represents the openfda sub-object in label endpoints
type FDAOpenFDALabel struct {
	BrandName        []string `json:"brand_name,omitempty"`
	GenericName      []string `json:"generic_name,omitempty"`
	ManufacturerName []string `json:"manufacturer_name,omitempty"`
	Route            []string `json:"route,omitempty"`
}

// --- Drug Recalls/Enforcement ---

// FDAEnforcementResponse is the top-level response from /drug/enforcement.json
type FDAEnforcementResponse struct {
	Meta    FDAMeta          `json:"meta"`
	Results []FDAEnforcement `json:"results"`
}

// FDAEnforcement represents a single recall/enforcement record
type FDAEnforcement struct {
	RecallNumber       string `json:"recall_number"`
	ReasonForRecall    string `json:"reason_for_recall"`
	Classification     string `json:"classification"`
	Status             string `json:"status"`
	ProductDescription string `json:"product_description"`
	RecallingFirm      string `json:"recalling_firm"`
	City               string `json:"city"`
	State              string `json:"state"`
	ReportDate         string `json:"report_date"` // YYYYMMDD
}

// --- Medical Device Events (MAUDE) ---

// FDADeviceEventResponse is the top-level response from /device/event.json
type FDADeviceEventResponse struct {
	Meta    FDAMeta          `json:"meta"`
	Results []FDADeviceEvent `json:"results"`
}

// FDADeviceEvent represents a single device adverse event
type FDADeviceEvent struct {
	ReportNumber string             `json:"report_number"`
	DateReceived string             `json:"date_received"` // YYYYMMDD
	EventType    string             `json:"event_type"`
	Device       []FDADevice        `json:"device"`
	Patient      []FDADevicePatient `json:"patient,omitempty"`
	MDRText      []FDAMDRText       `json:"mdr_text,omitempty"`
}

// FDADevice represents a device in the event record
// Note: openfda values are plain strings in device endpoints, NOT arrays
type FDADevice struct {
	GenericName             string            `json:"generic_name"`
	BrandName               string            `json:"brand_name"`
	ManufacturerDName       string            `json:"manufacturer_d_name"`
	DeviceReportProductCode string            `json:"device_report_product_code,omitempty"`
	OpenFDA                 *FDAOpenFDADevice `json:"openfda,omitempty"`
}

// FDAOpenFDADevice represents the openfda sub-object in device endpoints
// Note: In device endpoints, values may be plain strings OR arrays -- handle both
type FDAOpenFDADevice struct {
	DeviceName json.RawMessage `json:"device_name,omitempty"` // May be string or []string
}

// FDADevicePatient represents patient outcome in device events
type FDADevicePatient struct {
	PatientSequenceNumber  string   `json:"patient_sequence_number,omitempty"`
	SequenceNumberOutcome  []string `json:"sequence_number_outcome,omitempty"` // e.g., ["Death", "Hospitalization"]
}

// FDAMDRText represents the MDR text narrative
type FDAMDRText struct {
	Text     string `json:"text"`
	TextType string `json:"text_type_code"` // e.g., "Description of Event or Problem"
}

// buildSearchQuery constructs the OpenFDA search query string.
// OpenFDA query syntax: ?search=field:"value"+AND+field2:"value2"&limit=N
// If apiKey is set, it's appended as &api_key=xxx
func (c *FDAClient) buildURL(endpoint string, searchTerms []string, limit int) string {
	params := url.Values{}

	if len(searchTerms) > 0 {
		params.Set("search", strings.Join(searchTerms, "+AND+"))
	}

	if limit > 0 {
		params.Set("limit", fmt.Sprintf("%d", limit))
	}

	if c.apiKey != "" {
		params.Set("api_key", c.apiKey)
	}

	return fmt.Sprintf("%s%s?%s", fdaBaseURL, endpoint, params.Encode())
}

// SearchAdverseEvents searches the FAERS database for drug adverse events
func (c *FDAClient) SearchAdverseEvents(ctx context.Context, drugName string, serious *bool, limit int) (*FDAAdverseEventResponse, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	searchTerms := []string{
		fmt.Sprintf(`patient.drug.openfda.brand_name:"%s"`, drugName),
	}
	if serious != nil && *serious {
		searchTerms = append(searchTerms, `serious:"1"`)
	}

	fullURL := c.buildURL("/drug/event.json", searchTerms, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result FDAAdverseEventResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// SearchDrugLabels searches FDA-approved drug labeling
func (c *FDAClient) SearchDrugLabels(ctx context.Context, query string, limit int) (*FDADrugLabelResponse, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}

	searchTerms := []string{
		fmt.Sprintf(`openfda.brand_name:"%s"`, query),
	}

	fullURL := c.buildURL("/drug/label.json", searchTerms, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result FDADrugLabelResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// SearchDrugRecalls searches drug enforcement/recall reports
func (c *FDAClient) SearchDrugRecalls(ctx context.Context, drugName, classification, status string, limit int) (*FDAEnforcementResponse, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	var searchTerms []string
	if drugName != "" {
		searchTerms = append(searchTerms, fmt.Sprintf(`product_description:"%s"`, drugName))
	}
	if classification != "" {
		searchTerms = append(searchTerms, fmt.Sprintf(`classification:"%s"`, classification))
	}
	if status != "" {
		searchTerms = append(searchTerms, fmt.Sprintf(`status:"%s"`, status))
	}

	fullURL := c.buildURL("/drug/enforcement.json", searchTerms, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result FDAEnforcementResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// SearchDeviceEvents searches medical device adverse events (MAUDE)
func (c *FDAClient) SearchDeviceEvents(ctx context.Context, deviceName string, limit int) (*FDADeviceEventResponse, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	searchTerms := []string{
		fmt.Sprintf(`device.generic_name:"%s"`, deviceName),
	}

	fullURL := c.buildURL("/device/event.json", searchTerms, limit)

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result FDADeviceEventResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// truncate limits a string to maxLen characters, appending "..." if truncated.
// Used for drug label sections which can be thousands of characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// firstOrEmpty returns the first element of a string slice, or "" if empty.
// Used for FDA label fields which are []string but typically have one element.
func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
