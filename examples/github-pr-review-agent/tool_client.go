package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

// GitHubToolClient calls the github-tool via Truva-G3 service discovery.
// Discovery is resolved at call time because BaseAgent.Discovery is nil
// until framework.Run() populates it.
type GitHubToolClient struct {
	HTTPClient *http.Client
	Agent      *PRReviewAgent
}

func NewGitHubToolClient(httpClient *http.Client, agent *PRReviewAgent) *GitHubToolClient {
	return &GitHubToolClient{HTTPClient: httpClient, Agent: agent}
}

// --- request/response shapes mirroring github-tool capability schemas ---

type GetPRBundleRequest struct {
	Owner                   string `json:"owner"`
	Repo                    string `json:"repo"`
	PullNumber              int    `json:"pull_number"`
	IncludeExistingComments bool   `json:"include_existing_comments,omitempty"`
	IncludeFileContents     bool   `json:"include_file_contents,omitempty"`
}

type GetFileContextRequest struct {
	BundleID      string `json:"bundle_id"`
	Path          string `json:"path"`
	LineStart     int    `json:"line_start"`
	LineEnd       int    `json:"line_end"`
	ContextBefore int    `json:"context_before,omitempty"`
	ContextAfter  int    `json:"context_after,omitempty"`
}

type GetFileContextResponse struct {
	BundleID  string `json:"bundle_id"`
	Path      string `json:"path"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type GetArtifactSliceRequest struct {
	BundleID   string `json:"bundle_id"`
	ArtifactID string `json:"artifact_id"`
	ByteStart  int64  `json:"byte_start"`
	ByteLimit  int64  `json:"byte_limit"`
}

type GetArtifactSliceResponse struct {
	BundleID   string `json:"bundle_id"`
	ArtifactID string `json:"artifact_id"`
	ByteStart  int64  `json:"byte_start"`
	ByteEnd    int64  `json:"byte_end"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}

type CreatePRReviewRequest struct {
	Owner      string                `json:"owner"`
	Repo       string                `json:"repo"`
	PullNumber int                   `json:"pull_number"`
	CommitID   string                `json:"commit_id"`
	Event      string                `json:"event"`
	Body       string                `json:"body"`
	Comments   []GitHubReviewComment `json:"comments,omitempty"`
	DryRun     bool                  `json:"dry_run,omitempty"`
}

type GitHubReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

type CreatePRReviewResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// --- typed call methods ---

func (c *GitHubToolClient) GetPRBundle(ctx context.Context, req GetPRBundleRequest) (PRBundleManifest, error) {
	var out PRBundleManifest
	if err := c.callCapability(ctx, "get_pr_bundle", req, &out); err != nil {
		return PRBundleManifest{}, err
	}
	return out, nil
}

func (c *GitHubToolClient) GetFileContext(ctx context.Context, req GetFileContextRequest) (GetFileContextResponse, error) {
	var out GetFileContextResponse
	if err := c.callCapability(ctx, "get_file_context", req, &out); err != nil {
		return GetFileContextResponse{}, err
	}
	return out, nil
}

func (c *GitHubToolClient) GetArtifactSlice(ctx context.Context, req GetArtifactSliceRequest) (GetArtifactSliceResponse, error) {
	var out GetArtifactSliceResponse
	if err := c.callCapability(ctx, "get_artifact_slice", req, &out); err != nil {
		return GetArtifactSliceResponse{}, err
	}
	return out, nil
}

// shardContextByteLimit caps how many bytes of a single file's patch the
// agent fetches for the shard review prompt. Matches github-tool's default
// MaxSliceBytes (128 KiB). Larger files get truncated; the AI still reviews
// what we got.
const shardContextByteLimit = 128 * 1024

// GetShardContext concatenates the unified-diff patch for every file in the
// shard by issuing one get_artifact_slice call per file (using the
// PatchArtifactID from the bundle manifest). The previous implementation used
// get_file_context with an unbounded LineEnd which the tool always rejected
// with RANGE_TOO_LARGE — see the developer feedback that surfaced that bug.
func (c *GitHubToolClient) GetShardContext(ctx context.Context, bundleID string, shard ReviewShard) (string, error) {
	var b bytes.Buffer
	for _, f := range shard.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		// Files without a patch artifact (binary, removed-with-no-patch,
		// over-the-MaxPatchBytes-cap during bundle build) have nothing
		// reviewable. Skip them so they don't trigger noisy not-found errors.
		if f.PatchArtifactID == "" {
			fmt.Fprintf(&b, "=== %s (%s, +%d -%d, no patch artifact — skipped) ===\n\n",
				f.Path, f.Status, f.Additions, f.Deletions)
			continue
		}
		resp, err := c.GetArtifactSlice(ctx, GetArtifactSliceRequest{
			BundleID:   bundleID,
			ArtifactID: f.PatchArtifactID,
			ByteStart:  0,
			ByteLimit:  shardContextByteLimit,
		})
		if err != nil {
			return "", fmt.Errorf("get_artifact_slice %s: %w", f.Path, err)
		}
		fmt.Fprintf(&b, "=== %s (%s, +%d -%d", f.Path, f.Status, f.Additions, f.Deletions)
		if resp.Truncated {
			b.WriteString(", truncated")
		}
		b.WriteString(") ===\n")
		b.WriteString(resp.Content)
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

func (c *GitHubToolClient) CreatePRReview(ctx context.Context, req CreatePRReviewRequest) (CreatePRReviewResponse, error) {
	var out CreatePRReviewResponse
	if err := c.callCapability(ctx, "create_pr_review", req, &out); err != nil {
		return CreatePRReviewResponse{}, err
	}
	return out, nil
}

// --- transport ---

// callCapability resolves github-tool via discovery, looks up the capability
// endpoint on the resolved ServiceInfo, and POSTs the input. The github-tool
// wraps responses in core.ToolResponse{Success, Data, Error}. Every error
// return is recorded on the active span via the deferred recorder so callers
// don't need to wrap individually.
func (c *GitHubToolClient) callCapability(ctx context.Context, capability string, input interface{}, output interface{}) (err error) {
	defer func() {
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
		}
	}()

	if c.Agent == nil || c.Agent.Discovery == nil {
		return fmt.Errorf("discovery not ready; framework.Run() has not populated Discovery yet")
	}

	services, err := c.Agent.Discovery.Discover(ctx, core.DiscoveryFilter{Name: "github-tool"})
	if err != nil {
		return fmt.Errorf("discover github-tool: %w", err)
	}
	svc := pickHealthyService(services)
	if svc == nil {
		return fmt.Errorf("no healthy github-tool instances registered")
	}

	endpoint := capabilityEndpoint(svc, capability)
	if endpoint == "" {
		return fmt.Errorf("github-tool has no capability %q", capability)
	}

	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal input: %w", err)
	}
	url := fmt.Sprintf("http://%s:%d%s", svc.Address, svc.Port, endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("call github-tool.%s: %w", capability, err)
	}
	defer func() { _ = resp.Body.Close() }()

	wrapper := struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *core.ToolError `json:"error"`
	}{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024*1024)).Decode(&wrapper); err != nil {
		return fmt.Errorf("decode github-tool response: %w", err)
	}
	if !wrapper.Success {
		if wrapper.Error != nil {
			return fmt.Errorf("github-tool.%s failed [%s]: %s", capability, wrapper.Error.Code, wrapper.Error.Message)
		}
		return fmt.Errorf("github-tool.%s failed (status %d)", capability, resp.StatusCode)
	}
	if output != nil && len(wrapper.Data) > 0 {
		if err := json.Unmarshal(wrapper.Data, output); err != nil {
			return fmt.Errorf("decode github-tool data: %w", err)
		}
	}
	return nil
}

func pickHealthyService(services []*core.ServiceInfo) *core.ServiceInfo {
	// Prefer explicitly healthy. If discovery doesn't track health, accept
	// the first instance to keep the integration usable in dev.
	for _, s := range services {
		if s != nil && s.Health == core.HealthHealthy {
			return s
		}
	}
	for _, s := range services {
		if s != nil && s.Health != core.HealthUnhealthy {
			return s
		}
	}
	return nil
}

func capabilityEndpoint(svc *core.ServiceInfo, name string) string {
	for _, cap := range svc.Capabilities {
		if cap.Name == name {
			return cap.Endpoint
		}
	}
	return ""
}
