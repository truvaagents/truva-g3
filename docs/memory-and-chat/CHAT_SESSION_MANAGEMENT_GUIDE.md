# Chat Session Management Guide

Hey there! This guide explains how TruvaG3's example chat agents keep a conversation going across multiple requests.

If you've ever wondered things like:

- "Where does the `session_id` come from?"
- "What exactly gets stored in Redis?"
- "How does the model see previous turns?"
- "What happens if a session expires?"
- "How does the DevOps agent resume the same chat after HITL approval?"

you are in the right place.

This guide is based on the real implementations in:

- [`examples/travel-chat-agent/`](../../examples/travel-chat-agent)
- [`examples/devops-chat-agent/`](../../examples/devops-chat-agent)

It is meant to be friendly to someone seeing this system for the first time, while still being precise enough for deeper debugging and design work.

## When To Read This Guide

Read this guide when your question is mostly about the **session lifecycle**:

- how sessions are created
- how they are stored
- how messages are appended
- how sessions are listed, renamed, and deleted
- how SSE requests create or resume sessions
- how HITL resume attempts to continue the same conversation in the DevOps example

If your question is instead:

- "How does TruvaG3 compact long chat history?"
- "What are Tier 1 and Tier 2 conversation-history protection?"
- "How does the framework prepare `<conversation_history>` for prompts?"

then the better guide is [`CONVERSATION_HISTORY_GUIDE.md`](CONVERSATION_HISTORY_GUIDE.md).

The short version is:

- this guide = where conversation history comes from and how the app maintains it
- `CONVERSATION_HISTORY_GUIDE.md` = how orchestration prepares that history for prompt use

## Why This Guide Exists

This repo already had good related documentation, but the story was spread across several places:

- [`CHAT_AGENT_GUIDE.md`](CHAT_AGENT_GUIDE.md) explains the overall chat-agent pattern
- [`CONVERSATION_HISTORY_GUIDE.md`](CONVERSATION_HISTORY_GUIDE.md) explains prompt-side history preparation and compaction
- [`AGENT_DEVELOPMENT_GUIDE.md`](../building/AGENT_DEVELOPMENT_GUIDE.md) summarizes the session-store API

What was missing was a single guide that says:

"Show me the whole thing, from HTTP request to Redis storage to orchestration context to follow-up turns."

That is what this guide is for.

## The Big Picture

Think of session management here as a three-part job:

1. The agent needs a place to **store the conversation**.
2. The streaming and query handlers need to **attach new messages to the right session**.
3. The orchestrator needs the stored messages converted into **prompt context** the model can understand.

Here is the full flow in one picture:

```text
Client
  |
  | POST /chat/stream  {session_id?, message}
  v
SSE Handler
  |
  | create session if needed
  | or load existing session
  | append current user message
  v
Redis Session Store
  |
  | return stored message history
  v
Chat Agent
  |
  | convert []Message -> []ConversationTurn
  | attach metadata:
  |   - conversation_turns
  |   - conversation_session_key
  v
Orchestrator
  |
  | prepare <conversation_history>
  | compact/budget if needed
  v
LLM + Tools
  |
  | produce response
  v
Chat Agent
  |
  | append assistant message back into session
  v
Redis Session Store
```

For the DevOps chat agent, there is one extra path:

```text
orchestration interrupted for approval
  |
  v
checkpoint stores session context
  |
  v
/hitl/resume/{checkpoint_id}
  |
  v
same session ID reused and the conversation resumed if that session still exists
```

## The Files We Will Keep Referring To

Both example agents follow the same design, so we mainly need to understand a handful of files:

- Session store:
  - [`examples/travel-chat-agent/session.go`](../../examples/travel-chat-agent/session.go)
  - [`examples/devops-chat-agent/session.go`](../../examples/devops-chat-agent/session.go)
- Session-related HTTP handlers:
  - [`examples/travel-chat-agent/handlers.go`](../../examples/travel-chat-agent/handlers.go)
  - [`examples/devops-chat-agent/handlers.go`](../../examples/devops-chat-agent/handlers.go)
- Streaming entrypoint:
  - [`examples/travel-chat-agent/sse_handler.go`](../../examples/travel-chat-agent/sse_handler.go)
  - [`examples/devops-chat-agent/sse_handler.go`](../../examples/devops-chat-agent/sse_handler.go)
- Orchestrator integration:
  - [`examples/travel-chat-agent/chat_agent.go`](../../examples/travel-chat-agent/chat_agent.go)
  - [`examples/devops-chat-agent/chat_agent.go`](../../examples/devops-chat-agent/chat_agent.go)
- DevOps-only HITL resume flow:
  - [`examples/devops-chat-agent/handlers_hitl.go`](../../examples/devops-chat-agent/handlers_hitl.go)

The framework-side history preparation path lives in:

- [`orchestration/pipeline_hooks.go`](../../orchestration/pipeline_hooks.go)
- [`orchestration/conversation_history_processor.go`](../../orchestration/conversation_history_processor.go)
- [`orchestration/metadata_keys.go`](../../orchestration/metadata_keys.go)
- [`core/interfaces.go`](../../core/interfaces.go)

## Start With The Short Answer

If you are skimming, this is the behavior today:

1. Both example agents store chat sessions in Redis DB 2 under the `truvag3:sessions` namespace.
2. A session holds metadata plus a bounded list of messages.
3. Sessions live for 48 hours and keep at most 50 messages.
4. `POST /chat/session` creates a session explicitly.
5. `POST /chat/stream` can also create a session automatically if no `session_id` is provided.
6. Each incoming user message is saved before orchestration begins.
7. The full retained message list is converted into `ConversationTurn` values and passed into orchestration metadata.
8. The orchestration layer prepares prompt-safe `conversation_history` from those turns.
9. The assistant response is saved back into the same session.
10. The DevOps example also carries session context across HITL interruption and resume.

If you want the deeper version, keep going.

## Step 1: Agent Startup Creates The Session Store

When either chat agent starts, it creates a Redis-backed `SessionStore`.

That happens in:

- [`examples/travel-chat-agent/chat_agent.go`](../../examples/travel-chat-agent/chat_agent.go)
- [`examples/devops-chat-agent/chat_agent.go`](../../examples/devops-chat-agent/chat_agent.go)

Both examples use the same constructor settings:

- Redis DB: `core.RedisDBSessions` which is DB 2
- Namespace: `truvag3:sessions`
- TTL: `48*time.Hour`
- Max messages: `50`

That choice matters for two reasons:

- sessions are isolated from service discovery, which uses Redis DB 0
- the examples intentionally keep chat history bounded in both time and size

If `REDIS_URL` is missing, startup fails. In these examples, session storage is a required part of the chat-agent design.

## Step 2: What A Session Looks Like

Each session record stores:

- `id`
- `user_id`
- `title`
- `created_at`
- `updated_at`
- `messages`
- `metadata`

Each message stores:

- `id`
- `role`
- `content`
- `timestamp`
- `metadata`

This is intentionally simple. A session is just a conversation folder, and `messages` is the transcript inside it.

## Step 3: Where The Data Lives In Redis

The implementation uses three kinds of Redis data.

### The session record

The main session object is saved under the session ID using the namespaced Redis client.

So conceptually it lives under a key like:

```text
truvag3:sessions:{session_id}
```

### The per-user session index

If a session has a `user_id`, the session ID is also added to a sorted set:

```text
truvag3:sessions:index:{user_id}
```

The score is the session's `UpdatedAt.UnixMilli()`.

That gives the app a clean way to list a user's sessions in most-recent-first order.

### The active session counter

The store also increments a simple Redis counter:

```text
truvag3:sessions:active_session_count
```

This is used by the health endpoint.

Advanced note:
This counter is incremented on explicit create and decremented on explicit delete, but Redis key expiry does not automatically decrement it. So it is useful operationally, but it is not a perfectly reconciled count of live TTL-valid sessions.

## Step 4: How A New Session Gets Created

There are two ways a session begins.

### Path A: explicit session creation

`POST /chat/session`

This path is great for UIs that want to create a chat first and then send messages later.

The handler:

1. reads `X-User-ID`
2. optionally decodes request metadata
3. calls `sessionStore.Create(userID, metadata)`
4. returns `session_id`, `created_at`, and `expires_at`

### Path B: implicit session creation during streaming

`POST /chat/stream`

If the request body does not include a `session_id`, the SSE handler creates a session on the fly and immediately emits an SSE `session` event back to the client.

That makes the frontend flow nice and simple:

- send a message
- receive a session ID
- keep using it for later turns

## Step 5: What Happens On A Streaming Chat Request

This is the path most people care about, so let's walk it slowly.

Imagine the client sends:

```json
{
  "session_id": "optional-existing-id",
  "message": "What changed in the cluster today?"
}
```

The SSE handler does the following:

### 1. Validate the request

It checks:

- method is `POST`
- body is valid JSON
- `message` is present

### 2. Resolve the session

If `session_id` is empty:

- create a new session
- send SSE event `session` with the new ID

If `session_id` is present but not found:

- create a replacement session
- send SSE event `session` with the new ID

This is a very important behavior difference from the non-streaming `/query` path:

- `/chat/stream` self-heals by creating a replacement session
- `/query` does not

### 3. Append the user message

Before orchestration even starts, the handler writes the incoming user message into the session.

That means the current user turn is already part of stored history when the agent later asks for session history.

### 4. Start orchestration

The handler calls `ProcessWithStreaming(ctx, sessionID, req.Message, callback)`.

That is where the session transcript becomes orchestration input.

## Step 6: How Stored Messages Become Model Context

This is the bridge between "session management" and "conversation history preparation."

Inside `ProcessWithStreaming()`, both agents do the same core work:

1. read the stored transcript with `sessionStore.GetHistory(sessionID)`
2. convert `[]Message` into `[]core.ConversationTurn`
3. attach those turns into metadata

The metadata keys are:

- `orchestration.MetadataConversationTurns`
- `orchestration.MetadataConversationSessionKey`

Both agents also populate:

- `core.EnrichmentConversationHistory`

as a formatted fallback string.

One subtle but important implementation detail:

- the conversion to `ConversationTurn` copies `Role` and `Content`
- it does not copy the original message timestamps or per-message metadata into the raw turn slice sent to orchestration

So the session record in Redis is richer than the turn list used for prompt history preparation.

Travel adds one more useful field when available:

- `user_id`

That lets its user-memory hooks know which user the session belongs to.

DevOps adds one more important piece before orchestration:

- request mode in context
- session metadata in context

That extra context is what allows HITL checkpoints to remember which session the request belonged to.

## Step 7: What The Orchestrator Does With That History

This is where [`CONVERSATION_HISTORY_GUIDE.md`](CONVERSATION_HISTORY_GUIDE.md) takes over in much more detail, but the important idea is simple.

The orchestrator sees metadata like:

- `conversation_turns`
- `conversation_session_key`

and feeds it into the shared `ConversationHistoryPreparer`.

Both example agents explicitly inject a preparer built with:

```go
BuildCompactionEnabledConversationHistoryPreparer(...)
```

So the framework does not just dump raw history into the prompt. It prepares it carefully:

- older turns can be compacted into a summary
- recent turns stay verbatim
- the whole block is kept within a token budget
- if still too large, older turns may be dropped, long turns may be elided, and the final text may be truncated

The result becomes the prompt enrichment that the model sees as conversation history.

That is the key separation of responsibilities:

- session store = where turns are persisted
- conversation-history processor = how retained turns are shaped for prompt use

## Step 8: How The Assistant Response Gets Saved

After orchestration finishes successfully, both agents append one more message to the same session:

- `role = "assistant"`
- `content = result.Response`
- `timestamp = time.Now()`

They also attach useful metadata such as:

- `request_id`
- `tools_used`
- `confidence`
- `duration_ms`
- token counts

The travel agent also stores `usage_by_phase`.

This means the next user turn sees both:

- what the user asked before
- what the assistant already answered

That is exactly what makes later follow-up requests feel coherent.

## Step 9: What Happens On The Next Turn

The next time the client sends a message with the same `session_id`, the cycle repeats:

1. load session
2. append new user message
3. read retained transcript
4. convert transcript to `ConversationTurn`s
5. prepare prompt-safe history
6. run orchestration
7. append assistant response

That is session continuity in these examples.

## Step 10: How Listing, Renaming, And Deleting Work

Besides streaming, both examples expose standard session-management endpoints.

### `GET /chat/session/{id}`

Returns session metadata:

- `session_id`
- `created_at`
- `updated_at`
- `message_count`
- `metadata`

This does not return the full transcript.

### `GET /chat/session/{id}/history`

Returns the current stored transcript:

- `session_id`
- `messages`
- `count`

### `GET /chat/sessions`

Lists sessions for a user and requires `X-User-ID`.

This uses the per-user sorted-set index and returns the most recently updated sessions first.

It also supports pagination:

- `offset` defaults to `0`
- `limit` defaults to `20`
- `limit` is capped at `50`

The store does one nice cleanup trick here:

- if the index still points at a session key that has already expired
- or if the session data is unreadable
- it removes that stale entry from the index asynchronously

That means list results stay healthy over time even though only the main session key has a TTL.

Each returned summary includes:

- `id`
- `title`
- `message_count`
- `created_at`
- `updated_at`
- `preview`

### `PUT /chat/session/{id}/title`

Updates the title and bumps `UpdatedAt`.

That matters because the session list ordering is based on update time.

### `POST /chat/session/delete`

Deletes the session record and removes the session ID from the user's index.

## Step 11: Titles, Previews, And Retention

There are a few nice usability details in the store.

### Auto-title behavior

The first user message becomes the session title if the title is still empty.

The implementation caps it at 60 characters:

- first 57 characters
- then `...`

### Preview behavior

The list preview comes from the first user message found in the transcript.

It is capped at 100 characters:

- first 97 characters
- then `...`

### Sliding window behavior

Every time a message is appended, the store trims the transcript to the most recent 50 messages.

That means these examples optimize for recent continuity, not permanent archival.

### TTL behavior

Every time the session is saved, Redis applies the configured TTL to the session key.

In practice, that means active conversations keep refreshing their session record while inactive sessions eventually age out.

## Step 12: What Happens When A Session Expires

This is another place where it helps to know the exact behavior.

If the session key expires:

- `Get(sessionID)` returns `nil`
- `GET /chat/session/{id}` returns 404
- `GET /chat/session/{id}/history` returns 404
- `PUT /chat/session/{id}/title` returns not found
- `POST /chat/session/delete` returns not found

For streaming:

- `POST /chat/stream` creates a replacement session and keeps going

For non-streaming `/query`:

- it does not create a replacement session
- the attempted message append simply fails quietly
- history lookup returns no turns
- orchestration still runs, but effectively without persisted session continuity

That distinction is worth remembering when debugging "why did this follow-up lose context?"

## Step 13: How The DevOps Agent Resumes The Same Chat After HITL

This is the main behavior the DevOps example has that the travel example does not.

When the DevOps chat agent enters orchestration, it stores session-related context before the request runs.

If orchestration gets interrupted for approval:

- the SSE flow emits a `checkpoint` event
- the checkpoint carries user context, including `session_id`

Later, when execution resumes through:

- `POST /hitl/resume/{checkpoint_id}`
- or `GET /hitl/auto-resume/{checkpoint_id}/stream`

the handler:

1. loads the checkpoint
2. rebuilds resume context
3. reads `session_id` from `checkpoint.UserContext`
4. reuses that session if it exists
5. creates a new session only if no session ID is available
6. calls `ProcessWithStreaming()` again using the original request

This is the crucial point:

the DevOps agent does not treat resume as a brand-new chat. It tries to continue the same conversation thread by reusing the checkpoint's `session_id`.

There is one important edge case to know:

- the resume handlers create a new session only when the checkpoint has no `session_id`
- if the checkpoint does have a `session_id` but that session record has already expired, the handler still passes that same ID into `ProcessWithStreaming()`
- in that case the resumed execution keeps orchestration context from the checkpoint, but it does not recreate the expired session record automatically

That is what keeps approval-driven workflows coherent from the user's point of view.

## Step 14: What About Agent-To-Agent Delegation?

Both examples also expose `/query` for non-streaming agent-to-agent use.

If a caller provides `session_id` and that session still exists:

- the handler tries to append the user turn
- `ProcessQuery()` loads session history
- the response is written back into the same session

If no `session_id` is provided:

- the request is stateless from the session-store point of view

If `session_id` is provided but the session no longer exists:

- `/query` does not create a replacement session
- history is effectively empty
- the orchestration still runs, but not with stored session continuity

So `/query` supports both:

- stateless delegation
- session-aware follow-up delegation

but it is stricter than `/chat/stream` about missing sessions.

## Advanced Notes And Gotchas

Senior developers usually want a few sharp edges called out explicitly, so here they are.

### The two examples duplicate the same session pattern

The code is nearly identical between travel and DevOps, but it is not yet extracted into one shared package.

So today this is a shared pattern, not a single reusable session library.

### Prompt compaction only sees the retained window

The conversation-history processor can compact older turns, but only from the turns the session store still retains.

That means:

- Redis sliding-window retention happens first
- prompt-side compaction happens second

So if a turn already fell out of the 50-message session window, orchestration cannot compact it because it no longer has it.

### Listing is user-scoped; direct lookup is session-ID-based

`GET /chat/sessions` is explicitly keyed by `X-User-ID`.

The direct session endpoints operate by session ID lookup. This guide is documenting current implementation behavior, not making a broader security claim.

### `active_session_count` is approximate

It is useful for health and visibility, but TTL expiry does not automatically decrement it.

If you ever need an exact count of currently existing session keys, you would need a different reconciliation strategy.

## A Good Mental Model To Keep

If you only want one sentence to carry around in your head, use this one:

A TruvaG3 chat session in these examples is a Redis-backed transcript that the agent updates on every turn, then converts into structured conversation history for orchestration.

And if you want the slightly longer version:

1. the app owns persistence, session IDs, TTLs, listing, and resume behavior
2. the orchestration layer owns prompt preparation, budgeting, and compaction
3. the two are connected by `conversation_turns` and `conversation_session_key`

That separation is the design.

## See Also

- [`CHAT_AGENT_GUIDE.md`](CHAT_AGENT_GUIDE.md)
- [`CONVERSATION_HISTORY_GUIDE.md`](CONVERSATION_HISTORY_GUIDE.md)
- [`AGENT_DEVELOPMENT_GUIDE.md`](../building/AGENT_DEVELOPMENT_GUIDE.md)
- [`examples/travel-chat-agent/README.md`](../../examples/travel-chat-agent/README.md)
