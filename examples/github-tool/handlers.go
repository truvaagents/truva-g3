package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// allowedReviewEvents enforces the MVP rule that the tool only emits COMMENT
// or REQUEST_CHANGES, regardless of what the agent sends.
var allowedReviewEvents = map[string]struct{}{
	"COMMENT":         {},
	"REQUEST_CHANGES": {},
}

// --- Error helpers -----------------------------------------------------------

func (t *GitHubTool) sendError(w http.ResponseWriter, message string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status >= 500,
		},
	})
}

func (t *GitHubTool) sendUpstreamError(w http.ResponseWriter, message string, err error) {
	info := core.ClassifyUpstreamError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(info.HTTPStatus)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message + ": " + err.Error(),
			Retryable: info.Retryable,
			Category:  info.Category,
		},
	})
}

func (t *GitHubTool) sendOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(core.ToolResponse{Success: true, Data: data})
}

// requestID returns the cross-component correlation ID. Resolution order:
//  1. W3C baggage member "request_id" (preferred — survives gRPC + HTTP hops).
//  2. X-TruvaG3-Request-ID header (legacy / direct callers).
//  3. OTel trace ID (last-ditch correlation; always available when traced).
func requestID(r *http.Request) string {
	if bag := telemetry.GetBaggage(r.Context()); bag != nil {
		if id := bag["request_id"]; id != "" {
			return id
		}
	}
	if id := r.Header.Get("X-TruvaG3-Request-ID"); id != "" {
		return id
	}
	return telemetry.GetTraceContext(r.Context()).TraceID
}

// markReceived emits the request_received span event AND sets baseline span
// attributes (truvag3.tool.name + truvag3.capability + request_id) so every
// capability span is searchable in Jaeger by tool/capability/request_id without
// per-handler boilerplate.
func markReceived(ctx context.Context, capability, reqID string) {
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.tool.name", "github-tool"),
		attribute.String("truvag3.capability", capability),
		attribute.String("request_id", reqID),
	)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", reqID),
		attribute.String("truvag3.capability", capability),
	)
}

// recordValidationError consolidates the three things that must happen on
// every validation/decode failure: span error, structured log, HTTP response.
// Returns the same error so the caller can early-return in one line.
func (t *GitHubTool) recordValidationError(ctx context.Context, w http.ResponseWriter,
	operation, errType, message, code string, status int, reqID string, err error) {
	telemetry.RecordSpanError(ctx, err)
	t.Logger.WarnWithContext(ctx, operation+": "+errType, map[string]interface{}{
		"operation":  operation,
		"error_type": errType,
		"error":      err.Error(),
		"request_id": reqID,
	})
	t.sendError(w, message, status, code)
}

// --- get_pr_bundle -----------------------------------------------------------

func (t *GitHubTool) handleGetPRBundle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "get_pr_bundle"
	markReceived(ctx, op, reqID)

	var req GetPRBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if err := validatePRRef(req.Owner, req.Repo, req.PullNumber); err != nil {
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.owner", req.Owner),
		attribute.String("github.repo", req.Repo),
		attribute.Int("github.pull_number", req.PullNumber),
	)
	t.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":   op,
		"owner":       req.Owner,
		"repo":        req.Repo,
		"pull_number": req.PullNumber,
		"request_id":  reqID,
	})

	telemetry.AddSpanEvent(ctx, "github_api_call_started",
		attribute.String("github.endpoint", "GET /repos/.../pulls/{n}"))
	ghStart := time.Now()
	pr, err := t.Client.GetPullRequest(ctx, req.Owner, req.Repo, req.PullNumber)
	t.recordGitHubLatency(ghStart)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.ErrorWithContext(ctx, op+": fetch_pr_failed", map[string]interface{}{
			"operation":  op,
			"error_type": "upstream_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		t.sendUpstreamError(w, "failed to fetch pull request", err)
		t.recordOutcome(op, "upstream_error", start)
		return
	}

	telemetry.AddSpanEvent(ctx, "github_api_call_started",
		attribute.String("github.endpoint", "GET /repos/.../pulls/{n}/files"))
	ghStart = time.Now()
	files, err := t.Client.ListPullRequestFiles(ctx, req.Owner, req.Repo, req.PullNumber)
	t.recordGitHubLatency(ghStart)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.ErrorWithContext(ctx, op+": list_files_failed", map[string]interface{}{
			"operation":  op,
			"error_type": "upstream_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		t.sendUpstreamError(w, "failed to fetch pull request files", err)
		t.recordOutcome(op, "upstream_error", start)
		return
	}

	bundleID := NewBundleID(req.Owner, req.Repo, req.PullNumber, pr.Head.SHA)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.head_sha", pr.Head.SHA),
		attribute.String("truvag3.bundle_id", bundleID),
		attribute.Int("github.changed_files", len(files)),
	)
	manifest := PRBundleManifest{
		BundleID:     bundleID,
		Owner:        req.Owner,
		Repo:         req.Repo,
		PullNumber:   req.PullNumber,
		BaseSHA:      pr.Base.SHA,
		HeadSHA:      pr.Head.SHA,
		Title:        pr.Title,
		Author:       pr.User.Login,
		ChangedFiles: make([]ChangedFileEntry, 0, len(files)),
	}

	for _, f := range files {
		entry := ChangedFileEntry{
			Path:        f.Filename,
			Status:      f.Status,
			Additions:   f.Additions,
			Deletions:   f.Deletions,
			IsGenerated: looksGenerated(f.Filename, f.Patch),
			IsLockfile:  looksLockfile(f.Filename),
			RiskHints:   riskHintsFor(f.Filename),
		}
		if f.Patch != "" {
			patchBytes := []byte(f.Patch)
			if t.Config.MaxPatchBytes > 0 && int64(len(patchBytes)) > t.Config.MaxPatchBytes {
				t.Logger.WarnWithContext(ctx, op+": patch over MaxPatchBytes; storing without patch artifact", map[string]interface{}{
					"operation":  op,
					"error_type": "patch_too_large",
					"path":       f.Filename,
					"bytes":      len(patchBytes),
					"limit":      t.Config.MaxPatchBytes,
					"request_id": reqID,
				})
			} else {
				ref, err := t.Artifacts.Put(ctx, bundleID, f.Filename+".patch", patchBytes)
				if err != nil {
					telemetry.RecordSpanError(ctx, err)
					t.Logger.ErrorWithContext(ctx, op+": store_patch_failed", map[string]interface{}{
						"operation":  op,
						"error_type": "artifact_store_error",
						"error":      err.Error(),
						"path":       f.Filename,
						"request_id": reqID,
					})
					t.sendError(w, "failed to store patch artifact: "+err.Error(),
						http.StatusServiceUnavailable, "ARTIFACT_STORE_ERROR")
					t.recordOutcome(op, "artifact_error", start)
					return
				}
				entry.PatchArtifactID = ref.ID
				telemetry.AddSpanEvent(ctx, "artifact_stored",
					attribute.String("truvag3.artifact_id", ref.ID),
					attribute.Int64("truvag3.artifact_bytes", ref.SizeBytes))
				telemetry.Histogram("github_tool.artifact_bytes", float64(ref.SizeBytes))
			}
		}
		// Optional full-file storage. Off by default to keep PR bundles small.
		// Per-type cap (MaxFileBytes) is enforced here, not in the store.
		if req.IncludeFileContents && !entry.IsLockfile && !entry.IsGenerated {
			content, err := t.Client.FetchFileAtRef(ctx, req.Owner, req.Repo, f.Filename, pr.Head.SHA)
			if err != nil {
				t.Logger.WarnWithContext(ctx, op+": fetch_file_failed; skipping file artifact", map[string]interface{}{
					"operation":  op,
					"error_type": "upstream_error",
					"error":      err.Error(),
					"path":       f.Filename,
					"request_id": reqID,
				})
			} else if len(content) == 0 {
				// nothing to store
			} else if t.Config.MaxFileBytes > 0 && int64(len(content)) > t.Config.MaxFileBytes {
				t.Logger.InfoWithContext(ctx, op+": file over MaxFileBytes; skipping file artifact", map[string]interface{}{
					"operation":  op,
					"error_type": "file_too_large",
					"path":       f.Filename,
					"bytes":      len(content),
					"limit":      t.Config.MaxFileBytes,
					"request_id": reqID,
				})
			} else {
				ref, putErr := t.Artifacts.Put(ctx, bundleID, f.Filename, content)
				if putErr != nil {
					t.Logger.WarnWithContext(ctx, op+": store_file_failed; continuing with patch only", map[string]interface{}{
						"operation":  op,
						"error_type": "artifact_store_error",
						"error":      putErr.Error(),
						"path":       f.Filename,
						"request_id": reqID,
					})
				} else {
					entry.FileArtifactID = ref.ID
					telemetry.AddSpanEvent(ctx, "artifact_stored",
						attribute.String("truvag3.artifact_id", ref.ID),
						attribute.Int64("truvag3.artifact_bytes", ref.SizeBytes))
					telemetry.Histogram("github_tool.artifact_bytes", float64(ref.SizeBytes))
				}
			}
		}
		manifest.ChangedFiles = append(manifest.ChangedFiles, entry)
	}

	if req.IncludeExistingComments {
		comments, err := t.Client.ListExistingComments(ctx, req.Owner, req.Repo, req.PullNumber)
		if err != nil {
			// Don't fail the bundle just because comment fetch failed — log and continue.
			telemetry.RecordSpanError(ctx, err)
			t.Logger.WarnWithContext(ctx, op+": list_comments_failed; continuing without prior comments", map[string]interface{}{
				"operation":  op,
				"error_type": "upstream_error",
				"error":      err.Error(),
				"request_id": reqID,
			})
		}
		manifest.Comments = comments
	}

	t.sendOK(w, manifest)
	t.recordOutcome(op, "success", start)
}

// --- get_file_context --------------------------------------------------------

func (t *GitHubTool) handleGetFileContext(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "get_file_context"
	markReceived(ctx, op, reqID)

	var req GetFileContextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if req.BundleID == "" || req.Path == "" {
		err := fmt.Errorf("bundle_id and path are required")
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}
	if req.LineStart <= 0 {
		req.LineStart = 1
	}
	if req.LineEnd < req.LineStart {
		req.LineEnd = req.LineStart
	}
	span := req.LineEnd - req.LineStart
	if t.Config.MaxContextLines > 0 && span > t.Config.MaxContextLines {
		err := fmt.Errorf("line range too large: %d lines (max %d)", span, t.Config.MaxContextLines)
		t.recordValidationError(ctx, w, op, "range_too_large", err.Error(),
			"RANGE_TOO_LARGE", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "range_too_large", start)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.bundle_id", req.BundleID),
		attribute.String("github.path", req.Path),
		attribute.Int("github.line_start", req.LineStart),
		attribute.Int("github.line_end", req.LineEnd),
	)
	t.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":  op,
		"bundle_id":  req.BundleID,
		"path":       req.Path,
		"line_start": req.LineStart,
		"line_end":   req.LineEnd,
		"request_id": reqID,
	})

	// Resolve the artifact for this path. Prefer file artifact (full file) over
	// patch artifact (only the diff hunk lines), since file context is typically
	// what callers want. One Get call per attempt — no extra existence probe.
	artifactID := NewArtifactID(req.Path)
	raw, err := t.Artifacts.Get(ctx, req.BundleID, artifactID)
	if err != nil {
		artifactID = NewArtifactID(req.Path + ".patch")
		raw, err = t.Artifacts.Get(ctx, req.BundleID, artifactID)
	}
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.WarnWithContext(ctx, op+": artifact_not_found", map[string]interface{}{
			"operation":  op,
			"error_type": "artifact_not_found",
			"path":       req.Path,
			"request_id": reqID,
		})
		t.sendError(w, "artifact not found for path", http.StatusNotFound, "ARTIFACT_NOT_FOUND")
		t.recordOutcome(op, "artifact_not_found", start)
		return
	}
	telemetry.AddSpanEvent(ctx, "artifact_read",
		attribute.String("truvag3.artifact_id", artifactID),
		attribute.Int("truvag3.artifact_bytes", len(raw)))

	window := extractLineWindow(raw, req.LineStart, req.LineEnd, req.ContextBefore, req.ContextAfter, t.Config.MaxFileBytes)
	t.sendOK(w, GetFileContextResponse{
		BundleID:  req.BundleID,
		Path:      req.Path,
		LineStart: window.LineStart,
		LineEnd:   window.LineEnd,
		Content:   window.Text,
		Truncated: window.Truncated,
	})
	t.recordOutcome(op, "success", start)
}

// --- get_artifact_slice ------------------------------------------------------

func (t *GitHubTool) handleGetArtifactSlice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "get_artifact_slice"
	markReceived(ctx, op, reqID)

	var req GetArtifactSliceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if req.BundleID == "" || req.ArtifactID == "" {
		err := fmt.Errorf("bundle_id and artifact_id are required")
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.bundle_id", req.BundleID),
		attribute.String("truvag3.artifact_id", req.ArtifactID),
		attribute.Int64("github.byte_start", req.ByteStart),
		attribute.Int64("github.byte_limit", req.ByteLimit),
	)
	t.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":   op,
		"bundle_id":   req.BundleID,
		"artifact_id": req.ArtifactID,
		"byte_start":  req.ByteStart,
		"byte_limit":  req.ByteLimit,
		"request_id":  reqID,
	})

	data, totalSize, err := t.Artifacts.GetSlice(ctx, req.BundleID, req.ArtifactID, SliceRequest{
		ByteStart: req.ByteStart,
		ByteLimit: req.ByteLimit,
	})
	if err != nil {
		t.recordValidationError(ctx, w, op, "invalid_slice", err.Error(),
			"INVALID_SLICE", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_slice", start)
		return
	}
	telemetry.AddSpanEvent(ctx, "artifact_read",
		attribute.String("truvag3.artifact_id", req.ArtifactID),
		attribute.Int("truvag3.artifact_bytes", len(data)))
	telemetry.Histogram("github_tool.artifact_bytes", float64(len(data)))

	byteEnd := req.ByteStart + int64(len(data))
	t.sendOK(w, GetArtifactSliceResponse{
		BundleID:   req.BundleID,
		ArtifactID: req.ArtifactID,
		ByteStart:  req.ByteStart,
		ByteEnd:    byteEnd,
		Content:    string(data),
		// Truncated means "more bytes exist past this slice" — true only if
		// the slice ended before the artifact end.
		Truncated: byteEnd < totalSize,
	})
	t.recordOutcome(op, "success", start)
}

// --- list_existing_review_comments -------------------------------------------

func (t *GitHubTool) handleListExistingReviewComments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "list_existing_review_comments"
	markReceived(ctx, op, reqID)

	var req ListExistingReviewCommentsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if err := validatePRRef(req.Owner, req.Repo, req.PullNumber); err != nil {
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.owner", req.Owner),
		attribute.String("github.repo", req.Repo),
		attribute.Int("github.pull_number", req.PullNumber),
	)
	t.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":   op,
		"owner":       req.Owner,
		"repo":        req.Repo,
		"pull_number": req.PullNumber,
		"request_id":  reqID,
	})

	telemetry.AddSpanEvent(ctx, "github_api_call_started",
		attribute.String("github.endpoint", "GET /repos/.../pulls/{n}/comments + /issues/{n}/comments"))
	ghStart := time.Now()
	comments, err := t.Client.ListExistingComments(ctx, req.Owner, req.Repo, req.PullNumber)
	t.recordGitHubLatency(ghStart)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.ErrorWithContext(ctx, op+": github call failed", map[string]interface{}{
			"operation":  op,
			"error_type": "upstream_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		t.sendUpstreamError(w, "failed to list existing comments", err)
		t.recordOutcome(op, "upstream_error", start)
		return
	}

	t.sendOK(w, ListExistingReviewCommentsResponse{Comments: comments})
	t.recordOutcome(op, "success", start)
}

// --- create_pr_review --------------------------------------------------------

func (t *GitHubTool) handleCreatePRReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "create_pr_review"
	markReceived(ctx, op, reqID)

	var req CreatePRReviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if err := validatePRRef(req.Owner, req.Repo, req.PullNumber); err != nil {
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}
	if req.CommitID == "" || req.Body == "" {
		err := fmt.Errorf("commit_id and body are required")
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}
	if _, ok := allowedReviewEvents[req.Event]; !ok {
		err := fmt.Errorf("invalid review event %q; allowed: COMMENT, REQUEST_CHANGES", req.Event)
		t.recordValidationError(ctx, w, op, "invalid_event",
			"invalid review event; allowed: COMMENT, REQUEST_CHANGES",
			"INVALID_EVENT", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_event", start)
		return
	}
	for i, c := range req.Comments {
		if c.Path == "" || c.Line <= 0 || c.Body == "" {
			err := fmt.Errorf("invalid review comment at index %d", i)
			t.recordValidationError(ctx, w, op, "invalid_comment", err.Error(),
				"INVALID_COMMENT", http.StatusBadRequest, reqID, err)
			t.recordOutcome(op, "invalid_comment", start)
			return
		}
		if c.Side != "" && c.Side != "LEFT" && c.Side != "RIGHT" {
			err := fmt.Errorf("invalid side for comment at index %d (must be LEFT or RIGHT)", i)
			t.recordValidationError(ctx, w, op, "invalid_comment", err.Error(),
				"INVALID_COMMENT", http.StatusBadRequest, reqID, err)
			t.recordOutcome(op, "invalid_comment", start)
			return
		}
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.owner", req.Owner),
		attribute.String("github.repo", req.Repo),
		attribute.Int("github.pull_number", req.PullNumber),
		attribute.String("github.commit_id", req.CommitID),
		attribute.String("github.review_event", req.Event),
		attribute.Int("github.inline_comments", len(req.Comments)),
		attribute.Bool("truvag3.dry_run", req.DryRun),
	)

	if req.DryRun {
		t.Logger.InfoWithContext(ctx, op+": dry_run", map[string]interface{}{
			"operation":   op,
			"owner":       req.Owner,
			"repo":        req.Repo,
			"pull_number": req.PullNumber,
			"event":       req.Event,
			"comments":    len(req.Comments),
			"request_id":  reqID,
		})
		t.sendOK(w, CreatePRReviewResponse{State: "DRY_RUN", DryRun: true})
		telemetry.Counter("github_tool.review_posts", "event", req.Event, "outcome", "dry_run")
		t.recordOutcome(op, "dry_run", start)
		return
	}

	t.Logger.InfoWithContext(ctx, op+": posting", map[string]interface{}{
		"operation":   op,
		"owner":       req.Owner,
		"repo":        req.Repo,
		"pull_number": req.PullNumber,
		"event":       req.Event,
		"comments":    len(req.Comments),
		"request_id":  reqID,
	})

	telemetry.AddSpanEvent(ctx, "github_api_call_started",
		attribute.String("github.endpoint", "POST /repos/.../pulls/{n}/reviews"))
	ghStart := time.Now()
	resp, err := t.Client.CreatePullRequestReview(ctx, req)
	t.recordGitHubLatency(ghStart)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("github_tool.review_posts", "event", req.Event, "outcome", "failed")
		t.Logger.ErrorWithContext(ctx, op+": github call failed", map[string]interface{}{
			"operation":  op,
			"error_type": "upstream_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		t.sendUpstreamError(w, "failed to create PR review", err)
		t.recordOutcome(op, "upstream_error", start)
		return
	}
	telemetry.AddSpanEvent(ctx, "review_posted",
		attribute.String("github.html_url", resp.HTMLURL))
	telemetry.Counter("github_tool.review_posts", "event", req.Event, "outcome", "posted")
	t.Logger.InfoWithContext(ctx, op+": posted", map[string]interface{}{
		"operation":  op,
		"html_url":   resp.HTMLURL,
		"request_id": reqID,
	})
	t.sendOK(w, CreatePRReviewResponse{
		ID:      resp.ID,
		HTMLURL: resp.HTMLURL,
		State:   resp.State,
	})
	t.recordOutcome(op, "success", start)
}

// --- create_issue_comment ----------------------------------------------------

func (t *GitHubTool) handleCreateIssueComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	start := time.Now()
	reqID := requestID(r)
	const op = "create_issue_comment"
	markReceived(ctx, op, reqID)

	var req CreateIssueCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.recordValidationError(ctx, w, op, "decode_error", "invalid JSON body",
			"INVALID_JSON", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_json", start)
		return
	}
	if err := validatePRRef(req.Owner, req.Repo, req.PullNumber); err != nil {
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		err := fmt.Errorf("body is required")
		t.recordValidationError(ctx, w, op, "validation_error", err.Error(),
			"INVALID_REQUEST", http.StatusBadRequest, reqID, err)
		t.recordOutcome(op, "invalid_request", start)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.owner", req.Owner),
		attribute.String("github.repo", req.Repo),
		attribute.Int("github.pull_number", req.PullNumber),
		attribute.Bool("truvag3.dry_run", req.DryRun),
	)

	if req.DryRun {
		t.Logger.InfoWithContext(ctx, op+": dry_run", map[string]interface{}{
			"operation":   op,
			"owner":       req.Owner,
			"repo":        req.Repo,
			"pull_number": req.PullNumber,
			"request_id":  reqID,
		})
		t.sendOK(w, CreateIssueCommentResponse{DryRun: true})
		t.recordOutcome(op, "dry_run", start)
		return
	}

	telemetry.AddSpanEvent(ctx, "github_api_call_started",
		attribute.String("github.endpoint", "POST /repos/.../issues/{n}/comments"))
	ghStart := time.Now()
	resp, err := t.Client.CreateIssueComment(ctx, req)
	t.recordGitHubLatency(ghStart)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		t.Logger.ErrorWithContext(ctx, op+": github call failed", map[string]interface{}{
			"operation":  op,
			"error_type": "upstream_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		t.sendUpstreamError(w, "failed to create issue comment", err)
		t.recordOutcome(op, "upstream_error", start)
		return
	}
	t.Logger.InfoWithContext(ctx, op+": posted", map[string]interface{}{
		"operation":  op,
		"html_url":   resp.HTMLURL,
		"request_id": reqID,
	})
	t.sendOK(w, CreateIssueCommentResponse{ID: resp.ID, HTMLURL: resp.HTMLURL})
	t.recordOutcome(op, "success", start)
}

// --- Telemetry helpers -------------------------------------------------------

func (t *GitHubTool) recordOutcome(capability, status string, start time.Time) {
	telemetry.Counter("github_tool.requests", "capability", capability, "status", status)
	telemetry.Histogram("github_tool.request_duration_ms", float64(time.Since(start).Milliseconds()))
	telemetry.RecordToolCall("github-tool", capability,
		float64(time.Since(start).Milliseconds()), status)
}

func (t *GitHubTool) recordGitHubLatency(start time.Time) {
	telemetry.Histogram("github_tool.github_api_latency_ms", float64(time.Since(start).Milliseconds()))
}

// --- Validation + classification helpers -------------------------------------

func validatePRRef(owner, repo string, pullNumber int) error {
	if owner == "" || repo == "" {
		return fmt.Errorf("owner and repo are required")
	}
	if pullNumber <= 0 {
		return fmt.Errorf("pull_number must be positive")
	}
	return nil
}

// looksLockfile recognizes common lockfile basenames across ecosystems.
func looksLockfile(filename string) bool {
	base := path.Base(filename)
	switch base {
	case "go.sum",
		"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb",
		"Pipfile.lock", "poetry.lock", "uv.lock",
		"Cargo.lock",
		"Gemfile.lock",
		"composer.lock",
		"mix.lock",
		"pubspec.lock":
		return true
	}
	return false
}

// looksGenerated heuristically detects generated files by path pattern or by
// the conventional "DO NOT EDIT" marker in the patch hunk.
func looksGenerated(filename, patch string) bool {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasPrefix(lower, "vendor/"),
		strings.HasPrefix(lower, "node_modules/"),
		strings.HasPrefix(lower, "third_party/"),
		strings.HasSuffix(lower, ".pb.go"),
		strings.HasSuffix(lower, ".pb.gw.go"),
		strings.HasSuffix(lower, ".deepcopy.go"),
		strings.HasSuffix(lower, "_generated.go"),
		strings.HasSuffix(lower, ".gen.ts"),
		strings.HasSuffix(lower, ".generated.ts"),
		strings.Contains(lower, "/generated/"),
		strings.Contains(lower, "/mocks/") || strings.HasPrefix(path.Base(lower), "mock_"):
		return true
	}
	if patch != "" && strings.Contains(patch, "DO NOT EDIT") {
		return true
	}
	return false
}

// riskHintsFor returns coarse domain hints from a path. Used by the agent's
// shard planner to prioritize sensitive areas.
func riskHintsFor(filename string) []string {
	lower := strings.ToLower(filename)
	var hints []string
	add := func(h string) { hints = append(hints, h) }

	if containsAny(lower, "auth", "session", "credential", "secret", "token") {
		add("auth")
	}
	if containsAny(lower, "crypto", "tls", "ssl", "cipher") {
		add("crypto")
	}
	if containsAny(lower, "permission", "rbac", "acl", "policy") {
		add("permissions")
	}
	if containsAny(lower, "migration", "schema") {
		add("schema")
	}
	if containsAny(lower, "validate", "sanitize", "parser", "decode") {
		add("input_parsing")
	}
	if containsAny(lower, "/api/", "_api.go", "handler", "route") {
		add("public_api")
	}
	return hints
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// --- File-window extraction --------------------------------------------------

type lineWindow struct {
	LineStart int
	LineEnd   int
	Text      string
	Truncated bool
}

// extractLineWindow pulls a line range out of file bytes, padding by
// context_before / context_after, and clamps total bytes to maxBytes.
func extractLineWindow(raw []byte, lineStart, lineEnd, before, after int, maxBytes int64) lineWindow {
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	startWanted := lineStart - before
	if startWanted < 1 {
		startWanted = 1
	}
	endWanted := lineEnd + after

	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	lineNum := 0
	emittedStart := 0
	emittedEnd := 0
	truncated := false
	for scanner.Scan() {
		lineNum++
		if lineNum < startWanted {
			continue
		}
		if lineNum > endWanted {
			break
		}
		if emittedStart == 0 {
			emittedStart = lineNum
		}
		if maxBytes > 0 && int64(b.Len()+len(scanner.Text())+1) > maxBytes {
			truncated = true
			break
		}
		b.WriteString(scanner.Text())
		b.WriteByte('\n')
		emittedEnd = lineNum
	}
	if emittedStart == 0 {
		emittedStart = startWanted
	}
	if emittedEnd == 0 {
		emittedEnd = emittedStart
	}
	return lineWindow{
		LineStart: emittedStart,
		LineEnd:   emittedEnd,
		Text:      b.String(),
		Truncated: truncated,
	}
}
