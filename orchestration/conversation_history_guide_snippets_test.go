package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

type guideSnippetAIClient struct{}

func (guideSnippetAIClient) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	return &core.AIResponse{Content: "summary"}, nil
}

type guideSnippetCompactor struct {
	aiClient core.AIClient
	logger   core.Logger
}

func newGuideSnippetCompactor(aiClient core.AIClient) *guideSnippetCompactor {
	return &guideSnippetCompactor{
		aiClient: aiClient,
		logger:   &core.NoOpLogger{},
	}
}

func (c *guideSnippetCompactor) SetLogger(logger core.Logger) {
	if logger == nil {
		c.logger = &core.NoOpLogger{}
		return
	}
	c.logger = logger
}

func (c *guideSnippetCompactor) Compact(
	ctx context.Context,
	priorSummary string,
	newTurns []core.ConversationTurn,
) (string, error) {
	if len(newTurns) == 0 {
		return priorSummary, nil
	}

	var prompt strings.Builder
	prompt.WriteString("Summarize the durable state of this conversation.\n")
	prompt.WriteString("Preserve goals, constraints, decisions, and unresolved questions.\n")
	prompt.WriteString("Do not include stale workflow narration.\n\n")
	if priorSummary != "" {
		prompt.WriteString("Existing summary:\n")
		prompt.WriteString(priorSummary)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("New turns:\n")
	for _, turn := range newTurns {
		prompt.WriteString(turn.Role)
		prompt.WriteString(": ")
		prompt.WriteString(turn.Content)
		prompt.WriteString("\n")
	}

	resp, err := c.aiClient.GenerateResponse(ctx, prompt.String(), nil)
	if err != nil {
		if c.logger != nil {
			c.logger.WarnWithContext(ctx, "Custom conversation compaction failed", map[string]interface{}{
				"operation":  "conversation_history",
				"error":      err.Error(),
				"error_type": "compaction",
			})
		}
		return "", nil
	}

	return strings.TrimSpace(resp.Content), nil
}

func TestConversationHistoryGuideLayer3SnippetCompilesAndBuildsProcessor(t *testing.T) {
	config := DefaultConfig()

	cache, err := NewSummaryCache(config.ConversationSummaryCacheSize)
	if err != nil {
		t.Fatalf("NewSummaryCache() error = %v", err)
	}

	myCompactor := newGuideSnippetCompactor(guideSnippetAIClient{})

	preparer, err := NewConversationHistoryProcessor(
		ConversationHistoryProcessorConfig{
			TokenBudget:          config.ConversationTokenBudget,
			RecentTurnsPreserved: config.ConversationRecentTurnsPreserved,
		},
		WithConversationSummaryCache(cache),
		WithConversationCompactor(myCompactor),
	)
	if err != nil {
		t.Fatalf("NewConversationHistoryProcessor() error = %v", err)
	}
	if preparer == nil {
		t.Fatal("expected preparer")
	}
}

type guideSnippetAgent struct {
	AI           core.AIClient
	Logger       core.Logger
	orchestrator *AIOrchestrator
}

func (a *guideSnippetAgent) initializeOrchestratorTier1(discovery core.Discovery) error {
	// Normal orchestrator config
	config := DefaultConfig()

	// Tier 1 requires no special conversation-history object here.
	// The factory will auto-install the default preparer for you.
	deps := OrchestratorDependencies{
		Discovery:           discovery,
		AIClient:            a.AI,
		Logger:              a.Logger,
		Telemetry:           telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer: true,
	}

	// Key line for Tier 1:
	// CreateOrchestrator(...) sees that no custom
	// ConversationHistoryPreparer was injected and builds the
	// default shared preparer automatically.
	orch, err := CreateOrchestrator(config, deps)
	if err != nil {
		return err
	}

	// The rest of your startup flow stays the same.
	if err := orch.Start(context.Background()); err != nil {
		return err
	}

	a.orchestrator = orch
	return nil
}

func (a *guideSnippetAgent) initializeOrchestratorTier2(discovery core.Discovery) error {
	config := DefaultConfig()

	// --- Tier 2 code starts here -------------------------------------
	preparer, err := BuildCompactionEnabledConversationHistoryPreparer(
		config,
		a.AI,
	)
	if err != nil {
		return err
	}
	// --- Tier 2 code ends here ---------------------------------------

	deps := OrchestratorDependencies{
		Discovery:           discovery,
		AIClient:            a.AI,
		Logger:              a.Logger,
		Telemetry:           telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer: true,

		// Key line for Tier 2:
		// injecting this preparer replaces the factory's default
		// Tier 1-only preparer with a Tier 2-enabled one.
		ConversationHistoryPreparer: preparer,
	}

	orch, err := CreateOrchestrator(config, deps)
	if err != nil {
		return err
	}

	if err := orch.Start(context.Background()); err != nil {
		return err
	}

	a.orchestrator = orch
	return nil
}

func (a *guideSnippetAgent) initializeOrchestratorTier2WithFallback(discovery core.Discovery) error {
	config := DefaultConfig()

	var conversationHistoryPreparer ConversationHistoryPreparer

	// Only enable Tier 2 when an AI client is available.
	// If not, leave this nil and the factory falls back to Tier 1.
	if a.AI != nil {
		preparer, err := BuildCompactionEnabledConversationHistoryPreparer(
			config,
			a.AI,
		)
		if err != nil {
			return err
		}
		conversationHistoryPreparer = preparer
	}

	deps := OrchestratorDependencies{
		Discovery:                   discovery,
		AIClient:                    a.AI,
		Logger:                      a.Logger,
		Telemetry:                   telemetry.GetTelemetryProvider(),
		EnableErrorAnalyzer:         true,
		ConversationHistoryPreparer: conversationHistoryPreparer,
	}

	orch, err := CreateOrchestrator(config, deps)
	if err != nil {
		return err
	}

	if err := orch.Start(context.Background()); err != nil {
		return err
	}

	a.orchestrator = orch
	return nil
}

func TestConversationHistoryGuideTier1InitializationSnippetWorks(t *testing.T) {
	agent := &guideSnippetAgent{
		AI:     guideSnippetAIClient{},
		Logger: &core.NoOpLogger{},
	}
	if err := agent.initializeOrchestratorTier1(core.NewMockDiscovery()); err != nil {
		t.Fatalf("initializeOrchestratorTier1() error = %v", err)
	}
	if agent.orchestrator == nil {
		t.Fatal("expected orchestrator")
	}
	agent.orchestrator.Stop()
}

func TestConversationHistoryGuideTier2InitializationSnippetWorks(t *testing.T) {
	agent := &guideSnippetAgent{
		AI:     guideSnippetAIClient{},
		Logger: &core.NoOpLogger{},
	}
	if err := agent.initializeOrchestratorTier2(core.NewMockDiscovery()); err != nil {
		t.Fatalf("initializeOrchestratorTier2() error = %v", err)
	}
	if agent.orchestrator == nil {
		t.Fatal("expected orchestrator")
	}
	agent.orchestrator.Stop()
}

func TestConversationHistoryGuideTier2FallbackSnippetWorks(t *testing.T) {
	agent := &guideSnippetAgent{
		Logger: &core.NoOpLogger{},
	}
	if err := agent.initializeOrchestratorTier2WithFallback(core.NewMockDiscovery()); err != nil {
		t.Fatalf("initializeOrchestratorTier2WithFallback() error = %v", err)
	}
	if agent.orchestrator == nil {
		t.Fatal("expected orchestrator")
	}
	agent.orchestrator.Stop()
}
