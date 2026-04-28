package main

// ReviewPRInput is the shape of the task.Input map for "review_pr" tasks.
type ReviewPRInput struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	PullNumber  int    `json:"pull_number"`
	HeadSHA     string `json:"head_sha,omitempty"`
	PostReview  bool   `json:"post_review"`
	ReviewDepth string `json:"review_depth,omitempty"`
}

type ReviewFinding struct {
	Severity   string  `json:"severity"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Side       string  `json:"side"`
	Claim      string  `json:"claim"`
	Evidence   string  `json:"evidence"`
	Suggestion string  `json:"suggestion"`
	Confidence float64 `json:"confidence"`
}

type SkippedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type ReviewTaskResult struct {
	Status     string `json:"status"` // task lifecycle: "completed" | "stub" | error string
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	PullNumber int    `json:"pull_number"`
	HeadSHA    string `json:"head_sha"`
	Decision   string `json:"decision"` // COMMENT | REQUEST_CHANGES
	Summary    string `json:"summary"`
	// PostStatus distinguishes the three terminal posting outcomes so
	// operators don't have to infer from an empty GitHubReviewURL whether
	// posting was skipped by policy or attempted-and-failed:
	//   "skipped" — gates blocked posting (dry-run, kill-switch, throttle,
	//               policy mismatch, etc.); GitHub was not contacted.
	//   "posted"  — review successfully created on GitHub; URL is set.
	//   "failed"  — posting was attempted but failed; URL is empty.
	PostStatus      string          `json:"post_status"`
	Findings        []ReviewFinding `json:"findings"`
	SkippedFiles    []SkippedFile   `json:"skipped_files,omitempty"`
	GitHubReviewURL string          `json:"github_review_url,omitempty"`
}

// PRBundleManifest mirrors the github-tool get_pr_bundle response.
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

// ReviewShard is a worker-local planning unit.
type ReviewShard struct {
	Description   string
	Files         []ChangedFileEntry
	TokenEstimate int
}
