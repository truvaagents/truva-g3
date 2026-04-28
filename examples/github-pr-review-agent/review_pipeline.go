package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// HandlePullRequestReview is the TaskHandler registered for "review_pr" tasks.
// It runs a deterministic pipeline: fetch bundle -> plan shards -> review
// shards (bounded parallelism, exact code) -> merge -> verify evidence ->
// post if policy allows.
func (a *PRReviewAgent) HandlePullRequestReview(
	ctx context.Context,
	task *core.Task,
	reporter core.ProgressReporter,
) (handlerErr error) {
	taskStart := time.Now()
	const op = "review_pr"

	input, err := ParseReviewPRInput(task.Input)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("github_pr_review.tasks_processed", "status", "invalid_input", "decision", "")
		return err
	}

	// Pull request_id forward from the webhook's task.Input so worker logs
	// stitch back to the originating HTTP request. Falls back to task.ID for
	// purely-internal task submissions.
	reqID, _ := task.Input["request_id"].(string)
	if reqID == "" {
		reqID = task.ID
	}

	// Restore the trace context that the webhook captured into the task so
	// the worker's consumer span links back to the originating HTTP request.
	ctx, endSpan := telemetry.StartLinkedSpanWithOptions(
		ctx,
		"review.pull_request",
		task.TraceID,
		task.ParentSpanID,
		map[string]string{
			"task.id":   task.ID,
			"link.type": "github_pr_review_task",
		},
		trace.SpanKindConsumer,
	)
	defer endSpan()

	// Enrich the consumer span with searchable business + framework attributes.
	// Setting these via SetSpanAttributes (rather than the link's static map)
	// lets Jaeger search by exact key like `github.repo:payments`.
	telemetry.SetSpanAttributes(ctx,
		attribute.String("truvag3.agent.name", "github-pr-review-agent"),
		attribute.String("truvag3.capability", op),
		attribute.String("request_id", reqID),
		attribute.String("github.owner", input.Owner),
		attribute.String("github.repo", input.Repo),
		attribute.Int("github.pull_number", input.PullNumber),
		attribute.String("github.head_sha", input.HeadSHA),
	)

	a.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":   op,
		"task_id":     task.ID,
		"owner":       input.Owner,
		"repo":        input.Repo,
		"pull_number": input.PullNumber,
		"head_sha":    input.HeadSHA,
		"request_id":  reqID,
	})

	// Emit task duration on every exit path. Decision label is filled in via
	// the result on success; left empty on early-error paths.
	defer func() {
		telemetry.Histogram("github_pr_review.task_duration_ms", float64(time.Since(taskStart).Milliseconds()))
		if handlerErr != nil {
			telemetry.Counter("github_pr_review.tasks_processed", "status", "failed", "decision", "")
		}
	}()

	report(reporter, 1, 8, 5, "Fetching PR bundle", "Loading pull request metadata and changed files")

	bundle, err := a.ToolClient.GetPRBundle(ctx, GetPRBundleRequest{
		Owner:                   input.Owner,
		Repo:                    input.Repo,
		PullNumber:              input.PullNumber,
		IncludeExistingComments: true,
		// REQUIRED for evidence verification to work correctly. Without this,
		// github-tool stores only patch artifacts; verifyEvidence then asks
		// for source-file lines but get_file_context falls back to extracting
		// "lines" from the patch text, where line numbers don't match source
		// line numbers. Findings then almost always fail grounding and get
		// dropped as ungrounded_evidence — the review reports "no issues"
		// with real findings discarded.
		IncludeFileContents: true,
	})
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": get_pr_bundle failed", map[string]interface{}{
			"operation":  op,
			"error_type": "tool_call_error",
			"error":      err.Error(),
			"owner":      input.Owner,
			"repo":       input.Repo,
			"pr":         input.PullNumber,
			"request_id": reqID,
		})
		return fmt.Errorf("get_pr_bundle: %w", err)
	}

	// Stale-task check: if the caller knew a specific head SHA and it has
	// since moved, abort early — the findings would reference stale lines.
	if input.HeadSHA != "" && input.HeadSHA != bundle.HeadSHA {
		err := fmt.Errorf("stale task: input head_sha %s != current head_sha %s", input.HeadSHA, bundle.HeadSHA)
		telemetry.RecordSpanError(ctx, err)
		a.Logger.WarnWithContext(ctx, op+": stale head SHA; aborting", map[string]interface{}{
			"operation":       op,
			"error_type":      "stale_head_sha",
			"input_head_sha":  input.HeadSHA,
			"bundle_head_sha": bundle.HeadSHA,
			"request_id":      reqID,
		})
		return err
	}

	report(reporter, 2, 8, 15, "Planning review shards", "Grouping changed files into token-bounded shards")
	plan, skipped, err := a.PlanShards(ctx, bundle)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": plan_shards failed", map[string]interface{}{
			"operation":  op,
			"error_type": "plan_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		return err
	}
	for _, sf := range skipped {
		telemetry.Counter("github_pr_review.skipped_files", "reason", sf.Reason)
	}

	report(reporter, 3, 8, 20, "Reviewing shards", fmt.Sprintf("Reviewing %d shard(s)", len(plan)))
	shardFindings, shardSummary, err := a.reviewShards(ctx, bundle, plan, reporter, reqID)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": shard review aborted", map[string]interface{}{
			"operation":  op,
			"error_type": "shard_review_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		return err
	}
	// Total-failure guard: when every attempted shard failed (context fetch,
	// AI call, etc.), the result would otherwise be an empty findings list
	// indistinguishable from a clean review. Fail loudly instead so operators
	// see a "failed" task in metrics + status, not a misleading "no issues".
	// Empty plan (zero shards attempted) is fine — handled below as
	// "no reviewable files".
	if shardSummary.Total > 0 && shardSummary.Succeeded == 0 {
		err := fmt.Errorf("all %d shard(s) failed to review (first error: %s)",
			shardSummary.Total, firstError(shardSummary.Errors))
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": all shards failed; failing task", map[string]interface{}{
			"operation":   op,
			"error_type":  "shard_total_failure",
			"shards":      shardSummary.Total,
			"first_error": firstError(shardSummary.Errors),
			"request_id":  reqID,
		})
		return err
	}

	report(reporter, 6, 8, 88, "Merging findings", "Deduplicating across shards")
	merged := MergeAndDedupeFindings(shardFindings)
	for _, f := range merged {
		telemetry.Counter("github_pr_review.findings", "severity", f.Severity, "stage", "merged")
	}

	report(reporter, 7, 8, 94, "Verifying evidence", "Re-fetching exact code for each finding")
	verified, err := a.verifyEvidence(ctx, bundle, merged)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": evidence verification failed", map[string]interface{}{
			"operation":  op,
			"error_type": "verify_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		return err
	}
	for _, f := range verified {
		telemetry.Counter("github_pr_review.findings", "severity", f.Severity, "stage", "verified")
	}

	result := a.buildResult(input, bundle, verified, skipped)

	// Surface partial shard failure in the result. We don't fail the task
	// (some findings did come back), but operators reading status need to see
	// "this review only covered M/N shards" so they don't trust it as fully
	// complete. The total-failure case (M=0) was already returned above.
	if shardSummary.Failed > 0 {
		result.Status = "partial"
		result.Summary = fmt.Sprintf("PARTIAL: %d/%d shard(s) failed. %s",
			shardSummary.Failed, shardSummary.Total, result.Summary)
	}

	// Default: posting was skipped by policy. Overwritten below when posting
	// is attempted, regardless of the post outcome.
	result.PostStatus = "skipped"
	if a.ShouldPostReview(ctx, input, bundle, result) {
		url, postErr := a.PostReview(ctx, input, bundle, result)
		if postErr != nil {
			// Release the throttle so a legitimate retry of this same head
			// SHA isn't blocked for the full PostMinInterval. Successful
			// posts (else branch) keep the slot consumed.
			a.releasePostThrottle(ctx, input.Owner, input.Repo, bundle.HeadSHA)

			telemetry.RecordSpanError(ctx, postErr)
			telemetry.Counter("github_pr_review.posts_attempted", "outcome", "failed", "decision", result.Decision)
			a.Logger.ErrorWithContext(ctx, op+": post_review failed", map[string]interface{}{
				"operation":  op,
				"error_type": "post_error",
				"error":      postErr.Error(),
				"owner":      input.Owner,
				"repo":       input.Repo,
				"pr":         input.PullNumber,
				"request_id": reqID,
			})
			result.PostStatus = "failed"
			// Don't fail the task — the review result is still useful.
		} else {
			result.GitHubReviewURL = url
			result.PostStatus = "posted"
			telemetry.Counter("github_pr_review.posts_attempted", "outcome", "posted", "decision", result.Decision)
			a.Logger.InfoWithContext(ctx, op+": review posted", map[string]interface{}{
				"operation":  op,
				"url":        url,
				"decision":   result.Decision,
				"findings":   len(result.Findings),
				"request_id": reqID,
			})
		}
	} else {
		telemetry.Counter("github_pr_review.posts_attempted", "outcome", "skipped", "decision", result.Decision)
	}

	task.Result = result
	telemetry.Counter("github_pr_review.tasks_processed", "status", "completed", "decision", result.Decision)

	// Emit a shared-domain episodic event so other agents in the same
	// "infrastructure" domain (devops-chat-agent, event-driven-agent) and
	// the registry viewer can see this review in their planning context
	// without a custom history API. Best-effort — failures don't fail the
	// task. See AGENT_PLAN.md "Shared Agent Memory" for the design rationale.
	if a.Episodic != nil {
		summary := fmt.Sprintf("PR review %s on %s/%s#%d (%d findings)",
			result.Decision, input.Owner, input.Repo, input.PullNumber,
			len(result.Findings))
		tc := telemetry.GetTraceContext(ctx)
		evtErr := a.Episodic.RecordEvent(ctx, core.AgentEvent{
			AgentName:   "github-pr-review-agent",
			AgentDomain: "infrastructure",
			ActionType:  "review_completed",
			EntityType:  "pull_request",
			EntityID:    fmt.Sprintf("%s/%s#%d", input.Owner, input.Repo, input.PullNumber),
			Summary:     summary,
			Outcome:     "success",
			// EventConfidence (not MaxConfidence) so clean reviews report
			// 1.0 ("high confidence in nothing-to-flag") instead of 0.0
			// ("no high-confidence finding") in the registry / chat agent.
			Confidence: EventConfidence(result.Findings),
			TraceID:    tc.TraceID,
			RequestID:  reqID,
			Scope:      core.ScopeSharedDomain,
			Metadata: map[string]string{
				"decision":    result.Decision,   // COMMENT or REQUEST_CHANGES
				"post_status": result.PostStatus, // posted | skipped | failed
				"head_sha":    bundle.HeadSHA,
				"findings":    strconv.Itoa(len(result.Findings)),
				"skipped":     strconv.Itoa(len(result.SkippedFiles)),
				"github_url":  result.GitHubReviewURL,
				"task_id":     task.ID,
			},
		})
		if evtErr != nil {
			a.Logger.WarnWithContext(ctx, op+": episodic.RecordEvent failed; review still completed", map[string]interface{}{
				"operation":  op,
				"error_type": "memory_write_error",
				"error":      evtErr.Error(),
				"request_id": reqID,
			})
		}
	}

	a.Logger.InfoWithContext(ctx, op+": completed", map[string]interface{}{
		"operation":   op,
		"decision":    result.Decision,
		"findings":    len(result.Findings),
		"skipped":     len(result.SkippedFiles),
		"duration_ms": time.Since(taskStart).Milliseconds(),
		"request_id":  reqID,
	})
	report(reporter, 8, 8, 100, "Complete", "Pull request review complete")
	return nil
}

// shardReviewSummary is what reviewShards returns alongside the aggregate
// findings. It's how the caller distinguishes "all shards reviewed cleanly,
// no findings" from "every shard failed to even fetch context, no findings".
// Total includes only shards we attempted (excludes upfront ctx-cancellation).
type shardReviewSummary struct {
	Total     int      // shards we attempted to review
	Succeeded int      // shards that returned (possibly empty) findings
	Failed    int      // shards whose goroutine returned an error
	Errors    []string // first-line error message per failed shard, for surfacing in result.Summary
}

// firstError returns the first entry of errs or an empty string. Used to
// embed a representative error in log fields and the failure error message
// without dumping the whole list.
func firstError(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	return errs[0]
}

func (a *PRReviewAgent) reviewShards(
	ctx context.Context,
	bundle PRBundleManifest,
	plan []ReviewShard,
	reporter core.ProgressReporter,
	reqID string,
) ([]ReviewFinding, shardReviewSummary, error) {
	if len(plan) == 0 {
		return nil, shardReviewSummary{}, nil
	}

	maxParallel := a.Config.MaxParallelShards
	if maxParallel < 1 {
		maxParallel = 1
	}

	sem := make(chan struct{}, maxParallel)
	type shardResult struct {
		findings []ReviewFinding
		err      error
	}
	results := make([]shardResult, len(plan))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var completed int

	for i, shard := range plan {
		if err := ctx.Err(); err != nil {
			return nil, shardReviewSummary{}, err
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, sh ReviewShard) {
			defer wg.Done()
			defer func() { <-sem }()

			// Re-check cancellation between every blocking step inside the
			// goroutine — the outer loop's check only catches cancellation
			// before the goroutine started.
			if err := ctx.Err(); err != nil {
				results[idx] = shardResult{err: err}
				return
			}
			exact, err := a.ToolClient.GetShardContext(ctx, bundle.BundleID, sh)
			if err != nil {
				results[idx] = shardResult{err: fmt.Errorf("shard %d context: %w", idx, err)}
				return
			}
			if err := ctx.Err(); err != nil {
				results[idx] = shardResult{err: err}
				return
			}

			prompt := BuildShardReviewPrompt(bundle, sh, exact)
			findings, err := a.callReviewModel(ctx, prompt, reqID)
			if err != nil {
				results[idx] = shardResult{err: fmt.Errorf("shard %d model: %w", idx, err)}
				return
			}
			results[idx] = shardResult{findings: findings}

			mu.Lock()
			completed++
			percent := 20 + int(float64(completed)/float64(len(plan))*65)
			mu.Unlock()
			report(reporter, 3, 8, percent,
				fmt.Sprintf("Reviewing shard %d/%d", completed, len(plan)),
				sh.Description)
		}(i, shard)
	}
	wg.Wait()

	summary := shardReviewSummary{Total: len(results)}
	var all []ReviewFinding
	for _, r := range results {
		if r.err != nil {
			summary.Failed++
			summary.Errors = append(summary.Errors, r.err.Error())
			telemetry.Counter("github_pr_review.shards_reviewed", "status", "failed")
			a.Logger.WarnWithContext(ctx, "review_pr: shard review failed", map[string]interface{}{
				"operation":  "review_pr",
				"error_type": "shard_error",
				"error":      r.err.Error(),
				"request_id": reqID,
			})
			continue
		}
		summary.Succeeded++
		telemetry.Counter("github_pr_review.shards_reviewed", "status", "ok")
		all = append(all, r.findings...)
	}
	return all, summary, nil
}

func (a *PRReviewAgent) callReviewModel(ctx context.Context, prompt, reqID string) ([]ReviewFinding, error) {
	if a.AI == nil {
		return nil, fmt.Errorf("AI client not configured")
	}
	resp, err := a.AI.GenerateResponse(ctx, prompt, &core.AIOptions{
		Model:          a.Config.ShardModel,
		Temperature:    0.2,
		ResponseFormat: "json",
	})
	if err != nil {
		// Surface provider failures (auth, rate limit, billing) distinctly so
		// operators can alert on them separately from transient shard errors.
		var pe core.ProviderError
		if errors.As(err, &pe) {
			telemetry.RecordSpanError(ctx, err)
			telemetry.Counter("github_pr_review.provider_errors",
				"provider", pe.Provider(),
				"status", fmt.Sprintf("%d", pe.StatusCode()),
				"transient", fmt.Sprintf("%t", pe.IsTransient()),
			)
			a.Logger.ErrorWithContext(ctx, "review_pr: AI provider error", map[string]interface{}{
				"operation":   "review_pr",
				"error_type":  "provider_error",
				"provider":    pe.Provider(),
				"model":       pe.Model(),
				"status_code": pe.StatusCode(),
				"transient":   pe.IsTransient(),
				"retryable":   pe.IsRetryable(),
				"error":       pe.Error(),
				"request_id":  reqID,
			})
			return nil, fmt.Errorf("AI provider %s failed: %w", pe.Provider(), err)
		}
		// Non-provider errors (transport, deadline, parse) — record on span
		// but don't log here (the shard-level loop logs the wrapped error).
		telemetry.RecordSpanError(ctx, err)
		return nil, err
	}
	return parseFindings(resp.Content)
}

// parseFindings decodes the model response into []ReviewFinding. Strips common
// code-fence wrappers and tolerates a top-level object wrapping a "findings"
// array for providers that refuse bare arrays.
func parseFindings(raw string) ([]ReviewFinding, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	// Try bare array first.
	var arr []ReviewFinding
	if err := json.Unmarshal([]byte(s), &arr); err == nil {
		return arr, nil
	}
	// Fallback: {"findings": [...]}
	var wrapper struct {
		Findings []ReviewFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(s), &wrapper); err == nil {
		return wrapper.Findings, nil
	}
	return nil, fmt.Errorf("could not parse model response as findings JSON")
}

// verifyEvidence runs two filters per finding:
//
//  1. Diff-position check: the (Path, Line, Side) must correspond to a line
//     GitHub will accept as the target of an inline review comment — i.e. it
//     must appear in one of the file's diff hunks on the right side. The
//     parser is in diff_positions.go; we cache one patch fetch per file so
//     a 50-finding review against 5 files only fetches 5 patches.
//
//  2. Evidence-grounding check: the finding's quoted evidence must appear in
//     the surrounding source. Catches "line doesn't exist" and "claim
//     invented" failure modes. Independent of the position check (a finding
//     can land on a real diff line but reference code that isn't there).
//
// Both filters drop the finding silently (with a metric); callers see only
// findings that survive both gates.
// patchCacheEntry holds the per-path data verifyEvidence builds lazily:
// the raw patch text (for LEFT-side context extraction) and the parsed set
// of valid (line, side) positions (for diff-position validation).
//
// A nil entry means a negative cache: the patch couldn't be fetched or the
// path isn't in the bundle. Subsequent findings on that path skip without
// re-trying.
type patchCacheEntry struct {
	text      string
	positions map[validPosition]bool
}

func (a *PRReviewAgent) verifyEvidence(
	ctx context.Context,
	bundle PRBundleManifest,
	findings []ReviewFinding,
) ([]ReviewFinding, error) {
	// Map path → PatchArtifactID, so we don't have to scan ChangedFiles per
	// finding when fetching patches for the diff-position cache.
	patchArtifactByPath := make(map[string]string, len(bundle.ChangedFiles))
	// Track which paths have a usable file artifact. RIGHT-side findings need
	// a head-file artifact to get accurate source-line context. LEFT-side
	// findings verify against the patch instead (deleted code lives only
	// there) — so they don't need this.
	hasFileArtifact := make(map[string]bool, len(bundle.ChangedFiles))
	for _, cf := range bundle.ChangedFiles {
		if cf.PatchArtifactID != "" {
			patchArtifactByPath[cf.Path] = cf.PatchArtifactID
		}
		if cf.FileArtifactID != "" {
			hasFileArtifact[cf.Path] = true
		}
	}
	// Per-path cache: patch text + parsed positions. Fetched lazily — empty
	// review = no fetches; many findings on one file = single fetch.
	cacheByPath := make(map[string]*patchCacheEntry)

	verified := make([]ReviewFinding, 0, len(findings))
	for _, f := range findings {
		// Re-check cancellation each iteration — verification fans out one
		// or two bounded HTTP calls per finding and the loop can run for a
		// while on large reviews.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.Path == "" || f.Line <= 0 {
			telemetry.Counter("github_pr_review.findings_dropped", "reason", "missing_path_or_line")
			continue
		}

		// Fetch + parse the file's patch on first encounter.
		entry, cached := cacheByPath[f.Path]
		if !cached {
			artID := patchArtifactByPath[f.Path]
			if artID == "" {
				// Finding references a path the bundle doesn't have a patch
				// for — model invented a path. Drop with a diagnostic metric.
				telemetry.Counter("github_pr_review.findings_dropped", "reason", "unknown_path")
				cacheByPath[f.Path] = nil // negative cache
				continue
			}
			slice, err := a.ToolClient.GetArtifactSlice(ctx, GetArtifactSliceRequest{
				BundleID:   bundle.BundleID,
				ArtifactID: artID,
				ByteStart:  0,
				ByteLimit:  shardContextByteLimit,
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				telemetry.Counter("github_pr_review.findings_dropped", "reason", "patch_fetch_failed")
				cacheByPath[f.Path] = nil // negative cache for this call
				continue
			}
			entry = &patchCacheEntry{
				text:      slice.Content,
				positions: parseValidPositions(slice.Content),
			}
			cacheByPath[f.Path] = entry
		}
		if entry == nil {
			// Negative-cached: previous fetch failed or path unknown.
			telemetry.Counter("github_pr_review.findings_dropped", "reason", "no_positions_for_path")
			continue
		}
		if !findingMappable(f, entry.positions) {
			// Finding's (line, side) is not a commentable diff position.
			// Posting it would either fail late at GitHub or land at the
			// wrong line. Drop instead.
			telemetry.Counter("github_pr_review.findings_dropped", "reason", "not_in_diff")
			continue
		}

		// Side-branched evidence verification:
		//   LEFT  → verify against the patch (deleted code lives only there;
		//           the head-file artifact wouldn't contain it). Critical for
		//           security regressions like "this PR removed an auth check."
		//   RIGHT → verify against the head-file artifact for richer
		//           surrounding context. Falls back to "drop" if the file
		//           artifact wasn't captured (rate limit, oversize, deletion);
		//           we don't fall back to the patch here because head-file
		//           gives strictly better context for added code.
		side := f.Side
		if side == "" {
			side = "RIGHT"
		}

		var contextText string
		if side == "LEFT" {
			contextText = extractPatchContext(entry.text, f.Line, "LEFT", 10)
			if contextText == "" {
				telemetry.Counter("github_pr_review.findings_dropped", "reason", "no_left_context")
				continue
			}
		} else {
			if !hasFileArtifact[f.Path] {
				telemetry.Counter("github_pr_review.findings_dropped", "reason", "no_file_artifact")
				continue
			}
			start := f.Line - 10
			if start < 1 {
				start = 1
			}
			resp, err := a.ToolClient.GetFileContext(ctx, GetFileContextRequest{
				BundleID:  bundle.BundleID,
				Path:      f.Path,
				LineStart: start,
				LineEnd:   f.Line + 10,
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				telemetry.Counter("github_pr_review.findings_dropped", "reason", "context_fetch_failed")
				continue
			}
			contextText = resp.Content
		}

		if !claimGroundedInContext(f, contextText) {
			telemetry.Counter("github_pr_review.findings_dropped", "reason", "ungrounded_evidence")
			continue
		}
		f.Side = side // normalize empty → RIGHT
		verified = append(verified, f)
	}
	return verified, nil
}

// claimGroundedInContext checks that the finding's evidence snippet actually
// appears in the fetched context. Normalized to whitespace-squashed lowercase
// substrings so minor formatting differences don't cause false negatives.
func claimGroundedInContext(f ReviewFinding, context string) bool {
	if f.Evidence == "" {
		// Without a quoted snippet we can't ground the claim. Require one.
		return false
	}
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	return strings.Contains(norm(context), norm(f.Evidence))
}

func (a *PRReviewAgent) buildResult(
	input ReviewPRInput,
	bundle PRBundleManifest,
	findings []ReviewFinding,
	skipped []SkippedFile,
) ReviewTaskResult {
	decision := decideEvent(findings, a.Config)
	return ReviewTaskResult{
		Status:       "completed",
		Owner:        input.Owner,
		Repo:         input.Repo,
		PullNumber:   input.PullNumber,
		HeadSHA:      bundle.HeadSHA,
		Decision:     decision,
		Summary:      buildSummary(findings, skipped),
		Findings:     findings,
		SkippedFiles: skipped,
	}
}

// decideEvent returns REQUEST_CHANGES when there is at least one blocking
// finding above the confidence threshold (and policy allows it); otherwise
// COMMENT. APPROVE is out of scope for the MVP.
func decideEvent(findings []ReviewFinding, cfg *ReviewConfig) string {
	if cfg.AllowRequestChanges {
		for _, f := range findings {
			if f.Severity == "blocking" && f.Confidence >= cfg.RequestChangesMinConfidence {
				return "REQUEST_CHANGES"
			}
		}
	}
	return "COMMENT"
}

func buildSummary(findings []ReviewFinding, skipped []SkippedFile) string {
	if len(findings) == 0 {
		return "No issues flagged in this review."
	}
	var blocking, warning, info int
	for _, f := range findings {
		switch f.Severity {
		case "blocking":
			blocking++
		case "warning":
			warning++
		case "info":
			info++
		}
	}
	parts := []string{}
	if blocking > 0 {
		parts = append(parts, fmt.Sprintf("%d blocking", blocking))
	}
	if warning > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", warning))
	}
	if info > 0 {
		parts = append(parts, fmt.Sprintf("%d info", info))
	}
	summary := "Found " + strings.Join(parts, ", ") + "."
	if len(skipped) > 0 {
		summary += fmt.Sprintf(" Skipped %d file(s) (generated/lockfile).", len(skipped))
	}
	return summary
}

// ParseReviewPRInput decodes the task.Input map into a typed struct.
func ParseReviewPRInput(in map[string]interface{}) (ReviewPRInput, error) {
	var out ReviewPRInput
	var ok bool

	if out.Owner, ok = in["owner"].(string); !ok || out.Owner == "" {
		return out, fmt.Errorf("input.owner is required")
	}
	if out.Repo, ok = in["repo"].(string); !ok || out.Repo == "" {
		return out, fmt.Errorf("input.repo is required")
	}
	switch v := in["pull_number"].(type) {
	case float64:
		out.PullNumber = int(v)
	case int:
		out.PullNumber = v
	default:
		return out, fmt.Errorf("input.pull_number is required (got %T)", in["pull_number"])
	}
	if out.PullNumber <= 0 {
		return out, fmt.Errorf("input.pull_number must be positive")
	}
	if s, ok := in["head_sha"].(string); ok {
		out.HeadSHA = s
	}
	if b, ok := in["post_review"].(bool); ok {
		out.PostReview = b
	}
	if s, ok := in["review_depth"].(string); ok {
		out.ReviewDepth = s
	}
	return out, nil
}

func report(r core.ProgressReporter, step, total, pct int, name, message string) {
	if r == nil {
		return
	}
	_ = r.Report(&core.TaskProgress{
		CurrentStep: step,
		TotalSteps:  total,
		StepName:    name,
		Percentage:  float64(pct),
		Message:     message,
	})
}
