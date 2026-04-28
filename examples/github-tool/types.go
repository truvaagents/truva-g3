package main

// --- Manifest types returned to the agent ---
//
// These mirror the types declared in the agent at examples/github-pr-review-agent/types.go.
// The two packages are independent; only the JSON wire shape must agree.

type PRBundleManifest struct {
	BundleID     string             `json:"bundle_id"`
	Owner        string             `json:"owner"`
	Repo         string             `json:"repo"`
	PullNumber   int                `json:"pull_number"`
	BaseSHA      string             `json:"base_sha"`
	HeadSHA      string             `json:"head_sha"`
	Title        string             `json:"title"`
	Author       string             `json:"author"`
	ChangedFiles []ChangedFileEntry `json:"changed_files"`
	Comments     []ExistingComment  `json:"comments,omitempty"`
}

type ChangedFileEntry struct {
	Path            string   `json:"path"`
	Status          string   `json:"status"`
	Additions       int      `json:"additions"`
	Deletions       int      `json:"deletions"`
	PatchArtifactID string   `json:"patch_artifact_id,omitempty"`
	FileArtifactID  string   `json:"file_artifact_id,omitempty"`
	IsGenerated     bool     `json:"is_generated"`
	IsLockfile      bool     `json:"is_lockfile"`
	RiskHints       []string `json:"risk_hints,omitempty"`
}

type ExistingComment struct {
	ID   int64  `json:"id"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	Body string `json:"body"`
	User string `json:"user"`
}

type ArtifactRef struct {
	ID        string `json:"id"`
	Backend   string `json:"backend"`
	SizeBytes int64  `json:"size_bytes"`
	MediaType string `json:"media_type"`
}

// --- Capability request/response shapes ---

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

type ListExistingReviewCommentsRequest struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	PullNumber int    `json:"pull_number"`
}

type ListExistingReviewCommentsResponse struct {
	Comments []ExistingComment `json:"comments"`
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

type CreateIssueCommentRequest struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	PullNumber int    `json:"pull_number"`
	Body       string `json:"body"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

type CreateIssueCommentResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	DryRun  bool   `json:"dry_run,omitempty"`
}

// --- Raw GitHub REST API response shapes (private to the package) ---

type githubPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
	RawURL    string `json:"raw_url"`
}

type githubReviewComment struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubIssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type githubReviewResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
}

type githubIssueCommentResponse struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
}
