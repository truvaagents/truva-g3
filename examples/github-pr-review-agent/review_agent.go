package main

import (
	"net/http"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/memory"
	"github.com/truvaagents/truva-g3/telemetry"
)

type PRReviewAgent struct {
	*core.BaseAgent

	Config      *ReviewConfig
	ToolClient  *GitHubToolClient
	HTTPClient  *http.Client
	RedisClient *redis.Client
	TaskQueue   core.TaskQueue
	TaskStore   core.TaskStore

	// Episodic is the optional shared-memory write surface. Captured from
	// SharedBackends.ToDeps().Episodic at construction so handlers can call
	// RecordEvent directly without re-resolving every call. Nil when shared
	// memory is unavailable; handlers must nil-check.
	Episodic core.EpisodicMemory
}

// declareMetrics registers the agent's domain metrics with telemetry. Safe to
// call before telemetry.Initialize — declarations are stored and processed at
// init time. Called from NewPRReviewAgent so every mode (api/worker/embedded)
// gets the same registrations.
func declareMetrics() {
	telemetry.DeclareMetrics("github-pr-review-agent", telemetry.ModuleConfig{
		Metrics: []telemetry.MetricDefinition{
			{Name: "github_pr_review.tasks_processed", Type: "counter",
				Help: "Review tasks completed by status.", Labels: []string{"status", "decision"}},
			{Name: "github_pr_review.task_duration_ms", Type: "histogram",
				Help: "End-to-end review task duration.", Unit: "milliseconds"},
			{Name: "github_pr_review.shards_reviewed", Type: "counter",
				Help: "Shards reviewed by status.", Labels: []string{"status"}},
			{Name: "github_pr_review.findings", Type: "counter",
				Help: "Findings emitted by severity (post-merge, post-verify).", Labels: []string{"severity", "stage"}},
			{Name: "github_pr_review.skipped_files", Type: "counter",
				Help: "Files skipped by reason (generated, lockfile).", Labels: []string{"reason"}},
			{Name: "github_pr_review.posts_attempted", Type: "counter",
				Help: "Review-post attempts by outcome.", Labels: []string{"outcome", "decision"}},
			{Name: "github_pr_review.provider_errors", Type: "counter",
				Help: "AI provider errors during shard review.", Labels: []string{"provider", "status", "transient"}},
			{Name: "github_pr_review.findings_dropped", Type: "counter",
				Help: "Findings rejected during evidence verification.", Labels: []string{"reason"}},
		},
	})
}

func NewPRReviewAgent(
	redisClient *redis.Client,
	taskQueue core.TaskQueue,
	taskStore core.TaskStore,
	cfg *ReviewConfig,
	memBackends *memory.SharedBackends, // may be nil; handlers nil-check
) (*PRReviewAgent, error) {
	declareMetrics()

	base := core.NewBaseAgent("github-pr-review-agent")

	chainClient, err := ai.NewChainClient(
		ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
		ai.WithChainLogger(base.Logger),
	)
	if err != nil {
		// ChainClient needs ≥1 provider configured. Fall back to a single
		// provider client (auto-detects the first available API key). If
		// that also fails, log clearly that AI is fully unavailable —
		// shard reviews will then return errors at runtime.
		base.Logger.Warn("AI ChainClient unavailable; falling back to single-provider client", map[string]interface{}{
			"chain_error": err.Error(),
		})
		singleClient, serr := ai.NewClient()
		if serr != nil {
			base.Logger.Error("AI fully unavailable; shard reviews will fail at runtime", map[string]interface{}{
				"chain_error":  err.Error(),
				"single_error": serr.Error(),
			})
		} else {
			base.AI = singleClient
		}
	} else {
		base.AI = chainClient
	}

	tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	})
	tracedClient.Timeout = 60 * time.Second

	agent := &PRReviewAgent{
		BaseAgent:   base,
		Config:      cfg,
		HTTPClient:  tracedClient,
		RedisClient: redisClient,
		TaskQueue:   taskQueue,
		TaskStore:   taskStore,
	}

	// Tool client resolves Discovery lazily at call time, since
	// BaseAgent.Discovery is nil until framework.Run() populates it.
	agent.ToolClient = NewGitHubToolClient(tracedClient, agent)

	// Capture Episodic from the memory backends if available. Handlers
	// nil-check before writing — review correctness never depends on memory.
	if memBackends != nil {
		agent.Episodic = memBackends.ToDeps().Episodic
	}

	agent.registerCapabilities()
	return agent, nil
}
