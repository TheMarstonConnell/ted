# Spec-first control-plane API

[`openapi.yaml`](openapi.yaml) is the source of truth for Ted API v1. Generated
Go models, the server interface, **actual HTTP routes**, and the embedded
validation document are in `generated.go`. `controlplane.NewHandler` implements
the generated interface with the durable runtime adapter; it does not maintain
a second handwritten route table. Both HTTP and WS requests are validated
against the embedded OpenAPI schemas before calling runtime methods.

## Generate and verify

```sh
go generate ./api          # oapi-codegen v2.5.1, pinned in generate.go
./api/check-generated.sh   # regenerate in isolation and compare exact bytes
go test ./api ./controlplane
go test -race ./controlplane
```

`go test ./api` also executes the drift check, so normal CI rejects stale
committed generated types, routes, or embedded schemas. It requires Go and bash;
the pinned generator is fetched through Go modules on first use. No SDK is
generated. Edit the YAML, then regenerate; never edit `generated.go` manually.

## Resource endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | `{ "api_version": "1" }` |
| GET, POST | `/v1/projects` | List or create projects |
| GET, PATCH, DELETE | `/v1/projects/{project_id}` | Read, update name/defaults, delete an empty project |
| GET, POST | `/v1/agents` | List or create durable agents |
| GET, PATCH | `/v1/agents/{agent_id}` | Read agent or update required `settled` boolean |
| PATCH | `/v1/agents/{agent_id}/settings` | Patch model and/or effort for future turns |
| GET, POST | `/v1/agents/{agent_id}/messages` | Inspect queue (including terminal entries) or submit text |
| DELETE | `/v1/agents/{agent_id}/messages/{message_id}` | Cancel a pending entry only |
| POST | `/v1/agents/{agent_id}/stop` | Stop required `turn_id`; advance pending FIFO, safe to retry |
| POST | `/v1/agents/{agent_id}/continue` | Release failure/interruption hold; no body |
| GET | `/v1/agents/{agent_id}/history` | Paginated committed conversation |
| GET | `/v1/agents/{agent_id}/events` | Paginated lifetime event log |
| GET | `/v1/agents/{agent_id}/events/{cursor}` | Complete single retained event, including large tool output |
| GET | `/v1/models` | Provider/model capability catalog |
| GET | `/v1/ws` | Multiplexed WebSocket; see [protocol](websocket.md) |

JSON request objects reject unknown properties, null where not allowed, and
missing required fields. Empty patches are rejected. Unknown/repeated query
parameters, malformed escapes, trailing JSON, and bodies on bodyless operations
are rejected. JSON operations require `Content-Type: application/json`; entire
request bodies are limited to **2 MiB**, and text to **1,048,576 characters**.
Names are limited to 256 characters, roots to 4096, model identifiers to 256,
effort strings to 64, and idempotency keys to 256. Root paths must be existing
absolute server-local directories. Model/effort availability is checked against
the runtime provider catalog. See YAML for every request field and bound.

Project creation requires `name`, `root`, and complete `defaults` (`model` and
`effort`; an empty effort selects the model's default). Project patches replace
complete defaults for **future agents only**. Agent creation requires
`project_id`; optional `title`, nonempty `prompt`, and partial settings are
accepted. Omitting settings inherits project defaults. A supplied model resets
the default effort for that model unless an effort is also supplied. Settings
changes do not alter a running turn's captured `active_settings`.

Agent lists default to unsettled only; set `include_settled=true` to inspect all,
and optionally filter by `project_id`. Settling a running agent initiates
cancellation; the final output and conversation still remain replayable.
Settled agents must be restored with `PATCH {"settled":false}` before continuing
or submitting. Deleting projects with any agents (including settled) returns
409; there is no destructive agent/log deletion endpoint.

HTTP create-agent and submit-message operations accept optional
`Idempotency-Key`. A key is global for agent creation, per agent for messages,
and retained durably across retries/restarts. Reusing it with a different
semantic request yields 409. HTTP and WS submissions share the message-key
namespace. A submission response is acceptance, not successful execution.

## Pagination

History and events take `after` (default 0, nonnegative int64) and `limit`
(default 100, range 1–1000). Responses contain `messages` or `events`, plus
`next_cursor`; an empty page leaves the cursor unchanged. Iterate until an
empty page to catch up. Events use exclusive lifetime per-agent cursors.
History uses the count of messages already consumed (an index, not an event
cursor); conversation compaction may invalidate history cursors, so durable
streaming clients should use the event log. Ahead-of-log/history cursors return
410; none are silently skipped or clamped. An event's complete payload can be
fetched individually even when its WS representation exceeds 64 KiB.

## Error contract

All HTTP errors use the generated `Error` envelope:

```json
{"error":{"code":"cursor_invalid","message":"cursor is beyond retained history"}}
```

| HTTP status | Stable codes | Meaning |
| --- | --- | --- |
| 400 | `invalid`, `invalid_project`, `invalid_settings`, `invalid_message`, `invalid_limit`, `turn_required` | Schema/parameter/body violation, invalid directory/catalog setting/text, or missing turn identity |
| 403 | `forbidden` | Browser WebSocket Origin mismatch |
| 404 | `not_found` | Unknown resource, route or event |
| 405 | `method_not_allowed` | Method unsupported for route; `Allow` lists methods |
| 409 | `conflict`, `project_exists`, `project_not_empty`, `idempotency_conflict`, `not_pending`, `not_running`, `settled` | Runtime state/precondition conflict |
| 410 | `cursor_invalid` | Invalid cursor; do not silently reset |
| 413 | `too_large` | HTTP request body exceeds 2 MiB |
| 415 | `unsupported_media_type` | Expected JSON Content-Type |
| 500 | `internal` | Unexpected failure; internal details are not exposed |
| 503 | `shutting_down`, `storage_failed` | Runtime unavailable; failed mutations are not acknowledged as successful |

WS error frames share `ErrorCode` and add optional `request_id`; see the protocol.
Some codes are reserved generic categories, while runtime errors are more
specific. All errors are declared on their OpenAPI operations. Error messages
are diagnostic, not stable machine identifiers.

## Security and lifecycle

This is an **unauthenticated control API capable of executing tools**, binding
to **`0.0.0.0:8281` by default**. Use only on a trusted network; bind to loopback
or protect it with an authenticating TLS gateway when appropriate.
No wildcard CORS is enabled. Browser WS handshakes require the same Origin;
non-browser clients may omit Origin. An ordinary client disconnect does not end an agent turn. Exiting/crashing the
TUI that auto-started a server does intentionally shut down that entire server. Slow WS writes time out after 5 seconds; clients resume from
persisted event cursors. Output remains fully retained on HTTP, not truncated
to satisfy WebSocket transport bounds.
