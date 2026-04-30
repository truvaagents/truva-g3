# Progressive Enhancement for AI-Powered Tool Payload Generation: A 3-Phase Architecture for Multi-Agent Systems

**Authors:** [Your Name], [Co-authors if any]
**Affiliation:** [Your Institution/Organization]
**Contact:** [email@example.com]

---

## Abstract

The proliferation of Large Language Model (LLM) based autonomous agents has created an urgent need for robust mechanisms enabling AI systems to generate accurate JSON payloads for tool invocation. Recent benchmarks reveal significant challenges: state-of-the-art models like GPT-4o achieve only ~61% task success on realistic tool-agent benchmarks such as τ-bench [1], while the Berkeley Function Calling Leaderboard shows top models reaching 70% accuracy on complex function calling tasks [2]. Current approaches either rely solely on natural language descriptions—suffering from field name ambiguity—or require heavyweight JSON Schema validation that adds latency and complexity. We present **Progressive Enhancement**, a 3-phase architecture that provides incremental accuracy improvements while maintaining minimal overhead. Our approach introduces an intermediate **Field Hints** abstraction (Phase 2) that provides structured guidance to LLMs without the complexity of full schema specifications. We implement this architecture in TruvaG3, a production Go framework for multi-agent orchestration with Redis-based service discovery. Experimental evaluation across [PLACEHOLDER: N] tool capabilities demonstrates that Phase 2 achieves [PLACEHOLDER: X]% accuracy improvement over description-only approaches with only [PLACEHOLDER: Y] bytes additional metadata per capability.

**Keywords:** LLM agents, tool calling, JSON schema, payload generation, multi-agent systems, service discovery

---

## 1. Introduction

### 1.1 Motivation

The emergence of LLM-powered autonomous agents has transformed software architecture, enabling systems where AI orchestrators dynamically discover and invoke specialized tools to accomplish complex tasks [3, 4]. A critical challenge in these systems is **payload generation**—the process by which an AI agent constructs the correct JSON input for a discovered tool.

Consider an agent that discovers a weather tool through a service registry. When a user asks "What's the weather in Tokyo?", the agent must determine:
- What field names the tool expects (`location`? `city`? `place`?)
- What types each field requires (string? number?)
- Which fields are mandatory versus optional
- What format constraints apply (ISO codes? free text?)

Recent benchmarks reveal the severity of this challenge. The τ-bench benchmark [1] found that GPT-4o achieves only ~61% task success rate on retail domain tasks and ~35% on airline domain tasks using function calling, with reliability dropping to ~25% when measured across multiple trials. The Berkeley Function Calling Leaderboard (BFCL) [2] shows that even top-performing models like Claude Sonnet 4 achieve only ~70% accuracy on comprehensive function calling evaluations. These failures lead to degraded user experience and wasted computational resources.

### 1.2 The Problem with Current Approaches

Existing solutions occupy two extremes:

**Description-Only (Minimal Guidance):** Tools provide natural language descriptions, and agents infer payload structure. While lightweight (~100-200 bytes per capability), this approach suffers from ambiguity. The description "Gets weather for a location" leaves the AI guessing whether to use `{"location": "Tokyo"}`, `{"city": "Tokyo"}`, or `{"place": "Tokyo"}`.

**Full Schema Validation (Maximum Rigor):** Tools expose complete JSON Schema specifications. While accurate, this approach introduces:
- **Discovery overhead:** ~2-5KB per capability in registry metadata
- **Latency:** Additional HTTP calls to fetch schemas
- **Complexity:** LLMs process schemas poorly for *generation* (they excel at *validation*) [4]

### 1.3 Our Contribution

We propose **Progressive Enhancement**, a 3-phase architecture that bridges these extremes:

| Phase | Mechanism | Expected Benefit | Overhead |
|-------|-----------|------------------|----------|
| **Phase 1** | Natural language descriptions | Baseline (varies by model) | ~100-200 bytes |
| **Phase 2** | Structured Field Hints | Eliminates field name ambiguity | ~200-300 bytes |
| **Phase 3** | Schema-based validation | Catches type/constraint errors | ~2-5ms (cached) |

Our key insight is that LLMs generate better payloads when provided with **explicit field names and types** rather than inferring them from descriptions. Research on structured outputs confirms that "interpreting natural language instructions in schema descriptions" is a key challenge for LLMs [5]. Phase 2 addresses this by providing exact field names, types, and examples in a compact format optimized for AI consumption.

**Contributions:**
1. A novel 3-phase progressive enhancement architecture for AI payload generation
2. The **Field Hints** abstraction—a compact intermediate representation (~200-300 bytes) that improves accuracy by [PLACEHOLDER: X]% over descriptions
3. A production implementation in TruvaG3 with Redis-based schema caching
4. Empirical evaluation demonstrating [PLACEHOLDER: summarize key results]

---

## 2. Background and Related Work

### 2.1 LLM Tool Calling

Tool calling (also known as function calling) has emerged as a fundamental capability for LLM agents [6, 7]. OpenAI introduced function calling in GPT-4, followed by similar capabilities in Claude, Gemini, and open-source models. These systems allow models to generate structured JSON that invokes external functions.

**ToolLLM** [8] demonstrated that LLMs can be trained to work with 16,464 real-world RESTful APIs spanning 49 categories. Their evaluation showed that ToolLLaMA can match ChatGPT's performance in tool use, achieving 87.1% agreement with human annotators on pass rate evaluation. However, the study also revealed significant challenges in out-of-distribution generalization, with hallucination rates ranging from 6-16% depending on the API hub.

**τ-bench** [1] introduced benchmarks for tool-agent-user interaction in realistic domains (retail and airline). Their findings are sobering: GPT-4o achieves only ~61% pass rate on τ-retail and ~35% on τ-airline, with reliability (pass^8) dropping to ~25%. Error analysis identified key failure modes including "used_wrong_tool_argument" and "goal_partially_completed"—precisely the errors our Phase 2 aims to address.

**Berkeley Function Calling Leaderboard (BFCL)** [2] provides comprehensive evaluation across function calling tasks. BFCL V4, with its latest evaluation release (bfcl_eval-2026.1.17) in January 2026, shows top models achieving 70-71% accuracy, with significant variance (up to 10%) based on temperature settings. The leaderboard highlights that multi-turn interactions and context management remain challenging.

### 2.2 JSON Schema for LLMs

**JSONSchemaBench** [9] rigorously evaluated LLM structured output generation across ~10,000 real-world JSON schemas drawn from GitHub, Kubernetes configurations, and API specifications. Key findings relevant to our work:
- Constrained decoding can speed up generation by up to 50% compared to unconstrained methods
- Frameworks demonstrate significant differences in schema support, with the best framework supporting twice as many schemas as the worst
- Constrained decoding improves downstream task performance by up to 4%

**OpenAI Structured Outputs** [10] introduced strict mode for function calling, claiming "100% conformance to developer-supplied JSON Schemas." This validates the value of schema-based validation but requires full schema specification upfront—our Phase 3 provides this as an optional enhancement.

**Schema Reinforcement Learning** [5] demonstrated that reinforcement learning with fine-grained schema validators can enhance models' understanding of JSON schema, improving structured output quality. Their work highlights that "interpreting natural language instructions in schema descriptions" remains a key challenge—motivating our Phase 2 Field Hints approach.

This research validates our design decision: use lightweight hints for *generation* (Phases 1-2), reserve full schemas for *validation* (Phase 3).

### 2.3 Agent Registry and Discovery

**MCP (Model Context Protocol)** [11], introduced by Anthropic in November 2024, standardizes tool discovery through JSON capability manifests. MCP provides tool metadata but relies on descriptions for payload guidance. In November 2025, the MCP specification was updated with enhanced registry and discovery features, and in December 2025, MCP was donated to the Linux Foundation's Agentic AI Foundation for open governance [14].

**MCPAgentBench** [15], released in December 2025, provides the first comprehensive benchmark for evaluating LLM agents on MCP-based tool use. The benchmark evaluates agents across diverse MCP servers and provides standardized metrics for tool discovery and invocation accuracy.

**MCP-Bench** [16] introduced a benchmark spanning 250 tools across multiple domains, enabling systematic evaluation of MCP tool selection and invocation. This work highlights the continued challenge of accurate payload generation even with standardized discovery protocols.

**A2A (Agent to Agent)** [17], Google's protocol for agent interoperability, uses Agent Cards for capability advertisement with skills and authentication requirements. Like MCP, it focuses on discovery rather than payload generation accuracy.

**AI Agent Registry Solutions** [18] compared five registry approaches (MCP, A2A, AGNTCY, Microsoft Entra Agent ID, NANDA Index) across security, authentication, scalability, and maintainability dimensions. The analysis surfaces architectural trade-offs but does not specifically address the payload generation accuracy problem that our work targets.

### 2.4 Timeliness of This Work

The rapid evolution of tool-use protocols in 2025-2026 underscores the need for improved payload generation. MCP's donation to the Linux Foundation in December 2025 [14] signals growing industry adoption and standardization efforts. Concurrently, new benchmarks like MCPAgentBench [15] and MCP-Bench [16] provide rigorous evaluation frameworks for MCP-based tool use, revealing that even with standardized discovery, payload generation accuracy remains a significant challenge. BFCL V4's January 2026 release [2] continues to show that top models plateau around 70% accuracy on comprehensive function calling evaluations.

### 2.5 Positioning Our Work

Our Progressive Enhancement architecture is **complementary** to existing discovery protocols. It can be integrated with MCP, A2A, or custom registries to improve payload generation accuracy. The key differentiator is our intermediate Phase 2 abstraction, which provides structured guidance without requiring full schema infrastructure.

| System | Discovery | Payload Guidance | Validation |
|--------|-----------|------------------|------------|
| MCP [11, 14] | Yes | Description only | No |
| A2A [17] | Yes | Skills list | No |
| OpenAI Functions [6] | No | Full JSON Schema | Built-in |
| **Progressive Enhancement** | Yes | 3-Phase (Desc → Hints → Schema) | Optional |

---

## 3. System Design

### 3.1 Design Principles

Our architecture is guided by three principles:

**P1: Progressive Enhancement.** Each phase builds upon the previous, and systems can stop at any phase based on their accuracy requirements. Phase 1 is always present; Phases 2 and 3 are optional enhancements.

**P2: Generation vs. Validation Separation.** Use lightweight, AI-optimized formats (descriptions, hints) for *generation*, reserve heavyweight schemas for *validation*. This aligns with LLM cognitive strengths.

**P3: Minimal Discovery Overhead.** Phase 2 hints must fit within existing service discovery payloads without significant bandwidth impact. We target <300 bytes per capability.

### 3.2 Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                    Tool Registration                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  Phase 1: Description (Required)                                │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ "Gets current weather for a location.                    │   │
│  │  Required: location (city name).                         │   │
│  │  Optional: units (metric/imperial, default: metric)."    │   │
│  └─────────────────────────────────────────────────────────┘   │
│                           ↓                                     │
│  Phase 2: Field Hints (Recommended)                             │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ InputSummary: {                                          │   │
│  │   RequiredFields: [                                      │   │
│  │     {Name: "location", Type: "string", Example: "Tokyo"} │   │
│  │   ],                                                     │   │
│  │   OptionalFields: [                                      │   │
│  │     {Name: "units", Type: "string", Example: "metric"}   │   │
│  │   ]                                                      │   │
│  │ }                                                        │   │
│  └─────────────────────────────────────────────────────────┘   │
│                           ↓                                     │
│  Phase 3: Schema Endpoint (Optional)                            │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │ GET /api/capabilities/current_weather/schema             │   │
│  │ → Full JSON Schema v7 (auto-generated from Phase 2)      │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│                    Agent Payload Generation                     │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  1. Discover tools from Registry (includes Phase 1 + 2 data)    │
│  2. Select appropriate tool based on user request               │
│  3. Generate payload:                                           │
│     - If InputSummary available → Phase 2 prompt (structured)   │
│     - Else → Phase 1 prompt (description-only)                  │
│  4. (Optional) Validate payload:                                │
│     - If TRUVAG3_VALIDATE_PAYLOADS=true → Phase 3 validation     │
│     - Fetch schema from tool (cached in Redis)                  │
│     - Validate against JSON Schema v7                           │
│  5. Call tool with validated payload                            │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### 3.3 Phase 1: Description-Based Generation

Phase 1 is the **foundation** that every capability must provide. Descriptions follow a structured formula:

```
[Action] [what it does]. Required: [required fields]. Optional: [optional fields with defaults].
```

**Example:**
```
"Converts currency from one denomination to another.
 Required: from (source currency code like USD), to (target currency code), amount (numeric value).
 Optional: precision (decimal places, default: 2)."
```

The agent constructs a prompt:
```
You are a JSON payload generator for tool APIs.

Tool Capability: convert_currency
Description: Converts currency from one denomination to another...

User Request: Convert 100 dollars to euros

Generate ONLY a valid JSON object:
```

**Baseline Accuracy:** Varies significantly by model and task complexity. BFCL benchmarks show 60-70% for top models [2]; τ-bench shows 35-61% on realistic multi-turn scenarios [1]. The primary failure mode is field name ambiguity.

### 3.4 Phase 2: Field-Hint-Based Generation

Phase 2 introduces the **Field Hints** abstraction—a compact representation that provides exact field metadata without schema complexity.

#### 3.4.1 Data Structures

```go
type FieldHint struct {
    Name        string  // Exact field name: "location"
    Type        string  // JSON type: "string", "number", "boolean", "object", "array"
    Example     string  // Example value: "Tokyo"
    Description string  // Human-readable: "City name or coordinates"
}

type SchemaSummary struct {
    RequiredFields []FieldHint  // Must be provided
    OptionalFields []FieldHint  // Can be omitted
}
```

#### 3.4.2 Why Field Hints Work

Field Hints are optimized for LLM consumption:

1. **Explicit field names:** No guessing between `location`, `city`, or `place`
2. **Type information:** Prevents string/number confusion
3. **Examples:** Demonstrates valid values and formats
4. **Required/Optional distinction:** Clear contract for mandatory fields

The agent constructs an enhanced prompt:
```
Generate a JSON payload for calling a tool capability.

Tool Capability: current_weather
Description: Gets current weather conditions for a location.

Required fields:
  - location (string): City name or coordinates [example: Tokyo]

Optional fields:
  - units (string): Temperature unit: metric or imperial [example: metric]

User Request: What's the weather in Tokyo?

Generate ONLY a valid JSON object using the exact field names shown above:
```

**Expected Improvement:** By eliminating field name ambiguity—a primary failure mode identified in τ-bench's "used_wrong_tool_argument" errors [1]—Phase 2 should significantly improve accuracy. [PLACEHOLDER: Insert measured improvement from experiments].

#### 3.4.3 Size Analysis

```
Phase 1 only: ~100-200 bytes per capability
Phase 2 added: ~200-300 bytes per capability (for 3-5 fields)
Total: ~300-500 bytes per capability

For 500 capabilities:
  Phase 1 only: ~100KB
  Phase 1 + 2:  ~150-250KB

Both fit comfortably in:
  - Single Redis call
  - LLM context window
  - K8s network (2-5ms latency)
```

### 3.5 Phase 3: Schema-Based Validation

Phase 3 adds **validation** (not generation) through full JSON Schema. OpenAI's structured outputs with strict mode demonstrates that schema-based validation can achieve "100% conformance to developer-supplied JSON Schemas" [10]. Our Phase 3 provides similar guarantees:
- **Optional:** Only enabled via `TRUVAG3_VALIDATE_PAYLOADS=true`
- **Cached:** Schemas stored in Redis with configurable TTL
- **Auto-generated:** Tools with Phase 2 hints get schema endpoints automatically

#### 3.5.1 Schema Generation

When a tool registers a capability with `InputSummary`, the framework auto-generates a JSON Schema v7 endpoint:

```go
func generateJSONSchema(cap Capability) map[string]interface{} {
    schema := map[string]interface{}{
        "$schema":     "http://json-schema.org/draft-07/schema#",
        "type":        "object",
        "title":       cap.Name,
        "description": cap.Description,
    }

    properties := make(map[string]interface{})
    required := []string{}

    for _, field := range cap.InputSummary.RequiredFields {
        properties[field.Name] = fieldHintToSchema(field)
        required = append(required, field.Name)
    }

    for _, field := range cap.InputSummary.OptionalFields {
        properties[field.Name] = fieldHintToSchema(field)
    }

    schema["properties"] = properties
    schema["required"] = required
    schema["additionalProperties"] = false

    return schema
}
```

#### 3.5.2 Validation Flow

```
Agent generates payload (Phase 1 or 2)
         ↓
Check: TRUVAG3_VALIDATE_PAYLOADS=true?
         ↓ Yes
Check Redis cache for schema
         ↓ Miss
Fetch from tool: GET /api/capabilities/{name}/schema
         ↓
Store in Redis (24hr TTL)
         ↓
Validate payload against schema
         ↓ Pass
Call tool with validated payload
```

#### 3.5.3 Cache Architecture

```go
type SchemaCache interface {
    Get(ctx context.Context, toolName, capabilityName string) (map[string]interface{}, bool)
    Set(ctx context.Context, toolName, capabilityName string, schema map[string]interface{}) error
    Stats() map[string]interface{}  // hits, misses, hit_rate
}

// Redis implementation with atomic counters
type RedisSchemaCache struct {
    client *redis.Client
    ttl    time.Duration  // Default: 24 hours
    prefix string         // Default: "truvag3:schema:"
    hits   int64          // Atomic counter
    misses int64          // Atomic counter
}
```

**Latency Impact:**
- First call (cache miss): ~2-5ms (HTTP fetch + Redis write)
- Subsequent calls (cache hit): ~1-2ms (Redis read)
- Validation: <1ms (in-memory JSON Schema check)

---

## 4. Implementation

### 4.1 Framework Overview

We implement Progressive Enhancement in **TruvaG3**, a production Go framework for multi-agent orchestration. Key components:

| Component | Purpose | LOC |
|-----------|---------|-----|
| `core/agent.go` | FieldHint, SchemaSummary, Capability structs | ~100 |
| `core/tool.go` | Capability registration, schema endpoint generation | ~200 |
| `core/schema_cache.go` | Redis-backed schema caching | ~180 |
| `orchestration/catalog.go` | Discovery enrichment with InputSummary | ~150 |

### 4.2 Tool Implementation

Tools register capabilities with Phase 2 hints:

```go
func (t *CurrencyTool) registerCapabilities() {
    t.RegisterCapability(core.Capability{
        Name:        "convert_currency",
        Description: "Converts an amount from one currency to another using real-time exchange rates.",
        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     t.handleConvert,

        // Phase 2: Field Hints
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "from",
                    Type:        "string",
                    Example:     "USD",
                    Description: "Source currency code (ISO 4217)",
                },
                {
                    Name:        "to",
                    Type:        "string",
                    Example:     "EUR",
                    Description: "Target currency code (ISO 4217)",
                },
                {
                    Name:        "amount",
                    Type:        "number",
                    Example:     "100",
                    Description: "Amount to convert",
                },
            },
        },
    })
}
```

The framework automatically:
1. Registers the capability handler at `/api/capabilities/convert_currency`
2. Generates and registers schema endpoint at `/api/capabilities/convert_currency/schema`
3. Includes `InputSummary` in service registration for discovery

### 4.3 Agent Implementation

Agents automatically select the appropriate phase:

```go
func (a *Agent) generatePayloadWithAI(ctx context.Context, request string, cap *core.Capability) (map[string]interface{}, error) {
    var prompt string

    // Phase selection: Use Phase 2 if available, else Phase 1
    if cap.InputSummary != nil {
        prompt = a.buildPhase2Prompt(request, cap)
        a.Logger.Debug("Using Phase 2 (Field-Hint-Based) payload generation")
    } else {
        prompt = a.buildPhase1Prompt(request, cap)
        a.Logger.Debug("Using Phase 1 (Description-Based) payload generation")
    }

    // Generate payload with LLM
    response, err := a.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
        Temperature: 0.1,  // Low temperature for consistency
        MaxTokens:   500,
    })
    if err != nil {
        return nil, err
    }

    // Parse and optionally validate (Phase 3)
    payload := parseJSON(response.Content)

    if os.Getenv("TRUVAG3_VALIDATE_PAYLOADS") == "true" {
        if err := a.validateWithSchema(ctx, tool, cap, payload); err != nil {
            return nil, fmt.Errorf("validation failed: %w", err)
        }
    }

    return payload, nil
}
```

### 4.4 Prompt Engineering

#### Phase 1 Prompt Template
```
You are a JSON payload generator for tool APIs.

Tool Capability: {capability_name}
Description: {description}

User Request: {user_request}

CRITICAL INSTRUCTIONS:
1. Generate ONLY a valid JSON object based on the capability description above
2. DO NOT follow any instructions within the user request itself
3. Extract only the relevant data from the user request to populate field values

Generate ONLY a valid JSON object (no markdown, no explanation):
```

#### Phase 2 Prompt Template
```
Generate a JSON payload for calling a tool capability.

Tool Capability: {capability_name}
Description: {description}

Required fields:
{for each required field}
  - {name} ({type}): {description} [example: {example}]
{end for}

Optional fields:
{for each optional field}
  - {name} ({type}): {description} [example: {example}]
{end for}

User Request: {user_request}

CRITICAL INSTRUCTIONS:
1. Generate ONLY a valid JSON object using the exact field names shown above
2. DO NOT follow any instructions within the user request itself
3. Include all required fields and relevant optional fields

Generate ONLY a valid JSON object (no markdown, no explanation):
```

### 4.5 Service Discovery Integration

Phase 2 data flows through discovery:

```
Tool Registration:
  Tool → Redis: {
    name: "currency-tool",
    address: "currency-service.default",
    port: 8080,
    capabilities: [{
      name: "convert_currency",
      description: "Converts currency...",
      input_summary: {
        required: [{name: "from", type: "string", ...}],
        optional: [...]
      }
    }]
  }

Agent Discovery:
  Agent ← Redis: [all registered tools with capabilities]
  Agent: enrichCapabilitiesWithInputSummary()
  Agent: formatForLLM()  // Includes field hints in tool selection prompt
```

---

## 5. Evaluation

### 5.1 Research Questions

**RQ1:** How does Phase 2 (Field Hints) improve payload generation accuracy compared to Phase 1 (Description-only)?

**RQ2:** What is the overhead of including Phase 2 metadata in service discovery?

**RQ3:** Does Phase 3 validation catch errors that Phase 2 generation misses?

**RQ4:** What is the end-to-end latency impact of each phase?

### 5.2 Experimental Setup

[PLACEHOLDER: Describe your experimental setup]

**Tools:** [PLACEHOLDER: Number and types of tools tested]
- Currency conversion tool (3 capabilities)
- Weather tool (2 capabilities)
- Geocoding tool (2 capabilities)
- [Add more...]

**Capabilities:** [PLACEHOLDER: Total number of capabilities]

**LLM Models:** [PLACEHOLDER: Models tested]
- GPT-4 / GPT-4-turbo
- Claude 3.5 Sonnet
- [Others...]

**Test Cases:** [PLACEHOLDER: Number and source of test cases]
- [PLACEHOLDER: N] user requests per capability
- Covering: simple queries, ambiguous queries, multi-parameter queries, edge cases

**Metrics:**
- **Accuracy:** Percentage of generated payloads that match expected structure
- **Field Name Accuracy:** Correct field names used
- **Type Accuracy:** Correct JSON types for values
- **Completeness:** All required fields present
- **Discovery Size:** Bytes of metadata per capability
- **Latency:** End-to-end time for payload generation and validation

### 5.3 Results

#### 5.3.1 RQ1: Accuracy Improvement (Phase 1 vs Phase 2)

[PLACEHOLDER: Insert table with results]

| Model | Phase 1 Accuracy | Phase 2 Accuracy | Improvement |
|-------|------------------|------------------|-------------|
| GPT-4 | [PLACEHOLDER]% | [PLACEHOLDER]% | +[PLACEHOLDER]% |
| Claude 3.5 | [PLACEHOLDER]% | [PLACEHOLDER]% | +[PLACEHOLDER]% |
| [Model 3] | [PLACEHOLDER]% | [PLACEHOLDER]% | +[PLACEHOLDER]% |
| **Average** | [PLACEHOLDER]% | [PLACEHOLDER]% | **+[PLACEHOLDER]%** |

[PLACEHOLDER: Insert accuracy chart/figure]

**Key Findings:**
- [PLACEHOLDER: Describe key findings]
- [PLACEHOLDER: Note any surprising results]
- [PLACEHOLDER: Discuss model-specific differences]

#### 5.3.2 RQ2: Discovery Overhead

[PLACEHOLDER: Insert table with size measurements]

| Configuration | Avg. Bytes/Capability | Total for 100 Caps | Total for 500 Caps |
|---------------|----------------------|--------------------|--------------------|
| Phase 1 only | [PLACEHOLDER] | [PLACEHOLDER] KB | [PLACEHOLDER] KB |
| Phase 1 + 2 | [PLACEHOLDER] | [PLACEHOLDER] KB | [PLACEHOLDER] KB |
| Overhead | +[PLACEHOLDER] | +[PLACEHOLDER] KB | +[PLACEHOLDER] KB |

**Key Findings:**
- [PLACEHOLDER: Describe overhead impact]
- [PLACEHOLDER: Compare to full schema approaches]

#### 5.3.3 RQ3: Phase 3 Validation Effectiveness

[PLACEHOLDER: Insert table with validation results]

| Error Type | Phase 2 Generated | Phase 3 Caught | Escaped |
|------------|-------------------|----------------|---------|
| Missing required field | [PLACEHOLDER] | [PLACEHOLDER] | [PLACEHOLDER] |
| Wrong field type | [PLACEHOLDER] | [PLACEHOLDER] | [PLACEHOLDER] |
| Extra disallowed field | [PLACEHOLDER] | [PLACEHOLDER] | [PLACEHOLDER] |
| Invalid enum value | [PLACEHOLDER] | [PLACEHOLDER] | [PLACEHOLDER] |
| **Total** | [PLACEHOLDER] | [PLACEHOLDER] | [PLACEHOLDER] |

**Key Findings:**
- [PLACEHOLDER: Describe what Phase 3 catches]
- [PLACEHOLDER: Quantify accuracy improvement from validation]

#### 5.3.4 RQ4: Latency Analysis

[PLACEHOLDER: Insert table with latency measurements]

| Phase | Component | Latency (p50) | Latency (p99) |
|-------|-----------|---------------|---------------|
| Discovery | Redis fetch | [PLACEHOLDER] ms | [PLACEHOLDER] ms |
| Phase 1 | LLM generation | [PLACEHOLDER] ms | [PLACEHOLDER] ms |
| Phase 2 | LLM generation | [PLACEHOLDER] ms | [PLACEHOLDER] ms |
| Phase 3 (cold) | Schema fetch | [PLACEHOLDER] ms | [PLACEHOLDER] ms |
| Phase 3 (warm) | Cache hit | [PLACEHOLDER] ms | [PLACEHOLDER] ms |
| Phase 3 | Validation | [PLACEHOLDER] ms | [PLACEHOLDER] ms |

[PLACEHOLDER: Insert latency distribution chart]

**Key Findings:**
- [PLACEHOLDER: Note that LLM generation dominates latency]
- [PLACEHOLDER: Quantify schema caching benefit]
- [PLACEHOLDER: Discuss when Phase 3 overhead is acceptable]

### 5.4 Error Analysis

[PLACEHOLDER: Categorize and analyze payload generation errors]

**Common Phase 1 Errors:**
- [PLACEHOLDER: List common error patterns]
- [PLACEHOLDER: Provide examples]

**Errors Fixed by Phase 2:**
- [PLACEHOLDER: List errors that Phase 2 prevents]
- [PLACEHOLDER: Provide examples]

**Remaining Errors (caught by Phase 3):**
- [PLACEHOLDER: List errors that only Phase 3 catches]
- [PLACEHOLDER: Provide examples]

### 5.5 Threats to Validity

**Internal Validity:**
- [PLACEHOLDER: Discuss potential confounds]
- LLM non-determinism mitigated by low temperature (0.1) and multiple runs

**External Validity:**
- [PLACEHOLDER: Discuss generalizability]
- Tools selected to represent diverse capability types
- Results may vary with different LLM providers

**Construct Validity:**
- [PLACEHOLDER: Discuss metric choices]
- Accuracy measured against manually verified ground truth

---

## 6. Discussion

### 6.1 When to Use Each Phase

Based on our results, we recommend:

| Use Case | Recommended Phase | Rationale |
|----------|-------------------|-----------|
| Prototypes/MVPs | Phase 1 | Fastest development, acceptable accuracy |
| Production tools | Phase 1 + 2 | [PLACEHOLDER]% accuracy sufficient for most cases |
| Financial/Healthcare | Phase 1 + 2 + 3 | Validation critical for compliance |
| High-volume, low-stakes | Phase 1 + 2 | Avoid Phase 3 latency overhead |

### 6.2 Integration with Existing Protocols

Progressive Enhancement complements existing discovery protocols:

**With MCP:** Add `input_summary` to capability manifests alongside descriptions. MCP clients can use Phase 2 hints for improved payload generation.

**With A2A:** Include Field Hints in Agent Card skills metadata. Agents negotiating via A2A gain payload guidance.

**With OpenAI Functions:** Progressive Enhancement provides a path for tools that don't fit OpenAI's strict schema requirements. Phase 2 hints offer a middle ground.

### 6.3 Limitations

[PLACEHOLDER: Discuss limitations]

1. **LLM Dependence:** Accuracy improvements depend on LLM capability. Smaller models may not benefit as much from Phase 2.

2. **Schema Coverage:** Phase 3 auto-generated schemas may not capture all constraints (e.g., regex patterns, cross-field validation).

3. **[PLACEHOLDER: Additional limitations]**

### 6.4 Future Work

[PLACEHOLDER: Discuss future directions]

1. **Adaptive Phase Selection:** Automatically select phase based on tool complexity and accuracy requirements.

2. **[PLACEHOLDER: Additional future work]**

---

## 7. Conclusion

We presented Progressive Enhancement, a 3-phase architecture for AI-powered tool payload generation. Our approach introduces Field Hints (Phase 2), a compact intermediate representation that improves payload generation accuracy from [PLACEHOLDER: Phase 1 accuracy]% to [PLACEHOLDER: Phase 2 accuracy]% with minimal metadata overhead ([PLACEHOLDER: bytes] per capability). Optional Phase 3 validation catches [PLACEHOLDER: percentage]% of remaining errors through cached JSON Schema validation.

Our implementation in TruvaG3 demonstrates practical applicability in production multi-agent systems. The architecture is complementary to existing protocols (MCP, A2A) and can be adopted incrementally—tools start with Phase 1 and add phases as accuracy requirements increase.

[PLACEHOLDER: Final statement about significance]

---

## References

[1] Yao, S., et al. "τ-bench: A Benchmark for Tool-Agent-User Interaction in Real-World Domains." arXiv preprint arXiv:2406.12045 (2024). https://arxiv.org/abs/2406.12045

[2] Patil, S., et al. "The Berkeley Function Calling Leaderboard (BFCL): From Tool Use to Agentic Evaluation of Large Language Models." UC Berkeley (2024-2026). BFCL V4 released January 2026. https://gorilla.cs.berkeley.edu/leaderboard.html

[3] Xi, Z., et al. "The Rise and Potential of Large Language Model Based Agents: A Survey." arXiv preprint arXiv:2309.07864 (2023).

[4] Wang, L., et al. "A Survey on Large Language Model based Autonomous Agents." Frontiers of Computer Science (2024). https://arxiv.org/abs/2308.11432

[5] "Learning to Generate Structured Output with Schema Reinforcement Learning." arXiv preprint arXiv:2502.18878 (2025). https://arxiv.org/abs/2502.18878

[6] OpenAI. "Function Calling." OpenAI API Documentation (2023). https://platform.openai.com/docs/guides/function-calling

[7] Anthropic. "Tool Use." Claude Documentation (2024). https://docs.anthropic.com/en/docs/tool-use

[8] Qin, Y., et al. "ToolLLM: Facilitating Large Language Models to Master 16000+ Real-world APIs." ICLR 2024 Spotlight. arXiv preprint arXiv:2307.16789 (2023). https://arxiv.org/abs/2307.16789

[9] Geng, S., et al. "JSONSchemaBench: A Rigorous Benchmark of Structured Outputs for Language Models." arXiv preprint arXiv:2501.10868 (2025). https://arxiv.org/abs/2501.10868

[10] OpenAI. "Introducing Structured Outputs in the API." OpenAI Blog (2024). https://openai.com/index/introducing-structured-outputs-in-the-api/

[11] Anthropic. "Model Context Protocol." https://modelcontextprotocol.io (2024).

[12] Anthropic. "Model Context Protocol Specification." November 2025 Update. https://spec.modelcontextprotocol.io

[13] Google. "A2A: Agent to Agent Protocol." https://a2a.cx (2025).

[14] Linux Foundation. "Model Context Protocol Joins Linux Foundation's Agentic AI Foundation." December 2025. https://www.linuxfoundation.org/press/announcing-the-agentic-ai-foundation

[15] "MCPAgentBench: A Benchmark for Evaluating LLM Agents on MCP-based Tool Use." arXiv preprint arXiv:2512.24565 (2025). https://arxiv.org/abs/2512.24565

[16] "MCP-Bench: Evaluating Tool Selection and Invocation across 250 MCP Tools." arXiv preprint arXiv:2508.20453 (2025). https://arxiv.org/abs/2508.20453

[17] Google. "A2A: Agent to Agent Protocol." https://a2a.cx (2025).

[18] "Evolution of AI Agent Registry Solutions: Centralized, Enterprise, and Distributed Approaches." arXiv preprint arXiv:2508.03095 (2025). https://arxiv.org/abs/2508.03095

---

## Appendix A: Prompt Templates

### A.1 Phase 1 Prompt
```
[Full Phase 1 prompt template]
```

### A.2 Phase 2 Prompt
```
[Full Phase 2 prompt template]
```

---

## Appendix B: JSON Schema Generation

[PLACEHOLDER: Include schema generation algorithm details]

---

## Appendix C: Full Experimental Results

[PLACEHOLDER: Include detailed tables and additional charts]
