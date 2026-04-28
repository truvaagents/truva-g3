package main

import (
	"bytes"
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
	liteapiBaseURL   = "https://api.liteapi.travel/v3.0"
	liteapiAPIKeyHdr = "X-API-Key"
)

// LiteAPIClient handles API communication with LiteAPI (hotel data + rates).
type LiteAPIClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewLiteAPIClient creates a LiteAPI client with distributed tracing.
func NewLiteAPIClient(apiKey string) *LiteAPIClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 30 * time.Second

	return &LiteAPIClient{
		apiKey:     apiKey,
		httpClient: tracedClient,
	}
}

// --- LiteAPI raw response types ---

// liteHotelsDataResponse — /data/hotels
type liteHotelsDataResponse struct {
	Data []liteHotelData `json:"data"`
}

type liteHotelData struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Chain     string  `json:"chain"`
	Currency  string  `json:"currency"`
	Country   string  `json:"country"`
	City      string  `json:"city"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Address   string  `json:"address"`
	Zip       string  `json:"zip"`
	MainPhoto string  `json:"main_photo"`
	Stars     int     `json:"stars"`
	Rating    float64 `json:"rating"`
}

// liteRatesResponse — /hotels/rates
type liteRatesResponse struct {
	Data []liteHotelRates `json:"data"`
}

type liteHotelRates struct {
	HotelID   string         `json:"hotelId"`
	RoomTypes []liteRoomType `json:"roomTypes"`
}

type liteRoomType struct {
	RoomTypeID string     `json:"roomTypeId"`
	OfferID    string     `json:"offerId"`
	Rates      []liteRate `json:"rates"`
}

type liteRate struct {
	RateID       string         `json:"rateId"`
	Name         string         `json:"name"`
	MaxOccupancy int            `json:"maxOccupancy"`
	BoardType    string         `json:"boardType"`
	BoardName    string         `json:"boardName"`
	RetailRate   liteRetailRate `json:"retailRate"`
}

type liteRetailRate struct {
	Total []liteAmount `json:"total"`
}

type liteAmount struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// liteReviewsResponse — /data/reviews
type liteReviewsResponse struct {
	Data  []liteReview `json:"data"`
	Total int          `json:"total"`
}

type liteReview struct {
	AverageScore float64 `json:"averageScore"`
	Country      string  `json:"country"`
	Type         string  `json:"type"`
	Name         string  `json:"name"`
	Date         string  `json:"date"`
	Headline     string  `json:"headline"`
	Language     string  `json:"language"`
	Pros         string  `json:"pros"`
	Cons         string  `json:"cons"`
	Source       string  `json:"source"`
}

// liteErrorResponse — LiteAPI error envelope.
type liteErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- Client methods ---

// SearchHotels lists hotels in a city and fetches live rates for them.
// This is a 2-step call: /data/hotels → /hotels/rates.
func (c *LiteAPIClient) SearchHotels(ctx context.Context, req SearchHotelsRequest) (*SearchHotelsResponse, error) {
	limit := req.MaxResults
	if limit <= 0 {
		limit = 10
	}

	hotels, err := c.listHotelsByCity(ctx, req.CountryCode, req.CityName, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list hotels: %w", err)
	}
	if len(hotels) == 0 {
		return &SearchHotelsResponse{
			CityName:    req.CityName,
			CountryCode: req.CountryCode,
			CheckIn:     req.CheckIn,
			CheckOut:    req.CheckOut,
			Hotels:      []HotelOffer{},
			Source:      "LiteAPI",
		}, nil
	}

	hotelIDs := make([]string, 0, len(hotels))
	hotelIndex := make(map[string]liteHotelData, len(hotels))
	for _, h := range hotels {
		hotelIDs = append(hotelIDs, h.ID)
		hotelIndex[h.ID] = h
	}

	adults := req.Adults
	if adults <= 0 {
		adults = 2
	}
	currency := strings.ToUpper(req.Currency)
	if currency == "" {
		currency = "USD"
	}
	nationality := strings.ToUpper(req.GuestNationality)
	if nationality == "" {
		nationality = "US"
	}

	ratesReq := map[string]interface{}{
		"hotelIds": hotelIDs,
		"occupancies": []map[string]interface{}{
			{"adults": adults, "children": []int{}},
		},
		"currency":         currency,
		"guestNationality": nationality,
		"checkin":          req.CheckIn,
		"checkout":         req.CheckOut,
	}

	body, err := c.doJSONRequest(ctx, "POST", liteapiBaseURL+"/hotels/rates", ratesReq)
	if err != nil {
		return nil, err
	}

	var raw liteRatesResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode hotel rates: %w", err)
	}

	results := make([]HotelOffer, 0, len(raw.Data))
	for _, hr := range raw.Data {
		meta := hotelIndex[hr.HotelID]
		hotel := HotelOffer{
			HotelID:   hr.HotelID,
			Name:      meta.Name,
			Rating:    strconv.Itoa(meta.Stars),
			Latitude:  meta.Latitude,
			Longitude: meta.Longitude,
		}

		for _, rt := range hr.RoomTypes {
			for _, r := range rt.Rates {
				price := ""
				rateCurrency := currency
				if len(r.RetailRate.Total) > 0 {
					price = strconv.FormatFloat(r.RetailRate.Total[0].Amount, 'f', 2, 64)
					if r.RetailRate.Total[0].Currency != "" {
						rateCurrency = r.RetailRate.Total[0].Currency
					}
				}
				hotel.Rooms = append(hotel.Rooms, RoomOffer{
					Type:        r.Name,
					Description: r.Name,
					Price:       price,
					Currency:    rateCurrency,
					BoardType:   r.BoardName,
				})
			}
		}

		results = append(results, hotel)
	}

	return &SearchHotelsResponse{
		CityName:    req.CityName,
		CountryCode: req.CountryCode,
		CheckIn:     req.CheckIn,
		CheckOut:    req.CheckOut,
		Hotels:      results,
		Source:      "LiteAPI",
	}, nil
}

// ListHotelsByCity returns basic hotel metadata for a city (no pricing).
func (c *LiteAPIClient) ListHotelsByCity(ctx context.Context, req ListHotelsByCityRequest) (*ListHotelsByCityResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	hotels, err := c.listHotelsByCity(ctx, req.CountryCode, req.CityName, limit)
	if err != nil {
		return nil, err
	}

	results := make([]HotelInfo, 0, len(hotels))
	for _, h := range hotels {
		results = append(results, HotelInfo{
			HotelID:     h.ID,
			Name:        h.Name,
			ChainCode:   h.Chain,
			Latitude:    h.Latitude,
			Longitude:   h.Longitude,
			CountryCode: strings.ToUpper(h.Country),
		})
	}

	return &ListHotelsByCityResponse{
		CityName:    req.CityName,
		CountryCode: req.CountryCode,
		Hotels:      results,
		Source:      "LiteAPI",
	}, nil
}

// HotelRatings fetches recent reviews for a single hotel and computes aggregates.
func (c *LiteAPIClient) HotelRatings(ctx context.Context, hotelID string) (*HotelRatingsResponse, error) {
	params := url.Values{}
	params.Set("hotelId", hotelID)
	params.Set("limit", "30")

	endpoint := fmt.Sprintf("%s/data/reviews?%s", liteapiBaseURL, params.Encode())

	body, err := c.doGetRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw liteReviewsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode reviews: %w", err)
	}

	var sum float64
	for _, r := range raw.Data {
		sum += r.AverageScore
	}
	var overall float64
	if len(raw.Data) > 0 {
		overall = sum / float64(len(raw.Data))
	}

	sentiments := map[string]float64{}
	if len(raw.Data) > 0 {
		sentiments["average_score"] = overall
	}

	return &HotelRatingsResponse{
		Hotels: []HotelSentiment{{
			HotelID:         hotelID,
			OverallRating:   overall,
			NumberOfReviews: len(raw.Data),
			NumberOfRatings: raw.Total,
			Sentiments:      sentiments,
		}},
		Source: "LiteAPI",
	}, nil
}

// --- Helpers ---

// listHotelsByCity is the shared /data/hotels call used by search_hotels and list_hotels_by_city.
func (c *LiteAPIClient) listHotelsByCity(ctx context.Context, countryCode, cityName string, limit int) ([]liteHotelData, error) {
	params := url.Values{}
	params.Set("countryCode", strings.ToUpper(countryCode))
	params.Set("cityName", cityName)
	params.Set("limit", strconv.Itoa(limit))

	endpoint := fmt.Sprintf("%s/data/hotels?%s", liteapiBaseURL, params.Encode())

	body, err := c.doGetRequest(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw liteHotelsDataResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("failed to decode hotels: %w", err)
	}
	return raw.Data, nil
}

func (c *LiteAPIClient) doGetRequest(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	return c.executeRequest(req)
}

func (c *LiteAPIClient) doJSONRequest(ctx context.Context, method, endpoint string, payload interface{}) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.executeRequest(req)
}

func (c *LiteAPIClient) executeRequest(req *http.Request) ([]byte, error) {
	req.Header.Set(liteapiAPIKeyHdr, c.apiKey)
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
		var errResp liteErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("LiteAPI error %d: %s", errResp.Error.Code, errResp.Error.Message)
		}
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}
