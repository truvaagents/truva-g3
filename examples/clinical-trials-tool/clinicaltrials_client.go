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
	clinicalTrialsBaseURL = "https://clinicaltrials.gov/api/v2"

	// fields parameter to limit response size (responses can be 200KB+ without this)
	defaultStudyFields = "NCTId,BriefTitle,OverallStatus,Phase,Condition," +
		"InterventionName,LocationFacility,LocationCity,LocationCountry," +
		"StartDate,CompletionDate,EnrollmentCount,LeadSponsorName"
)

type ClinicalTrialsClient struct {
	httpClient *http.Client
}

func NewClinicalTrialsClient() *ClinicalTrialsClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second // 30s timeout (longer than stock-tool's 10s due to larger responses)

	return &ClinicalTrialsClient{
		httpClient: tracedClient,
	}
}

// ctGovSearchResponse represents the top-level search response
// Pagination is token-based: nextPageToken key is ABSENT (not null) when no more pages
type ctGovSearchResponse struct {
	Studies       []ctGovStudy `json:"studies"`
	TotalCount    int          `json:"totalCount"`
	NextPageToken string       `json:"nextPageToken,omitempty"`
}

// ctGovStudy represents a single study from the API
// Single study endpoint returns this object directly (NOT wrapped in {studies:[...]})
type ctGovStudy struct {
	ProtocolSection ctGovProtocolSection `json:"protocolSection"`
}

// ctGovProtocolSection contains all nested modules
type ctGovProtocolSection struct {
	IdentificationModule       ctGovIdentificationModule    `json:"identificationModule"`
	StatusModule               ctGovStatusModule            `json:"statusModule"`
	DesignModule               ctGovDesignModule            `json:"designModule"`
	ConditionsModule           ctGovConditionsModule        `json:"conditionsModule"`
	ArmsInterventionsModule    ctGovArmsInterventionsModule `json:"armsInterventionsModule"`
	ContactsLocationsModule    ctGovContactsLocationsModule `json:"contactsLocationsModule"`
	SponsorCollaboratorsModule ctGovSponsorModule           `json:"sponsorCollaboratorsModule"`
}

type ctGovIdentificationModule struct {
	NCTId      string `json:"nctId"`
	BriefTitle string `json:"briefTitle"`
}

type ctGovStatusModule struct {
	OverallStatus        string    `json:"overallStatus"`
	StartDateStruct      ctGovDate `json:"startDateStruct"`
	CompletionDateStruct ctGovDate `json:"completionDateStruct"`
}

type ctGovDate struct {
	Date string `json:"date"` // Inconsistent: "YYYY-MM" or "YYYY-MM-DD" in same response
}

type ctGovDesignModule struct {
	Phases         []string        `json:"phases"` // Array, e.g., ["PHASE2", "PHASE3"]
	EnrollmentInfo ctGovEnrollment `json:"enrollmentInfo"`
}

type ctGovEnrollment struct {
	Count int `json:"count"` // Integer, not string
}

type ctGovConditionsModule struct {
	Conditions []string `json:"conditions"`
}

type ctGovArmsInterventionsModule struct {
	Interventions []ctGovIntervention `json:"interventions"`
}

type ctGovIntervention struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type ctGovContactsLocationsModule struct {
	Locations []ctGovLocation `json:"locations"`
}

type ctGovLocation struct {
	Facility string `json:"facility"`
	City     string `json:"city"`
	Country  string `json:"country"`
}

type ctGovSponsorModule struct {
	LeadSponsor ctGovSponsor `json:"leadSponsor"`
}

type ctGovSponsor struct {
	Name string `json:"name"`
}

// flattenStudy converts the deeply nested API response to our flat ClinicalTrial struct
func flattenStudy(study ctGovStudy) ClinicalTrial {
	ps := study.ProtocolSection

	// Flatten interventions to name strings
	interventions := make([]string, 0, len(ps.ArmsInterventionsModule.Interventions))
	for _, intr := range ps.ArmsInterventionsModule.Interventions {
		interventions = append(interventions, intr.Name)
	}

	// Flatten locations to "Facility, City, Country" strings
	locations := make([]string, 0, len(ps.ContactsLocationsModule.Locations))
	for _, loc := range ps.ContactsLocationsModule.Locations {
		parts := []string{}
		if loc.Facility != "" {
			parts = append(parts, loc.Facility)
		}
		if loc.City != "" {
			parts = append(parts, loc.City)
		}
		if loc.Country != "" {
			parts = append(parts, loc.Country)
		}
		locations = append(locations, strings.Join(parts, ", "))
	}

	// Join phases array into single string (e.g., "PHASE2, PHASE3")
	phase := strings.Join(ps.DesignModule.Phases, ", ")

	return ClinicalTrial{
		NCTID:          ps.IdentificationModule.NCTId,
		Title:          ps.IdentificationModule.BriefTitle,
		Status:         ps.StatusModule.OverallStatus,
		Phase:          phase,
		Conditions:     ps.ConditionsModule.Conditions,
		Interventions:  interventions,
		Locations:      locations,
		StartDate:      ps.StatusModule.StartDateStruct.Date,
		CompletionDate: ps.StatusModule.CompletionDateStruct.Date,
		Enrollment:     ps.DesignModule.EnrollmentInfo.Count,
		Sponsor:        ps.SponsorCollaboratorsModule.LeadSponsor.Name,
		Source:         "ClinicalTrials.gov",
	}
}

func (c *ClinicalTrialsClient) SearchTrials(ctx context.Context, condition, intervention, phase, status string, maxResults int) ([]ClinicalTrial, int, error) {
	if maxResults <= 0 || maxResults > 1000 {
		maxResults = 10
	}

	params := url.Values{}
	params.Set("query.cond", condition)
	if intervention != "" {
		params.Set("query.intr", intervention)
	}
	if status != "" {
		params.Set("filter.overallStatus", status)
	}
	if phase != "" {
		// filter.phase is NOT a valid v2 API parameter (returns HTTP 400)
		// Must use filter.advanced with AREA[Phase] syntax instead
		params.Set("filter.advanced", fmt.Sprintf("AREA[Phase]%s", phase))
	}
	params.Set("pageSize", fmt.Sprintf("%d", maxResults))
	// countTotal=true is required — without it, totalCount is null (not 0, null)
	params.Set("countTotal", "true")
	// Use fields param to limit response size (responses can be 200KB+ without this)
	params.Set("fields", defaultStudyFields)

	fullURL := fmt.Sprintf("%s/studies?%s", clinicalTrialsBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var searchResp ctGovSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, 0, fmt.Errorf("failed to decode response: %w", err)
	}

	trials := make([]ClinicalTrial, 0, len(searchResp.Studies))
	for _, study := range searchResp.Studies {
		trials = append(trials, flattenStudy(study))
	}

	return trials, searchResp.TotalCount, nil
}

func (c *ClinicalTrialsClient) GetTrial(ctx context.Context, nctID string) (*ClinicalTrial, error) {
	// Single study endpoint: /studies/{nctId}
	// Returns ctGovStudy directly, NOT wrapped in {studies:[...]}
	fullURL := fmt.Sprintf("%s/studies/%s", clinicalTrialsBaseURL, url.PathEscape(nctID))

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

	var study ctGovStudy
	if err := json.NewDecoder(resp.Body).Decode(&study); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	trial := flattenStudy(study)
	return &trial, nil
}

func (c *ClinicalTrialsClient) SearchByLocation(ctx context.Context, condition, country, city, status string, maxResults int) ([]ClinicalTrial, error) {
	if maxResults <= 0 || maxResults > 1000 {
		maxResults = 10
	}

	params := url.Values{}
	params.Set("query.cond", condition)

	// Build location query: "country:city" or just "country"
	locationQuery := country
	if city != "" {
		locationQuery = fmt.Sprintf("%s %s", city, country)
	}
	params.Set("query.locn", locationQuery)

	if status != "" {
		params.Set("filter.overallStatus", status)
	}
	params.Set("pageSize", fmt.Sprintf("%d", maxResults))
	// countTotal=true is required — without it, totalCount is null (not 0, null)
	params.Set("countTotal", "true")
	params.Set("fields", defaultStudyFields)

	fullURL := fmt.Sprintf("%s/studies?%s", clinicalTrialsBaseURL, params.Encode())

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

	var searchResp ctGovSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	trials := make([]ClinicalTrial, 0, len(searchResp.Studies))
	for _, study := range searchResp.Studies {
		trials = append(trials, flattenStudy(study))
	}

	return trials, nil
}
