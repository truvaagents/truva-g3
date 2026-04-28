package main

import (
	"fmt"
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// PlaywrightTool provides browser automation capabilities for autonomous QA testing.
// It explores web pages, runs Playwright tests, uploads artifacts to S3, and indexes results in Redis.
type PlaywrightTool struct {
	*core.BaseTool
	browser  *BrowserClient
	s3       *S3Client
	store    *TestStore
	s3Ready  bool // true if S3 client initialized successfully
}

// --- Request Types ---

// ExplorePageRequest represents the input for explore_page
type ExplorePageRequest struct {
	URL          string `json:"url"`                        // Required: target page URL
	Depth        int    `json:"depth,omitempty"`            // Optional: link follow depth (default 1, max 3)
	FollowLinks  bool   `json:"follow_links,omitempty"`     // Optional: follow same-origin links (default false)
	Viewport     string `json:"viewport,omitempty"`         // Optional: WxH (default "1280x720")
	WaitForSPA   *bool  `json:"wait_for_spa,omitempty"`     // Optional: enable SPA detection (default true)
	SPATimeoutMs int    `json:"spa_timeout_ms,omitempty"`   // Optional: SPA hydration wait (default 15000)
}

// RunTestsRequest represents the input for run_tests
type RunTestsRequest struct {
	Script          string    `json:"script,omitempty"`            // Conditionally required: Playwright TypeScript test code
	TargetURL       string    `json:"target_url"`                  // Required: website URL being tested
	ScriptName      string    `json:"script_name,omitempty"`       // Optional: name for S3 storage
	ReuseScriptName string    `json:"reuse_script_name,omitempty"` // Optional: fetch stored script from S3 by name
	TimeoutMs       int       `json:"timeout_ms,omitempty"`        // Optional: overall timeout (default 60000, max 300000)
	Browser         string    `json:"browser,omitempty"`           // Optional: chromium/firefox/webkit (default chromium)
	Viewport        *Viewport `json:"viewport,omitempty"`          // Optional: viewport dimensions
}

// Viewport represents browser viewport dimensions
type Viewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// GetResultsRequest represents the input for get_results
type GetResultsRequest struct {
	Site        string `json:"site,omitempty"`         // Optional: filter by domain
	Status      string `json:"status,omitempty"`       // Optional: passed/failed/mixed
	FromDate    string `json:"from_date,omitempty"`    // Optional: YYYY-MM-DD
	ToDate      string `json:"to_date,omitempty"`      // Optional: YYYY-MM-DD
	Limit       int    `json:"limit,omitempty"`        // Optional: max results (default 20)
	IncludeURLs bool   `json:"include_urls,omitempty"` // Optional: generate fresh pre-signed URLs
}

// GetArtifactsRequest represents the input for get_artifacts
type GetArtifactsRequest struct {
	RunID       string `json:"run_id"`                  // Required: test run ID
	ExpiryHours int    `json:"expiry_hours,omitempty"`  // Optional: URL validity in hours (default 24, max 168)
}

// LookupScriptsRequest represents the input for lookup_scripts
type LookupScriptsRequest struct {
	Hostname string `json:"hostname"` // Required: subdomain to look up (e.g. "developer.cisco.com")
}

// LookupScriptsResponse represents the output from lookup_scripts
type LookupScriptsResponse struct {
	Hostname string           `json:"hostname"`
	Scripts  []ScriptMetadata `json:"scripts"`
}

// --- Response Types ---

// PageAnalysis represents the structured output from explore_page
type PageAnalysis struct {
	URL                 string               `json:"url"`
	Title               string               `json:"title"`
	PagesFound          []string             `json:"pages_found,omitempty"`
	Navigation          []NavElement         `json:"navigation,omitempty"`
	Forms               []FormElement        `json:"forms,omitempty"`
	InteractiveElements []InteractiveElement `json:"interactive_elements,omitempty"`
	Images              []ImageElement       `json:"images,omitempty"`
	ConsoleErrors       []string             `json:"console_errors,omitempty"`
	Performance         *PagePerformance     `json:"performance,omitempty"`
	Meta                map[string]string    `json:"meta,omitempty"`
	SPAInfo             *SPAInfo             `json:"spa_info,omitempty"`
	DurationMs          int64                `json:"duration_ms"`
}

// NavElement represents a navigation link
type NavElement struct {
	Text     string `json:"text"`
	Href     string `json:"href"`
	Selector string `json:"selector"`
}

// FormElement represents a form on the page
type FormElement struct {
	Action       string       `json:"action,omitempty"`
	Method       string       `json:"method,omitempty"`
	Selector     string       `json:"selector"`
	Fields       []FormField  `json:"fields,omitempty"`
	SubmitButton *NavElement  `json:"submit_button,omitempty"`
}

// FormField represents a form input field
type FormField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
	Selector    string `json:"selector"`
}

// InteractiveElement represents a button, dropdown, or other interactive element
type InteractiveElement struct {
	Type     string   `json:"type"`
	Text     string   `json:"text"`
	Selector string   `json:"selector"`
	Options  []string `json:"options,omitempty"` // for dropdowns
}

// ImageElement represents an image on the page
type ImageElement struct {
	Src    string `json:"src"`
	Alt    string `json:"alt"`
	Loaded bool   `json:"loaded"`
}

// PagePerformance contains performance metrics
type PagePerformance struct {
	LoadTimeMs  int64 `json:"load_time_ms"`
	LCPMs       int64 `json:"lcp_ms,omitempty"`
	DOMElements int   `json:"dom_elements"`
}

// SPAInfo contains SPA framework detection results
type SPAInfo struct {
	Detected              bool         `json:"detected"`
	Framework             string       `json:"framework"`
	Version               string       `json:"version,omitempty"`
	Router                string       `json:"router,omitempty"`
	HydrationTimeMs       int64        `json:"hydration_time_ms,omitempty"`
	ClientRoutesDiscovered []string    `json:"client_routes_discovered,omitempty"`
	LazyLoadedComponents  int          `json:"lazy_loaded_components,omitempty"`
	APICallsDuringLoad    []APICall    `json:"api_calls_during_load,omitempty"`
}

// APICall represents an API call made during page load
type APICall struct {
	URL    string `json:"url"`
	Method string `json:"method"`
	Status int    `json:"status"`
}

// TestRunResult represents the output from run_tests
type TestRunResult struct {
	RunID      string           `json:"run_id"`
	TargetURL  string           `json:"target_url"`
	ScriptName string           `json:"script_name"`
	Timestamp  string           `json:"timestamp"`
	Summary    TestSummary      `json:"summary"`
	Results    []TestCaseResult `json:"results"`
	Artifacts  *ArtifactSummary `json:"artifacts,omitempty"`
	ScriptSaved *ScriptRef     `json:"script_saved,omitempty"`

	// internal — local artifact directory path (not serialized)
	artifactDir string
}

// TestSummary contains aggregate test results
type TestSummary struct {
	Total      int   `json:"total"`
	Passed     int   `json:"passed"`
	Failed     int   `json:"failed"`
	Skipped    int   `json:"skipped"`
	DurationMs int64 `json:"duration_ms"`
}

// TestCaseResult represents a single test case result
type TestCaseResult struct {
	Test          string `json:"test"`
	Status        string `json:"status"` // passed, failed, skipped
	DurationMs    int64  `json:"duration_ms"`
	Error         string `json:"error,omitempty"`
	ScreenshotURL string `json:"screenshot_url,omitempty"`
	TraceURL      string `json:"trace_url,omitempty"`
}

// ArtifactSummary contains S3 artifact metadata
type ArtifactSummary struct {
	BasePath        string `json:"base_path"`
	ScreenshotCount int    `json:"screenshot_count"`
	TraceCount      int    `json:"trace_count"`
	TotalSizeBytes  int64  `json:"total_size_bytes"`
	URLsExpireAt    string `json:"urls_expire_at,omitempty"`
}

// ScriptRef contains a reference to a saved script
type ScriptRef struct {
	S3Path  string `json:"s3_path"`
	Version int    `json:"version"`
}

// ArtifactInfo represents a single artifact with its pre-signed URL
type ArtifactInfo struct {
	Type      string `json:"type"`       // screenshot, trace, script
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url,omitempty"`
	S3Key     string `json:"s3_key"`
}

// GetArtifactsResponse represents the output from get_artifacts
type GetArtifactsResponse struct {
	RunID        string         `json:"run_id"`
	TargetURL    string         `json:"target_url"`
	Artifacts    []ArtifactInfo `json:"artifacts"`
	URLsExpireAt string         `json:"urls_expire_at"`
	Script       *ScriptArtifact `json:"script,omitempty"`
}

// ScriptArtifact represents a script artifact with URL
type ScriptArtifact struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// NewPlaywrightTool creates a new playwright tool
func NewPlaywrightTool() *PlaywrightTool {
	tool := &PlaywrightTool{
		BaseTool: core.NewTool("playwright-tool"),
		browser:  NewBrowserClient(),
	}

	// Initialize S3 client (optional — tool works without it, just no persistence)
	s3Endpoint := os.Getenv("S3_ENDPOINT")
	s3Bucket := os.Getenv("S3_BUCKET")
	s3AccessKey := os.Getenv("S3_ACCESS_KEY")
	s3SecretKey := os.Getenv("S3_SECRET_KEY")
	s3Region := os.Getenv("S3_REGION")
	if s3Region == "" {
		s3Region = "us-east-1"
	}

	if s3Bucket != "" {
		s3Client, err := NewS3Client(s3Endpoint, s3Bucket, s3AccessKey, s3SecretKey, s3Region)
		if err != nil {
			// Log at startup — tool can still function without S3
			fmt.Fprintf(os.Stderr, "Warning: S3 client initialization failed: %v (artifacts will not be persisted)\n", err)
		} else {
			tool.s3 = s3Client
			tool.s3Ready = true
		}
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all playwright tool capabilities
func (t *PlaywrightTool) registerCapabilities() {

	// Capability 1: Explore Page
	t.RegisterCapability(core.Capability{
		Name: "explore_page",
		Description: "Explores a web page using a real Chromium browser and extracts all testable elements " +
			"(navigation links, forms, buttons, images, interactive components). " +
			"Use before generating test scripts to discover actual selectors and page structure. " +
			"Handles SPAs (React, Vue, Angular, Next.js) with framework detection and hydration waits. " +
			"Returns structured page analysis JSON with selectors, forms, navigation, and SPA info. " +
			"Required: url. Optional: depth, follow_links, viewport, wait_for_spa, spa_timeout_ms.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleExplorePage,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "url", Type: "string", Example: "https://example.com", Description: "Target page URL to explore (must include protocol)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "depth", Type: "integer", Example: "2", Description: "How many levels of links to follow (default 1, max 3)"},
				{Name: "follow_links", Type: "boolean", Example: "true", Description: "Follow discovered same-origin links to explore sub-pages (default false)"},
				{Name: "viewport", Type: "string", Example: "1280x720", Description: "Browser viewport size as WxH (default 1280x720)"},
				{Name: "wait_for_spa", Type: "boolean", Example: "true", Description: "Enable SPA detection and hydration wait (default true)"},
				{Name: "spa_timeout_ms", Type: "integer", Example: "15000", Description: "Max time to wait for SPA hydration in ms (default 15000)"},
			},
		},

		// Phase 2b: Output schema — fields match PageAnalysis JSON tags.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "url", Type: "string", Description: "URL that was explored"},
				{Name: "title", Type: "string", Description: "Page title"},
				{Name: "duration_ms", Type: "number", Description: "Total exploration duration in milliseconds"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "pages_found", Type: "array", Description: "URLs of additional pages discovered when follow_links is enabled"},
				{Name: "navigation", Type: "array", Description: "Navigation links with text, href, and selector"},
				{Name: "forms", Type: "array", Description: "Forms with action, method, selector, fields, and submit_button"},
				{Name: "interactive_elements", Type: "array", Description: "Buttons, dropdowns, and other interactive elements with type, text, selector, and options"},
				{Name: "images", Type: "array", Description: "Images with src, alt, and loaded status"},
				{Name: "console_errors", Type: "array", Description: "JavaScript console errors captured during page load"},
				{Name: "performance", Type: "object", Description: "Page performance metrics: load_time_ms, lcp_ms, dom_elements"},
				{Name: "meta", Type: "object", Description: "Meta tag key/value pairs from the page head"},
				{Name: "spa_info", Type: "object", Description: "SPA framework detection: detected, framework, version, router, hydration_time_ms, client_routes_discovered, lazy_loaded_components, api_calls_during_load"},
			},
		},
	})

	// Capability 2: Run Tests
	t.RegisterCapability(core.Capability{
		Name: "run_tests",
		Description: "Executes Playwright test scripts against a target URL in a real Chromium browser. " +
			"Use after explore_page to run AI-generated or stored test scripts. " +
			"Supports script reuse: provide reuse_script_name to fetch a stored script from S3 instead of inline script. " +
			"Uploads screenshots and traces to S3, saves scripts for regression re-runs, indexes results in Redis. " +
			"Returns test summary with pass/fail counts and pre-signed artifact URLs (24h expiry). " +
			"Required: target_url + either script or reuse_script_name. Optional: script_name, timeout_ms, browser, viewport.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleRunTests,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "target_url", Type: "string",
					Example:     "https://example.com",
					Description: "Website URL being tested (used for S3 path organization and indexing)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "script", Type: "string",
					Example:     "test('page loads', async ({page}) => { await page.goto('https://example.com'); await expect(page).toHaveTitle(/Example/); });",
					Description: "Playwright TypeScript test code to execute. Required if reuse_script_name is not provided."},
				{Name: "reuse_script_name", Type: "string", Example: "homepage-nav",
					Description: "Name of a stored script to fetch from S3 and execute. Use lookup_scripts to discover available scripts. If both script and reuse_script_name are provided, the inline script takes precedence."},
				{Name: "script_name", Type: "string", Example: "login-flow",
					Description: "Name for the test script (used for S3 storage and regression re-runs)"},
				{Name: "timeout_ms", Type: "integer", Example: "60000",
					Description: "Overall test timeout in milliseconds (default 60000, max 300000)"},
				{Name: "browser", Type: "string", Example: "chromium",
					Description: "Browser engine: chromium, firefox, webkit (default chromium)"},
				{Name: "viewport", Type: "object", Example: `{"width": 1280, "height": 720}`,
					Description: "Browser viewport dimensions (default 1280x720)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "run_id", Type: "string", Description: "Unique test run identifier"},
				{Name: "target_url", Type: "string", Description: "The URL that was tested"},
				{Name: "script_name", Type: "string", Description: "Name of the executed script"},
				{Name: "timestamp", Type: "string", Description: "ISO 8601 timestamp of the run"},
				{Name: "summary", Type: "object", Description: "Aggregate results: total, passed, failed, skipped, duration_ms"},
				{Name: "results", Type: "array", Description: "Per-test results: test name, status, duration_ms, error, screenshot_url, trace_url"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "artifacts", Type: "object", Description: "S3 artifact summary: base_path, screenshot_count, trace_count, total_size_bytes, urls_expire_at"},
				{Name: "script_saved", Type: "object", Description: "Script S3 reference: s3_path, version"},
			},
		},
	})

	// Capability 3: Get Results
	t.RegisterCapability(core.Capability{
		Name: "get_results",
		Description: "Queries past test run results from Redis with optional filters by site, status, and date range. " +
			"Use to check previous test outcomes before re-running or to build regression reports. " +
			"Returns run metadata summaries with optional fresh pre-signed artifact URLs. " +
			"Optional: site, status, from_date, to_date, limit, include_urls.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetResults,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "site", Type: "string", Example: "myapp.com", Description: "Filter by site domain"},
				{Name: "status", Type: "string", Example: "failed", Description: "Filter by overall status: passed, failed, mixed"},
				{Name: "from_date", Type: "string", Example: "2026-03-01", Description: "Start date (YYYY-MM-DD)"},
				{Name: "to_date", Type: "string", Example: "2026-03-12", Description: "End date (YYYY-MM-DD)"},
				{Name: "limit", Type: "integer", Example: "20", Description: "Max results to return (default 20)"},
				{Name: "include_urls", Type: "boolean", Example: "true", Description: "Generate fresh pre-signed URLs for artifacts (default false)"},
			},
		},

		// Note: get_results returns an array of RunMetadata at data (not a wrapping
		// object), so OutputSummary is intentionally omitted — SchemaSummary/FieldHint
		// only describe top-level object fields. Per the tool development guide,
		// omitting OutputSummary is backwards-compatible: template references to
		// this capability's output pass through without validation.
	})

	// Capability 4: Get Artifacts
	t.RegisterCapability(core.Capability{
		Name: "get_artifacts",
		Description: "Regenerates time-limited pre-signed S3 URLs for a test run's screenshots and traces. " +
			"Use when previously returned artifact URLs have expired (default 24h expiry). " +
			"Returns fresh download URLs without re-running the tests. " +
			"Required: run_id. Optional: expiry_hours.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetArtifacts,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "run_id", Type: "string", Example: "run-abc123", Description: "Test run ID to get artifacts for"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "expiry_hours", Type: "integer", Example: "24", Description: "URL validity in hours (default 24, max 168)"},
			},
		},

		// Phase 2b: Output schema — fields match GetArtifactsResponse JSON tags.
		// Each entry in `artifacts` is an ArtifactInfo with: type, name, size_bytes, url, s3_key.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "run_id", Type: "string", Description: "Test run ID that was queried"},
				{Name: "target_url", Type: "string", Description: "URL that the original run tested"},
				{Name: "artifacts", Type: "array", Description: "List of screenshot/trace artifacts with type, name, size_bytes, url (pre-signed), and s3_key"},
				{Name: "urls_expire_at", Type: "string", Description: "ISO 8601 timestamp when the pre-signed URLs will expire"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "script", Type: "object", Description: "Stored test script reference with name and pre-signed download url"},
			},
		},
	})

	// Capability 5: Lookup Scripts
	t.RegisterCapability(core.Capability{
		Name: "lookup_scripts",
		Description: "Lists reusable Playwright test scripts stored for a hostname (subdomain). " +
			"Returns script names, test names they cover, version, and last run status. " +
			"Use before run_tests to check if a reusable script already exists for the target site. " +
			"Does not return script content — only metadata for the LLM to judge relevance. " +
			"Required: hostname.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleLookupScripts,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "hostname", Type: "string", Example: "developer.cisco.com",
					Description: "Subdomain to look up scripts for (e.g. developer.cisco.com, not cisco.com)"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "hostname", Type: "string",
					Description: "The hostname that was queried"},
				{Name: "scripts", Type: "array",
					Description: "List of available scripts with metadata: name, version, test_names, test_count, last_run_status, last_run_date"},
			},
		},
	})
}
