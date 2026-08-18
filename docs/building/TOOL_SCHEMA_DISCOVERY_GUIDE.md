# AI-Powered Payload Generation Guide

Welcome to the complete guide on how TruvaG3 helps your AI agents generate perfect JSON payloads for tool calls! This guide will walk you through everything step-by-step, with plenty of examples and explanations. ☕

## 📚 Table of Contents

- [What Is This and Why Should You Care?](#-what-is-this-and-why-should-you-care)
- [The Problem We're Solving](#-the-problem-were-solving)
- [The Solution: 3-Phase Progressive Enhancement](#-the-solution-3-phase-progressive-enhancement)
- [Phase 1: Description-Based Generation](#-phase-1-description-based-generation-always-present)
- [Phase 2: Field-Hint-Based Generation](#-phase-2-field-hint-based-generation-recommended)
- [Phase 3: Schema-Based Validation](#-phase-3-schema-based-validation-optional)
- [Implementation Guide for Tool Developers](#️-implementation-guide-for-tool-developers)
- [Implementation Guide for Agent Developers](#-implementation-guide-for-agent-developers)
- [How It Works Under the Hood](#️-how-it-works-under-the-hood)
- [Performance Characteristics](#-performance-characteristics)
- [Best Practices](#-best-practices)
- [Common Patterns and Solutions](#-common-patterns-and-solutions)
- [Troubleshooting](#-troubleshooting)
- [Summary](#-summary)

## 🎯 What Is This and Why Should You Care?

Let me explain this with a real-world analogy.

### The Restaurant Order Analogy

Imagine you're a waiter in a busy restaurant, and you need to place orders with the kitchen. The kitchen has many different stations:
- **The Grill** (needs: protein type, doneness, temperature)
- **The Salad Station** (needs: greens type, dressing, toppings)
- **The Dessert Bar** (needs: dessert name, portion size, add-ons)

Now, imagine three scenarios:

**Scenario 1 (Phase 1):** You get a note that says "Make something with chicken" - you have to guess what the grill needs.

**Scenario 2 (Phase 2):** You get a form with labeled blanks: "Protein: ______, Doneness: ______, Temperature: ______" - much easier!

**Scenario 3 (Phase 3):** The kitchen checks your filled form before cooking to make sure everything is valid - prevents mistakes!

**That's exactly what TruvaG3's 3-Phase approach does for AI agents!** It helps AI generate the right JSON payloads for tool calls, with progressive levels of guidance and validation.

### Why This Matters

When building AI agents that orchestrate tools, one critical challenge emerges:

**How does an AI agent know what JSON data to send to a tool?**

Consider this scenario:
```go
// Agent discovers a weather tool
tools := agent.DiscoverTools(ctx)

// Agent needs to call it for user request: "weather in London"
// What JSON should it send?
payload := ??? // {"location": "London"}? {"city": "London"}? {"place": "London"}?
```

Without guidance, the AI must guess field names, types, and structure. This leads to:
- ❌ Wrong field names (`city` vs `location`)
- ❌ Wrong types (string vs number)
- ❌ Missing required fields
- ❌ Unexpected extra fields
- ❌ Failed tool calls and poor user experience

**TruvaG3 solves this with a 3-phase progressive enhancement approach.**

## 🔍 The Problem We're Solving

Before we dive into the solution, let's understand the constraints and challenges:

### The Challenge: Scale and Efficiency

In a typical TruvaG3 deployment:
- **50-100 tools** × **3-5 capabilities each** = **150-500 total capabilities**
- Each capability needs payload metadata for AI generation
- AI has limited context window (~100k tokens)
- Network latency in K8s clusters (~2-5ms per call)
- Tools register capabilities in Redis for discovery

### The Questions We Need to Answer

1. **For Tool Selection:** How does the AI know which tool to use?
   - Answer: Natural language descriptions (Phase 1)

2. **For Payload Generation:** How does the AI know what fields to include?
   - Answer: Field hints with types and examples (Phase 2)

3. **For Validation:** How do we ensure the generated payload is correct?
   - Answer: JSON Schema validation with caching (Phase 3)

### Key Insights from 2025 LLM Research

Research in 2025 has significantly advanced our understanding of LLMs and structured outputs:

**SchemaBench Study (February 2025):** A comprehensive benchmark with ~40,000 JSON schemas revealed that while LLMs have improved, they still struggle with valid JSON generation without proper guidance. However, studies show that **descriptions and field hints remain the most effective mechanism for AI generation**, with reinforcement learning enhancing schema understanding.

**OpenAI's Structured Output Breakthrough:** The introduction of "strict mode" achieved **100% compliance** with JSON schemas (compared to just 35% with prompting alone), validating the critical insight:

> **"For LLMs interacting with schemas, descriptions are potent instructions and a fundamental part of the implicit prompt the model receives."**

**Industry Evolution:** Tools like Outlines, Transformers-cfg, and LLM 0.23 (February 2025) now provide grammar-constrained generation and schema support, but the fundamental principle remains:

**Implication:** AI generates best from *rich descriptions* and *field hints*, not from reading raw JSON Schemas. Schemas are for **validation**, not **generation**.

This 2025 research validates our 3-phase approach:
- **Phase 1 & 2:** Descriptions and hints for AI generation (what LLMs excel at)
- **Phase 3:** Full schemas for validation (ensuring 100% compliance)

## 🚀 The Solution: 3-Phase Progressive Enhancement

TruvaG3 uses a **progressive enhancement** approach - each phase builds on the previous one, and you can stop at any phase based on your needs.

### Overview: The Three Phases

```
┌─────────────────────────────────────────────────────────┐
│ Phase 3: Schema-Based Validation (OPTIONAL)            │
│ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │
│ • Validates AI-generated payloads before sending       │
│ • Only if TRUVAG3_VALIDATE_PAYLOADS=true                │
│ • Fetched from tool's /schema endpoint                 │
│ • Cached in Redis, 0ms overhead after first fetch      │
│ • Accuracy: ~99% (catches the ~4% Phase 2 misses)      │
├─────────────────────────────────────────────────────────┤
│ Phase 2: Field-Hint-Based Generation (RECOMMENDED)     │
│ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │
│ • AI uses exact field names from structured hints      │
│ • Includes in service discovery (registry)             │
│ • ~200-300 bytes per capability                        │
│ • Falls back to Phase 1 if hints unavailable           │
│ • Accuracy: ~95% for most tools                        │
├─────────────────────────────────────────────────────────┤
│ Phase 1: Description-Based Generation (ALWAYS PRESENT) │
│ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ │
│ • AI generates payloads from natural language          │
│ • Always included in service discovery (registry)      │
│ • ~100-200 bytes per capability                        │
│ • Works for all tools, no extra configuration          │
│ • Accuracy: ~85-90% baseline                           │
│ • Foundation that other phases build upon              │
└─────────────────────────────────────────────────────────┘
```

### How They Work Together

**Think of it like building a house:**

- **Phase 1** = Foundation (everyone has it, always present)
- **Phase 2** = Walls and roof (recommended for most homes)
- **Phase 3** = Security system (optional, for high-value homes)

**You don't choose one phase - they stack!** Each phase enhances the previous:

```go
// Phase 1: AI reads description
"Gets current weather for a location. Required: location (city name)."
↓
// Phase 2: AI sees structured hints
RequiredFields: [{Name: "location", Type: "string", Example: "London"}]
↓
// Phase 3: Generated payload is validated
Validate({"location": "London"}) → ✓ Valid
```

## 📝 Phase 1: Description-Based Generation (Always Present)

Phase 1 is the **foundation** of the 3-phase approach. Every tool capability MUST have a good description.

### What Is Phase 1?

Phase 1 uses **natural language descriptions** to help AI understand:
- What the capability does
- What inputs it expects
- What outputs it provides

### Why Phase 1 Alone Works Pretty Well

Modern LLMs (GPT-4, Claude, Gemini) are surprisingly good at inferring structure from descriptions. Research shows ~85-90% accuracy with well-written descriptions.

The key: **Descriptions are "implicit prompts"** that guide AI generation.

### How to Write Great Phase 1 Descriptions

A good description follows this formula:

```
[Action] [what it does]. Required: [required fields]. Optional: [optional fields with defaults].
```

**Examples:**

✅ **Good Description:**
```go
Description: "Gets current weather conditions for a location. " +
             "Required: location (city name). " +
             "Optional: units (metric/imperial, default: metric)."
```

❌ **Poor Description:**
```go
Description: "Weather API"  // Too vague!
```

✅ **Good Description:**
```go
Description: "Analyzes historical weather patterns. " +
             "Required: location (city name), start_date, end_date (YYYY-MM-DD format). " +
             "Optional: include_trends (boolean, default: false)."
```

### Tool Implementation - Phase 1

Here's how to implement Phase 1 in your tool:

```go
package main

import "github.com/truvaagents/truva-g3/core"

func main() {
    // Create a tool
    tool := core.NewTool("weather-service")

    // Register capability with Phase 1 description
    tool.RegisterCapability(core.Capability{
        Name: "current_weather",

        // Phase 1: Clear, structured description
        Description: "Gets current weather conditions for a location. " +
                     "Required: location (city name or coordinates). " +
                     "Optional: units (metric/imperial, default: metric), " +
                     "include_forecast (boolean, default: false).",

        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     handleWeather,
    })

    // Start the tool
    tool.Start(8080)
}

func handleWeather(w http.ResponseWriter, r *http.Request) {
    // Your handler logic here
}
```

### Agent Usage - Phase 1

When an agent discovers tools, it automatically gets Phase 1 descriptions:

```go
// Agent discovers all tools
tools := agent.DiscoverTools(ctx)

// Returns capabilities with descriptions:
// {
//   "name": "current_weather",
//   "description": "Gets current weather conditions for a location. Required: location (city name)..."
// }

// Agent sends description to AI for payload generation
prompt := fmt.Sprintf(`Generate JSON payload for tool: %s
Description: %s
User request: %s`, capability.Name, capability.Description, userRequest)

// AI generates: {"location": "London", "units": "metric"}
```

### When Phase 1 Alone Is Enough

Phase 1 is sufficient for:
- ✅ **Prototypes and MVPs** - Get started fast
- ✅ **Simple tools with 1-2 fields** - Descriptions work well
- ✅ **Internal tools with relaxed validation** - ~85-90% accuracy is fine
- ✅ **Tools with flexible input formats** - Don't need exact field names

## 🎯 Phase 2: Field-Hint-Based Generation (Recommended)

Phase 2 adds **structured field hints** to guide AI more precisely. This is the **recommended approach** for production tools.

### What Is Phase 2?

Phase 2 provides the AI with:
- **Exact field names** (no guessing: `location` not `city`)
- **Field types** (`string`, `number`, `boolean`, `object`, `array`)
- **Examples** (showing valid values)
- **Field descriptions** (what each field means)
- **Required vs Optional** (which fields must be included)

### Why Phase 2 Improves Accuracy

With Phase 2, AI doesn't need to infer field names - it gets them explicitly:

```
Phase 1: "Required: location (city name)"
         → AI guesses: "location"? "city"? "place"? "cityName"?

Phase 2: {Name: "location", Type: "string", Example: "London"}
         → AI knows exactly: "location" (no guessing!)
```

This boosts accuracy from ~85-90% to **~95%**.

### The SchemaSummary and FieldHint Structs

Phase 2 uses two simple structs:

```go
// FieldHint provides basic field information for AI-powered payload generation
type FieldHint struct {
    Name        string `json:"name"`                  // Field name (e.g., "location")
    Type        string `json:"type"`                  // JSON type: "string", "number", "boolean", "object", "array"
    Example     string `json:"example,omitempty"`     // Example value (e.g., "London")
    Description string `json:"description,omitempty"` // Human-readable description
}

// SchemaSummary provides compact schema hints for the registry
type SchemaSummary struct {
    RequiredFields []FieldHint `json:"required,omitempty"` // Fields that must be provided
    OptionalFields []FieldHint `json:"optional,omitempty"` // Fields that are optional
}
```

### Tool Implementation - Phase 2

Here's how to add Phase 2 to your tool (builds on Phase 1):

```go
package main

import "github.com/truvaagents/truva-g3/core"

func main() {
    tool := core.NewTool("weather-service")

    // Register capability with Phase 1 + Phase 2
    tool.RegisterCapability(core.Capability{
        Name: "current_weather",

        // Phase 1: Still include description (fallback + human-readable)
        Description: "Gets current weather conditions for a location.",

        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     handleWeather,

        // Phase 2: Add structured field hints
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "location",
                    Type:        "string",
                    Example:     "London",
                    Description: "City name or coordinates (lat,lon)",
                },
            },
            OptionalFields: []core.FieldHint{
                {
                    Name:        "units",
                    Type:        "string",
                    Example:     "metric",
                    Description: "Temperature unit: metric or imperial",
                },
                {
                    Name:        "include_forecast",
                    Type:        "boolean",
                    Example:     "false",
                    Description: "Include 7-day forecast in response",
                },
            },
        },
    })

    tool.Start(8080)
}
```

### Agent Usage - Phase 2

Agents automatically use Phase 2 hints if available:

```go
// Agent discovers tools with Phase 2 hints
capability := tool.GetCapabilities()[0]

// Check if Phase 2 hints are available
if capability.InputSummary != nil {
    // Build Phase 2 prompt with field hints
    prompt := buildPhase2Prompt(userRequest, capability)
    // Prompt includes exact field names, types, examples
} else {
    // Fall back to Phase 1 (description only)
    prompt := buildPhase1Prompt(userRequest, capability)
}

// AI generates payload with correct field names
payload := aiClient.GeneratePayload(ctx, prompt)
// Returns: {"location": "London", "units": "metric"}
```

### Phase 2 Prompt Example

Here's what the AI receives with Phase 2:

```
Generate a JSON payload for calling a tool capability.

Tool Capability: current_weather
Description: Gets current weather conditions for a location.

Required fields:
  - location (string): City name or coordinates (lat,lon) [example: London]

Optional fields:
  - units (string): Temperature unit: metric or imperial [example: metric]
  - include_forecast (boolean): Include 7-day forecast in response [example: false]

User Request: What's the weather in Tokyo?

Generate ONLY a valid JSON object using the exact field names shown above.
```

**AI generates:**
```json
{
  "location": "Tokyo",
  "units": "metric"
}
```

### Size Impact - Phase 2

Phase 2 adds minimal overhead to service discovery:

```
Phase 1 only: ~100-200 bytes per capability
Phase 2 added: ~200-300 bytes per capability
Total: ~300-500 bytes per capability

For 500 capabilities:
  Phase 1 only: ~100KB total
  Phase 1 + 2:  ~150-250KB total

Still fits easily in:
  - Single Redis call
  - AI context window
  - K8s network (2-5ms latency)
```

### When to Use Phase 2

Use Phase 2 for:
- ✅ **Production tools** - 95% accuracy matters
- ✅ **Tools with 3+ fields** - Field hints eliminate ambiguity
- ✅ **Public APIs** - Clear contract for external consumers
- ✅ **Tools with strict field names** - No room for guessing
- ✅ **Tools with complex types** - Objects, arrays need type hints

## 🔒 Phase 3: Schema-Based Validation (Optional)

Phase 3 adds **full JSON Schema validation** before sending payloads to tools. This is **optional** and meant for high-reliability scenarios.

### What Is Phase 3?

Phase 3:
- Fetches full JSON Schema from tool's `/schema` endpoint
- Validates AI-generated payloads against the schema
- Caches schemas in Redis (shared across agent replicas)
- Only runs if `TRUVAG3_VALIDATE_PAYLOADS=true` environment variable is set

### Why Phase 3 Is Optional

Phase 2 already gives 95% accuracy. Phase 3 adds validation to catch the remaining 5% of edge cases:
- Wrong types (string instead of number)
- Values outside allowed enums
- Missing required fields
- Extra disallowed fields
- Pattern/format violations

**Trade-off:** Phase 3 adds ~2-5ms latency on first call (schema fetch), then 0ms after (cached).

### The Schema Cache Architecture

Phase 3 uses a Redis-backed cache shared across all agent replicas:

```
┌─────────────────────────────────────────────────────────┐
│ RedisSchemaCache (Production Implementation)           │
├─────────────────────────────────────────────────────────┤
│ Redis Cache                                            │
│ - Shared across all agent replicas                     │
│ - ~1-2ms latency in K8s                                │
│ - 24-hour TTL (configurable)                           │
│ - Automatic key namespacing (truvag3:schema:)           │
│ - Thread-safe operations (atomic counters)             │
│ - Built-in statistics (hits, misses, hit rate)         │
└─────────────────────────────────────────────────────────┘
```

### Tool Implementation - Phase 3

Tools don't need to do anything special - the framework **auto-generates** schema endpoints:

```go
// Tool registers capability with Phase 2 hints
tool.RegisterCapability(core.Capability{
    Name: "current_weather",
    Description: "Gets current weather conditions for a location.",
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "location", Type: "string", Example: "London"},
        },
        // ...
    },
    Handler: handleWeather,
})

// Framework automatically creates schema endpoint:
// GET /api/capabilities/current_weather/schema
//
// Returns full JSON Schema v7 auto-generated from InputSummary:
// {
//   "$schema": "http://json-schema.org/draft-07/schema#",
//   "type": "object",
//   "properties": {
//     "location": {"type": "string", "description": "..."}
//   },
//   "required": ["location"],
//   "additionalProperties": false
// }
```

If you want a custom schema, implement the `/schema` endpoint yourself:

```go
// Custom schema endpoint (advanced)
tool.mux.HandleFunc("/api/capabilities/current_weather/schema", func(w http.ResponseWriter, r *http.Request) {
    schema := map[string]interface{}{
        "$schema": "http://json-schema.org/draft-07/schema#",
        "type": "object",
        "properties": map[string]interface{}{
            "location": map[string]interface{}{
                "type": "string",
                "minLength": 1,
                "pattern": "^[a-zA-Z ]+$|^-?\\d+\\.\\d+,-?\\d+\\.\\d+$",
                "description": "City name or coordinates",
            },
            "units": map[string]interface{}{
                "type": "string",
                "enum": []string{"metric", "imperial"},
                "default": "metric",
            },
        },
        "required": []string{"location"},
        "additionalProperties": false,
    }
    json.NewEncoder(w).Encode(schema)
})
```

### Agent Implementation - Phase 3

Agents enable Phase 3 by:

1. **Setting up Redis schema cache** (shared across replicas)
2. **Setting environment variable** `TRUVAG3_VALIDATE_PAYLOADS=true`

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/truvaagents/truva-g3/core"
)

func main() {
    // Create agent
    agent, err := NewResearchAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // Phase 3 Setup: Initialize schema cache with Redis
    if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
        redisOpt, err := redis.ParseURL(redisURL)
        if err != nil {
            log.Printf("⚠️  Warning: Failed to parse REDIS_URL")
            log.Println("   Schema caching will be disabled")
        } else {
            redisClient := redis.NewClient(core.ApplyRedisClientDefaults(redisOpt))

            // Test connection
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer cancel()

            if err := redisClient.Ping(ctx).Err(); err != nil {
                log.Printf("⚠️  Warning: Redis connection failed: %v", err)
                log.Println("   Schema caching will be disabled")
                redisClient.Close()
            } else {
                // Enable schema cache (default configuration)
                agent.SchemaCache = core.NewSchemaCache(redisClient)
                log.Println("✅ Schema cache initialized with Redis backend")

                // Or customize with options:
                // agent.SchemaCache = core.NewSchemaCache(redisClient,
                //     core.WithTTL(1 * time.Hour),        // Custom TTL
                //     core.WithPrefix("myapp:schemas:"),  // Custom prefix
                // )
            }
        }
    } else {
        log.Println("ℹ️  Schema caching disabled (no REDIS_URL)")
    }

    // Start agent
    agent.Start(8090)
}
```

### Agent Validation Logic - Phase 3

Here's how agents use Phase 3 validation:

```go
func (a *Agent) callTool(ctx context.Context, tool *core.ServiceInfo, capability core.Capability, userRequest string) error {
    // Phase 1 + 2: Generate payload with AI
    payload, err := a.generatePayloadWithAI(ctx, userRequest, capability)
    if err != nil {
        return fmt.Errorf("payload generation failed: %w", err)
    }

    // Phase 3: Validate payload (only if enabled)
    if os.Getenv("TRUVAG3_VALIDATE_PAYLOADS") == "true" {
        schema, err := a.fetchSchemaIfNeeded(ctx, tool, &capability)
        if err != nil {
            // Log warning but don't fail - validation is optional
            a.Logger.Warn("Schema fetch failed, proceeding without validation", map[string]interface{}{
                "tool":  tool.Name,
                "error": err.Error(),
            })
        } else {
            // Validate generated payload
            if err := a.validatePayload(payload, schema); err != nil {
                return fmt.Errorf("payload validation failed: %w", err)
            }
            a.Logger.Info("Payload validated successfully", map[string]interface{}{
                "tool":       tool.Name,
                "capability": capability.Name,
            })
        }
    }

    // Send validated payload to tool
    return a.sendToTool(ctx, tool, capability, payload)
}

// fetchSchemaIfNeeded fetches schema with caching
func (a *Agent) fetchSchemaIfNeeded(ctx context.Context, tool *core.ServiceInfo, capability *core.Capability) (map[string]interface{}, error) {
    // Check cache first
    if a.SchemaCache != nil {
        if schema, ok := a.SchemaCache.Get(ctx, tool.Name, capability.Name); ok {
            a.Logger.Debug("Schema cache hit", map[string]interface{}{
                "tool":       tool.Name,
                "capability": capability.Name,
            })
            return schema, nil
        }
    }

    // Cache miss - fetch from tool
    schemaEndpoint := capability.SchemaEndpoint
    if schemaEndpoint == "" {
        endpoint := capability.Endpoint
        if endpoint == "" {
            endpoint = fmt.Sprintf("/api/capabilities/%s", capability.Name)
        }
        schemaEndpoint = fmt.Sprintf("%s/schema", endpoint)
    }

    url := fmt.Sprintf("http://%s:%d%s", tool.Address, tool.Port, schemaEndpoint)

    // Fetch with timeout
    httpCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()

    req, err := http.NewRequestWithContext(httpCtx, "GET", url, nil)
    if err != nil {
        return nil, fmt.Errorf("schema request creation failed: %w", err)
    }

    resp, err := a.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("schema fetch failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("schema fetch returned status %d", resp.StatusCode)
    }

    var schema map[string]interface{}
    if err := json.NewDecoder(resp.Body).Decode(&schema); err != nil {
        return nil, fmt.Errorf("schema parse failed: %w", err)
    }

    // Cache for future use
    if a.SchemaCache != nil {
        if err := a.SchemaCache.Set(ctx, tool.Name, capability.Name, schema); err != nil {
            // Log but don't fail
            a.Logger.Warn("Failed to cache schema", map[string]interface{}{
                "error": err.Error(),
            })
        }
    }

    return schema, nil
}

// validatePayload performs basic JSON Schema validation
func (a *Agent) validatePayload(payload map[string]interface{}, schema map[string]interface{}) error {
    // Check required fields
    required, ok := schema["required"].([]interface{})
    if ok {
        for _, reqField := range required {
            fieldName, ok := reqField.(string)
            if !ok {
                continue
            }
            if _, exists := payload[fieldName]; !exists {
                return fmt.Errorf("missing required field: %s", fieldName)
            }
        }
    }

    // Check additionalProperties
    additionalProps, ok := schema["additionalProperties"].(bool)
    if ok && !additionalProps {
        properties, ok := schema["properties"].(map[string]interface{})
        if ok {
            for fieldName := range payload {
                if _, allowed := properties[fieldName]; !allowed {
                    return fmt.Errorf("unexpected field: %s (not in schema)", fieldName)
                }
            }
        }
    }

    // For production, use a full JSON Schema validator:
    // import "github.com/xeipuuv/gojsonschema"

    return nil
}
```

### Environment Variables - Phase 3

```bash
# Enable Phase 3 validation (disabled by default)
export TRUVAG3_VALIDATE_PAYLOADS=true

# Redis URL for schema cache (required for Phase 3)
export REDIS_URL=redis://localhost:6379
```

### Schema Cache Options

The schema cache supports customization via options:

```go
// Default configuration (24-hour TTL, "truvag3:schema:" prefix)
agent.SchemaCache = core.NewSchemaCache(redisClient)

// Custom TTL (shorter if schemas change frequently)
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(1 * time.Hour),
)

// Custom prefix (multi-tenant deployments)
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithPrefix("tenant-abc:schema:"),
)

// Both customizations
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(6 * time.Hour),
    core.WithPrefix("prod:schemas:"),
)
```

### Schema Cache Statistics

Monitor cache performance with built-in stats:

```go
// Get cache statistics
if agent.SchemaCache != nil {
    stats := agent.SchemaCache.Stats()
    // Returns:
    // {
    //   "hits": 1295,
    //   "misses": 105,
    //   "total_lookups": 1400,
    //   "hit_rate": 0.925
    // }

    log.Printf("Schema cache stats: %+v", stats)
}
```

### When to Use Phase 3

Enable Phase 3 for:
- ✅ **High-reliability systems** - Every payload must be correct
- ✅ **Financial/healthcare tools** - Errors have real consequences
- ✅ **New tool integrations** - Validate untested tools
- ✅ **Strict compliance requirements** - Need proof of validation
- ✅ **Development/testing** - Catch payload errors early

Skip Phase 3 for:
- ❌ **Performance-critical paths** - Save ~5ms on first call
- ❌ **Mature, stable tools** - 95% accuracy is sufficient
- ❌ **High-volume, low-stakes operations** - Volume > perfection

## 🛠️ Implementation Guide for Tool Developers

This section is your step-by-step guide to implementing the 3-phase approach in your tools.

### Step 1: Write a Great Description (Phase 1)

Every capability needs a clear, structured description:

```go
tool.RegisterCapability(core.Capability{
    Name: "search_products",

    // Phase 1: Follow the formula
    Description: "Searches product catalog by keyword. " +
                 "Required: query (search term). " +
                 "Optional: category (filter by category), " +
                 "max_results (default: 10), " +
                 "sort_by (relevance/price/rating, default: relevance).",

    Handler: handleProductSearch,
})
```

**Formula:** `[Action] [what]. Required: [fields]. Optional: [fields with defaults].`

### Step 2: Add Field Hints (Phase 2)

Add `InputSummary` with structured field hints:

```go
tool.RegisterCapability(core.Capability{
    Name: "search_products",
    Description: "Searches product catalog by keyword.",
    Handler: handleProductSearch,

    // Phase 2: Add field hints
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {
                Name:        "query",
                Type:        "string",
                Example:     "wireless headphones",
                Description: "Search term to find products",
            },
        },
        OptionalFields: []core.FieldHint{
            {
                Name:        "category",
                Type:        "string",
                Example:     "electronics",
                Description: "Filter results by category",
            },
            {
                Name:        "max_results",
                Type:        "number",
                Example:     "10",
                Description: "Maximum number of results to return (1-100)",
            },
            {
                Name:        "sort_by",
                Type:        "string",
                Example:     "relevance",
                Description: "Sort order: relevance, price, or rating",
            },
        },
    },
})
```

### Step 2b: Add Output Schema (OutputSummary)

While `InputSummary` declares what fields a capability **accepts**, `OutputSummary` declares what fields it **returns**. This enables the orchestrator to validate template references in DAG plans — catching hallucinated field names before execution.

```go
tool.RegisterCapability(core.Capability{
    Name: "search_products",
    Description: "Searches product catalog by keyword.",
    Handler: handleProductSearch,

    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "query", Type: "string", Example: "wireless headphones",
                Description: "Search term to find products"},
        },
    },

    // Declare what this capability returns
    OutputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {Name: "products", Type: "array",
                Description: "List of matching products with name, price, and rating"},
            {Name: "total_count", Type: "number",
                Description: "Total number of matching products"},
            {Name: "query", Type: "string",
                Description: "Search query that was executed"},
        },
        OptionalFields: []core.FieldHint{
            {Name: "facets", Type: "object",
                Description: "Category and brand facets for filtering"},
        },
    },
})
```

**How it works in practice:**

When the orchestrator generates a DAG plan like:
```
Step 1: search_products → {"query": "wireless headphones"}
Step 2: create_report  → {"data": "{{step-1.response.data.products}}"}
```

The orchestrator's `validateTemplatePaths` function checks that `products` is a declared field in `search_products`'s `OutputSummary`. If the LLM hallucinated a field name (e.g., `results` instead of `products`), the validation catches it and requests a corrected plan.

**Key rules:**
- Field names must exactly match your response struct's JSON tags
- If you omit `OutputSummary`, template references pass through without validation (backwards compatible)
- Only top-level fields are validated; nested paths are not yet checked

### Step 3: Test Your Tool

Test that AI can generate correct payloads:

```go
// Test Phase 1 (description only)
// Agent should infer: {"query": "..."}

// Test Phase 2 (with hints)
// Agent should generate: {"query": "...", "category": "...", "max_results": 10}
```

### Step 4: (Optional) Custom Schema Endpoint (Phase 3)

If you need custom validation rules, implement a schema endpoint:

```go
tool.mux.HandleFunc("/api/capabilities/search_products/schema", func(w http.ResponseWriter, r *http.Request) {
    schema := map[string]interface{}{
        "$schema": "http://json-schema.org/draft-07/schema#",
        "type": "object",
        "properties": map[string]interface{}{
            "query": map[string]interface{}{
                "type": "string",
                "minLength": 1,
                "maxLength": 200,
            },
            "category": map[string]interface{}{
                "type": "string",
                "enum": []string{"electronics", "clothing", "books", "home"},
            },
            "max_results": map[string]interface{}{
                "type": "number",
                "minimum": 1,
                "maximum": 100,
                "default": 10,
            },
            "sort_by": map[string]interface{}{
                "type": "string",
                "enum": []string{"relevance", "price", "rating"},
                "default": "relevance",
            },
        },
        "required": []string{"query"},
        "additionalProperties": false,
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(schema)
})
```

### Complete Tool Example

Here's a complete weather tool with all 3 phases:

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"

    "github.com/truvaagents/truva-g3/core"
)

type WeatherTool struct {
    *core.BaseTool
}

func NewWeatherTool() *WeatherTool {
    tool := &WeatherTool{
        BaseTool: core.NewTool("weather-service"),
    }
    tool.registerCapabilities()
    return tool
}

func (w *WeatherTool) registerCapabilities() {
    // Current weather capability with all 3 phases
    w.RegisterCapability(core.Capability{
        Name: "current_weather",

        // Phase 1: Clear description
        Description: "Gets current weather conditions for a location. " +
                     "Required: location (city name). " +
                     "Optional: units (metric/imperial, default: metric).",

        InputTypes:  []string{"json"},
        OutputTypes: []string{"json"},
        Handler:     w.handleCurrentWeather,

        // Phase 2: Field hints
        InputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {
                    Name:        "location",
                    Type:        "string",
                    Example:     "London",
                    Description: "City name or coordinates (lat,lon)",
                },
            },
            OptionalFields: []core.FieldHint{
                {
                    Name:        "units",
                    Type:        "string",
                    Example:     "metric",
                    Description: "Temperature unit: metric or imperial",
                },
            },
        },

        // Output schema: Declares what fields this capability returns
        // Used by the orchestrator for template path validation in DAG plans
        OutputSummary: &core.SchemaSummary{
            RequiredFields: []core.FieldHint{
                {Name: "location", Type: "string", Description: "Requested location"},
                {Name: "temperature", Type: "number", Description: "Current temperature"},
                {Name: "units", Type: "string", Description: "Temperature units (metric/imperial)"},
                {Name: "condition", Type: "string", Description: "Weather condition description"},
            },
        },
        // Phase 3: Schema endpoint auto-generated by framework
    })
}

func (w *WeatherTool) handleCurrentWeather(writer http.ResponseWriter, r *http.Request) {
    var req struct {
        Location string `json:"location"`
        Units    string `json:"units"`
    }

    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(writer, "Invalid request", http.StatusBadRequest)
        return
    }

    // Default units
    if req.Units == "" {
        req.Units = "metric"
    }

    // Mock weather data
    response := map[string]interface{}{
        "location":    req.Location,
        "temperature": 22,
        "units":       req.Units,
        "condition":   "Partly cloudy",
    }

    writer.Header().Set("Content-Type", "application/json")
    json.NewEncoder(writer).Encode(response)
}

func main() {
    tool := NewWeatherTool()

    log.Println("Starting weather tool on :8080")
    if err := tool.Start(8080); err != nil {
        log.Fatalf("Failed to start tool: %v", err)
    }
}
```

## 🤖 Implementation Guide for Agent Developers

This section guides you through implementing the 3-phase approach in your AI agents.

### Step 1: Basic Agent Setup

Create your agent structure:

```go
package main

import (
    "github.com/truvaagents/truva-g3/core"
)

type ResearchAgent struct {
    *core.BaseAgent
    aiClient   core.AIClient
    httpClient *http.Client
}

func NewResearchAgent() (*ResearchAgent, error) {
    agent := &ResearchAgent{
        BaseAgent:  core.NewBaseAgent("research-agent"),
        httpClient: &http.Client{Timeout: 30 * time.Second},
    }

    // Initialize AI client (OpenAI, Anthropic, etc.)
    aiClient, err := core.NewOpenAIClient(
        os.Getenv("OPENAI_API_KEY"),
        "gpt-4",
    )
    if err != nil {
        return nil, err
    }
    agent.aiClient = aiClient

    return agent, nil
}
```

### Step 2: Tool Discovery

Discover tools and their capabilities:

```go
func (a *ResearchAgent) researchTopic(ctx context.Context, topic string) (string, error) {
    // Discover all available tools (gets Phase 1 + Phase 2 data)
    tools, err := a.DiscoverTools(ctx)
    if err != nil {
        return "", fmt.Errorf("tool discovery failed: %w", err)
    }

    // Each tool has capabilities with:
    // - Phase 1: Description
    // - Phase 2: InputSummary (if provided)

    a.Logger.Info("Discovered tools", map[string]interface{}{
        "count": len(tools),
    })

    return a.orchestrateResearch(ctx, topic, tools)
}
```

### Step 3: AI Payload Generation (Phase 1 + 2)

Generate payloads using AI with automatic phase selection:

```go
func (a *ResearchAgent) generatePayloadWithAI(ctx context.Context, userRequest string, capability *core.Capability) (map[string]interface{}, error) {
    var prompt string

    // Automatic phase selection
    if capability.InputSummary != nil {
        // Phase 2: Use field hints (95% accuracy)
        prompt = a.buildPhase2Prompt(userRequest, capability)
        a.Logger.Debug("Using Phase 2 payload generation", map[string]interface{}{
            "capability": capability.Name,
        })
    } else {
        // Phase 1: Fall back to description (85-90% accuracy)
        prompt = a.buildPhase1Prompt(userRequest, capability)
        a.Logger.Debug("Using Phase 1 payload generation", map[string]interface{}{
            "capability": capability.Name,
        })
    }

    // Generate payload with AI
    response, err := a.aiClient.GenerateResponse(ctx, prompt, &core.AIOptions{
        Temperature: 0.1, // Low temperature for consistent output
        MaxTokens:   500,
    })
    if err != nil {
        return nil, fmt.Errorf("AI generation failed: %w", err)
    }

    // Parse JSON response
    var payload map[string]interface{}
    content := strings.TrimSpace(response.Content)

    // Strip markdown code blocks if present
    if strings.HasPrefix(content, "```") {
        content = a.stripCodeBlocks(content)
    }

    if err := json.Unmarshal([]byte(content), &payload); err != nil {
        return nil, fmt.Errorf("failed to parse AI payload: %w", err)
    }

    return payload, nil
}

// Phase 1 prompt builder
func (a *ResearchAgent) buildPhase1Prompt(userRequest string, capability *core.Capability) string {
    return fmt.Sprintf(`You are a JSON payload generator for tool APIs.

Tool Capability: %s
Description: %s

User Request: %s

Generate ONLY a valid JSON object (no markdown, no explanation):`,
        capability.Name, capability.Description, userRequest)
}

// Phase 2 prompt builder
func (a *ResearchAgent) buildPhase2Prompt(userRequest string, capability *core.Capability) string {
    prompt := fmt.Sprintf(`Generate a JSON payload for calling a tool capability.

Tool Capability: %s
Description: %s

Required fields:
`, capability.Name, capability.Description)

    for _, field := range capability.InputSummary.RequiredFields {
        prompt += fmt.Sprintf("  - %s (%s): %s", field.Name, field.Type, field.Description)
        if field.Example != "" {
            prompt += fmt.Sprintf(" [example: %s]", field.Example)
        }
        prompt += "\n"
    }

    if len(capability.InputSummary.OptionalFields) > 0 {
        prompt += "\nOptional fields:\n"
        for _, field := range capability.InputSummary.OptionalFields {
            prompt += fmt.Sprintf("  - %s (%s): %s", field.Name, field.Type, field.Description)
            if field.Example != "" {
                prompt += fmt.Sprintf(" [example: %s]", field.Example)
            }
            prompt += "\n"
        }
    }

    prompt += fmt.Sprintf(`
User Request: %s

Generate ONLY a valid JSON object using the exact field names shown above:`, userRequest)

    return prompt
}
```

### Step 4: (Optional) Enable Phase 3 Validation

Initialize schema cache in `main.go`:

```go
func main() {
    agent, err := NewResearchAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // Phase 3: Initialize schema cache
    if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
        redisOpt, err := redis.ParseURL(redisURL)
        if err != nil {
            log.Printf("⚠️  Warning: Failed to parse REDIS_URL")
        } else {
            redisClient := redis.NewClient(core.ApplyRedisClientDefaults(redisOpt))

            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
            defer cancel()

            if err := redisClient.Ping(ctx).Err(); err != nil {
                log.Printf("⚠️  Warning: Redis connection failed: %v", err)
                redisClient.Close()
            } else {
                agent.SchemaCache = core.NewSchemaCache(redisClient)
                log.Println("✅ Schema cache initialized")
            }
        }
    }

    // Start agent
    if err := agent.Start(8090); err != nil {
        log.Fatalf("Failed to start agent: %v", err)
    }
}
```

Add validation to tool calling logic:

```go
func (a *ResearchAgent) callTool(ctx context.Context, tool *core.ServiceInfo, capability core.Capability, userRequest string) (*ToolResult, error) {
    startTime := time.Now()

    // Phase 1 + 2: Generate payload
    payload, err := a.generatePayloadWithAI(ctx, userRequest, &capability)
    if err != nil {
        return nil, fmt.Errorf("payload generation failed: %w", err)
    }

    // Phase 3: Optional validation
    if os.Getenv("TRUVAG3_VALIDATE_PAYLOADS") == "true" {
        schema, err := a.fetchSchemaIfNeeded(ctx, tool, &capability)
        if err != nil {
            a.Logger.Warn("Schema fetch failed, proceeding without validation", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            if err := a.validatePayload(payload, schema); err != nil {
                return nil, fmt.Errorf("payload validation failed: %w", err)
            }
            a.Logger.Info("Payload validated successfully")
        }
    }

    // Call tool
    return a.sendPayloadToTool(ctx, tool, capability, payload)
}
```

### Complete Agent Example

See `examples/agent-example/` for a complete working implementation with all 3 phases.

## ⚙️ How It Works Under the Hood

Let's trace a complete request through all 3 phases.

### Scenario: User asks "What's the weather in Tokyo?"

```
┌──────────────────────────────────────────────────────────────┐
│ Step 1: Tool Discovery (Redis)                              │
│ ──────────────────────────────────────────────────────────── │
│                                                              │
│ Agent → Redis: GET discovery/tools?type=tool                │
│ Redis → Agent: [                                            │
│   {                                                          │
│     "name": "weather-service",                              │
│     "address": "weather-service.default.svc.cluster.local", │
│     "port": 8080,                                           │
│     "capabilities": [{                                      │
│       "name": "current_weather",                            │
│       // Phase 1:                                           │
│       "description": "Gets current weather conditions...",  │
│       // Phase 2:                                           │
│       "input_summary": {                                    │
│         "required": [{"name": "location", "type": "string"}]│
│       }                                                      │
│     }]                                                       │
│   },                                                         │
│   ... 99 more tools                                         │
│ ]                                                            │
│                                                              │
│ Size: ~150KB for 500 capabilities                          │
│ Time: ~2-5ms (single Redis call)                           │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ Step 2: Tool Selection (AI)                                 │
│ ──────────────────────────────────────────────────────────── │
│                                                              │
│ Agent → AI: "User asked: 'weather in Tokyo'                 │
│             Available tools:                                │
│             - weather-service.current_weather: Gets weather │
│             - stock-service.get_quote: Gets stock price     │
│             ... 498 more                                    │
│             Which tool?"                                    │
│                                                              │
│ AI → Agent: "weather-service.current_weather"               │
│                                                              │
│ Time: ~500-2000ms (AI API call)                            │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ Step 3: Payload Generation (AI + Phase 1/2)                 │
│ ──────────────────────────────────────────────────────────── │
│                                                              │
│ Agent checks: capability.InputSummary != nil? → YES         │
│ → Use Phase 2 (field hints)                                 │
│                                                              │
│ Agent → AI: "Generate JSON for current_weather:             │
│             Required fields:                                │
│             - location (string): City name [example: London]│
│             Optional fields:                                │
│             - units (string): metric/imperial [example: ...]│
│             User request: 'weather in Tokyo'"               │
│                                                              │
│ AI → Agent: {"location": "Tokyo", "units": "metric"}        │
│                                                              │
│ Time: ~500-2000ms (AI API call)                            │
│ Accuracy: ~95% (Phase 2 hints)                             │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ Step 4: Validation (Phase 3, OPTIONAL)                      │
│ ──────────────────────────────────────────────────────────── │
│                                                              │
│ IF TRUVAG3_VALIDATE_PAYLOADS=true:                          │
│                                                              │
│ Agent checks: SchemaCache.Get("weather-service", "current_weather")│
│                                                              │
│ First Call (Cache Miss):                                    │
│   Agent → Tool: GET /api/capabilities/current_weather/schema│
│   Tool → Agent: {                                           │
│     "$schema": "...",                                       │
│     "properties": {"location": {"type": "string"}, ...},    │
│     "required": ["location"]                                │
│   }                                                          │
│   Agent → Redis: SET truvag3:schema:weather-service:current_weather│
│   Time: ~2-5ms (HTTP + Redis write)                        │
│                                                              │
│ Subsequent Calls (Cache Hit):                               │
│   Agent → Redis: GET truvag3:schema:weather-service:current_weather│
│   Redis → Agent: {schema}                                   │
│   Time: ~1-2ms (Redis read)                                 │
│                                                              │
│ Agent validates: payload against schema → ✓ Valid           │
│                                                              │
│ ELSE: Skip validation                                        │
│                                                              │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ Step 5: Tool Call (HTTP)                                    │
│ ──────────────────────────────────────────────────────────── │
│                                                              │
│ Agent → Tool: POST http://weather-service:8080/api/capabilities/current_weather│
│               Body: {"location": "Tokyo", "units": "metric"}│
│                                                              │
│ Tool → Agent: {                                             │
│   "location": "Tokyo",                                      │
│   "temperature": 18,                                        │
│   "condition": "Sunny"                                      │
│ }                                                            │
│                                                              │
│ Time: ~50-200ms (HTTP call in K8s)                         │
└──────────────────────────────────────────────────────────────┘

Total Time Breakdown:
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Step 1: Discovery         ~5ms
Step 2: Tool Selection    ~1000ms (AI)
Step 3: Payload Gen       ~1000ms (AI)
Step 4: Validation        ~5ms (first call) / ~0ms (cached)
Step 5: Tool Call         ~100ms
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
TOTAL:                    ~2110ms

Bottleneck: AI calls (Steps 2 & 3) = ~2000ms
Schema overhead: ~5ms first call, 0ms after (negligible)
```

### Key Observations

1. **AI dominates latency** (~2000ms) - schema operations are negligible (~5ms)
2. **Discovery is efficient** - Single Redis call gets 500 capabilities in ~5ms
3. **Phase 2 data is small** - ~200-300 bytes per capability, fits in discovery response
4. **Phase 3 caching works** - 0ms overhead after first schema fetch
5. **Validation is optional** - Can skip Phase 3 entirely for performance

## 📊 Performance Characteristics

### Latency Breakdown by Phase

```
Phase 1 (Description):
  Discovery: 2-5ms (Redis)
  AI Generation: 500-2000ms
  Total: ~500-2000ms

Phase 2 (Field Hints):
  Discovery: 2-5ms (Redis, includes hints)
  AI Generation: 500-2000ms
  Total: ~500-2000ms

Phase 3 (Validation):
  First call: +2-5ms (schema fetch + cache)
  Subsequent: +0ms (cached in Redis)
  Validation: <1ms (in-memory check)
  Total: +2-5ms (one-time), then +0ms

Combined (All 3 Phases):
  First call: ~505-2010ms
  Subsequent: ~500-2000ms (0ms schema overhead)
```

### Memory Impact

```
Registry Size (500 capabilities):
  Phase 1 only: ~100KB
  Phase 1 + 2:  ~150-250KB
  Phase 3:      Not in registry (fetched on-demand)

Redis Schema Cache (500 capabilities):
  Storage: ~500KB-1MB (full schemas)
  Shared across: All agent replicas
  TTL: 24 hours (configurable)

Per-Agent Memory (without Phase 3):
  No schema storage needed

Per-Agent Memory (with Phase 3 + Redis cache):
  Minimal (schemas in Redis, not agent memory)
```

### Accuracy Improvements

```
Phase 1 (Description-Based):
  Accuracy: ~85-90%
  Good for: Simple tools, 1-2 fields

Phase 2 (Field-Hint-Based):
  Accuracy: ~95%
  Improvement: +5-10% over Phase 1
  Good for: Production tools, 3+ fields

Phase 3 (Schema Validation):
  Accuracy: ~99%
  Improvement: +4% over Phase 2 (catches edge cases)
  Good for: Mission-critical, high-reliability systems
```

### Network Calls Per Request

```
Without Phase 3:
  1. Discovery (Redis): 1 call
  2. AI Generation: 2-3 calls
  3. Tool Call: 1 call
  Total: 4-5 network calls

With Phase 3 (First Call):
  1. Discovery (Redis): 1 call
  2. AI Generation: 2-3 calls
  3. Schema Fetch (HTTP): 1 call
  4. Schema Cache (Redis): 1 write
  5. Tool Call: 1 call
  Total: 6-8 network calls

With Phase 3 (Cached):
  Same as without Phase 3 (schema in cache)
  Total: 4-5 network calls
```

## 🎓 Best Practices

### For Tool Developers

#### 1. Always Write Clear Descriptions (Phase 1)

✅ **Do:**
```go
Description: "Searches product catalog by keyword. " +
             "Required: query (search term). " +
             "Optional: category (electronics/books/clothing), max_results (default: 10)."
```

❌ **Don't:**
```go
Description: "Product search API"  // Too vague!
```

#### 2. Add Field Hints for Production Tools (Phase 2)

✅ **Do:**
```go
InputSummary: &core.SchemaSummary{
    RequiredFields: []core.FieldHint{
        {
            Name:        "query",
            Type:        "string",
            Example:     "laptop",
            Description: "Search term for products",
        },
    },
    // ...
}
```

❌ **Don't:**
```go
// Skipping Phase 2 for production tools with 3+ fields
```

#### 3. Use Consistent Field Types

✅ **Do:**
```go
{Name: "max_results", Type: "number", Example: "10"}
{Name: "include_details", Type: "boolean", Example: "true"}
{Name: "filters", Type: "object", Example: "{}"}
```

❌ **Don't:**
```go
{Name: "max_results", Type: "string", Example: "ten"}  // Should be number!
```

#### 4. Provide Helpful Examples

✅ **Do:**
```go
{
    Name:    "date_range",
    Type:    "string",
    Example: "2024-01-01:2024-01-31",  // Shows format!
}
```

❌ **Don't:**
```go
{
    Name:    "date_range",
    Type:    "string",
    Example: "...",  // Not helpful!
}
```

### For Agent Developers

#### 1. Enable Phase 3 for High-Reliability Systems

```go
// Development: Always enable for early error detection
export TRUVAG3_VALIDATE_PAYLOADS=true

// Production: Enable for critical paths
if isCriticalOperation {
    os.Setenv("TRUVAG3_VALIDATE_PAYLOADS", "true")
}
```

#### 2. Monitor Schema Cache Performance

```go
// Log cache stats periodically
go func() {
    ticker := time.NewTicker(5 * time.Minute)
    for range ticker.C {
        if agent.SchemaCache != nil {
            stats := agent.SchemaCache.Stats()
            agent.Logger.Info("Schema cache stats", stats)

            // Alert on low hit rate
            if hitRate, ok := stats["hit_rate"].(float64); ok && hitRate < 0.8 {
                agent.Logger.Warn("Low schema cache hit rate", map[string]interface{}{
                    "hit_rate": hitRate,
                })
            }
        }
    }
}()
```

#### 3. Handle Validation Failures Gracefully

```go
// Don't fail hard on validation errors
if err := validatePayload(payload, schema); err != nil {
    agent.Logger.Error("Validation failed", map[string]interface{}{
        "payload": payload,
        "error":   err.Error(),
    })

    // Option 1: Retry with AI (maybe AI made a mistake)
    payload, err = retryPayloadGeneration(ctx, capability)

    // Option 2: Use default values
    payload = applyDefaults(payload, schema)

    // Option 3: Skip this tool, try another
    return tryAlternativeTool(ctx, userRequest)
}
```

#### 4. Set Appropriate Cache TTLs

```go
// Schemas rarely change - use long TTL
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(24 * time.Hour),  // Default, good for most cases
)

// If schemas change frequently (development)
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(1 * time.Hour),  // Shorter TTL
)

// If schemas never change (stable APIs)
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(7 * 24 * time.Hour),  // Week-long TTL
)
```

### General Best Practices

#### 1. Start Simple, Add Complexity

```
Week 1: Phase 1 (descriptions) - Get it working
Week 2: Phase 2 (field hints) - Improve accuracy
Week 3: Phase 3 (validation) - Add for critical paths
```

#### 2. Use Phase 3 Selectively

```go
// Enable validation only for critical operations
func (a *Agent) callTool(ctx context.Context, tool *core.ServiceInfo, capability core.Capability, userRequest string) error {
    payload, err := a.generatePayload(ctx, userRequest, capability)

    // Validate only high-stakes operations
    if isHighStakes(capability.Name) {
        schema, _ := a.fetchSchema(ctx, tool, capability)
        if err := a.validatePayload(payload, schema); err != nil {
            return err
        }
    }

    return a.sendToTool(ctx, tool, capability, payload)
}
```

#### 3. Document Your Capabilities

```go
// Add comments explaining your tool's behavior
tool.RegisterCapability(core.Capability{
    Name: "complex_analysis",

    // Phase 1: Be explicit about format requirements
    Description: "Analyzes financial data. " +
                 "Required: stock_symbol (uppercase ticker), date_range (YYYY-MM-DD:YYYY-MM-DD). " +
                 "Optional: indicators (array of: RSI/MACD/SMA).",

    // Phase 2: Show exact format in examples
    InputSummary: &core.SchemaSummary{
        RequiredFields: []core.FieldHint{
            {
                Name:    "date_range",
                Type:    "string",
                Example: "2024-01-01:2024-01-31",  // Format is clear!
            },
        },
    },
})
```

## 🔧 Common Patterns and Solutions

### Pattern 1: Complex Nested Objects

For tools with nested object inputs:

```go
// Phase 2 hints can describe objects
InputSummary: &core.SchemaSummary{
    RequiredFields: []core.FieldHint{
        {
            Name:        "filter",
            Type:        "object",
            Example:     `{"category": "electronics", "price_range": {"min": 100, "max": 500}}`,
            Description: "Filter criteria with category and optional price_range",
        },
    },
}
```

**AI will generate:**
```json
{
  "filter": {
    "category": "electronics",
    "price_range": {"min": 100, "max": 500}
  }
}
```

### Pattern 2: Array Inputs

For tools accepting arrays:

```go
InputSummary: &core.SchemaSummary{
    RequiredFields: []core.FieldHint{
        {
            Name:        "stock_symbols",
            Type:        "array",
            Example:     `["AAPL", "GOOGL", "MSFT"]`,
            Description: "List of stock ticker symbols (uppercase)",
        },
    },
}
```

### Pattern 3: Enum Values

For fields with restricted values:

```go
InputSummary: &core.SchemaSummary{
    OptionalFields: []core.FieldHint{
        {
            Name:        "sort_order",
            Type:        "string",
            Example:     "asc",
            Description: "Sort order: 'asc' or 'desc'",  // AI learns valid values
        },
    },
}

// Phase 3 schema enforces it:
schema := map[string]interface{}{
    "properties": map[string]interface{}{
        "sort_order": map[string]interface{}{
            "type": "string",
            "enum": []string{"asc", "desc"},
        },
    },
}
```

### Pattern 4: Multi-Tenant Schema Caching

For multi-tenant deployments:

```go
// Separate cache per tenant
tenantID := getTenantID(ctx)
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithPrefix(fmt.Sprintf("tenant-%s:schema:", tenantID)),
)
```

### Pattern 5: Conditional Validation

Enable validation based on environment:

```go
func (a *Agent) callTool(ctx context.Context, tool *core.ServiceInfo, capability core.Capability, userRequest string) error {
    payload, err := a.generatePayload(ctx, userRequest, capability)

    // Validate in dev/staging, skip in prod for performance
    if os.Getenv("ENV") != "production" {
        os.Setenv("TRUVAG3_VALIDATE_PAYLOADS", "true")
    }

    // ... rest of logic
}
```

## 🐛 Troubleshooting

### Issue 1: AI Generates Wrong Field Names

**Symptom:**
```json
// Tool expects: {"location": "London"}
// AI generates: {"city": "London"}
```

**Solution:** Add Phase 2 field hints:
```go
InputSummary: &core.SchemaSummary{
    RequiredFields: []core.FieldHint{
        {Name: "location", Type: "string", Example: "London"},
    },
}
```

### Issue 2: Schema Cache Always Misses

**Symptom:**
```
Schema cache miss count increasing, hit rate < 20%
```

**Possible Causes:**
1. **Redis connection issues** - Check Redis connectivity
2. **Different tool/capability names** - Ensure consistent naming
3. **Cache TTL too short** - Increase TTL

**Solution:**
```go
// Check Redis connection
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()
if err := redisClient.Ping(ctx).Err(); err != nil {
    log.Printf("Redis connection failed: %v", err)
}

// Increase TTL
agent.SchemaCache = core.NewSchemaCache(redisClient,
    core.WithTTL(24 * time.Hour),
)

// Check cache stats
stats := agent.SchemaCache.Stats()
log.Printf("Cache stats: %+v", stats)
```

### Issue 3: Validation Fails for Valid Payloads

**Symptom:**
```
Payload validation failed: unexpected field: temperature_unit
```

**Possible Causes:**
1. **Schema too strict** - Doesn't allow valid fields
2. **Field name mismatch** - AI uses different name than schema expects

**Solution:**
```go
// Option 1: Fix schema to allow field
schema["additionalProperties"] = true

// Option 2: Add field to Phase 2 hints
InputSummary: &core.SchemaSummary{
    OptionalFields: []core.FieldHint{
        {Name: "temperature_unit", Type: "string", Example: "celsius"},
    },
}
```

### Issue 4: AI Response Contains Markdown

**Symptom:**
```
Failed to parse AI payload: invalid character '`'
Raw content: ```json\n{"location": "London"}\n```
```

**Solution:** Strip markdown code blocks:
```go
content := strings.TrimSpace(response.Content)

// Strip ```json ... ``` or ``` ... ```
if strings.HasPrefix(content, "```") {
    lines := strings.Split(content, "\n")
    if len(lines) >= 3 {
        startIdx := 1
        endIdx := len(lines) - 1
        for i := len(lines) - 1; i >= startIdx; i-- {
            if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
                endIdx = i
                break
            }
        }
        if endIdx > startIdx {
            content = strings.Join(lines[startIdx:endIdx], "\n")
        }
    }
}

var payload map[string]interface{}
json.Unmarshal([]byte(content), &payload)
```

### Issue 5: Schema Fetch Timeout

**Symptom:**
```
Schema fetch failed: context deadline exceeded
```

**Possible Causes:**
1. **Tool not responding** - Tool down or slow
2. **Network issues** - K8s DNS or connectivity problems
3. **Timeout too short** - Tool takes longer than expected

**Solution:**
```go
// Increase timeout
httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)  // Was 5s
defer cancel()

// Add retry logic
var schema map[string]interface{}
err := retry.Do(
    func() error {
        return fetchSchema(ctx, tool, capability)
    },
    retry.Attempts(3),
    retry.Delay(1*time.Second),
)

// Fall back to proceeding without validation
if err != nil {
    agent.Logger.Warn("Schema fetch failed after retries, proceeding without validation")
    // Continue without Phase 3
}
```

## 📝 Summary

The 3-phase approach to AI-powered payload generation in TruvaG3 provides a **progressive enhancement** strategy that balances accuracy, performance, and complexity.

### Quick Reference

| Phase | What It Does | When to Use | Accuracy | Overhead |
|-------|--------------|-------------|----------|----------|
| **Phase 1** | Descriptions guide AI | Always (required) | ~85-90% | 0ms |
| **Phase 2** | Field hints guide AI | Production (recommended) | ~95% | 0ms |
| **Phase 3** | Schema validates payloads | High-reliability (optional) | ~99% | ~5ms first call, 0ms after |

### The Progressive Path

```
Start Here → Phase 1 (Descriptions)
  ↓
Add accuracy → Phase 2 (Field Hints)
  ↓
Add validation → Phase 3 (Schema Validation)
```

### Key Takeaways

1. **Phase 1 is required** - Every tool needs a clear description
2. **Phase 2 is recommended** - 95% accuracy for production tools
3. **Phase 3 is optional** - Add validation only where critical
4. **They stack together** - Each phase enhances the previous
5. **AI is the bottleneck** - Schema operations add negligible latency (~5ms)
6. **Cache is shared** - All agent replicas share Redis schema cache
7. **Graceful degradation** - System works even without Redis or validation

### Real-World Usage

```
Prototypes: Phase 1 only
  ↓
Production (most): Phase 1 + 2
  ↓
Mission-critical: Phase 1 + 2 + 3
```

### Next Steps

1. **For Tool Developers:**
   - Review your existing tools and add Phase 2 field hints
   - See `examples/tool-example/` for complete implementation

2. **For Agent Developers:**
   - Implement Phase 1 + 2 payload generation
   - Optionally enable Phase 3 validation for critical paths
   - See `examples/agent-example/` for complete implementation

3. **Learn More:**
   - [Core Module README](https://github.com/truvaagents/truva-g3/blob/main/core/README.md) - Framework fundamentals
   - [AI Module README](https://github.com/truvaagents/truva-g3/blob/main/ai/README.md) - AI client integration
   - [Examples](https://github.com/truvaagents/truva-g3/tree/main/examples) - Working code samples

### Questions?

- Check the [troubleshooting section](#-troubleshooting) above
- Review the [examples](https://github.com/truvaagents/truva-g3/tree/main/examples) for working code
- Open an issue on GitHub for additional help

Happy building! 🚀
