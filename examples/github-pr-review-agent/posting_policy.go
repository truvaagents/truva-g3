package main

import (
	"context"
	"fmt"

	"github.com/truvaagents/truva-g3/telemetry"
)

// ShouldPostReview applies all deterministic gates before posting. Gates are
// ordered so the cheapest checks run first. The last check (throttle) acquires
// a Redis SETNX with TTL; once acquired, the caller commits to posting.
func (a *PRReviewAgent) ShouldPostReview(
	ctx context.Context,
	input ReviewPRInput,
	bundle PRBundleManifest,
	result ReviewTaskResult,
) bool {
	// Global kill-switch short-circuits all writes regardless of other config.
	if a.Config.PostingDisabled {
		return false
	}
	if a.Config.DryRun {
		return false
	}
	if !input.PostReview {
		return false
	}
	if !a.Config.AllowedRepos.Contains(input.Owner + "/" + input.Repo) {
		return false
	}
	if bundle.HeadSHA == "" || result.HeadSHA != bundle.HeadSHA {
		return false
	}
	if result.Decision == "REQUEST_CHANGES" && !a.Config.AllowRequestChanges {
		return false
	}
	if result.Decision == "REQUEST_CHANGES" && MaxConfidence(result.Findings) < a.Config.RequestChangesMinConfidence {
		return false
	}
	if !AllFindingsHaveValidDiffPositions(result.Findings) {
		return false
	}
	// Throttle: at most one posted review per (owner, repo, head SHA) per
	// configured interval. acquirePostThrottle uses Redis SETNX with TTL.
	return a.acquirePostThrottle(ctx, input.Owner, input.Repo, bundle.HeadSHA)
}

func (a *PRReviewAgent) acquirePostThrottle(ctx context.Context, owner, repo, headSHA string) bool {
	if a.RedisClient == nil {
		// Local dev without Redis: don't silently gate posting.
		return true
	}
	key := postThrottleKey(owner, repo, headSHA)
	ok, err := a.RedisClient.SetNX(ctx, key, "1", a.Config.PostMinInterval).Result()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.WarnWithContext(ctx, "review_pr: post throttle check failed; denying post", map[string]interface{}{
			"operation":  "review_pr",
			"error_type": "redis_error",
			"error":      err.Error(),
			"owner":      owner,
			"repo":       repo,
			"head_sha":   headSHA,
		})
		return false
	}
	return ok
}

// releasePostThrottle deletes the throttle key for (owner, repo, headSHA),
// allowing a retry of a failed PostReview to proceed instead of being blocked
// for the full PostMinInterval. Caller should invoke this only when posting
// was attempted and failed — successful posts must keep the slot consumed so
// repeat-post protection still works.
func (a *PRReviewAgent) releasePostThrottle(ctx context.Context, owner, repo, headSHA string) {
	if a.RedisClient == nil {
		return
	}
	key := postThrottleKey(owner, repo, headSHA)
	if err := a.RedisClient.Del(ctx, key).Err(); err != nil {
		a.Logger.WarnWithContext(ctx, "review_pr: post throttle release failed; key will TTL out", map[string]interface{}{
			"operation":  "review_pr",
			"error_type": "redis_error",
			"error":      err.Error(),
			"owner":      owner,
			"repo":       repo,
			"head_sha":   headSHA,
		})
	}
}

func postThrottleKey(owner, repo, headSHA string) string {
	return fmt.Sprintf("github-pr-review:post-throttle:%s/%s:%s", owner, repo, headSHA)
}

// PostReview calls github-tool.create_pr_review. APPROVE is rejected at the
// tool boundary regardless of what this agent sends.
func (a *PRReviewAgent) PostReview(
	ctx context.Context,
	input ReviewPRInput,
	bundle PRBundleManifest,
	result ReviewTaskResult,
) (string, error) {
	resp, err := a.ToolClient.CreatePRReview(ctx, CreatePRReviewRequest{
		Owner:      input.Owner,
		Repo:       input.Repo,
		PullNumber: input.PullNumber,
		CommitID:   bundle.HeadSHA,
		Event:      result.Decision,
		Body:       result.Summary,
		Comments:   BuildGitHubReviewComments(result.Findings),
	})
	if err != nil {
		return "", err
	}
	return resp.HTMLURL, nil
}

func MaxConfidence(findings []ReviewFinding) float64 {
	m := 0.0
	for _, f := range findings {
		if f.Confidence > m {
			m = f.Confidence
		}
	}
	return m
}

// EventConfidence returns the confidence value to attach to a shared-memory
// review event. For empty findings (clean review), 1.0 — high confidence in
// "nothing to flag." For non-empty, the highest per-finding confidence.
//
// Distinct from MaxConfidence because the posting gate semantics ("can a
// REQUEST_CHANGES proceed?") and the observability semantics ("how confident
// were we?") differ on the empty case: the gate must reject empty
// REQUEST_CHANGES, but operators reading the event should see "yes, we
// looked and found nothing."
func EventConfidence(findings []ReviewFinding) float64 {
	if len(findings) == 0 {
		return 1.0
	}
	return MaxConfidence(findings)
}

// AllFindingsHaveValidDiffPositions returns true iff every finding has a
// non-empty path, a positive line, and a side of LEFT or RIGHT. Evidence
// verification is responsible for making the line values match current diff
// positions; this check just guards against obviously-malformed entries.
func AllFindingsHaveValidDiffPositions(findings []ReviewFinding) bool {
	for _, f := range findings {
		if f.Path == "" || f.Line <= 0 {
			return false
		}
		if f.Side != "LEFT" && f.Side != "RIGHT" {
			return false
		}
	}
	return true
}

func BuildGitHubReviewComments(findings []ReviewFinding) []GitHubReviewComment {
	out := make([]GitHubReviewComment, 0, len(findings))
	for _, f := range findings {
		out = append(out, GitHubReviewComment{
			Path: f.Path,
			Line: f.Line,
			Side: f.Side,
			Body: formatFindingBody(f),
		})
	}
	return out
}

func formatFindingBody(f ReviewFinding) string {
	// Single-line header + evidence/suggestion stacked below.
	// Keep it terse — GitHub review comments are scanned, not read.
	s := fmt.Sprintf("**%s** — %s", severityLabel(f.Severity), f.Claim)
	if f.Evidence != "" {
		s += "\n\n" + "_Evidence:_ " + f.Evidence
	}
	if f.Suggestion != "" {
		s += "\n\n" + "_Suggestion:_ " + f.Suggestion
	}
	return s
}

func severityLabel(s string) string {
	switch s {
	case "blocking":
		return "Blocking"
	case "warning":
		return "Warning"
	case "info":
		return "Info"
	default:
		return s
	}
}
