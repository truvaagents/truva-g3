package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	travelpayoutsBaseURL  = "https://api.travelpayouts.com"
	travelpayoutsAutoURL  = "https://autocomplete.travelpayouts.com"
	travelpayoutsTokenHdr = "X-Access-Token"
)

// TravelpayoutsClient handles API communication with the Travelpayouts Data API
// (flights) and the public Travelpayouts autocomplete service (airports/cities).
type TravelpayoutsClient struct {
	token      string
	httpClient *http.Client
}

// NewTravelpayoutsClient creates a Travelpayouts client with distributed tracing.
func NewTravelpayoutsClient(token string) *TravelpayoutsClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &TravelpayoutsClient{
		token:      token,
		httpClient: tracedClient,
	}
}

// --- Travelpayouts raw response types ---

// tpCheapResponse — /v1/prices/cheap returns data keyed by destination IATA,
// then by transfer-count ("0", "1", "2").
type tpCheapResponse struct {
	Success  bool                                `json:"success"`
	Data     map[string]map[string]tpCheapOffer  `json:"data"`
	Currency string                              `json:"currency"`
	Error    string                              `json:"error,omitempty"`
}

type tpCheapOffer struct {
	Airline      string `json:"airline"`
	FlightNumber int    `json:"flight_number"`
	Price        int    `json:"price"`
	DepartureAt  string `json:"departure_at"`
	ReturnAt     string `json:"return_at"`
	ExpiresAt    string `json:"expires_at"`
	Duration     int    `json:"duration"`
}

// tpCalendarResponse — /v1/prices/calendar returns data keyed by date string.
type tpCalendarResponse struct {
	Success  bool                       `json:"success"`
	Data     map[string]tpCalendarEntry `json:"data"`
	Currency string                     `json:"currency"`
	Error    string                     `json:"error,omitempty"`
}

type tpCalendarEntry struct {
	Origin       string `json:"origin"`
	Destination  string `json:"destination"`
	Airline      string `json:"airline"`
	FlightNumber int    `json:"flight_number"`
	Price        int    `json:"price"`
	DepartureAt  string `json:"departure_at"`
	ReturnAt     string `json:"return_at"`
	Transfers    int    `json:"transfers"`
}

// tpCityDirectionsResponse — /v1/city-directions returns data keyed by destination IATA.
// The per-destination value object is discarded; only the IATA keys are used,
// so it's decoded as json.RawMessage to skip per-field parsing.
type tpCityDirectionsResponse struct {
	Success  bool                       `json:"success"`
	Data     map[string]json.RawMessage `json:"data"`
	Currency string                     `json:"currency"`
	Error    string                     `json:"error,omitempty"`
}

// tpAutocompletePlace — response element from autocomplete.travelpayouts.com
type tpAutocompletePlace struct {
	Type        string `json:"type"` // "airport" or "city"
	Code        string `json:"code"`
	Name        string `json:"name"`
	CityCode    string `json:"city_code"`
	CityName    string `json:"city_name"`
	CountryCode string `json:"country_code"`
	CountryName string `json:"country_name"`
	Coordinates struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"coordinates"`
}

// --- Client methods ---

// SearchFlights returns cached flight offers between two airports/cities.
// Uses /v1/prices/cheap — returns non-stop + 1-stop + 2-stop cheapest offers
// from the Aviasales cache. Cached prices can be up to 48h old.
func (c *TravelpayoutsClient) SearchFlights(ctx context.Context, req SearchFlightsRequest) (*SearchFlightsResponse, error) {
	params := url.Values{}
	params.Set("origin", req.Origin)
	params.Set("destination", req.Destination)
	if req.DepartureDate != "" {
		params.Set("depart_date", req.DepartureDate)
	}
	if req.ReturnDate != "" {
		params.Set("return_date", req.ReturnDate)
	}
	currency := "usd"
	params.Set("currency", currency)

	endpoint := fmt.Sprintf("%s/v1/prices/cheap?%s", travelpayoutsBaseURL, params.Encode())

	body, err := c.doTokenRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw tpCheapResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode flight offers: %w", err)
	}
	if !raw.Success && raw.Error != "" {
		return nil, fmt.Errorf("Travelpayouts API error: %s", raw.Error)
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}

	respCurrency := strings.ToUpper(currency)
	if raw.Currency != "" {
		respCurrency = strings.ToUpper(raw.Currency)
	}

	offers := make([]FlightOffer, 0, maxResults)
	// /v1/prices/cheap nests results: data[destination_iata][transfers_count] = offer.
	// Iterate through each destination bucket, and within it each transfer-count
	// bucket, collecting up to maxResults offers.
	for destCode, byTransfers := range raw.Data {
		for transfersStr, p := range byTransfers {
			transfers, _ := strconv.Atoi(transfersStr)
			fo := FlightOffer{
				Price:    strconv.Itoa(p.Price),
				Currency: respCurrency,
				Airlines: []string{p.Airline},
				Stops:    transfers,
				Segments: []FlightSegment{{
					DepartureAirport: req.Origin,
					DepartureTime:    p.DepartureAt,
					ArrivalAirport:   destCode,
					ArrivalTime:      p.DepartureAt,
					Airline:          p.Airline,
					FlightNumber:     p.Airline + strconv.Itoa(p.FlightNumber),
				}},
			}
			offers = append(offers, fo)
			if len(offers) >= maxResults {
				break
			}
		}
		if len(offers) >= maxResults {
			break
		}
	}

	return &SearchFlightsResponse{
		Origin:        req.Origin,
		Destination:   req.Destination,
		DepartureDate: req.DepartureDate,
		ReturnDate:    req.ReturnDate,
		Offers:        offers,
		Currency:      respCurrency,
		Source:        "Travelpayouts Aviasales (cached)",
	}, nil
}

// SearchAirports resolves a keyword (city/airport name) to IATA codes via the
// public Travelpayouts autocomplete service. No auth required.
func (c *TravelpayoutsClient) SearchAirports(ctx context.Context, keyword, subType string) (*SearchAirportsResponse, error) {
	params := url.Values{}
	params.Set("term", keyword)
	params.Set("locale", "en")

	switch strings.ToUpper(subType) {
	case "AIRPORT":
		params.Add("types[]", "airport")
	case "CITY":
		params.Add("types[]", "city")
	default:
		params.Add("types[]", "airport")
		params.Add("types[]", "city")
	}

	endpoint := fmt.Sprintf("%s/places2?%s", travelpayoutsAutoURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create autocomplete request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("autocomplete request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read autocomplete response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("autocomplete returned status %d: %s", resp.StatusCode, string(body))
	}

	var places []tpAutocompletePlace
	if err := json.Unmarshal(body, &places); err != nil {
		return nil, fmt.Errorf("failed to decode autocomplete response: %w", err)
	}

	airports := make([]AirportResult, 0, len(places))
	for _, p := range places {
		airports = append(airports, AirportResult{
			Name:      p.Name,
			IATACode:  p.Code,
			Type:      p.Type,
			City:      p.CityName,
			Country:   p.CountryName,
			Latitude:  p.Coordinates.Lat,
			Longitude: p.Coordinates.Lon,
		})
	}

	return &SearchAirportsResponse{
		Keyword:  keyword,
		Airports: airports,
		Source:   "Travelpayouts Autocomplete",
	}, nil
}

// CheapestDates finds the cheapest travel dates between two airports/cities.
// Uses /v1/prices/calendar, which returns daily prices for a given month.
func (c *TravelpayoutsClient) CheapestDates(ctx context.Context, origin, destination, departureDate string) (*CheapestDatesResponse, error) {
	params := url.Values{}
	params.Set("origin", origin)
	params.Set("destination", destination)
	params.Set("calendar_type", "departure_date")
	params.Set("currency", "usd")
	// /v1/prices/calendar accepts YYYY-MM (month) or YYYY-MM-DD for depart_date.
	// If empty, the API returns a sensible default window.
	if departureDate != "" {
		// Accept both "2026-04" and "2026-04-15" — strip day if present.
		depart := departureDate
		if len(depart) == 10 {
			depart = depart[:7]
		}
		params.Set("depart_date", depart)
	}

	endpoint := fmt.Sprintf("%s/v1/prices/calendar?%s", travelpayoutsBaseURL, params.Encode())

	body, err := c.doTokenRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw tpCalendarResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode calendar: %w", err)
	}
	if !raw.Success && raw.Error != "" {
		return nil, fmt.Errorf("Travelpayouts API error: %s", raw.Error)
	}

	currency := "USD"
	if raw.Currency != "" {
		currency = strings.ToUpper(raw.Currency)
	}

	dates := make([]CheapestDate, 0, len(raw.Data))
	for date, entry := range raw.Data {
		dates = append(dates, CheapestDate{
			DepartureDate: date,
			ReturnDate:    entry.ReturnAt,
			Price:         strconv.Itoa(entry.Price),
			Currency:      currency,
		})
	}

	return &CheapestDatesResponse{
		Origin:      origin,
		Destination: destination,
		Dates:       dates,
		Source:      "Travelpayouts Aviasales (cached)",
	}, nil
}

// AirportRoutes lists popular direct destinations from an origin city or airport.
// Uses /v1/city-directions; accepts IATA airport or city codes as origin.
func (c *TravelpayoutsClient) AirportRoutes(ctx context.Context, departureAirport string) (*AirportRoutesResponse, error) {
	params := url.Values{}
	params.Set("origin", departureAirport)
	params.Set("currency", "usd")

	endpoint := fmt.Sprintf("%s/v1/city-directions?%s", travelpayoutsBaseURL, params.Encode())

	body, err := c.doTokenRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw tpCityDirectionsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode city-directions: %w", err)
	}
	if !raw.Success && raw.Error != "" {
		return nil, fmt.Errorf("Travelpayouts API error: %s", raw.Error)
	}

	destinations := make([]RouteDestination, 0, len(raw.Data))
	for destCode := range raw.Data {
		destinations = append(destinations, RouteDestination{
			Name:     destCode,
			IATACode: destCode,
			Type:     "city",
		})
	}

	return &AirportRoutesResponse{
		DepartureAirport: departureAirport,
		Destinations:     destinations,
		Source:           "Travelpayouts Aviasales (cached)",
	}, nil
}

// --- Helpers ---

// doTokenRequest executes a GET request against the Travelpayouts Data API
// with the token passed in the X-Access-Token header.
func (c *TravelpayoutsClient) doTokenRequest(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set(travelpayoutsTokenHdr, c.token)
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

	return body, nil
}
