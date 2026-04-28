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

const geoapifyBaseURL = "https://api.geoapify.com/v2"

// GeoapifyClient handles API communication with Geoapify Places v2
type GeoapifyClient struct {
	apiKey     string
	httpClient *http.Client
}

// Geoapify raw response types
type geoapifySearchResponse struct {
	Features []geoapifyFeature `json:"features"`
}

type geoapifyFeature struct {
	Type       string              `json:"type"`
	Properties geoapifyProperties  `json:"properties"`
	Geometry   geoapifyGeometry    `json:"geometry"`
}

type geoapifyProperties struct {
	PlaceID    string   `json:"place_id"`
	Name       string   `json:"name"`
	Street     string   `json:"street"`
	HouseNum   string   `json:"housenumber"`
	City       string   `json:"city"`
	State      string   `json:"state"`
	Country    string   `json:"country"`
	PostCode   string   `json:"postcode"`
	Formatted  string   `json:"formatted"`
	Categories []string `json:"categories"`
	Distance   int      `json:"distance"`
	Phone      string   `json:"contact:phone"`
	Website    string   `json:"contact:website"`
	Lat        float64  `json:"lat"`
	Lon        float64  `json:"lon"`
}

type geoapifyGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"` // [lon, lat]
}

// NewGeoapifyClient creates a new Geoapify Places client
func NewGeoapifyClient(apiKey string) *GeoapifyClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 15 * time.Second

	return &GeoapifyClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// SearchPlaces searches for places using Geoapify
func (c *GeoapifyClient) SearchPlaces(ctx context.Context, req SearchPlacesRequest) ([]PlaceResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Geoapify API key not configured")
	}

	params := url.Values{}
	params.Set("text", req.Query)
	params.Set("apiKey", c.apiKey)
	params.Set("format", "geojson")

	if req.Lat != 0 && req.Lon != 0 {
		params.Set("bias", fmt.Sprintf("proximity:%f,%f", req.Lon, req.Lat))
	}
	if req.Categories != "" {
		params.Set("categories", req.Categories)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	params.Set("limit", strconv.Itoa(limit))

	endpoint := fmt.Sprintf("%s/places?%s", geoapifyBaseURL, params.Encode())
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw geoapifySearchResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	results := make([]PlaceResult, 0, len(raw.Features))
	for _, f := range raw.Features {
		lat, lon := f.Properties.Lat, f.Properties.Lon
		if len(f.Geometry.Coordinates) >= 2 {
			lon = f.Geometry.Coordinates[0]
			lat = f.Geometry.Coordinates[1]
		}

		results = append(results, PlaceResult{
			ID:         f.Properties.PlaceID,
			Name:       f.Properties.Name,
			Address:    f.Properties.Formatted,
			Lat:        lat,
			Lon:        lon,
			Categories: f.Properties.Categories,
			Distance:   f.Properties.Distance,
			Provider:   "geoapify",
		})
	}

	return results, nil
}

// GetPlaceDetails gets detailed information about a specific place
func (c *GeoapifyClient) GetPlaceDetails(ctx context.Context, placeID string) (*PlaceDetailsResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Geoapify API key not configured")
	}

	params := url.Values{}
	params.Set("apiKey", c.apiKey)

	endpoint := fmt.Sprintf("%s/place-details?id=%s&%s", geoapifyBaseURL, url.QueryEscape(placeID), params.Encode())
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw geoapifySearchResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(raw.Features) == 0 {
		return nil, fmt.Errorf("API error 404: place not found: %s", placeID)
	}

	f := raw.Features[0]
	lat, lon := f.Properties.Lat, f.Properties.Lon
	if len(f.Geometry.Coordinates) >= 2 {
		lon = f.Geometry.Coordinates[0]
		lat = f.Geometry.Coordinates[1]
	}

	return &PlaceDetailsResponse{
		ID:         f.Properties.PlaceID,
		Name:       f.Properties.Name,
		Address:    f.Properties.Formatted,
		Lat:        lat,
		Lon:        lon,
		Categories: f.Properties.Categories,
		Phone:      f.Properties.Phone,
		Website:    f.Properties.Website,
		Provider:   "geoapify",
		Source:     "Geoapify Places API",
	}, nil
}

// NearbyPlaces finds places by category near coordinates
func (c *GeoapifyClient) NearbyPlaces(ctx context.Context, req NearbyPlacesRequest) ([]PlaceResult, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("Geoapify API key not configured")
	}

	params := url.Values{}
	params.Set("apiKey", c.apiKey)
	params.Set("format", "geojson")
	params.Set("filter", fmt.Sprintf("circle:%f,%f,%d", req.Lon, req.Lat, max(req.Radius, 1000)))

	if req.Categories != "" {
		params.Set("categories", req.Categories)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	params.Set("limit", strconv.Itoa(limit))

	endpoint := fmt.Sprintf("%s/places?%s", geoapifyBaseURL, params.Encode())
	body, err := c.doRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw geoapifySearchResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	results := make([]PlaceResult, 0, len(raw.Features))
	for _, f := range raw.Features {
		lat, lon := f.Properties.Lat, f.Properties.Lon
		if len(f.Geometry.Coordinates) >= 2 {
			lon = f.Geometry.Coordinates[0]
			lat = f.Geometry.Coordinates[1]
		}

		results = append(results, PlaceResult{
			ID:         f.Properties.PlaceID,
			Name:       f.Properties.Name,
			Address:    f.Properties.Formatted,
			Lat:        lat,
			Lon:        lon,
			Categories: f.Properties.Categories,
			Distance:   f.Properties.Distance,
			Provider:   "geoapify",
		})
	}

	return results, nil
}

// doRequest executes a GET request (API key passed as query param for Geoapify)
func (c *GeoapifyClient) doRequest(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

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
		return nil, fmt.Errorf("Geoapify API returned status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
