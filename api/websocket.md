# Ted v1 multiplexed WebSocket protocol

Connect to `GET /v1/ws` using RFC 6455. One connection can observe and submit to
many agents in many projects. No SDK is required. There is no authentication;
expose only on a trusted interface or behind an authenticated reverse proxy.
Browser `Origin` must match the request's scheme and host (including port).
Non-browser clients may omit `Origin`. No wildcard CORS is enabled.

The normative payload schemas are `WSSubscribe`, `WSSubmit`, `WSSubscribed`,
`WSInventory`, `WSEvent`, `WSEventReference`, `WSAck`, and `WSError` in
[`openapi.yaml`](openapi.yaml). The server validates client frames against these
same embedded schemas. HTTP and WS share `Settings`, `AgentSummary`, `Event`, and
message schemas. Each application message is a single UTF-8 JSON text message.
Unknown fields, missing required fields, nulls, invalid types, and out-of-bound
values are rejected with an `error` frame; binary messages disconnect.

## Subscribe / replace subscriptions

```json
{"type":"subscribe","request_id":"sub-1","subscribe_all":true,"agent_ids":[],"cursors":{"agent-a":42}}
```

- `type` and nonempty `request_id` (at most 256 characters) are required.
- `subscribe_all` defaults to false. When true, observe **all unsettled agents
  across all projects**, including projects and agents created in the future.
- `agent_ids` defaults to empty; these explicit subscriptions also include
  settled agents. This selection is unioned with `subscribe_all`.
- `cursors` maps agent IDs to the last fully processed event cursor. Values are
  nonnegative int64 integers. Omitted agent cursors start at zero, replaying the
  agent's entire lifetime. Each agent has its own independent contiguous log.
- Cursor keys must belong to explicit subscriptions, unless `subscribe_all` is
  true. In all mode, resumed cursor keys are inventoried and replayed even if
  the agent settled while the client was offline; this flushes terminal events.
- Unknown cursor agent IDs or cursors beyond the log produce an explicit
  `cursor_invalid` error (HTTP equivalent: 410), **never a silent skip/reset**.
  Explicit unknown agent IDs produce `not_found`. A malformed cursor produces
  `invalid`. Invalid replacement subscriptions leave the previous subscription
  unchanged.
- A successful command **replaces** the previous subscription set and starts
  with `{"type":"subscribed","request_id":"sub-1"}`. An empty selection
  unsubscribes everything. Include saved cursors when replacing subscriptions
  if replay is not desired. Reusing `request_id` does not deduplicate subscribe.

Inventory, replay and the next-change wakeup are captured atomically by runtime
`SnapshotEvents`. The server first sends an `inventory` frame for each agent,
then events strictly after the requested cursor. Changes during transmission
close the captured wakeup; the next snapshot catches them. Thus there is no gap
between initial inventory, replay and live observation. There is **no global
ordering across agents**; each agent's events are in ascending cursor order.

```json
{"type":"inventory","agent":{"id":"agent-a","project_id":"project-p","title":"Fix tests","settings":{"model":"provider/model","effort":"high"},"settled":false,"state":"idle","held":false,"cursor":43,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:01Z"}}
{"type":"event","event":{"agent_id":"agent-a","cursor":43,"type":"agent.updated","data":{},"created_at":"2026-01-01T00:00:01Z"}}
```

Inventory is a **current** summary, not an event-time snapshot; it omits queue and
conversation history. `inventory.agent.cursor` is a snapshot high-water mark,
**not a processed-event acknowledgement**: never advance your resume cursor from
inventory. Inventory updates are sent only when its summary changes. Cursors
are tracked per connection so unchanged events/history are not retransmitted on
each runtime change. A `conversation` event contains only newly appended committed messages
(a delta, not a full checkpoint); fetch `/history` for the full committed
conversation rather than interpreting inventory as history. Runtime event `data` remains lossless and extensible.

In all mode, an observed agent that settles remains observed while its state is
`stopping`, including any last tool output and conversation delta. Only after
its state becomes `idle` and all terminal events are sent does it stop being
observed, unless explicitly selected. Its processed
cursor is remembered on the connection; if restored to unsettled, only newer
events are sent. An all-mode client reconnecting should send cursors for every
previously observed agent to catch offline settlements.

## Submit

```json
{"type":"submit","request_id":"send-12","agent_id":"agent-a","idempotency_key":"durable-unique-key","text":"Please fix the failing tests"}
```

All fields are required. `request_id` correlates the response only; it is not an
idempotency key. `idempotency_key` (1–256 characters) deduplicates durably in the
same per-agent namespace as HTTP `Idempotency-Key`. Retry the **same text and
key**, with any request ID, after a disconnect. A different text with that key
returns `idempotency_conflict` (HTTP 409). Submission needs no subscription.

```json
{"type":"ack","request_id":"send-12","agent_id":"agent-a","message_id":"turn-m","status":"pending"}
{"type":"error","request_id":"send-12","code":"settled","message":"restore agent before submitting messages"}
```

An ack means the queue entry was durably accepted, not that execution completed.
Observe events or HTTP queue inspection for the outcome. `message_id` is also
the `turn_id` required by HTTP `POST /v1/agents/{id}/stop`. Retrying a completed
submission returns the same message ID and may report its current terminal
status. `ack` deliberately does not echo arbitrarily long message text.

## Replay, output size, and backpressure

Event logs are retained for the lifetime of the durable agent, across client
and server restarts; disconnecting does not cancel a turn. Persist cursors only
after processing the event (or its reference). Resume using a fresh subscribe
command. Duplicate processing across a disconnect is possible; deduplicate by
`(agent_id, cursor)`. HTTP `/events?after=42&limit=100` exposes the same log.

Client messages and server messages are bounded to **65,536 bytes (64 KiB)**,
including the JSON envelope. Oversized incoming messages disconnect (1009).
To submit longer text, use HTTP (text up to 1,048,576 Unicode characters, entire
request up to 2 MiB). A retained event whose serialized WS envelope exceeds
64 KiB is delivered as a reference, never truncated:

```json
{"type":"event_ref","agent_id":"agent-a","cursor":44,"url":"/v1/agents/agent-a/events/44"}
```

Fetch that relative URL from the same HTTP server to obtain the **complete
Event**, including uncapped tool output. This applies to every large event type,
including output and conversation deltas. Advance the cursor only after
the referenced event is fetched and processed. HTTP responses are not capped at
64 KiB. References work for the agent's entire retained lifetime.

The server does not maintain an unbounded per-client outgoing queue. Every data
write has a **5 second deadline**; slow consumers disconnect and resume with
cursors. One reader and one writer per connection serialize messages. The
server pings every 20 seconds, expects a pong within 60 seconds, and does not
negotiate compression. Standard WebSocket clients should automatically answer
pings while reading. An error without `request_id` is a stream-level error;
reconnect only after resolving it. Server shutdown/process loss closes the
transport without erasing replay state.

## Error codes

HTTP errors use `{"error":{"code":"…","message":"…"}}`; WebSocket errors
use `{"type":"error","request_id":"…","code":"…","message":"…"}`.
See the OpenAPI error schema and [`README.md`](README.md) for status/code mapping.
Schema failures do not mutate runtime state. Messages are diagnostic text, not
stable identifiers; branch on `code` instead.

## Typed event payload mapping

The Event schema's `x-event-data-schemas` extension records this mapping, and
`EventData` is a generated union of these named schemas:

| Event type | `data` schema | Meaning |
| --- | --- | --- |
| `agent.created`, `agent.updated` | `AgentUpdate` | Settings/state/settled/usage metadata. Cursor and event timestamp are in the outer Event; these partial updates contain neither history nor queue. |
| `message.queued`, `message.cancelled` | `QueuedMessage` | Durable queue acceptance or pending deletion. |
| `turn.started`, `turn.completed`, `turn.failed`, `turn.interrupted`, `turn.cancelled` | `QueuedMessage` | Turn queue entry and its status; its `id` is the HTTP stop `turn_id`. |
| `output` | `AgentOutput` | Existing runtime fields `Content`, `ResponseType`, `ToolCallID`, `ToolName`, `FullToolOutput` (intentionally PascalCase). Response types are `agent`, `tool`, `tool_result`, `status`, `usage`. |
| `conversation` | `ConversationDelta` (`Message[]`) | New committed messages appended since the previous delta. HTTP history is the full current conversation. |

`Message` explicitly includes `role`, `content`, `reasoning`,
`reasoning_details`, `tool_calls`, `tool_call_id` and `source_model`.
`MessageContent` is a string, multimodal `ContentPart[]`, or null. `ToolCall`
contains `id`, `type`, `index`, and `FunctionCall` (`name`, JSON-encoded
`arguments`). Unknown provider content/reasoning fields remain extensible to
preserve opaque signatures and encrypted reasoning records. `source_model`
retains provenance needed to replay that reasoning to the original model.
`AgentUpdate.context_usage` uses the same snake_case `ContextUsage` schema as
HTTP and inventory; `AgentOutput` alone preserves historical PascalCase keys.
