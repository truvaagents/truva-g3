package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendError sends a structured error response using core.ToolResponse
func (s *SlackTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status == http.StatusBadGateway || status == http.StatusTooManyRequests,
		},
	})
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (s *SlackTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(info.HTTPStatus)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

// handleSendMessage processes send message requests with full telemetry
func (s *SlackTool) handleSendMessage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "slack-tool"),
		attribute.String("truvag3.capability", "send_message"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "send_message"),
	)

	s.Logger.InfoWithContext(ctx, "Processing send message request", map[string]interface{}{
		"operation":  "send_message",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req SendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.errors.total",
			"capability", "send_message",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "send_message",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Validate required fields
	req.Channel = strings.TrimSpace(req.Channel)
	req.Text = strings.TrimSpace(req.Text)
	if req.Channel == "" || req.Text == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: channel and text are required"))
		telemetry.Counter("slack.errors.total",
			"capability", "send_message",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "send_message",
			"request_id": upstreamRequestID,
			"error":      "channel and text are required",
			"error_type": "validation_error",
		})
		s.sendError(rw, "channel and text are required", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Add business context to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("slack.channel", req.Channel),
		attribute.Bool("slack.threaded", req.ThreadTS != ""),
	)

	s.Logger.InfoWithContext(ctx, "Received send message request", map[string]interface{}{
		"operation":  "send_message",
		"channel":    req.Channel,
		"threaded":   req.ThreadTS != "",
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_slack_api",
		attribute.String("channel", req.Channel),
		attribute.String("api", "chat.postMessage"),
	)

	// 7. Call Slack API with timing
	apiStartTime := time.Now()
	slackResp, err := s.client.PostMessage(ctx, req.Channel, req.Text, req.ThreadTS)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("slack.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "send_message",
		"api", "chat.postMessage",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.api.errors.total",
			"capability", "send_message",
			"error_type", "api_error",
			"module", "tool",
		)

		// Extract Slack error string for logging if available
		errDetail := err.Error()
		if slackErr, ok := err.(*SlackAPIError); ok {
			errDetail = slackErr.SlackError
		}

		s.Logger.ErrorWithContext(ctx, "Slack API call failed", map[string]interface{}{
			"operation":   "send_message",
			"error":       errDetail,
			"channel":     req.Channel,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Slack API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Build response
	s.Logger.InfoWithContext(ctx, "Slack API call successful", map[string]interface{}{
		"operation":   "send_message",
		"channel":     slackResp.Channel,
		"ts":          slackResp.TS,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	response := SendMessageResponse{
		Channel:   slackResp.Channel,
		Timestamp: slackResp.TS,
		Text:      req.Text,
		Source:    "Slack Web API",
	}
	if slackResp.Message != nil && slackResp.Message.ThreadTS != "" {
		response.ThreadTS = slackResp.Message.ThreadTS
	}

	// Add span attributes for the result
	telemetry.SetSpanAttributes(ctx,
		attribute.String("slack.message_ts", response.Timestamp),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("slack.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "send_message",
	)
	telemetry.Counter("slack.requests.total",
		"capability", "send_message",
		"status", "success",
		"module", "tool",
	)

	// 9. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("slack-tool", "send_message", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "message_sent",
		attribute.String("channel", response.Channel),
		attribute.String("timestamp", response.Timestamp),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion with context
	s.Logger.InfoWithContext(ctx, "Send message request completed", map[string]interface{}{
		"operation":   "send_message",
		"channel":     response.Channel,
		"timestamp":   response.Timestamp,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSendRichMessage processes send rich message (Block Kit) requests with full telemetry
func (s *SlackTool) handleSendRichMessage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "slack-tool"),
		attribute.String("truvag3.capability", "send_rich_message"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "send_rich_message"),
	)

	s.Logger.InfoWithContext(ctx, "Processing send rich message request", map[string]interface{}{
		"operation":  "send_rich_message",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req SendRichMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.errors.total",
			"capability", "send_rich_message",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "send_rich_message",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Validate required fields
	req.Channel = strings.TrimSpace(req.Channel)
	req.Text = strings.TrimSpace(req.Text)
	if req.Channel == "" || req.Text == "" || len(req.Blocks) == 0 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: channel, text, and blocks are required"))
		telemetry.Counter("slack.errors.total",
			"capability", "send_rich_message",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "send_rich_message",
			"request_id": upstreamRequestID,
			"error":      "channel, text, and blocks are required",
			"error_type": "validation_error",
		})
		s.sendError(rw, "channel, text, and blocks are required", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Add business context to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("slack.channel", req.Channel),
		attribute.Int("slack.blocks_count", len(req.Blocks)),
	)

	s.Logger.InfoWithContext(ctx, "Received send rich message request", map[string]interface{}{
		"operation":    "send_rich_message",
		"channel":      req.Channel,
		"blocks_count": len(req.Blocks),
		"request_id":   upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_slack_api",
		attribute.String("channel", req.Channel),
		attribute.String("api", "chat.postMessage"),
		attribute.Int("blocks_count", len(req.Blocks)),
	)

	// 7. Call Slack API with timing
	apiStartTime := time.Now()
	slackResp, err := s.client.PostBlockMessage(ctx, req.Channel, req.Text, req.Blocks)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("slack.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "send_rich_message",
		"api", "chat.postMessage",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.api.errors.total",
			"capability", "send_rich_message",
			"error_type", "api_error",
			"module", "tool",
		)

		// Extract Slack error string for logging if available
		errDetail := err.Error()
		if slackErr, ok := err.(*SlackAPIError); ok {
			errDetail = slackErr.SlackError
		}

		s.Logger.ErrorWithContext(ctx, "Slack API call failed", map[string]interface{}{
			"operation":   "send_rich_message",
			"error":       errDetail,
			"channel":     req.Channel,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Slack API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Build response
	s.Logger.InfoWithContext(ctx, "Slack API call successful", map[string]interface{}{
		"operation":   "send_rich_message",
		"channel":     slackResp.Channel,
		"ts":          slackResp.TS,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	response := SendRichMessageResponse{
		Channel:   slackResp.Channel,
		Timestamp: slackResp.TS,
		Text:      req.Text,
		Source:    "Slack Web API",
	}

	// Add span attributes for the result
	telemetry.SetSpanAttributes(ctx,
		attribute.String("slack.message_ts", response.Timestamp),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("slack.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "send_rich_message",
	)
	telemetry.Counter("slack.requests.total",
		"capability", "send_rich_message",
		"status", "success",
		"module", "tool",
	)

	// 9. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("slack-tool", "send_rich_message", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "rich_message_sent",
		attribute.String("channel", response.Channel),
		attribute.String("timestamp", response.Timestamp),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion with context
	s.Logger.InfoWithContext(ctx, "Send rich message request completed", map[string]interface{}{
		"operation":   "send_rich_message",
		"channel":     response.Channel,
		"timestamp":   response.Timestamp,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleListChannels processes list channels requests with full telemetry
func (s *SlackTool) handleListChannels(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "slack-tool"),
		attribute.String("truvag3.capability", "list_channels"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_channels"),
	)

	s.Logger.InfoWithContext(ctx, "Processing list channels request", map[string]interface{}{
		"operation":  "list_channels",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req ListChannelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.errors.total",
			"capability", "list_channels",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "list_channels",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	s.Logger.InfoWithContext(ctx, "Received list channels request", map[string]interface{}{
		"operation":        "list_channels",
		"limit":            req.Limit,
		"exclude_archived": req.ExcludeArchived,
		"request_id":       upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_slack_api",
		attribute.String("api", "conversations.list"),
		attribute.Int("limit", req.Limit),
	)

	// 7. Call Slack API with timing
	apiStartTime := time.Now()
	slackResp, err := s.client.ListConversations(ctx, req.Limit, req.ExcludeArchived)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("slack.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "list_channels",
		"api", "conversations.list",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.api.errors.total",
			"capability", "list_channels",
			"error_type", "api_error",
			"module", "tool",
		)

		// Extract Slack error string for logging if available
		errDetail := err.Error()
		if slackErr, ok := err.(*SlackAPIError); ok {
			errDetail = slackErr.SlackError
		}

		s.Logger.ErrorWithContext(ctx, "Slack API call failed", map[string]interface{}{
			"operation":   "list_channels",
			"error":       errDetail,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Slack API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Convert SlackChannel to ChannelInfo
	s.Logger.InfoWithContext(ctx, "Slack API call successful", map[string]interface{}{
		"operation":     "list_channels",
		"channel_count": len(slackResp.Channels),
		"duration_ms":   apiDuration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	channels := make([]ChannelInfo, 0, len(slackResp.Channels))
	for _, ch := range slackResp.Channels {
		channels = append(channels, ChannelInfo{
			ID:         ch.ID,
			Name:       ch.Name,
			IsArchived: ch.IsArchived,
			IsPrivate:  ch.IsPrivate,
			Topic:      ch.Topic.Value,
			Purpose:    ch.Purpose.Value,
			NumMembers: ch.NumMembers,
			Created:    ch.Created,
			Updated:    ch.Updated,
		})
	}

	// Determine if there are more pages
	hasMore := false
	nextCursor := ""
	if slackResp.ResponseMetadata != nil && slackResp.ResponseMetadata.NextCursor != "" {
		hasMore = true
		nextCursor = slackResp.ResponseMetadata.NextCursor
	}

	response := ListChannelsResponse{
		Channels:   channels,
		TotalCount: len(channels),
		HasMore:    hasMore,
		NextCursor: nextCursor,
		Source:     "Slack Web API",
	}

	// Add span attributes for the result
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("slack.channel_count", len(response.Channels)),
		attribute.Bool("slack.has_more", response.HasMore),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("slack.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "list_channels",
	)
	telemetry.Counter("slack.requests.total",
		"capability", "list_channels",
		"status", "success",
		"module", "tool",
	)

	// 9. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("slack-tool", "list_channels", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "channels_listed",
		attribute.Int("channel_count", len(response.Channels)),
		attribute.Bool("has_more", response.HasMore),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion with context
	s.Logger.InfoWithContext(ctx, "List channels request completed", map[string]interface{}{
		"operation":     "list_channels",
		"channel_count": len(response.Channels),
		"has_more":      response.HasMore,
		"source":        response.Source,
		"status":        "success",
		"duration_ms":   duration.Milliseconds(),
		"request_id":    upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleSearchMessages processes search messages requests with full telemetry
func (s *SlackTool) handleSearchMessages(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers (helps clients locate traces)
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	upstreamRequestID := baggage["request_id"]
	if upstreamRequestID == "" {
		upstreamRequestID = r.Header.Get("X-TruvaG3-Request-ID")
	}

	// 3. Add span attributes for business context (searchable in Jaeger)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "slack-tool"),
		attribute.String("truvag3.capability", "search_messages"),
	)

	// 4. Add span event for request start
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "search_messages"),
	)

	s.Logger.InfoWithContext(ctx, "Processing search messages request", map[string]interface{}{
		"operation":  "search_messages",
		"method":     r.Method,
		"path":       r.URL.Path,
		"request_id": upstreamRequestID,
	})

	// 5. Decode request
	var req SearchMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.errors.total",
			"capability", "search_messages",
			"error_type", "decode_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"operation":  "search_messages",
			"request_id": upstreamRequestID,
			"error":      err.Error(),
			"error_type": "decode_error",
		})
		s.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Validate required fields
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("validation error: query is required"))
		telemetry.Counter("slack.errors.total",
			"capability", "search_messages",
			"error_type", "validation_error",
			"module", "tool",
		)
		s.Logger.ErrorWithContext(ctx, "Missing required fields", map[string]interface{}{
			"operation":  "search_messages",
			"request_id": upstreamRequestID,
			"error":      "query is required",
			"error_type": "validation_error",
		})
		s.sendError(rw, "query is required", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Add business context to span attributes for filtering
	telemetry.SetSpanAttributes(ctx,
		attribute.String("slack.query", req.Query),
	)

	s.Logger.InfoWithContext(ctx, "Received search messages request", map[string]interface{}{
		"operation":  "search_messages",
		"query":      req.Query,
		"count":      req.Count,
		"sort":       req.Sort,
		"request_id": upstreamRequestID,
	})

	// 6. Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_slack_api",
		attribute.String("query", req.Query),
		attribute.String("api", "search.messages"),
	)

	// 7. Call Slack API with timing
	apiStartTime := time.Now()
	slackResp, err := s.client.SearchMessages(ctx, req.Query, req.Count, req.Sort)
	apiDuration := time.Since(apiStartTime)

	// Record API latency as histogram metric
	telemetry.Histogram("slack.api.duration_ms",
		float64(apiDuration.Milliseconds()),
		"capability", "search_messages",
		"api", "search.messages",
	)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("slack.api.errors.total",
			"capability", "search_messages",
			"error_type", "api_error",
			"module", "tool",
		)

		// Extract Slack error string for logging if available
		errDetail := err.Error()
		if slackErr, ok := err.(*SlackAPIError); ok {
			errDetail = slackErr.SlackError
		}

		s.Logger.ErrorWithContext(ctx, "Slack API call failed", map[string]interface{}{
			"operation":   "search_messages",
			"error":       errDetail,
			"query":       req.Query,
			"duration_ms": apiDuration.Milliseconds(),
			"request_id":  upstreamRequestID,
		})
		s.sendUpstreamError(rw, "Slack API failed: "+err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Convert SlackSearchMatch to SearchMatch
	s.Logger.InfoWithContext(ctx, "Slack API call successful", map[string]interface{}{
		"operation":   "search_messages",
		"query":       req.Query,
		"duration_ms": apiDuration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	matches := make([]SearchMatch, 0)
	totalCount := 0
	if slackResp.Messages != nil {
		totalCount = slackResp.Messages.Total
		for _, m := range slackResp.Messages.Matches {
			matches = append(matches, SearchMatch{
				Channel:   m.Channel.Name,
				Text:      m.Text,
				Username:  m.Username,
				Timestamp: m.TS,
				Permalink: m.Permalink,
			})
		}
	}

	response := SearchMessagesResponse{
		Query:      req.Query,
		Matches:    matches,
		TotalCount: totalCount,
		Source:     "Slack Web API",
	}

	// Add span attributes for the result
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("slack.match_count", len(response.Matches)),
	)

	// 8. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("slack.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "search_messages",
	)
	telemetry.Counter("slack.requests.total",
		"capability", "search_messages",
		"status", "success",
		"module", "tool",
	)

	// 9. Record unified metrics for dashboard integration
	telemetry.RecordToolCall("slack-tool", "search_messages", float64(duration.Milliseconds()), "success")

	// 10. Add completion span event
	telemetry.AddSpanEvent(ctx, "messages_searched",
		attribute.String("query", response.Query),
		attribute.Int("match_count", len(response.Matches)),
		attribute.Int("total_count", response.TotalCount),
		attribute.String("source", response.Source),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 11. Log completion with context
	s.Logger.InfoWithContext(ctx, "Search messages request completed", map[string]interface{}{
		"operation":   "search_messages",
		"query":       response.Query,
		"match_count": len(response.Matches),
		"total_count": response.TotalCount,
		"source":      response.Source,
		"status":      "success",
		"duration_ms": duration.Milliseconds(),
		"request_id":  upstreamRequestID,
	})

	// 12. Send response with core.ToolResponse wrapper
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}
