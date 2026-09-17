# Live browser protocol

`GET /v1/agents/{agent_id}/browser` upgrades to a dedicated WebSocket. It is
independent of `/v1/ws` and carries ephemeral browser state, not agent event-log
cursors. The operation, command/event models, and HTTP route are generated from
[`openapi.yaml`](openapi.yaml).

The server resolves the durable agent's ID and project. Browser identity uses
`browser.ProjectRoot(project.Root)`, matching runtime `SetIdentity` →
`TED_PROJECT_ROOT` → `ted browser`, **not the workspace working directory**.
A managed-worktree agent and a child sharing that worktree still use their
project's browser profile with distinct thread IDs. The connector uses the
service's `runtime` home, matching the agent tool's pinned `TED_HOME`, rather
than the server process environment. Clients cannot supply a
project, thread, socket, home, or filesystem path. The endpoint has no query
parameters and accepts no HTTP body.

Subscribing may start the local browser daemon, but does not start Chrome,
create a session, open a tab, or change agent selection. An empty browser is a
normal state. Explicit `new` or initial `navigate` opens it. The browser is
shared and interactive: human commands do not pause, cancel, or take over agent
work, and do not enter the agent's message queue.

## Handshake and security

By default, browser `Origin` must exactly match the request's scheme and host (case-insensitive
host); malformed, repeated, null, and cross-origin values are rejected with
HTTP 403. Non-browser clients may omit Origin. Forwarded-origin headers are not
trusted. For a TLS-terminating authenticated proxy, set
`ted serve --public-origin https://ted.example.com` to pin the expected browser
Origin independently of the backend HTTP transport. Preserve the browser's Origin
when proxying; other origins are still rejected. This configuration also applies
to `/v1/ws` and does not add authentication or trust forwarded headers.

Unknown agents return 404, unavailable project directories return 409
`workspace_unavailable`, daemon connection failures return 503
`browser_unavailable`, and viewer limits return 503 `browser_limit`. Other HTTP
validation and error envelopes follow the [API contract](README.md).

This is the same unauthenticated trusted-network control API as the rest of Ted.
Same-origin protection is not authentication. Navigation includes server-local
`file:` URLs just as the agent browser does; do not expose the server to untrusted
users or networks.

## Client messages

Send one JSON object per **text** WebSocket message, with a `type` and only the
fields applicable to that command. Unknown properties, duplicate keys, nulls,
wrong scalar types, trailing JSON, and arbitrary CDP methods are rejected.

| Type | Fields |
| --- | --- |
| `watch` | Optional `tab_id`; omitted/empty follows agent selection, nonempty pins the viewer |
| `new` | Optional `url`; no `tab_id` |
| `navigate` | Required `url`; optional `tab_id` only for initial navigation when no tabs exist |
| `back`, `forward`, `reload`, `close` | Required nonempty `tab_id` |
| `mouse` | Required `tab_id`, `event`, `x`, `y`; optional `button`, `buttons`, `click_count`, `delta_x`, `delta_y`, `modifiers` |
| `key` | Required `tab_id`, `event`, and `key` or `code`; optional `text`, `key_code`, `modifiers` |
| `text` | Required `tab_id`, `text` (paste or explicit text insertion) |
| `release` | Required `tab_id`; release this viewer's held keys/buttons |

Mouse events: `mouseMoved`, `mousePressed`, `mouseReleased`, `mouseWheel`.
Press/release requires `button` of `left`, `middle`, or `right`; other events can
use `none`. Key events: `keyDown`, `keyUp`. Coordinates are **CSS viewport
pixels**, not displayed-image or JPEG pixels. `buttons` is the CDP bit mask
(left=1, right=2, middle=4). Modifiers use Alt=1, Control=2, Meta=4, Shift=8.
Printable key-down events may carry `text`; `text` commands support paste.
Release on focus loss, Escape, or panel closure; disconnect also releases the
viewer's tracked input.

Example:

```json
{"type":"watch"}
{"type":"new","url":"https://example.com"}
{"type":"watch","tab_id":"tab-123"}
{"type":"mouse","tab_id":"tab-123","event":"mouseMoved","x":140,"y":90}
{"type":"mouse","tab_id":"tab-123","event":"mousePressed","x":140,"y":90,"button":"left","buttons":1,"click_count":1}
{"type":"mouse","tab_id":"tab-123","event":"mouseReleased","x":140,"y":90,"button":"left","buttons":0,"click_count":1}
{"type":"text","tab_id":"tab-123","text":"Hello"}
{"type":"release","tab_id":"tab-123"}
```

Bounds: 64 KiB per incoming message; tab IDs ≤256 bytes, URLs ≤8192 characters,
key/code ≤128 bytes, text ≤16384 characters. Coordinates range 0–100000 and wheel deltas
−100000–100000; all numbers must be finite. Integer bounds: buttons 0–7,
click count 0–3, modifiers 0–15, key code 0–65535. URL schemes are `http`,
`https`, `file`, `about`, and `data`. Daemon-side tab ownership and state
validation remain authoritative. Viewer pinning never changes agent selection;
commands for a closed/foreign tab return an error rather than silently targeting
another tab. Cleanup `release` is idempotent for closed tabs and never retargets
another tab. Neither API paths nor telemetry accept arbitrary CDP execution.

## Server messages

Messages follow `BrowserLiveEvent`:

- `state`: optional `tabs` (`{id,url,title}`), `selected` (agent selection), and
  `tab_id` (the viewer's current tab), and `pinned` (the authoritative viewer pin;
  empty/omitted means follow agent selection). Missing tabs means an empty list.
  A human `new` automatically pins its new tab. Use `pinned` rather than guessing
  from new tab IDs: the agent can create tabs concurrently. State refreshes
  periodically (approximately one second) and on changes.
- `frame`: `tab_id`, base64 JPEG `data` (no data-URL prefix), and CSS viewport
  `width`/`height`. Frame and state delivery are independently buffered: a frame
  can arrive before the state that names its tab. Retain at most one such early
  frame and apply it when matching state arrives; do not display it on another
  tab. A same-tab URL/title update must not clear the last image: a static page
  may not produce another frame. Tab switches clear the displayed old-tab frame.
- `activity`: `tab_id`, `kind` (`move`, `click`, `fill`, `clear`), and optional
  CSS `x`/`y`. Only agent actions produce activity; typed content is never
  included. The UI fades indicators after approximately 1.5 seconds and clears
  them on navigation/tab changes. Activity is not a takeover indicator.
- `error`: stable `code` and diagnostic `message`. Validation errors use
  `invalid`; daemon errors (for example an unavailable tab) retain their code.
  There are no success acknowledgements or request IDs: state/frame changes
  reflect successful interaction.

## Flow control and lifecycle

A handler allows at most 32 active viewers, at most four per agent. Incoming
commands have a 32-entry queue and a 120-message/second token bucket with a
240-message burst. Frontends should coalesce hover and wheel input. Overflow
returns `backpressure` or `rate_limit` and closes with policy violation (1008).
Eight invalid commands also close with 1008; earlier invalid commands produce
recoverable error events. Binary messages close with 1003; oversized input
closes with 1009.

The bridge retains only the latest pending frame, plus a bounded 32-entry
state/activity/error queue. It never accumulates frame history. Reliable-event
queue overflow closes with `slow_consumer`. Outgoing messages are capped at
4 MiB. Writes time out after five seconds. WebSocket ping frames are sent every
20 seconds and pongs must arrive within 60 seconds. These are transport control
frames, not JSON commands.

Daemon transport failure emits `browser_unavailable` and closes with 1011.
Server shutdown or agent deletion ends the subscription, cancels daemon I/O,
and closes the connection. An ordinary disconnect closes only that viewer's
subscription, not the browser tabs or agent turn. Reconnect with a fresh socket
and resend the desired `watch`; there is no durable frame/activity replay.
Streaming shares the tab's screencast with recording rather than taking over or
stopping an existing recording.
