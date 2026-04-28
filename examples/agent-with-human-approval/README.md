# Agent with Human Approval (HITL)

A demonstration agent showcasing **Human-in-the-Loop (HITL)** capabilities for AI orchestration. This agent requires human approval before executing any planned actions, providing oversight for AI-driven workflows.

## Overview

This agent demonstrates:
- **Plan Approval**: All execution plans require human approval before proceeding
- **Checkpoint System**: Execution state is saved and can be resumed after approval
- **SSE Streaming**: Real-time streaming with checkpoint events for UI integration
- **Redis-backed State**: Checkpoints and sessions stored in Redis for durability

## Key Features

### HITL Always Enabled
Unlike other agents where HITL is optional, this agent **always requires human approval**. When you send a request:

1. The orchestrator generates an execution plan
2. The plan is sent to the client as a `checkpoint` SSE event
3. Execution pauses until the user approves or rejects
4. After approval, execution resumes via the `/hitl/resume/{id}` endpoint

### Sensitive Capabilities
The following capabilities are marked as sensitive and always require approval:
- `stock_quote`, `company_profile`, `company_news`, `market_news`
- `currency_convert`, `get_weather`

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Agent with Human Approval                     │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │ SSE Handler │  │  HITL APIs  │  │   Session Management    │  │
│  │  /chat/     │  │  /hitl/*    │  │   (Redis-backed)        │  │
│  │  stream     │  │             │  │                         │  │
│  └──────┬──────┘  └──────┬──────┘  └────────────┬────────────┘  │
│         │                │                      │                │
│  ┌──────▼────────────────▼──────────────────────▼──────────────┐│
│  │                   HITLChatAgent                              ││
│  │  - AI Chain Client (OpenAI → Anthropic → Groq)              ││
│  │  - Orchestrator with HITL Controller                         ││
│  │  - Checkpoint Store (Redis DB 6)                             ││
│  └──────────────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

## API Endpoints Reference

### Core Chat Endpoints

#### POST /chat (Non-Streaming)

Send a chat request and receive JSON response. May return a checkpoint if HITL approval is required.

**Request:**
```bash
curl -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{
    "request": "What is the stock price of AAPL?",
    "session_id": "optional-session-id",
    "use_ai": true,
    "metadata": {"user_id": "123"}
  }'
```

**Response (HITL Interrupted):**
```json
{
  "request_id": "1768796753776585169-76585211",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-abc12345",
    "interrupt_point": "plan_generated",
    "decision": {
      "should_interrupt": true,
      "reason": "plan_review_required",
      "message": "Plan requires approval before execution"
    },
    "plan": {
      "plan_id": "stock-query-001",
      "steps": [...]
    },
    "status": "pending",
    "expires_at": "2026-01-19T04:24:53Z"
  },
  "duration_ms": 1503
}
```

**Response (Completed - no HITL):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "response": "The current stock price of AAPL is $198.50.",
  "tools_used": ["stock-service"],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 2345
}
```

#### POST /chat/stream (SSE Streaming)

Send a chat request and receive Server-Sent Events stream.

**Request:**
```bash
curl -X POST http://localhost:8352/chat/stream \
  -H "Content-Type: application/json" \
  -H "Accept: text/event-stream" \
  -d '{"message": "What is the weather in Tokyo?", "session_id": "my-session"}'
```

**SSE Events:**
| Event | Description | Example Data |
|-------|-------------|--------------|
| `session` | New session created | `{"id": "sess-123"}` |
| `status` | Processing status | `{"step": "planning", "message": "Generating plan..."}` |
| `checkpoint` | HITL approval needed | `{"checkpoint_id": "cp-xxx", "plan": {...}}` |
| `step` | Tool executed | `{"step_id": "step-1", "tool": "weather-service", "success": true}` |
| `chunk` | Response text | `{"text": "The weather in Tokyo..."}` |
| `usage` | Token usage | `{"prompt_tokens": 150, "completion_tokens": 50}` |
| `done` | Request completed | `{"request_id": "xxx", "tools_used": [...]}` |
| `error` | Error occurred | `{"code": "timeout", "message": "...", "retryable": true}` |

---

### HITL Management Endpoints

#### POST /hitl/command

Submit an approval, rejection, or other decision for a checkpoint.

**Request (Approve):**
```bash
curl -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-abc12345",
    "type": "approve"
  }'
```

**Request (Reject with Feedback):**
```bash
curl -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-abc12345",
    "type": "reject",
    "feedback": "This plan looks too expensive, please find cheaper options"
  }'
```

**Response (Approved):**
```json
{
  "checkpoint_id": "cp-abc12345",
  "action": "approve",
  "should_resume": true
}
```

**Response (Rejected):**
```json
{
  "checkpoint_id": "cp-abc12345",
  "action": "reject",
  "should_resume": false
}
```

**Command Types:**
| Type | Description | Requires Resume |
|------|-------------|-----------------|
| `approve` | Proceed with the plan/step as-is | Yes |
| `reject` | Stop execution, optionally provide feedback | No |
| `edit` | Proceed with modifications (provide `edited_plan`) | Yes |
| `skip` | Skip current step, continue with next | Yes |
| `abort` | Stop entire workflow immediately | No |
| `retry` | Retry with new parameters | Yes |

**Optional Fields:**
| Field | Type | Description |
|-------|------|-------------|
| `feedback` | string | Reason for rejection (used with `reject`) |
| `user_id` | string | Audit trail identifier |
| `edited_plan` | object | Modified plan (used with `edit`) |
| `new_parameters` | object | New parameters (used with `retry`) |

---

#### GET /hitl/checkpoints

List all pending checkpoints awaiting approval.

**Request:**
```bash
curl -s http://localhost:8352/hitl/checkpoints | python3 -m json.tool
```

**Response:**
```json
{
  "checkpoints": [
    {
      "checkpoint_id": "cp-d6c2b787-292c-4f",
      "request_id": "",
      "interrupt_point": "before_step",
      "decision": {
        "should_interrupt": true,
        "reason": "sensitive_operation",
        "message": "Step approval required for operation: stock-service.stock_quote",
        "priority": "high",
        "metadata": {
          "agent_name": "stock-service",
          "capability": "stock_quote",
          "step_id": "step-1",
          "trigger": "step_sensitive_capability"
        }
      },
      "current_step": {
        "step_id": "step-1",
        "agent_name": "stock-service",
        "instruction": "Get TSLA stock quote"
      },
      "resolved_parameters": {
        "symbol": "TSLA"
      },
      "completed_steps": [
        {
          "step_id": "step-2",
          "agent_name": "weather-service",
          "success": true
        }
      ],
      "status": "pending",
      "expires_at": "2026-01-19T04:24:06Z"
    }
  ],
  "count": 1,
  "limit": 50,
  "offset": 0
}
```

**Query Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `status` | string | `pending` | Filter by status: `pending`, `approved`, `rejected`, `expired`, `completed` |
| `limit` | int | 50 | Maximum results to return |
| `offset` | int | 0 | Pagination offset |

---

#### GET /hitl/checkpoints/{id}

Get detailed information for a specific checkpoint.

**Request:**
```bash
curl -s http://localhost:8352/hitl/checkpoints/cp-d6c2b787-292c-4f | python3 -m json.tool
```

**Response:**
```json
{
  "checkpoint_id": "cp-d6c2b787-292c-4f",
  "request_id": "",
  "interrupt_point": "before_step",
  "decision": {
    "should_interrupt": true,
    "reason": "sensitive_operation",
    "message": "Step approval required for operation: stock-service.stock_quote",
    "priority": "high",
    "timeout": 300000000000,
    "default_action": "approve",
    "metadata": {
      "agent_name": "stock-service",
      "capability": "stock_quote",
      "step_id": "step-1",
      "trigger": "step_sensitive_capability"
    }
  },
  "plan": {
    "plan_id": "trip-to-sydney-001",
    "original_request": "I want to sell 100 TESLA shares...",
    "mode": "autonomous",
    "steps": [
      {
        "step_id": "step-1",
        "agent_name": "stock-service",
        "namespace": "default",
        "instruction": "Get TSLA stock quote",
        "metadata": {
          "capability": "stock_quote",
          "parameters": {"symbol": "TSLA"}
        }
      },
      {
        "step_id": "step-2",
        "agent_name": "weather-service",
        "instruction": "Get Sydney weather"
      },
      {
        "step_id": "step-3",
        "agent_name": "currency-tool",
        "instruction": "Convert USD to AUD",
        "depends_on": ["step-1"]
      }
    ]
  },
  "current_step": {
    "step_id": "step-1",
    "agent_name": "stock-service",
    "instruction": "Get TSLA stock quote"
  },
  "resolved_parameters": {
    "symbol": "TSLA"
  },
  "completed_steps": [
    {
      "step_id": "step-2",
      "agent_name": "weather-service",
      "success": true,
      "response": "{\"temperature\": 21, \"condition\": \"partly cloudy\"}"
    }
  ],
  "step_results": {
    "step-2": {
      "step_id": "step-2",
      "success": true,
      "response": "{\"temperature\": 21}",
      "duration": 1220833
    }
  },
  "original_request": "I want to sell 100 TESLA shares...",
  "user_context": {
    "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
    "original_trace_id": "e1ae17144851ab88380553de12218de8"
  },
  "created_at": "2026-01-19T04:19:06Z",
  "expires_at": "2026-01-19T04:24:06Z",
  "status": "pending"
}
```

---

#### POST /hitl/resume-sync/{id}

Resume execution after approval. Returns JSON response.

**Request:**
```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-abc12345 \
  -H "Content-Type: application/json" \
  --max-time 120 | python3 -m json.tool
```

**Response (Completed):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "response": "The current stock price of AAPL is $198.50...",
  "tools_used": ["stock-service"],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 2345
}
```

**Response (Another Checkpoint - Chained Approval):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-step-xyz789",
    "interrupt_point": "before_step",
    "current_step": {...}
  },
  "duration_ms": 500
}
```

---

#### POST /hitl/resume/{id}

Resume execution after approval. Returns SSE stream.

**Request:**
```bash
curl -X POST http://localhost:8352/hitl/resume/cp-abc12345 \
  -H "Accept: text/event-stream"
```

**SSE Events:** Same as `/chat/stream` (status, step, chunk, checkpoint, done, error)

---

### Utility Endpoints

#### GET /health

Check service health and HITL status.

**Request:**
```bash
curl -s http://localhost:8352/health | python3 -m json.tool
```

**Response:**
```json
{
  "service": "agent-with-human-approval",
  "status": "healthy",
  "timestamp": 1768796949,
  "ai_provider": "connected",
  "redis": "healthy",
  "hitl": {
    "enabled": true,
    "status": "active"
  },
  "orchestrator": {
    "status": "active",
    "total_requests": 4,
    "successful_requests": 4,
    "failed_requests": 0,
    "average_latency_ms": 5000
  },
  "active_sessions": 0
}
```

#### GET /discover

List available tools and their capabilities.

**Request:**
```bash
curl -s http://localhost:8352/discover | python3 -m json.tool
```

## Quick Start

### Prerequisites
- Go 1.25+
- Redis server running
- At least one AI provider API key (OpenAI, Anthropic, or Groq)
- Tool services running (weather, currency, etc.)

### Local Development

1. **Copy environment configuration:**
   ```bash
   cp .env.example .env
   # Edit .env with your API keys
   ```

2. **Start Redis:**
   ```bash
   docker run -d -p 6379:6379 redis:alpine
   ```

3. **Start tool services** (in separate terminals or use k8-deployment):
   ```bash
   # Start weather tool, currency tool, etc.
   ```

4. **Run the agent:**
   ```bash
   go run .
   ```

5. **Open the HITL UI:**
   Open `examples/chat-ui/hitl.html` in a browser, or:
   ```bash
   # From truvag3 root
   open examples/chat-ui/hitl.html
   ```

### Docker Build

```bash
# Build the image
docker build -t agent-with-human-approval:latest .

# Run with environment variables
docker run -p 8098:8098 \
  -e REDIS_URL=redis://host.docker.internal:6379 \
  -e OPENAI_API_KEY=sk-your-key \
  agent-with-human-approval:latest
```

## Testing Guide (Step-by-Step)

This guide walks through testing HITL flows using curl commands. There are two approval modes that can be combined:

| Mode | Trigger | What Gets Approved |
|------|---------|-------------------|
| **Plan Approval** | `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true` | Entire execution plan before any tools run |
| **Step Approval** | `TRUVAG3_HITL_SENSITIVE_CAPABILITIES=...` | Individual tool calls for sensitive operations |

When both are enabled, you get **chained approvals**: Plan first, then each sensitive step.

---

### Prerequisites

Ensure the agent is running and port forwarding is active:
```bash
./setup.sh rebuild      # Build and deploy
./setup.sh forward-all  # Start port forwarding
```

Verify health and HITL status:
```bash
curl -s http://localhost:8352/health | python3 -m json.tool
```

**Expected Output:**
```json
{
  "service": "agent-with-human-approval",
  "status": "healthy",
  "hitl": {
    "enabled": true,
    "status": "active"
  },
  "orchestrator": {
    "status": "active"
  }
}
```

---

## Test 1: Plan-Only Approval

This tests the simpler flow where only the plan needs approval.

**Configuration:** Set `TRUVAG3_HITL_SENSITIVE_CAPABILITIES=""` (empty) to disable step approvals.

### Step 1: Send a Request

```bash
curl -s -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{
    "request": "What is 2+2?",
    "use_ai": true
  }' | python3 -m json.tool
```

**Response (Plan Checkpoint):**
```json
{
  "request_id": "1768796753776585169-76585211",
  "session_id": "f5e4a8b2-3c1d-4e5f-a6b7-c8d9e0f1a2b3",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-a34f968a-4e15-46",
    "interrupt_point": "plan_generated",
    "decision": {
      "should_interrupt": true,
      "reason": "plan_review_required",
      "message": "Plan requires approval before execution",
      "priority": "medium",
      "metadata": {
        "trigger": "require_plan_approval"
      }
    },
    "plan": {
      "plan_id": "math-question-001",
      "original_request": "What is 2+2?",
      "steps": []
    },
    "status": "pending"
  },
  "duration_ms": 1503
}
```

**Key Fields:**
- `interrupt_point: "plan_generated"` - Stopped at plan stage
- `decision.reason: "plan_review_required"` - Plan approval trigger
- `status: "pending"` - Awaiting your decision

### Step 2: List Pending Checkpoints

See all checkpoints awaiting approval:

```bash
curl -s http://localhost:8352/hitl/checkpoints | python3 -m json.tool
```

**Response:**
```json
{
  "checkpoints": [
    {
      "checkpoint_id": "cp-a34f968a-4e15-46",
      "interrupt_point": "plan_generated",
      "status": "pending",
      "expires_at": "2026-01-19T04:24:53Z"
    }
  ],
  "count": 1
}
```

### Step 3: Get Checkpoint Details

Get full details for a specific checkpoint:

```bash
curl -s http://localhost:8352/hitl/checkpoints/cp-a34f968a-4e15-46 | python3 -m json.tool
```

### Step 4: Approve the Plan

```bash
curl -s -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-a34f968a-4e15-46",
    "type": "approve"
  }' | python3 -m json.tool
```

**Response:**
```json
{
  "checkpoint_id": "cp-a34f968a-4e15-46",
  "action": "approve",
  "should_resume": true
}
```

### Step 5: Resume Execution

```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-a34f968a-4e15-46 | python3 -m json.tool
```

**Response (Completed):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "f5e4a8b2-3c1d-4e5f-a6b7-c8d9e0f1a2b3",
  "response": "The answer to your question \"What is 2+2?\" is 4.",
  "tools_used": [],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 1234
}
```

---

## Test 2: Chained Approvals (Plan + Step)

This tests the full flow: Plan approval first, then step approvals for sensitive operations.

**Configuration:** Ensure both are set:
```bash
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=true
TRUVAG3_HITL_SENSITIVE_CAPABILITIES=stock_quote,company_profile
```

### Step 1: Send a Complex Request

```bash
curl -s -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{
    "request": "I want to sell 100 TESLA shares to fund my trip to Sydney. How much will I have in local currency? What is the weather like?",
    "use_ai": true
  }' | python3 -m json.tool
```

**Response (Plan Checkpoint):**
```json
{
  "request_id": "1768796753776585169-76585211",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-plan-abc123",
    "interrupt_point": "plan_generated",
    "decision": {
      "reason": "plan_review_required",
      "message": "Plan requires approval before execution"
    },
    "plan": {
      "plan_id": "trip-to-sydney-001",
      "original_request": "I want to sell 100 TESLA shares...",
      "steps": [
        {
          "step_id": "step-1",
          "agent_name": "stock-service",
          "instruction": "Get TSLA stock quote",
          "metadata": { "capability": "stock_quote" }
        },
        {
          "step_id": "step-2",
          "agent_name": "weather-service",
          "instruction": "Get Sydney weather forecast"
        },
        {
          "step_id": "step-3",
          "agent_name": "currency-tool",
          "instruction": "Convert USD to AUD",
          "depends_on": ["step-1"]
        },
        {
          "step_id": "step-4",
          "agent_name": "news-tool",
          "instruction": "Get Sydney news"
        }
      ]
    },
    "status": "pending"
  }
}
```

### Step 2: Approve the Plan

```bash
curl -s -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-plan-abc123",
    "type": "approve"
  }' | python3 -m json.tool
```

### Step 3: Resume (May Trigger Step Checkpoint)

```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-plan-abc123 \
  --max-time 60 | python3 -m json.tool
```

**Response (Step Checkpoint):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-d6c2b787-292c-4f",
    "interrupt_point": "before_step",
    "decision": {
      "reason": "sensitive_operation",
      "message": "Step approval required for operation: stock-service.stock_quote",
      "priority": "high",
      "metadata": {
        "agent_name": "stock-service",
        "capability": "stock_quote",
        "step_id": "step-1",
        "trigger": "step_sensitive_capability"
      }
    },
    "current_step": {
      "step_id": "step-1",
      "agent_name": "stock-service",
      "instruction": "Get TSLA stock quote"
    },
    "resolved_parameters": {
      "symbol": "TSLA"
    },
    "completed_steps": [
      {
        "step_id": "step-2",
        "agent_name": "weather-service",
        "success": true,
        "response": "{\"temperature\": 21, \"condition\": \"partly cloudy\"}"
      },
      {
        "step_id": "step-4",
        "agent_name": "news-tool",
        "success": true
      }
    ],
    "status": "pending"
  }
}
```

**What Happened:**
- Plan was approved ✓
- Non-sensitive steps (weather, news) executed immediately ✓
- Sensitive step (stock-service with `stock_quote` capability) paused for approval
- Currency step is waiting (depends on stock step)

**Key Fields:**
- `interrupt_point: "before_step"` - Stopped before a specific step
- `current_step` - The step awaiting approval
- `resolved_parameters` - Actual values that will be sent to the tool
- `completed_steps` - Steps that already ran

### Step 4: Approve the Step

```bash
curl -s -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-d6c2b787-292c-4f",
    "type": "approve"
  }' | python3 -m json.tool
```

### Step 5: Resume to Completion

```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-d6c2b787-292c-4f \
  --max-time 120 | python3 -m json.tool
```

**Response (Completed):**
```json
{
  "request_id": "1768796991076402758-76402800",
  "session_id": "759ca0dd-1133-4ec0-88d0-438c1d490c87",
  "response": "Selling 100 TESLA shares at $437.50 would yield $43,750 USD. At the current exchange rate of 1.493, this converts to approximately 65,300 AUD. The weather in Sydney is partly cloudy at 21°C - a good time to visit!",
  "tools_used": ["stock-service", "weather-service", "currency-tool", "news-tool"],
  "confidence": 0.95,
  "interrupted": false,
  "duration_ms": 10806
}
```

---

## Chained Approval Flow Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     Chained HITL Approval Flow                               │
└─────────────────────────────────────────────────────────────────────────────┘

POST /chat {"request": "Sell TESLA shares for Sydney trip..."}
                │
                ▼
    ┌───────────────────────────────────────────────┐
    │ Response 1: Plan Checkpoint                    │
    │ {                                              │
    │   "interrupted": true,                         │
    │   "checkpoint": {                              │
    │     "checkpoint_id": "cp-plan-xxx",            │
    │     "interrupt_point": "plan_generated",       │
    │     "plan": { steps: [...] }                   │
    │   }                                            │
    │ }                                              │
    └───────────────────────────────────────────────┘
                │
                ▼
POST /hitl/command {"checkpoint_id": "cp-plan-xxx", "type": "approve"}
                │
                ▼
POST /hitl/resume-sync/cp-plan-xxx
                │
                ▼  (non-sensitive steps execute: weather, news)
    ┌───────────────────────────────────────────────┐
    │ Response 2: Step Checkpoint                    │
    │ {                                              │
    │   "interrupted": true,                         │
    │   "checkpoint": {                              │
    │     "checkpoint_id": "cp-step-xxx",            │
    │     "interrupt_point": "before_step",          │
    │     "current_step": { agent: "stock-service" },│
    │     "resolved_parameters": { symbol: "TSLA" }, │
    │     "completed_steps": [weather, news]         │
    │   }                                            │
    │ }                                              │
    └───────────────────────────────────────────────┘
                │
                ▼
POST /hitl/command {"checkpoint_id": "cp-step-xxx", "type": "approve"}
                │
                ▼
POST /hitl/resume-sync/cp-step-xxx
                │
                ▼  (stock-service executes, then currency-tool)
    ┌───────────────────────────────────────────────┐
    │ Response 3: Final Response                     │
    │ {                                              │
    │   "interrupted": false,                        │
    │   "response": "Selling 100 TESLA shares...",   │
    │   "tools_used": ["stock", "weather", ...]      │
    │ }                                              │
    └───────────────────────────────────────────────┘
```

---

## Quick Test Script

Here's a bash script to test the complete chained approval flow:

```bash
#!/bin/bash
# test-chained-hitl.sh - Test chained HITL approval flow

BASE_URL="http://localhost:8352"

echo "=== Step 1: Send request ==="
RESPONSE=$(curl -s -X POST "$BASE_URL/chat" \
  -H "Content-Type: application/json" \
  -d '{"request": "Get the TSLA stock price and Sydney weather", "use_ai": true}')

echo "$RESPONSE" | python3 -m json.tool

# Extract checkpoint ID
CHECKPOINT_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('checkpoint',{}).get('checkpoint_id',''))")

if [ -z "$CHECKPOINT_ID" ]; then
  echo "No checkpoint - request completed without HITL"
  exit 0
fi

echo -e "\n=== Step 2: Approve checkpoint: $CHECKPOINT_ID ==="
curl -s -X POST "$BASE_URL/hitl/command" \
  -H "Content-Type: application/json" \
  -d "{\"checkpoint_id\": \"$CHECKPOINT_ID\", \"type\": \"approve\"}" | python3 -m json.tool

echo -e "\n=== Step 3: Resume execution ==="
RESPONSE=$(curl -s -X POST "$BASE_URL/hitl/resume-sync/$CHECKPOINT_ID" --max-time 60)
echo "$RESPONSE" | python3 -m json.tool

# Check if another checkpoint was returned (chained approval)
NEW_CHECKPOINT=$(echo "$RESPONSE" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('checkpoint',{}).get('checkpoint_id',''))" 2>/dev/null)

if [ -n "$NEW_CHECKPOINT" ]; then
  echo -e "\n=== Step 4: Approve step checkpoint: $NEW_CHECKPOINT ==="
  curl -s -X POST "$BASE_URL/hitl/command" \
    -H "Content-Type: application/json" \
    -d "{\"checkpoint_id\": \"$NEW_CHECKPOINT\", \"type\": \"approve\"}" | python3 -m json.tool

  echo -e "\n=== Step 5: Resume to completion ==="
  curl -s -X POST "$BASE_URL/hitl/resume-sync/$NEW_CHECKPOINT" --max-time 120 | python3 -m json.tool
fi

echo -e "\n=== Done ==="
```

---

## Alternative: Reject the Plan

Instead of approving, you can reject with feedback:

```bash
curl -s -X POST http://localhost:8352/hitl/command \
  -H "Content-Type: application/json" \
  -d '{
    "checkpoint_id": "cp-abc12345",
    "type": "reject",
    "feedback": "I only want the stock price, not the weather"
  }' | python3 -m json.tool
```

**Response:**
```json
{
  "checkpoint_id": "cp-abc12345",
  "action": "reject",
  "should_resume": false
}
```

---

## Test 3: Auto-Approve on Timeout (Non-Streaming)

This tests the automatic approval behavior when a checkpoint expires without human action.

**Configuration:** Set short timeout:
```bash
TRUVAG3_HITL_DEFAULT_TIMEOUT=30s
```

**Note:** The `DefaultAction` is determined by the HITL policy, not an environment variable:
- **Plan checkpoints** (`interrupt_point: plan_generated`): `DefaultAction=approve` (auto-proceeds)
- **Step checkpoints** (`interrupt_point: before_step`): `DefaultAction=reject` (fail-safe for sensitive operations)

### How It Works

In non-streaming mode, when a checkpoint expires:
1. The **Expiry Processor** detects the expired checkpoint
2. It applies the `DefaultAction` from the checkpoint's policy decision
3. For plan checkpoints, `DefaultAction=approve` causes automatic approval
4. The execution can then be resumed

### Step 1: Send a Request

```bash
curl -s -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{"request": "What is the TSLA stock price?", "use_ai": true}' | python3 -m json.tool
```

**Response:**
```json
{
  "interrupted": true,
  "checkpoint": {
    "checkpoint_id": "cp-xyz789",
    "status": "pending",
    "expires_at": "2026-01-19T04:24:53Z",
    "decision": {
      "default_action": "approve",
      "timeout": 30000000000
    }
  }
}
```

### Step 2: Wait for Timeout (or poll status)

Wait for the timeout to expire, then check checkpoint status:

```bash
# Poll checkpoint status
curl -s http://localhost:8352/hitl/checkpoints/cp-xyz789 | python3 -m json.tool
```

**Response (after expiry):**
```json
{
  "checkpoint_id": "cp-xyz789",
  "status": "expired_approved",
  "decision": {
    "default_action": "approve"
  }
}
```

**Key Field:** `status: "expired_approved"` - The checkpoint was auto-approved on timeout.

### Step 3: Resume Execution

```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-xyz789 \
  --max-time 60 | python3 -m json.tool
```

**Response (Completed):**
```json
{
  "response": "The current stock price of TSLA is $437.50.",
  "tools_used": ["stock-service"],
  "interrupted": false
}
```

---

## Test 4: Auto-Reject on Timeout (Non-Streaming)

This tests the automatic rejection behavior when a **step-level** checkpoint expires.

**Configuration:** Set short timeout and enable step-level approval for sensitive capabilities:
```bash
TRUVAG3_HITL_DEFAULT_TIMEOUT=30s
TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL=false
TRUVAG3_HITL_SENSITIVE_CAPABILITIES=stock_quote,company_profile
```

**How It Differs:** With plan approval disabled but step approval enabled, the orchestrator creates a **step checkpoint** (not plan checkpoint) when a sensitive operation is about to run. Step checkpoints have `DefaultAction=reject` as a fail-safe.

### Step 1: Send a Request

```bash
curl -s -X POST http://localhost:8352/chat \
  -H "Content-Type: application/json" \
  -d '{"request": "What is the AAPL stock price?", "use_ai": true}' | python3 -m json.tool
```

### Step 2: Wait for Timeout

After the timeout expires, check checkpoint status:

```bash
curl -s http://localhost:8352/hitl/checkpoints/cp-abc123 | python3 -m json.tool
```

**Response (after expiry):**
```json
{
  "checkpoint_id": "cp-abc123",
  "status": "expired_rejected",
  "decision": {
    "default_action": "reject"
  }
}
```

**Key Field:** `status: "expired_rejected"` - The checkpoint was auto-rejected on timeout.

### Step 3: Resume Attempt (Fails)

```bash
curl -s -X POST http://localhost:8352/hitl/resume-sync/cp-abc123 | python3 -m json.tool
```

**Response (Error - 409 Conflict):**
```json
{
  "error": "Checkpoint has not been approved"
}
```

The resume fails because `expired_rejected` is a terminal status - the checkpoint was auto-rejected on timeout and cannot be resumed.

---

## Expiry Behavior Summary

| Request Mode | Checkpoint Type | Policy DefaultAction | Expiry Behavior | Final Status |
|--------------|-----------------|----------------------|-----------------|--------------|
| `streaming` | Plan or Step | `approve` or `reject` | `implicit_deny` - marked expired, no auto-action | `expired` |
| `non_streaming` | Plan (`plan_generated`) | `approve` | `apply_default` - auto-approves | `expired_approved` |
| `non_streaming` | Step (`before_step`) | `reject` | `apply_default` - auto-rejects | `expired_rejected` |

**Key Points:**
- **Streaming**: Client maintains an open SSE connection and should be notified of expiry to handle it. No automatic action is taken.
- **Non-Streaming**: No open connection, so the system applies the policy's `DefaultAction` autonomously.
- **DefaultAction** is set by the HITL policy based on checkpoint type, not by environment variable.

---

### Monitoring in Jaeger

To trace the full request flow:
1. Open Jaeger UI: http://localhost:16686
2. Search for service: `agent-with-human-approval`
3. Find traces with `trace_id` from the response metadata
4. The trace shows: request → plan generation → HITL pause → approval → resume → tool execution → response

### Viewing Logs

**Important:** When using `kubectl logs` with a label selector (`-l`), the output may be truncated to only ~10 lines. Use the explicit pod name with `--tail` for complete logs:

```bash
# Get pod name
POD=$(kubectl get pods -n truvag3-examples -l app=agent-with-human-approval -o jsonpath='{.items[0].metadata.name}')

# View recent logs (recommended)
kubectl logs -n truvag3-examples $POD --tail=500

# Stream logs in real-time
kubectl logs -n truvag3-examples $POD -f --tail=100

# Search for a specific request ID
kubectl logs -n truvag3-examples $POD --tail=2000 | grep "your-request-id"
```

**Alternative: Access log files directly** (for complete logs including rotated entries):
```bash
# Get Kind cluster node name
NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')

# Find log file path
docker exec $NODE find /var/log/pods -name "*.log" | grep human-approval

# View logs directly (bypasses kubectl truncation)
docker exec $NODE tail -500 /var/log/pods/truvag3-examples_agent-with-human-approval-*/agent/0.log

# Search for a specific request ID in the log file
docker exec $NODE grep "your-request-id" /var/log/pods/truvag3-examples_agent-with-human-approval-*/agent/0.log
```

**Log configuration** (Kind cluster defaults):
| Setting | Default Value |
|---------|---------------|
| `containerLogMaxSize` | 10Mi |
| `containerLogMaxFiles` | 5 |

## Workflow Example (Streaming)

For SSE streaming workflow:

1. **User sends request:**
   ```
   POST /chat/stream
   "What's the weather in Tokyo and convert 1000 USD to JPY?"
   ```

2. **Agent creates plan and streams checkpoint:**
   ```
   event: checkpoint
   data: {"checkpoint_id": "cp-456", "plan": {...}}
   ```

3. **User reviews and approves** via UI or API:
   ```
   POST /hitl/command
   {"checkpoint_id": "cp-456", "type": "approve"}
   ```

4. **Resume with SSE streaming:**
   ```
   POST /hitl/resume/cp-456
   ```

5. **Tools execute and response streams back** as SSE events

## Configuration

### Core Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8098 | HTTP server port |
| `REDIS_URL` | redis://localhost:6379 | Redis connection URL |
| `OPENAI_API_KEY` | - | OpenAI API key (primary) |
| `ANTHROPIC_API_KEY` | - | Anthropic API key (fallback) |
| `GROQ_API_KEY` | - | Groq API key (fallback) |
| `APP_ENV` | development | Telemetry profile |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | - | OpenTelemetry endpoint |

### HITL Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `TRUVAG3_HITL_REQUIRE_PLAN_APPROVAL` | `true` | Require approval for all execution plans |
| `TRUVAG3_HITL_SENSITIVE_CAPABILITIES` | `stock_quote,...` | Comma-separated capabilities requiring step approval |
| `TRUVAG3_HITL_DEFAULT_TIMEOUT` | `5m` | Checkpoint expiry timeout (e.g., `30s`, `5m`, `1h`) |

**Note on DefaultAction:** The action taken on timeout (`approve`/`reject`) is determined by the HITL policy based on checkpoint type:
- **Plan checkpoints** (`plan_generated`): Auto-approve on timeout
- **Step checkpoints** (`before_step`): Auto-reject on timeout (fail-safe for sensitive operations)

## Frontend Integration

The agent is designed to work with `examples/chat-ui/hitl.html` which provides:
- Chat interface with SSE streaming
- Approval dialog showing execution plan
- Countdown timer for checkpoint expiration
- Approve/Reject buttons
- Resume execution after approval

## Related Documentation

- [HITL Chat Assistant Plan](../HITL_CHAT_ASSISTANT_PLAN.md) - Detailed implementation plan
- [Human-in-the-Loop Proposal](../../orchestration/HUMAN_IN_THE_LOOP_PROPOSAL.md) - Original design proposal
- [Travel Chat Agent](../travel-chat-agent/README.md) - Similar agent without HITL
