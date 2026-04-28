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
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

const (
	slackBaseURL = "https://slack.com/api"
)

// --- Slack API Response Types ---

// SlackResponse is the universal Slack API response envelope.
// CRITICAL: Slack ALWAYS returns HTTP 200. Errors are in {ok: false, error: "..."}.
type SlackResponse struct {
	OK               bool              `json:"ok"`
	Error            string            `json:"error,omitempty"`
	ResponseMetadata *ResponseMetadata `json:"response_metadata,omitempty"`
}

// ResponseMetadata contains pagination cursors
type ResponseMetadata struct {
	NextCursor string `json:"next_cursor"`
}

// SlackPostMessageResponse is the response from chat.postMessage
type SlackPostMessageResponse struct {
	SlackResponse
	Channel string       `json:"channel"`
	TS      string       `json:"ts"`      // Timestamp (STRING) = unique message ID
	Message *SlackPosted `json:"message,omitempty"`
}

// SlackPosted represents a posted message in the response
type SlackPosted struct {
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

// SlackChannel represents a channel from conversations.list
type SlackChannel struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	IsArchived bool         `json:"is_archived"`
	IsPrivate  bool         `json:"is_private"`
	Topic      SlackTopic   `json:"topic"`
	Purpose    SlackPurpose `json:"purpose"`
	NumMembers int          `json:"num_members"`
	Created    int64        `json:"created"` // Unix epoch INTEGER
	Updated    int64        `json:"updated"` // Unix epoch INTEGER
}

// SlackTopic represents a channel topic
type SlackTopic struct {
	Value string `json:"value"`
}

// SlackPurpose represents a channel purpose
type SlackPurpose struct {
	Value string `json:"value"`
}

// SlackConversationsListResponse is the response from conversations.list
type SlackConversationsListResponse struct {
	SlackResponse
	Channels []SlackChannel `json:"channels"`
}

// SlackSearchResponse is the response from search.messages
type SlackSearchResponse struct {
	SlackResponse
	Messages *SlackSearchMessages `json:"messages,omitempty"`
}

// SlackSearchMessages contains the actual search results
type SlackSearchMessages struct {
	Total   int                `json:"total"`
	Matches []SlackSearchMatch `json:"matches"`
}

// SlackSearchMatch represents a single search result
type SlackSearchMatch struct {
	Channel   SlackSearchChannel `json:"channel"`
	Text      string             `json:"text"`
	Username  string             `json:"username"`
	TS        string             `json:"ts"`        // Timestamp STRING = message ID
	Permalink string             `json:"permalink"`
}

// SlackSearchChannel represents a channel in search results
type SlackSearchChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- Client ---

// SlackClient handles API communication with Slack Web API
type SlackClient struct {
	botToken   string // xoxb- token for chat/channels APIs
	userToken  string // xoxp- token for search API (requires user token)
	httpClient *http.Client
}

// NewSlackClient creates a new Slack API client with traced HTTP client
// for distributed tracing visibility into external API calls.
// Even though Slack won't propagate traceparent, TracedHTTPClient provides
// client-side span visibility in Jaeger showing exact API call durations.
//
// botToken is used for chat.postMessage and conversations.list.
// userToken is used for search.messages (Slack requires a user token for search).
func NewSlackClient(botToken, userToken string) *SlackClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 10 * time.Second

	return &SlackClient{
		botToken:   botToken,
		userToken:  userToken,
		httpClient: tracedClient,
	}
}

// --- CRITICAL: Custom Slack Error Handling ---
// Slack ALWAYS returns HTTP 200. We must check the `ok` field in the JSON body.
// This is fundamentally different from the Finnhub pattern which checks resp.StatusCode.

// SlackAPIError represents a Slack API error with the error string
type SlackAPIError struct {
	SlackError string // The Slack error string (e.g., "channel_not_found")
	HTTPStatus int    // Our mapped HTTP status code for orchestrator
}

func (e *SlackAPIError) Error() string {
	return fmt.Sprintf("Slack API error: %s", e.SlackError)
}

// slackErrorToHTTPStatus maps Slack error strings to HTTP status codes.
// This replaces the standard apiErrorStatus() because Slack errors are
// strings in the JSON body, NOT HTTP status codes.
func slackErrorToHTTPStatus(slackErr string) int {
	switch slackErr {
	// Auth errors -> 401 (fail immediately, can't be fixed by orchestrator)
	case "not_authed", "invalid_auth", "token_revoked", "account_inactive":
		return http.StatusUnauthorized // 401

	// Permission errors -> 403 (fail immediately)
	case "not_in_channel", "missing_scope", "channel_is_archived",
		"not_allowed_token_type", "ekm_access_denied":
		return http.StatusForbidden // 403

	// Not found errors -> 404 (LLM Error Analyzer can suggest correction)
	case "channel_not_found", "user_not_found":
		return http.StatusNotFound // 404

	// Rate limiting -> 429 (resilience module retries with backoff)
	case "rate_limited", "ratelimited":
		return http.StatusTooManyRequests // 429

	// Client errors -> 400 (LLM Error Analyzer can suggest correction)
	case "invalid_arguments", "no_text", "too_many_attachments",
		"msg_too_long", "invalid_blocks", "invalid_blocks_format":
		return http.StatusBadRequest // 400

	// All other errors -> 502 (resilience module retries)
	default:
		return http.StatusBadGateway // 502
	}
}

// --- API Methods ---

// PostMessage sends a text message to a channel (chat.postMessage)
func (c *SlackClient) PostMessage(ctx context.Context, channel, text, threadTS string) (*SlackPostMessageResponse, error) {
	if c.botToken == "" {
		return nil, fmt.Errorf("Slack bot token not configured")
	}

	payload := map[string]interface{}{
		"channel": channel,
		"text":    text,
	}
	if threadTS != "" && threadTS != "null" {
		payload["thread_ts"] = threadTS
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/chat.postMessage", slackBaseURL),
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var slackResp SlackPostMessageResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// CRITICAL: Check ok field, NOT HTTP status code
	if !slackResp.OK {
		return nil, &SlackAPIError{
			SlackError: slackResp.Error,
			HTTPStatus: slackErrorToHTTPStatus(slackResp.Error),
		}
	}

	return &slackResp, nil
}

// PostBlockMessage sends a Block Kit formatted message (chat.postMessage with blocks)
func (c *SlackClient) PostBlockMessage(ctx context.Context, channel, text string, blocks []map[string]interface{}) (*SlackPostMessageResponse, error) {
	if c.botToken == "" {
		return nil, fmt.Errorf("Slack bot token not configured")
	}

	payload := map[string]interface{}{
		"channel": channel,
		"text":    text,   // Fallback text, required even with blocks
		"blocks":  blocks,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/chat.postMessage", slackBaseURL),
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var slackResp SlackPostMessageResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// CRITICAL: Check ok field, NOT HTTP status code
	if !slackResp.OK {
		return nil, &SlackAPIError{
			SlackError: slackResp.Error,
			HTTPStatus: slackErrorToHTTPStatus(slackResp.Error),
		}
	}

	return &slackResp, nil
}

// ListConversations lists public channels (conversations.list)
func (c *SlackClient) ListConversations(ctx context.Context, limit int, excludeArchived bool) (*SlackConversationsListResponse, error) {
	if c.botToken == "" {
		return nil, fmt.Errorf("Slack bot token not configured")
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	params := url.Values{}
	params.Add("types", "public_channel")
	params.Add("limit", strconv.Itoa(limit))
	if excludeArchived {
		params.Add("exclude_archived", "true")
	}

	fullURL := fmt.Sprintf("%s/conversations.list?%s", slackBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var slackResp SlackConversationsListResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// CRITICAL: Check ok field, NOT HTTP status code
	if !slackResp.OK {
		return nil, &SlackAPIError{
			SlackError: slackResp.Error,
			HTTPStatus: slackErrorToHTTPStatus(slackResp.Error),
		}
	}

	return &slackResp, nil
}

// SearchMessages searches message history (search.messages).
// CRITICAL: Slack's search API requires a user token (xoxp-), not a bot token (xoxb-).
func (c *SlackClient) SearchMessages(ctx context.Context, query string, count int, sort string) (*SlackSearchResponse, error) {
	if c.userToken == "" {
		return nil, fmt.Errorf("SLACK_USER_TOKEN not configured (search.messages requires a user token, not a bot token)")
	}

	if count <= 0 || count > 100 {
		count = 20
	}
	if sort == "" {
		sort = "score"
	}

	params := url.Values{}
	params.Add("query", query)
	params.Add("count", strconv.Itoa(count))
	params.Add("sort", sort)

	fullURL := fmt.Sprintf("%s/search.messages?%s", slackBaseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.userToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var slackResp SlackSearchResponse
	if err := json.Unmarshal(respBody, &slackResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// CRITICAL: Check ok field, NOT HTTP status code
	if !slackResp.OK {
		return nil, &SlackAPIError{
			SlackError: slackResp.Error,
			HTTPStatus: slackErrorToHTTPStatus(slackResp.Error),
		}
	}

	return &slackResp, nil
}

// --- Block Kit Helper Functions ---

// headerBlock creates a Block Kit header block
func headerBlock(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "header",
		"text": map[string]interface{}{
			"type": "plain_text",
			"text": text,
		},
	}
}

// sectionBlock creates a Block Kit section block with markdown text
func sectionBlock(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "section",
		"text": map[string]interface{}{
			"type": "mrkdwn",
			"text": text,
		},
	}
}

// dividerBlock creates a Block Kit divider block
func dividerBlock() map[string]interface{} {
	return map[string]interface{}{
		"type": "divider",
	}
}
