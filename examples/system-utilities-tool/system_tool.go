package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// SystemTool provides system utility capabilities: date/time, command execution, and ID generation.
// It is entirely self-contained — no external API clients needed.
type SystemTool struct {
	*core.BaseTool
}

// --- Request Types ---

// GetCurrentTimeRequest represents the input for get_current_time
type GetCurrentTimeRequest struct {
	Timezone string `json:"timezone"`         // Required: IANA timezone (e.g. "Asia/Seoul")
	Format   string `json:"format,omitempty"` // Optional: "iso8601"|"unix"|"human" or Go layout
}

// ConvertTimezoneRequest represents the input for convert_timezone
type ConvertTimezoneRequest struct {
	Datetime     string `json:"datetime"`      // Required: ISO 8601 datetime string
	FromTimezone string `json:"from_timezone"` // Required: source IANA timezone
	ToTimezone   string `json:"to_timezone"`   // Required: target IANA timezone
}

// ListTimezonesRequest represents the input for list_timezones
type ListTimezonesRequest struct {
	Region string `json:"region,omitempty"` // Optional: filter by region (e.g. "Asia", "Europe")
}

// DateArithmeticRequest represents the input for date_arithmetic
type DateArithmeticRequest struct {
	Date      string `json:"date"`               // Required: YYYY-MM-DD or ISO 8601
	Operation string `json:"operation"`           // Required: "add" or "subtract"
	Value     int    `json:"value"`               // Required: number of units
	Unit      string `json:"unit"`                // Required: "days"|"hours"|"minutes"|"weeks"|"months"|"years"
	Timezone  string `json:"timezone,omitempty"`  // Optional: timezone for the calculation
}

// ExecuteCommandRequest represents the input for execute_command
type ExecuteCommandRequest struct {
	Command          string `json:"command"`                      // Required: shell command to execute
	Timeout          int    `json:"timeout,omitempty"`            // Optional: timeout in seconds (default 30, max 300)
	WorkingDirectory string `json:"working_directory,omitempty"`  // Optional: working directory
}

// GenerateIDRequest represents the input for generate_id
type GenerateIDRequest struct {
	Type  string `json:"type,omitempty"`  // Optional: "uuid"|"ulid"|"nanoid" (default "uuid")
	Count int    `json:"count,omitempty"` // Optional: number of IDs (default 1, max 100)
}

// StealthBrowserRequest represents the input for stealth_browser
type StealthBrowserRequest struct {
	URL            string `json:"url"`                        // Required: URL to navigate to
	WaitFor        string `json:"wait_for,omitempty"`         // Optional: CSS selector to wait for before extracting
	ExtractContent string `json:"extract_content,omitempty"`  // Optional: "text"|"html"|"both" (default "text")
	Screenshot     bool   `json:"screenshot,omitempty"`       // Optional: capture screenshot (default false)
	Timeout        int    `json:"timeout,omitempty"`          // Optional: navigation timeout in seconds (default 30, max 120)
	JavaScript     string `json:"javascript,omitempty"`       // Optional: JS to execute after page load
	UserAgent      string `json:"user_agent,omitempty"`       // Optional: custom user-agent override
}

// --- Response Types ---

// GetCurrentTimeResponse represents the output for get_current_time
type GetCurrentTimeResponse struct {
	Timezone      string `json:"timezone"`
	Datetime      string `json:"datetime"`
	UnixTimestamp int64  `json:"unix_timestamp"`
	UTCOffset     string `json:"utc_offset"`
	IsDST         bool   `json:"is_dst"`
	Abbreviation  string `json:"abbreviation"`
}

// ConvertTimezoneResponse represents the output for convert_timezone
type ConvertTimezoneResponse struct {
	Original         string `json:"original"`
	Converted        string `json:"converted"`
	FromTimezone     string `json:"from_timezone"`
	ToTimezone       string `json:"to_timezone"`
	OffsetDifference string `json:"offset_difference"`
}

// TimezoneInfo represents a single timezone entry
type TimezoneInfo struct {
	Name          string `json:"name"`
	CurrentOffset string `json:"current_offset"`
	Abbreviation  string `json:"abbreviation"`
}

// ListTimezonesResponse represents the output for list_timezones
type ListTimezonesResponse struct {
	Region    string         `json:"region"`
	Timezones []TimezoneInfo `json:"timezones"`
}

// DateArithmeticResponse represents the output for date_arithmetic
type DateArithmeticResponse struct {
	OriginalDate string `json:"original_date"`
	ResultDate   string `json:"result_date"`
	Operation    string `json:"operation"`
	Value        int    `json:"value"`
	Unit         string `json:"unit"`
	DaysBetween  int    `json:"days_between"`
}

// ExecuteCommandResponse represents the output for execute_command
type ExecuteCommandResponse struct {
	Command    string `json:"command"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// GenerateIDResponse represents the output for generate_id
type GenerateIDResponse struct {
	Type string   `json:"type"`
	IDs  []string `json:"ids"`
}

// StealthBrowserResponse represents the output for stealth_browser
type StealthBrowserResponse struct {
	URL              string `json:"url"`                          // Final URL (may differ from input due to redirects)
	Title            string `json:"title"`                        // Page title
	TextContent      string `json:"text_content,omitempty"`       // Extracted text (if extract_content is text or both)
	HTMLContent      string `json:"html_content,omitempty"`       // Extracted HTML (if extract_content is html or both)
	ScreenshotBase64 string `json:"screenshot_base64,omitempty"`  // Base64-encoded PNG screenshot
	JSResult         string `json:"js_result,omitempty"`          // Result of JavaScript execution
	StatusCode       int    `json:"status_code"`                  // HTTP response status code
	DurationMs       int64  `json:"duration_ms"`                  // Browser operation duration
}

// BrowserTestRequest represents the input for browser_test (SPA UI testing)
type BrowserTestRequest struct {
	URL      string          `json:"url"`                // Required: starting URL
	Actions  []BrowserAction `json:"actions"`            // Required: ordered test steps
	Timeout  int             `json:"timeout,omitempty"`  // Optional: overall test timeout in seconds (default 120, max 300)
	Viewport *Viewport       `json:"viewport,omitempty"` // Optional: viewport size (default 1280x720)
}

// Viewport represents browser viewport dimensions for responsive testing
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// BrowserAction represents a single test step in a browser_test sequence
type BrowserAction struct {
	Action    string `json:"action"`              // Action type: click, fill, select, check, uncheck, hover, press, navigate, wait_for_selector, wait_for_url, wait_for_network_idle, screenshot, assert
	Selector  string `json:"selector,omitempty"`  // CSS or Playwright selector for the target element
	Value     string `json:"value,omitempty"`     // Input value / target URL / key name
	Timeout   int    `json:"timeout,omitempty"`   // Per-step timeout in ms (default 10000)
	Assertion string `json:"assertion,omitempty"` // Assertion type (for assert action): visible, hidden, text_contains, text_equals, url_contains, url_equals, count_equals, has_attribute, has_class
	Expected  string `json:"expected,omitempty"`  // Expected value for assertions
}

// BrowserTestResponse represents the output for browser_test
type BrowserTestResponse struct {
	URL         string              `json:"url"`                   // Final page URL
	Passed      bool                `json:"passed"`                // Overall pass/fail
	TotalSteps  int                 `json:"total_steps"`           // Total number of steps
	PassedSteps int                 `json:"passed_steps"`          // Number of passed steps
	FailedSteps int                 `json:"failed_steps"`          // Number of failed steps
	Steps       []BrowserStepResult `json:"steps"`                 // Per-step results
	Screenshots map[string]string   `json:"screenshots,omitempty"` // step_index -> base64 PNG
	ConsoleLog  []string            `json:"console_log,omitempty"` // Browser console output
	DurationMs  int64               `json:"duration_ms"`           // Total browser execution duration
}

// BrowserStepResult represents the result of a single test step
type BrowserStepResult struct {
	Step       int    `json:"step"`                // 0-indexed step number
	Action     string `json:"action"`              // Action type that was executed
	Selector   string `json:"selector,omitempty"`  // Selector used (if any)
	Passed     bool   `json:"passed"`              // Whether the step passed
	Error      string `json:"error,omitempty"`     // Error message if failed
	DurationMs int64  `json:"duration_ms"`         // Step execution duration
}

// WaitRequest represents the input for wait
type WaitRequest struct {
	DurationSeconds int    `json:"duration_seconds"`    // Required: seconds to wait (1-120)
	Reason          string `json:"reason,omitempty"`     // Optional: why the wait is needed
}

// WaitResponse represents the output for wait
type WaitResponse struct {
	RequestedSeconds int    `json:"requested_seconds"` // What the caller asked for
	DurationSeconds  int    `json:"duration_seconds"`  // How long was actually waited (post-clamp)
	Reason           string `json:"reason,omitempty"`   // Echo of input
	StartedAt        string `json:"started_at"`         // RFC 3339 UTC timestamp
	EndedAt          string `json:"ended_at"`           // RFC 3339 UTC timestamp
	Cancelled        bool   `json:"cancelled"`          // True if ctx cancelled early
}

// --- Timezone Data ---

// timezonesByRegion contains curated IANA timezone names grouped by region.
// Go's time.LoadLocation() handles actual offset/DST resolution from the system's tzdata.
var timezonesByRegion = map[string][]string{
	"Africa": {
		"Africa/Cairo", "Africa/Casablanca", "Africa/Johannesburg", "Africa/Lagos",
		"Africa/Nairobi", "Africa/Tunis", "Africa/Accra", "Africa/Addis_Ababa",
		"Africa/Algiers", "Africa/Dar_es_Salaam", "Africa/Khartoum",
	},
	"America": {
		"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
		"America/Anchorage", "America/Phoenix", "America/Toronto", "America/Vancouver",
		"America/Mexico_City", "America/Bogota", "America/Lima", "America/Santiago",
		"America/Buenos_Aires", "America/Sao_Paulo", "America/Caracas",
		"America/Havana", "America/Jamaica", "America/Panama",
	},
	"Asia": {
		"Asia/Tokyo", "Asia/Seoul", "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Taipei",
		"Asia/Singapore", "Asia/Kolkata", "Asia/Mumbai", "Asia/Dubai", "Asia/Riyadh",
		"Asia/Tehran", "Asia/Baghdad", "Asia/Karachi", "Asia/Dhaka", "Asia/Bangkok",
		"Asia/Ho_Chi_Minh", "Asia/Jakarta", "Asia/Manila", "Asia/Kuala_Lumpur",
		"Asia/Almaty", "Asia/Tashkent", "Asia/Vladivostok", "Asia/Novosibirsk",
	},
	"Australia": {
		"Australia/Sydney", "Australia/Melbourne", "Australia/Brisbane",
		"Australia/Perth", "Australia/Adelaide", "Australia/Darwin",
		"Australia/Hobart",
	},
	"Europe": {
		"Europe/London", "Europe/Paris", "Europe/Berlin", "Europe/Rome",
		"Europe/Madrid", "Europe/Amsterdam", "Europe/Brussels", "Europe/Zurich",
		"Europe/Vienna", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen",
		"Europe/Helsinki", "Europe/Warsaw", "Europe/Prague", "Europe/Budapest",
		"Europe/Bucharest", "Europe/Athens", "Europe/Istanbul", "Europe/Moscow",
		"Europe/Kiev", "Europe/Dublin", "Europe/Lisbon",
	},
	"Pacific": {
		"Pacific/Auckland", "Pacific/Fiji", "Pacific/Honolulu",
		"Pacific/Guam", "Pacific/Samoa", "Pacific/Tahiti",
	},
	"UTC": {
		"UTC",
	},
}

// NewSystemTool creates a new system utilities tool
func NewSystemTool() *SystemTool {
	tool := &SystemTool{
		BaseTool: core.NewTool("system-utilities-tool"),
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all system utility capabilities
func (s *SystemTool) registerCapabilities() {

	// Capability 1: Get Current Time
	// Auto-generated endpoint: /api/capabilities/get_current_time
	// Schema endpoint: /api/capabilities/get_current_time/schema
	s.RegisterCapability(core.Capability{
		Name:        "get_current_time",
		Description: "Gets the current date and time in a specified timezone. Returns datetime, unix timestamp, UTC offset, DST status, and timezone abbreviation.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleGetCurrentTime,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "timezone",
					Type:        "string",
					Example:     "Asia/Seoul",
					Description: "IANA timezone name (e.g., Asia/Seoul, America/New_York, Europe/London, UTC)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "format",
					Type:        "string",
					Example:     "iso8601",
					Description: "Output format: iso8601, unix, human, or a Go time layout string",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "timezone", Type: "string", Description: "Requested timezone name"},
				{Name: "datetime", Type: "string", Description: "Formatted datetime string"},
				{Name: "unix_timestamp", Type: "number", Description: "Unix epoch timestamp"},
				{Name: "utc_offset", Type: "string", Description: "UTC offset string (e.g. +09:00)"},
				{Name: "is_dst", Type: "boolean", Description: "Whether daylight saving time is active"},
				{Name: "abbreviation", Type: "string", Description: "Timezone abbreviation (e.g. KST, EST)"},
			},
		},
	})

	// Capability 2: Convert Timezone
	// Auto-generated endpoint: /api/capabilities/convert_timezone
	s.RegisterCapability(core.Capability{
		Name:        "convert_timezone",
		Description: "Converts a datetime from one timezone to another. Returns original and converted times with offset difference.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleConvertTimezone,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "datetime",
					Type:        "string",
					Example:     "2026-02-23T15:00:00Z",
					Description: "ISO 8601 datetime string to convert",
				},
				{
					Name:        "from_timezone",
					Type:        "string",
					Example:     "UTC",
					Description: "Source IANA timezone name",
				},
				{
					Name:        "to_timezone",
					Type:        "string",
					Example:     "Asia/Tokyo",
					Description: "Target IANA timezone name",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "original", Type: "string", Description: "Original datetime in source timezone"},
				{Name: "converted", Type: "string", Description: "Converted datetime in target timezone"},
				{Name: "from_timezone", Type: "string", Description: "Source timezone name"},
				{Name: "to_timezone", Type: "string", Description: "Target timezone name"},
				{Name: "offset_difference", Type: "string", Description: "Offset difference between timezones"},
			},
		},
	})

	// Capability 3: List Timezones
	// Auto-generated endpoint: /api/capabilities/list_timezones
	s.RegisterCapability(core.Capability{
		Name:        "list_timezones",
		Description: "Lists available timezones grouped by region with current offsets.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleListTimezones,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "region",
					Type:        "string",
					Example:     "Asia",
					Description: "Region to filter: Africa, America, Asia, Australia, Europe, Pacific, UTC",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "region", Type: "string", Description: "Region filter applied (or 'all')"},
				{Name: "timezones", Type: "array", Description: "List of timezones with name, current_offset, and abbreviation"},
			},
		},
	})

	// Capability 4: Date Arithmetic
	// Auto-generated endpoint: /api/capabilities/date_arithmetic
	s.RegisterCapability(core.Capability{
		Name:        "date_arithmetic",
		Description: "Performs date/time arithmetic — add or subtract durations from a date. Returns the resulting date and the number of days between original and result.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleDateArithmetic,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "date",
					Type:        "string",
					Example:     "2026-02-23",
					Description: "Starting date in YYYY-MM-DD or ISO 8601 format",
				},
				{
					Name:        "operation",
					Type:        "string",
					Example:     "add",
					Description: "Operation to perform: add or subtract",
				},
				{
					Name:        "value",
					Type:        "integer",
					Example:     "7",
					Description: "Number of units to add or subtract",
				},
				{
					Name:        "unit",
					Type:        "string",
					Example:     "days",
					Description: "Unit of time: days, hours, minutes, weeks, months, or years",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "timezone",
					Type:        "string",
					Example:     "America/New_York",
					Description: "Timezone for the calculation (default: UTC)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "original_date", Type: "string", Description: "Starting date"},
				{Name: "result_date", Type: "string", Description: "Resulting date after arithmetic"},
				{Name: "operation", Type: "string", Description: "Operation performed (add/subtract)"},
				{Name: "value", Type: "number", Description: "Number of units applied"},
				{Name: "unit", Type: "string", Description: "Unit of time applied"},
				{Name: "days_between", Type: "number", Description: "Number of days between original and result"},
			},
		},
	})

	// Capability 5: Execute Command
	// Auto-generated endpoint: /api/capabilities/execute_command
	s.RegisterCapability(core.Capability{
		Name: "execute_command",
		Description: "Executes a shell command and returns stdout, stderr, and exit code. " +
			"Runs inside an isolated container with non-root user. " +
			"Available tools: bash, curl, jq, bc, git, python3, pip3, grep, awk, sed. " +
			"Pre-installed Python packages: numpy, requests. " +
			"To install additional packages at runtime: pip3 install --user <package>.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleExecuteCommand,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "command",
					Type:        "string",
					Example:     "echo hello world",
					Description: "Shell command to execute (run as-is via sh -c)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "timeout",
					Type:        "integer",
					Example:     "30",
					Description: "Execution timeout in seconds (default 30, max 300)",
				},
				{
					Name:        "working_directory",
					Type:        "string",
					Example:     "/tmp",
					Description: "Working directory for command execution",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "command", Type: "string", Description: "The command that was executed"},
				{Name: "stdout", Type: "string", Description: "Standard output from the command"},
				{Name: "stderr", Type: "string", Description: "Standard error output from the command"},
				{Name: "exit_code", Type: "number", Description: "Process exit code (0 = success)", Example: "0"},
				{Name: "duration_ms", Type: "number", Description: "Command execution duration in milliseconds", Example: "150"},
			},
		},
	})

	// Capability 6: Generate ID
	// Auto-generated endpoint: /api/capabilities/generate_id
	s.RegisterCapability(core.Capability{
		Name:        "generate_id",
		Description: "Generates unique identifiers. Supports UUID v4, ULID, and nanoid formats.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleGenerateID,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{
					Name:        "type",
					Type:        "string",
					Example:     "uuid",
					Description: "ID format: uuid, ulid, or nanoid (default: uuid)",
				},
				{
					Name:        "count",
					Type:        "integer",
					Example:     "3",
					Description: "Number of IDs to generate (default 1, max 100)",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "type", Type: "string", Description: "ID format used (uuid, ulid, or nanoid)"},
				{Name: "ids", Type: "array", Description: "List of generated unique identifiers"},
			},
		},
	})

	// Capability 7: Stealth Browser
	// Auto-generated endpoint: /api/capabilities/stealth_browser
	// Schema endpoint: /api/capabilities/stealth_browser/schema
	s.RegisterCapability(core.Capability{
		Name: "stealth_browser",
		Description: "Opens a URL in a headless stealth Chromium browser (with anti-detection) and extracts page content. " +
			"Uses Playwright with stealth plugin to bypass bot-detection, CAPTCHAs, and fingerprinting. " +
			"Use for: scraping JavaScript-rendered pages, accessing bot-protected sites, taking screenshots, " +
			"executing a single JS snippet on a page. " +
			"Returns: page title, text/HTML content, optional screenshot (base64 PNG), optional JS execution result. " +
			"NOTE: For multi-step interactions (clicking buttons, filling forms, navigating between pages, " +
			"asserting element state), use the browser_test capability instead — it supports ordered action " +
			"sequences with per-step pass/fail reporting. stealth_browser is for single-page content extraction only. " +
			"IMPORTANT: Each call launches a full Chromium browser process. To avoid resource exhaustion, " +
			"chain stealth_browser steps sequentially using depends_on rather than running them all in parallel. " +
			"For multiple page loads, use at most 2 concurrent calls. Use timeout of 60 for JavaScript-heavy SPA sites.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleStealthBrowser,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "url",
					Type:        "string",
					Example:     "https://example.com",
					Description: "Full URL to navigate to (must include protocol, e.g. https://)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "wait_for",
					Type:        "string",
					Example:     "#main-content",
					Description: "CSS selector to wait for before extracting content (useful for JS-rendered pages)",
				},
				{
					Name:        "extract_content",
					Type:        "string",
					Example:     "text",
					Description: "What to extract: text (visible text), html (full HTML), or both (default: text)",
				},
				{
					Name:        "screenshot",
					Type:        "boolean",
					Example:     "false",
					Description: "Capture a full-page screenshot as base64 PNG (default: false)",
				},
				{
					Name:        "timeout",
					Type:        "integer",
					Example:     "60",
					Description: "Navigation timeout in seconds (default 60, max 120). Use 60+ for JavaScript-heavy SPA sites.",
				},
				{
					Name:        "javascript",
					Type:        "string",
					Example:     "return document.querySelectorAll('h1').length",
					Description: "JavaScript code to execute on the page after load. Must include an explicit 'return' statement to return a value. Supports multi-statement code and async/await.",
				},
				{
					Name:        "user_agent",
					Type:        "string",
					Example:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
					Description: "Custom User-Agent string to override the default browser UA",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "url", Type: "string", Description: "Final URL (may differ from input due to redirects)"},
				{Name: "title", Type: "string", Description: "Page title"},
				{Name: "status_code", Type: "number", Description: "HTTP response status code"},
				{Name: "duration_ms", Type: "number", Description: "Browser operation duration in milliseconds"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "text_content", Type: "string", Description: "Extracted visible text content"},
				{Name: "html_content", Type: "string", Description: "Extracted HTML content"},
				{Name: "screenshot_base64", Type: "string", Description: "Base64-encoded PNG screenshot"},
				{Name: "js_result", Type: "string", Description: "Result of JavaScript execution"},
			},
		},
	})

	// Capability 8: Browser Test (SPA UI Testing)
	// Auto-generated endpoint: /api/capabilities/browser_test
	// Schema endpoint: /api/capabilities/browser_test/schema (auto-generated from InputSummary)
	s.RegisterCapability(core.Capability{
		Name: "browser_test",
		Description: "Executes a multi-step UI test in a headless Chromium browser using Playwright with stealth anti-detection. " +
			"Designed for testing Single Page Applications (Vue.js, React, Angular). " +
			"Use for: login flows, form submissions, navigation testing, CRUD operations, " +
			"responsive layout verification, SPA route transitions. " +
			"SPA TESTING GUIDELINES: After clicks that trigger route changes, add wait_for_url before assertions. " +
			"After navigation, add wait_for_network_idle to let async data fetches complete. " +
			"Prefer [data-testid='...'] selectors over CSS class selectors for stability. " +
			"Use wait_for_selector before interacting with dynamically rendered components. " +
			"RESOURCE USAGE: Each call launches a full Chromium browser process. " +
			"Chain browser_test steps sequentially using depends_on rather than running them in parallel. " +
			"Use at most 2 concurrent calls.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleBrowserTest,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "url",
					Type:        "string",
					Example:     "https://myapp.com/login",
					Description: "Full starting URL to navigate to (must include protocol, e.g. https://)",
				},
				{
					Name: "actions",
					Type: "array",
					Example: `[{"action":"wait_for_selector","selector":"[data-testid='email-input']"},` +
						`{"action":"fill","selector":"[data-testid='email-input']","value":"user@test.com"},` +
						`{"action":"click","selector":"[data-testid='login-button']"},` +
						`{"action":"wait_for_url","value":"**/dashboard**"},` +
						`{"action":"wait_for_network_idle"},` +
						`{"action":"assert","assertion":"visible","selector":"[data-testid='welcome-message']"},` +
						`{"action":"assert","assertion":"text_contains","selector":"[data-testid='welcome-message']","expected":"Welcome"},` +
						`{"action":"screenshot"}]`,
					Description: "Ordered array of test steps. Each step is an object with: " +
						"'action' (required: click/fill/select/check/uncheck/hover/press/navigate/" +
						"wait_for_selector/wait_for_url/wait_for_network_idle/screenshot/assert), " +
						"'selector' (CSS or [data-testid='...'] selector for the target element), " +
						"'value' (input text for fill, option for select, key for press, URL for navigate/wait_for_url), " +
						"'timeout' (per-step timeout in ms, default 10000), " +
						"'assertion' (for assert action: visible/hidden/text_contains/text_equals/" +
						"url_contains/url_equals/count_equals/has_attribute/has_class), " +
						"'expected' (expected value for text_contains/text_equals/url_contains/url_equals/count_equals/has_attribute/has_class assertions)",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "timeout",
					Type:        "number",
					Example:     "120",
					Description: "Overall test timeout in seconds (default 120, max 300). Use 120+ for multi-step SPA flows.",
				},
				{
					Name:        "viewport",
					Type:        "object",
					Example:     `{"width": 1280, "height": 720}`,
					Description: "Browser viewport dimensions for responsive testing. Default: 1280x720. Use {\"width\": 375, \"height\": 812} for mobile (iPhone).",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "url", Type: "string", Description: "Final page URL"},
				{Name: "passed", Type: "boolean", Description: "Overall pass/fail result"},
				{Name: "total_steps", Type: "number", Description: "Total number of test steps"},
				{Name: "passed_steps", Type: "number", Description: "Number of passed steps"},
				{Name: "failed_steps", Type: "number", Description: "Number of failed steps"},
				{Name: "steps", Type: "array", Description: "Per-step results with step number, action, passed status, and error if any"},
				{Name: "duration_ms", Type: "number", Description: "Total browser execution duration in milliseconds"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "screenshots", Type: "object", Description: "Map of step_index to base64 PNG screenshots"},
				{Name: "console_log", Type: "array", Description: "Browser console output"},
			},
		},
	})

	// Capability 9: Wait
	// Auto-generated endpoint: /api/capabilities/wait
	s.RegisterCapability(core.Capability{
		Name: "wait",
		Description: "Waits (sleeps) for a short, bounded duration before returning. " +
			"Use this for brief in-line pauses — e.g., to let an external system settle after a write, " +
			"or to space out polling checks. Max 120 seconds; requests above are clamped. " +
			"For longer or recurring waits, use scheduler-tool/schedule_task instead. " +
			"Required: duration_seconds (integer, 1-120). Optional: reason (string, echoed into traces).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleWait,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "duration_seconds",
					Type:        "integer",
					Example:     "30",
					Description: "How long to wait, in seconds. Must be between 1 and 120.",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "reason",
					Type:        "string",
					Example:     "waiting for kubectl rollout to propagate",
					Description: "Optional explanation of why the wait is needed; echoed into traces and logs.",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "requested_seconds", Type: "integer", Example: "30", Description: "Duration the caller requested"},
				{Name: "duration_seconds", Type: "integer", Example: "30", Description: "Duration actually waited (post-clamp or post-cancel)"},
				{Name: "started_at", Type: "string", Description: "RFC 3339 UTC timestamp when the wait began"},
				{Name: "ended_at", Type: "string", Description: "RFC 3339 UTC timestamp when the wait ended"},
				{Name: "cancelled", Type: "boolean", Example: "false", Description: "True if the wait was cancelled by context before completing"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "reason", Type: "string", Description: "Echo of the reason supplied in the request"},
			},
		},
	})
}
