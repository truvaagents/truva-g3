package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// SlackTool wraps the Slack Web API as a TruvaG3 tool
// It demonstrates a WRITE-CAPABLE tool pattern (send_message, send_rich_message)
type SlackTool struct {
	*core.BaseTool
	botToken string
	client   *SlackClient
}

// --- Request Types ---

// SendMessageRequest represents the input for sending a text message
type SendMessageRequest struct {
	Channel  string `json:"channel"`             // Channel ID or name (e.g., "C123ABC456")
	Text     string `json:"text"`                // Message text content
	ThreadTS string `json:"thread_ts,omitempty"` // Thread parent timestamp for reply threading
}

// SendRichMessageRequest represents the input for sending a Block Kit message
type SendRichMessageRequest struct {
	Channel string                   `json:"channel"` // Channel ID or name
	Text    string                   `json:"text"`    // Fallback text (required even with blocks)
	Blocks  []map[string]interface{} `json:"blocks"`  // Block Kit blocks array
}

// ListChannelsRequest represents the input for listing channels
type ListChannelsRequest struct {
	Limit           int  `json:"limit,omitempty"`            // Max channels 1-1000, default 100
	ExcludeArchived bool `json:"exclude_archived,omitempty"` // Exclude archived channels
}

// SearchMessagesRequest represents the input for searching messages
type SearchMessagesRequest struct {
	Query string `json:"query"`           // Search query text
	Count int    `json:"count,omitempty"` // Results per page, max 100
	Sort  string `json:"sort,omitempty"`  // Sort: "score" or "timestamp"
}

// --- Response Types ---

// SendMessageResponse represents the output after sending a message
type SendMessageResponse struct {
	Channel   string `json:"channel"`             // Channel where message was posted
	Timestamp string `json:"timestamp"`           // Message timestamp (unique ID)
	Text      string `json:"text"`                // Message text as sent
	ThreadTS  string `json:"thread_ts,omitempty"` // Thread timestamp if threaded
	Source    string `json:"source"`              // "Slack Web API"
}

// SendRichMessageResponse represents the output after sending a Block Kit message
type SendRichMessageResponse struct {
	Channel   string `json:"channel"`   // Channel where message was posted
	Timestamp string `json:"timestamp"` // Message timestamp (unique ID)
	Text      string `json:"text"`      // Fallback text
	Source    string `json:"source"`    // "Slack Web API"
}

// ChannelInfo represents a single Slack channel
type ChannelInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsArchived bool   `json:"is_archived"`
	IsPrivate  bool   `json:"is_private"`
	Topic      string `json:"topic,omitempty"`
	Purpose    string `json:"purpose,omitempty"`
	NumMembers int    `json:"num_members"`
	Created    int64  `json:"created"` // Unix epoch INTEGER (not string)
	Updated    int64  `json:"updated"` // Unix epoch INTEGER (not string)
}

// ListChannelsResponse represents the output for listing channels
type ListChannelsResponse struct {
	Channels   []ChannelInfo `json:"channels"`
	TotalCount int           `json:"total_count"`
	HasMore    bool          `json:"has_more"`    // True if more pages exist
	NextCursor string        `json:"next_cursor"` // Empty string "" means no more pages
	Source     string        `json:"source"`
}

// SearchMatch represents a single message search result
type SearchMatch struct {
	Channel   string `json:"channel"`
	Text      string `json:"text"`
	Username  string `json:"username"`
	Timestamp string `json:"timestamp"` // ts is a STRING (unique message ID)
	Permalink string `json:"permalink"`
}

// SearchMessagesResponse represents the output for searching messages
type SearchMessagesResponse struct {
	Query      string        `json:"query"`
	Matches    []SearchMatch `json:"matches"`
	TotalCount int           `json:"total_count"`
	Source     string        `json:"source"`
}

// --- Block Kit Types ---

// Block represents a single Block Kit block
type Block struct {
	Type string      `json:"type"`
	Text *TextObject `json:"text,omitempty"`
}

// TextObject represents a Block Kit text element
type TextObject struct {
	Type string `json:"type"` // "plain_text" or "mrkdwn"
	Text string `json:"text"`
}

// NewSlackTool creates a new Slack messaging tool
func NewSlackTool() *SlackTool {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	userToken := os.Getenv("SLACK_USER_TOKEN")

	tool := &SlackTool{
		BaseTool: core.NewTool("slack-tool"),
		botToken: botToken,
		client:   NewSlackClient(botToken, userToken),
	}

	// Register all capabilities
	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all Slack-related capabilities
func (s *SlackTool) registerCapabilities() {
	// Capability 1: Send Message (Write)
	// Auto-generated endpoint: /api/capabilities/send_message
	s.RegisterCapability(core.Capability{
		Name:        "send_message",
		Description: "Posts a text message to a Slack channel.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleSendMessage,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "channel",
					Type:        "string",
					Example:     "C123ABC456",
					Description: "Channel ID or name to post message to",
				},
				{
					Name:        "text",
					Type:        "string",
					Example:     "Incident resolved",
					Description: "Message text content",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "thread_ts",
					Type:        "string",
					Example:     "1503435956.000247",
					Description: "Thread parent timestamp for reply threading",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "channel", Type: "string", Description: "Channel where message was posted"},
				{Name: "timestamp", Type: "string", Description: "Message timestamp (unique ID)"},
				{Name: "text", Type: "string", Description: "Message text as sent"},
				{Name: "source", Type: "string", Description: "API source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "thread_ts", Type: "string", Description: "Thread timestamp if threaded"},
			},
		},
	})

	// Capability 2: Send Rich Message (Write)
	// Auto-generated endpoint: /api/capabilities/send_rich_message
	s.RegisterCapability(core.Capability{
		Name:        "send_rich_message",
		Description: "Posts a Block Kit formatted message to a Slack channel.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleSendRichMessage,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "channel",
					Type:        "string",
					Example:     "C123ABC456",
					Description: "Channel ID or name to post message to",
				},
				{
					Name:        "text",
					Type:        "string",
					Example:     "Incident summary",
					Description: "Fallback text, required even with blocks",
				},
				{
					Name:        "blocks",
					Type:        "array",
					Example:     `[{"type":"header","text":{"type":"plain_text","text":"Alert"}}]`,
					Description: "Block Kit blocks array",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "channel", Type: "string", Description: "Channel where message was posted"},
				{Name: "timestamp", Type: "string", Description: "Message timestamp (unique ID)"},
				{Name: "text", Type: "string", Description: "Fallback text"},
				{Name: "source", Type: "string", Description: "API source identifier"},
			},
		},
	})

	// Capability 3: List Channels (Read)
	// Auto-generated endpoint: /api/capabilities/list_channels
	s.RegisterCapability(core.Capability{
		Name:        "list_channels",
		Description: "Lists public channels in the Slack workspace.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleListChannels,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "limit",
					Type:        "number",
					Example:     "100",
					Description: "Max channels 1-1000, default 100",
				},
				{
					Name:        "exclude_archived",
					Type:        "boolean",
					Example:     "true",
					Description: "Exclude archived channels",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "channels", Type: "array", Description: "List of channels with id, name, is_archived, is_private, topic, purpose, and num_members"},
				{Name: "total_count", Type: "number", Description: "Total number of channels returned"},
				{Name: "has_more", Type: "boolean", Description: "True if more pages exist"},
				{Name: "source", Type: "string", Description: "API source identifier"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "next_cursor", Type: "string", Description: "Pagination cursor for next page (empty if no more pages)"},
			},
		},
	})

	// Capability 4: Search Messages (Read)
	// Auto-generated endpoint: /api/capabilities/search_messages
	s.RegisterCapability(core.Capability{
		Name:        "search_messages",
		Description: "Searches message history in the Slack workspace.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleSearchMessages,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "query",
					Type:        "string",
					Example:     "incident deploy",
					Description: "Search query text",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "count",
					Type:        "number",
					Example:     "20",
					Description: "Results per page, max 100",
				},
				{
					Name:        "sort",
					Type:        "string",
					Example:     "timestamp",
					Description: "Sort: score or timestamp",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Description: "Search query that was executed"},
				{Name: "matches", Type: "array", Description: "List of matching messages with channel, text, username, timestamp, and permalink"},
				{Name: "total_count", Type: "number", Description: "Total number of matching messages"},
				{Name: "source", Type: "string", Description: "API source identifier"},
			},
		},
	})
}
