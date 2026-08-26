package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// FlightTool provides flight search capabilities via the Travelpayouts Data API
// (cached Aviasales prices) plus the public Travelpayouts autocomplete service.
// It demonstrates the passive tool pattern - can register but not discover.
type FlightTool struct {
	*core.BaseTool
	client *TravelpayoutsClient
}

// SearchFlightsRequest represents the input for flight search requests
type SearchFlightsRequest struct {
	Origin        string `json:"origin"`                 // IATA airport code (e.g., "JFK")
	Destination   string `json:"destination"`            // IATA airport code (e.g., "NRT")
	DepartureDate string `json:"departure_date"`         // YYYY-MM-DD format
	ReturnDate    string `json:"return_date,omitempty"`  // YYYY-MM-DD format (for round trips)
	Adults        int    `json:"adults,omitempty"`       // Number of adult travelers (default 1)
	MaxResults    int    `json:"max_results,omitempty"`  // Max number of offers to return (default 5)
	TravelClass   string `json:"travel_class,omitempty"` // ECONOMY, PREMIUM_ECONOMY, BUSINESS, FIRST
}

// SearchFlightsResponse represents the output for flight search
type SearchFlightsResponse struct {
	Origin        string        `json:"origin"`
	Destination   string        `json:"destination"`
	DepartureDate string        `json:"departure_date"`
	ReturnDate    string        `json:"return_date,omitempty"`
	Offers        []FlightOffer `json:"offers"`
	Currency      string        `json:"currency"`
	Source        string        `json:"source"`
}

// FlightOffer represents a single flight offer
type FlightOffer struct {
	Price         string          `json:"price"`
	Currency      string          `json:"currency"`
	Airlines      []string        `json:"airlines"`
	TotalDuration string          `json:"total_duration"`
	Stops         int             `json:"stops"`
	Segments      []FlightSegment `json:"segments"`
}

// FlightSegment represents a single flight segment
type FlightSegment struct {
	DepartureAirport string `json:"departure_airport"`
	DepartureTime    string `json:"departure_time"`
	ArrivalAirport   string `json:"arrival_airport"`
	ArrivalTime      string `json:"arrival_time"`
	Airline          string `json:"airline"`
	FlightNumber     string `json:"flight_number"`
	Duration         string `json:"duration"`
	CabinClass       string `json:"cabin_class,omitempty"`
}

// SearchAirportsRequest represents the input for airport search requests
type SearchAirportsRequest struct {
	Keyword string `json:"keyword"`            // City or airport name (e.g., "Tokyo")
	SubType string `json:"sub_type,omitempty"` // AIRPORT, CITY, or both (default both)
}

// SearchAirportsResponse represents the output for airport search
type SearchAirportsResponse struct {
	Keyword  string          `json:"keyword"`
	Airports []AirportResult `json:"airports"`
	Source   string          `json:"source"`
}

// AirportResult represents a single airport/city result
type AirportResult struct {
	Name      string  `json:"name"`
	IATACode  string  `json:"iata_code"`
	Type      string  `json:"type"` // "airport" or "city"
	City      string  `json:"city,omitempty"`
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// CheapestDatesRequest represents the input for cheapest dates requests
type CheapestDatesRequest struct {
	Origin        string `json:"origin"`                   // IATA airport code
	Destination   string `json:"destination"`              // IATA airport code
	DepartureDate string `json:"departure_date,omitempty"` // YYYY-MM-DD starting search date
}

// CheapestDatesResponse represents the output for cheapest dates
type CheapestDatesResponse struct {
	Origin      string         `json:"origin"`
	Destination string         `json:"destination"`
	Dates       []CheapestDate `json:"dates"`
	Source      string         `json:"source"`
}

// CheapestDate represents a single cheapest travel date
type CheapestDate struct {
	DepartureDate string `json:"departure_date"`
	ReturnDate    string `json:"return_date"`
	Price         string `json:"price"`
	Currency      string `json:"currency"`
}

// AirportRoutesRequest represents the input for airport routes requests
type AirportRoutesRequest struct {
	DepartureAirport string `json:"departure_airport"` // IATA airport code
}

// AirportRoutesResponse represents the output for airport routes
type AirportRoutesResponse struct {
	DepartureAirport string             `json:"departure_airport"`
	Destinations     []RouteDestination `json:"destinations"`
	Source           string             `json:"source"`
}

// RouteDestination represents a single route destination
type RouteDestination struct {
	Name     string `json:"name"`
	IATACode string `json:"iata_code"`
	Type     string `json:"type"` // "airport" or "city"
}

// NewFlightTool creates a new flight search tool
func NewFlightTool() *FlightTool {
	token := os.Getenv("TRAVELPAYOUTS_TOKEN")

	tool := &FlightTool{
		BaseTool: core.NewTool("flight-tool"),
		client:   NewTravelpayoutsClient(token),
	}

	// Register flight search capabilities
	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all flight-related capabilities
func (f *FlightTool) registerCapabilities() {
	// Capability 1: Search Flights
	// Auto-generated endpoint: /api/capabilities/search_flights
	// Schema endpoint: /api/capabilities/search_flights/schema
	f.RegisterCapability(core.Capability{
		Name: "search_flights",
		Description: "Searches for flights between two airports using cached Aviasales prices. " +
			"Use when the user wants actual flight offers (airline, price, departure time) for a route and date. " +
			"Returns: offers with price, currency, airlines, stops, segments (departure/arrival airports + times). " +
			"Note: prices are cached (up to 48h old), not live booking quotes. " +
			"Required: origin (3-letter IATA airport code), destination (3-letter IATA airport code), departure_date (YYYY-MM-DD). " +
			"Optional: return_date (YYYY-MM-DD), adults (default 1), max_results (default 5), travel_class (ECONOMY, PREMIUM_ECONOMY, BUSINESS, FIRST).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     f.handleSearchFlights,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "origin",
					Type:        "string",
					Example:     "JFK",
					Description: "3-letter IATA airport code for departure (e.g., JFK, LAX, SFO)",
				},
				{
					Name:        "destination",
					Type:        "string",
					Example:     "NRT",
					Description: "3-letter IATA airport code for arrival (e.g., NRT, LHR, CDG)",
				},
				{
					Name:        "departure_date",
					Type:        "string",
					Example:     "2026-04-15",
					Description: "Departure date in YYYY-MM-DD format",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "return_date",
					Type:        "string",
					Example:     "2026-04-22",
					Description: "Return date in YYYY-MM-DD format for round trips",
				},
				{
					Name:        "adults",
					Type:        "integer",
					Example:     "1",
					Description: "Number of adult travelers (default 1)",
				},
				{
					Name:        "max_results",
					Type:        "integer",
					Example:     "5",
					Description: "Maximum number of offers to return (default 5)",
				},
				{
					Name:        "travel_class",
					Type:        "string",
					Example:     "ECONOMY",
					Description: "Travel class: ECONOMY, PREMIUM_ECONOMY, BUSINESS, or FIRST",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "origin", Type: "string", Description: "Departure airport IATA code"},
				{Name: "destination", Type: "string", Description: "Arrival airport IATA code"},
				{Name: "departure_date", Type: "string", Description: "Departure date"},
				{Name: "offers", Type: "array", Description: "Array of flight offers with price, currency, airlines, total_duration, stops, and segments"},
				{Name: "currency", Type: "string", Description: "Currency code for prices"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "return_date", Type: "string", Description: "Return date for round trip flights"},
			},
		},
	})

	// Capability 2: Search Airports
	// Auto-generated endpoint: /api/capabilities/search_airports
	f.RegisterCapability(core.Capability{
		Name: "search_airports",
		Description: "Resolves a city or airport name keyword to IATA codes via autocomplete. " +
			"Use AFTER the user mentions a place by name (e.g., 'Tokyo', 'London') to get the IATA code required by search_flights, cheapest_dates, and airport_routes. " +
			"Returns: array of airports with name, iata_code, type (airport|city), city, country, latitude, longitude. " +
			"Required: keyword (city or airport name in English). " +
			"Optional: sub_type (AIRPORT, CITY, or omit for both).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     f.handleSearchAirports,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "keyword",
					Type:        "string",
					Example:     "Tokyo",
					Description: "City or airport name to search for",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "sub_type",
					Type:        "string",
					Example:     "AIRPORT",
					Description: "Filter by type: AIRPORT, CITY, or both (default both)",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "keyword", Type: "string", Description: "The search keyword used"},
				{Name: "airports", Type: "array", Description: "Array of airport results with name, iata_code, type, country, latitude, and longitude"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	// Capability 3: Cheapest Dates
	// Auto-generated endpoint: /api/capabilities/cheapest_dates
	f.RegisterCapability(core.Capability{
		Name: "cheapest_dates",
		Description: "Finds the cheapest departure dates across a month for a given route. " +
			"Use when the user has flexible dates and wants to know which days are cheapest to fly. " +
			"Returns: array of date options with departure_date, return_date, price, currency. " +
			"Required: origin (IATA airport or city code), destination (IATA airport or city code). " +
			"Optional: departure_date (YYYY-MM for the month to search; defaults to the nearest available month).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     f.handleCheapestDates,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "origin",
					Type:        "string",
					Example:     "JFK",
					Description: "3-letter IATA airport code for departure",
				},
				{
					Name:        "destination",
					Type:        "string",
					Example:     "NRT",
					Description: "3-letter IATA airport code for arrival",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "departure_date",
					Type:        "string",
					Example:     "2026-04-01",
					Description: "Starting search date in YYYY-MM-DD format",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "origin", Type: "string", Description: "Departure airport IATA code"},
				{Name: "destination", Type: "string", Description: "Arrival airport IATA code"},
				{Name: "dates", Type: "array", Description: "Array of cheapest date options with departure_date, return_date, price, and currency"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})

	// Capability 4: Airport Routes
	// Auto-generated endpoint: /api/capabilities/airport_routes
	f.RegisterCapability(core.Capability{
		Name: "airport_routes",
		Description: "Lists popular direct-flight destinations from an origin city or airport. " +
			"Use when the user wants to know where they can fly to from a given city without planning a specific date. " +
			"Returns: array of destinations with name, iata_code, type (airport|city). " +
			"Required: departure_airport (IATA airport or city code).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     f.handleAirportRoutes,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "departure_airport",
					Type:        "string",
					Example:     "JFK",
					Description: "3-letter IATA airport code for departure airport",
				},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "departure_airport", Type: "string", Description: "Departure airport IATA code"},
				{Name: "destinations", Type: "array", Description: "Array of route destinations with name, iata_code, and type"},
				{Name: "source", Type: "string", Description: "Data source attribution"},
			},
		},
	})
}
