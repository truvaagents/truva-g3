# TruvaG3 Orchestration Module

Multi-agent coordination with AI-driven orchestration and declarative workflows.

## Table of Contents

1. [What Does This Module Do?](#1-what-does-this-module-do)
2. [Quick Start](#2-quick-start)
3. [How It Works](#3-how-it-works)
4. [AI Orchestration in Detail](#4-ai-orchestration-in-detail)
   - [LLM-Generated Execution Plan Structure](#llm-execution-plan)
5. [Workflow Engine in Detail](#5-workflow-engine-in-detail)
6. [When to Use Each Mode](#6-when-to-use-each-mode)
7. [Async Tasks for Long-Running Operations](#7-async-tasks-for-long-running-operations)
8. [Human-in-the-Loop (HITL) Approval](#8-human-in-the-loop-hitl-approval)
9. [Architecture & Design Decisions](#9-architecture--design-decisions)
10. [How Everything Fits Together](#10-how-everything-fits-together)
11. [Performance & Caching](#11-performance--caching-explained)
12. [Monitoring & Metrics](#12-monitoring--metrics---know-whats-happening)
13. [Configuration](#13-configuration)
14. [Usage Patterns](#14-usage-patterns)
15. [Requirements](#15-requirements)
16. [Scaling to Hundreds of Agents](#16-scaling-to-hundreds-of-agents---capability-provider-architecture)
17. [Performance Considerations](#17-performance-considerations)
18. [Streaming Support](#18-streaming-support)
19. [Potential Enhancements](#19-potential-enhancements)
20. [API Reference](#20-api-reference)
21. [Best Practices & Tips](#21-best-practices--tips)
22. [Production-Ready Enhancements](#22-production-ready-enhancements)
23. [Summary](#23-summary---what-youve-learned)

## 1. What Does This Module Do?

Think of this module as the **conductor of an orchestra**. Just like a conductor coordinates musicians to create beautiful music, this module coordinates multiple agents to accomplish complex tasks.

It provides two powerful ways to orchestrate agents and tools:

1. **AI Orchestration** - Tell it what you want in natural language, and AI figures out which tools and agents to call
2. **Workflow Engine** - Define step-by-step "recipes" that execute reliably every time
3. **Memory Pipeline Hooks** - Shared agent memory (`BuildMemoryHooks`) for cross-agent visibility, and per-user memory (`BuildUserMemoryHooks`) for personal assistant personalization

### Real-World Analogy: The Coffee Shop

Imagine running a coffee shop with different workers:
- **Barista** - Makes coffee (like a data processing tool)
- **Cashier** - Takes orders (like an API gateway tool)
- **Baker** - Makes pastries (like a report generator tool)

The orchestration module ensures:
1. The cashier takes the order
2. The barista and baker work **in parallel** (no waiting!)
3. Everything comes together for the customer

That's exactly how it coordinates your tools and agents!

## 2. Quick Start

### Installation

```go
import "github.com/truvaagents/truva-g3/orchestration"
```

### Two Ways to Orchestrate

#### Option 1: AI-Driven (Natural Language)
```go
// Just describe what you want in plain English
response, _ := orchestrator.ProcessRequest(ctx,
    "Analyze Tesla stock and summarize recent news",
    nil,
)

// Behind the scenes:
// 1. AI reads your request
// 2. Looks at available tools and agents (stock-tool, news-tool, analyzer-agent)
// 3. Creates an execution plan
// 4. Calls components in the right order
// 5. Combines results into a coherent response
```

#### Option 2: Workflow (Predictable Recipes)
```yaml
# Define exact steps like a recipe
name: analyze-stock
steps:
  - name: get-price          # Step 1: Get the data
    tool: stock-tool         # Using a tool (passive component)
    action: fetch_price
    
  - name: get-news           # Step 2: Get news (parallel with step 1!)
    tool: news-tool          # Another tool
    action: fetch_latest
    
  - name: analyze            # Step 3: Analyze everything
    agent: ai-analyzer       # Using an agent (active orchestrator)
    action: analyze
    inputs:
      price: ${steps.get-price.output}  # Use output from step 1
      news: ${steps.get-news.output}    # Use output from step 2
    depends_on: [get-price, get-news]   # Wait for both
```

## 3. How It Works

### The Two Orchestration Modes Explained

#### 1. AI Orchestration - The Smart Assistant
**How it works:** Like having a smart assistant who understands your request and figures out what to do.

```
Your Request: "Analyze Apple stock"
     ↓
1. AI understands: "User wants stock analysis"
     ↓
2. AI checks available components: "I have stock-price tool, news tool, and analyzer agent"
     ↓
3. AI creates plan: "First get price and news from tools, then analyze with agent"
     ↓
4. Executes plan: Calls tools and agents in parallel where possible
     ↓
5. AI synthesizes: Combines all responses into one answer
     ↓
Your Response: "Apple stock is up 3% today. Based on news about..."
```

#### 2. Workflow Engine - The Recipe Book
**How it works:** Like following a recipe - same steps every time, predictable results.

```
Workflow: Daily Report
     ↓
1. Read recipe: "Get sales, inventory, and customers"
     ↓
2. Execute in parallel: All three can run at once!
     ↓
3. Wait for dependencies: Report generator waits for all data
     ↓
4. Variable substitution: ${steps.sales.output} becomes actual data
     ↓
5. Return outputs: Structured result every time
```

### 🔧 Core Components Explained

| Component | What It Does | Real-World Analogy |
|-----------|--------------|-------------------|
| **Component Catalog** | Keeps track of all available tools and agents and what they can do | Like a phone book of workers and their skills |
| **Smart Executor** | Runs multiple tool/agent calls in parallel when possible | Like a project manager coordinating team members |
| **AI Synthesizer** | Combines responses from multiple tools and agents into one answer | Like an editor combining reporter stories into one article |
| **Workflow Engine** | Executes predefined step-by-step processes | Like a factory assembly line |
| **DAG Scheduler** | Figures out what can run in parallel | Like a smart scheduler who knows task dependencies |
| **Routing Cache** | Remembers recent decisions to speed things up | Like remembering phone numbers instead of looking them up |

## 4. AI Orchestration in Detail

### Step-by-Step: How AI Processes Your Request

#### Example: "Get me a comprehensive analysis of Tesla"

**Step 1: Understanding (Natural Language → Intent)**
```javascript
You say: "Get me a comprehensive analysis of Tesla"
          ↓
AI understands: {
  "intent": "analyze_company",
  "target": "Tesla",
  "scope": "comprehensive"
}
```

**Step 2: Discovery (Finding the Right Workers)**
```javascript
AI checks catalog:
✓ financial-tool     -> can get financials
✓ news-tool         -> can get news
✓ sentiment-agent    -> can analyze sentiment
✓ technical-agent    -> can do technical analysis

AI decides: "I'll use both tools and agents!"
```

**Step 3: Smart Planning (Creating Execution Order)**
```javascript
AI creates plan:
1. [Parallel Group 1]
   - financial-tool: get_financials("TSLA")
   - news-tool: get_recent_news("Tesla")
   
2. [Parallel Group 2] 
   - sentiment-agent: analyze(news_data)
   - technical-agent: analyze(financial_data)
   
3. [Final Step]
   - Synthesize all results into coherent analysis
```

**Step 4: Synthesis (Making Sense of Everything)**
```javascript
Tool and agent responses:
- Financial Tool: "Revenue $96B, up 35% YoY..."
- News Tool: "Tesla announces new factory..."
- Sentiment Agent: "72% positive sentiment..."
- Technical Agent: "RSI 65, bullish trend..."
          ↓
AI combines into:
"Tesla shows strong growth with $96B revenue (+35% YoY).
Recent factory announcement drives positive sentiment (72%).
Technical indicators suggest continued bullish trend.
Recommendation: Strong Buy"
```

### LLM-Generated Execution Plan Structure {#llm-execution-plan}

When the AI orchestrator processes a natural language request, the LLM generates a **DAG-based execution plan** in JSON format. This plan defines which tools/agents to call, their parameters, and dependencies between steps.

#### JSON Plan Structure

```json
{
  "plan_id": "travel-plan-1766115892559988547",
  "steps": [
    {
      "step_id": "step-1",
      "agent_name": "stock-service",
      "capability": "stock_quote",
      "description": "Get TSLA stock quote to calculate funds",
      "parameters": {"symbol": "TSLA"},
      "depends_on": []
    },
    {
      "step_id": "step-2",
      "agent_name": "country-info-tool",
      "capability": "get_country_info",
      "description": "Get Switzerland currency info",
      "parameters": {"country": "Switzerland"},
      "depends_on": []
    },
    {
      "step_id": "step-3",
      "agent_name": "currency-tool",
      "capability": "convert_currency",
      "description": "Convert USD to CHF using step-1 & step-2 data",
      "parameters": {
        "from": "USD",
        "to": "{{step-2.response.data.currency.code}}",
        "amount": "{{step-1.response.data.price}}"
      },
      "depends_on": ["step-1", "step-2"]
    },
    {
      "step_id": "step-4",
      "agent_name": "geocoding-tool",
      "capability": "geocode_location",
      "description": "Get Zurich coordinates for weather lookup",
      "parameters": {"location": "Zurich"},
      "depends_on": []
    },
    {
      "step_id": "step-5",
      "agent_name": "weather-tool-v2",
      "capability": "get_current_weather",
      "description": "Get Zurich weather using coordinates from step-4",
      "parameters": {
        "lat": "{{step-4.response.data.lat}}",
        "lon": "{{step-4.response.data.lon}}"
      },
      "depends_on": ["step-4"]
    },
    {
      "step_id": "step-6",
      "agent_name": "news-tool",
      "capability": "search_news",
      "description": "Search news about Zurich",
      "parameters": {"query": "Zurich", "max_results": 5},
      "depends_on": []
    }
  ]
}
```

#### Key Fields

| Field | Description |
|-------|-------------|
| `plan_id` | Unique identifier for the execution plan |
| `step_id` | Unique identifier for each step (used in `depends_on` references) |
| `agent_name` | The tool/agent to call (discovered via Redis) |
| `capability` | The specific capability/action to invoke |
| `parameters` | Input parameters, may include template references |
| `depends_on` | Array of step IDs that must complete before this step runs |

#### Template References

Parameters can reference outputs from previous steps using the template syntax:
- `{{step-N.response.data.field}}` - Access nested field from step N's response
- At execution time, templates are resolved with actual values from completed steps

#### DAG Visualization: Parallel Execution Groups

The executor analyzes `depends_on` to determine which steps can run in parallel:

```
┌─────────────────────────────────────────────────────────────────────┐
│  PARALLEL GROUP 1 (4 independent steps - no dependencies)          │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌─────────────┐  ┌─────────────────┐  ┌──────────────┐  ┌───────┐ │
│  │   step-1    │  │     step-2      │  │    step-4    │  │step-6 │ │
│  │ stock_quote │  │ get_country_info│  │geocode_location│ │ news │ │
│  │  (TSLA)     │  │  (Switzerland)  │  │   (Zurich)   │  │       │ │
│  └──────┬──────┘  └────────┬────────┘  └──────┬───────┘  └───────┘ │
│         │                  │                  │                     │
└─────────┼──────────────────┼──────────────────┼─────────────────────┘
          │                  │                  │
          ▼                  ▼                  ▼
┌─────────────────────────────────────────────────────────────────────┐
│  PARALLEL GROUP 2 (2 dependent steps - wait for dependencies)      │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│  ┌───────────────────────────────┐    ┌──────────────────────────┐ │
│  │           step-3              │    │         step-5           │ │
│  │     convert_currency          │    │   get_current_weather    │ │
│  │  depends_on: [step-1, step-2] │    │   depends_on: [step-4]   │ │
│  │                               │    │                          │ │
│  │  amount: {{step-1...price}}   │    │  lat: {{step-4...lat}}   │ │
│  │  to: {{step-2...currency}}    │    │  lon: {{step-4...lon}}   │ │
│  └───────────────────────────────┘    └──────────────────────────┘ │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │    RESPONSE     │
                    │  (all 6 steps   │
                    │   aggregated)   │
                    └─────────────────┘
```

**Execution Flow:**
1. **Group 1**: Steps 1, 2, 4, 6 execute concurrently (no dependencies)
2. **Group 2**: Steps 3, 5 execute concurrently after their dependencies complete
3. **Response**: All results are aggregated and optionally synthesized by AI

#### Intelligent Error Recovery

When a step fails (e.g., wrong parameters, validation errors), the executor uses a **4-layer error recovery system**:

| Layer | Component | Trigger | Capability |
|-------|-----------|---------|------------|
| **Layer 1** | Auto-Wirer | Before execution | Exact name matching, type coercion |
| **Layer 2** | Micro-Resolver | Before execution | LLM extracts values from source data |
| **Layer 3** | Error Analyzer | After 4xx error | LLM suggests parameter corrections |
| **Layer 4** | Semantic Retry | After Layer 3 fails | LLM computes derived values with full context |

**Example: Layer 4 Semantic Retry in Action**

```
User: "Sell 100 Tesla shares and convert to EUR"

Step 1: stock_quote("TSLA") → {price: 468.285}

Step 3: convert_currency({amount: 0}) → HTTP 400 "amount must be > 0"
        ↓
Layer 3: ErrorAnalyzer says "cannot fix - don't know the amount"
        ↓
Layer 4: ContextualReResolver sees:
         - User query: "sell 100 shares"
         - Source data: {price: 468.285}
         - Computes: 100 × 468.285 = 46,828.50
        ↓
Retry:  convert_currency({amount: 46828.5}) → HTTP 200 ✓
```

> 📖 **Full Details**: See [LLM-First Hybrid Parameter Resolution](#-production-ready-enhancements) for the complete 4-layer architecture.

#### Observing Plans in Jaeger

The execution plan and its execution are fully traced in Jaeger:

1. **Find the trace**: Use the `request_id` from your API response to search in Jaeger
2. **Plan generation span**: Look for `orchestrator.plan` span with attributes:
   - `plan_steps`: Number of steps in the plan
   - `ai.tokens_used`: Tokens consumed by LLM for planning
   - `ai.model`: Model used (e.g., `gpt-4o-mini`)
3. **Step execution spans**: Each `executor.step.*` span shows:
   - `step_id`: Which step executed
   - `agent_name`: Tool/agent called
   - `capability`: Action invoked
   - `duration`: Execution time
   - `status`: Success/failure
4. **Parallel execution**: Steps in the same group will have overlapping timestamps

**Example Jaeger URL:**
```
http://localhost:16686/trace/<trace_id>
```

**Key Metrics from Trace:**
| Metric | Where to Find |
|--------|---------------|
| Plan generation time | `orchestrator.plan` span duration |
| Total execution time | Root span duration |
| Per-step latency | Individual `executor.step.*` spans |
| LLM token usage | `ai.tokens_used` attribute |
| Parallelization efficiency | Compare overlapping step timestamps |

### Setting Up AI Orchestration

```go
// Step 1: Set up discovery (the registry for tools and agents)
// Set environment: export REDIS_URL="redis://localhost:6379"
discovery := core.NewRedisDiscovery(os.Getenv("REDIS_URL"))  // e.g., "redis://localhost:6379"

// Step 2: Set up AI (the brain)
aiClient, _ := ai.NewClient(
    ai.WithProvider("openai"),
    ai.WithAPIKey(apiKey),
)

// Step 3: Create orchestrator with dependencies
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,  // Required: Agent/tool discovery
    AIClient:  aiClient,   // Required: LLM for routing decisions
    // Optional dependencies (can be nil):
    // CircuitBreaker: cb,  // For sophisticated resilience
    // Logger: logger,      // For structured logging
    // Telemetry: telemetry,// For observability
}

config := orchestration.DefaultConfig()
config.CacheEnabled = true  // Remember recent decisions
config.ExecutionOptions.MaxConcurrency = 10  // Run up to 10 tools/agents at once

orchestrator, err := orchestration.CreateOrchestrator(config, deps)
if err != nil {
    log.Fatal(err)
}

// Step 4: Start the orchestrator
orchestrator.Start(ctx)

// Step 5: Just ask questions!
response, _ := orchestrator.ProcessRequest(ctx,
    "What's the weather and traffic like in NYC?",
    nil,
)
fmt.Println(response.Response)
// Output: "Current NYC weather is 72°F and sunny. 
//          Traffic is moderate with 25 min delays on I-95..."
```

#### Quick Start with Simple Orchestrator

```go
// For rapid prototyping - zero configuration required!
orchestrator := orchestration.CreateSimpleOrchestrator(discovery, aiClient)

// That's it! Start using immediately
response, _ := orchestrator.ProcessRequest(ctx,
    "Analyze Apple stock performance",
    nil,
)
```

## 5. Workflow Engine in Detail

### How Workflows Work - The Smart Recipe Executor

#### Understanding DAG Execution (It's Simpler Than It Sounds!)

**DAG = Directed Acyclic Graph** - Fancy words for "tasks with dependencies"

Think of it like cooking dinner:
```yaml
steps:
  - name: boil-water        # Can start immediately
  - name: chop-vegetables   # Can also start immediately (parallel!)
  - name: cook-pasta
    depends_on: [boil-water]  # Must wait for water
  - name: make-sauce
    depends_on: [chop-vegetables]  # Must wait for veggies
  - name: combine-dish
    depends_on: [cook-pasta, make-sauce]  # Waits for both
```

The workflow engine automatically figures out:
```
[boil-water]  [chop-vegetables]  <- These run in parallel!
      ↓              ↓
[cook-pasta]   [make-sauce]       <- These also run in parallel!
      \            /
       [combine-dish]             <- This waits for both
```

### Three Powerful Discovery Methods

#### 1. Direct Component Discovery
```yaml
steps:
  - name: get-price
    tool: stock-price-tool  # "I want THIS specific tool"
    action: fetch_price
```

#### 2. Capability-Based Discovery  
```yaml
steps:
  - name: analyze-text
    capability: sentiment_analysis  # "I need ANY component that can do this"
    action: analyze
    # Engine finds available tools/agents: sentiment-tool-v1, sentiment-agent-v2, etc.
    # Picks the best one (healthy, lowest load)
```

#### 3. Dynamic Component Discovery
```yaml
# No hardcoded URLs needed!
# Workflow says: "I need financial-advisor-agent" or "I need stock-price-tool"
# Discovery returns: "It's at http://10.0.0.5:8080" 
# But if it moves to http://10.0.0.9:9090, workflow still works!
```

### Complete Workflow Example: Investment Analysis

```yaml
name: investment-analysis
version: "1.0"
description: Analyze a stock for investment decisions

inputs:
  symbol:
    type: string
    required: true
    description: Stock symbol (e.g., AAPL, TSLA)

steps:
  # Phase 1: Gather Data (all run in parallel!)
  - name: get-price
    tool: market-data-tool     # Tool for data fetching
    action: fetch_price
    inputs:
      symbol: ${inputs.symbol}
    timeout: 5s
    
  - name: get-news
    capability: news_aggregation  # Find ANY news tool or agent
    action: fetch_recent
    inputs:
      query: ${inputs.symbol}
      limit: 10
    
  - name: get-sentiment
    tool: social-sentiment-tool  # Tool for sentiment data
    action: analyze
    inputs:
      symbol: ${inputs.symbol}
    retry:  # Handle flaky services
      max_attempts: 3
      backoff: exponential
      initial_wait: 1s
    
  # Phase 2: Analysis (waits for data, then parallel)
  - name: technical-analysis
    agent: technical-analyzer    # Agent for complex analysis
    action: analyze_technicals
    inputs:
      price_data: ${steps.get-price.output}
    depends_on: [get-price]
    
  - name: news-analysis
    agent: ai-news-analyzer      # Agent for intelligent analysis
    action: analyze_impact
    inputs:
      articles: ${steps.get-news.output}
      sentiment: ${steps.get-sentiment.output}
    depends_on: [get-news, get-sentiment]
    
  # Phase 3: Generate Report (waits for all analysis)
  - name: final-report
    agent: ai-advisor           # Agent for orchestration and synthesis
    action: generate_recommendation
    inputs:
      price: ${steps.get-price.output}
      technical: ${steps.technical-analysis.output}
      news_impact: ${steps.news-analysis.output}
    depends_on: [technical-analysis, news-analysis]

outputs:
  recommendation: ${steps.final-report.output.action}  # BUY/SELL/HOLD
  confidence: ${steps.final-report.output.confidence}   # 0-100%
  report: ${steps.final-report.output.summary}

on_error:
  strategy: continue  # Keep going even if one service fails
```

### Using Workflows in Code

```go
// Step 1: Create the workflow engine
stateStore := orchestration.NewRedisStateStore(discovery)
engine := orchestration.NewWorkflowEngine(discovery, stateStore, logger)

// Step 2: Load your workflow (from file or string)
yamlData, _ := os.ReadFile("investment-analysis.yaml")
workflow, _ := engine.ParseWorkflowYAML(yamlData)

// Step 3: Execute with inputs
inputs := map[string]interface{}{
    "symbol": "AAPL",
}

execution, err := engine.ExecuteWorkflow(ctx, workflow, inputs)
if err != nil {
    log.Printf("Workflow failed: %v", err)
    return
}

// Step 4: Use the results!
fmt.Printf("Recommendation: %s\n", execution.Outputs["recommendation"])
fmt.Printf("Confidence: %.0f%%\n", execution.Outputs["confidence"])
fmt.Printf("Analysis: %s\n", execution.Outputs["report"])

// Step 5: Check what happened (optional)
for stepName, step := range execution.Steps {
    fmt.Printf("Step %s: %s (took %v)\n", 
        stepName, step.Status, step.EndTime.Sub(*step.StartTime))
}
```

### How Variables Work - Data Flow Between Steps

```yaml
# Variables let steps share data, like passing ingredients in cooking!

steps:
  - name: step-one
    tool: data-fetcher          # Tool fetches data
    action: get_data
    # This step produces output
    
  - name: step-two
    agent: processor            # Agent processes data
    action: process
    inputs:
      data: ${steps.step-one.output}  # Uses output from step-one!
      # At runtime, this becomes the actual data
    depends_on: [step-one]

# You can also use:
# ${inputs.fieldName}           - Input parameters
# ${steps.stepName.output}      - Full output object
# ${steps.stepName.output.field} - Specific field from output
```

## 5.1 When to Use the Orchestration Module

Use this module when you need capabilities beyond basic tool/agent coordination:

| Capability | Without Orchestration | With Orchestration |
|------------|----------------------|-------------------|
| Tool discovery | Manual HTTP calls | Automatic via registry |
| Error handling | Fail or basic retry | **AI analyzes error, corrects parameters** |
| Multi-tool workflows | Custom coordination code | Declarative YAML or AI-planned |
| Parameter generation | Manual payload construction | **LLM-generated from natural language** |
| Retry logic | Simple exponential backoff | **Semantic retry with parameter correction** |

### Key Features You Get

1. **Intelligent Error Recovery (Layer 4 Semantic Retry)**
   - LLM analyzes why a tool call failed
   - Automatically corrects parameters (e.g., "Flower Mound, TX" → "Flower Mound, US")
   - Retries with corrected payload

2. **AI-Powered Orchestration**
   - Natural language request processing
   - Dynamic tool/agent selection
   - Intelligent parameter binding

3. **Workflow Engine**
   - Declarative YAML workflows
   - Parallel and sequential execution
   - Template variable interpolation

> **Note**: If your agent only needs basic tool calls without intelligent retry, you can use `core` directly. For smarter error recovery, use this module.

## 6. When to Use Each Mode

### Use AI Orchestration When:
- Processing natural language requests
- Tool/agent selection needs to be dynamic
- Tasks require intelligent routing decisions
- Exploring new tool and agent combinations

### Use Workflows When:
- Processes are well-defined and repeatable
- You need guaranteed execution order
- Predictable performance is important
- Avoiding LLM costs for routine tasks

## 7. Async Tasks for Long-Running Operations

### The Problem: When AI Takes Too Long

Imagine you walk into a restaurant and order a slow-cooked brisket. The waiter doesn't make you stand at the counter for 6 hours waiting—they give you a ticket number and tell you they'll let you know when it's ready.

The same principle applies to AI orchestration. When your workflow involves:
- Multiple tool calls executed in sequence or parallel
- AI reasoning that takes 30+ seconds
- External API calls with unpredictable latency
- Complex research tasks spanning several services

...the HTTP connection might timeout before you get results. Your client is left wondering: "Did it fail? Is it still running?"

### The Solution: HTTP 202 + Polling Pattern

TruvaG3 provides an async task system that works like that restaurant ticket:

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Async Task Flow                                 │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│   1. Client submits task    →  HTTP 202 + Task ID (instant)        │
│   2. Worker processes task  →  Background (takes as long as needed) │
│   3. Client polls status    →  GET /tasks/{id} (progress updates)   │
│   4. Task completes         →  Results available in response        │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

The framework provides two components:
- **TaskAPIHandler**: Accepts task submissions, returns HTTP 202 immediately
- **TaskWorkerPool**: Processes tasks in background with configurable concurrency

### When to Use Async Tasks

| Scenario | Sync (Regular HTTP) | Async (Task System) |
|----------|---------------------|---------------------|
| Single tool call (< 5s) | ✅ | Overkill |
| Multi-tool orchestration (< 30s) | ✅ | Optional |
| Complex AI research (30s - 5min) | ⚠️ Risky | ✅ Recommended |
| Batch processing or pipelines | ❌ Timeout likely | ✅ Required |

### Quick Example

```go
// Register an async-capable handler
workerPool.RegisterHandler("research", func(ctx context.Context, task *core.Task, reporter core.ProgressReporter) error {
    // Report progress as you go
    reporter.Report(&core.TaskProgress{
        CurrentStep: 1,
        TotalSteps:  3,
        Message:     "Calling weather service...",
    })

    // Use orchestrator for AI-driven tool selection
    response, err := orchestrator.ProcessRequest(ctx, task.Input["query"].(string), nil)
    if err != nil {
        return err
    }

    task.Result = response
    return nil
})
```

📖 **For complete implementation details, deployment patterns, and production configuration, see the [Async Orchestration Guide](../docs/orchestration/ASYNC_ORCHESTRATION_GUIDE.md).**

## 8. Human-in-the-Loop (HITL) Approval

For sensitive operations, the orchestration module supports pausing execution and waiting for human approval before proceeding.

### When to Use HITL

| Scenario | HITL Recommended |
|----------|------------------|
| Read-only queries (weather, stock prices) | ❌ No |
| Financial transactions (transfers, payments) | ✅ Yes |
| Data modifications (delete, bulk update) | ✅ Yes |
| Sensitive API calls (external integrations) | ✅ Yes |
| AI-generated execution plans | Optional (configurable) |

### Quick Start

```go
// 1. Create checkpoint store (Redis-backed for distributed deployments)
checkpointStore, _ := orchestration.NewRedisCheckpointStore(
    orchestration.WithCheckpointStoreRedisURL(redisURL),
)

// 2. Create webhook handler to notify your approval system
commandStore, _ := orchestration.NewRedisCommandStore(
    orchestration.WithCommandStoreRedisURL(redisURL),
)
handler := orchestration.NewWebhookInterruptHandler(
    "https://your-service/hitl/webhook",
    commandStore,
)

// 3. Define policy: which operations need approval
policy := orchestration.NewRuleBasedPolicy(orchestration.HITLConfig{
    RequirePlanApproval:   true,                     // Approve all AI plans
    SensitiveCapabilities: []string{"transfer_funds", "delete_account"},
    SensitiveAgents:       []string{"payment-service"},
    DefaultTimeout:        5 * time.Minute,
    DefaultAction:         orchestration.CommandReject, // Reject on timeout
})

// 4. Create controller and attach to orchestrator
controller := orchestration.NewInterruptController(policy, checkpointStore, handler)
orchestrator := orchestration.NewAIOrchestrator(config, discovery, aiClient,
    orchestration.WithHITL(controller),
)
```

### Four Interrupt Points

HITL can pause execution at different stages:

| Interrupt Point | Triggered When | Use Case |
|-----------------|----------------|----------|
| `before_plan_execution` | AI generates a plan | Approve overall strategy |
| `before_step` | Before each tool call | Approve individual operations |
| `after_step` | After tool returns | Validate output before proceeding |
| `on_error` | Errors exceed retry threshold | Human escalation |

### Approval Flow

```
1. Execution reaches checkpoint → Creates ExecutionCheckpoint
2. Webhook notification sent to your system
3. Human reviews and responds:
   POST /hitl/command
   {"checkpoint_id": "...", "type": "approve"} // or "reject", "abort", "modify"
4. Resume execution:
   POST /hitl/resume/{checkpoint_id}
5. Orchestrator continues or stops based on command
```

### Auto-Resume on Timeout

Configure automatic behavior when humans don't respond:

```bash
# Enable expiry processor (background goroutine)
export TRUVAG3_HITL_EXPIRY_ENABLED=true
export TRUVAG3_HITL_EXPIRY_INTERVAL=10s

# Default action on timeout: approve, reject, or abort
export TRUVAG3_HITL_DEFAULT_ACTION=reject
```

📖 **For complete HITL documentation including multi-pod deployment, metrics, and troubleshooting, see the [Human-in-the-Loop User Guide](../docs/orchestration/HUMAN_IN_THE_LOOP_USER_GUIDE.md).**

## 8.1 Scheduled Task Execution

The `orchestration/` package ships the consumer-side primitives for the scheduled-execution subsystem: `TaskConsumer` reference implementations (`RedisTaskConsumer` default + `RedisStreamsTaskConsumer` alternative), the `RegisterScheduledEndpoint` helper agents use to receive scheduled tasks, and the `SchedulerBackends` factory that bundles producer + consumer backends.

**One-line agent integration:**

```go
orchestration.RegisterScheduledEndpoint(agent.BaseAgent, orchestratorFn)
```

This mounts `/api/v1/scheduled` on the agent. The centralized `scheduled-executor` service dispatches scheduled tasks to this endpoint when they fire.

**New interfaces in `core/`:** `TaskConsumer` + `TaskHandle` (borrow-then-settle pattern). Contract testing via `core/conformance/RunTaskConsumerConformance`.

📖 **For the full story -- architecture, delivery semantics, observability, troubleshooting, and extending to non-Redis backends -- see the [Scheduled Tasks Guide](../docs/orchestration/SCHEDULED_TASKS_GUIDE.md).**

## 9. Architecture & Design Decisions

### Why Orchestration Doesn't Import the AI Module

**Critical Design Decision**: The orchestration module uses `core.AIClient` interface instead of importing the `ai` module directly. This is intentional and follows the framework's "Zero Framework Dependencies" principle.

#### The Dependency Injection Pattern

```go
// ❌ NEVER DO THIS - Violates architectural principles
// orchestration/orchestrator.go
import "github.com/truvaagents/truva-g3/ai"  // FORBIDDEN: Module importing module

// ✅ THIS IS CORRECT - Interface-based dependency injection
import "github.com/truvaagents/truva-g3/core"  // Only import core

type AIOrchestrator struct {
    aiClient core.AIClient  // Uses interface from core, NOT ai module
}

func NewAIOrchestrator(config *OrchestratorConfig, discovery core.Discovery, aiClient core.AIClient) *AIOrchestrator {
    // AIClient is INJECTED as parameter, not created internally
    return &AIOrchestrator{
        aiClient: aiClient,  // Dependency injection
    }
}
```

#### How Applications Wire Everything Together

The application layer is responsible for creating both the AI client and orchestrator:

```go
// main.go - Application wires components together
import (
    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/ai"            // App imports ai
    "github.com/truvaagents/truva-g3/orchestration" // App imports orchestration
)

func main() {
    // Step 1: Create AI client (from ai module)
    aiClient, _ := ai.NewClient(
        ai.WithProvider("openai"),
        ai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
    )

    // Step 2: Pass AI client to orchestrator (dependency injection)
    orchestrator := orchestration.CreateSimpleOrchestrator(discovery, aiClient)

    // The orchestrator now has AI capabilities WITHOUT importing ai module!
}
```

#### Benefits of This Design

1. **True Modularity**: Orchestration can work with ANY implementation of `core.AIClient`:
   ```go
   // Use ai module's implementation
   aiClient := ai.NewClient(...)

   // OR use a custom implementation
   aiClient := mycompany.NewCustomAIClient(...)

   // OR use a mock for testing
   aiClient := &MockAIClient{}

   // Orchestration doesn't care - just uses the interface
   orchestrator := orchestration.NewAIOrchestrator(config, discovery, aiClient)
   ```

2. **No Circular Dependencies**: Modules only import core, never each other:
   ```
   orchestration → core ← ai
   telemetry    → core ← resilience
   ui           → core

   (No direct connections between optional modules)
   ```

3. **Testing Isolation**: Test orchestration without the ai module:
   ```go
   // orchestration/factory_test.go
   func TestOrchestrator(t *testing.T) {
       aiClient := NewMockAIClient()  // Mock implementation
       orchestrator := CreateSimpleOrchestrator(discovery, aiClient)
       // Test orchestration logic without real AI calls
   }
   ```

4. **Provider Flexibility**: Switch AI providers without touching orchestration code:
   ```go
   // Today: Using OpenAI
   aiClient := ai.NewClient(ai.WithProvider("openai"))

   // Tomorrow: Switch to Anthropic
   aiClient := ai.NewClient(ai.WithProvider("anthropic"))

   // Next week: Use your private LLM
   aiClient := mycompany.PrivateLLMClient()

   // Orchestration code remains unchanged!
   ```

#### The Dependency Flow

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│     core     │     │     core     │     │     core     │
│              │     │              │     │              │
│ Defines:     │     │ Defines:     │     │ Defines:     │
│ - AIClient   │     │ - Discovery  │     │ - Telemetry  │
│   interface  │     │   interface  │     │   interface  │
└──────▲───────┘     └──────▲───────┘     └──────▲───────┘
       │                    │                    │
       ├────────────────────┼────────────────────┤
       │                    │                    │
┌──────┴───────┐     ┌──────┴───────┐     ┌──────┴───────┐
│      ai      │     │orchestration │     │  telemetry   │
│              │     │              │     │              │
│ Implements:  │     │    Uses:     │     │ Implements:  │
│ - AIClient   │     │ - AIClient   │     │ - Telemetry  │
└──────────────┘     │ - Discovery  │     └──────────────┘
                     └──────────────┘

Note: orchestration NEVER imports ai or telemetry directly!
```

#### Comparison with Telemetry Pattern

This follows the exact same pattern as telemetry integration:

| Aspect | Telemetry Pattern | AI Pattern |
|--------|------------------|------------|
| **Interface** | `core.Telemetry` | `core.AIClient` |
| **Implementation** | `telemetry` module | `ai` module |
| **Usage** | Components have `Telemetry` field | Orchestrator has `AIClient` field |
| **Initialization** | App calls `telemetry.Initialize()` | App creates `ai.NewClient()` |
| **Injection** | Set via `SetTelemetry()` | Pass via constructor |

#### Summary

This design is a textbook example of the **Dependency Inversion Principle**:
- High-level modules (orchestration) depend on abstractions (`core.AIClient`)
- Not on concrete implementations (`ai` module)
- This maintains architectural purity and enables true modularity

## 10. How Everything Fits Together

### The Orchestra Metaphor - Complete Picture

The orchestration module offers **two modes** - the application chooses which to use:

```
                    ┌─────────────────────────────────────────────┐
                    │          🎭 User Request                     │
                    │     "Analyze Tesla and recommend action"     │
                    └──────────────────┬──────────────────────────┘
                                       │
                         ┌─────────────┴─────────────┐
                         │   Application chooses     │
                         │   which mode to use       │
                         └─────────────┬─────────────┘
                                       │
          ┌────────────────────────────┴────────────────────────────┐
          │                                                         │
          ▼                                                         ▼
┌──────────────────────┐                              ┌──────────────────────┐
│  🤖 AI Orchestrator  │                              │  📋 Workflow Engine  │
│  ProcessRequest()    │                              │  ExecuteWorkflow()   │
│                      │                              │                      │
│  "I'll figure out    │                              │  "I'll follow the    │
│   what to call"      │                              │   predefined recipe" │
└──────────┬───────────┘                              └──────────┬───────────┘
           │                                                     │
           ▼                                                     │
┌──────────────────────┐                                         │
│  🧠 LLM Planner      │                                         │
│                      │                                         │
│  Generates DAG-based │                                         │
│  execution plan      │                                         │
└──────────┬───────────┘                                         │
           │                                                     │
           └────────────────────┬────────────────────────────────┘
                                │
                      ┌─────────▼──────────┐
                      │   🎯 Executor      │ (The Stage Manager)
                      │                    │
                      │ Calls agents in    │
                      │ parallel when      │
                      │ possible           │
                      └─────────┬──────────┘
                                │
               ┌────────────────┼────────────────┐
               │                │                │
          ┌────▼───┐       ┌────▼───┐       ┌────▼───┐
          │  Tool  │       │  Tool  │       │ Agent  │ (The Musicians)
          │   A    │       │   B    │       │   C    │
          └────┬───┘       └────┬───┘       └────┬───┘
               │                │                │
               └────────────────┼────────────────┘
                                │
                      ┌─────────▼──────────┐
                      │  🎨 Synthesizer    │ (The Editor)
                      │                    │
                      │ Combines all       │
                      │ responses into     │
                      │ one answer         │
                      └─────────┬──────────┘
                                │
                      ┌─────────▼──────────┐
                      │   📜 Response      │
                      │                    │
                      │ "Tesla: BUY        │
                      │  Confidence: 85%   │
                      │  Reasons: ..."     │
                      └────────────────────┘
```

**Key difference:**
- **AI Orchestrator**: LLM dynamically generates the execution plan based on discovered capabilities
- **Workflow Engine**: Uses a predefined workflow definition (no LLM planning step)

### How Components Work Together

| Component | Role | Real-World Analogy |
|-----------|------|-------------------|
| **Discovery Service** | Finds where tools and agents live | Like DNS for your components |
| **Component Catalog** | Knows what each tool/agent can do | Like LinkedIn profiles for components |
| **Routing Cache** | Remembers recent decisions | Like muscle memory |
| **Executor** | Runs tools and agents efficiently | Like a project manager |
| **State Store** | Tracks workflow progress | Like a progress tracker |

## 11. Performance & Caching Explained

### Why Caching Matters - The Restaurant Analogy

Imagine a restaurant where:
- **Without cache**: Every order requires calling suppliers to check prices (slow!)
- **With cache**: The menu has today's prices ready (fast!)

### Two Smart Caching Strategies

#### 1. Time-Based Cache (SimpleCache)
**How it works**: Like milk with an expiration date
```go
// "Remember this for 5 minutes"
cache.Set("tesla-analysis", result, 5*time.Minute)

// Later...
if cached := cache.Get("tesla-analysis"); cached != nil {
    return cached  // Instant response!
}
```

#### 2. LRU Cache (Least Recently Used)
**How it works**: Like a small notebook - when full, erase the oldest unused notes
```go
// Cache holds 100 most recent items
lruCache := NewLRUCache(100)

// Automatically removes least-used items when full
lruCache.Set("apple-data", data)  // Might remove "old-company-data"
```

### Configuring Cache for Your Needs

```go
config := orchestration.DefaultConfig()

// For frequently changing data (stock prices)
config.CacheEnabled = true
config.CacheTTL = 1 * time.Minute  // Short cache

// For stable data (company profiles)
config.CacheEnabled = true  
config.CacheTTL = 1 * time.Hour  // Long cache

// For real-time critical systems
config.CacheEnabled = false  // No cache, always fresh
```

## 12. Monitoring & Metrics - Know What's Happening

### Understanding Your System's Health

Think of metrics like your car's dashboard - they tell you if everything's running smoothly!

#### Key Metrics Explained

| Metric | What It Tells You | Why You Care |
|--------|------------------|--------------|
| **Total Requests** | How busy is your system? | Capacity planning |
| **Success Rate** | Are things working? | System health |
| **Average Latency** | How fast are responses? | User experience |
| **Component Failures** | Which tools/agents are struggling? | Troubleshooting |
| **Cache Hit Rate** | Is caching helping? | Performance tuning |

### Using Metrics in Practice

```go
// Get current metrics
metrics := orchestrator.GetMetrics()

// Check system health
successRate := float64(metrics.SuccessfulRequests) / float64(metrics.TotalRequests) * 100
if successRate < 95 {
    alert("Success rate below 95%!")
}

// Monitor performance
if metrics.AverageLatency > 5*time.Second {
    alert("System is slow!")
}

// Track specific workflows
fmt.Printf("Investment Analysis Workflow:\n")
fmt.Printf("  Executions: %d\n", metrics.WorkflowExecutions["investment-analysis"])
fmt.Printf("  Avg Duration: %v\n", metrics.WorkflowAvgDuration["investment-analysis"])
```

### Debugging with Metrics

```go
// When things go wrong, metrics help you find the problem:
if metrics.ComponentCallsFailed > 10 {
    // Check which tools/agents are failing
    for component, failures := range metrics.ComponentFailures {
        if failures > 5 {
            fmt.Printf("Component %s is having issues (%d failures)\n", component, failures)
        }
    }
}
```

## 13. Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_ORCHESTRATION_TIMEOUT` | `120s` | HTTP client timeout for tool/agent calls. For long-running AI workflows, set higher values (e.g., `5m`, `10m`). Uses Go duration format. |
| `TRUVAG3_OAUTH_TOKEN` | (empty) | Bearer token for service-to-service authentication. Per-request tokens via `WithOAuthToken(ctx, token)` take priority. When set, all outbound HTTP calls include `Authorization: Bearer <token>`. |
| *(no env var)* | N/A | Custom header propagation via `config.PropagatedHeaders` (config-level) or `WithPropagatedHeaders(ctx, headers)` (per-request). Context headers override config headers on key conflict. Reserved headers (`Authorization`, `Content-Type`, `X-TruvaG3-*`) are protected. |
| `TRUVAG3_TIERED_RESOLUTION_ENABLED` | `true` | Enable tiered capability resolution for LLM token optimization. Automatically selects relevant tools before plan generation. |
| `TRUVAG3_TIERED_MIN_TOOLS` | `20` | Minimum tool count to trigger tiered resolution. Below this threshold, all tools are sent directly. |
| `TRUVAG3_LLM_DEBUG_ENABLED` | `false` | Enable LLM debug payload capture for production debugging |
| `TRUVAG3_LLM_DEBUG_TTL` | `24h` | TTL for successful debug records |
| `TRUVAG3_LLM_DEBUG_ERROR_TTL` | `168h` | TTL for error debug records (7 days) |
| `TRUVAG3_LLM_DEBUG_REDIS_DB` | `7` | Redis database index for debug storage |
| `TRUVAG3_ITERATIVE_PLANNING_ENABLED` | `true` | Enable multi-phase iterative planning. When enabled, the LLM planner can generate partial plans that execute in phases. Uses a tiered approach: Phase 1 discovers, Phase 2+ acts on results with context-aware tool re-selection. |
| `TRUVAG3_ITERATIVE_MAX_PHASES` | `5` | Maximum planning phases per request. Most queries need at most 2. |
| `TRUVAG3_ITERATIVE_MAX_TOTAL_STEPS` | `200` | Maximum total steps across all phases. Prevents runaway plan generation. |
| `TRUVAG3_ITERATIVE_PHASE_TIMEOUT` | `180s` | Maximum duration for a single phase (plan generation + execution). Prevents a single continuation phase from hanging. |
| `TRUVAG3_STEP_RETRY_INITIAL_DELAY` | `500ms` | Initial backoff delay for step retries. Go duration format. Exponential backoff doubles this per attempt. |
| `TRUVAG3_STEP_RETRY_MAX_DELAY` | `10s` | Maximum backoff delay for step retries. Delay is capped at this value regardless of attempt count. |
| `TRUVAG3_FAILURE_PATTERN_MIN_FAILURES` | `2` | Minimum distinct failed upstream steps required before the remediation continuation prompt embeds a shared-error pattern summary. Raise to make the analyzer more conservative. |
| `TRUVAG3_FAILURE_PATTERN_SIGNATURE_LEN` | `120` | Max chars of each step's error used to bucket failures into a shared signature. |
| `TRUVAG3_FAILURE_PATTERN_DISPLAY_LEN` | `80` | Max chars of the shared error rendered into the remediation prompt (trailing `…` appended on truncation). |
| `TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL` | `""` | Model for event summarization and activity compaction LLM calls. Supports aliases (`fast`, `smart`). |
| `TRUVAG3_SHARED_MEMORY_ENRICHMENT_SUMMARY_MAX_TOKENS` | `500` | Max tokens for compacted domain activity digest. |
| `TRUVAG3_SHARED_MEMORY_COMPACTION_RAW_LIMIT` | `200` | Max events fetched before compaction. |
| `TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL` | `5m` | Cached digest expiry. Next request after TTL does full recompaction. |
| `TRUVAG3_SHARED_MEMORY_DIGEST_INCREMENTAL_THRESHOLD` | `20` | New events above this trigger full recompaction instead of incremental update. |
| `TRUVAG3_ACTIVITY_SIGNAL_TTL` | `5m` | Activity coordination signal expiry. |
| `TRUVAG3_ACTIVITY_SIGNAL_MAX_IN_PROMPT` | `10` | Max activity signals in `<agent_coordination>` prompt section. |

```bash
# Example: Allow 5 minutes for AI-heavy workflows
export TRUVAG3_ORCHESTRATION_TIMEOUT=5m

# Example: Enable LLM debug capture
export TRUVAG3_LLM_DEBUG_ENABLED=true

# Example: Configure iterative planning
export TRUVAG3_ITERATIVE_PLANNING_ENABLED=true
export TRUVAG3_ITERATIVE_MAX_PHASES=3

# Example: Tune step retry backoff (exponential, context-aware)
export TRUVAG3_STEP_RETRY_INITIAL_DELAY=500ms
export TRUVAG3_STEP_RETRY_MAX_DELAY=10s

# Example: Configure shared memory compaction
export TRUVAG3_SHARED_MEMORY_SUMMARIZER_MODEL=fast
export TRUVAG3_SHARED_MEMORY_DIGEST_CACHE_TTL=10m
```

### Programmatic Configuration

```go
config := &orchestration.OrchestratorConfig{
    // Routing mode
    RoutingMode: orchestration.ModeAutonomous,  // Options: ModeAutonomous, ModeWorkflow

    // Synthesis strategy
    SynthesisStrategy: orchestration.StrategyLLM, // Options: StrategyLLM, StrategyTemplate, StrategySimple

    // Capability Provider (for scaling)
    CapabilityProviderType: "default",  // Options: "default" or "service"
    EnableFallback: true,                // Fallback to default provider on service failure

    // Tiered Capability Resolution (token optimization)
    // Enabled by default - uses 2-phase approach for 20+ tools
    EnableTieredResolution: true,
    TieredResolution: orchestration.TieredCapabilityConfig{
        MinToolsForTiering: 20,  // Threshold to trigger tiered resolution
    },

    // Prompt customization
    PromptConfig: orchestration.PromptConfig{
        // SystemInstructions defines the orchestrator's persona and behavioral context
        SystemInstructions: `You are a helpful assistant.
Prioritize accuracy and provide practical recommendations.`,
    },

    // Iterative planning (multi-phase DAG planning)
    IterativePlanning: orchestration.IterativePlanConfig{
        Enabled:       true,
        MaxPhases:     5,
        MaxTotalSteps: 200,
        PhaseTimeout:  180 * time.Second,
    },

    // Execution configuration
    ExecutionOptions: orchestration.ExecutionOptions{
        MaxConcurrency:   5,                // Maximum parallel tool/agent calls
        StepTimeout:      120 * time.Second, // Timeout per step
        TotalTimeout:     5 * time.Minute,  // Overall execution timeout
        RetryAttempts:    2,                // Retry failed steps
        RetryDelay:       2 * time.Second,  // Delay between retries
        CircuitBreaker:   true,             // Enable circuit breaker
        FailureThreshold: 5,                // Circuit breaker threshold
        RecoveryTimeout:  30 * time.Second, // Circuit breaker recovery
    },

    // Authentication and header propagation
    OAuthToken: os.Getenv("TRUVAG3_OAUTH_TOKEN"), // Bearer token for outbound calls
    PropagatedHeaders: map[string]string{         // Custom headers for all outbound calls
        "X-Tenant-ID": "tenant-42",
    },

    // History and caching
    HistorySize:  100,              // Execution history buffer size
    CacheEnabled: true,              // Enable routing cache
    CacheTTL:     5 * time.Minute,  // Cache expiration time
}
```

## 14. Usage Patterns

The orchestration module supports various usage patterns as demonstrated in the documentation above. Refer to the code examples in this README for implementation guidance.

## 15. Requirements

- **Redis** - For tool/agent discovery and state storage
- **OpenAI API Key** - For AI orchestration (or compatible LLM)
- **Running Components** - Tools and agents registered with discovery

## 16. Scaling to Hundreds of Agents - Capability Provider Architecture

### The Problem: Token Overflow at Scale

When you have hundreds or thousands of agents and tools, sending ALL their capabilities to the LLM causes:
- **Token limit overflow** (even with 1M+ token models)  
- **Increased costs** (more tokens = more money)
- **Slower responses** (processing huge contexts)

### The Solution: Smart Capability Discovery

The orchestration module provides three strategies:

#### 1. Default Provider (Small Scale: < 20 tools)
```go
// Sends ALL capabilities to LLM (original behavior)
config := orchestration.DefaultConfig()
config.CapabilityProviderType = "default"
config.EnableTieredResolution = false  // Disable tiering for small deployments

// Simple, no external dependencies, perfect for getting started
```

#### 2. Tiered Resolution (Medium Scale: 20-100 tools) - **Default**

A 2-phase approach that reduces LLM token usage by 50-75% without external services.

**How it works:**
1. **Phase 1 (Tier 1)**: Send lightweight tool summaries to LLM for tool selection
2. **Phase 2 (Tier 2)**: Fetch full schemas only for selected tools
3. **Phase 3 (Tier 3)**: Generate execution plan with focused context

```go
// Enabled by default for optimal token usage
config := orchestration.DefaultConfig()
// EnableTieredResolution: true (default)
// TieredResolution.MinToolsForTiering: 20 (default)

// Or configure explicitly
config.EnableTieredResolution = true
config.TieredResolution = orchestration.TieredCapabilityConfig{
    MinToolsForTiering: 25,  // Custom threshold (default: 20)
}
```

**Environment configuration:**
```bash
export TRUVAG3_TIERED_RESOLUTION_ENABLED=true
export TRUVAG3_TIERED_MIN_TOOLS=20
```

**Token savings (example: 50 tools, user needs 3):**
| Approach | Total Tokens | Savings |
|----------|--------------|---------|
| All tools | ~13,000 | - |
| Tiered | ~6,500 | **50%** |

**Context-aware Phase 2+ re-selection:** During [multi-phase iterative planning](#iterative-planning-environment-variables), the tiered provider automatically detects phase context and builds a continuation selection prompt that includes prior tools used, the planner's continuation note, and a compact result summary. This enables differentiated tool discovery across phases — e.g., Phase 1 selects search/discovery tools while Phase 2 selects weather/currency tools based on Phase 1 results.

#### 3. Service Provider (Large Scale: 100s-1000s of agents)

**Kubernetes (Recommended):** Use environment variable for the endpoint:
```bash
export TRUVAG3_CAPABILITY_SERVICE_URL="http://capability-service:8080"
```

```go
// Uses external RAG service for semantic search
// Endpoint is read from TRUVAG3_CAPABILITY_SERVICE_URL environment variable
config := orchestration.DefaultConfig()
config.CapabilityProviderType = "service"
config.CapabilityService = orchestration.ServiceCapabilityConfig{
    // Endpoint: automatically loaded from TRUVAG3_CAPABILITY_SERVICE_URL
    TopK:      20,       // Return top 20 most relevant agents
    Threshold: 0.7,      // Minimum relevance score
    Timeout:   10 * time.Second,
}
config.EnableFallback = true  // Fall back to default if service fails

deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
}

orchestrator, _ := orchestration.CreateOrchestrator(config, deps)
```

> **Note:** If `TRUVAG3_CAPABILITY_SERVICE_URL` is not set and `CapabilityService.Endpoint` is empty, `CreateOrchestrator` returns an error. See [factory.go:79](factory.go#L79).

### How Service Provider Works

```
User Request: "Analyze customer sentiment"
         ↓
1. Query RAG Service with semantic search
         ↓
2. Service returns ONLY relevant agents:
   - sentiment-analyzer (score: 0.95)
   - text-processor (score: 0.88)
   - emotion-detector (score: 0.85)
         ↓
3. Send only these 3 to LLM (not all 1000!)
         ↓
4. LLM makes decision with focused context
```

### Production Configuration with Resilience

```go
// For production: Add circuit breaker and monitoring
import "github.com/truvaagents/truva-g3/resilience"

// Create circuit breaker for the external service
cb, _ := resilience.NewCircuitBreaker(&resilience.CircuitBreakerConfig{
    Name:           "capability-service",
    ErrorThreshold: 0.5,
    VolumeThreshold: 10,
    SleepWindow:    30 * time.Second,
})

// Create logger for observability
logger := myapp.NewLogger()

// Configure with full resilience
config := orchestration.DefaultConfig()
config.CapabilityProviderType = "service"
config.CapabilityService = orchestration.ServiceCapabilityConfig{
    Endpoint:  "http://capability-service:8080",
    TopK:      50,  // More results for production
    Threshold: 0.8, // Higher quality threshold
}
config.EnableFallback = true  // Graceful degradation

deps := orchestration.OrchestratorDependencies{
    Discovery:      discovery,
    AIClient:       aiClient,
    CircuitBreaker: cb,      // Optional: Sophisticated resilience
    Logger:         logger,  // Optional: Structured logging
}

orchestrator, _ := orchestration.CreateOrchestrator(config, deps)
```

### Environment-Based Configuration

```bash
# Configure via environment variables
export TRUVAG3_CAPABILITY_SERVICE_URL="http://capability-service:8080"
export TRUVAG3_CAPABILITY_TOP_K="30"
export TRUVAG3_CAPABILITY_THRESHOLD="0.75"

# The orchestrator auto-configures when these are set
```

### Three Layers of Resilience

The service provider includes built-in resilience:

1. **Circuit Breaker** (if injected) - Prevents cascading failures
2. **Retry Logic** (built-in) - 3 retries with exponential backoff
3. **Fallback Provider** (configurable) - Falls back to default provider

### When to Use Each Provider

| Scenario | Provider | Why |
|----------|----------|-----|
| **Development/Testing** | Default (tiered disabled) | Simple, fast iteration |
| **< 20 tools** | Default (tiered disabled) | Overhead not worth it |
| **20-100 tools** | Tiered (default) | 50-75% token savings, no external dependencies |
| **100s-1000s agents** | Service | Semantic search scales better |
| **Production critical** | Service + Circuit Breaker | Maximum resilience |

## 17. Performance Considerations

1. **Workflow Execution** - DAG-based execution with automatic parallelization
2. **Caching** - Use routing cache to reduce redundant LLM calls
3. **Discovery** - Component catalog refreshes every 10 seconds by default
4. **Concurrency** - Default 25 parallel tool/agent calls, configurable via `MaxConcurrency`
5. **Timeouts** - Configure appropriate timeouts for your use case
6. **Capability Provider** - Use service provider for 100s+ agents to avoid token overflow

## 18. Streaming Support

The orchestration module supports real-time streaming of AI responses, enabling SSE/WebSocket chat interfaces and lower time-to-first-token UX.

### ProcessRequestStreaming

Stream orchestration results token-by-token as the AI generates them:

```go
func (o *AIOrchestrator) ProcessRequestStreaming(
    ctx context.Context,
    query string,
    tools []*ServiceInfo,
    callback core.StreamCallback,
) (*StreamingOrchestratorResponse, error)
```

### Basic Streaming Example

```go
// Stream orchestration results
result, err := orchestrator.ProcessRequestStreaming(ctx,
    "Analyze Tesla stock and recommend action",
    nil, // Auto-discover tools
    func(chunk core.StreamChunk) error {
        if chunk.Content != "" {
            fmt.Print(chunk.Content) // Print each token as it arrives
        }
        return nil
    },
)

if err != nil {
    log.Printf("Streaming failed: %v", err)
    return
}

fmt.Printf("\n\nCompleted in %v\n", result.ExecutionTime)
```

### Streaming with SSE (Chat Agent)

```go
func (h *ChatHandler) HandleStream(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    flusher := w.(http.Flusher)

    // Set SSE headers
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    var req ChatRequest
    json.NewDecoder(r.Body).Decode(&req)

    // Stream with per-step callbacks for real-time progress
    ctx = orchestration.WithStepCallback(ctx,
        func(stepIndex, totalSteps int, step orchestration.RoutingStep, stepResult orchestration.StepResult) {
            // Send SSE event for each tool completion
            data, _ := json.Marshal(map[string]interface{}{
                "event":      "step",
                "step_id":    stepIndex,
                "tool":       step.AgentName,
                "success":    stepResult.Success,
                "duration_ms": stepResult.Duration.Milliseconds(),
            })
            fmt.Fprintf(w, "event: step\ndata: %s\n\n", data)
            flusher.Flush()
        },
    )

    // Stream the AI response
    result, err := h.orchestrator.ProcessRequestStreaming(ctx, req.Message, nil,
        func(chunk core.StreamChunk) error {
            if chunk.Content != "" {
                data, _ := json.Marshal(map[string]string{"text": chunk.Content})
                fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", data)
                flusher.Flush()
            }
            return nil
        },
    )

    if err != nil {
        fmt.Fprintf(w, "event: error\ndata: %s\n\n", err.Error())
        return
    }

    // Send completion event
    fmt.Fprintf(w, "event: done\ndata: {\"request_id\": \"%s\"}\n\n", result.RequestID)
}
```

> **Note:** This is a simplified example. For a production implementation with conversation history, session management, and proper error handling, see the [Chat Agent Implementation Guide](../docs/memory-and-chat/CHAT_AGENT_GUIDE.md). For the dedicated Tier 1 / Tier 2 / Layer 3 conversation-history reference, see the [Conversation History Guide](../docs/memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

### StreamingOrchestratorResponse

The response includes both standard orchestration fields and streaming-specific metadata:

```go
type StreamingOrchestratorResponse struct {
    OrchestratorResponse              // Embedded base response (see below)

    // Streaming-specific fields
    ChunksDelivered int               // Number of chunks delivered
    StreamCompleted bool              // Whether streaming completed successfully
    PartialContent  bool              // True if response is partial due to error
    StepResults     []StepResult      // Detailed results from each execution step
    FinishReason    string            // Why streaming stopped
}

type OrchestratorResponse struct {
    RequestID       string                    // Unique request identifier
    OriginalRequest string                    // The original user query
    Response        string                    // Complete accumulated response
    RoutingMode     RouterMode                // Routing mode used
    ExecutionTime   time.Duration             // Total execution time
    AgentsInvolved  []string                  // Tools/agents used
    Metadata        map[string]interface{}    // Additional metadata
    Errors          []string                  // Any errors encountered
    Confidence      float64                   // Orchestration confidence
    Steps           []StepResult              // Individual step results

    // Aggregated token usage across all LLM calls in this request
    Usage           *core.TokenUsage          // Total prompt + completion tokens
    UsageByPhase    map[string]core.TokenUsage // Breakdown by orchestration phase

    // Set when the planner determines it needs information from the user to
    // continue (e.g., "What are your travel dates?"). The natural-language
    // question is also woven into Response by the synthesizer; this structured
    // field is provided for sophisticated UI consumers (quick-reply chips,
    // form prompts, etc.). Nil for normal completions.
    Clarification   *ClarificationRequest     // Structured clarification request
}
```

The `UsageByPhase` map keys correspond to orchestration phases: `planning`, `correction`, `synthesis`, `micro_resolution`, `schema_mapping`, `distillation`, `semantic_retry`, `error_analysis`, `tiered_selection`.

#### Clarification Requests

When the planner determines that progress depends on information only the user can provide — for example, *"What are your travel dates?"* in a multi-step trip planner — the orchestrator surfaces the question two ways simultaneously:

1. **`Response`** — the synthesizer weaves the question into the natural-language reply, so simple chat agents can forward it as-is without any code changes.
2. **`Clarification`** — a structured field for UI consumers that want to render quick-reply chips, form fields, or other affordances:

```go
type ClarificationRequest struct {
    Question        string   // Natural-language question to surface
    MissingFields   []string // Optional structured field names (e.g., ["travel_dates", "destination_cities"])
    PartialProgress string   // Optional one-line description of work already gathered
}
```

When `Clarification` is set, the orchestrator has terminated the current turn cleanly — the conversation continues on the next user message, with the new turn re-entering through `ProcessRequest` as a fresh invocation. There is no special "resume" protocol; conversation state is owned by the calling agent (typically via session-store enrichments).

For chat agents that don't need structured clarification handling, just forward `response.Response` as-is — the embedded question reaches the user automatically. The `Clarification` field can be ignored.

### Step Callbacks for Real-Time Progress

Use `WithStepCallback` to receive notifications as each tool completes:

```go
ctx = orchestration.WithStepCallback(ctx,
    func(stepIndex, totalSteps int, step orchestration.RoutingStep, result orchestration.StepResult) {
        log.Printf("Step %d/%d: %s completed in %v",
            stepIndex+1, totalSteps,
            step.AgentName, result.Duration)
    },
)
```

### When to Use Streaming

| Scenario | Regular `ProcessRequest` | Streaming `ProcessRequestStreaming` |
|----------|-------------------------|-------------------------------------|
| API backend | ✅ Simpler | ❌ Overkill |
| Chat UI | ⚠️ Poor UX | ✅ Real-time feedback |
| Long-running queries | ⚠️ User waits | ✅ Immediate feedback |
| Progress indicators | ❌ No visibility | ✅ Per-step updates |

**For a complete production example** with SSE streaming, session management, and conversation history, see the [Chat Agent Implementation Guide](../docs/memory-and-chat/CHAT_AGENT_GUIDE.md). For conversation-history-specific guidance and advanced overrides, see the [Conversation History Guide](../docs/memory-and-chat/CONVERSATION_HISTORY_GUIDE.md).

## 19. Potential Enhancements

These features are not yet implemented but could be added:
- Visual workflow designer UI
- Distributed workflow execution across nodes
- WebSocket transport for bidirectional streaming
- Workflow versioning and migration tools
- Custom capability provider implementations (e.g., GraphQL-based)

## 20. API Reference

### Core Types
- `Orchestrator` - Main orchestration interface
- `WorkflowEngine` - Workflow execution engine
- `OrchestratorConfig` - Configuration structure
- `OrchestratorDependencies` - Dependency injection container
- `CapabilityProvider` - Interface for capability discovery
- `ServiceCapabilityConfig` - Configuration for service-based provider
- `WorkflowDefinition` - YAML workflow structure
- `ExecutionResult` - Execution results
- `StreamingOrchestratorResponse` - Streaming response with accumulated content and metadata
- `StepCallback` - Callback type for per-step progress notifications
- `LLMDebugStore` - Interface for LLM debug payload storage
- `LLMInteraction` - Single LLM call record with `SourceComponent` and `CallDescription` attribution
- `LLMDebugRecordSummary` - Lightweight summary with `SourceComponents` for agent name listing
- `LLMCallRecorderAdapter` - Bridges `LLMDebugStore` to `telemetry.LLMCallRecorder`

### Key Functions
- `CreateOrchestrator(config, deps)` - Create orchestrator with dependencies
- `CreateSimpleOrchestrator(discovery, aiClient)` - Quick start orchestrator
- `CreateOrchestratorWithOptions(deps, opts...)` - Create with option functions
- `NewAIOrchestrator(config, discovery, aiClient)` - Low-level orchestrator creation
- `NewWorkflowEngine(discovery, stateStore, logger)` - Create workflow engine
- `ProcessRequest(ctx, request, metadata)` - Process natural language request
- `ProcessRequestStreaming(ctx, query, tools, callback)` - Stream orchestration response with real-time tokens
- `ExecutePlan(ctx, plan)` - Execute pre-defined routing plan (raw results, no synthesis)
- `ExecutePlanWithSynthesis(ctx, plan, originalRequest)` - Execute plan with synthesis + DAG storage
- `WithStepCallback(ctx, callback)` - Add per-request step completion callback
- `ExecuteWorkflow(ctx, workflow, inputs)` - Execute defined workflow
- `ParseWorkflowYAML(data)` - Parse workflow from YAML
- `NewLLMCallRecorderAdapter(store)` - Bridge `LLMDebugStore` to `telemetry.LLMCallRecorder`

### Configuration Options
- `WithCapabilityProvider(type, url)` - Configure capability provider type and URL
- `WithTelemetry(enabled)` - Enable/disable telemetry
- `WithFallback(enabled)` - Enable/disable fallback provider

## 21. Best Practices & Tips

### The Journey from Prototype to Production

#### Phase 1: Exploration (Use AI Orchestration)
```go
// Start with natural language - let AI figure it out
response := orchestrator.ProcessRequest(ctx, 
    "analyze this company and tell me if I should invest", nil)
```

#### Phase 2: Pattern Recognition
```
// After a few runs, you notice the pattern:
// 1. Always fetches financials
// 2. Always checks news
// 3. Always runs sentiment analysis
// 4. Always generates report
```

#### Phase 3: Production (Create Workflow)
```yaml
# Now codify the pattern into a reliable workflow
name: investment-analysis
steps:
  - name: get-financials
  - name: check-news  
  - name: sentiment-analysis
  - name: generate-report
# Faster, cheaper, predictable!
```

### Golden Rules

1. **🎯 Start Simple**: Use AI mode to explore, then optimize with workflows
2. **🔍 Use Discovery**: Never hardcode agent URLs - let discovery find them
3. **⚡ Cache Smartly**: Cache stable data long, volatile data short
4. **📊 Monitor Everything**: If you can't measure it, you can't improve it
5. **🔄 Handle Failures**: Always configure retries and timeouts
6. **🚀 Think Parallel**: Design workflows to maximize parallelism

## 22. Production-Ready Enhancements

### LLM-First Hybrid Parameter Resolution

In multi-step workflows, parameters must flow between tools automatically. The orchestration module uses a **four-layer resolution system** where LLM handles all semantic understanding:

| Layer | Strategy | When Used | Cost |
|-------|----------|-----------|------|
| **Layer 1: Auto-Wiring** | Exact name match, case-insensitive match, type coercion | Always (first) | Free |
| **Layer 2: Micro-Resolution** | LLM extracts parameters via function calling | When Layer 1 leaves required params unmapped | 1 LLM call |
| **Layer 3: Error Analysis** | LLM analyzes tool errors and suggests corrections | When tool returns 400/404/409/422 | 1 LLM call |
| **Layer 4: Semantic Retry** | LLM computes parameters from full execution context | When Layer 3 says "cannot fix" but source data exists | 1 LLM call |

**How it works:**

```
Step 1 completes → Output: {"latitude": "48.85", "country": "France"}

┌─────────────────────────────────────────────────────────────┐
│ Layer 1: Auto-Wiring (instant, free)                        │
│   • Exact match: lat ← lat ✓                                │
│   • Type coercion: "48.85" → 48.85                          │
│   • Nested extraction: {code: "EUR"} → "EUR"                │
│                                                             │
│   NOTE: No semantic aliases - framework is domain-agnostic  │
└─────────────────────────────────────────────────────────────┘
         │
         ▼ (if required params still missing)
┌─────────────────────────────────────────────────────────────┐
│ Layer 2: Micro-Resolution (LLM call)                        │
│   • LLM understands "latitude" means "lat"                  │
│   • LLM infers "France" uses "EUR" currency                 │
│   • Guaranteed type safety via JSON schema                  │
└─────────────────────────────────────────────────────────────┘
         │
         ▼ (if tool call fails with correctable error)
┌─────────────────────────────────────────────────────────────┐
│ Layer 3: Error Analysis (LLM call)                          │
│   • Analyzes error: "City 'Tokio' not found"                │
│   • Suggests fix: {"city": "Tokyo"}                         │
│   • Retries with corrected parameters                       │
└─────────────────────────────────────────────────────────────┘
```

**Key Design Principle:** The framework contains **no domain-specific knowledge** (weather, currency, etc.). All semantic understanding is delegated to the LLM.

**Disabling LLM Layers:**
```go
// Disable Layer 2: Micro-resolution (auto-wiring only)
resolver := NewHybridResolver(aiClient, logger,
    WithMicroResolution(false))

// Disable Layer 3: Error Analysis (no LLM-based error recovery)
analyzer := NewErrorAnalyzer(aiClient, logger,
    WithErrorAnalysisEnabled(false))

// Runtime toggle for Layer 3
analyzer.Enable(false)  // Disable
analyzer.Enable(true)   // Re-enable
```

**Error Handling by HTTP Status:**

| Status | Handler | Action |
|--------|---------|--------|
| 400, 404, 409, 422 | LLM Error Analyzer → Semantic Retry | Analyze → correct → retry |
| 408, 429, 5xx | Resilience Module | Same payload + backoff |
| 401, 403, 405 | Neither | Fail immediately |

**Observability:**
- Span events: `llm.micro_resolution.*`, `error_analyzer.*`
- All LLM calls are traced in Jaeger with prompts, responses, and token usage

### Layer 4: Semantic Retry (Contextual Re-Resolution)

**The Problem Solved:** When Layer 3 (Error Analysis) determines "this error cannot be fixed with different parameters" but the source data to compute the correct value actually exists, standard retry gives up. Semantic Retry uses the full execution trajectory to compute the correct parameters.

**Real-World Example:**
```
User: "Sell 100 Tesla shares and convert proceeds to EUR"

Step 1 (stock-tool): Returns {symbol: "TSLA", price: 468.285}
Step 2 (currency-tool): Called with {amount: 0} ← Layer 1/2 couldn't compute this!
        ↓
Tool returns 400: "amount must be greater than 0"
        ↓
Layer 3 (Error Analysis): "Cannot fix - don't know what amount should be"
        ↓
🆕 Layer 4 (Semantic Retry): Has access to:
   • User query: "Sell 100 Tesla shares..."
   • Source data: {price: 468.285}
   • Failed params: {amount: 0}

   LLM computes: 100 × 468.285 = 46828.5
        ↓
Retries with: {amount: 46828.5} ✅ SUCCESS!
```

**How It Works:**
```
┌─────────────────────────────────────────────────────────────┐
│ Layer 4: Semantic Retry (enabled by default)                 │
│                                                              │
│   Triggers when:                                             │
│   • Tool returns 4xx error (400, 404, 409, 422)             │
│   • Layer 3 says "cannot fix"                                │
│   • Source data exists from dependent steps                  │
│                                                              │
│   The LLM receives:                                          │
│   • User's original query (intent)                           │
│   • All source data from previous steps                      │
│   • Failed parameters and error message                      │
│   • Target capability schema                                 │
│                                                              │
│   Returns:                                                   │
│   • should_retry: true/false                                 │
│   • corrected_parameters: computed values                    │
│   • analysis: explanation of the fix                         │
└─────────────────────────────────────────────────────────────┘
```

**Configuration:**
```go
config := orchestration.DefaultConfig()

// Semantic retry is enabled by default
config.SemanticRetry.Enabled = true                   // Default: true
config.SemanticRetry.MaxAttempts = 2                  // Default: 2
config.SemanticRetry.EnableForIndependentSteps = true // Default: true

// Disable for cost-sensitive deployments
config.SemanticRetry.Enabled = false

// Disable only for independent steps (revert to old behavior)
config.SemanticRetry.EnableForIndependentSteps = false
```

**Environment Variables:**
```bash
# Enable/disable semantic retry (default: true)
export TRUVAG3_SEMANTIC_RETRY_ENABLED=true

# Maximum retry attempts (default: 2)
export TRUVAG3_SEMANTIC_RETRY_MAX_ATTEMPTS=2

# Enable for independent steps - steps without dependencies (default: true)
export TRUVAG3_SEMANTIC_RETRY_INDEPENDENT_STEPS=true
```

**When Semantic Retry Activates:**

| Condition | Layer 4 Activates? |
|-----------|-------------------|
| Tool returns 400 + Layer 3 says "cannot fix" | ✅ Yes |
| Tool returns 500 (server error) | ❌ No (handled by resilience) |
| Tool returns 401 (auth error) | ❌ No (not retryable) |
| Layer 3 successfully corrects | ❌ No (already fixed) |
| Independent step (no dependencies) with error | ✅ Yes (uses user query + error context) |
| Independent step + `EnableForIndependentSteps=false` | ❌ No (disabled by config) |

**Observability:**
- Span events: `contextual_re_resolution.start`, `contextual_re_resolution.complete`
- Span attributes: `independent_step` (bool) indicating step had no dependencies
- Metrics: `orchestration.semantic_retry.success`, `orchestration.semantic_retry.cannot_fix`, `orchestration.semantic_retry.independent_step`
- Full visibility in Jaeger traces

**Key Insight:** Semantic Retry succeeds where static rules fail because it has access to:
1. **User intent** - understands what computation is needed
2. **Source data** - has the values to compute from
3. **Error context** - knows exactly what went wrong

This is the **same reasoning a human developer would apply** when debugging a failed API call, now automated by the framework.

### LLM Debug Payload Store

The orchestration module captures complete LLM request/response payloads for production debugging. Unlike Jaeger spans which truncate large payloads, the debug store preserves full prompts and responses.

**Key Features:**
- **Complete Payload Visibility**: No truncation of prompts or responses
- **Request Correlation**: Query by `request_id` (trace ID or sequence number)
- **9 Recording Sites**: `plan_generation`, `correction`, `synthesis`, `synthesis_streaming`, `micro_resolution`, `semantic_retry`, `tiered_selection`, `hallucination_detection`, plus `agent_llm_call` (via `ai.InstrumentedAIClient`)
- **Source Attribution**: `SourceComponent` field identifies which agent/component made each LLM call; `SourceComponents` on summaries provides per-record agent name listing
- **Three-Layer Resilience**: Built-in retry → optional circuit breaker → NoOp fallback
- **Provider Tracking**: Captures AI provider (openai, anthropic, gemini, bedrock)
- **Atomic Storage**: List-based storage (RPUSH) safe for concurrent writes from orchestrator and agents

**Configuration:**
```bash
# Enable debug capture (disabled by default)
export TRUVAG3_LLM_DEBUG_ENABLED=true

# TTL for records (default: 24h success, 168h errors)
export TRUVAG3_LLM_DEBUG_TTL=24h
export TRUVAG3_LLM_DEBUG_ERROR_TTL=168h

# Redis database (default: 7)
export TRUVAG3_LLM_DEBUG_REDIS_DB=7
```

**Programmatic Configuration:**
```go
deps := orchestration.OrchestratorDependencies{
    Discovery: discovery,
    AIClient:  aiClient,
}

orchestrator, _ := orchestration.CreateOrchestratorWithOptions(deps,
    orchestration.WithLLMDebug(true),  // Enable debug capture
)
```

**Agent-Side LLM Recording:**

Agents can record their own LLM calls to the same debug store without importing the orchestration module. This uses `telemetry.RedisLLMCallRecorder` + `ai.InstrumentedAIClient`:

```go
// In agent main.go
recorder, _ := telemetry.NewRedisLLMCallRecorder()
aiClient := ai.NewInstrumentedClient(baseClient, recorder,
    ai.WithComponentName("my-agent"),
)
```

The orchestrator propagates `X-TruvaG3-Request-ID` and `X-TruvaG3-Step-ID` headers to agents during plan execution. Agents extract these via `core.ExtractRequestContext()` in their HTTP handlers, which enables `InstrumentedAIClient` to correlate recordings back to the orchestration request.

**LLMCallRecorderAdapter:**

When the orchestrator's `LLMDebugStore` already exists and you want to reuse the same Redis connection (instead of creating a separate `RedisLLMCallRecorder`), use the adapter:

```go
debugStore := orchestrator.GetLLMDebugStore()
recorder := orchestration.NewLLMCallRecorderAdapter(debugStore)
aiClient := ai.NewInstrumentedClient(baseClient, recorder)
```

### Result Trimming Pipeline

The orchestration module automatically trims large step results before they are embedded in LLM synthesis prompts. This prevents token budget overflow and ensures the LLM receives the most relevant data.

**Architecture:**

The `ResultProcessor` interface defines the trimming contract. It is pluggable — inject a custom implementation via `OrchestratorDependencies.ResultProcessor`, or use the built-in `StructuralTrimmer` (default).

```
Step Results → ResultProcessor.ProcessForPrompt() → Trimmed Results → Synthesis Prompt
                        │
          ┌─────────────┼─────────────────┐
          │             │                 │
   StructuralTrimmer  LLMDistiller    Custom
   (default)          (opt-in)        (user)
```

**How StructuralTrimmer Works:**

1. **Keyword extraction** — Extracts query-relevant keywords from the step instruction
2. **Field inventory** — Recursively enumerates all JSON fields with sizes (arrays >1024B are decomposed into sub-fields)
3. **Relevance scoring** — Scores each field by keyword match (path + content preview) and size efficiency
4. **Greedy selection** — Selects fields by relevance/size ratio within the byte budget
5. **Multi-field backfill** — Recovers dropped string fields using a fractional knapsack over remaining budget (Phase 4A)
6. **Relevance-ordered output** — Serializes selected fields highest-relevance-first to exploit LLM primacy bias (Phase 4C)

**Key Features:**

| Feature | Description |
|---------|-------------|
| Content-aware scoring | Analyzes first 500 chars of string values for keyword matches (Phase 4B) |
| Minimum relevance threshold | 0.3 floor prevents backfilling irrelevant fields like `metadata.uid` (Phase 4D) |
| JSON-in-string handling | Pre-parses stringified JSON (e.g., kubectl stdout) before field inventory |
| Budget allocation | Multi-result scenarios use `BudgetAllocator` for proportional distribution with redistribution |
| Observability | `result_trim.completed` span event captures trim metadata per step (Phase 4E) |

**Configuration:**

```bash
export TRUVAG3_RESULT_TRIM_ENABLED=true                    # Enable/disable (default: true)
export TRUVAG3_RESULT_TRIM_MAX_BYTES=16384                 # Per-result max (default: 16KB)
export TRUVAG3_RESULT_TRIM_MAX_TOTAL_BYTES=32768           # Total synthesis prompt max (default: 32KB)
export TRUVAG3_RESULT_TRIM_MAX_MICRO_BYTES=65536           # Micro-resolution source data max (default: 64KB)
export TRUVAG3_RESULT_TRIM_MAX_AGENT_INPUT_BYTES=0         # Agent HTTP parameter max (default: 0 = no cap, fidelity-first)
export TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD=16384  # Schema-mapping threshold (default: 16KB; 0 disables)
```

> For struct details and metadata reference, see [API_REFERENCE.md](../docs/reference/API_REFERENCE.md#resulttrimconfig).
> For trim span events in Jaeger, see [DISTRIBUTED_TRACING_GUIDE.md](../docs/observability/DISTRIBUTED_TRACING_GUIDE.md#15-llm-telemetry-in-orchestration-automatic).

### Comprehensive Logging System
The orchestration module now includes production-grade logging for all operations:

- **Workflow Execution Logging**: Track every step of workflow execution with structured logs
- **AI Decision Logging**: Capture AI orchestrator decisions and reasoning
- **Component Interaction Logging**: Log all tool and agent interactions
- **Error Context Logging**: Detailed error information with full context

#### Logging Examples:
```go
// Workflow step execution
logger.InfoWithContext(ctx, "Executing workflow step", map[string]interface{}{
    "workflow": "data-analysis",
    "step": "fetch-data",
    "attempt": 1,
})

// AI orchestration decision
logger.DebugWithContext(ctx, "AI selected components", map[string]interface{}{
    "query": "analyze Tesla stock",
    "selected_tools": []string{"stock-fetcher", "analyzer"},
    "confidence": 0.95,
})

// Error with context
logger.ErrorWithContext(ctx, "Workflow step failed", map[string]interface{}{
    "workflow": "report-generation",
    "step": "pdf-export",
    "error": err.Error(),
    "retry_count": 3,
})
```

### Enhanced Service Discovery
- **Improved Registry Performance**: Optimized component lookup and caching
- **Better Error Recovery**: Automatic retry on transient discovery failures
- **Detailed Metrics**: Track discovery latency and success rates

### Internal Capability Flag

Capabilities can be marked as "internal" to exclude them from the LLM planning catalog while keeping them HTTP-callable. This prevents self-referential orchestration bugs where an orchestrator agent might recursively call itself.

**Problem Solved:**
When an orchestrator agent registers its `orchestrate_natural` capability, the LLM might include it in execution plans, causing recursive self-calls with 400 errors. The `Internal` flag prevents this by filtering internal capabilities from the catalog sent to the LLM.

**Usage:**
```go
// Mark orchestration capabilities as internal to prevent LLM from calling them
agent.RegisterCapability(core.Capability{
    Name:        "orchestrate_natural",
    Description: "Process natural language requests with AI orchestration",
    Endpoint:    "/orchestrate/natural",
    Internal:    true,  // Exclude from LLM catalog
    Handler:     handleOrchestration,
})
```

**Key Behaviors:**
| Behavior | Internal: true | Internal: false (default) |
|----------|----------------|---------------------------|
| HTTP callable | ✅ Yes | ✅ Yes |
| In LLM catalog | ❌ No | ✅ Yes |
| In `FormatForLLM()` output | ❌ No | ✅ Yes |

**When to Use `Internal: true`:**
- Orchestration endpoints (prevent recursive planning)
- Admin/maintenance endpoints
- Deprecated capabilities (still accessible but hidden from AI)
- Health check or metrics endpoints

**Backward Compatibility:**
- `Internal` defaults to `false` (Go zero value)
- Existing capabilities without the field remain public
- No changes required for existing tool/agent code

### Workflow Engine Improvements
- **Parallel Step Execution**: Execute independent steps concurrently
- **Step Timeout Configuration**: Set timeouts per workflow step
- **Conditional Branching**: Support for if/else logic in workflows
- **Error Handling Strategies**: Configure retry, skip, or fail strategies per step

### Cross-Agent Shared Memory (Pipeline Hooks)

The orchestration module provides five built-in pipeline hooks for cross-agent shared memory. These hooks run automatically during orchestration phases, giving agents awareness of what other agents have done and are currently doing.

**Hook execution order:**

| # | Hook | Stage | Purpose |
|---|------|-------|---------|
| 1 | `ActivityAnnouncementHook` | BeforePlanning | Announces this agent's activity, injects other agents' signals into `<agent_coordination>` |
| 2 | `MemoryEnrichmentHook` | BeforePlanning | Queries episodic events, compacts into digest, searches knowledge — injects into `<agent_memory>` |
| 3 | `MemoryRecordHook` | AfterExecution | Records structured events for each step with LLM-powered summaries |
| 4 | `KnowledgeExtractionHook` | AfterSynthesis | Extracts reusable knowledge from synthesis and stores in vector DB |
| 5 | `ActivityCleanupHook` | AfterSynthesis | Marks this agent's activity signal as completed |

**Key components:**

- **`LLMActivityCompactor`** — Compresses 200 raw events into a ~500-token digest via LLM. Supports incremental updates via `UpdateDigest()` to avoid reprocessing unchanged events.
- **`LLMEventSummarizer`** — Generates one-sentence factual summaries AND identifies domain entities for each execution step, extracting key identifiers (ticket IDs, pod names, etc.) from tool responses. Returns `core.StepSummary` with `Summary` (string) and `Entities` ([]EntityRef) fields.
- **`DigestCache`** — Caches compacted digests in Redis to avoid redundant LLM calls. Cache hit → 0ms; incremental update → 0.3-1s; full compaction → 5-10s.
- **`ActivityCoordinator`** — Transient Redis signals with TTL for real-time agent coordination (what agents are currently working on).
- **`NoOpEntityExtractor` / `LLMEntityExtractor`** — see "Entity Extraction" subsection below.

Wire via `memory.NewSharedBackends()` + `orchestration.BuildMemoryHooks()` — see [Agent Memory User Guide](../docs/memory-and-chat/AGENT_MEMORY_USER_GUIDE.md) for the complete pattern. See [ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md](../docs/building/ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md) for building custom hooks.

#### Entity Extraction

`MemoryEnrichmentHook` and `MemoryRecordHook` use an `EntityExtractor` to identify which domain entities each event/step operated on. Entities are then used as index keys for fast retrieval (e.g., "show me everything about `pod product-catalog-api`"). The framework ships two implementations:

| Extractor | When used | What it does |
|---|---|---|
| **`NoOpEntityExtractor`** | Default for the enrichment hook. Default for both hooks when no `AIClient` is wired. | Honors explicit `metadata["entity_type"]`/`entity_id` or multi-entity `metadata["entities"]`. Performs no extraction of its own. |
| **`LLMEntityExtractor`** | Default for the record hook when an `AIClient` is wired. | Reads entities the `EventSummarizer` LLM identifies as a side effect of summarization (the LLM emits `entities` alongside the summary in a single call — zero additional LLM round-trips). Falls through to explicit metadata if `llm_entities` is absent. |

**Auto-default selection** by `BuildMemoryHooks`:
- Record hook: `LLMEntityExtractor` when `aiClient != nil`, `NoOpEntityExtractor` otherwise.
- Enrichment hook: always `NoOpEntityExtractor`. Pre-planning, the summarizer hasn't run yet, so `LLMEntityExtractor` would always silently return nothing — making `NoOpEntityExtractor` the explicit default removes ambiguity. Pre-planning collision avoidance is handled by `ActivityCoordinator` (which has zero dependency on entity extraction).

**Per-hook overrides** for fine-grained control:
- `WithMemoryEntityExtractor(e)` — overrides BOTH hooks (shorthand)
- `WithBuilderRecordEntityExtractor(e)` — overrides record hook only
- `WithBuilderEnrichmentEntityExtractor(e)` — overrides enrichment hook only

Custom domain-specific extractors implement the `EntityExtractor` interface (a single `ExtractEntities(text, metadata) []Entity` method). The framework treats `Entity{Type, ID}` as an opaque key — domain semantics are entirely the application's responsibility.

## 23. Summary - What You've Learned

### This Module Gives You Two Superpowers:

#### 1. **AI Orchestration** - The Smart Assistant
- Understands natural language
- Figures out which tools and agents to call
- Adapts to available components
- Perfect for exploration and dynamic tasks

#### 2. **Workflow Engine** - The Reliable Machine
- Follows exact recipes
- Maximizes parallelism automatically
- Handles failures gracefully
- Perfect for production and repeated tasks

### Remember the Coffee Shop

Just like a coffee shop needs someone to:
- Take orders (orchestrator)
- Coordinate workers (executor)
- Ensure quality (synthesizer)
- Serve customers (response)

This module does the same for your tools and agents!

### Quick Decision Guide

**Choose AI Orchestration when:**
- You're exploring what's possible
- Requirements change frequently
- You want natural language interface
- Flexibility is more important than speed

**Choose Workflows when:**
- You know exactly what needs to happen
- You need predictable performance
- You want to minimize costs (no LLM calls)
- Reliability is critical

### The Power of Both

The real magic happens when you use both:
1. **Explore** with AI orchestration
2. **Discover** patterns that work
3. **Codify** into workflows
4. **Deploy** with confidence

---

**🎉 Congratulations!** You now understand how to conduct your component orchestra. Whether you choose AI's flexibility or workflows' reliability (or both!), you have the tools to build powerful multi-agent systems with both passive tools and active agents.
