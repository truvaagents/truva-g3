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

// --- Error Code Constants ---

const (
	ErrCodeInvalidRequest    = "INVALID_REQUEST"
	ErrCodeInvalidInput      = "INVALID_INPUT"
	ErrCodeIssueNotFound     = "ISSUE_NOT_FOUND"
	ErrCodeAuthError         = "AUTH_ERROR"
	ErrCodeRateLimit         = "RATE_LIMIT"
	ErrCodeServiceError      = "SERVICE_ERROR"
	ErrCodeInvalidTransition = "INVALID_TRANSITION"
)

// --- Request Types ---

type GetIssueRequest struct {
	IssueKey string `json:"issue_key"`
	Fields   string `json:"fields,omitempty"`
}

type SearchIssuesRequest struct {
	JQL        string `json:"jql"`
	Fields     string `json:"fields,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type CreateIssueRequest struct {
	ProjectKey        string `json:"project_key"`
	Summary           string `json:"summary"`
	IssueType         string `json:"issue_type"`
	Description       string `json:"description,omitempty"`
	AssigneeAccountID string `json:"assignee_account_id,omitempty"`
	Priority          string `json:"priority,omitempty"`
	Labels            string `json:"labels,omitempty"`
}

type UpdateIssueRequest struct {
	IssueKey     string `json:"issue_key"`
	Summary      string `json:"summary,omitempty"`
	Description  string `json:"description,omitempty"`
	Priority     string `json:"priority,omitempty"`
	AddLabels    string `json:"add_labels,omitempty"`
	RemoveLabels string `json:"remove_labels,omitempty"`
}

type AddCommentRequest struct {
	IssueKey string `json:"issue_key"`
	Body     string `json:"body"`
}

type TransitionIssueRequest struct {
	IssueKey       string `json:"issue_key"`
	TransitionName string `json:"transition_name"`
}

type AssignIssueRequest struct {
	IssueKey  string `json:"issue_key"`
	AccountID string `json:"account_id"`
}

type LookupUserRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type GetProjectRequest struct {
	ProjectKey string `json:"project_key"`
}

type ListSprintsRequest struct {
	BoardID int    `json:"board_id"`
	State   string `json:"state,omitempty"`
}

type GetSprintIssuesRequest struct {
	SprintID   int    `json:"sprint_id"`
	Fields     string `json:"fields,omitempty"`
	MaxResults int    `json:"max_results,omitempty"`
}

type AddWorklogRequest struct {
	IssueKey    string `json:"issue_key"`
	TimeSpent   string `json:"time_spent"`
	Started     string `json:"started,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

type LinkIssuesRequest struct {
	LinkType   string `json:"link_type"`
	InwardKey  string `json:"inward_key"`
	OutwardKey string `json:"outward_key"`
}

type GetChangelogRequest struct {
	IssueKey   string `json:"issue_key"`
	MaxResults int    `json:"max_results,omitempty"`
}

type ListBoardsRequest struct {
	ProjectKey string `json:"project_key,omitempty"`
}

// --- Error Helpers ---

// sendError writes a structured error response. WriteHeader MUST be called before encoding
// to prevent Go from defaulting to HTTP 200.
func (t *JiraTool) sendError(w http.ResponseWriter, message string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: status == http.StatusTooManyRequests || status == http.StatusBadGateway,
		},
	})
}

// sendUpstreamError sends a structured error response using ClassifyUpstreamError classification.
func (t *JiraTool) sendUpstreamError(w http.ResponseWriter, message string, info core.UpstreamErrorInfo) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(info.HTTPStatus)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      info.Code,
			Message:   message,
			Category:  info.Category,
			Retryable: info.Retryable,
		},
	})
}

// --- Handlers ---

func (t *JiraTool) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_issue request", map[string]interface{}{
			"operation":  "get_issue",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_issue.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "get_issue"),
	)

	var req GetIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "get_issue",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "get_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.get_issue.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
	)

	issue, err := t.client.GetIssue(ctx, req.IssueKey, req.Fields)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.get_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "get_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "get_issue failed", map[string]interface{}{
				"operation":   "get_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_issue completed", map[string]interface{}{
			"operation":   "get_issue",
			"issue_key":   issue.Key,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_issue.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", issue.Key),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"key":    issue.Key,
			"id":     issue.ID,
			"fields": issue.Fields,
		},
	})
}

func (t *JiraTool) handleSearchIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing search_issues request", map[string]interface{}{
			"operation":  "search_issues",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.search_issues.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "search_issues"),
	)

	var req SearchIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "search_issues",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.JQL == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: jql", map[string]interface{}{
				"operation":  "search_issues",
				"request_id": requestID,
			})
		}
		t.sendError(w, "jql is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 100 {
		maxResults = 100
	}

	telemetry.AddSpanEvent(ctx, "jira.search_issues.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("jql", req.JQL),
		attribute.Int("max_results", maxResults),
	)

	result, err := t.client.SearchIssues(ctx, req.JQL, req.Fields, maxResults)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.search_issues.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("jql", req.JQL),
		)
		telemetry.Counter("jira.request.failed", "operation", "search_issues", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "search_issues failed", map[string]interface{}{
				"operation":   "search_issues",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "search_issues completed", map[string]interface{}{
			"operation":   "search_issues",
			"total":       result.Total,
			"returned":    len(result.Issues),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.search_issues.success",
		attribute.String("request_id", requestID),
		attribute.Int("total", result.Total),
		attribute.Int("returned", len(result.Issues)),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	// Build slim issue list for response
	issues := make([]map[string]interface{}, 0, len(result.Issues))
	for _, iss := range result.Issues {
		issues = append(issues, map[string]interface{}{
			"key":    iss.Key,
			"id":     iss.ID,
			"fields": iss.Fields,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issues": issues,
			"total":  result.Total,
		},
	})
}

func (t *JiraTool) handleCreateIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing create_issue request", map[string]interface{}{
			"operation":  "create_issue",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.create_issue.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "create_issue"),
	)

	var req CreateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "create_issue",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.ProjectKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: project_key", map[string]interface{}{
				"operation":  "create_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "project_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.Summary == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: summary", map[string]interface{}{
				"operation":  "create_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "summary is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.IssueType == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_type", map[string]interface{}{
				"operation":  "create_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_type is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Build JIRA API request body
	fields := map[string]interface{}{
		"project":   map[string]string{"key": req.ProjectKey},
		"summary":   req.Summary,
		"issuetype": map[string]string{"name": req.IssueType},
	}

	if req.Description != "" {
		fields["description"] = adfFromText(req.Description)
	}
	if req.AssigneeAccountID != "" {
		fields["assignee"] = map[string]string{"accountId": req.AssigneeAccountID}
	}
	if req.Priority != "" {
		fields["priority"] = map[string]string{"name": req.Priority}
	}
	if req.Labels != "" {
		parts := strings.Split(req.Labels, ",")
		labels := make([]string, 0, len(parts))
		for _, l := range parts {
			if trimmed := strings.TrimSpace(l); trimmed != "" {
				labels = append(labels, trimmed)
			}
		}
		fields["labels"] = labels
	}

	telemetry.AddSpanEvent(ctx, "jira.create_issue.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("project_key", req.ProjectKey),
		attribute.String("issue_type", req.IssueType),
	)

	result, err := t.client.CreateIssue(ctx, fields)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.create_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("project_key", req.ProjectKey),
			attribute.String("issue_type", req.IssueType),
		)
		telemetry.Counter("jira.request.failed", "operation", "create_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "create_issue failed", map[string]interface{}{
				"operation":   "create_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "create_issue completed", map[string]interface{}{
			"operation":   "create_issue",
			"issue_key":   result.Key,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.create_issue.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", result.Key),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"key":  result.Key,
			"id":   result.ID,
			"self": result.Self,
		},
	})
}

func (t *JiraTool) handleUpdateIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing update_issue request", map[string]interface{}{
			"operation":  "update_issue",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.update_issue.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "update_issue"),
	)

	var req UpdateIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "update_issue",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "update_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Build fields map for direct field updates
	fields := make(map[string]interface{})
	if req.Summary != "" {
		fields["summary"] = req.Summary
	}
	if req.Description != "" {
		fields["description"] = adfFromText(req.Description)
	}
	if req.Priority != "" {
		fields["priority"] = map[string]string{"name": req.Priority}
	}

	// Build update map for array operations (add/remove labels)
	update := make(map[string]interface{})
	if req.AddLabels != "" || req.RemoveLabels != "" {
		var labelOps []map[string]interface{}

		if req.AddLabels != "" {
			for _, l := range strings.Split(req.AddLabels, ",") {
				if trimmed := strings.TrimSpace(l); trimmed != "" {
					labelOps = append(labelOps, map[string]interface{}{"add": trimmed})
				}
			}
		}
		if req.RemoveLabels != "" {
			for _, l := range strings.Split(req.RemoveLabels, ",") {
				if trimmed := strings.TrimSpace(l); trimmed != "" {
					labelOps = append(labelOps, map[string]interface{}{"remove": trimmed})
				}
			}
		}

		if len(labelOps) > 0 {
			update["labels"] = labelOps
		}
	}

	if len(fields) == 0 && len(update) == 0 {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "No fields to update provided", map[string]interface{}{
				"operation":  "update_issue",
				"request_id": requestID,
				"issue_key":  req.IssueKey,
			})
		}
		t.sendError(w, "At least one field to update is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.update_issue.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.Int("fields_count", len(fields)),
		attribute.Int("update_ops_count", len(update)),
	)

	err := t.client.UpdateIssue(ctx, req.IssueKey, fields, update)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.update_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "update_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "update_issue failed", map[string]interface{}{
				"operation":   "update_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "update_issue completed", map[string]interface{}{
			"operation":   "update_issue",
			"issue_key":   req.IssueKey,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.update_issue.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key": req.IssueKey,
			"message":   "Issue updated successfully",
		},
	})
}

func (t *JiraTool) handleAddComment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing add_comment request", map[string]interface{}{
			"operation":  "add_comment",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.add_comment.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "add_comment"),
	)

	var req AddCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "add_comment",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "add_comment",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.Body == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: body", map[string]interface{}{
				"operation":  "add_comment",
				"request_id": requestID,
			})
		}
		t.sendError(w, "body is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.add_comment.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
	)

	result, err := t.client.AddComment(ctx, req.IssueKey, adfFromText(req.Body))
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.add_comment.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "add_comment", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "add_comment failed", map[string]interface{}{
				"operation":   "add_comment",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "add_comment completed", map[string]interface{}{
			"operation":   "add_comment",
			"issue_key":   req.IssueKey,
			"comment_id":  result.ID,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.add_comment.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("comment_id", result.ID),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key":  req.IssueKey,
			"comment_id": result.ID,
			"created":    result.Created,
		},
	})
}

func (t *JiraTool) handleTransitionIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing transition_issue request", map[string]interface{}{
			"operation":  "transition_issue",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.transition_issue.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "transition_issue"),
	)

	var req TransitionIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "transition_issue",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "transition_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.TransitionName == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: transition_name", map[string]interface{}{
				"operation":  "transition_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "transition_name is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Step 1: Fetch available transitions
	telemetry.AddSpanEvent(ctx, "jira.transition_issue.fetching_transitions",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
	)

	transitions, err := t.client.GetTransitions(ctx, req.IssueKey)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.transition_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
			attribute.String("phase", "get_transitions"),
		)
		telemetry.Counter("jira.request.failed", "operation", "transition_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to get transitions", map[string]interface{}{
				"operation":   "transition_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	// Step 2: Match by name (case-insensitive)
	var matched *Transition
	available := make([]string, 0, len(transitions))
	for i, tr := range transitions {
		available = append(available, tr.Name)
		if strings.EqualFold(tr.Name, req.TransitionName) {
			matched = &transitions[i]
		}
	}

	if matched == nil {
		t.sendError(w,
			fmt.Sprintf("Transition '%s' not available. Available: %s",
				req.TransitionName, strings.Join(available, ", ")),
			http.StatusBadRequest, ErrCodeInvalidTransition)
		return
	}

	// Step 3: Execute transition
	telemetry.AddSpanEvent(ctx, "jira.transition_issue.executing",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("transition_id", matched.ID),
		attribute.String("transition_name", matched.Name),
	)

	err = t.client.TransitionIssue(ctx, req.IssueKey, matched.ID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.transition_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
			attribute.String("phase", "execute_transition"),
			attribute.String("transition_name", matched.Name),
		)
		telemetry.Counter("jira.request.failed", "operation", "transition_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "transition_issue failed", map[string]interface{}{
				"operation":   "transition_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "transition_issue completed", map[string]interface{}{
			"operation":       "transition_issue",
			"issue_key":       req.IssueKey,
			"transition_name": matched.Name,
			"target_status":   matched.To.Name,
			"duration_ms":     time.Since(startTime).Milliseconds(),
			"request_id":      requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.transition_issue.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("transition_name", matched.Name),
		attribute.String("target_status", matched.To.Name),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key":       req.IssueKey,
			"transition_name": matched.Name,
			"target_status":   matched.To.Name,
			"message":         "Issue transitioned successfully",
		},
	})
}

func (t *JiraTool) handleAssignIssue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing assign_issue request", map[string]interface{}{
			"operation":  "assign_issue",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.assign_issue.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "assign_issue"),
	)

	var req AssignIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "assign_issue",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "assign_issue",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	action := "assigned"
	if req.AccountID == "" {
		action = "unassigned"
	}

	telemetry.AddSpanEvent(ctx, "jira.assign_issue.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("action", action),
	)

	err := t.client.AssignIssue(ctx, req.IssueKey, req.AccountID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.assign_issue.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
			attribute.String("action", action),
		)
		telemetry.Counter("jira.request.failed", "operation", "assign_issue", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "assign_issue failed", map[string]interface{}{
				"operation":   "assign_issue",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "assign_issue completed", map[string]interface{}{
			"operation":   "assign_issue",
			"issue_key":   req.IssueKey,
			"action":      action,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.assign_issue.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("action", action),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key": req.IssueKey,
			"action":    action,
			"message":   fmt.Sprintf("Issue %s successfully", action),
		},
	})
}

func (t *JiraTool) handleLookupUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing lookup_user request", map[string]interface{}{
			"operation":  "lookup_user",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.lookup_user.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "lookup_user"),
	)

	var req LookupUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "lookup_user",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.Query == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: query", map[string]interface{}{
				"operation":  "lookup_user",
				"request_id": requestID,
			})
		}
		t.sendError(w, "query is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 50 {
		maxResults = 50
	}

	telemetry.AddSpanEvent(ctx, "jira.lookup_user.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("query", req.Query),
		attribute.Int("max_results", maxResults),
	)

	users, err := t.client.SearchUsers(ctx, req.Query, maxResults)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.lookup_user.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("query", req.Query),
		)
		telemetry.Counter("jira.request.failed", "operation", "lookup_user", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "lookup_user failed", map[string]interface{}{
				"operation":   "lookup_user",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "lookup_user completed", map[string]interface{}{
			"operation":   "lookup_user",
			"results":     len(users),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.lookup_user.success",
		attribute.String("request_id", requestID),
		attribute.Int("results", len(users)),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	// Build slim user list
	userList := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		userList = append(userList, map[string]interface{}{
			"account_id":    u.AccountID,
			"display_name":  u.DisplayName,
			"email_address": u.EmailAddress,
			"active":        u.Active,
			"account_type":  u.AccountType,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"users": userList,
			"total": len(userList),
		},
	})
}

func (t *JiraTool) handleGetProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_project request", map[string]interface{}{
			"operation":  "get_project",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_project.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "get_project"),
	)

	var req GetProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "get_project",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.ProjectKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: project_key", map[string]interface{}{
				"operation":  "get_project",
				"request_id": requestID,
			})
		}
		t.sendError(w, "project_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.get_project.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("project_key", req.ProjectKey),
	)

	project, err := t.client.GetProject(ctx, req.ProjectKey)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.get_project.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("project_key", req.ProjectKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "get_project", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "get_project failed", map[string]interface{}{
				"operation":   "get_project",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_project completed", map[string]interface{}{
			"operation":   "get_project",
			"project_key": project.Key,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_project.success",
		attribute.String("request_id", requestID),
		attribute.String("project_key", project.Key),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"id":          project.ID,
			"key":         project.Key,
			"name":        project.Name,
			"lead":        project.Lead,
			"issue_types": project.IssueTypes,
			"components":  project.Components,
			"style":       project.Style,
		},
	})
}

func (t *JiraTool) handleListSprints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing list_sprints request", map[string]interface{}{
			"operation":  "list_sprints",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.list_sprints.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "list_sprints"),
	)

	var req ListSprintsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "list_sprints",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.BoardID <= 0 {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing or invalid required field: board_id", map[string]interface{}{
				"operation":  "list_sprints",
				"request_id": requestID,
			})
		}
		t.sendError(w, "board_id is required and must be a positive integer", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Validate state filter if provided
	if req.State != "" {
		validStates := map[string]bool{"active": true, "closed": true, "future": true}
		if !validStates[strings.ToLower(req.State)] {
			t.sendError(w, "state must be one of: active, closed, future", http.StatusBadRequest, ErrCodeInvalidInput)
			return
		}
		req.State = strings.ToLower(req.State)
	}

	telemetry.AddSpanEvent(ctx, "jira.list_sprints.calling_api",
		attribute.String("request_id", requestID),
		attribute.Int("board_id", req.BoardID),
		attribute.String("state", req.State),
	)

	sprints, err := t.client.GetSprints(ctx, req.BoardID, req.State)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.list_sprints.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.Int("board_id", req.BoardID),
		)
		telemetry.Counter("jira.request.failed", "operation", "list_sprints", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "list_sprints failed", map[string]interface{}{
				"operation":   "list_sprints",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "list_sprints completed", map[string]interface{}{
			"operation":   "list_sprints",
			"board_id":    req.BoardID,
			"results":     len(sprints),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.list_sprints.success",
		attribute.String("request_id", requestID),
		attribute.Int("results", len(sprints)),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	sprintList := make([]map[string]interface{}, 0, len(sprints))
	for _, s := range sprints {
		entry := map[string]interface{}{
			"id":    s.ID,
			"name":  s.Name,
			"state": s.State,
			"goal":  s.Goal,
		}
		if s.StartDate != "" {
			entry["start_date"] = s.StartDate
		}
		if s.EndDate != "" {
			entry["end_date"] = s.EndDate
		}
		if s.CompleteDate != "" {
			entry["complete_date"] = s.CompleteDate
		}
		sprintList = append(sprintList, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"sprints": sprintList,
			"total":   len(sprintList),
		},
	})
}

func (t *JiraTool) handleGetSprintIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_sprint_issues request", map[string]interface{}{
			"operation":  "get_sprint_issues",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_sprint_issues.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "get_sprint_issues"),
	)

	var req GetSprintIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "get_sprint_issues",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.SprintID <= 0 {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing or invalid required field: sprint_id", map[string]interface{}{
				"operation":  "get_sprint_issues",
				"request_id": requestID,
			})
		}
		t.sendError(w, "sprint_id is required and must be a positive integer", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 100 {
		maxResults = 100
	}

	telemetry.AddSpanEvent(ctx, "jira.get_sprint_issues.calling_api",
		attribute.String("request_id", requestID),
		attribute.Int("sprint_id", req.SprintID),
		attribute.Int("max_results", maxResults),
	)

	result, err := t.client.GetSprintIssues(ctx, req.SprintID, req.Fields, maxResults)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.get_sprint_issues.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.Int("sprint_id", req.SprintID),
		)
		telemetry.Counter("jira.request.failed", "operation", "get_sprint_issues", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "get_sprint_issues failed", map[string]interface{}{
				"operation":   "get_sprint_issues",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_sprint_issues completed", map[string]interface{}{
			"operation":   "get_sprint_issues",
			"sprint_id":   req.SprintID,
			"total":       result.Total,
			"returned":    len(result.Issues),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_sprint_issues.success",
		attribute.String("request_id", requestID),
		attribute.Int("total", result.Total),
		attribute.Int("returned", len(result.Issues)),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	issues := make([]map[string]interface{}, 0, len(result.Issues))
	for _, iss := range result.Issues {
		issues = append(issues, map[string]interface{}{
			"key":    iss.Key,
			"id":     iss.ID,
			"fields": iss.Fields,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issues": issues,
			"total":  result.Total,
		},
	})
}

// parseTimeSpent converts human-readable time strings to seconds.
// Supports formats: "2h", "30m", "1h 30m", "2h30m", "3600" (raw seconds).
func parseTimeSpent(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("time_spent is empty")
	}

	// Try raw seconds
	var totalSeconds int
	if _, err := fmt.Sscanf(s, "%d", &totalSeconds); err == nil && !strings.ContainsAny(s, "hmdwHMDW") {
		return totalSeconds, nil
	}

	totalSeconds = 0
	s = strings.ToLower(s)
	remaining := s

	for len(remaining) > 0 {
		remaining = strings.TrimSpace(remaining)
		if remaining == "" {
			break
		}

		var value int
		var unit byte
		n, err := fmt.Sscanf(remaining, "%d%c", &value, &unit)
		if err != nil || n != 2 {
			return 0, fmt.Errorf("invalid time format '%s': use format like '2h 30m', '1d', '45m'", s)
		}

		switch unit {
		case 'w':
			totalSeconds += value * 5 * 8 * 3600 // 1w = 5 working days
		case 'd':
			totalSeconds += value * 8 * 3600 // 1d = 8 hours
		case 'h':
			totalSeconds += value * 3600
		case 'm':
			totalSeconds += value * 60
		default:
			return 0, fmt.Errorf("unknown time unit '%c': use w (weeks), d (days), h (hours), m (minutes)", unit)
		}

		// Advance past the parsed portion
		idx := strings.IndexByte(remaining, unit)
		if idx >= 0 {
			remaining = remaining[idx+1:]
		}
	}

	if totalSeconds <= 0 {
		return 0, fmt.Errorf("time_spent must be positive")
	}
	return totalSeconds, nil
}

func (t *JiraTool) handleAddWorklog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing add_worklog request", map[string]interface{}{
			"operation":  "add_worklog",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.add_worklog.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "add_worklog"),
	)

	var req AddWorklogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "add_worklog",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "add_worklog",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.TimeSpent == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: time_spent", map[string]interface{}{
				"operation":  "add_worklog",
				"request_id": requestID,
			})
		}
		t.sendError(w, "time_spent is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	seconds, err := parseTimeSpent(req.TimeSpent)
	if err != nil {
		t.sendError(w, err.Error(), http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.add_worklog.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.Int("time_spent_seconds", seconds),
	)

	result, err := t.client.AddWorklog(ctx, req.IssueKey, seconds, req.Started, req.Comment)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.add_worklog.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "add_worklog", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "add_worklog failed", map[string]interface{}{
				"operation":   "add_worklog",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "add_worklog completed", map[string]interface{}{
			"operation":   "add_worklog",
			"issue_key":   req.IssueKey,
			"worklog_id":  result.ID,
			"time_spent":  result.TimeSpent,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.add_worklog.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.String("worklog_id", result.ID),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key":          req.IssueKey,
			"worklog_id":         result.ID,
			"time_spent":         result.TimeSpent,
			"time_spent_seconds": result.TimeSpentSeconds,
			"started":            result.Started,
			"created":            result.Created,
		},
	})
}

func (t *JiraTool) handleLinkIssues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing link_issues request", map[string]interface{}{
			"operation":  "link_issues",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.link_issues.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "link_issues"),
	)

	var req LinkIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "link_issues",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.LinkType == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: link_type", map[string]interface{}{
				"operation":  "link_issues",
				"request_id": requestID,
			})
		}
		t.sendError(w, "link_type is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.InwardKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: inward_key", map[string]interface{}{
				"operation":  "link_issues",
				"request_id": requestID,
			})
		}
		t.sendError(w, "inward_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}
	if req.OutwardKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: outward_key", map[string]interface{}{
				"operation":  "link_issues",
				"request_id": requestID,
			})
		}
		t.sendError(w, "outward_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	// Fetch available link types to validate and match by name (case-insensitive)
	telemetry.AddSpanEvent(ctx, "jira.link_issues.fetching_link_types",
		attribute.String("request_id", requestID),
	)

	linkTypes, err := t.client.GetIssueLinkTypes(ctx)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.link_issues.link_types_error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
		)
		telemetry.Counter("jira.request.failed", "operation", "link_issues", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to get link types", map[string]interface{}{
				"operation":   "link_issues",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	var matchedType string
	available := make([]string, 0, len(linkTypes))
	for _, lt := range linkTypes {
		available = append(available, lt.Name)
		if strings.EqualFold(lt.Name, req.LinkType) {
			matchedType = lt.Name
		}
	}

	if matchedType == "" {
		t.sendError(w,
			fmt.Sprintf("Link type '%s' not available. Available: %s",
				req.LinkType, strings.Join(available, ", ")),
			http.StatusBadRequest, ErrCodeInvalidInput)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.link_issues.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("link_type", matchedType),
		attribute.String("inward_key", req.InwardKey),
		attribute.String("outward_key", req.OutwardKey),
	)

	err = t.client.CreateIssueLink(ctx, matchedType, req.InwardKey, req.OutwardKey)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.link_issues.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("link_type", matchedType),
		)
		telemetry.Counter("jira.request.failed", "operation", "link_issues", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "link_issues failed", map[string]interface{}{
				"operation":   "link_issues",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "link_issues completed", map[string]interface{}{
			"operation":   "link_issues",
			"link_type":   matchedType,
			"inward_key":  req.InwardKey,
			"outward_key": req.OutwardKey,
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.link_issues.success",
		attribute.String("request_id", requestID),
		attribute.String("link_type", matchedType),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"link_type":   matchedType,
			"inward_key":  req.InwardKey,
			"outward_key": req.OutwardKey,
			"message":     "Issues linked successfully",
		},
	})
}

func (t *JiraTool) handleGetChangelog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing get_changelog request", map[string]interface{}{
			"operation":  "get_changelog",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_changelog.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "get_changelog"),
	)

	var req GetChangelogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "get_changelog",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	if req.IssueKey == "" {
		if t.Logger != nil {
			t.Logger.WarnWithContext(ctx, "Missing required field: issue_key", map[string]interface{}{
				"operation":  "get_changelog",
				"request_id": requestID,
			})
		}
		t.sendError(w, "issue_key is required", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 100 {
		maxResults = 100
	}

	telemetry.AddSpanEvent(ctx, "jira.get_changelog.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.Int("max_results", maxResults),
	)

	result, err := t.client.GetChangelog(ctx, req.IssueKey, maxResults)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.get_changelog.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("issue_key", req.IssueKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "get_changelog", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "get_changelog failed", map[string]interface{}{
				"operation":   "get_changelog",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "get_changelog completed", map[string]interface{}{
			"operation":   "get_changelog",
			"issue_key":   req.IssueKey,
			"total":       result.Total,
			"returned":    len(result.Histories),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.get_changelog.success",
		attribute.String("request_id", requestID),
		attribute.String("issue_key", req.IssueKey),
		attribute.Int("total", result.Total),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	// Build slim changelog entries
	histories := make([]map[string]interface{}, 0, len(result.Histories))
	for _, h := range result.Histories {
		items := make([]map[string]interface{}, 0, len(h.Items))
		for _, item := range h.Items {
			items = append(items, map[string]interface{}{
				"field":       item.Field,
				"field_type":  item.FieldType,
				"from_string": item.FromString,
				"to_string":   item.ToString,
			})
		}
		histories = append(histories, map[string]interface{}{
			"id":      h.ID,
			"author":  h.Author,
			"created": h.Created,
			"items":   items,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"issue_key": req.IssueKey,
			"histories": histories,
			"total":     result.Total,
		},
	})
}

func (t *JiraTool) handleListBoards(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	startTime := time.Now()
	requestID := telemetry.GetBaggage(ctx)["request_id"]

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "Processing list_boards request", map[string]interface{}{
			"operation":  "list_boards",
			"request_id": requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.list_boards.request",
		attribute.String("request_id", requestID),
		attribute.String("operation", "list_boards"),
	)

	var req ListBoardsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":  "list_boards",
				"request_id": requestID,
				"error":      err.Error(),
			})
		}
		t.sendError(w, "Invalid request format", http.StatusBadRequest, ErrCodeInvalidRequest)
		return
	}

	telemetry.AddSpanEvent(ctx, "jira.list_boards.calling_api",
		attribute.String("request_id", requestID),
		attribute.String("project_key", req.ProjectKey),
	)

	boards, err := t.client.GetBoards(ctx, req.ProjectKey)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.AddSpanEvent(ctx, "jira.list_boards.error",
			attribute.String("request_id", requestID),
			attribute.String("error", err.Error()),
			attribute.String("project_key", req.ProjectKey),
		)
		telemetry.Counter("jira.request.failed", "operation", "list_boards", "module", "jira-tool")
		if t.Logger != nil {
			t.Logger.ErrorWithContext(ctx, "list_boards failed", map[string]interface{}{
				"operation":   "list_boards",
				"error":       err.Error(),
				"request_id":  requestID,
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		t.sendUpstreamError(w, err.Error(), core.ClassifyUpstreamError(err))
		return
	}

	if t.Logger != nil {
		t.Logger.InfoWithContext(ctx, "list_boards completed", map[string]interface{}{
			"operation":   "list_boards",
			"results":     len(boards),
			"duration_ms": time.Since(startTime).Milliseconds(),
			"request_id":  requestID,
		})
	}

	telemetry.AddSpanEvent(ctx, "jira.list_boards.success",
		attribute.String("request_id", requestID),
		attribute.Int("results", len(boards)),
		attribute.Int64("duration_ms", time.Since(startTime).Milliseconds()),
	)

	boardList := make([]map[string]interface{}, 0, len(boards))
	for _, b := range boards {
		entry := map[string]interface{}{
			"id":   b.ID,
			"name": b.Name,
			"type": b.Type,
		}
		if b.Location.ProjectKey != "" {
			entry["project_key"] = b.Location.ProjectKey
			entry["project_name"] = b.Location.ProjectName
		}
		boardList = append(boardList, entry)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data: map[string]interface{}{
			"boards": boardList,
			"total":  len(boardList),
		},
	})
}
