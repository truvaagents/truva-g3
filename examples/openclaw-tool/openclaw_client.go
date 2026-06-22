package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/telemetry"
)

// OpenClawClient is a thin, traced HTTP client for the OpenClaw gateway's
// OpenAI-compatible OpenResponses API (POST /v1/responses). It is the tool's only
// integration boundary. Modeled on examples/stock-market-tool/finnhub_client.go: the
// telemetry-traced transport gives client-side spans in Jaeger even though OpenClaw won't
// propagate the trace further.
type OpenClawClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewOpenClawClient builds a traced client. Note: no client-level Timeout is set — the
// per-transaction context deadline (owned by the handler, ANALYSIS.md §4) governs how long
// a task may run, which can legitimately be minutes for a large map-reduce summary.
func NewOpenClawClient(baseURL, token string) *OpenClawClient {
	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	})

	return &OpenClawClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: tracedClient,
	}
}

// responsesRequest is the body of POST /v1/responses (non-streaming). OpenClaw has no
// "session" field; a unique `user` per call gives a fresh conversation with no carryover
// (ANALYSIS.md §8/§12). We deliberately omit previous_response_id for the same reason.
type responsesRequest struct {
	Model      string `json:"model"`
	Input      string `json:"input"`
	ToolChoice string `json:"tool_choice,omitempty"`
	User       string `json:"user"`
	Stream     bool   `json:"stream"`
}

// responsesReply models an OpenAI-compatible Responses payload. We read either the
// convenience output_text field or concatenate the structured output[].content[].text.
type responsesReply struct {
	OutputText string `json:"output_text"`
	Output     []struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

// Text returns the response text, preferring output_text and falling back to the
// structured output content.
func (r responsesReply) Text() string {
	if s := strings.TrimSpace(r.OutputText); s != "" {
		return s
	}
	var b strings.Builder
	for _, o := range r.Output {
		for _, c := range o.Content {
			if c.Text != "" {
				b.WriteString(c.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// RunResponses sends one non-streaming task to OpenClaw and returns its output text.
// Errors embed the upstream HTTP status as "status NNN" so core.ClassifyUpstreamError can
// route them correctly (see TOOL_DEVELOPMENT_GUIDE §10 error-message contract).
func (c *OpenClawClient) RunResponses(ctx context.Context, agentID, input, user, toolChoice string) (string, error) {
	payload, err := json.Marshal(responsesRequest{
		Model:      "openclaw",
		Input:      input,
		ToolChoice: toolChoice, // "none" forces pure-LLM (summarize/answer); "" lets the agent use its tools (run_task)
		User:       user,
		Stream:     false,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/responses", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("x-openclaw-agent-id", agentID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("OpenClaw returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var reply responsesReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	text := reply.Text()
	if text == "" {
		// Treat an empty body as an upstream 502 so it routes through error classification.
		return "", fmt.Errorf("OpenClaw error 502: empty response")
	}
	return text, nil
}
