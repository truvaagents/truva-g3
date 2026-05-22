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
	Operation string `json:"operation"`          // Required: "add" or "subtract"
	Value     int    `json:"value"`              // Required: number of units
	Unit      string `json:"unit"`               // Required: "days"|"hours"|"minutes"|"weeks"|"months"|"years"
	Timezone  string `json:"timezone,omitempty"` // Optional: timezone for the calculation
}

// ExecuteCommandRequest represents the input for execute_command
type ExecuteCommandRequest struct {
	Command          string `json:"command"`                     // Required: shell command to execute
	Timeout          int    `json:"timeout,omitempty"`           // Optional: timeout in seconds (default 30, max 300)
	WorkingDirectory string `json:"working_directory,omitempty"` // Optional: working directory
}

// GenerateIDRequest represents the input for generate_id
type GenerateIDRequest struct {
	Type  string `json:"type,omitempty"`  // Optional: "uuid"|"ulid"|"nanoid" (default "uuid")
	Count int    `json:"count,omitempty"` // Optional: number of IDs (default 1, max 100)
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

// SleepRequest represents the input for sleep
type SleepRequest struct {
	DurationSeconds int    `json:"duration_seconds"` // Required: seconds to sleep (1-120)
	Reason          string `json:"reason,omitempty"` // Optional: why the pause is needed
}

// SleepResponse represents the output for sleep
type SleepResponse struct {
	RequestedSeconds int    `json:"requested_seconds"` // What the caller asked for
	DurationSeconds  int    `json:"duration_seconds"`  // How long was actually slept (post-clamp)
	Reason           string `json:"reason,omitempty"`  // Echo of input
	StartedAt        string `json:"started_at"`        // RFC 3339 UTC timestamp
	EndedAt          string `json:"ended_at"`          // RFC 3339 UTC timestamp
	Cancelled        bool   `json:"cancelled"`         // True if ctx cancelled early
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
			"Available tools: bash, python3, pip3, curl, jq, bc, git, grep, awk, sed, openssl. " +
			"Network diagnostics: dig (dnsutils), nc (netcat-openbsd), traceroute, ping. " +
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

	// Capability 7: Sleep
	// Auto-generated endpoint: /api/capabilities/sleep
	s.RegisterCapability(core.Capability{
		Name: "sleep",
		Description: "Sleeps (pauses) for a short, bounded duration before returning. " +
			"Use this for brief in-line pauses — e.g., to let an external system settle after a write, " +
			"or to space out polling checks. Max 120 seconds; requests above are clamped. " +
			"For longer or recurring pauses, use scheduler-tool/schedule_task instead. " +
			"Required: duration_seconds (integer, 1-120). Optional: reason (string, echoed into traces).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     s.handleSleep,

		// Phase 2: Field hints for AI payload generation
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{
					Name:        "duration_seconds",
					Type:        "integer",
					Example:     "30",
					Description: "How long to sleep, in seconds. Must be between 1 and 120.",
				},
			},
			OptionalFields: []core.FieldHint{
				{
					Name:        "reason",
					Type:        "string",
					Example:     "waiting for kubectl rollout to propagate",
					Description: "Optional explanation of why the pause is needed; echoed into traces and logs.",
				},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "requested_seconds", Type: "integer", Example: "30", Description: "Duration the caller requested"},
				{Name: "duration_seconds", Type: "integer", Example: "30", Description: "Duration actually slept (post-clamp or post-cancel)"},
				{Name: "started_at", Type: "string", Description: "RFC 3339 UTC timestamp when the sleep began"},
				{Name: "ended_at", Type: "string", Description: "RFC 3339 UTC timestamp when the sleep ended"},
				{Name: "cancelled", Type: "boolean", Example: "false", Description: "True if the sleep was cancelled by context before completing"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "reason", Type: "string", Description: "Echo of the reason supplied in the request"},
			},
		},
	})
}
