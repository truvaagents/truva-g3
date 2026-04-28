package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// BrowserClient manages Playwright browser execution via Node.js scripts.
// It follows the same pattern as system-utilities-tool: Go generates/invokes
// Node.js scripts and parses JSON output from stdout.
type BrowserClient struct {
	workDir   string // /tmp/playwright-runs
	nodePath  string // node binary path
	scriptDir string // /app/scripts (embedded explore.js)
}

// NewBrowserClient creates a new browser client
func NewBrowserClient() *BrowserClient {
	workDir := "/tmp/playwright-runs"
	os.MkdirAll(workDir, 0755)

	scriptDir := os.Getenv("PLAYWRIGHT_SCRIPT_DIR")
	if scriptDir == "" {
		scriptDir = "/app/scripts"
	}

	return &BrowserClient{
		workDir:   workDir,
		nodePath:  "node",
		scriptDir: scriptDir,
	}
}

// ExplorePage runs the explore.js Node.js script against a URL and returns structured page analysis
func (c *BrowserClient) ExplorePage(ctx context.Context, req ExplorePageRequest) (*PageAnalysis, error) {
	// Build arguments for explore.js
	args := []string{
		filepath.Join(c.scriptDir, "explore.js"),
		"--url", req.URL,
	}

	if req.Depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", req.Depth))
	}
	if req.FollowLinks {
		args = append(args, "--follow-links")
	}
	if req.Viewport != "" {
		args = append(args, "--viewport", req.Viewport)
	}

	waitForSPA := true
	if req.WaitForSPA != nil {
		waitForSPA = *req.WaitForSPA
	}
	if !waitForSPA {
		args = append(args, "--no-spa")
	}

	spaTimeout := req.SPATimeoutMs
	if spaTimeout <= 0 {
		spaTimeout = 15000
	}
	args = append(args, "--spa-timeout", fmt.Sprintf("%d", spaTimeout))

	// Execute with timeout buffer (exploration timeout + 30s for Chromium startup/shutdown)
	execTimeout := time.Duration(spaTimeout+30000) * time.Millisecond
	if req.Depth > 1 {
		// More time for deeper crawls
		execTimeout = time.Duration(spaTimeout+60000*int(req.Depth)) * time.Millisecond
	}
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, c.nodePath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	duration := time.Since(startTime)

	if err != nil {
		errMsg := stderr.String()
		if execCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("Page exploration timed out after %dms", execTimeout.Milliseconds())
		}
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("explore_page failed: %s", errMsg)
	}

	var result PageAnalysis
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to parse explore output: %w (stdout length: %d)", err, stdout.Len())
	}

	result.DurationMs = duration.Milliseconds()
	return &result, nil
}

// RunTests writes a Playwright test script to a temp dir, runs it, and returns results
func (c *BrowserClient) RunTests(ctx context.Context, req RunTestsRequest) (*TestRunResult, error) {
	runID := "run-" + uuid.New().String()[:8]
	runDir := filepath.Join(c.workDir, runID)

	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create run directory: %w", err)
	}
	// NOTE: caller is responsible for cleaning up runDir after S3 upload via result.artifactDir

	// Determine script name — strip file extensions if the AI included them
	scriptName := req.ScriptName
	if scriptName == "" {
		scriptName = "test-" + uuid.New().String()[:6]
	}
	scriptName = strings.TrimSuffix(scriptName, ".spec.ts")
	scriptName = strings.TrimSuffix(scriptName, ".ts")

	// Write the test script
	scriptFile := filepath.Join(runDir, scriptName+".spec.ts")
	if err := os.WriteFile(scriptFile, []byte(req.Script), 0644); err != nil {
		return nil, fmt.Errorf("failed to write test script: %w", err)
	}

	// Set timeout
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 60000
	}
	if timeoutMs > 300000 {
		timeoutMs = 300000
	}

	// Set viewport
	viewportWidth := 1280
	viewportHeight := 720
	if req.Viewport != nil {
		if req.Viewport.Width > 0 {
			viewportWidth = req.Viewport.Width
		}
		if req.Viewport.Height > 0 {
			viewportHeight = req.Viewport.Height
		}
	}

	// Write playwright.config.ts
	configContent := buildPlaywrightConfig(req.TargetURL, timeoutMs, viewportWidth, viewportHeight)
	configFile := filepath.Join(runDir, "playwright.config.ts")
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write playwright config: %w", err)
	}

	// Execute Playwright tests
	execTimeout := time.Duration(timeoutMs+30000) * time.Millisecond
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "npx", "playwright", "test",
		"--config", configFile,
		"--reporter", "json",
	)
	cmd.Dir = runDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	// Playwright returns non-zero exit code when tests fail — that's expected
	cmd.Run()
	duration := time.Since(startTime)

	// Parse JSON reporter output from stdout
	result, err := parsePlaywrightJSON(stdout.Bytes(), runID, req.TargetURL, scriptName)
	if err != nil {
		// If JSON parsing fails, create a minimal result with diagnostic info
		errDetail := fmt.Sprintf("Parse error: %v", err)
		if stderrStr := stderr.String(); stderrStr != "" {
			errDetail += fmt.Sprintf(". Stderr: %s", truncate(stderrStr, 500))
		}
		if stdout.Len() > 0 {
			errDetail += fmt.Sprintf(". Stdout preview: %s", truncate(stdout.String(), 300))
		}
		result = &TestRunResult{
			RunID:      runID,
			TargetURL:  req.TargetURL,
			ScriptName: scriptName,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
			Summary: TestSummary{
				Total:      0,
				Failed:     1,
				DurationMs: duration.Milliseconds(),
			},
			Results: []TestCaseResult{
				{
					Test:       "Playwright execution",
					Status:     "failed",
					Error:      errDetail,
					DurationMs: duration.Milliseconds(),
				},
			},
		}
	}

	result.Summary.DurationMs = duration.Milliseconds()

	// Collect local artifact paths for S3 upload
	result.artifactDir = runDir

	return result, nil
}

// buildPlaywrightConfig generates a playwright.config.ts for test execution
func buildPlaywrightConfig(baseURL string, timeoutMs, viewportWidth, viewportHeight int) string {
	return fmt.Sprintf(`import { defineConfig } from '@playwright/test';

export default defineConfig({
  timeout: %d,
  retries: 0,
  use: {
    baseURL: '%s',
    viewport: { width: %d, height: %d },
    screenshot: 'on',
    trace: 'on-first-retry',
    actionTimeout: 10000,
    navigationTimeout: 15000,
  },
  reporter: [['json', { outputFile: 'results.json' }]],
});
`, timeoutMs, escapeJS(baseURL), viewportWidth, viewportHeight)
}

// playwrightSuite is a recursive structure matching Playwright's JSON reporter output.
// Playwright nests suites when test.describe() is used, so we must walk the tree.
type playwrightSuite struct {
	Title  string            `json:"title"`
	Suites []playwrightSuite `json:"suites"` // nested describe() blocks
	Specs  []struct {
		Title string `json:"title"`
		Tests []struct {
			Status  string  `json:"status"` // expected, unexpected, flaky, skipped
			Results []struct {
				Status   string  `json:"status"`
				Duration float64 `json:"duration"` // Playwright emits float ms
				Errors   []struct {
					Message string `json:"message"`
				} `json:"errors"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
				Attachments []struct {
					Name string `json:"name"`
					Path string `json:"path"`
				} `json:"attachments"`
			} `json:"results"`
		} `json:"tests"`
	} `json:"specs"`
}

// parsePlaywrightJSON parses Playwright's JSON reporter output into our result type.
// Handles recursive suite nesting (test.describe blocks) and float duration values.
func parsePlaywrightJSON(data []byte, runID, targetURL, scriptName string) (*TestRunResult, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty playwright output")
	}

	var report struct {
		Suites []playwrightSuite `json:"suites"`
		Stats  struct {
			Duration float64 `json:"duration"` // float in Playwright JSON
		} `json:"stats"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("invalid playwright JSON: %w", err)
	}

	result := &TestRunResult{
		RunID:      runID,
		TargetURL:  targetURL,
		ScriptName: scriptName,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	// Recursively walk all suites to collect test results
	var walkSuites func(suites []playwrightSuite)
	walkSuites = func(suites []playwrightSuite) {
		for _, suite := range suites {
			// Process specs at this level
			for _, spec := range suite.Specs {
				for _, test := range spec.Tests {
					// Get duration from the first result entry
					var durationMs int64
					if len(test.Results) > 0 {
						durationMs = int64(test.Results[0].Duration)
					}

					tc := TestCaseResult{
						Test:       spec.Title,
						DurationMs: durationMs,
					}

					switch test.Status {
					case "expected":
						tc.Status = "passed"
						result.Summary.Passed++
					case "unexpected", "flaky":
						tc.Status = "failed"
						result.Summary.Failed++
						// Extract error message from results (try errors array first, then error field)
						if len(test.Results) > 0 {
							r := test.Results[0]
							if len(r.Errors) > 0 {
								tc.Error = r.Errors[0].Message
							} else if r.Error != nil {
								tc.Error = r.Error.Message
							}
						}
					case "skipped":
						tc.Status = "skipped"
						result.Summary.Skipped++
					default:
						tc.Status = test.Status
					}

					result.Summary.Total++
					result.Results = append(result.Results, tc)
				}
			}

			// Recurse into nested suites (test.describe blocks)
			if len(suite.Suites) > 0 {
				walkSuites(suite.Suites)
			}
		}
	}

	walkSuites(report.Suites)

	// If there are top-level errors (e.g. syntax errors preventing any tests from running)
	if len(report.Errors) > 0 && result.Summary.Total == 0 {
		result.Summary.Total = 1
		result.Summary.Failed = 1
		errMsg := report.Errors[0].Message
		result.Results = append(result.Results, TestCaseResult{
			Test:   "Playwright setup",
			Status: "failed",
			Error:  errMsg,
		})
	}

	return result, nil
}

// escapeJS escapes a string for safe embedding in JavaScript
func escapeJS(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return s
}

// truncate truncates a string to maxLen characters
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
