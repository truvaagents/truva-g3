package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	foursquareBaseURL    = "https://places-api.foursquare.com"
	foursquareAPIVersion = "2025-06-17"
)

// FoursquareClient handles API communication with Foursquare Places v3
type FoursquareClient struct {
	apiKey     string
	httpClient *http.Client
}

// Foursquare raw response types
type foursquareSearchResponse struct {
	Results []foursquarePlace `json:"results"`
}

type foursquarePlace struct {
	FSQID      string               `json:"fsq_place_id"`
	Name       string               `json:"name"`
	Latitude   float64              `json:"latitude"`
	Longitude  float64              `json:"longitude"`
	Location   foursquareLocation   `json:"location"`
	Categories []foursquareCategory `json:"categories"`
	Distance   int                  `json:"distance"`
	Tel        string               `json:"tel"`
	Website    string               `json:"website"`
	Rating     float64              `json:"rating"`
	Hours      *foursquareHours     `json:"hours"`
}

type foursquareLocation struct {
	Address          string `json:"address"`
	Locality         string `json:"locality"`
	Region           string `json:"region"`
	Country          string `json:"country"`
	PostCode         string `json:"post_code"`
	FormattedAddress string `json:"formatted_address"`
	Latitude         float64
	Longitude        float64
}

type foursquareCategory struct {
	ID   string `json:"fsq_category_id"`
	Name string `json:"name"`
}

type foursquareHours struct {
	Display string `json:"display"`
}

// NewFoursquareClient creates a new Foursquare Places client
func NewFoursquareClient(apiKey string) *FoursquareClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 15 * time.Second

	return &FoursquareClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// SearchPlaces searches for places using Foursquare
func (c *FoursquareClient) SearchPlaces(ctx context.Context, req SearchPlacesRequest) ([]PlaceResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Foursquare API key not configured")
	}

	params := url.Values{}
	params.Set("query", req.Query)
	if req.Lat != 0 && req.Lon != 0 {
		params.Set("ll", fmt.Sprintf("%f,%f", req.Lat, req.Lon))
		// Foursquare allows radius only with ll (lat/lon), not with near
		if req.Radius > 0 {
			params.Set("radius", strconv.Itoa(req.Radius))
		}
	} else if req.Location != "" {
		params.Set("near", req.Location)
		// Do NOT set radius with near — Foursquare returns 400:
		// "May pass only one of radius (for ip biasing), ne/sw, polygon, near, or ll/radius"
	}
	if req.Categories != "" {
		params.Set("categories", req.Categories)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	params.Set("limit", strconv.Itoa(limit))

	endpoint := fmt.Sprintf("%s/places/search?%s", foursquareBaseURL, params.Encode())
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw foursquareSearchResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	results := make([]PlaceResult, 0, len(raw.Results))
	for _, p := range raw.Results {
		categories := make([]string, 0, len(p.Categories))
		for _, c := range p.Categories {
			categories = append(categories, c.Name)
		}

		results = append(results, PlaceResult{
			ID:         p.FSQID,
			Name:       p.Name,
			Address:    p.Location.FormattedAddress,
			Lat:        p.Latitude,
			Lon:        p.Longitude,
			Categories: categories,
			Distance:   p.Distance,
			Provider:   "foursquare",
		})
	}

	return results, nil
}

// GetPlaceDetails gets detailed information about a specific place
func (c *FoursquareClient) GetPlaceDetails(ctx context.Context, placeID string) (*PlaceDetailsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Foursquare API key not configured")
	}

	endpoint := fmt.Sprintf("%s/places/%s", foursquareBaseURL, placeID)
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var p foursquarePlace
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	categories := make([]string, 0, len(p.Categories))
	for _, c := range p.Categories {
		categories = append(categories, c.Name)
	}

	hours := ""
	if p.Hours != nil {
		hours = p.Hours.Display
	}

	return &PlaceDetailsResponse{
		ID:         p.FSQID,
		Name:       p.Name,
		Address:    p.Location.FormattedAddress,
		Lat:        p.Latitude,
		Lon:        p.Longitude,
		Categories: categories,
		Phone:      p.Tel,
		Website:    p.Website,
		Hours:      hours,
		Rating:     p.Rating,
		Provider:   "foursquare",
		Source:     "Foursquare Places API",
	}, nil
}

// NearbyPlaces finds places by category near coordinates
func (c *FoursquareClient) NearbyPlaces(ctx context.Context, req NearbyPlacesRequest) ([]PlaceResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Foursquare API key not configured")
	}

	params := url.Values{}
	params.Set("ll", fmt.Sprintf("%f,%f", req.Lat, req.Lon))
	if req.Categories != "" {
		params.Set("categories", req.Categories)
	}
	radius := req.Radius
	if radius <= 0 {
		radius = 1000
	}
	params.Set("radius", strconv.Itoa(radius))
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	params.Set("limit", strconv.Itoa(limit))

	endpoint := fmt.Sprintf("%s/places/search?%s", foursquareBaseURL, params.Encode())
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw foursquareSearchResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	results := make([]PlaceResult, 0, len(raw.Results))
	for _, p := range raw.Results {
		categories := make([]string, 0, len(p.Categories))
		for _, c := range p.Categories {
			categories = append(categories, c.Name)
		}

		results = append(results, PlaceResult{
			ID:         p.FSQID,
			Name:       p.Name,
			Address:    p.Location.FormattedAddress,
			Lat:        p.Latitude,
			Lon:        p.Longitude,
			Categories: categories,
			Distance:   p.Distance,
			Provider:   "foursquare",
		})
	}

	return results, nil
}

// doRequest executes an authenticated GET request
func (c *FoursquareClient) doRequest(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Places-Api-Version", foursquareAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Foursquare API returned status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
