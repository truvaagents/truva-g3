# Orchestration Modes Guide

Hey there! This guide will help you understand the different ways to orchestrate multi-tool workflows in TruvaG3. If you've ever wondered "Should I let the AI figure out which tools to use, or should I define the steps myself?" - this is the guide for you.

> **Working Example**
>
> Everything in this guide is demonstrated in a fully working implementation:
> - **Agent**: [`examples/agent-with-orchestration/`](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-orchestration)
>
> We recommend running the example alongside reading this guide. It makes everything click faster.

---

## Table of Contents

1. [What Are Orchestration Modes?](#1-what-are-orchestration-modes)
2. [Quick Reference](#2-quick-reference)
3. [Dynamic Mode: Let the AI Decide](#3-dynamic-mode-let-the-ai-decide)
   - 3.1 [When to Use It](#31-when-to-use-it)
   - 3.2 [How It Works](#32-how-it-works)
   - 3.3 [Try It](#33-try-it)
   - 3.4 [The Trade-off](#34-the-trade-off)
4. [Predefined Workflow Mode: The Reliable Recipe](#4-predefined-workflow-mode-the-reliable-recipe)
   - 4.1 [When to Use It](#41-when-to-use-it)
   - 4.2 [How Workflows Are Defined](#42-how-workflows-are-defined)
   - 4.3 [Try It](#43-try-it)
   - 4.4 [How It Executes](#44-how-it-executes)
   - 4.5 [Enabling AI Synthesis (Optional)](#45-enabling-ai-synthesis-optional)
5. [Custom Mode: Precise Control](#5-custom-mode-precise-control)
   - 5.1 [When to Use It](#51-when-to-use-it)
   - 5.2 [Try It](#52-try-it)
   - 5.3 [How It Works](#53-how-it-works)
   - 5.4 [Step Parameters](#54-step-parameters)
   - 5.5 [Important: No Dependencies](#55-important-no-dependencies)
6. [YAML Workflow Engine: Declarative Definitions](#6-yaml-workflow-engine-declarative-definitions)
   - 6.1 [When to Use It](#61-when-to-use-it)
   - 6.2 [Implementation Status](#62-implementation-status)
   - 6.3 [YAML Format](#63-yaml-format)
   - 6.4 [Programmatic Usage](#64-programmatic-usage)
7. [Choosing the Right Mode](#7-choosing-the-right-mode)
   - 7.1 [Mode Comparison](#71-mode-comparison)
   - 7.2 [The Journey: Dynamic → Workflow](#72-the-journey-dynamic--workflow)
8. [Troubleshooting](#8-troubleshooting)
   - 8.1 ["Tool not found in catalog"](#81-tool-not-found-in-catalog)
   - 8.2 [Steps executing in wrong order](#82-steps-executing-in-wrong-order-predefined-workflow)
   - 8.3 [Dynamic mode choosing wrong tools](#83-dynamic-mode-choosing-wrong-tools)
   - 8.4 [Custom mode steps failing silently](#84-custom-mode-steps-failing-silently)
9. [Further Reading](#9-further-reading)

---

## 1. What Are Orchestration Modes?

Think of orchestration like running a kitchen:

```
Dynamic Mode = Head chef decides what to cook based on customer's description
               "I want something Italian with seafood" → Chef picks the dishes

Predefined Workflow = Following a recipe card
               "Make the house special pasta" → Same steps every time

Custom Mode = Customer gives exact instructions
               "First boil water, then add pasta for 8 min" → You control every step
```

Each mode serves different needs. The AI-driven mode is flexible but costs tokens. The workflow modes are predictable and free of LLM planning overhead.

---

## 2. Quick Reference

| Mode | Endpoint | LLM Used For | Best For |
|------|----------|--------------|----------|
| **Dynamic** | `/orchestrate/natural` | Planning + Synthesis | Natural language queries |
| **Predefined** | `/orchestrate/travel-research` | Synthesis only (optional) | Production workflows |
| **Custom** | `/orchestrate/custom` | None | Testing, debugging |
| **YAML** | Programmatic | Synthesis only (optional) | Declarative configs |

---

## 3. Dynamic Mode: Let the AI Decide

This is the "smart" mode. You describe what you want in plain English, and the AI figures out which tools to call and in what order.

### 3.1 When to Use It

- **Chat interfaces** where users ask free-form questions
- **Exploratory use cases** where you don't know upfront which tools are needed
- **Rapid prototyping** before you know the optimal workflow

### 3.2 How It Works

```
User: "What's the weather in Tokyo and how much is $100 in Yen?"
         │
         ▼
    AI analyzes request
         │
         ▼
    AI discovers available tools (weather, currency, geocoding...)
         │
         ▼
    AI creates execution plan:
    1. geocode "Tokyo" → get coordinates
    2. weather-tool(lat, lon) → get weather
    3. currency-tool(USD, JPY, 100) → get conversion
         │
         ▼
    Execute steps (parallel when possible)
         │
         ▼
    AI synthesizes results into natural response
```

### 3.3 Try It

From [handlers.go:73](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-orchestration/handlers.go#L73):

```bash
curl -X POST http://localhost:8094/orchestrate/natural \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I am planning a trip to Paris. What is the weather like and what currency do they use?",
    "ai_synthesis": true
  }'
```

The orchestrator calls `ProcessRequest()` which:
1. Uses the LLM to generate an execution plan
2. Executes the plan (tools run in parallel where possible)
3. Synthesizes results into a coherent response

### 3.4 The Trade-off

Dynamic mode is powerful but has overhead:
- **LLM tokens** for plan generation (varies based on tool catalog size and query complexity)
- **Latency** for the planning step
- **Non-deterministic** - the AI might choose different tools for similar queries

If you find yourself using the same tool sequence repeatedly, consider switching to Predefined Workflow mode.

---

## 4. Predefined Workflow Mode: The Reliable Recipe

This is like having a recipe card. You define the exact steps once, and they execute the same way every time. No LLM is used for planning - only for synthesis (and that's optional).

### 4.1 When to Use It

- **Production pipelines** where reliability matters
- **Cost-sensitive deployments** (no LLM planning tokens)
- **Repeatable workflows** with known tool sequences
- **Compliance requirements** where you need predictable behavior

### 4.2 How Workflows Are Defined

Workflows are defined as Go structs in [research_agent.go:339-400](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-orchestration/research_agent.go#L339-L400):

```go
t.workflows["travel-research"] = &TravelWorkflow{
    Name:        "travel-research",
    Description: "Comprehensive travel research",
    Steps: []WorkflowStep{
        {
            ID:          "geocode",
            ToolName:    "geocoding-tool",
            Capability:  "geocode_location",
            Parameters:  map[string]interface{}{"location": "{{destination}}"},
        },
        {
            ID:          "weather",
            ToolName:    "weather-tool-v2",
            Capability:  "get_current_weather",
            DependsOn:   []string{"geocode"},  // Waits for geocode to complete
            Parameters:  map[string]interface{}{
                "lat": "{{geocode.response.data.lat}}",
                "lon": "{{geocode.response.data.lon}}",
            },
        },
        // ... more steps
    },
}
```

Notice the `DependsOn` field - this creates a DAG (Directed Acyclic Graph). Steps without dependencies run in parallel automatically.

### 4.3 Try It

```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{
    "destination": "Tokyo, Japan",
    "country": "Japan",
    "base_currency": "USD",
    "amount": 1000
  }'
```

### 4.4 How It Executes

From [handlers.go:224-234](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-orchestration/handlers.go#L224-L234):

```go
// Convert workflow to routing plan
plan := t.workflowToRoutingPlan(workflow, req.Parameters)

// Execute directly - NO LLM planning step
result, err := t.orchestrator.ExecutePlan(ctx, plan)
```

The `ExecutePlan()` method skips plan generation entirely. It takes your predefined steps and executes them with intelligent parallelization:

```
geocode ──┬──> weather
          │
          │    country-info ──> currency
          │
          └──> news (runs in parallel with everything)
```

### 4.5 Enabling AI Synthesis (Optional)

By default, you get raw tool results. If you want the AI to combine them into a natural response:

```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{
    "destination": "Tokyo",
    "ai_synthesis": true
  }'
```

This uses LLM tokens only for synthesis, not planning.

---

## 5. Custom Mode: Precise Control

This is for when you need exact control over what happens. You define the steps inline in your request - no predefined workflows, no LLM planning.

### 5.1 When to Use It

- **Testing** - Verify a specific tool works before using it in workflows
- **Debugging** - Isolate issues by calling tools with known parameters
- **Integration tests** - Test exact tool chains in CI/CD
- **Ad-hoc queries** - When you know exactly what you need

### 5.2 Try It

```bash
curl -X POST http://localhost:8094/orchestrate/custom \
  -H "Content-Type: application/json" \
  -d '{
    "steps": [
      {
        "tool": "geocoding-tool",
        "capability": "geocode_location",
        "params": {"location": "Paris, France"}
      },
      {
        "tool": "weather-tool-v2",
        "capability": "get_current_weather",
        "params": {"lat": 48.8566, "lon": 2.3522}
      }
    ]
  }'
```

### 5.3 How It Works

From [handlers.go:368-390](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-orchestration/handlers.go#L368-L390), the handler builds a routing plan from your inline steps:

```go
plan := &orchestration.RoutingPlan{
    PlanID:          fmt.Sprintf("custom-%d", time.Now().UnixNano()),
    OriginalRequest: "Custom workflow execution",
    Mode:            orchestration.ModeWorkflow,
}

for i, step := range steps {
    plan.Steps = append(plan.Steps, orchestration.RoutingStep{
        StepID:    fmt.Sprintf("step-%d", i+1),
        AgentName: step["tool"].(string),
        Metadata: map[string]interface{}{
            "capability": step["capability"],
            "parameters": step["params"],
        },
    })
}

result, err := t.orchestrator.ExecutePlan(ctx, plan)
```

### 5.4 Step Parameters

| Field | Required | Description |
|-------|----------|-------------|
| `tool` | Yes | Tool name as registered in Redis discovery |
| `capability` | Yes | The specific capability/action to invoke |
| `params` | No | Parameters to pass to the tool |

### 5.5 Important: No Dependencies

Custom mode executes steps **sequentially** in the order you provide them. There's no `depends_on` support - if you need parallel execution or complex dependencies, use Predefined Workflow mode instead.

---

## 6. YAML Workflow Engine: Declarative Definitions

The orchestration module includes a full YAML-based workflow engine. This lets you define workflows declaratively in YAML files instead of Go code.

### 6.1 When to Use It

- **Non-developers** defining workflows
- **Version-controlled** workflow configurations
- **Runtime-configurable** workflows without recompilation

### 6.2 Implementation Status

The `WorkflowEngine` is fully implemented in the orchestration module:
- `ParseWorkflowYAML()` - Parses YAML into executable workflow definitions
- `ExecuteWorkflow()` - Executes workflows with full DAG support

**However**, the `agent-with-orchestration` example currently uses Go structs instead of YAML files. See the TODO in its [README](https://github.com/truvaagents/truva-g3/blob/main/examples/agent-with-orchestration/README.md#todo).

### 6.3 YAML Format

```yaml
name: analyze-stock
version: "1.0"

inputs:
  symbol:
    type: string
    required: true

steps:
  - name: get-price
    tool: stock-price-tool
    action: get_current_price
    inputs:
      symbol: ${inputs.symbol}

  - name: analyze
    agent: technical-analyst
    action: analyze
    inputs:
      price_data: ${steps.get-price.output}
    depends_on: [get-price]

outputs:
  recommendation: ${steps.analyze.output.recommendation}
```

### 6.4 Programmatic Usage

```go
// Create the workflow engine
stateStore := orchestration.NewRedisStateStore(discovery)
engine := orchestration.NewWorkflowEngine(discovery, stateStore, logger)

// Load and parse YAML
yamlData, _ := os.ReadFile("workflow.yaml")
workflow, _ := engine.ParseWorkflowYAML(yamlData)

// Execute with inputs
inputs := map[string]interface{}{"symbol": "AAPL"}
execution, err := engine.ExecuteWorkflow(ctx, workflow, inputs)

// Get results
fmt.Printf("Result: %v\n", execution.Outputs["recommendation"])
```

> **Note**: The above is from the framework API. For a working example with YAML workflows, check back after the TODO in agent-with-orchestration is completed.

---

## 7. Choosing the Right Mode

Here's a decision tree to help you pick:

```
Is this a chat/conversational interface with free-form user input?
├── Yes → Dynamic Mode
└── No  → Is this a repeatable, production workflow?
    ├── Yes → Do you want YAML configuration files?
    │   ├── Yes → YAML Workflow Engine
    │   └── No  → Predefined Workflow Mode
    └── No  → Is this for testing or debugging?
        ├── Yes → Custom Mode
        └── No  → Start with Dynamic Mode, then optimize
```

### 7.1 Mode Comparison

| Aspect | Dynamic | Predefined | Custom | YAML |
|--------|---------|------------|--------|------|
| Plan source | LLM generates | Go structs | Inline JSON | YAML files |
| LLM tokens | Planning + synthesis | Synthesis only | None | Synthesis only |
| Parallelization | LLM-inferred | DAG-based | Sequential | DAG-based |
| Dependencies | Automatic | `DependsOn` field | None | `depends_on` field |
| Best for | Chat UIs | Production | Testing | Config-driven |

### 7.2 The Journey: Dynamic → Workflow

Many projects follow this path:

1. **Start with Dynamic Mode** - Let the AI figure things out while you're exploring
2. **Notice patterns** - "Users always ask about weather + currency + country info together"
3. **Create Predefined Workflow** - Codify the pattern for reliability and cost savings
4. **Use Custom Mode** - For debugging when something goes wrong

---

## 8. Troubleshooting

### 8.1 "Tool not found in catalog"

The orchestrator can't find the tool you're trying to call.

**Check:**
1. Is the tool running? `curl http://tool-host:port/health`
2. Is it registered in Redis? `redis-cli keys "truvag3:services:*"`
3. Is the tool name spelled correctly? (case-sensitive)

### 8.2 Steps executing in wrong order (Predefined Workflow)

**Check:** Your `DependsOn` field. Steps without dependencies run in parallel.

```go
// Wrong - weather runs before geocode completes
{ID: "weather", Parameters: map[string]interface{}{"lat": "{{geocode.lat}}"}}

// Right - weather waits for geocode
{ID: "weather", DependsOn: []string{"geocode"}, Parameters: ...}
```

### 8.3 Dynamic mode choosing wrong tools

The LLM picked tools that don't make sense for the query.

**Check:** Your prompt configuration in `InitializeOrchestrator()`. Add custom instructions:

```go
config.PromptConfig.CustomInstructions = []string{
    "For weather queries, always geocode the location first",
    "For currency queries, use country-info to get the local currency code",
}
```

### 8.4 Custom mode steps failing silently

**Check:** The response `steps` array for individual step errors:

```json
{
  "steps": [
    {"step_id": "step-1", "success": false, "error": "connection refused"}
  ]
}
```

---

## 9. Further Reading

| Topic | Document |
|-------|----------|
| Full orchestration module | [orchestration/README.md](https://github.com/truvaagents/truva-g3/blob/main/orchestration/README.md) |
| Async tasks (long-running) | [ASYNC_ORCHESTRATION_GUIDE.md](ASYNC_ORCHESTRATION_GUIDE.md) |
| Human-in-the-Loop approval | [HUMAN_IN_THE_LOOP_USER_GUIDE.md](HUMAN_IN_THE_LOOP_USER_GUIDE.md) |
| Error recovery (4-layer) | [INTELLIGENT_ERROR_HANDLING.md](INTELLIGENT_ERROR_HANDLING.md) |
| Working example | [examples/agent-with-orchestration](https://github.com/truvaagents/truva-g3/tree/main/examples/agent-with-orchestration) |

Happy orchestrating! If something doesn't make sense, run the `agent-with-orchestration` example and poke at the endpoints - it's the best way to understand how everything fits together.
