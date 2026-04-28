package main

import (
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type GitHubTool struct {
	*core.BaseTool

	Config      Config
	Client      *GitHubClient
	Artifacts   ArtifactStore
	RedisClient *redis.Client
}

func NewGitHubTool(cfg Config, redisClient *redis.Client) (*GitHubTool, error) {
	httpClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	})
	httpClient.Timeout = 60 * time.Second

	client := NewGitHubClient(httpClient, GitHubClientConfig{
		Token:   cfg.GitHubToken,
		BaseURL: cfg.GitHubAPIBaseURL,
	})

	artifacts, err := NewArtifactStore(cfg, redisClient)
	if err != nil {
		return nil, err
	}

	tool := &GitHubTool{
		BaseTool:    core.NewTool("github-tool"),
		Config:      cfg,
		Client:      client,
		Artifacts:   artifacts,
		RedisClient: redisClient,
	}
	tool.registerCapabilities()
	return tool, nil
}

// declareMetrics registers the tool's domain metrics. Safe to call before
// telemetry.Initialize — declarations are stored and processed at init time.
func declareMetrics() {
	telemetry.DeclareMetrics("github-tool", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{Name: "github_tool.requests", Type: "counter",
				Help: "Capability calls by status.", Labels: []string{"capability", "status"}},
			{Name: "github_tool.request_duration_ms", Type: "histogram",
				Help: "Capability call latency.", Unit: "milliseconds"},
			{Name: "github_tool.github_api_latency_ms", Type: "histogram",
				Help: "GitHub REST API call latency.", Unit: "milliseconds"},
			{Name: "github_tool.artifact_bytes", Type: "histogram",
				Help: "Bytes per artifact stored or read.", Unit: "bytes"},
			{Name: "github_tool.rate_limit_errors", Type: "counter",
				Help: "GitHub 429 / secondary rate limit responses."},
			{Name: "github_tool.review_posts", Type: "counter",
				Help: "Review posting outcomes.", Labels: []string{"event", "outcome"}},
		},
	})
}

func (t *GitHubTool) registerCapabilities() {
	t.RegisterCapability(core.Capability{
		Name: "get_pr_bundle",
		Description: "Fetch GitHub PR metadata and changed files; stores large patches/files as artifacts and returns a compact manifest. " +
			"Required: owner, repo, pull_number. " +
			"Optional: include_existing_comments (boolean, default false), include_file_contents (boolean, default false).",
		Endpoint: "/api/capabilities/get_pr_bundle",
		Handler:  t.handleGetPRBundle,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "owner", Type: "string", Example: "acme", Description: "Repository owner or organization."},
				{Name: "repo", Type: "string", Example: "payments", Description: "Repository name."},
				{Name: "pull_number", Type: "number", Example: "42", Description: "Pull request number."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "include_existing_comments", Type: "boolean", Example: "true", Description: "Return prior review and issue comments."},
				{Name: "include_file_contents", Type: "boolean", Example: "false", Description: "Also store full file contents as artifacts, not just patches."},
			},
		},
		// Field names mirror the JSON tags on PRBundleManifest in types.go.
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "bundle_id", Type: "string", Description: "Opaque handle for subsequent artifact/context calls."},
				{Name: "owner", Type: "string"},
				{Name: "repo", Type: "string"},
				{Name: "pull_number", Type: "number"},
				{Name: "base_sha", Type: "string"},
				{Name: "head_sha", Type: "string"},
				{Name: "title", Type: "string", Description: "PR title."},
				{Name: "author", Type: "string", Description: "PR author login."},
				{Name: "changed_files", Type: "array", Description: "ChangedFileEntry[] with artifact IDs, not inline patches."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "comments", Type: "array", Description: "Present when include_existing_comments=true."},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "get_file_context",
		Description: "Return exact bounded file or patch context for one PR path and line range. " +
			"Required: bundle_id, path, line_start, line_end. " +
			"Optional: context_before, context_after (extra lines around the range).",
		Endpoint: "/api/capabilities/get_file_context",
		Handler:  t.handleGetFileContext,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "bundle_id", Type: "string", Example: "prb_acme_payments_42_abc123", Description: "Handle returned by get_pr_bundle."},
				{Name: "path", Type: "string", Example: "core/auth/session.go", Description: "Path of a file in the PR."},
				{Name: "line_start", Type: "number", Example: "130", Description: "1-indexed start line."},
				{Name: "line_end", Type: "number", Example: "160", Description: "1-indexed end line (inclusive)."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "context_before", Type: "number", Example: "10", Description: "Extra lines before line_start."},
				{Name: "context_after", Type: "number", Example: "10", Description: "Extra lines after line_end."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "bundle_id", Type: "string"},
				{Name: "path", Type: "string"},
				{Name: "line_start", Type: "number"},
				{Name: "line_end", Type: "number"},
				{Name: "content", Type: "string", Description: "Exact bounded source, no summary."},
				{Name: "truncated", Type: "boolean", Description: "True when MaxFileBytes was hit before reaching line_end."},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "get_artifact_slice",
		Description: "Return exact bounded bytes from a stored artifact (patch or file) by byte offset. " +
			"Required: bundle_id, artifact_id, byte_start, byte_limit (capped server-side by GITHUB_TOOL_MAX_SLICE_BYTES).",
		Endpoint: "/api/capabilities/get_artifact_slice",
		Handler:  t.handleGetArtifactSlice,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "bundle_id", Type: "string", Example: "prb_acme_payments_42_abc123", Description: "Handle returned by get_pr_bundle."},
				{Name: "artifact_id", Type: "string", Example: "art_a1b2c3d4", Description: "Artifact reference from a ChangedFileEntry."},
				{Name: "byte_start", Type: "number", Example: "0", Description: "Inclusive start offset (0-indexed)."},
				{Name: "byte_limit", Type: "number", Example: "8192", Description: "Max bytes to return; capped server-side."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "bundle_id", Type: "string"},
				{Name: "artifact_id", Type: "string"},
				{Name: "byte_start", Type: "number"},
				{Name: "byte_end", Type: "number", Description: "Exclusive end offset (= byte_start + len(content))."},
				{Name: "content", Type: "string"},
				{Name: "truncated", Type: "boolean", Description: "True iff more bytes exist past this slice."},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "list_existing_review_comments",
		Description: "Return existing review and issue comments on a PR so callers can avoid duplicate findings. " +
			"Required: owner, repo, pull_number.",
		Endpoint: "/api/capabilities/list_existing_review_comments",
		Handler:  t.handleListExistingReviewComments,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "owner", Type: "string", Example: "acme", Description: "Repository owner or organization."},
				{Name: "repo", Type: "string", Example: "payments", Description: "Repository name."},
				{Name: "pull_number", Type: "number", Example: "42", Description: "Pull request number."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "comments", Type: "array", Description: "ExistingComment[] with id, path, line, body, user."},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "create_pr_review",
		Description: "Post a grouped GitHub PR review with inline comments for a specific commit ID. " +
			"Accepts only COMMENT or REQUEST_CHANGES in the MVP — APPROVE is rejected at the tool boundary. " +
			"Required: owner, repo, pull_number, commit_id, event (COMMENT or REQUEST_CHANGES), body. " +
			"Optional: comments (array), dry_run (boolean, default false).",
		Endpoint: "/api/capabilities/create_pr_review",
		Handler:  t.handleCreatePRReview,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "owner", Type: "string", Example: "acme", Description: "Repository owner or organization."},
				{Name: "repo", Type: "string", Example: "payments", Description: "Repository name."},
				{Name: "pull_number", Type: "number", Example: "42", Description: "Pull request number."},
				{Name: "commit_id", Type: "string", Example: "abc123def456", Description: "Head SHA the review is grounded against."},
				{Name: "event", Type: "string", Example: "COMMENT", Description: "COMMENT or REQUEST_CHANGES. APPROVE is rejected at the tool boundary."},
				{Name: "body", Type: "string", Example: "Found 2 blocking issues and 3 warnings.", Description: "Summary body for the review."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "comments", Type: "array", Description: "Inline comments with path/line/side/body."},
				{Name: "dry_run", Type: "boolean", Example: "true", Description: "If true, validate and return a simulated response without calling GitHub."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "id", Type: "number", Description: "GitHub review ID (0 in dry_run mode)."},
				{Name: "html_url", Type: "string", Description: "Browser URL for the posted review (empty in dry_run mode)."},
				{Name: "state", Type: "string", Description: "Review state from GitHub; \"DRY_RUN\" when dry_run=true."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "dry_run", Type: "boolean", Description: "Echoes dry_run=true when no GitHub write was made."},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "create_issue_comment",
		Description: "Post a top-level (non-inline) PR comment as a fallback when inline positioning is not viable. " +
			"Required: owner, repo, pull_number, body. " +
			"Optional: dry_run (boolean, default false).",
		Endpoint: "/api/capabilities/create_issue_comment",
		Handler:  t.handleCreateIssueComment,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "owner", Type: "string", Example: "acme", Description: "Repository owner or organization."},
				{Name: "repo", Type: "string", Example: "payments", Description: "Repository name."},
				{Name: "pull_number", Type: "number", Example: "42", Description: "Pull request number."},
				{Name: "body", Type: "string", Example: "PR review summary: 2 blocking, 3 warnings.", Description: "Comment body (Markdown supported)."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "dry_run", Type: "boolean", Example: "true", Description: "If true, validate and return a simulated response without calling GitHub."},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "id", Type: "number", Description: "GitHub comment ID (0 in dry_run mode)."},
				{Name: "html_url", Type: "string", Description: "Browser URL for the posted comment (empty in dry_run mode)."},
			},
			OptionalFields: []core.FieldHint{
				{Name: "dry_run", Type: "boolean"},
			},
		},
	})
}
