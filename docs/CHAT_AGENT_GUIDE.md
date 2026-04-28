# Chat Agent Implementation Guide

Hey there! Welcome to the Truva-G3 chat agent guide. If you're reading this, you probably want to build something like ChatGPT - a conversational AI that can stream responses in real-time, remember what users said earlier, and maybe even call external tools to get information.

This guide will walk you through everything step-by-step. We'll use the [`travel-chat-agent`](../examples/travel-chat-agent/) example as our reference - it's a working implementation you can run and poke at.

## Table of Contents

- [Why This Guide Exists](#why-this-guide-exists)
- [The Big Picture: What Are We Building?](#the-big-picture-what-are-we-building)
- [Architecture Overview](#architecture-overview)
- [Session Management: Remembering Conversations](#session-management-remembering-conversations)
- [SSE Streaming: Real-Time Responses](#sse-streaming-real-time-responses)
- [AI Orchestration: Making It Smart](#ai-orchestration-making-it-smart)
- [Putting It All Together](#putting-it-all-together)
- [HTTP Handlers: The Entry Points](#http-handlers-the-entry-points)
- [Telemetry: Knowing What's Happening](#telemetry-knowing-whats-happening)
- [Deployment: Getting It Running](#deployment-getting-it-running)
- [Common Patterns and Gotchas](#common-patterns-and-gotchas)
- [Quick Reference](#quick-reference)

---

## Why This Guide Exists

Building a chat agent seems simple at first: "Just call an AI API and return the response, right?"

Well... not quite. Here's what you'll quickly discover you need:

1. **Session management** - Users expect the AI to remember what they said 5 messages ago. Without sessions, every message is a brand new conversation.

2. **Streaming** - Nobody wants to stare at a blank screen for 10 seconds waiting for a complete response. Users expect to see text appearing word-by-word, like someone typing.

3. **Tool orchestration** - "What's the weather in Tokyo?" requires calling a weather API. "Convert $100 to Yen" needs a currency API. Your AI needs to coordinate these.

4. **Observability** - When something breaks at 2 AM, you need to know what happened. Logs, traces, metrics - they're not optional in production.

Without a clear architecture, you end up with spaghetti code that's impossible to debug. This guide gives you a battle-tested pattern.

---

## The Big Picture: What Are We Building?

Let's start with what the user experience looks like:

```
User: "What's the weather in Tokyo?"

[SSE Event: status] "Analyzing your request..."
[SSE Event: status] "Finding weather service..."
[SSE Event: step] "weather-tool completed in 234ms"
[SSE Event: chunk] "The"
[SSE Event: chunk] " weather"
[SSE Event: chunk] " in"
[SSE Event: chunk] " Tokyo"
[SSE Event: chunk] " is"
[SSE Event: chunk] " currently"
[SSE Event: chunk] " 22°C"
[SSE Event: chunk] " and"
[SSE Event: chunk] " sunny."
[SSE Event: done] "Request completed in 1234ms"
```

See those `[SSE Event: ...]` lines? That's Server-Sent Events - a way to push data from server to client in real-time. Each "chunk" is a few words of the AI's response, delivered as the AI generates them.

The user sees: "The weather in Tokyo is currently 22°C and sunny." appearing word by word, with a progress indicator showing which tools are being used.

---

## Architecture Overview

Before we dive into code, let's understand the components and how they fit together. Think of it like a restaurant:

```
┌─────────────────────────────────────────────────────────────────┐
│                         Chat Agent                               │
│                                                                  │
│  Think of this as a restaurant:                                  │
│                                                                  │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐  │
│  │   Session   │  │     SSE     │  │      Orchestrator       │  │
│  │    Store    │  │   Handler   │  │  (The Head Chef)        │  │
│  │             │  │             │  │                         │  │
│  │ The notepad │  │ The waiter  │  │ Decides what to cook    │  │
│  │ that tracks │  │ who brings  │  │ and coordinates the     │  │
│  │ each table's│  │ food as     │  │ kitchen staff           │  │
│  │ order       │  │ it's ready  │  │                         │  │
│  └─────────────┘  └─────────────┘  └─────────────────────────┘  │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │                     BaseAgent                             │   │
│  │  The restaurant building itself - provides:               │   │
│  │  - AI Client (the stove/oven)                             │   │
│  │  - Discovery (menu of available dishes/tools)             │   │
│  │  - Logger (security cameras)                              │   │
│  │  - HTTP Server (the front door)                           │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### How a Request Flows Through the System

Let's trace what happens when a user sends "What's the weather in Tokyo?":

```
1. Client POSTs to /chat/stream
   └─> "Hey, I want to know about weather in Tokyo"

2. SSE Handler receives the request
   └─> "Got it, let me set up a streaming connection"
   └─> Sets headers: Content-Type: text/event-stream

3. Session Store creates or retrieves session
   └─> "This is session abc-123, user has asked 3 questions before"
   └─> Retrieves conversation history for LLM context

4. Query formatted with history context
   └─> "Previous conversation: User: What's the capital of France?..."
   └─> "Current request: What's the population there?"

5. Orchestrator plans the execution
   └─> "To answer this, I need to: 1) Find Tokyo's coordinates 2) Get weather data"
   └─> Discovers available tools in Redis registry

6. Orchestrator executes tools
   └─> Calls geocoding-tool → gets lat/lon
   └─> Calls weather-tool → gets temperature, conditions
   └─> (Streams progress updates via SSE)

7. AI synthesizes the response
   └─> "Based on the tool results, here's a human-friendly answer..."
   └─> (Streams tokens via SSE as they're generated)

8. Session Store saves the response
   └─> "Saved assistant's response to conversation history"

9. SSE Handler sends completion event
   └─> "All done! Here's the summary."
```

---

## Session Management: Remembering Conversations

### Why Sessions Matter

Imagine this conversation:

```
User: "What's the capital of France?"
AI: "The capital of France is Paris."
User: "What's the population?"
```

Without sessions, the AI has no idea what "the population" refers to. Is it France? Paris? The user's hometown? With sessions, the AI sees the full conversation and understands "What's the population of Paris?"

### The Session and Message Types

Here's how we represent a conversation:

```go
// A Session is like a folder containing all messages from one conversation
type Session struct {
    ID        string                 `json:"id"`         // Unique identifier (UUID)
    CreatedAt time.Time              `json:"created_at"` // When conversation started
    UpdatedAt time.Time              `json:"updated_at"` // Last activity (for expiration)
    Messages  []Message              `json:"messages"`   // The actual conversation
    Metadata  map[string]interface{} `json:"metadata,omitempty"` // Extra data (user ID, etc.)
}

// A Message is a single turn in the conversation
type Message struct {
    ID        string                 `json:"id"`
    Role      string                 `json:"role"`      // "user" or "assistant"
    Content   string                 `json:"content"`   // The actual text
    Timestamp time.Time              `json:"timestamp"`
    Metadata  map[string]interface{} `json:"metadata,omitempty"` // Tool calls, token usage, etc.
}
```

### The SessionStore: Where Conversations Live

The SessionStore is basically a thread-safe map with some extra features:

```go
type SessionStore struct {
    sessions    map[string]*Session  // The actual storage
    mu          sync.RWMutex         // Protects concurrent access
    ttl         time.Duration        // How long before sessions expire (e.g., 30 min)
    maxMessages int                  // Max messages per session (sliding window)
    stopCleanup chan struct{}        // Signal to stop the cleanup goroutine
}
```

**Why these fields matter:**

- **`mu sync.RWMutex`**: Multiple HTTP requests might access the same session simultaneously. The mutex prevents race conditions. We use RWMutex (not plain Mutex) so multiple readers don't block each other.

- **`ttl time.Duration`**: Sessions should expire. If a user walks away, we don't want to keep their data forever. 30 minutes is a reasonable default.

- **`maxMessages int`**: AI models have context limits. If you keep 1000 messages, you'll hit token limits. A sliding window (e.g., last 50 messages) keeps things manageable.

### Creating a New SessionStore

```go
func NewSessionStore(ttl time.Duration, maxMessages int) *SessionStore {
    store := &SessionStore{
        sessions:    make(map[string]*Session),
        ttl:         ttl,
        maxMessages: maxMessages,
        stopCleanup: make(chan struct{}),
    }

    // Start a background goroutine that periodically cleans up expired sessions
    // This runs forever until you call store.Stop()
    go store.cleanupLoop()

    return store
}
```

### Creating a Session

When a user starts chatting (or doesn't provide a session ID), create a new session:

```go
func (s *SessionStore) Create(metadata map[string]interface{}) *Session {
    s.mu.Lock()         // Lock for writing
    defer s.mu.Unlock() // Always unlock when done (defer ensures this even if we panic)

    session := &Session{
        ID:        uuid.New().String(),  // Generate a unique ID
        CreatedAt: time.Now(),
        UpdatedAt: time.Now(),
        Messages:  make([]Message, 0),   // Start with empty conversation
        Metadata:  metadata,
    }

    s.sessions[session.ID] = session
    return session
}
```

### Adding Messages (The Sliding Window)

This is where it gets interesting. We want to:
1. Add the new message
2. Keep only the last N messages (to prevent unbounded growth)
3. Update the "last activity" timestamp

```go
func (s *SessionStore) AddMessage(sessionID string, msg Message) bool {
    s.mu.Lock()
    defer s.mu.Unlock()

    session, exists := s.sessions[sessionID]
    if !exists {
        return false  // Session doesn't exist (maybe expired?)
    }

    // Generate message ID if not provided
    if msg.ID == "" {
        msg.ID = uuid.New().String()
    }

    // Add the message
    session.Messages = append(session.Messages, msg)

    // HERE'S THE SLIDING WINDOW:
    // If we have more than maxMessages, drop the oldest ones
    if len(session.Messages) > s.maxMessages {
        // Keep only the last maxMessages
        // Example: maxMessages=50, we have 52 messages
        // 52 - 50 = 2, so we slice from index 2 onwards, dropping first 2
        session.Messages = session.Messages[len(session.Messages)-s.maxMessages:]
    }

    // Update activity timestamp (resets the expiration timer)
    session.UpdatedAt = time.Now()
    return true
}
```

**Why sliding window instead of just rejecting new messages?**

If we rejected messages after hitting the limit, users would get stuck. The sliding window keeps the conversation flowing by always having room for new messages.

### Background Cleanup: Removing Expired Sessions

We don't want to check expiration on every request (slow). Instead, a background goroutine periodically cleans up:

```go
func (s *SessionStore) cleanupLoop() {
    // Check every 5 minutes
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            s.cleanup()  // Do the actual cleanup
        case <-s.stopCleanup:
            return  // Someone called Stop(), exit the loop
        }
    }
}

func (s *SessionStore) cleanup() {
    s.mu.Lock()
    defer s.mu.Unlock()

    for id, session := range s.sessions {
        // If last activity was more than TTL ago, delete it
        if time.Since(session.UpdatedAt) > s.ttl {
            delete(s.sessions, id)
        }
    }
}
```

### Important Limitation: Single-Pod Only!

**This is critical to understand:** The in-memory SessionStore only works on a single pod. If you have 3 pods behind a load balancer, a user might hit pod A for their first message, then pod B for their second message. Pod B has no idea what happened on pod A!

**Solutions for multi-pod deployments:**

1. **Redis-based sessions**: Store sessions in Redis instead of memory. All pods read/write to the same Redis.

2. **Sticky sessions**: Configure your load balancer to always route the same user to the same pod. (Works but has downsides - if that pod crashes, user loses their session.)

3. **Distributed session stores**: Use something like Redis Cluster or a database.

For learning and development, the in-memory store is fine. For production with multiple pods, you'll need one of the above solutions.

---

## SSE Streaming: Real-Time Responses

### What is SSE (Server-Sent Events)?

SSE is a web standard that lets the server push data to the client. Unlike WebSockets (which are bidirectional), SSE is one-way: server → client. This is perfect for chat because:

- The client sends a message (normal HTTP POST)
- The server streams back the response (SSE)

**Why SSE over WebSockets?**

| Feature | SSE | WebSocket |
|---------|-----|-----------|
| Direction | Server → Client only | Bidirectional |
| Reconnection | Built-in (browser handles it) | You implement it |
| HTTP/2 Support | Yes (multiplexed) | Separate connection |
| Complexity | Low | Higher |
| Proxy Support | Works through HTTP proxies | May need special config |

For chat agents, SSE is usually simpler and sufficient.

### The SSE Event Format

SSE has a simple text format:

```
event: eventtype
data: {"some": "json"}

event: anotherevent
data: more data here

```

Notice:
- `event:` line specifies the event type
- `data:` line contains the payload (often JSON)
- Blank line (`\n\n`) marks the end of an event

### Our Event Types

We use different events for different purposes:

| Event | When It's Sent | What It Contains |
|-------|----------------|------------------|
| `session` | New session created | `{"id": "session-uuid"}` |
| `status` | Progress update | `{"step": "planning", "message": "Analyzing..."}` |
| `step` | Tool finished | `{"tool": "weather", "success": true, "duration_ms": 234}` |
| `chunk` | Part of AI response | `{"text": "The weather"}` |
| `usage` | Token stats | `{"prompt_tokens": 100, "completion_tokens": 50}` |
| `finish` | Stream ending reason | `{"reason": "stop"}` |
| `done` | All complete | `{"request_id": "abc", "total_duration_ms": 1234}` |
| `error` | Something went wrong | `{"code": "rate_limit", "message": "...", "retryable": true}` |

### The StreamCallback Interface

We define an interface for sending SSE events. This abstraction lets us test without real HTTP:

```go
// StreamCallback defines what our SSE sender can do
type StreamCallback interface {
    // Progress updates
    SendStatus(step, message string)

    // Tool execution results
    SendStep(stepID, tool string, success bool, durationMs int64)

    // The actual AI response, piece by piece
    SendChunk(text string)

    // When everything is done
    SendDone(requestID string, toolsUsed []string, totalDurationMs int64)

    // Error handling
    SendError(code, message string, retryable bool)

    // Token usage statistics
    SendUsage(promptTokens, completionTokens, totalTokens int)

    // Why the stream ended
    SendFinish(reason string)
}
```

### Implementing SSECallback

Here's how we actually send SSE events:

```go
type SSECallback struct {
    w       http.ResponseWriter  // Where we write the response
    flusher http.Flusher         // Lets us push data immediately
}

func NewSSECallback(w http.ResponseWriter, flusher http.Flusher) *SSECallback {
    return &SSECallback{w: w, flusher: flusher}
}

// Send a response chunk (the most common operation)
func (c *SSECallback) SendChunk(text string) {
    c.sendEvent("chunk", map[string]interface{}{
        "text": text,
    })
}

// Send any event
func (c *SSECallback) sendEvent(eventType string, data interface{}) {
    // Convert data to JSON
    jsonData, err := json.Marshal(data)
    if err != nil {
        return  // Silently fail (logging would be better in production)
    }

    // Write in SSE format: event: type\ndata: json\n\n
    fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", eventType, jsonData)

    // CRITICAL: Flush immediately!
    // Without this, data might be buffered and not sent until the response ends
    c.flusher.Flush()
}
```

### Setting Up the SSE Handler

The handler needs to:
1. Handle CORS (if your frontend is on a different domain)
2. Check that SSE is supported
3. Set the right headers
4. Process the request

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // === CORS HANDLING ===
    // Browsers send a "preflight" OPTIONS request before the real POST
    // We need to handle this, or the browser will reject the request
    w.Header().Set("Access-Control-Allow-Origin", "*")  // Or your specific domain
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, X-Requested-With")
    w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")

    if r.Method == http.MethodOptions {
        // This is just a preflight check, respond with 200 and return
        w.WriteHeader(http.StatusOK)
        return
    }

    // === CHECK SSE SUPPORT ===
    // http.Flusher is an optional interface - not all ResponseWriters support it
    // (Though in practice, the standard library's implementation always does)
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "SSE not supported", http.StatusInternalServerError)
        return
    }

    // === SET SSE HEADERS ===
    // These headers tell the browser "this is a streaming response"
    w.Header().Set("Content-Type", "text/event-stream")  // MIME type for SSE
    w.Header().Set("Cache-Control", "no-cache")          // Don't cache!
    w.Header().Set("Connection", "keep-alive")           // Keep the connection open

    // This one is important if you're behind Nginx:
    // Nginx buffers responses by default, which breaks SSE
    w.Header().Set("X-Accel-Buffering", "no")

    // Now we're ready to stream!
    // ... parse request, process, send events ...
}
```

### Client-Side: Consuming SSE

Standard `EventSource` doesn't support POST requests (it's GET-only). For a chat agent, we need POST to send the user's message. Here's how to handle it:

```javascript
// Using fetch with ReadableStream
async function streamChat(message, sessionId) {
    const response = await fetch('/chat/stream', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ message, session_id: sessionId })
    });

    // Get a reader for the response body stream
    const reader = response.body.getReader();
    const decoder = new TextDecoder();

    // Buffer for incomplete events (SSE events might be split across chunks)
    let buffer = '';

    while (true) {
        const { value, done } = await reader.read();
        if (done) break;

        // Decode the bytes to text and add to buffer
        buffer += decoder.decode(value, { stream: true });

        // Parse complete SSE events from buffer
        const events = parseSSEEvents(buffer);
        buffer = events.remaining;  // Keep incomplete data for next iteration

        for (const event of events.complete) {
            handleEvent(event);
        }
    }
}

function handleEvent(event) {
    switch (event.type) {
        case 'session':
            // New session created, save the ID for future requests
            currentSessionId = event.data.id;
            break;

        case 'chunk':
            // Append text to the response area
            responseDiv.textContent += event.data.text;
            break;

        case 'step':
            // Show tool progress
            const status = event.data.success ? '✓' : '✗';
            console.log(`${status} ${event.data.tool} (${event.data.duration_ms}ms)`);
            break;

        case 'done':
            // Request complete
            console.log(`Completed in ${event.data.total_duration_ms}ms`);
            enableSubmitButton();
            break;

        case 'error':
            // Something went wrong
            showError(event.data.message);
            if (event.data.retryable) {
                showRetryButton();
            }
            break;
    }
}
```

---

## AI Orchestration: Making It Smart

### What is Orchestration?

Orchestration is how the AI figures out what tools to use and in what order. When a user asks "What's the weather in Tokyo and how much is $100 in Yen?", the orchestrator:

1. **Analyzes** the query - "This needs weather data AND currency conversion"
2. **Discovers** available tools - "I have weather-tool and currency-tool"
3. **Plans** the execution - "I can call both in parallel since they're independent"
4. **Executes** the plan - Calls both tools, collects results
5. **Synthesizes** the response - "Based on the results, here's a coherent answer"

### Setting Up the Orchestrator

The orchestrator needs several components configured:

```go
func (t *TravelChatAgent) InitializeOrchestrator(discovery core.Discovery) error {
    t.mu.Lock()
    defer t.mu.Unlock()

    // === CONFIGURATION ===
    config := orchestration.DefaultConfig()

    // ModeAutonomous: AI decides which tools to use
    // ModeDirected: You specify exactly which tools
    // ModeHybrid: AI suggests, you approve
    config.RoutingMode = orchestration.ModeAutonomous

    // StrategyLLM: Use AI to synthesize final response from tool results
    // StrategyTemplate: Use predefined templates
    config.SynthesisStrategy = orchestration.StrategyLLM

    // Enable metrics and tracing
    config.MetricsEnabled = true
    config.EnableTelemetry = true

    // === TIMEOUTS ===
    // These are important! AI calls can be slow.
    // If a tool takes too long, you don't want to hang forever.
    config.ExecutionOptions.TotalTimeout = 5 * time.Minute  // Overall limit
    config.ExecutionOptions.StepTimeout = 120 * time.Second // Per-tool limit

    // === DOMAIN-SPECIFIC CONFIGURATION ===
    // Help the AI understand your domain's data types
    config.PromptConfig = orchestration.PromptConfig{
        Domain: "travel",  // Affects prompt wording

        // Type rules add domain-specific information beyond core type rules.
        // Core rules already handle number, integer, boolean, etc.
        // Use AdditionalTypeRules for domain-specific format patterns (e.g., currency codes).
        AdditionalTypeRules: []orchestration.TypeRule{
            {
                TypeNames:   []string{"currency_code", "from_currency", "to_currency"},
                JsonType:    "JSON strings",
                Example:     `"USD"`,
                Description: "ISO 4217 currency codes",
            },
        },

        // Custom instructions for your domain
        CustomInstructions: []string{
            "For weather queries, always geocode the location first",
            "For currency conversion, extract the destination country's currency",
            "Prefer parallel execution when steps are independent",
        },
    }

    // === DEPENDENCIES ===
    deps := orchestration.OrchestratorDependencies{
        Discovery:           discovery,                        // How to find tools
        AIClient:            t.AI,                             // For LLM calls
        Logger:              t.Logger,                         // For logging
        Telemetry:           telemetry.GetTelemetryProvider(), // For tracing
        EnableErrorAnalyzer: true,                             // Smart error handling
    }

    // === CREATE AND START ===
    orch, err := orchestration.CreateOrchestrator(config, deps)
    if err != nil {
        return fmt.Errorf("failed to create orchestrator: %w", err)
    }

    // Start initializes background tasks (discovery refresh, etc.)
    if err := orch.Start(context.Background()); err != nil {
        return fmt.Errorf("failed to start orchestrator: %w", err)
    }

    t.orchestrator = orch
    return nil
}
```

### Conversation History for Context

For a chat agent to understand references like "What's the population there?", it needs context from previous messages. The recommended integration is to pass raw conversation turns in metadata and let orchestration build `<conversation_history>` separately from the current query:

```go
func (t *TravelChatAgent) addConversationHistoryMetadata(
    metadata map[string]interface{},
    sessionID string,
    history []Message,
) map[string]interface{} {
    if metadata == nil {
        metadata = make(map[string]interface{})
    }
    if len(history) == 0 {
        return metadata
    }

    turns := make([]core.ConversationTurn, 0, len(history))
    for _, msg := range history {
        turns = append(turns, core.ConversationTurn{
            Role:    msg.Role,
            Content: msg.Content,
        })
    }

    metadata[orchestration.MetadataConversationTurns] = turns
    metadata[orchestration.MetadataConversationSessionKey] = sessionID
    return metadata
}
```

**What the framework does with that metadata:**
```
metadata[orchestration.MetadataConversationTurns] = []core.ConversationTurn{...}
metadata[orchestration.MetadataConversationSessionKey] = "session-123"
orchestrator.ProcessRequestStreaming(ctx, "What's the population?", metadata, ...)
```

The shared conversation-history preparer then builds the `<conversation_history>` enrichment before planning, so the LLM still understands that "the population" refers to Paris without you having to bake old turns into the query text yourself.

If you want the full Tier 1 / Tier 2 / Layer 3 story in one place, see [CONVERSATION_HISTORY_GUIDE.md](./CONVERSATION_HISTORY_GUIDE.md).

Tier 1 is the default behavior. If you want Tier 2 recursive compaction, use the Layer 2 helper and inject the preparer explicitly:

```go
preparer, err := orchestration.BuildCompactionEnabledConversationHistoryPreparer(
    config,
    t.AI,
    orchestration.WithConversationSummaryCache(myCache),   // optional override
    orchestration.WithConversationCompactor(myCompactor),  // optional override
)
if err != nil {
    return err
}

deps := orchestration.OrchestratorDependencies{
    Discovery:                   discovery,
    AIClient:                    t.AI,
    Logger:                      t.Logger,
    Telemetry:                   telemetry.GetTelemetryProvider(),
    ConversationHistoryPreparer: preparer,
}
```

That keeps the richer Layer 2 path ergonomic: the helper installs the default cache and LLM compactor, and your overrides win if you provide them.

### Streaming with the Orchestrator

Here's where the magic happens - processing a query with real-time streaming:

```go
func (t *TravelChatAgent) ProcessWithStreaming(
    ctx context.Context,
    sessionID string,
    query string,
    callback StreamCallback,
) error {
    startTime := time.Now()

    // Retrieve conversation history for continuity
    history := t.sessionStore.GetHistory(sessionID)
    metadata := t.addConversationHistoryMetadata(nil, sessionID, history)

    // Let the user know we're working on it
    callback.SendStatus("planning", "Analyzing your request...")

    // === STEP CALLBACKS ===
    // We want to notify the user as each tool completes
    // WithStepCallback attaches a callback to the context
    ctx = orchestration.WithStepCallback(ctx,
        func(stepIndex, totalSteps int, step orchestration.RoutingStep, stepResult orchestration.StepResult) {
            // This is called AFTER each tool finishes
            callback.SendStep(
                fmt.Sprintf("step_%d", stepIndex+1),  // e.g., "step_1"
                step.AgentName,                       // e.g., "weather-tool"
                stepResult.Success,                   // true/false
                stepResult.Duration.Milliseconds(),   // how long it took
            )
        },
    )

    // === PROCESS WITH STREAMING ===
    // This is the main orchestration call
    // The callback receives tokens as they're generated
    result, err := t.orchestrator.ProcessRequestStreaming(
        ctx,
        query,     // Keep the live user query separate from prior turns
        metadata,  // Raw turns become <conversation_history> inside orchestration
        func(chunk core.StreamChunk) error {
            // Each chunk is a few tokens of the AI's response
            if chunk.Content != "" {
                callback.SendChunk(chunk.Content)
            }
            // Return an error here to stop streaming
            return nil
        },
    )
    if err != nil {
        return fmt.Errorf("streaming orchestration failed: %w", err)
    }

    // === SEND FINAL STATS ===
    if result.Usage != nil {
        callback.SendUsage(
            result.Usage.PromptTokens,
            result.Usage.CompletionTokens,
            result.Usage.TotalTokens,
        )
    }

    callback.SendDone(
        result.RequestID,
        result.AgentsInvolved,
        time.Since(startTime).Milliseconds(),
    )

    // === SAVE TO SESSION ===
    // Store the assistant's response so the next query has context
    t.sessionStore.AddMessage(sessionID, Message{
        Role:      "assistant",
        Content:   result.Response,  // The complete, accumulated response
        Timestamp: time.Now(),
        Metadata: map[string]interface{}{
            "request_id":  result.RequestID,
            "tools_used":  result.AgentsInvolved,
            "confidence":  result.Confidence,
            "duration_ms": time.Since(startTime).Milliseconds(),
        },
    })

    return nil
}
```

### Adding Human Approval for Sensitive Operations

For chat agents handling sensitive operations (payments, data deletion, account changes), you can add Human-in-the-Loop (HITL) approval checkpoints. When configured, the orchestrator pauses before executing sensitive tools and waits for human approval via your approval system (Slack, dashboard, etc.).

```go
// Add HITL to your orchestrator configuration
controller := orchestration.NewInterruptController(policy, checkpointStore, handler)
orchestrator := orchestration.NewAIOrchestrator(config, discovery, aiClient,
    orchestration.WithHITL(controller),
)

// When a sensitive operation is detected, the orchestrator:
// 1. Creates a checkpoint and pauses execution
// 2. Sends webhook to your approval system
// 3. Waits for approve/reject command
// 4. Continues or aborts based on the response
```

→ See [Human-in-the-Loop User Guide](HUMAN_IN_THE_LOOP_USER_GUIDE.md) for complete HITL setup and configuration

---

## Putting It All Together

### Project Structure

Here's how the travel-chat-agent example is organized:

```
travel-chat-agent/
├── main.go           # Entry point - wires everything together
├── chat_agent.go     # The agent itself - orchestrator, session store
├── session.go        # Session management implementation
├── sse_handler.go    # HTTP handler for /chat/stream
└── handlers.go       # Other HTTP handlers (health, session management)
```

### The Main Entry Point

Let's walk through `main.go` - it's the blueprint for how everything starts up:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/truvaagents/truva-g3/core"
    "github.com/truvaagents/truva-g3/telemetry"

    // These blank imports register the AI providers
    // Without them, ai.NewClient() won't find any providers!
    _ "github.com/truvaagents/truva-g3/ai/providers/anthropic"
    _ "github.com/truvaagents/truva-g3/ai/providers/openai"
)

func main() {
    // === STEP 1: VALIDATE CONFIGURATION ===
    // Fail fast if something is misconfigured
    // Better to fail at startup than at 2 AM
    if err := validateConfig(); err != nil {
        log.Fatalf("Configuration error: %v", err)
    }

    // === STEP 2: SET COMPONENT TYPE ===
    // This affects how telemetry categorizes this service
    core.SetCurrentComponentType(core.ComponentTypeAgent)

    // === STEP 3: INITIALIZE TELEMETRY ===
    // IMPORTANT: Do this BEFORE creating the agent!
    // If you create the agent first, its logger won't have trace correlation
    initTelemetry("travel-chat-agent")

    // Ensure telemetry is flushed on shutdown
    defer func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        telemetry.Shutdown(ctx)
    }()

    // === STEP 4: CREATE THE AGENT ===
    agent, err := NewTravelChatAgent()
    if err != nil {
        log.Fatalf("Failed to create agent: %v", err)
    }

    // === STEP 5: CREATE THE FRAMEWORK ===
    // The framework handles HTTP server, discovery registration, etc.
    middlewareConfig := &telemetry.TracingMiddlewareConfig{
        ExcludedPaths: []string{"/health", "/metrics", "/ready", "/live"},  // Don't trace these
    }

    framework, err := core.NewFramework(agent,
        core.WithName("travel-chat-agent"),
        core.WithPort(getPort()),                       // Usually from PORT env var
        core.WithNamespace(os.Getenv("NAMESPACE")),     // Kubernetes namespace (optional)
        core.WithRedisURL(os.Getenv("REDIS_URL")),      // For service discovery
        core.WithDiscovery(true, "redis"),              // Enable Redis-based discovery
        core.WithCORS([]string{"*"}, true),             // Allow all origins (adjust for production)
        core.WithMiddleware(telemetry.TracingMiddlewareWithConfig("travel-chat-agent", middlewareConfig)),
    )
    if err != nil {
        log.Fatalf("Failed to create framework: %v", err)
    }

    // === STEP 6: INITIALIZE ORCHESTRATOR (IN BACKGROUND) ===
    // WHY BACKGROUND? The Discovery isn't available until framework.Run() starts.
    // We don't want to block startup waiting for it.
    go func() {
        // Wait for Discovery to be initialized
        for agent.BaseAgent.Discovery == nil {
            time.Sleep(100 * time.Millisecond)
        }

        // Now we can initialize the orchestrator
        if err := agent.InitializeOrchestrator(agent.BaseAgent.Discovery); err != nil {
            // Log but don't crash - agent can still work without orchestration
            agent.Logger.Warn("Failed to initialize orchestrator", map[string]interface{}{
                "error": err.Error(),
            })
        }
    }()

    // === STEP 7: GRACEFUL SHUTDOWN ===
    // Handle Ctrl+C and termination signals gracefully
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    go func() {
        <-sigChan  // Wait for signal
        cancel()   // Cancel context, which triggers framework shutdown
    }()

    // === STEP 8: RUN! ===
    // This blocks until ctx is canceled
    if err := framework.Run(ctx); err != nil && err != context.Canceled {
        log.Fatalf("Framework error: %v", err)
    }
}
```

### The Agent Definition

Here's how we define the chat agent itself:

```go
type TravelChatAgent struct {
    *core.BaseAgent                             // Embedded - gives us Discovery, Logger, etc.
    orchestrator *orchestration.AIOrchestrator  // For processing queries
    sessionStore *SessionStore                  // Conversation memory
    httpClient   *http.Client                   // Traced HTTP client for tool calls
    mu           sync.RWMutex                   // Protects orchestrator (it's set async)
}

func NewTravelChatAgent() (*TravelChatAgent, error) {
    // Create base agent - this gives us the foundation
    agent := core.NewBaseAgent("travel-chat-agent")

    // === SET UP AI CLIENT ===
    // We try to create a chain client (failover between providers)
    // If that fails, fall back to a single provider
    chainClient, err := ai.NewChainClient(
        ai.WithProviderChain("openai", "anthropic"),  // Try OpenAI first, then Anthropic
        ai.WithChainTelemetry(telemetry.GetTelemetryProvider()),
        ai.WithChainLogger(agent.Logger),
    )
    if err != nil {
        // Chain client failed, try single provider
        singleClient, err := ai.NewClient()
        if err != nil {
            // No AI at all - log warning but don't crash
            // (Agent might still be useful for other things)
            agent.Logger.Warn("AI client creation failed", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            agent.AI = singleClient
        }
    } else {
        agent.AI = chainClient
    }

    // === DECLARE METRICS ===
    // Tell the telemetry system what metrics we'll emit
    // (See Telemetry section for full metrics declaration)

    // === SET UP TRACED HTTP CLIENT ===
    // For making tool calls with distributed tracing
    tracedClient := telemetry.NewTracedHTTPClientWithTransport(&http.Transport{
        MaxIdleConns:        100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout:     90 * time.Second,
    })
    tracedClient.Timeout = 300 * time.Second  // Increased for complex orchestration

    // === SET UP SESSION STORE ===
    // 30 minute TTL, 50 message sliding window
    sessionStore := NewSessionStore(30*time.Minute, 50)

    chatAgent := &TravelChatAgent{
        BaseAgent:    agent,
        sessionStore: sessionStore,
        httpClient:   tracedClient,
    }

    // Register our HTTP endpoints
    chatAgent.registerCapabilities()

    return chatAgent, nil
}

func (t *TravelChatAgent) registerCapabilities() {
    // === STREAMING CHAT ENDPOINT ===
    t.RegisterCapability(core.Capability{
        Name:        "chat_stream",
        Description: "SSE streaming chat endpoint for travel queries",
        Endpoint:    "/chat/stream",
        Handler:     NewSSEHandler(t).ServeHTTP,
        Internal:    true,  // <-- IMPORTANT: Don't expose to orchestrator
    })

    // === SESSION MANAGEMENT ===
    t.RegisterCapability(core.Capability{
        Name:        "create_session",
        Description: "Create a new chat session",
        Endpoint:    "/chat/session",
        Handler:     t.handleCreateSession,
        Internal:    true,
    })

    t.RegisterCapability(core.Capability{
        Name:        "get_session",
        Description: "Get session information",
        Endpoint:    "/chat/session/{id}",
        Handler:     t.handleGetSession,
        Internal:    true,
    })

    t.RegisterCapability(core.Capability{
        Name:        "get_history",
        Description: "Get conversation history for a session",
        Endpoint:    "/chat/session/{id}/history",
        Handler:     t.handleGetHistory,
        Internal:    true,
    })

    // === HEALTH AND DISCOVERY ===
    t.RegisterCapability(core.Capability{
        Name:        "health",
        Description: "Health check with orchestrator status",
        Endpoint:    "/health",
        Handler:     t.handleHealth,
        Internal:    true,
    })

    t.RegisterCapability(core.Capability{
        Name:        "discover",
        Description: "Discover available tools",
        Endpoint:    "/discover",
        Handler:     t.handleDiscover,
        Internal:    true,
    })
}
```

**Why `Internal: true`?**

When the orchestrator discovers tools, it builds a catalog of available capabilities. Setting `Internal: true` excludes these endpoints from that catalog. You don't want the orchestrator trying to call `/chat/stream` - that's for humans, not AI!

---

## HTTP Handlers: The Entry Points

### Health Check Handler

Every production service needs a health check. Kubernetes uses it to know if your pod is ready:

```go
func (t *TravelChatAgent) handleHealth(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Start with healthy
    health := map[string]interface{}{
        "status":    "healthy",
        "timestamp": time.Now().Unix(),
        "service":   "travel-chat-agent",
    }

    // === CHECK REDIS/DISCOVERY ===
    if t.Discovery != nil {
        _, err := t.Discovery.Discover(ctx, core.DiscoveryFilter{})
        if err != nil {
            health["status"] = "degraded"  // Not "unhealthy" - we can still work
            health["redis"] = "unavailable"
            t.Logger.WarnWithContext(ctx, "Health check: Redis unavailable", map[string]interface{}{
                "error": err.Error(),
            })
        } else {
            health["redis"] = "healthy"
        }
    } else {
        health["redis"] = "not configured"
    }

    // === CHECK ORCHESTRATOR ===
    orch := t.GetOrchestrator()  // Thread-safe getter
    if orch != nil {
        metrics := orch.GetMetrics()
        health["orchestrator"] = map[string]interface{}{
            "status":              "active",
            "total_requests":      metrics.TotalRequests,
            "successful_requests": metrics.SuccessfulRequests,
            "failed_requests":     metrics.FailedRequests,
            "average_latency_ms":  metrics.AverageLatency.Milliseconds(),
        }
    } else {
        // Orchestrator still initializing
        health["orchestrator"] = "initializing"
    }

    // === CHECK AI PROVIDER ===
    if t.AI != nil {
        health["ai_provider"] = "connected"
    } else {
        health["ai_provider"] = "not configured"
    }

    // === SESSION STATS ===
    health["active_sessions"] = t.sessionStore.GetActiveSessionCount()

    // Set appropriate status code
    statusCode := http.StatusOK
    if health["status"] == "unhealthy" {
        statusCode = http.StatusServiceUnavailable
    }

    // Return JSON with CORS headers
    setCORSHeaders(w)
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(statusCode)
    json.NewEncoder(w).Encode(health)
}
```

### Session Creation Handler

Let clients create sessions explicitly (optional - we also auto-create):

```go
func (t *TravelChatAgent) handleCreateSession(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // Handle CORS preflight
    if r.Method == http.MethodOptions {
        setCORSHeaders(w)
        w.WriteHeader(http.StatusOK)
        return
    }

    // Only accept POST
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
        return
    }

    // Parse optional metadata from request body
    var metadata map[string]interface{}
    if r.Body != nil && r.ContentLength > 0 {
        if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
            // Ignore decode errors, proceed without metadata
            metadata = nil
        }
    }

    // Create a new session
    session := t.sessionStore.Create(metadata)

    // Log it (with trace correlation!)
    t.Logger.InfoWithContext(ctx, "Session created", map[string]interface{}{
        "operation":  "create_session",
        "session_id": session.ID,
    })

    // Return session info
    writeJSON(w, http.StatusCreated, map[string]interface{}{
        "session_id": session.ID,
        "created_at": session.CreatedAt,
        "expires_at": session.CreatedAt.Add(30 * time.Minute),
    })
}
```

---

## Telemetry: Knowing What's Happening

### Why Telemetry Matters

Imagine this scenario:
- User reports: "The chat was really slow yesterday afternoon"
- You: "Let me check... which user? What time exactly? What did they ask?"

Without telemetry, you're guessing. With telemetry, you can:
- Find all requests from that time period
- See exactly how long each step took
- Identify which tool was slow
- Trace the entire request path

### Declaring Metrics

Tell the telemetry system what metrics you'll record:

```go
// In NewTravelChatAgent()
telemetry.DeclareMetrics("travel-chat-agent", telemetry.ModuleConfig{
    Metrics: []telemetry.MetricDefinition{
        {
            Name:    "chat.request.duration_ms",
            Type:    "histogram",  // For distributions (p50, p95, p99)
            Help:    "Chat request duration in milliseconds",
            Labels:  []string{"session_id", "status"},
            Buckets: []float64{100, 500, 1000, 2000, 5000, 10000, 30000},
        },
        {
            Name:   "chat.requests",
            Type:   "counter",  // Just counts up
            Help:   "Number of chat requests",
            Labels: []string{"status"},
        },
        {
            Name: "chat.sessions.active",
            Type: "gauge",  // Can go up or down
            Help: "Number of active chat sessions",
        },
    },
})
```

### Adding Span Events

Span events are checkpoints within a trace:

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    startTime := time.Now()

    // Mark when we received the request
    telemetry.AddSpanEvent(ctx, "request_received",
        attribute.String("method", r.Method),
        attribute.String("operation", "chat_stream"),
    )

    // ... later, when we start processing ...

    telemetry.AddSpanEvent(ctx, "processing_started",
        attribute.String("session_id", sessionID),
        attribute.Int("message_length", len(req.Message)),
    )

    // ... and when we're done ...

    telemetry.AddSpanEvent(ctx, "stream_completed",
        attribute.String("session_id", sessionID),
        attribute.Float64("duration_ms", float64(time.Since(startTime).Milliseconds())),
    )
}
```

### Logging with Trace Correlation

**Always use `WithContext` methods in HTTP handlers!** This attaches the trace ID to logs:

```go
// GOOD - trace ID is included
h.agent.Logger.InfoWithContext(ctx, "SSE stream started", map[string]interface{}{
    "operation": "chat_stream",
    "method":    r.Method,
    "path":      r.URL.Path,
})

// BAD - no trace correlation
h.agent.Logger.Info("SSE stream started", map[string]interface{}{
    "operation": "chat_stream",
    "method":    r.Method,
    "path":      r.URL.Path,
})
```

When you use `WithContext`, the log might look like:
```json
{
    "level": "info",
    "msg": "SSE stream started",
    "trace_id": "abc123def456",
    "span_id": "789ghi",
    "operation": "chat_stream"
}
```

Now you can correlate logs with traces in your observability tool!

---

## Deployment: Getting It Running

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | HTTP server port | 8095 |
| `REDIS_URL` | Redis connection (required for discovery) | - |
| `OPENAI_API_KEY` | OpenAI API key | - |
| `ANTHROPIC_API_KEY` | Anthropic API key | - |
| `APP_ENV` | Environment name | development |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTEL collector for traces | - |

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: travel-chat-agent
spec:
  # IMPORTANT: Start with 1 replica due to in-memory sessions
  # For multiple replicas, you need Redis-based sessions
  replicas: 1
  template:
    spec:
      containers:
      - name: travel-chat-agent
        image: travel-chat-agent:latest
        ports:
        - containerPort: 8095
        env:
        - name: PORT
          value: "8095"
        - name: REDIS_URL
          valueFrom:
            secretKeyRef:
              name: redis-secrets
              key: url
        - name: OPENAI_API_KEY
          valueFrom:
            secretKeyRef:
              name: ai-secrets
              key: openai-key

        # Resource limits - adjust based on your needs
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"

        # Kubernetes health checks
        readinessProbe:
          httpGet:
            path: /health
            port: 8095
          initialDelaySeconds: 10  # Give it time to start
          periodSeconds: 5

        livenessProbe:
          httpGet:
            path: /health
            port: 8095
          initialDelaySeconds: 30
          periodSeconds: 10
```

### Nginx Configuration for SSE

If you're using Nginx as a reverse proxy, you **must** disable buffering for SSE to work:

```nginx
location /chat/stream {
    proxy_pass http://travel-chat-agent:8095;

    # Use HTTP/1.1 for keep-alive
    proxy_http_version 1.1;
    proxy_set_header Connection "";

    # CRITICAL: Disable buffering!
    # Without these, Nginx buffers the entire response before sending
    proxy_buffering off;
    proxy_cache off;
    chunked_transfer_encoding off;

    # Increase timeouts for long-running streams
    proxy_read_timeout 300s;
}
```

---

## Common Patterns and Gotchas

### Pattern 1: Graceful Degradation

The orchestrator might not be ready immediately. Handle this gracefully:

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... setup code ...

    // Check if orchestrator is ready
    if h.agent.GetOrchestrator() == nil {
        callback.SendError(
            "service_unavailable",
            "Chat service is still starting up, please try again in a moment",
            true,  // retryable = true
        )
        return
    }

    // ... rest of handler ...
}
```

### Pattern 2: Auto-Create Sessions

Don't force users to create sessions explicitly:

```go
sessionID := req.SessionID
if sessionID == "" {
    // No session provided, create one
    session := h.agent.sessionStore.Create(nil)
    sessionID = session.ID

    // Tell the client their new session ID
    callback.sendEvent("session", map[string]interface{}{
        "id": sessionID,
    })
}
```

### Pattern 3: Error Classification

Tell the client whether they should retry:

```go
// Retryable errors - client should try again
callback.SendError("rate_limit", "Too many requests, please wait", true)
callback.SendError("timeout", "Request timed out, please try again", true)
callback.SendError("service_unavailable", "Service temporarily unavailable", true)

// Non-retryable errors - client needs to fix something
callback.SendError("validation_error", "Message cannot be empty", false)
callback.SendError("session_expired", "Session has expired, start a new conversation", false)
callback.SendError("invalid_input", "Invalid JSON format", false)
```

### Pattern 4: Thread-Safe Orchestrator Access

The orchestrator is set asynchronously. Always use a getter with locks:

```go
func (t *TravelChatAgent) GetOrchestrator() *orchestration.AIOrchestrator {
    t.mu.RLock()
    defer t.mu.RUnlock()
    return t.orchestrator
}

func (t *TravelChatAgent) SetOrchestrator(orch *orchestration.AIOrchestrator) {
    t.mu.Lock()
    defer t.mu.Unlock()
    t.orchestrator = orch
}
```

### Gotcha: Don't Block on Discovery!

**Wrong - blocks startup:**
```go
func main() {
    agent := NewTravelChatAgent()

    // BAD: This blocks until discovery is ready
    for agent.Discovery == nil {
        time.Sleep(100 * time.Millisecond)
    }
    agent.InitializeOrchestrator(agent.Discovery)

    framework.Run(ctx)  // HTTP server doesn't start until above completes!
}
```

**Right - async initialization:**
```go
func main() {
    agent := NewTravelChatAgent()

    // GOOD: Initialize in background
    go func() {
        for agent.Discovery == nil {
            time.Sleep(100 * time.Millisecond)
        }
        agent.InitializeOrchestrator(agent.Discovery)
    }()

    framework.Run(ctx)  // Server starts immediately, orchestrator catches up
}
```

### Gotcha: CORS Preflight

Browsers send an OPTIONS request before the actual POST. If you don't handle it, the request fails:

```go
func (h *SSEHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Set CORS headers FIRST, before anything else
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
    w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")

    // Handle preflight
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return  // Don't process further
    }

    // ... rest of handler ...
}
```

### Gotcha: Flush After Every Event

SSE won't work if you don't flush:

```go
// WRONG - data buffers and nothing is sent
fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", jsonData)

// RIGHT - flush immediately
fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", jsonData)
flusher.Flush()  // <-- Critical!
```

---

## Quick Reference

### SSE Event Format

```
event: <type>
data: <json>

```

Example stream:
```
event: session
data: {"id":"abc-123"}

event: status
data: {"step":"planning","message":"Analyzing request..."}

event: step
data: {"tool":"weather-tool","success":true,"duration_ms":234}

event: chunk
data: {"text":"The weather"}

event: chunk
data: {"text":" in Tokyo"}

event: chunk
data: {"text":" is sunny."}

event: usage
data: {"prompt_tokens":150,"completion_tokens":25,"total_tokens":175}

event: done
data: {"request_id":"req-xyz","tools_used":["weather-tool"],"total_duration_ms":1234}
```

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/chat/stream` | POST | Main streaming chat endpoint |
| `/chat/session` | POST | Create a new session |
| `/chat/session/{id}` | GET | Get session info |
| `/chat/session/{id}/history` | GET | Get conversation history |
| `/health` | GET | Health check |
| `/discover` | GET | List available tools |

### Request Format

```json
{
    "session_id": "optional-existing-session-id",
    "message": "What is the weather in Tokyo?",
    "options": {}
}
```

### Pre-Flight Checklist

Before deploying, verify:

- [ ] Telemetry initialized BEFORE creating the agent
- [ ] Using `WithContext` logging in all HTTP handlers
- [ ] CORS preflight (OPTIONS) handled
- [ ] SSE headers set (`Content-Type: text/event-stream`, etc.)
- [ ] `X-Accel-Buffering: no` set for Nginx compatibility
- [ ] Orchestrator initialized in background goroutine
- [ ] `Internal: true` set for non-tool capabilities
- [ ] Health check endpoint implemented
- [ ] Appropriate timeouts configured
- [ ] Flush called after every SSE event

---

## See Also

- **[AGENT_MEMORY_USER_GUIDE.md](./AGENT_MEMORY_USER_GUIDE.md)** - Cross-agent shared memory, activity compaction, and coordination
- **[CONVERSATION_HISTORY_GUIDE.md](./CONVERSATION_HISTORY_GUIDE.md)** - Tier 1 protection, Tier 2 recursive compaction, and Layer 3 overrides for chat history
- **[ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md](./ADDING_CONTEXT_TO_YOUR_AGENT_GUIDE.md)** - Building custom pipeline hooks
- **[LOGGING_IMPLEMENTATION_GUIDE.md](./LOGGING_IMPLEMENTATION_GUIDE.md)** - Deep dive into logging with trace correlation
- **[DISTRIBUTED_TRACING_GUIDE.md](./DISTRIBUTED_TRACING_GUIDE.md)** - Setting up distributed tracing
- **[HUMAN_IN_THE_LOOP_USER_GUIDE.md](./HUMAN_IN_THE_LOOP_USER_GUIDE.md)** - Adding approval workflows for sensitive operations
- **[orchestration/README.md](../orchestration/README.md)** - Orchestration module documentation
- **[ai/README.md](../ai/README.md)** - AI module with streaming support
- **[examples/travel-chat-agent/](../examples/travel-chat-agent/)** - The complete working example
- **[examples/devops-chat-agent/](../examples/devops-chat-agent/)** - Chat agent with shared memory and cross-agent coordination

Happy building! If something doesn't make sense, check the travel-chat-agent example - it's a working implementation of everything in this guide.
