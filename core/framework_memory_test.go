package core

import (
	"context"
	"testing"
	"time"
)

// ---- BaseAgent default Memory ----

func TestNewBaseAgent_DefaultMemoryIsMemoryStore(t *testing.T) {
	agent := NewBaseAgent("test-default-memory")
	if agent.Memory == nil {
		t.Fatal("expected Memory to be defaulted, got nil")
	}
	if _, ok := agent.Memory.(*MemoryStore); !ok {
		t.Errorf("expected agent.Memory to be *MemoryStore, got %T", agent.Memory)
	}
}

// sentinelMemory is an alternate Memory implementation used to verify that
// Initialize does not overwrite a caller-injected impl.
type sentinelMemory struct{}

func (sentinelMemory) Get(_ context.Context, _ string) (string, error) { return "", nil }
func (sentinelMemory) Set(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (sentinelMemory) Delete(_ context.Context, _ string) error         { return nil }
func (sentinelMemory) Exists(_ context.Context, _ string) (bool, error) { return false, nil }

func TestBaseAgent_InitializeDoesNotTouchMemory(t *testing.T) {
	agent := NewBaseAgent("test-initialize-no-touch")
	custom := sentinelMemory{}
	agent.Memory = custom

	if err := agent.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	got, ok := agent.Memory.(sentinelMemory)
	if !ok {
		t.Fatalf("expected agent.Memory to remain sentinelMemory after Initialize, got %T", agent.Memory)
	}
	_ = got // value comparison not needed; type identity is the contract
}

// ---- findEmbeddedBaseAgent helper ----

// wrappedAgent embeds *BaseAgent — matches the in-tree custom-agent pattern
// (research_agent.go etc.).
type wrappedAgent struct {
	*BaseAgent
	domainField string
}

// noBaseAgent is a struct that does NOT embed *BaseAgent.
type noBaseAgent struct {
	other string
}

func TestFindEmbeddedBaseAgent_DirectBaseAgent(t *testing.T) {
	agent := NewBaseAgent("direct")
	got := findEmbeddedBaseAgent(agent)
	if got != agent {
		t.Errorf("expected direct *BaseAgent to be returned, got %v", got)
	}
}

func TestFindEmbeddedBaseAgent_EmbeddedBaseAgent(t *testing.T) {
	inner := NewBaseAgent("embedded")
	wrapper := &wrappedAgent{BaseAgent: inner, domainField: "x"}
	got := findEmbeddedBaseAgent(wrapper)
	if got != inner {
		t.Errorf("expected embedded *BaseAgent to be returned, got %v", got)
	}
}

func TestFindEmbeddedBaseAgent_StructWithoutBaseAgent(t *testing.T) {
	got := findEmbeddedBaseAgent(&noBaseAgent{other: "x"})
	if got != nil {
		t.Errorf("expected nil for struct without *BaseAgent, got %v", got)
	}
}

func TestFindEmbeddedBaseAgent_NilComponent(t *testing.T) {
	got := findEmbeddedBaseAgent(nil)
	if got != nil {
		t.Errorf("expected nil for nil component, got %v", got)
	}
}

func TestFindEmbeddedBaseAgent_NonPointerStruct(t *testing.T) {
	got := findEmbeddedBaseAgent(noBaseAgent{other: "x"}) // value, not pointer
	if got != nil {
		t.Errorf("expected nil for non-pointer struct, got %v", got)
	}
}

func TestFindEmbeddedBaseAgent_PointerToNonStruct(t *testing.T) {
	x := 42
	got := findEmbeddedBaseAgent(&x)
	if got != nil {
		t.Errorf("expected nil for *int, got %v", got)
	}
}

// ---- Framework.AutoRegisterMemorySweeper ----

func newTestFrameworkWithComponent(t *testing.T, component HTTPComponent, cleanupInterval time.Duration) *Framework {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Memory.CleanupInterval = cleanupInterval
	applyConfigToComponent(component, cfg)
	return &Framework{component: component, config: cfg}
}

func TestFramework_AutoRegisterMemorySweeper_DirectAgent(t *testing.T) {
	agent := NewBaseAgent("autoregister-direct")
	f := newTestFrameworkWithComponent(t, agent, 5*time.Minute)

	before := len(f.runnables)
	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != before+1 {
		t.Fatalf("expected exactly 1 runnable registered, got %d (before=%d)", len(f.runnables)-before, before)
	}
	if _, ok := f.runnables[len(f.runnables)-1].(*MemoryStoreSweeper); !ok {
		t.Errorf("expected registered Runnable to be *MemoryStoreSweeper, got %T", f.runnables[len(f.runnables)-1])
	}
}

// Critical regression test: matches the in-tree pattern (research_agent.go
// embeds *core.BaseAgent in a custom struct). Without the reflection lookup,
// AutoRegisterMemorySweeper would silently no-op for these.
func TestFramework_AutoRegisterMemorySweeper_EmbeddedAgent(t *testing.T) {
	inner := NewBaseAgent("autoregister-embedded")
	wrapper := &wrappedAgent{BaseAgent: inner, domainField: "x"}
	f := newTestFrameworkWithComponent(t, wrapper, 5*time.Minute)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 1 {
		t.Fatalf("expected exactly 1 runnable registered for embedded-agent case, got %d", len(f.runnables))
	}
	sweeper, ok := f.runnables[0].(*MemoryStoreSweeper)
	if !ok {
		t.Fatalf("expected *MemoryStoreSweeper, got %T", f.runnables[0])
	}
	// The sweeper should be wired to the inner BaseAgent's Memory.
	innerStore, _ := inner.Memory.(*MemoryStore)
	if sweeper.store != innerStore {
		t.Errorf("sweeper.store does not point at the embedded agent's *MemoryStore")
	}
}

func TestFramework_AutoRegisterMemorySweeper_CustomMemory_NoOp(t *testing.T) {
	agent := NewBaseAgent("autoregister-custom-memory")
	agent.Memory = sentinelMemory{} // custom impl, NOT *MemoryStore
	f := newTestFrameworkWithComponent(t, agent, 5*time.Minute)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 0 {
		t.Errorf("expected no runnable registered when Memory is custom, got %d", len(f.runnables))
	}
}

func TestFramework_AutoRegisterMemorySweeper_Tool_NoOp(t *testing.T) {
	tool := NewTool("autoregister-tool")
	f := newTestFrameworkWithComponent(t, tool, 5*time.Minute)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 0 {
		t.Errorf("expected no runnable registered for *BaseTool, got %d", len(f.runnables))
	}
}

// embeddedTool wraps *BaseTool — the helper must also reject this case.
type embeddedTool struct {
	*BaseTool
}

func TestFramework_AutoRegisterMemorySweeper_EmbeddedTool_NoOp(t *testing.T) {
	tool := &embeddedTool{BaseTool: NewTool("autoregister-embedded-tool")}
	f := newTestFrameworkWithComponent(t, tool, 5*time.Minute)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 0 {
		t.Errorf("expected no runnable registered for struct embedding *BaseTool, got %d", len(f.runnables))
	}
}

func TestFramework_AutoRegisterMemorySweeper_NegativeInterval_NoOp(t *testing.T) {
	agent := NewBaseAgent("autoregister-negative")
	f := newTestFrameworkWithComponent(t, agent, -1*time.Second)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 0 {
		t.Errorf("expected no runnable registered for negative CleanupInterval, got %d", len(f.runnables))
	}
}

func TestFramework_AutoRegisterMemorySweeper_ZeroInterval_NoOp(t *testing.T) {
	agent := NewBaseAgent("autoregister-zero")
	f := newTestFrameworkWithComponent(t, agent, 0)

	f.AutoRegisterMemorySweeper()
	if len(f.runnables) != 0 {
		t.Errorf("expected no runnable registered for zero CleanupInterval, got %d", len(f.runnables))
	}
}

// TestNewFramework_PropagatesMemoryCleanupIntervalToTool locks in the
// FRAMEWORK_DESIGN_PRINCIPLES "Externalize Hardcoded Limits" contract for
// tool sweepers: setting TRUVAG3_MEMORY_CLEANUP_INTERVAL in the environment
// must reach a *BaseTool's Config.Memory.CleanupInterval after NewFramework
// runs, so example-tool main.go's pattern
// `core.NewMemoryStoreSweeper(tool.cache, tool.Config.Memory.CleanupInterval, ...)`
// actually honors the env var. Regression guard for the original PR-#1
// drift where tools hard-coded 10*time.Minute and silently ignored the var.
func TestNewFramework_PropagatesMemoryCleanupIntervalToTool(t *testing.T) {
	// Set the env var to a non-default value so we can distinguish "the var
	// was honored" from "we got the default by accident".
	const want = 7 * time.Minute
	t.Setenv("TRUVAG3_MEMORY_CLEANUP_INTERVAL", want.String())

	tool := NewTool("propagation-test")
	cfg := DefaultConfig()
	if err := cfg.LoadFromEnv(); err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	// Make a framework option that injects the env-loaded config — mirrors
	// what real callers do via core.WithConfig / per-field options.
	f, err := NewFramework(tool, func(c *Config) error {
		c.Memory = cfg.Memory
		return nil
	})
	if err != nil {
		t.Fatalf("NewFramework: %v", err)
	}
	_ = f

	if got := tool.Config.Memory.CleanupInterval; got != want {
		t.Errorf("tool.Config.Memory.CleanupInterval = %v, want %v "+
			"(TRUVAG3_MEMORY_CLEANUP_INTERVAL=%q must propagate through "+
			"LoadFromEnv → NewFramework → applyConfigToComponent → tool.Config)",
			got, want, want.String())
	}
}

// TestNewFramework_PropagatesMemoryCleanupIntervalToAgent — same contract
// for agents. AutoRegisterMemorySweeper reads from f.config (the framework's
// own copy), but Config-driven user code on the agent reads from
// agent.Config — both must agree.
func TestNewFramework_PropagatesMemoryCleanupIntervalToAgent(t *testing.T) {
	const want = 11 * time.Minute
	t.Setenv("TRUVAG3_MEMORY_CLEANUP_INTERVAL", want.String())

	agent := NewBaseAgent("propagation-test-agent")
	cfg := DefaultConfig()
	if err := cfg.LoadFromEnv(); err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	f, err := NewFramework(agent, func(c *Config) error {
		c.Memory = cfg.Memory
		return nil
	})
	if err != nil {
		t.Fatalf("NewFramework: %v", err)
	}
	if got := agent.Config.Memory.CleanupInterval; got != want {
		t.Errorf("agent.Config.Memory.CleanupInterval = %v, want %v", got, want)
	}
	if got := f.config.Memory.CleanupInterval; got != want {
		t.Errorf("framework.config.Memory.CleanupInterval = %v, want %v "+
			"(must agree with agent.Config; AutoRegisterMemorySweeper reads from here)",
			got, want)
	}
}
