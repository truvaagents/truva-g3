# Workflow Execution Guide

This guide explains how the Travel Research Agent orchestrates multiple tools using predefined DAG-based workflows. It walks through the complete execution flow with code snippets and actual log output.

## Table of Contents

1. [Overview](#overview)
2. [Request Payload](#request-payload)
3. [Workflow Definition](#workflow-definition)
4. [Template Interpolation](#template-interpolation)
5. [Execution Flow](#execution-flow)
6. [Complete Example](#complete-example)
7. [AI Response Synthesis](#ai-response-synthesis)

---

## Overview

The Travel Research Agent demonstrates **predefined workflow orchestration** - a DAG (Directed Acyclic Graph) based approach where:

- Workflows are defined in code with explicit steps and dependencies
- Steps without dependencies execute in parallel
- Steps with dependencies wait for their dependencies to complete
- Results from one step can be used as inputs to dependent steps via template interpolation

### Two Orchestration Modes

| Mode | Endpoint | Description |
|------|----------|-------------|
| **Predefined Workflow** | `/orchestrate/travel-research` | Uses hardcoded workflow definitions |
| **Natural Language (AI)** | `/orchestrate/natural` | AI dynamically generates execution plan |

This guide focuses on the **predefined workflow** mode.

> 📖 **For AI-generated execution plans**: See [LLM-Generated Execution Plan Structure](../../orchestration/README.md#llm-execution-plan) in the orchestration module documentation for the JSON plan format, DAG visualization, and Jaeger tracing details.

---

## Request Payload

The request payload contains the parameters needed to execute the workflow. These parameters are used to fill in templates defined in the workflow steps.

### Request Format

```http
POST /orchestrate/travel-research
Content-Type: application/json

{
  "destination": "Tokyo, Japan",
  "country": "Japan",
  "base_currency": "USD",
  "amount": 1000,
  "ai_synthesis": false
}
```

### Parameter Descriptions

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `destination` | string | Yes | City/location to research (e.g., "Tokyo, Japan") |
| `country` | string | Yes | Country name for country info lookup |
| `base_currency` | string | Yes | Source currency for conversion (e.g., "USD") |
| `amount` | number | Yes | Amount to convert for currency rates |
| `ai_synthesis` | boolean | No | If `true`, AI synthesizes a human-readable response. If `false` (default), returns raw JSON data. |

### How Parameters Flow Through the Workflow

```
REQUEST                          WORKFLOW STEP                    RESOLVED VALUE
─────────────────────────────────────────────────────────────────────────────────
destination: "Tokyo, Japan"  →  {{destination}}              →  "Tokyo, Japan"
country: "Japan"             →  {{country}}                  →  "Japan"
base_currency: "USD"         →  {{base_currency}}            →  "USD"
amount: 1000                 →  {{amount}}                   →  1000 (number preserved)
```

### The `ai_synthesis` Flag

This flag controls **response synthesis only** - it does NOT affect workflow generation:

| ai_synthesis | Workflow Steps | Response Format |
|--------|----------------|-----------------|
| `false` | Predefined (same) | Raw JSON from all tools concatenated |
| `true` | Predefined (same) | AI-synthesized human-readable summary |

**Important:** Whether `ai_synthesis` is `true` or `false`, the **same workflow steps execute**. The only difference is how the final response is formatted.

---

## Workflow Definition

Workflows are defined in `research_agent.go`. Here's the travel-research workflow:

```go
// From research_agent.go:241-302
t.workflows["travel-research"] = &TravelWorkflow{
    Name:        "travel-research",
    Description: "Comprehensive travel research including weather, currency, country info, and local news",
    Steps: []WorkflowStep{
        {
            ID:          "geocode",
            ToolName:    "geocoding-tool",
            Capability:  "geocode_location",
            Description: "Get coordinates for the destination",
            Parameters: map[string]interface{}{
                "location": "{{destination}}",  // Template: uses request parameter
            },
        },
        {
            ID:          "weather",
            ToolName:    "weather-tool-v2",
            Capability:  "get_current_weather",
            Description: "Get current weather at destination",
            DependsOn:   []string{"geocode"},  // Waits for geocode to complete
            Parameters: map[string]interface{}{
                "lat": "{{geocode.data.lat}}",  // Template: uses geocode step output
                "lon": "{{geocode.data.lon}}",
            },
        },
        {
            ID:          "country-info",
            ToolName:    "country-info-tool",
            Capability:  "get_country_info",
            Description: "Get country information",
            Parameters: map[string]interface{}{
                "country": "{{country}}",  // Template: uses request parameter
            },
        },
        {
            ID:          "currency",
            ToolName:    "currency-tool",
            Capability:  "convert_currency",
            Description: "Get currency exchange rates",
            DependsOn:   []string{"country-info"},  // Waits for country-info
            Parameters: map[string]interface{}{
                "from":   "{{base_currency}}",
                "to":     "{{country-info.data.currency.code}}",  // Uses country-info output
                "amount": "{{amount}}",
            },
        },
        {
            ID:          "news",
            ToolName:    "news-tool",
            Capability:  "search_news",
            Description: "Get relevant news about the destination",
            Parameters: map[string]interface{}{
                "query":       "{{destination}} travel",
                "max_results": 5,
            },
        },
    },
}
```

### Dependency Graph Visualization

```
                    ┌─────────────┐
                    │   REQUEST   │
                    └──────┬──────┘
                           │
           ┌───────────────┼───────────────┐
           │               │               │
           ▼               ▼               ▼
    ┌──────────┐    ┌─────────────┐   ┌────────┐
    │ geocode  │    │ country-info│   │  news  │
    └────┬─────┘    └──────┬──────┘   └────────┘
         │                 │               │
         ▼                 ▼               │
    ┌──────────┐    ┌──────────┐           │
    │ weather  │    │ currency │           │
    └──────────┘    └──────────┘           │
         │                 │               │
         └─────────────────┴───────────────┘
                           │
                           ▼
                    ┌─────────────┐
                    │  RESPONSE   │
                    └─────────────┘
```

**Parallel Execution:**
- `geocode`, `country-info`, and `news` run in parallel (no dependencies)
- `weather` waits for `geocode` (needs lat/lon)
- `currency` waits for `country-info` (needs currency code)

---

## Template Interpolation

The workflow uses two types of template interpolation:

### 1. Request Parameter Templates: `{{paramName}}`

These reference values from the incoming request:

```go
// Template definition
"location": "{{destination}}"

// Request
{"destination": "Tokyo, Japan", "country": "Japan", ...}

// Resolved value
"location": "Tokyo, Japan"
```

**Implementation** (from `orchestration.go:60-100`):

```go
// substituteRequestParams replaces {{paramName}} templates with request values
func substituteRequestParams(template string, params map[string]interface{}) interface{} {
    // Pattern matches {{paramName}}
    matches := requestParamTemplatePattern.FindAllStringSubmatch(template, -1)

    // If entire string is a single template, preserve the value's type
    // (important for numeric values like "amount")
    if len(matches) == 1 && matches[0][0] == template {
        paramName := matches[0][1]
        if value, ok := params[paramName]; ok {
            return value  // Returns number as number, not string
        }
    }

    // Multiple templates - substitute as strings
    result := template
    for _, match := range matches {
        if value, ok := params[match[1]]; ok {
            result = strings.Replace(result, match[0], fmt.Sprintf("%v", value), 1)
        }
    }
    return result
}
```

### 2. Cross-Step Reference Templates: `{{stepId.field.path}}`

These reference output from a previous step:

```go
// Template definition (currency step)
"to": "{{country-info.data.currency.code}}"

// country-info step output
{"data": {"currency": {"code": "JPY", "name": "Japanese yen"}}, "success": true}

// Resolved value
"to": "JPY"
```

**Implementation** (from `orchestration/executor.go`):

```go
// Pattern matches {{stepId.field.path}} - supports hyphens in step IDs
var stepOutputTemplatePattern = regexp.MustCompile(`\{\{([\w-]+)\.([\w-]+(?:\.[\w-]+)*)\}\}`)

// resolveStepOutputTemplates replaces cross-step references with actual values
func resolveStepOutputTemplates(params map[string]interface{}, stepOutputs map[string]interface{}) {
    for key, value := range params {
        if strVal, ok := value.(string); ok {
            matches := stepOutputTemplatePattern.FindAllStringSubmatch(strVal, -1)
            for _, match := range matches {
                stepID := match[1]      // e.g., "country-info"
                fieldPath := match[2]   // e.g., "data.currency.code"

                if stepOutput, exists := stepOutputs[stepID]; exists {
                    resolvedValue := getNestedValue(stepOutput, fieldPath)
                    // Replace template with resolved value
                }
            }
        }
    }
}
```

---

## Execution Flow

### Step 1: Request Received

```http
POST /orchestrate/travel-research
Content-Type: application/json

{
  "destination": "Tokyo, Japan",
  "country": "Japan",
  "base_currency": "USD",
  "amount": 1000,
  "ai_synthesis": false
}
```

### Step 2: Parameter Extraction

The handler extracts top-level fields as workflow parameters (from `handlers.go:117-138`):

```go
// If Parameters is empty, extract top-level fields as parameters
if req.Parameters == nil || len(req.Parameters) == 0 {
    var rawBody map[string]interface{}
    if err := json.Unmarshal(bodyBytes, &rawBody); err == nil {
        req.Parameters = make(map[string]interface{})
        knownFields := map[string]bool{
            "request": true, "workflow_name": true, "parameters": true,
            "ai_synthesis": true, "metadata": true,
        }
        for key, value := range rawBody {
            if !knownFields[key] {
                req.Parameters[key] = value
            }
        }
    }
}
```

**Result:**
```json
{
  "destination": "Tokyo, Japan",
  "country": "Japan",
  "base_currency": "USD",
  "amount": 1000
}
```

### Step 3: Dependency Resolution

The executor analyzes which steps can run immediately vs. which are blocked:

**Log Output:**
```json
{"message": "Step blocked by dependency", "step_id": "weather", "blocked_by": "geocode", "status": "blocked"}
{"message": "Step blocked by dependency", "step_id": "currency", "blocked_by": "country-info", "status": "blocked"}
{"message": "Step ready for execution", "step_id": "geocode", "status": "ready"}
{"message": "Step ready for execution", "step_id": "country-info", "status": "ready"}
{"message": "Step ready for execution", "step_id": "news", "status": "ready"}
```

### Step 4: Parallel Execution (Round 1)

Steps without dependencies execute in parallel:

**Log Output:**
```json
{"message": "Executing steps in parallel", "parallel_count": 3, "ready_steps": ["geocode", "country-info", "news"]}
```

**HTTP Calls Made:**
```json
{"message": "HTTP request to agent", "url": "http://geocoding-tool-service.../geocode_location",
 "parameters": {"location": "Tokyo, Japan"}}

{"message": "HTTP request to agent", "url": "http://country-info-tool-service.../get_country_info",
 "parameters": {"country": "Japan"}}

{"message": "HTTP request to agent", "url": "http://news-tool-service.../search_news",
 "parameters": {"query": "Tokyo Japan travel", "max_results": 5}}
```

**Responses:**
```json
// geocoding-tool response
{"data": {"lat": 35.6768601, "lon": 139.7638947, "display_name": "東京都, 日本"}, "success": true}

// country-info-tool response
{"data": {"name": "Japan", "currency": {"code": "JPY", "name": "Japanese yen"}, "capital": "Tokyo"}, "success": true}

// news-tool response
{"data": {"total_articles": 229, "articles": [...]}, "success": true}
```

### Step 5: Template Resolution for Dependent Steps

Now that `geocode` and `country-info` completed, their outputs are used to resolve templates:

**weather step:**
```
Before: {"lat": "{{geocode.data.lat}}", "lon": "{{geocode.data.lon}}"}
After:  {"lat": 35.6768601, "lon": 139.7638947}
```

**currency step:**
```
Before: {"from": "USD", "to": "{{country-info.data.currency.code}}", "amount": 1000}
After:  {"from": "USD", "to": "JPY", "amount": 1000}
```

### Step 6: Parallel Execution (Round 2)

Dependent steps now execute:

**HTTP Calls Made:**
```json
{"message": "HTTP request to agent", "url": "http://weather-tool-v2-service.../get_current_weather",
 "parameters": {"lat": 35.6768601, "lon": 139.7638947}}

{"message": "HTTP request to agent", "url": "http://currency-tool-service.../convert_currency",
 "parameters": {"from": "USD", "to": "JPY", "amount": 1000}}
```

**Responses:**
```json
// weather-tool-v2 response
{"data": {"temperature_current": 3.8, "condition": "Clear sky", "humidity": 75}, "success": true}

// currency-tool response
{"data": {"from": "USD", "to": "JPY", "amount": 1000, "rate": 155.225, "result": 155225}, "success": true}
```

### Step 7: Response Aggregation

All step results are combined into the final response:

```json
{
  "request_id": "travel-research-1765052561457985797",
  "workflow_used": "travel-research",
  "execution_time": "1.365090626s",
  "step_results": [
    {"step_id": "country-info", "tool_name": "country-info-tool", "success": true, "duration": "160.2895ms"},
    {"step_id": "news", "tool_name": "news-tool", "success": true, "duration": "219.004042ms"},
    {"step_id": "geocode", "tool_name": "geocoding-tool", "success": true, "duration": "754.941709ms"},
    {"step_id": "currency", "tool_name": "currency-tool", "success": true, "duration": "82.262375ms"},
    {"step_id": "weather", "tool_name": "weather-tool-v2", "success": true, "duration": "607.65ms"}
  ],
  "confidence": 1,
  "tools_used": ["country-info-tool", "news-tool", "geocoding-tool", "currency-tool", "weather-tool-v2"]
}
```

---

## Complete Example

### Request

```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{
    "destination": "Tokyo, Japan",
    "country": "Japan",
    "base_currency": "USD",
    "amount": 1000,
    "ai_synthesis": false
  }'
```

### Agent Logs (Chronological)

```
1. REQUEST RECEIVED
{"message": "Processing workflow execution request", "method": "POST", "path": "/orchestrate/travel-research"}

2. PARAMETER EXTRACTION
{"message": "Extracted top-level parameters", "parameters": {"amount":1000,"base_currency":"USD","country":"Japan","destination":"Tokyo, Japan"}}

3. DEPENDENCY RESOLUTION
{"message": "Step blocked by dependency", "step_id": "weather", "blocked_by": "geocode"}
{"message": "Step blocked by dependency", "step_id": "currency", "blocked_by": "country-info"}
{"message": "Step ready for execution", "step_id": "geocode"}
{"message": "Step ready for execution", "step_id": "country-info"}
{"message": "Step ready for execution", "step_id": "news"}

4. PARALLEL EXECUTION (ROUND 1)
{"message": "Executing steps in parallel", "parallel_count": 3, "ready_steps": ["geocode", "country-info", "news"]}
{"message": "Starting step execution", "step_id": "geocode", "agent_name": "geocoding-tool"}
{"message": "Starting step execution", "step_id": "country-info", "agent_name": "country-info-tool"}
{"message": "Starting step execution", "step_id": "news", "agent_name": "news-tool"}

5. TOOL DISCOVERY
{"message": "Agent discovered successfully", "agent_name": "geocoding-tool", "agent_address": "geocoding-tool-service.truvag3-examples.svc.cluster.local"}
{"message": "Agent discovered successfully", "agent_name": "country-info-tool", "agent_address": "country-info-tool-service.truvag3-examples.svc.cluster.local"}
{"message": "Agent discovered successfully", "agent_name": "news-tool", "agent_address": "news-tool-service.truvag3-examples.svc.cluster.local"}

6. HTTP CALLS TO TOOLS
{"message": "HTTP request to agent", "url": "http://geocoding-tool-service.../geocode_location", "parameters": {"location": "Tokyo, Japan"}}
{"message": "HTTP request to agent", "url": "http://country-info-tool-service.../get_country_info", "parameters": {"country": "Japan"}}
{"message": "HTTP request to agent", "url": "http://news-tool-service.../search_news", "parameters": {"query": "Tokyo Japan travel", "max_results": 5}}

7. STEP COMPLETIONS (ROUND 1)
{"message": "Step execution completed", "step_id": "country-info", "success": true, "duration_ms": 48}
{"message": "Step execution completed", "step_id": "news", "success": true, "duration_ms": 219}
{"message": "Step execution completed", "step_id": "geocode", "success": true, "duration_ms": 754}

8. PARALLEL EXECUTION (ROUND 2) - Dependencies now resolved
{"message": "Executing steps in parallel", "parallel_count": 2, "ready_steps": ["weather", "currency"]}
{"message": "HTTP request to agent", "url": "http://weather-tool-v2-service.../get_current_weather", "parameters": {"lat": 35.6768601, "lon": 139.7638947}}
{"message": "HTTP request to agent", "url": "http://currency-tool-service.../convert_currency", "parameters": {"from": "USD", "to": "JPY", "amount": 1000}}

9. STEP COMPLETIONS (ROUND 2)
{"message": "Step execution completed", "step_id": "currency", "success": true, "duration_ms": 82}
{"message": "Step execution completed", "step_id": "weather", "success": true, "duration_ms": 607}

10. WORKFLOW COMPLETE
{"message": "Workflow execution completed", "workflow": "travel-research", "total_steps": 5, "successful_steps": 5, "execution_time_ms": 1365}
```

### Response

The response includes all data from each tool:

```json
{
  "request_id": "travel-research-1765053034785511794",
  "request": "Execute workflow: travel-research",
  "workflow_used": "travel-research",
  "execution_time": "1.437171167s",
  "confidence": 1,
  "tools_used": [
    "country-info-tool",
    "news-tool",
    "geocoding-tool",
    "weather-tool-v2",
    "currency-tool"
  ],
  "step_results": [
    {"step_id": "country-info", "tool_name": "country-info-tool", "success": true, "duration": "191.454792ms"},
    {"step_id": "news", "tool_name": "news-tool", "success": true, "duration": "236.553417ms"},
    {"step_id": "geocode", "tool_name": "geocoding-tool", "success": true, "duration": "789.471834ms"},
    {"step_id": "weather", "tool_name": "weather-tool-v2", "success": true, "duration": "611.073584ms"},
    {"step_id": "currency", "tool_name": "currency-tool", "success": true, "duration": "646.85775ms"}
  ],
  "response": "..." // Contains aggregated tool responses (see below)
}
```

#### Tool Response Details (from `response` field)

**country-info-tool:**
```json
{
  "data": {
    "name": "Japan",
    "official_name": "Japan",
    "capital": "Tokyo",
    "region": "Asia",
    "subregion": "Eastern Asia",
    "population": 123210000,
    "area": 377930,
    "currency": {"code": "JPY", "name": "Japanese yen", "symbol": "¥"},
    "languages": ["Japanese"],
    "timezones": ["UTC+09:00"],
    "flag": "🇯🇵",
    "flag_url": "https://flagcdn.com/w320/jp.png",
    "country_code": "JP"
  },
  "success": true
}
```

**geocoding-tool:**
```json
{
  "data": {
    "lat": 35.6768601,
    "lon": 139.7638947,
    "display_name": "東京都, 日本",
    "country": "日本",
    "country_code": "jp"
  },
  "success": true
}
```

**weather-tool-v2:**
```json
{
  "data": {
    "lat": 35.7,
    "lon": 139.75,
    "timezone": "Asia/Tokyo",
    "temperature_current": 3.7,
    "condition": "Clear sky",
    "humidity": 76,
    "wind_speed": 1.6,
    "weather_code": 0
  },
  "success": true
}
```

**currency-tool:**
```json
{
  "data": {
    "from": "USD",
    "to": "JPY",
    "amount": 1000,
    "rate": 155.225,
    "result": 155225,
    "date": "2025-12-05"
  },
  "success": true
}
```

**news-tool:**
```json
{
  "data": {
    "total_articles": 229,
    "articles": [
      {
        "title": "Japan leads Asia-Pacific tourism rebound as Tokyo sweeps global awards",
        "description": "Japan is the Asia-Pacific's top performer for inbound tourism recovery...",
        "source": "Japan Today",
        "published_at": "2025-11-27T21:00:00Z",
        "url": "https://japantoday.com/..."
      },
      {
        "title": "This Japanese airline is giving free domestic flights to travellers this winter",
        "description": "Japan is offering free domestic flights this winter to UK and European travelers...",
        "source": "The Economic Times",
        "published_at": "2025-11-25T11:52:49Z",
        "url": "https://economictimes.indiatimes.com/..."
      }
      // ... 3 more articles
    ]
  },
  "success": true
}
```

---

## Key Files

| File | Purpose |
|------|---------|
| `research_agent.go` | Workflow definitions and agent initialization |
| `orchestration.go` | Template interpolation and workflow conversion |
| `handlers.go` | HTTP handlers for workflow execution |
| `main.go` | Application entry point |

---

## Testing

```bash
# Deploy the agent and tools
./setup.sh deploy

# Port forward to the agent
kubectl port-forward -n truvag3-examples svc/travel-research-agent-service 8094:80 &

# Execute workflow
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{"destination":"Paris, France","country":"France","base_currency":"USD","amount":500}'

# View agent logs
kubectl logs -n truvag3-examples -l app=travel-research-agent --tail=100

# View tool logs
kubectl logs -n truvag3-examples -l app=geocoding-tool --tail=20
kubectl logs -n truvag3-examples -l app=country-info-tool --tail=20
kubectl logs -n truvag3-examples -l app=currency-tool --tail=20
kubectl logs -n truvag3-examples -l app=weather-tool-v2 --tail=20
kubectl logs -n truvag3-examples -l app=news-tool --tail=20
```

---

## AI Response Synthesis

When `ai_synthesis: true` is set, the agent uses an AI provider (OpenAI, Groq, etc.) to synthesize a human-readable response from the raw tool outputs.

### Comparison: `ai_synthesis: false` vs `ai_synthesis: true`

#### With `ai_synthesis: false` (Raw JSON)

**Request:**
```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{"destination":"Tokyo, Japan","country":"Japan","base_currency":"USD","amount":1000,"ai_synthesis":false}'
```

**Response (`response` field):**
```
country-info-tool: {"data":{"name":"Japan","currency":{"code":"JPY"...}}}
geocoding-tool: {"data":{"lat":35.6768601,"lon":139.7638947...}}
weather-tool-v2: {"data":{"temperature_current":3.7,"condition":"Clear sky"...}}
currency-tool: {"data":{"from":"USD","to":"JPY","rate":155.225,"result":155225...}}
news-tool: {"data":{"total_articles":229,"articles":[...]}}
```

- **Execution time:** ~1.4 seconds
- **Response format:** Raw JSON concatenated from each tool
- **Use case:** When you want to process the data programmatically

#### With `ai_synthesis: true` (AI-Synthesized)

**Request:**
```bash
curl -X POST http://localhost:8094/orchestrate/travel-research \
  -H "Content-Type: application/json" \
  -d '{"destination":"Tokyo, Japan","country":"Japan","base_currency":"USD","amount":1000,"ai_synthesis":true}'
```

**Response (`response` field):**
```markdown
Here's a helpful summary for your upcoming trip to Japan:

---

### General Information about Japan
- **Country:** Japan (日本) 🇯🇵
- **Capital:** Tokyo (東京都)
- **Region:** Eastern Asia
- **Population:** Approx. 123 million
- **Area:** 377,930 sq km
- **Official Language:** Japanese
- **Currency:** Japanese Yen (JPY, ¥)
- **Time Zone:** UTC+09:00

---

### Currency and Exchange Rate
- As of December 5, 2025:
  - 1,000 USD ≈ 155,225 JPY
- It's a good time to exchange currency given the favorable rate.

---

### Weather in Tokyo (Early December 2025)
- **Current Conditions:** Clear sky
- **Temperature:** Around 3.7°C – quite cold, so pack warm clothing.
- **Humidity:** 76%
- **Wind Speed:** Light breeze (1.6 m/s)

---

### Current Travel and Tourism Highlights
- **Tourism Recovery:** Japan is leading the Asia-Pacific region in
  inbound tourism recovery post-pandemic.
- **Sustainable Travel Initiatives:** Japan is offering free domestic
  flights this winter to UK and European travelers.

---

### Useful Tips
- **Explore beyond Tokyo and Kyoto:** Take advantage of the free
  domestic flights if you're traveling from Europe or the UK.
- **Prepare for cold weather:** Pack layers and warm clothing.
- **Currency exchange:** Plan your money exchange ahead.

---

If you want to know more, feel free to ask! Safe travels! 🌏✈️🇯🇵
```

- **Execution time:** ~7.8 seconds (includes AI processing)
- **Response format:** Human-readable Markdown summary
- **Use case:** When you want to display the response directly to users

### How AI Synthesis Works

1. **Workflow Execution:** Same predefined steps execute regardless of `ai_synthesis` value
2. **Data Collection:** All tool responses are gathered
3. **AI Prompt:** The raw data is sent to the AI provider with a synthesis prompt
4. **Response Generation:** AI creates a coherent, human-friendly summary

```
┌─────────────────────────────────────────────────────────────┐
│                    WORKFLOW EXECUTION                        │
│                                                              │
│  ┌──────────┐ ┌───────────────┐ ┌──────────┐ ┌──────────┐   │
│  │ geocode  │ │ country-info  │ │ weather  │ │ currency │   │
│  └────┬─────┘ └──────┬────────┘ └────┬─────┘ └────┬─────┘   │
│       │              │               │            │          │
│       └──────────────┴───────────────┴────────────┘          │
│                          │                                   │
│                          ▼                                   │
│              ┌───────────────────────┐                       │
│              │   Raw Tool Outputs    │                       │
│              └───────────┬───────────┘                       │
│                          │                                   │
│         ┌────────────────┴────────────────┐                  │
│         │                                 │                  │
│    ai_synthesis: false                    ai_synthesis: true             │
│         │                                 │                  │
│         ▼                                 ▼                  │
│  ┌─────────────────┐            ┌──────────────────┐        │
│  │   Return Raw    │            │  Send to AI for  │        │
│  │   JSON Concat   │            │    Synthesis     │        │
│  └─────────────────┘            └────────┬─────────┘        │
│                                          │                   │
│                                          ▼                   │
│                                 ┌──────────────────┐        │
│                                 │  Human-Readable  │        │
│                                 │    Response      │        │
│                                 └──────────────────┘        │
└─────────────────────────────────────────────────────────────┘
```

### Configuration

The AI provider is configured via environment variables:

```yaml
# In k8-deployment.yaml
env:
  - name: OPENAI_API_KEY
    valueFrom:
      secretKeyRef:
        name: openai-api-key
        key: api-key
  - name: AI_PROVIDER
    value: "openai"  # or "groq", "anthropic", etc.
```

Without a valid AI provider configuration, `ai_synthesis: true` will fall back to returning raw JSON.
