package main

import (
	"os"
	"strconv"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

// OpenClawTool wraps the OpenClaw smart-process (a sidecar container reached over
// localhost) as a TruvaG3 tool specialized for summarizing / answering over text that is
// too large to fit in an LLM context window. See ANALYSIS.md and plan.md for the design.
//
// OpenClaw is treated as a black-box transaction: request in -> response out (or timeout).
// The adapter owns the transaction lifecycle — concurrency (semaphore of 1, ANALYSIS.md §7)
// and statelessness (per-transaction workspace reset, §8).
type OpenClawTool struct {
	*core.BaseTool
	client *OpenClawClient
	sem    chan struct{} // semaphore of 1: serialize transactions per pod (§7)
	cfg    toolConfig
}

// toolConfig holds the adapter's env-derived settings.
type toolConfig struct {
	AgentID           string        // x-openclaw-agent-id header (OpenClaw's default agent: "main")
	MaxInputChars     int           // size cap; fits the model context window (§9/§12)
	DefaultTimeout    time.Duration // transaction default
	MaxTimeout        time.Duration // transaction ceiling
	SemAcquireTimeout time.Duration // bounded wait before 503 BUSY (§7)
}

// ---- Request / response DTOs (JSON tags are the source of truth for the *Summary fields) ----

// SummarizeRequest is the input for the summarize_text capability.
type SummarizeRequest struct {
	Text        string `json:"text"`                   // full content to summarize (required)
	Focus       string `json:"focus,omitempty"`        // topic/angle to emphasize
	Style       string `json:"style,omitempty"`        // executive|bullets|detailed|tldr
	TargetWords int    `json:"target_words,omitempty"` // approximate length
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// SummarizeResponse is the output of the summarize_text capability.
type SummarizeResponse struct {
	Summary           string `json:"summary"`
	InputChars        int    `json:"input_chars"`
	Style             string `json:"style"`
	SectionsProcessed int    `json:"sections_processed,omitempty"`
	Truncated         bool   `json:"truncated"`
}

// AnswerRequest is the input for the answer_over_text capability.
type AnswerRequest struct {
	Text        string `json:"text"`     // content to ground the answer in (required)
	Question    string `json:"question"` // the question to answer (required)
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// AnswerResponse is the output of the answer_over_text capability.
type AnswerResponse struct {
	Answer             string   `json:"answer"`
	Found              bool     `json:"found"`
	SupportingExcerpts []string `json:"supporting_excerpts,omitempty"`
}

// RunTaskRequest is the input for run_task — the autonomous mode (§13). The agent solves the
// task using its tools (exec/fs); summarize_text/answer_over_text are pure-LLM presets.
type RunTaskRequest struct {
	Task        string `json:"task"`
	TimeoutSecs int    `json:"timeout_seconds,omitempty"`
}

// RunTaskResponse is the output of run_task.
type RunTaskResponse struct {
	Result string `json:"result"`
}

// Error codes (Retryable is derived from HTTP status in sendError: status >= 500).
const (
	ErrCodeInvalidRequest = "INVALID_REQUEST"
	ErrCodeInputTooLarge  = "INPUT_TOO_LARGE"
	ErrCodeBusy           = "BUSY"
	ErrCodeTimeout        = "TIMEOUT"
)

// NewOpenClawTool reads configuration from the environment, builds the traced OpenClaw
// client, and registers capabilities. The constructor calls core.NewTool which sets the
// component type for telemetry auto-inference, so it must run BEFORE initTelemetry.
func NewOpenClawTool() *OpenClawTool {
	cfg := toolConfig{
		AgentID:           getenvStr("OPENCLAW_AGENT_ID", "main"),
		MaxInputChars:     getenvInt("MAX_INPUT_CHARS", 1_000_000),
		DefaultTimeout:    time.Duration(getenvInt("DEFAULT_TIMEOUT_SECONDS", 300)) * time.Second,
		MaxTimeout:        time.Duration(getenvInt("MAX_TIMEOUT_SECONDS", 900)) * time.Second,
		SemAcquireTimeout: time.Duration(getenvInt("SEM_ACQUIRE_TIMEOUT_SECONDS", 5)) * time.Second,
	}

	tool := &OpenClawTool{
		BaseTool: core.NewTool("openclaw-tool"),
		client: NewOpenClawClient(
			getenvStr("OPENCLAW_URL", "http://127.0.0.1:18789"),
			os.Getenv("OPENCLAW_GATEWAY_TOKEN"),
		),
		sem: make(chan struct{}, 1),
		cfg: cfg,
	}

	tool.registerCapabilities()
	return tool
}

func (t *OpenClawTool) registerCapabilities() {
	// run_task — the autonomous capability (§13): hand the agent a task, it solves it with tools.
	t.RegisterCapability(core.Capability{
		Name: "run_task",
		Description: "Hand a self-contained task to the autonomous OpenClaw agent, which solves it end-to-end using its tools (shell/exec + file I/O) inside a contained sandbox and returns the result. " +
			"Use for open-ended work an LLM alone cannot do: running commands, transforming or analyzing files and logs, multi-step computation. The agent has NO internet access (exec + files only). " +
			"Returns the agent's final result text. " +
			"Required: task (a clear, self-contained instruction). Optional: timeout_seconds (default 300, max 900).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleRunTask,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "task", Type: "string", Example: "Count the distinct ERROR lines in the file /home/node/.openclaw/workspace/input.log and report the top 3 with counts.", Description: "A clear, self-contained instruction for the agent to carry out with its tools"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "timeout_seconds", Type: "number", Example: "300", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "result", Type: "string", Description: "The agent's final result for the task"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "summarize_text",
		Description: "Summarizes text too large to fit in an LLM context window. " +
			"The OpenClaw smart-process writes the text to a scratch workspace and map-reduces over it (chunk, note, synthesize), so it handles inputs far beyond a single completion's token limit. " +
			"Use for long documents, transcripts, call logs, research papers, or concatenated articles where a one-shot LLM summary would overflow context. " +
			"Returns a synthesized summary plus processing metadata. " +
			"Required: text. Optional: focus (topic to emphasize), style (executive|bullets|detailed|tldr, default executive), target_words (approximate length), timeout_seconds (default 180).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSummarizeText,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "text", Type: "string", Example: "<full transcript of a 3-hour earnings call…>", Description: "The complete text to summarize; may be far larger than an LLM context window"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "focus", Type: "string", Example: "competitive risks", Description: "Topic or angle to emphasize in the summary"},
				{Name: "style", Type: "string", Example: "bullets", Description: "One of: executive, bullets, detailed, tldr (default executive)"},
				{Name: "target_words", Type: "number", Example: "300", Description: "Approximate length of the summary in words"},
				{Name: "timeout_seconds", Type: "number", Example: "180", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "summary", Type: "string", Description: "The synthesized summary"},
				{Name: "input_chars", Type: "number", Description: "Size of the input in characters"},
				{Name: "style", Type: "string", Description: "The summary style that was applied"},
				{Name: "truncated", Type: "boolean", Description: "Whether the input was truncated to fit the size cap (currently inputs over the cap are rejected, so always false)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "sections_processed", Type: "number", Description: "Number of chunks OpenClaw map-reduced over, if reported"},
			},
		},
	})

	t.RegisterCapability(core.Capability{
		Name: "answer_over_text",
		Description: "Answers a specific question grounded in text too large to fit in an LLM context window. " +
			"OpenClaw reads and searches the text in its workspace and returns a focused, grounded answer, so you can interrogate documents far beyond a single completion's token limit. " +
			"Use when you have a big document AND a precise question about it, for example the termination clauses in a long contract or the first error in a huge log. " +
			"Returns the answer, whether it was found, and optional supporting excerpts. " +
			"Required: text, question. Optional: timeout_seconds (default 180).",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleAnswerOverText,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "text", Type: "string", Example: "<200-page contract…>", Description: "The content to ground the answer in; may exceed an LLM context window"},
				{Name: "question", Type: "string", Example: "What are the contract's termination clauses?", Description: "The question to answer strictly from the text"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "timeout_seconds", Type: "number", Example: "180", Description: "Transaction time budget; clamped to the adapter's ceiling"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "answer", Type: "string", Description: "The grounded answer, or an explanation if the answer is not in the text"},
				{Name: "found", Type: "boolean", Description: "Whether the answer was supported by the text"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "supporting_excerpts", Type: "array", Description: "Verbatim snippets from the text that back the answer"},
			},
		},
	})

	// Structured capabilities (ANALYSIS section 14) - typed prompt+schema wrappers over the
	// same engine, registered from a data-driven spec table so adding one is a table row + a prompt.
	for _, spec := range capabilitySpecs() {
		t.registerStructured(spec)
	}
}

// ---- small env helpers ----

func getenvStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
