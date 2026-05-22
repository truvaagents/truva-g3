package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// --- Error helpers (canonical pattern from TOOL_DEVELOPMENT_GUIDE) ---

func (t *PlaywrightTool) sendError(rw http.ResponseWriter, message string, status int, code string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	json.NewEncoder(rw).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}

func (t *PlaywrightTool) sendUpstreamError(rw http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
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

// --- Handler: explore_page ---

func (t *PlaywrightTool) handleExplorePage(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Request ID from baggage, fallback to header, then UUID
	requestID := extractRequestID(ctx, r)

	// 3. Span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "explore_page"),
	)

	// 4. Span event
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("operation", "explore_page"),
	)

	// 5. Log request start
	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing explore_page request", map[string]interface{}{
			"operation":  "explore_page",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	// 6. Decode request
	var req ExplorePageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "explore_page",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// 7. Validate required fields
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("url is required"))
		t.sendError(rw, "url is required", http.StatusBadRequest, "MISSING_URL")
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		telemetry.RecordSpanError(ctx, fmt.Errorf("url must start with http:// or https://"))
		t.sendError(rw, "url must start with http:// or https://", http.StatusBadRequest, "INVALID_URL")
		return
	}

	// 8. Apply defaults
	if req.Depth <= 0 {
		req.Depth = 1
	}
	if req.Depth > 3 {
		req.Depth = 3
	}

	// 9. Set span attributes with request details
	telemetry.SetSpanAttributes(ctx,
		attribute.String("browser.url", req.URL),
		attribute.Int("browser.depth", req.Depth),
		attribute.Bool("browser.follow_links", req.FollowLinks),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Launching page exploration", map[string]interface{}{
			"operation":    "explore_page",
			"url":          req.URL,
			"depth":        req.Depth,
			"follow_links": req.FollowLinks,
			"request_id":   requestID,
		})
	}

	// 10. Execute exploration
	telemetry.AddSpanEvent(ctx, "browser_launching",
		attribute.String("request_id", requestID),
		attribute.String("url", req.URL),
	)

	result, err := t.browser.ExplorePage(ctx, req)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Page exploration failed", map[string]interface{}{
				"operation":   "explore_page",
				"url":         req.URL,
				"error":       err.Error(),
				"error_type":  "browser_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(rw, fmt.Sprintf("Page exploration failed: %v", err),
			core.ClassifyUpstreamError(err))
		return
	}

	// 11. Record success metrics
	duration := time.Since(startTime)
	telemetry.RecordToolCall("playwright-tool", "explore_page", float64(duration.Milliseconds()), "success")
	telemetry.AddSpanEvent(ctx, "explore_completed",
		attribute.String("request_id", requestID),
		attribute.String("url", result.URL),
		attribute.Int("forms_found", len(result.Forms)),
		attribute.Int("links_found", len(result.Navigation)),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "explore_page completed", map[string]interface{}{
			"operation":    "explore_page",
			"url":          result.URL,
			"title":        result.Title,
			"forms":        len(result.Forms),
			"links":        len(result.Navigation),
			"images":       len(result.Images),
			"spa_detected": result.SPAInfo != nil && result.SPAInfo.Detected,
			"request_id":   requestID,
			"status":       "success",
			"duration_ms":  duration.Milliseconds(),
		})
	}

	// 12. Send response
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: result})
}

// --- Handler: run_tests ---

func (t *PlaywrightTool) handleRunTests(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "run_tests"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("operation", "run_tests"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing run_tests request", map[string]interface{}{
			"operation":  "run_tests",
			"request_id": requestID,
		})
	}

	// Decode request
	var req RunTestsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "run_tests",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	// Validate required fields
	req.Script = strings.TrimSpace(req.Script)
	req.ReuseScriptName = strings.TrimSpace(req.ReuseScriptName)
	req.TargetURL = strings.TrimSpace(req.TargetURL)

	if req.TargetURL == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("target_url is required"))
		t.sendError(rw, "target_url is required", http.StatusBadRequest, "MISSING_TARGET_URL")
		return
	}

	// Set span attributes for searchability in Jaeger
	if req.ReuseScriptName != "" {
		telemetry.SetSpanAttributes(ctx,
			attribute.String("test.reuse_script_name", req.ReuseScriptName),
		)
	}

	// Resolve script: inline script takes precedence, then reuse from S3
	if req.Script == "" && req.ReuseScriptName != "" {
		site := extractSite(req.TargetURL)
		resolved, err := t.resolveReusableScript(ctx, site, req.ReuseScriptName)
		if err != nil {
			if t.Logger != nil {
				t.Logger.WarnWithContext(ctx, "Failed to resolve reusable script", map[string]interface{}{
					"operation":         "run_tests",
					"reuse_script_name": req.ReuseScriptName,
					"site":              site,
					"error":             err.Error(),
					"request_id":        requestID,
				})
			}
			telemetry.RecordSpanError(ctx, err)
			t.sendError(rw, fmt.Sprintf("Reusable script '%s' not found for %s: %v", req.ReuseScriptName, site, err),
				http.StatusBadRequest, "SCRIPT_NOT_FOUND")
			return
		}
		req.Script = resolved
		if req.ScriptName == "" {
			req.ScriptName = req.ReuseScriptName
		}

		telemetry.AddSpanEvent(ctx, "script_reused",
			attribute.String("request_id", requestID),
			attribute.String("reuse_script_name", req.ReuseScriptName),
			attribute.String("site", site),
		)

		if t.Logger != nil {
			t.Logger.InfoWithContext(ctx, "Reusing stored script", map[string]interface{}{
				"operation":         "run_tests",
				"reuse_script_name": req.ReuseScriptName,
				"site":              site,
				"script_len":        len(resolved),
				"request_id":        requestID,
			})
		}
	}

	if req.Script == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("either script or reuse_script_name is required"))
		t.sendError(rw, "Either 'script' (inline) or 'reuse_script_name' (fetch from S3) is required",
			http.StatusBadRequest, "MISSING_SCRIPT")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("test.target_url", req.TargetURL),
		attribute.String("test.script_name", req.ScriptName),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Launching test execution", map[string]interface{}{
			"operation":   "run_tests",
			"target_url":  req.TargetURL,
			"script_name": req.ScriptName,
			"request_id":  requestID,
		})
	}

	// Execute tests
	telemetry.AddSpanEvent(ctx, "test_execution_starting",
		attribute.String("request_id", requestID),
		attribute.String("target_url", req.TargetURL),
	)

	result, err := t.browser.RunTests(ctx, req)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Test execution failed", map[string]interface{}{
				"operation":   "run_tests",
				"target_url":  req.TargetURL,
				"error":       err.Error(),
				"error_type":  "browser_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(rw, fmt.Sprintf("Test execution failed: %v", err),
			core.ClassifyUpstreamError(err))
		return
	}

	// Upload artifacts to S3 and index in Redis (best-effort)
	site := extractSite(req.TargetURL)

	if t.s3Ready && result.artifactDir != "" {
		t.uploadAndIndex(ctx, result, req.Script, site, requestID)
	}

	// Update script run status in Redis (for lookup_scripts metadata)
	if t.store != nil && result.ScriptName != "" {
		runStatus := "passed"
		if result.Summary.Failed > 0 && result.Summary.Passed > 0 {
			runStatus = "mixed"
		} else if result.Summary.Failed > 0 {
			runStatus = "failed"
		}
		t.store.UpdateScriptRunStatus(ctx, site, result.ScriptName, runStatus,
			time.Now().UTC().Format("2006-01-02"))
	}

	// Clean up run directory after S3 upload is complete
	if result.artifactDir != "" {
		os.RemoveAll(result.artifactDir)
	}

	// Record success
	duration := time.Since(startTime)
	telemetry.RecordToolCall("playwright-tool", "run_tests", float64(duration.Milliseconds()), "success")
	telemetry.AddSpanEvent(ctx, "run_tests_completed",
		attribute.String("request_id", requestID),
		attribute.String("run_id", result.RunID),
		attribute.Int("total", result.Summary.Total),
		attribute.Int("passed", result.Summary.Passed),
		attribute.Int("failed", result.Summary.Failed),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "run_tests completed", map[string]interface{}{
			"operation":   "run_tests",
			"run_id":      result.RunID,
			"target_url":  result.TargetURL,
			"total":       result.Summary.Total,
			"passed":      result.Summary.Passed,
			"failed":      result.Summary.Failed,
			"request_id":  requestID,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: result})
}

// uploadAndIndex handles S3 upload and Redis indexing for test results
func (t *PlaywrightTool) uploadAndIndex(ctx context.Context, result *TestRunResult, script, site, requestID string) {
	// S3 path: {hostname}/runs/{YYYY}/{MM}/{DD}/{requestID}
	now := time.Now().UTC()
	s3Prefix := fmt.Sprintf("%s/runs/%s/%s",
		site,
		now.Format("2006/01/02"),
		requestID,
	)

	// Upload artifacts (screenshots, traces)
	artifacts, err := t.s3.UploadDirectory(ctx, s3Prefix, result.artifactDir)
	if err != nil {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Failed to upload artifacts to S3", map[string]interface{}{
				"operation":  "run_tests",
				"run_id":     result.RunID,
				"error":      err.Error(),
				"request_id": requestID,
			})
		}
	}

	// Generate pre-signed URLs for artifacts
	expiryHours := 24
	expiry := time.Duration(expiryHours) * time.Hour
	expiresAt := time.Now().Add(expiry).UTC().Format(time.RFC3339)

	var totalSize int64
	var screenshotCount, traceCount int

	for i := range artifacts {
		presignedURL, err := t.s3.GeneratePresignedURL(ctx, artifacts[i].S3Key, expiry)
		if err == nil {
			artifacts[i].URL = presignedURL
		}
		totalSize += artifacts[i].SizeBytes
		switch artifacts[i].Type {
		case "screenshot":
			screenshotCount++
		case "trace":
			traceCount++
		}
	}

	// Update result with artifact URLs
	result.Artifacts = &ArtifactSummary{
		BasePath:        fmt.Sprintf("s3://%s/%s/", t.s3.bucket, s3Prefix),
		ScreenshotCount: screenshotCount,
		TraceCount:      traceCount,
		TotalSizeBytes:  totalSize,
		URLsExpireAt:    expiresAt,
	}

	// Add pre-signed URLs to test case results that have screenshots
	for i, tc := range result.Results {
		if tc.Status == "failed" {
			for _, art := range artifacts {
				if art.Type == "screenshot" && art.URL != "" {
					result.Results[i].ScreenshotURL = art.URL
					break
				}
			}
			for _, art := range artifacts {
				if art.Type == "trace" && art.URL != "" {
					result.Results[i].TraceURL = art.URL
					break
				}
			}
		}
	}

	// Save script snapshot to the run folder (audit trail — the exact script that was executed)
	snapshotKey := s3Prefix + "/script.spec.ts"
	if err := t.s3.UploadContent(ctx, snapshotKey, script, "text/typescript"); err != nil {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Failed to save script snapshot", map[string]interface{}{
				"operation":  "run_tests",
				"run_id":     result.RunID,
				"error":      err.Error(),
				"request_id": requestID,
			})
		}
	}

	// Save script to S3 (reusable copy under {hostname}/scripts/)
	if result.ScriptName != "" {
		scriptKey := fmt.Sprintf("%s/scripts/%s.spec.ts", site, result.ScriptName)
		if err := t.s3.UploadContent(ctx, scriptKey, script, "text/typescript"); err == nil {
			// Get version from Redis
			version := 1
			var existingTestNames []string
			if t.store != nil {
				if existing, err := t.store.GetScriptRef(ctx, site, result.ScriptName); err == nil {
					version = existing.Version + 1
					existingTestNames = existing.TestNames
				}

				// Extract test names from results
				testNames := extractTestNames(result.Results)
				if len(testNames) == 0 {
					testNames = existingTestNames // Preserve existing if no results
				}

				// Determine run status
				runStatus := "passed"
				if result.Summary.Failed > 0 && result.Summary.Passed > 0 {
					runStatus = "mixed"
				} else if result.Summary.Failed > 0 {
					runStatus = "failed"
				}

				t.store.SaveScriptRef(ctx, site, ScriptMetadata{
					Name:          result.ScriptName,
					S3Path:        scriptKey,
					Version:       version,
					TestNames:     testNames,
					TestCount:     len(testNames),
					LastRunStatus: runStatus,
					LastRunDate:   time.Now().UTC().Format("2006-01-02"),
				})
			}
			result.ScriptSaved = &ScriptRef{
				S3Path:  fmt.Sprintf("s3://%s/%s", t.s3.bucket, scriptKey),
				Version: version,
			}
		}
	}

	// Index run in Redis
	if t.store != nil {
		status := "passed"
		if result.Summary.Failed > 0 && result.Summary.Passed > 0 {
			status = "mixed"
		} else if result.Summary.Failed > 0 {
			status = "failed"
		}

		t.store.IndexRun(ctx, RunMetadata{
			RunID:      result.RunID,
			TargetURL:  result.TargetURL,
			Site:       site,
			ScriptName: result.ScriptName,
			Timestamp:  result.Timestamp,
			Summary:    result.Summary,
			Status:     status,
			S3Prefix:   s3Prefix,
		})
	}
}

// --- Handler: get_results ---

func (t *PlaywrightTool) handleGetResults(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "get_results"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("operation", "get_results"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_results request", map[string]interface{}{
			"operation":  "get_results",
			"request_id": requestID,
		})
	}

	var req GetResultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "get_results",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	if t.store == nil {
		t.sendError(rw, "Test result store not available (Redis not configured)", http.StatusServiceUnavailable, "STORE_UNAVAILABLE")
		return
	}

	telemetry.AddSpanEvent(ctx, "querying_redis",
		attribute.String("request_id", requestID),
		attribute.String("site_filter", req.Site),
		attribute.String("status_filter", req.Status),
	)

	results, err := t.store.QueryRuns(ctx, RunFilter{
		Site:     req.Site,
		Status:   req.Status,
		FromDate: req.FromDate,
		ToDate:   req.ToDate,
		Limit:    req.Limit,
	})
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to query results", map[string]interface{}{
				"operation":   "get_results",
				"error":       err.Error(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(rw, fmt.Sprintf("Failed to query results: %v", err),
			core.ClassifyUpstreamError(err))
		return
	}

	// Optionally generate fresh pre-signed URLs
	if req.IncludeURLs && t.s3Ready {
		for i, run := range results {
			artifacts, err := t.s3.ListObjects(ctx, run.S3Prefix)
			if err == nil {
				for j := range artifacts {
					presignedURL, err := t.s3.GeneratePresignedURL(ctx, artifacts[j].S3Key, 24*time.Hour)
					if err == nil {
						artifacts[j].URL = presignedURL
					}
				}
				_ = artifacts
			}
			_ = i
		}
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("playwright-tool", "get_results", float64(duration.Milliseconds()), "success")
	telemetry.AddSpanEvent(ctx, "get_results_completed",
		attribute.String("request_id", requestID),
		attribute.Int("result_count", len(results)),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_results completed", map[string]interface{}{
			"operation":    "get_results",
			"result_count": len(results),
			"request_id":   requestID,
			"status":       "success",
			"duration_ms":  duration.Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: results})
}

// --- Handler: get_artifacts ---

func (t *PlaywrightTool) handleGetArtifacts(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "get_artifacts"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("operation", "get_artifacts"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_artifacts request", map[string]interface{}{
			"operation":  "get_artifacts",
			"request_id": requestID,
		})
	}

	var req GetArtifactsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "get_artifacts",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.RunID = strings.TrimSpace(req.RunID)
	if req.RunID == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("run_id is required"))
		t.sendError(rw, "run_id is required", http.StatusBadRequest, "MISSING_RUN_ID")
		return
	}

	if !t.s3Ready {
		t.sendError(rw, "S3 storage not available", http.StatusServiceUnavailable, "S3_UNAVAILABLE")
		return
	}

	// Set expiry
	expiryHours := req.ExpiryHours
	if expiryHours <= 0 {
		expiryHours = 24
	}
	if expiryHours > 168 {
		expiryHours = 168
	}
	expiry := time.Duration(expiryHours) * time.Hour

	// Look up run metadata from Redis to get the correct S3 prefix and target URL
	var s3Prefix string
	var targetURL string
	if t.store != nil {
		if meta, err := t.store.GetRunMetadata(ctx, req.RunID); err == nil {
			s3Prefix = meta.S3Prefix
			targetURL = meta.TargetURL
		}
	}
	// Fallback to legacy prefix format if not found in Redis
	if s3Prefix == "" {
		s3Prefix = fmt.Sprintf("runs/%s", req.RunID)
	}

	telemetry.AddSpanEvent(ctx, "listing_s3_artifacts",
		attribute.String("request_id", requestID),
		attribute.String("s3_prefix", s3Prefix),
	)

	artifacts, err := t.s3.ListObjects(ctx, s3Prefix)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to list artifacts from S3", map[string]interface{}{
				"operation":   "get_artifacts",
				"run_id":      req.RunID,
				"error":       err.Error(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(rw, fmt.Sprintf("Failed to list artifacts: %v", err),
			core.ClassifyUpstreamError(err))
		return
	}

	// Generate pre-signed URLs
	for i := range artifacts {
		presignedURL, err := t.s3.GeneratePresignedURL(ctx, artifacts[i].S3Key, expiry)
		if err == nil {
			artifacts[i].URL = presignedURL
		}
	}

	response := GetArtifactsResponse{
		RunID:        req.RunID,
		TargetURL:    targetURL,
		Artifacts:    artifacts,
		URLsExpireAt: time.Now().Add(expiry).UTC().Format(time.RFC3339),
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("playwright-tool", "get_artifacts", float64(duration.Milliseconds()), "success")
	telemetry.AddSpanEvent(ctx, "get_artifacts_completed",
		attribute.String("request_id", requestID),
		attribute.String("run_id", req.RunID),
		attribute.Int("artifact_count", len(artifacts)),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_artifacts completed", map[string]interface{}{
			"operation":      "get_artifacts",
			"run_id":         req.RunID,
			"artifact_count": len(artifacts),
			"expiry_hours":   expiryHours,
			"request_id":     requestID,
			"status":         "success",
			"duration_ms":    duration.Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: response})
}

// --- Handler: lookup_scripts ---

func (t *PlaywrightTool) handleLookupScripts(rw http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		rw.Header().Set("X-Trace-ID", tc.TraceID)
		rw.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "lookup_scripts"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("operation", "lookup_scripts"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing lookup_scripts request", map[string]interface{}{
			"operation":  "lookup_scripts",
			"request_id": requestID,
		})
	}

	var req LookupScriptsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.sendError(rw, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.Hostname = strings.TrimSpace(req.Hostname)
	if req.Hostname == "" {
		telemetry.RecordSpanError(ctx, fmt.Errorf("hostname is required"))
		t.sendError(rw, "hostname is required", http.StatusBadRequest, "MISSING_HOSTNAME")
		return
	}

	if t.store == nil {
		t.sendError(rw, "Script store not available (Redis not configured)", http.StatusServiceUnavailable, "STORE_UNAVAILABLE")
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("lookup.hostname", req.Hostname),
	)

	scripts, err := t.store.ListScripts(ctx, req.Hostname)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to list scripts", map[string]interface{}{
				"operation":  "lookup_scripts",
				"hostname":   req.Hostname,
				"error":      err.Error(),
				"request_id": requestID,
			})
		}
		t.sendUpstreamError(rw, fmt.Sprintf("Failed to list scripts: %v", err),
			core.ClassifyUpstreamError(err))
		return
	}

	if scripts == nil {
		scripts = []ScriptMetadata{} // Return empty array, not null
	}

	response := LookupScriptsResponse{
		Hostname: req.Hostname,
		Scripts:  scripts,
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("playwright-tool", "lookup_scripts", float64(duration.Milliseconds()), "success")
	telemetry.AddSpanEvent(ctx, "lookup_scripts_completed",
		attribute.String("request_id", requestID),
		attribute.String("hostname", req.Hostname),
		attribute.Int("script_count", len(scripts)),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "lookup_scripts completed", map[string]interface{}{
			"operation":    "lookup_scripts",
			"hostname":     req.Hostname,
			"script_count": len(scripts),
			"request_id":   requestID,
			"status":       "success",
			"duration_ms":  duration.Milliseconds(),
		})
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(core.ToolResponse{Success: true, Data: response})
}

// --- Handler: stealth_browser ---

func (t *PlaywrightTool) handleStealthBrowser(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "stealth_browser"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "stealth_browser"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing stealth_browser request", map[string]interface{}{
			"operation":  "stealth_browser",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req StealthBrowserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "stealth_browser",
			"error_type", "decode_error",
		)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "stealth_browser",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		err := fmt.Errorf("url is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "stealth_browser",
			"error_type", "validation_error",
		)
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Empty url in request", map[string]interface{}{
				"operation":   "stealth_browser",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "url is required", http.StatusBadRequest, "MISSING_URL")
		return
	}

	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		t.sendError(w, "url must start with http:// or https://", http.StatusBadRequest, "INVALID_URL")
		return
	}

	if req.ExtractContent == "" {
		req.ExtractContent = "text"
	}
	extractContent := strings.ToLower(req.ExtractContent)
	if extractContent != "text" && extractContent != "html" && extractContent != "both" {
		extractContent = "text"
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 120 {
		timeout = 120
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("browser.url", req.URL),
		attribute.Int("browser.timeout", timeout),
		attribute.Bool("browser.screenshot", req.Screenshot),
		attribute.String("browser.extract", extractContent),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Launching stealth browser", map[string]interface{}{
			"operation":       "stealth_browser",
			"url":             req.URL,
			"timeout":         timeout,
			"screenshot":      req.Screenshot,
			"extract_content": extractContent,
			"request_id":      requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "browser_launching",
		attribute.String("request_id", requestID),
		attribute.String("url", req.URL),
		attribute.Int("timeout_seconds", timeout),
	)

	script := buildPlaywrightScript(req.URL, req.WaitFor, extractContent, req.Screenshot, timeout, req.JavaScript, req.UserAgent)

	execTimeout := time.Duration(timeout+15) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "node", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "stealth_browser",
			"error_type", "browser_error",
		)

		errMsg := stderr.String()
		if execCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("Browser timed out after %d seconds", timeout)
		}
		if errMsg == "" {
			errMsg = err.Error()
		}

		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Stealth browser execution failed", map[string]interface{}{
				"operation":       "stealth_browser",
				"url":             req.URL,
				"error":           errMsg,
				"error_type":      "browser_error",
				"cmd_duration_ms": cmdDuration.Milliseconds(),
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}

		t.sendError(w, fmt.Sprintf("Browser execution failed: %s", errMsg), http.StatusInternalServerError, "BROWSER_ERROR")
		return
	}

	var result StealthBrowserResponse
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "stealth_browser",
			"error_type", "parse_error",
		)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to parse browser output", map[string]interface{}{
				"operation":   "stealth_browser",
				"error":       err.Error(),
				"error_type":  "parse_error",
				"stdout_len":  stdout.Len(),
				"stderr":      stderr.String(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "Failed to parse browser output", http.StatusInternalServerError, "PARSE_ERROR")
		return
	}

	result.DurationMs = cmdDuration.Milliseconds()

	duration := time.Since(startTime)
	telemetry.Histogram("playwright_tool.browser.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"capability", "stealth_browser",
	)
	telemetry.Counter("playwright_tool.requests.total",
		"capability", "stealth_browser",
		"status", "success",
	)
	telemetry.RecordToolCall("playwright-tool", "stealth_browser", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "stealth_browser_completed",
		attribute.String("request_id", requestID),
		attribute.String("url", result.URL),
		attribute.String("title", result.Title),
		attribute.Int("status_code", result.StatusCode),
		attribute.Int64("cmd_duration_ms", cmdDuration.Milliseconds()),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "stealth_browser completed", map[string]interface{}{
			"operation":       "stealth_browser",
			"url":             result.URL,
			"title":           result.Title,
			"status_code":     result.StatusCode,
			"has_screenshot":  result.ScreenshotBase64 != "",
			"has_js_result":   result.JSResult != "",
			"cmd_duration_ms": cmdDuration.Milliseconds(),
			"status":          "success",
			"duration_ms":     duration.Milliseconds(),
			"request_id":      requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// --- Handler: browser_test ---

func (t *PlaywrightTool) handleBrowserTest(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	requestID := extractRequestID(ctx, r)

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "playwright-tool"),
		attribute.String("truvag3.capability", "browser_test"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "browser_test"),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing browser_test request", map[string]interface{}{
			"operation":  "browser_test",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req BrowserTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "decode_error",
		)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "browser_test",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, "INVALID_REQUEST")
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		err := fmt.Errorf("url is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Empty url in request", map[string]interface{}{
				"operation":   "browser_test",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "url is required", http.StatusBadRequest, "MISSING_URL")
		return
	}

	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		err := fmt.Errorf("url must start with http:// or https://")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		t.sendError(w, "url must start with http:// or https://", http.StatusBadRequest, "INVALID_URL")
		return
	}

	if len(req.Actions) == 0 {
		err := fmt.Errorf("actions array is required and must not be empty")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Empty actions array", map[string]interface{}{
				"operation":   "browser_test",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "actions array is required and must not be empty", http.StatusBadRequest, "MISSING_ACTIONS")
		return
	}

	const maxActions = 200
	if len(req.Actions) > maxActions {
		err := fmt.Errorf("actions array exceeds maximum of %d steps (got %d)", maxActions, len(req.Actions))
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Actions array too large", map[string]interface{}{
				"operation":    "browser_test",
				"error_type":   "validation_error",
				"action_count": len(req.Actions),
				"max_actions":  maxActions,
				"request_id":   requestID,
				"status":       "failure",
				"duration_ms":  time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, fmt.Sprintf("actions array exceeds maximum of %d steps", maxActions), http.StatusBadRequest, "TOO_MANY_ACTIONS")
		return
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 300 {
		timeout = 300
	}

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

	telemetry.SetSpanAttributes(ctx,
		attribute.String("browser.url", req.URL),
		attribute.Int("browser.timeout", timeout),
		attribute.Int("browser.action_count", len(req.Actions)),
		attribute.String("browser.viewport", fmt.Sprintf("%dx%d", viewportWidth, viewportHeight)),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Launching browser test", map[string]interface{}{
			"operation":    "browser_test",
			"url":          req.URL,
			"timeout":      timeout,
			"action_count": len(req.Actions),
			"viewport":     fmt.Sprintf("%dx%d", viewportWidth, viewportHeight),
			"request_id":   requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "browser_test_launching",
		attribute.String("request_id", requestID),
		attribute.String("url", req.URL),
		attribute.Int("action_count", len(req.Actions)),
		attribute.Int("timeout_seconds", timeout),
	)

	script := buildPlaywrightTestScript(req.URL, req.Actions, timeout, viewportWidth, viewportHeight)

	execTimeout := time.Duration(timeout+15) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "node", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "browser_error",
		)

		errMsg := stderr.String()
		if execCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("Browser test timed out after %d seconds", timeout)
		}
		if errMsg == "" {
			errMsg = err.Error()
		}

		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Browser test execution failed", map[string]interface{}{
				"operation":       "browser_test",
				"url":             req.URL,
				"error":           errMsg,
				"error_type":      "browser_error",
				"cmd_duration_ms": cmdDuration.Milliseconds(),
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}

		t.sendError(w, fmt.Sprintf("Browser test execution failed: %s", errMsg), http.StatusInternalServerError, "BROWSER_ERROR")
		return
	}

	var result BrowserTestResponse
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("playwright_tool.errors.total",
			"module", "playwright-tool",
			"capability", "browser_test",
			"error_type", "parse_error",
		)
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to parse browser test output", map[string]interface{}{
				"operation":   "browser_test",
				"error":       err.Error(),
				"error_type":  "parse_error",
				"stdout_len":  stdout.Len(),
				"stderr":      stderr.String(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendError(w, "Failed to parse browser test output", http.StatusInternalServerError, "PARSE_ERROR")
		return
	}

	result.DurationMs = cmdDuration.Milliseconds()

	duration := time.Since(startTime)
	telemetry.Histogram("playwright_tool.browser.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"capability", "browser_test",
	)
	telemetry.Counter("playwright_tool.requests.total",
		"capability", "browser_test",
		"status", "success",
	)
	telemetry.RecordToolCall("playwright-tool", "browser_test", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "browser_test_completed",
		attribute.String("request_id", requestID),
		attribute.String("url", result.URL),
		attribute.Bool("passed", result.Passed),
		attribute.Int("total_steps", result.TotalSteps),
		attribute.Int("passed_steps", result.PassedSteps),
		attribute.Int("failed_steps", result.FailedSteps),
		attribute.Int64("cmd_duration_ms", cmdDuration.Milliseconds()),
	)

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "browser_test completed", map[string]interface{}{
			"operation":       "browser_test",
			"url":             result.URL,
			"passed":          result.Passed,
			"total_steps":     result.TotalSteps,
			"passed_steps":    result.PassedSteps,
			"failed_steps":    result.FailedSteps,
			"cmd_duration_ms": cmdDuration.Milliseconds(),
			"status":          "success",
			"duration_ms":     duration.Milliseconds(),
			"request_id":      requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// --- Helpers ---

// resolveReusableScript fetches a stored script from S3 by name
func (t *PlaywrightTool) resolveReusableScript(ctx context.Context, site, scriptName string) (string, error) {
	if !t.s3Ready {
		return "", fmt.Errorf("S3 not available")
	}

	if t.store == nil {
		return "", fmt.Errorf("script store not available")
	}

	meta, err := t.store.GetScriptRef(ctx, site, scriptName)
	if err != nil {
		return "", err
	}

	content, err := t.s3.GetContent(ctx, meta.S3Path)
	if err != nil {
		return "", fmt.Errorf("failed to fetch script from S3: %w", err)
	}

	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("script is empty in S3: %s", meta.S3Path)
	}

	return content, nil
}

// extractTestNames extracts test names from test case results
func extractTestNames(results []TestCaseResult) []string {
	var names []string
	for _, r := range results {
		if r.Test != "" {
			names = append(names, r.Test)
		}
	}
	return names
}

// extractRequestID extracts request ID from baggage, header fallback, or generates UUID
func extractRequestID(ctx context.Context, r *http.Request) string {
	baggage := telemetry.GetBaggage(ctx)
	if id := baggage["request_id"]; id != "" {
		return id
	}
	if id := r.Header.Get("X-TruvaG3-Request-ID"); id != "" {
		return id
	}
	return uuid.New().String()
}

// extractSite extracts the domain from a URL
func extractSite(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Hostname()
}

// buildPlaywrightScript generates a Node.js script that uses playwright-extra
// with the stealth plugin to navigate a URL and extract content.
func buildPlaywrightScript(url, waitFor, extractContent string, screenshot bool, timeout int, javascript, userAgent string) string {
	timeoutMs := timeout * 1000

	escapeJS := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		s = strings.ReplaceAll(s, "\n", `\n`)
		s = strings.ReplaceAll(s, "\r", `\r`)
		return s
	}

	var waitForBlock string
	if waitFor != "" {
		waitForBlock = fmt.Sprintf(`
    await page.waitForSelector('%s', { timeout: %d });`, escapeJS(waitFor), timeoutMs)
	}

	var contextOptions string
	if userAgent != "" {
		contextOptions = fmt.Sprintf(`{ userAgent: '%s' }`, escapeJS(userAgent))
	} else {
		contextOptions = `{}`
	}

	var extractBlock string
	switch extractContent {
	case "html":
		extractBlock = `
    result.html_content = await page.content();`
	case "both":
		extractBlock = `
    result.text_content = await page.evaluate(() => document.body.innerText);
    result.html_content = await page.content();`
	default: // "text"
		extractBlock = `
    result.text_content = await page.evaluate(() => document.body.innerText);`
	}

	var screenshotBlock string
	if screenshot {
		screenshotBlock = `
    const screenshotBuf = await page.screenshot({ fullPage: true });
    result.screenshot_base64 = screenshotBuf.toString('base64');`
	}

	var jsBlock string
	if javascript != "" {
		jsBlock = fmt.Sprintf(`
    try {
      const jsResult = await page.evaluate(async () => { %s });
      result.js_result = String(jsResult);
    } catch (jsErr) {
      result.js_result = 'JS_ERROR: ' + jsErr.message;
    }`, javascript)
	}

	script := fmt.Sprintf(`
const { chromium } = require('playwright-extra');
const stealth = require('puppeteer-extra-plugin-stealth');
chromium.use(stealth());

(async () => {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage']
    });
    const context = await browser.newContext(%s);
    const page = await context.newPage();
    const response = await page.goto('%s', {
      waitUntil: 'domcontentloaded',
      timeout: %d
    });
    %s
    const result = {
      url: page.url(),
      title: await page.title(),
      status_code: response ? response.status() : 0
    };
    %s%s%s
    console.log(JSON.stringify(result));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  } finally {
    if (browser) await browser.close();
  }
})();
`, contextOptions, escapeJS(url), timeoutMs, waitForBlock, extractBlock, screenshotBlock, jsBlock)

	return script
}

// buildPlaywrightTestScript generates a Node.js script that uses playwright-extra
// with the stealth plugin to execute an ordered sequence of browser test actions.
// Each action maps 1:1 to a Playwright API call and produces a per-step result.
func buildPlaywrightTestScript(startURL string, actions []BrowserAction, timeoutSec, viewportWidth, viewportHeight int) string {
	overallTimeoutMs := timeoutSec * 1000

	escapeJS := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		s = strings.ReplaceAll(s, "\n", `\n`)
		s = strings.ReplaceAll(s, "\r", `\r`)
		return s
	}

	var actionBlocks strings.Builder
	for i, action := range actions {
		stepTimeout := action.Timeout
		if stepTimeout <= 0 {
			stepTimeout = 10000
		}

		selector := escapeJS(action.Selector)
		value := escapeJS(action.Value)
		expected := escapeJS(action.Expected)

		var jsCode string

		switch action.Action {
		case "click":
			jsCode = fmt.Sprintf(`await page.click('%s', { timeout: %d });`, selector, stepTimeout)
		case "fill":
			jsCode = fmt.Sprintf(`await page.fill('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "select":
			jsCode = fmt.Sprintf(`await page.selectOption('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "check":
			jsCode = fmt.Sprintf(`await page.check('%s', { timeout: %d });`, selector, stepTimeout)
		case "uncheck":
			jsCode = fmt.Sprintf(`await page.uncheck('%s', { timeout: %d });`, selector, stepTimeout)
		case "hover":
			jsCode = fmt.Sprintf(`await page.hover('%s', { timeout: %d });`, selector, stepTimeout)
		case "press":
			jsCode = fmt.Sprintf(`await page.press('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "navigate":
			jsCode = fmt.Sprintf(`await page.goto('%s', { waitUntil: 'domcontentloaded', timeout: %d });`, value, stepTimeout)
		case "wait_for_selector":
			jsCode = fmt.Sprintf(`await page.waitForSelector('%s', { state: 'visible', timeout: %d });`, selector, stepTimeout)
		case "wait_for_url":
			jsCode = fmt.Sprintf(`await page.waitForURL('%s', { timeout: %d });`, value, stepTimeout)
		case "wait_for_network_idle":
			jsCode = `await page.waitForLoadState('networkidle');`
		case "screenshot":
			jsCode = fmt.Sprintf(`screenshots['%d'] = (await page.screenshot({ fullPage: true })).toString('base64');`, i)
		case "assert":
			jsCode = buildAssertionJS(action.Assertion, selector, expected, stepTimeout)
		default:
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: unknown action '%s'
  results.push({ step: %d, action: '%s', selector: '%s', passed: false, error: 'Unknown action type: %s', duration_ms: 0 });
`, i, escapeJS(action.Action), i, escapeJS(action.Action), selector, escapeJS(action.Action)))
			continue
		}

		if action.Action == "assert" {
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: assert %s
  { const start_%d = Date.now();
    try {
      let passed = false;
      %s
      results.push({ step: %d, action: 'assert', selector: '%s', passed: passed, duration_ms: Date.now() - start_%d });
    } catch(e) {
      results.push({ step: %d, action: 'assert', selector: '%s', passed: false, error: e.message, duration_ms: Date.now() - start_%d });
    }
  }
`, i, escapeJS(action.Assertion), i, jsCode, i, selector, i, i, selector, i))
		} else {
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: %s
  { const start_%d = Date.now();
    try {
      %s
      results.push({ step: %d, action: '%s', selector: '%s', passed: true, duration_ms: Date.now() - start_%d });
    } catch(e) {
      results.push({ step: %d, action: '%s', selector: '%s', passed: false, error: e.message, duration_ms: Date.now() - start_%d });
    }
  }
`, i, escapeJS(action.Action), i, jsCode, i, escapeJS(action.Action), selector, i, i, escapeJS(action.Action), selector, i))
		}
	}

	script := fmt.Sprintf(`
const { chromium } = require('playwright-extra');
const stealth = require('puppeteer-extra-plugin-stealth');
chromium.use(stealth());

(async () => {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage']
    });
    const context = await browser.newContext({ viewport: { width: %d, height: %d } });
    const page = await context.newPage();
    const results = [];
    const screenshots = {};
    const consoleLog = [];

    page.on('console', msg => consoleLog.push('[' + msg.type() + '] ' + msg.text()));

    await page.goto('%s', { waitUntil: 'domcontentloaded', timeout: %d });
%s
    const passed = results.every(r => r.passed);
    console.log(JSON.stringify({
      url: page.url(),
      passed: passed,
      total_steps: results.length,
      passed_steps: results.filter(r => r.passed).length,
      failed_steps: results.filter(r => !r.passed).length,
      steps: results,
      screenshots: screenshots,
      console_log: consoleLog
    }));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  } finally {
    if (browser) await browser.close();
  }
})();
`, viewportWidth, viewportHeight, escapeJS(startURL), overallTimeoutMs, actionBlocks.String())

	return script
}

// buildAssertionJS generates the JavaScript code for an assertion action.
// It sets a 'passed' boolean variable that the caller wraps in try/catch.
func buildAssertionJS(assertion, selector, expected string, timeoutMs int) string {
	switch assertion {
	case "visible":
		return fmt.Sprintf(`await page.locator('%s').waitFor({ state: 'visible', timeout: %d }); passed = true;`, selector, timeoutMs)
	case "hidden":
		return fmt.Sprintf(`await page.locator('%s').waitFor({ state: 'hidden', timeout: %d }); passed = true;`, selector, timeoutMs)
	case "text_contains":
		return fmt.Sprintf(`const txt = await page.locator('%s').textContent({ timeout: %d }); passed = txt !== null && txt.includes('%s');`, selector, timeoutMs, expected)
	case "text_equals":
		return fmt.Sprintf(`const txt = await page.locator('%s').textContent({ timeout: %d }); passed = txt !== null && txt.trim() === '%s';`, selector, timeoutMs, expected)
	case "url_contains":
		return fmt.Sprintf(`passed = page.url().includes('%s');`, expected)
	case "url_equals":
		return fmt.Sprintf(`passed = page.url() === '%s';`, expected)
	case "count_equals":
		return fmt.Sprintf(`passed = (await page.locator('%s').count()) === parseInt('%s');`, selector, expected)
	case "has_attribute":
		// Expected format: "attr=value"
		return fmt.Sprintf(`{
      const parts = '%s'.split('=');
      const attrName = parts[0];
      const attrVal = parts.slice(1).join('=');
      const actual = await page.locator('%s').getAttribute(attrName, { timeout: %d });
      passed = actual === attrVal;
    }`, expected, selector, timeoutMs)
	case "has_class":
		return fmt.Sprintf(`{
      const cls = await page.locator('%s').getAttribute('class', { timeout: %d });
      passed = cls !== null && cls.includes('%s');
    }`, selector, timeoutMs, expected)
	default:
		return fmt.Sprintf(`passed = false; /* unknown assertion type: %s */`, assertion)
	}
}
