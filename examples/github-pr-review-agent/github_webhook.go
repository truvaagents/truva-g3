package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

const (
	maxWebhookBytes  = 10 * 1024 * 1024 // 10 MiB safety cap on payload size
	deliveryDedupTTL = 24 * time.Hour
	headDedupTTL     = 1 * time.Hour
)

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Number int  `json:"number"`
		Draft  bool `json:"draft"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
		Name     string `json:"name"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
}

// handleGitHubWebhook receives pull_request webhooks, verifies the HMAC
// signature, performs two-layer deduplication (delivery ID + head SHA),
// and enqueues a "review_pr" task.
func (a *PRReviewAgent) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const op = "github_webhook"
	reqID := requestID(r)
	markReceived(ctx, op, reqID)

	if r.Method != http.MethodPost {
		err := fmt.Errorf("method %s not allowed", r.Method)
		a.recordWebhookError(ctx, w, op, "method_not_allowed", "method not allowed",
			http.StatusMethodNotAllowed, reqID, err)
		return
	}

	// Signature verification needs the raw bytes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		a.recordWebhookError(ctx, w, op, "body_read_error",
			"request body too large or unreadable",
			http.StatusBadRequest, reqID, err)
		return
	}

	if !verifyGitHubSignature(body, r.Header.Get("X-Hub-Signature-256"), a.Config.WebhookSecret) {
		err := fmt.Errorf("HMAC SHA-256 signature did not match")
		a.recordWebhookError(ctx, w, op, "invalid_signature", "invalid signature",
			http.StatusUnauthorized, reqID, err)
		return
	}

	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		a.Logger.InfoWithContext(ctx, op+": skipped (non-pull_request event)", map[string]interface{}{
			"operation":  op,
			"event":      r.Header.Get("X-GitHub-Event"),
			"request_id": reqID,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "reason": "non-pull_request event"})
		return
	}

	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		a.recordWebhookError(ctx, w, op, "decode_error", "invalid JSON",
			http.StatusBadRequest, reqID, err)
		return
	}

	if !shouldReviewAction(event.Action) {
		a.Logger.InfoWithContext(ctx, op+": skipped (unsupported action)", map[string]interface{}{
			"operation":  op,
			"action":     event.Action,
			"request_id": reqID,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "reason": "action " + event.Action})
		return
	}

	if event.PullRequest.Draft && !a.Config.ReviewDrafts {
		a.Logger.InfoWithContext(ctx, op+": skipped (draft PR)", map[string]interface{}{
			"operation":  op,
			"request_id": reqID,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "reason": "draft"})
		return
	}

	owner, repo := event.Repository.Owner.Login, event.Repository.Name
	if owner == "" || repo == "" {
		// Some payloads populate only full_name.
		if parts := strings.SplitN(event.Repository.FullName, "/", 2); len(parts) == 2 {
			owner, repo = parts[0], parts[1]
		}
	}
	pullNumber := event.Number
	if pullNumber == 0 {
		pullNumber = event.PullRequest.Number
	}
	headSHA := event.PullRequest.Head.SHA
	deliveryID := r.Header.Get("X-GitHub-Delivery")

	if owner == "" || repo == "" || pullNumber == 0 || headSHA == "" {
		err := fmt.Errorf("missing owner=%q repo=%q pr=%d head_sha=%q", owner, repo, pullNumber, headSHA)
		a.recordWebhookError(ctx, w, op, "missing_fields",
			"missing owner/repo/pull_number/head_sha in payload",
			http.StatusBadRequest, reqID, err)
		return
	}

	// Enrich the request span with business context so Jaeger searches like
	// `truvag3.agent.name:github-pr-review-agent github.repo:payments` work.
	telemetry.SetSpanAttributes(ctx,
		attribute.String("github.owner", owner),
		attribute.String("github.repo", repo),
		attribute.Int("github.pull_number", pullNumber),
		attribute.String("github.head_sha", headSHA),
		attribute.String("github.delivery_id", deliveryID),
		attribute.String("github.action", event.Action),
	)

	a.Logger.InfoWithContext(ctx, op+": starting", map[string]interface{}{
		"operation":   op,
		"owner":       owner,
		"repo":        repo,
		"pull_number": pullNumber,
		"head_sha":    headSHA,
		"action":      event.Action,
		"request_id":  reqID,
	})

	// Two-layer dedup. Delivery ID first (rejects webhook retries), then
	// (owner, repo, pull, head SHA) (collapses repeated events for same commit).
	//
	// Track keys we set so we can compensate-delete them on enqueue failure.
	// Without this, GitHub's webhook retry would hit our dedup as
	// "duplicate_delivery" and return 202, silently losing the review.
	var setDedupKeys []string

	if deliveryID != "" {
		key := fmt.Sprintf("github-pr-review:delivery:%s", deliveryID)
		if !a.markFresh(ctx, op, key, deliveryDedupTTL, reqID) {
			a.Logger.InfoWithContext(ctx, op+": duplicate delivery; ignoring", map[string]interface{}{
				"operation":   op,
				"delivery_id": deliveryID,
				"request_id":  reqID,
			})
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate_delivery"})
			return
		}
		setDedupKeys = append(setDedupKeys, key)
	}
	headKey := fmt.Sprintf("github-pr-review:head:%s/%s:%d:%s", owner, repo, pullNumber, headSHA)
	if !a.markFresh(ctx, op, headKey, headDedupTTL, reqID) {
		a.Logger.InfoWithContext(ctx, op+": duplicate head SHA; ignoring", map[string]interface{}{
			"operation":  op,
			"owner":      owner,
			"repo":       repo,
			"head_sha":   headSHA,
			"request_id": reqID,
		})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "duplicate_head"})
		return
	}
	setDedupKeys = append(setDedupKeys, headKey)

	// Carry the request_id forward into the worker via task.Input so the
	// review handler can stitch it into its own logs (baggage doesn't survive
	// the queue hop).
	input := map[string]interface{}{
		"owner":       owner,
		"repo":        repo,
		"pull_number": pullNumber,
		"head_sha":    headSHA,
		"post_review": a.Config.DefaultPost,
		"request_id":  reqID,
	}
	taskID := uuid.New().String()
	task := core.NewTaskWithTimeout(taskID, "review_pr", input, a.Config.TaskTimeout)

	// Carry the webhook span's trace context onto the task so the worker can
	// link its consumer span back to the originating HTTP request.
	tc := telemetry.GetTraceContext(ctx)
	task.SetTraceContext(tc.TraceID, tc.SpanID)

	if err := a.TaskQueue.Enqueue(ctx, task); err != nil {
		// Compensating delete: GitHub will retry on 503; the dedup keys must
		// not block re-entry. If the delete itself fails, log it but don't
		// surface to GitHub — the keys' TTLs are the safety net.
		a.releaseDedupKeys(ctx, op, setDedupKeys, reqID)

		telemetry.RecordSpanError(ctx, err)
		a.Logger.ErrorWithContext(ctx, op+": enqueue review_pr failed", map[string]interface{}{
			"operation":  op,
			"error_type": "enqueue_error",
			"error":      err.Error(),
			"owner":      owner,
			"repo":       repo,
			"pr":         pullNumber,
			"request_id": reqID,
		})
		http.Error(w, "enqueue failed", http.StatusServiceUnavailable)
		return
	}

	telemetry.AddSpanEvent(ctx, "task_enqueued",
		attribute.String("task.id", taskID),
		attribute.String("task.type", "review_pr"))

	a.Logger.InfoWithContext(ctx, op+": task enqueued", map[string]interface{}{
		"operation":   op,
		"task_id":     taskID,
		"owner":       owner,
		"repo":        repo,
		"pull_number": pullNumber,
		"request_id":  reqID,
	})

	writeJSON(w, http.StatusAccepted, map[string]string{
		"task_id": taskID,
		"status":  "accepted",
	})
}

func shouldReviewAction(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize", "ready_for_review":
		return true
	default:
		return false
	}
}

// verifyGitHubSignature verifies X-Hub-Signature-256 ("sha256=...") against
// HMAC-SHA256(secret, body). When secret is empty or header is missing,
// returns false.
func verifyGitHubSignature(body []byte, signatureHeader, secret string) bool {
	if secret == "" || signatureHeader == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

// releaseDedupKeys is the compensating action when enqueue fails after we've
// already set dedup keys. Without this, GitHub's webhook retry would hit our
// dedup as "duplicate_delivery" and return 202, silently losing the review.
// Failures here are logged but don't propagate — the keys' TTLs are the
// safety net of last resort.
func (a *PRReviewAgent) releaseDedupKeys(ctx context.Context, op string, keys []string, reqID string) {
	if a.RedisClient == nil || len(keys) == 0 {
		return
	}
	for _, key := range keys {
		if err := a.RedisClient.Del(ctx, key).Err(); err != nil {
			a.Logger.WarnWithContext(ctx, op+": dedup key release failed; key will TTL out", map[string]interface{}{
				"operation":  op,
				"error_type": "redis_error",
				"error":      err.Error(),
				"key":        key,
				"request_id": reqID,
			})
		}
	}
}

// markFresh returns true if the key was newly set, false if it already exists.
// On Redis errors, fail-open (return true) so genuine events aren't silently
// lost — but still record the error on the span and log it for visibility.
func (a *PRReviewAgent) markFresh(ctx context.Context, op, key string, ttl time.Duration, reqID string) bool {
	if a.RedisClient == nil {
		return true
	}
	ok, err := a.RedisClient.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		a.Logger.WarnWithContext(ctx, op+": dedup SETNX failed; allowing event", map[string]interface{}{
			"operation":  op,
			"error_type": "redis_error",
			"error":      err.Error(),
			"request_id": reqID,
		})
		return true
	}
	return ok
}
