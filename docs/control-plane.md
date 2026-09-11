# Agent control plane (API v1)

The normative HTTP contract is [`api/openapi.yaml`](../api/openapi.yaml). The
WebSocket protocol, including examples and schema references, is in
[`api/websocket.md`](../api/websocket.md). This document explains runtime behavior.

## Ownership and deployment

`ted serve` is a single-user control plane owning every agent it creates. Its
working directories, tools, credentials, and files are on the **server**, not on
the connecting device. The TUI is an HTTP/WebSocket client of that service.

The default listener is **`0.0.0.0:8281`, without authentication or TLS**. Anyone
who can reach it can create agents capable of running shell commands and reading
or modifying files as the server user. Use only on a trusted network, restrict
access with a firewall, or put authentication and TLS in a reverse proxy. Binding
`--addr 127.0.0.1:8281` is available for local-only use. Browser Origin checks and
the absence of wildcard CORS reduce cross-origin browser access; they are not
access control for non-browser clients.

A plain `ted tui` checks `http://localhost:8281` and starts a background server if
none is available. The TUI that starts it owns its lifetime: closing or crashing
that TUI closes an ownership pipe, causing server shutdown. Closing a different
TUI only disconnects that client. Launch `ted serve` explicitly for a persistent,
multi-device control plane. `ted tui --server URL` never starts a fallback server.

The listener's bind is the final arbiter of overlapping starts. Independently
launched servers also take a storage lock so two ports cannot write the same
state directory. This is not a distributed/multi-process service.

## Projects

A project has a stable ID, display name, canonical absolute root directory, and
model/effort defaults. The directory must already exist on the server. A canonical
root identifies one project. Roots are immutable; names and defaults can change.
Only empty projects can be deleted, including checking for settled agents.

Each new chat snapshots project workspace defaults and can choose Current checkout
or Worktree before its first message. Current checkout uses the root as-is;
Worktree fetches the selected remote branch and provisions a clean linked
worktree outside the checkout. The first message permanently locks the choice,
including setup failures. See [chat workspaces](workspaces.md) for setup,
persistence, failure behavior, and terminal flags.
Multiple agents can edit the same files concurrently. Project defaults are copied
into new agents; changes to defaults do not alter existing agents. Agent creation
accepts overrides and an optional initial prompt. Without a prompt it is idle.

Creation also accepts an immutable `parent_agent_id`. Children can share an
established parent worktree while retaining the original project, independent
runtime identities, queues, and lifecycle controls. `working_directory` explicitly
selects a project root or established managed worktree and takes precedence over
implicit parent inheritance. See [child agents](workspaces.md#child-agents-and-existing-directories)
for CLI precedence and sidebar grouping.

## Execution and queue

Each agent has one FIFO queue and at most one active turn. HTTP and WebSocket
submission use the same durable transaction. A submission is acknowledged before
completion, only after its queued record and event have been persisted. Creation
and submission accept scoped idempotency keys: reuse with a different payload is
a conflict. Retrying an accepted request does not implicitly continue held work.

The message ID also identifies its turn. Queue inspection includes both pending
and terminal records. Deleting a pending message marks it cancelled rather than
erasing its audit record or idempotency receipt.

| Transition | Queue behavior |
| --- | --- |
| Successful turn | Start next pending message |
| Stop a specified active turn | Cancel it, wait for its tools/provider to stop, then start next pending message |
| Failed turn | Hold pending work |
| Crash or shutdown during a turn | Mark interrupted; hold pending work; never automatically retry that turn |
| Continue | Release held work; run next pending message, not the failed/interrupted message |
| Submit a new message to an unsettled agent | Append it and implicitly release held work in original FIFO order |
| Settle | Prevent new starts, cancel active work, preserve and hold pending work |
| Unsettle | Restore visibility; leave work held until Continue or new input |

There is **no Pause action**. Stop requires `turn_id` so a retried stop cannot
cancel a subsequent turn. If the specified turn is already terminal it is a
no-op. A pending message is removed with the pending-message endpoint, not Stop.
Messages and Continue are rejected for settled agents until explicitly restored.

Stopping does not undo filesystem or external side effects. Cancellation is
propagated through provider HTTP requests, retries, and bash process groups on
Unix. Tools that deliberately detach from their process group are outside the
normal cancellation guarantee. The API reports `stopping` while cancellation is
in progress and never overlaps it with the next turn.

Model and effort updates change **desired settings for the next turn**. An active
turn retains the settings captured at its start (`active_settings`). Unsupported
values are rejected. Model switches retain compatible effort or select the new
model's default, matching the existing model-selection behavior.

## Settling and shutdown

Settled is an archive/visibility flag, independent of execution state. Default
lists omit settled agents; explicit inspection and historical subscriptions work.
Settling is durable before active cancellation is requested. It may return a
settled agent whose execution state is still `stopping`.

Server shutdown blocks starts globally, cancels active turns without ordinary
Stop's queue-advance behavior, persists pending work, and waits for bounded
cleanup. A killed server leaves a durable running reservation; recovery converts
it into an interrupted turn. Already accepted pending work remains durable.

Ordinary failed turns retain all emitted events, although their model-facing
conversation rolls back to the last valid checkpoint. Cancelled turns retain a
valid partial conversation with a result for every issued tool call, including
cancelled results for calls that never executed. Neither behavior undoes tools.

## Events and history

Clients may multiplex explicit agents or subscribe to **all unsettled agents
across all projects**, including future agents. The inventory, replay, and live
change notification are captured atomically so concurrent creation cannot be
missed. Settling/restoring is delivered to other clients without polling.

Each agent has a monotonically increasing cursor. Supply the last consumed
cursor, not an inventory snapshot's cursor, to replay missed events. There is no
global ordering across agents. Clients deduplicate by `(agent_id, cursor)`.
Invalid/unknown cursors produce explicit errors; the server never silently skips
history. Slow WebSocket consumers are disconnected and can resume from cursors.

Events contain completed assistant blocks, tool-call notices, complete tool
outputs, queue transitions, and state updates. There are no token deltas. The
model-facing transcript is available separately through paginated HTTP history.
`conversation` events carry newly committed message blocks, not a replacement
for all previous history. Output emitted during failed work remains available in
the lifetime event log even when absent from model-facing history.

All completed tool output is retained in `tool_result` events; provider context
keeps its existing 64 KiB output cap. WebSocket events larger than 64 KiB are sent
as a same-server HTTP event reference. Tool output is UTF-8 text, with invalid
bytes replaced, rather than a binary byte archive.

## Durability and operational bounds

State is stored under `$TED_HOME/controlplane` (default
`~/.ted/controlplane`), configurable with `ted serve --data-dir`. Runtime browser
artifacts are isolated under that directory's `runtime` subdirectory. Credentials
are not stored in the control-plane snapshot. Transcripts and outputs may contain
secrets, however: **the state files are private-permission plaintext**.

The initial storage implementation keeps lifetime events and queue receipts in a
single atomically replaced, synced JSON checkpoint. It favors a simple coherent
transaction over large-scale throughput: state is loaded in memory and checkpoint
cost increases with retained history. Full tool output is buffered until tool
completion. There is no automatic pruning, compression, or per-project quota.
Plan disk and memory accordingly; this is not yet a high-volume hosted backend.
Storage errors fail closed: new work is rejected and active work is cancelled
rather than acknowledging non-durable mutations.

No legacy session migration is performed. The embedded `agent` package can still
use its standalone persistence API, but control-plane agents have their own store.

## Contract development

```sh
go generate ./api
./api/check-generated.sh
go test ./...
go test -race ./...
```

OpenAPI generates checked-in Go server types, interfaces, route wrappers, and an
embedded spec. The HTTP adapter actually uses those generated routes and validates
requests against that same document. Shared WebSocket payload schemas are kept
in the contract and validated at runtime. No public client SDK is generated yet;
the TUI currently uses a small handwritten wire adapter.
