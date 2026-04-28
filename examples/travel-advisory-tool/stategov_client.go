package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

var levelRegex = regexp.MustCompile(`Level (\d+):\s*(.+)$`)

const (
	stateGovBaseURL = "https://cadataapi.state.gov/api/TravelAdvisories"
	cacheTTL        = 1 * time.Hour // Advisories change infrequently
)

// StateGovClient handles API communication with the US State Department
type StateGovClient struct {
	httpClient *http.Client
	cache      *advisoryCache
}

// advisoryCache provides in-memory caching for advisory data
type advisoryCache struct {
	mu          sync.RWMutex
	advisories  []rawAdvisory
	lastFetched time.Time
}

// Raw API response types (matches actual State Department API schema)
type rawAdvisoryResponse struct {
	Advisories []rawAdvisory `json:"data"`
}

type rawAdvisory struct {
	Title     string   `json:"Title"`
	Link      string   `json:"Link"`
	Category  []string `json:"Category"`
	Summary   string   `json:"Summary"`
	ID        string   `json:"id"`
	Published string   `json:"Published"`
	Updated   string   `json:"Updated"`
}

// Level text mapping
var levelTextMap = map[int]string{
	1: "Exercise Normal Precautions",
	2: "Exercise Increased Caution",
	3: "Reconsider Travel",
	4: "Do Not Travel",
}

// NewStateGovClient creates a new State Department API client
func NewStateGovClient() *StateGovClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 15 * time.Second

	return &StateGovClient{
		httpClient: tracedClient,
		cache:      &advisoryCache{},
	}
}

// parseTitle extracts country name, level, and level text from the Title field.
// Format: "Country Name - Level N: Level Text"
func parseTitle(title string) (country string, level int, levelText string) {
	parts := strings.SplitN(title, " - ", 2)
	if len(parts) >= 1 {
		country = strings.TrimSpace(parts[0])
	}
	if len(parts) == 2 {
		matches := levelRegex.FindStringSubmatch(parts[1])
		if len(matches) == 3 {
			level, _ = strconv.Atoi(matches[1])
			levelText = matches[2]
		}
	}
	return
}

// GetAdvisory gets the travel advisory for a specific country
func (c *StateGovClient) GetAdvisory(ctx context.Context, country string) (*GetAdvisoryResponse, error) {
	advisories, err := c.getAllAdvisories(ctx)
	if err != nil {
		return nil, err
	}

	// Case-insensitive search
	countryLower := strings.ToLower(country)
	for _, adv := range advisories {
		advCountry, advLevel, advLevelText := parseTitle(adv.Title)
		advCountryLower := strings.ToLower(advCountry)
		if advCountryLower == countryLower ||
			strings.Contains(advCountryLower, countryLower) {
			if advLevelText == "" {
				advLevelText = levelTextMap[advLevel]
			}
			isoCode := ""
			if len(adv.Category) > 0 {
				isoCode = adv.Category[0]
			}
			return &GetAdvisoryResponse{
				Country:     advCountry,
				ISOCode:     isoCode,
				Level:       advLevel,
				LevelText:   advLevelText,
				Description: adv.Summary,
				LastUpdated: adv.Updated,
				Source:      "US State Department",
			}, nil
		}
	}

	return nil, fmt.Errorf("no advisory found for country: %s", country)
}

// ListAdvisories lists all advisories, optionally filtered by level
func (c *StateGovClient) ListAdvisories(ctx context.Context, level int) (*ListAdvisoriesResponse, error) {
	advisories, err := c.getAllAdvisories(ctx)
	if err != nil {
		return nil, err
	}

	var summaries []AdvisorySummary
	for _, adv := range advisories {
		advCountry, advLevel, advLevelText := parseTitle(adv.Title)
		if level > 0 && advLevel != level {
			continue
		}
		if advLevelText == "" {
			advLevelText = levelTextMap[advLevel]
		}
		isoCode := ""
		if len(adv.Category) > 0 {
			isoCode = adv.Category[0]
		}
		summaries = append(summaries, AdvisorySummary{
			Country:     advCountry,
			ISOCode:     isoCode,
			Level:       advLevel,
			LevelText:   advLevelText,
			LastUpdated: adv.Updated,
		})
	}

	return &ListAdvisoriesResponse{
		Advisories: summaries,
		Count:      len(summaries),
		Level:      level,
		Source:     "US State Department",
	}, nil
}

// getAllAdvisories returns cached advisories or fetches fresh data
func (c *StateGovClient) getAllAdvisories(ctx context.Context) ([]rawAdvisory, error) {
	c.cache.mu.RLock()
	if len(c.cache.advisories) > 0 && time.Since(c.cache.lastFetched) < cacheTTL {
		defer c.cache.mu.RUnlock()
		return c.cache.advisories, nil
	}
	c.cache.mu.RUnlock()

	// Fetch fresh data
	c.cache.mu.Lock()
	defer c.cache.mu.Unlock()

	// Double-check after acquiring write lock
	if len(c.cache.advisories) > 0 && time.Since(c.cache.lastFetched) < cacheTTL {
		return c.cache.advisories, nil
	}

	advisories, err := c.fetchAdvisories(ctx)
	if err != nil {
		// If we have stale data, return it on error
		if len(c.cache.advisories) > 0 {
			return c.cache.advisories, nil
		}
		return nil, err
	}

	c.cache.advisories = advisories
	c.cache.lastFetched = time.Now()
	return advisories, nil
}

// fetchAdvisories fetches all advisories from the State Department API
func (c *StateGovClient) fetchAdvisories(ctx context.Context) ([]rawAdvisory, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", stateGovBaseURL, nil)
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
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Try parsing as array first (common format)
	var advisories []rawAdvisory
	if err := json.Unmarshal(body, &advisories); err == nil {
		return advisories, nil
	}

	// Try parsing as wrapped response
	var wrapped rawAdvisoryResponse
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("failed to decode advisory response: %w", err)
	}

	return wrapped.Advisories, nil
}
