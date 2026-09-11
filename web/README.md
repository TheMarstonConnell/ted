# Ted web control plane

React + TypeScript, Vite, Tailwind CSS v4, and shadcn/ui (Base UI), including the
[Message Scroller](https://ui.shadcn.com/docs/components/base/message-scroller).
Connects to one same-origin Ted server. There is no separate frontend login or
browser-side provider configuration.

## Interface conventions

Use the shared shadcn/ui `base-nova` primitives and neutral theme, standardized
by [the web design system](DESIGN_SYSTEM.md): an 8px layout grid, layered
24/16/8px insets, and locally bundled Inter / IBM Plex Mono. Tailwind utilities
handle layout and Markdown typography; `src/index.css` owns tokens, base styles,
touch targets, shared scrollbar alignment, and reduced-motion support. Avoid
decorative badges, marketing copy, and duplicate metadata in chat chrome.
Model and effort controls sit on the left of the input’s bottom toolbar, using
natural widths. Their shadcn Select menus open upward in the composer (with
selected-item/trigger overlap disabled). Project-default dialogs use the same selects
with normal downward positioning. Send, Stop, and Continue have a separate,
non-shrinking area on the right. Settings wrap on narrow screens without
crowding the action buttons. The message field starts at 64px tall on desktop
(48px on mobile), grows with the draft up to 208px, then scrolls without covering
the toolbar. On narrow screens, its growth is also capped at 25dvh to leave room
for controls in short viewports. Model/effort changes save immediately for the next turn.

Context is read-only, muted monospace text in the footer below the input on every
screen, matching the workspace mode metadata. It is not clickable or focusable.
Model and effort are edited only in the composer toolbar for existing chats.
There is no separate agent settings dialog; old `/agents/:id?panel=settings`
links render the normal chat without opening an overlay. Project defaults remain
available from the sidebar and apply only to new chats. Footer content aligns
with the input text area, not the outer input border. Mobile shows only its
percentage (or “0%” when usage is unavailable), while desktop retains the Context label. On mobile, the footer has
workspace controls or locked mode text and the Git branch on the left, and context
plus the sidebar menu on the right; the file path is not displayed. Desktop shows workspace controls (or locked mode text) on the left, with context
on the right. The path remains in the locked text’s title. The
context readout and mobile menu remain available for chats without a project.
Queued-message cancellation stays beside each queued message. The mobile drawer
opens with a subtle 200ms slide-up/fade and respects reduced-motion preferences.
Closing or handing off to another dialog still unmounts it immediately, so modal
focus/interaction locks are released.
Running, held, settled, and connection states remain visible
where they affect the current task. User messages use right-aligned neutral
bubbles; assistant replies stay unboxed. Sender names are accessible labels rather
than visible headings. Messages show only their body, with no timestamp or copy
button footer; normal text selection/copy remains available. Working/stopping status uses the same type size and line
height as message body text. Consecutive tool entries are separated by 8px;
all other transcript boundaries retain the normal 24px spacing. Tool calls show a spinner instead of a chevron while waiting for output and
cannot be expanded yet. Once output arrives (even an empty result), the spinner
becomes the normal expand chevron. This does not imply success or failure.
Reduced motion keeps the loading icon static. Tool headers
use 16px horizontal / 8px vertical padding; expanded output uses 16px padding
and compact 12px/20px monospace text. Chat images have a subtle theme-aware border and a drop shadow with 16px of surrounding padding to prevent clipping at
message edges, and open in a near-full-window, aspect-ratio-preserving lightbox
with a dark backdrop.
Escape, the close button, or the space outside the image dismisses the preview
and restores focus to its thumbnail. Linked images open the preview rather than
a new browser tab; normal text links retain their behavior.

The selected sidebar chat uses a high-contrast neutral fill and semibold title;
its branch remains secondary but readable. Other chat titles use medium weight.
Project headings use a folder icon and semibold text, with a trailing expand
chevron. Their settings action uses the same row-scoped hover/focus reveal as
chat settle/restore actions: no reserved width on desktop until revealed, and
always visible with a 48px target on touch devices. Running/Held/Stopping remain plain status text, not additional badges.
Selected settled chats retain their selection contrast instead of fading out.

## Chat workspace footer

The composer footer's left-hand directory slot now holds the workspace choice.
Draft chats use compact, borderless **Local / Worktree** controls;
Worktree exposes a **Start from** remote-branch picker alongside it. These stay
below the message input, not in a separate settings row above the composer.
After first send the slot becomes non-interactive **Local** or
**Worktree** text. No disabled select, directory string, or copy button is shown there. The Git
branch sits beside the workspace: Local uses the live project branch, while
Worktree uses the thread’s recorded branch. Before worktree setup, the starting-
branch picker occupies that branch slot instead. Thin, muted vertical dividers
separate workspace, branch, context, and mobile navigation; absent items do not
leave extra dividers. Long branches truncate with their full value in a tooltip,
and the draft controls can wrap on very narrow screens. The path remains available
as a tooltip; the sidebar also retains the generated worktree branch. This applies to desktop and mobile, with
context and the mobile navigation button retained on the right. Project-default
forms keep their normal labeled controls.

## iPhone checks

The normal browser suite includes iPhone-sized touch contexts in Chromium.
For WebKit coverage, install the browser and its system dependencies, then run:

```sh
cd web
npx playwright install --with-deps webkit
npm run test:iphone
```

`WEBKIT_PATH` can override the executable for a locally provisioned WebKit build.
The focused suite checks light/dark selection contrast, tool spinner-to-chevron
behavior, 48px tool targets, 16px input text, short viewport containment, and
project-dialog focus/draft preservation, and send/scroll regressions (including
queued turns, slow/failed sends, and chat switches). It also records screenshots when run
with `TED_WEB_RECORD=1`. This is browser/device emulation: verify actual iOS
keyboard occlusion, Safari toolbar resizing, and safe areas on a real iPhone.

## Run

From the repository root:

```sh
go run . serve --addr 127.0.0.1:8281
# Open http://localhost:8281
```

`ted serve` embeds `web/dist` at compile time. The bundle is not checked in, so
run `npm ci && npm run build` in `web/` before `go build`, `go install`, or
`go test`. An already running Go binary must be rebuilt and restarted to pick up
a new bundle.

For development (Node 22.12+):

```sh
# Terminal 1, repo root
go run . serve --addr 127.0.0.1:8281

# Terminal 2
cd web
npm ci
npm run dev
# Open http://localhost:8283 (or http://<server-ip>:8283 from another device)
```

Vite listens on `0.0.0.0:8283` and fails if the port is already occupied rather
than silently choosing another port. This exposes the development UI and its API
proxy on all interfaces; use it only on a trusted network.

`TED_API_URL=http://127.0.0.1:8289 npm run dev` targets a different server during
development. This is a Vite proxy setting, not a per-browser server connection.
The proxy preserves Host and Origin for the server's WebSocket origin validation.
Production assets and `/v1` HTTP/WebSocket routes are served on the same origin.
Do not use `vite preview` as the production API proxy; use `ted serve`.

## Workflow

The sidebar starts with two separate shadcn buttons, with a small gap between
them: the primary **New chat** action and a smaller, icon-only **New project**
action with an accessible label and hover title. Project creation is no longer repeated below the chat list.

- **New chat → project picker → empty chat.** The server copies the selected
  project's defaults. There is no title or initial-message form and no web-created
  misc agents. Until a server title exists, the first queued message labels a chat.
- **New project:** type an existing server-local directory, derive its name from
  the last path segment, and set model/effort defaults. Settings allow updating
  defaults and deleting empty projects; deletion never removes the directory.
- Active agents are ordered newest-created first within project groups. Misc
  agents come first. Each sidebar entry shows its project’s current Git branch on
  a second line, with a settle button on the right. Settled chats are in one
  ungrouped, collapsed section below, with restore buttons. Settled rows and their
  heading are subtly faded until hovered or keyboard-focused. On hover-capable
  pointer devices, settle/restore buttons appear only while their row is hovered
  or keyboard-focused. Entries use the full width at rest, then smoothly shrink
  by the action’s width and gap on reveal without changing row height. Reduced
  motion preferences are respected. Touch devices keep the action and its space
  visible. Sidebar actions do not switch chats or discard drafts; failures are
  shown beside the affected row. The sidebar has no persistent connection-status
  footer; connection failures still show the main reconnect notice.
- Sending to a settled agent explicitly restores it, then submits the message.
  Held messages run first in FIFO order. Restore alone does **not** continue work.
  If submission fails after restoration, the agent stays restored and the draft
  remains available for retry.
- The pending queue has a sticky count/header (including “Queue held”) that
  stays visible while its messages scroll.
- Pending messages have **Edit** and **Cancel** actions. Edit removes only that
  queue entry and loads its text into the composer, focusing it without sending.
  Replacing an existing draft requires confirmation. The composer is briefly
  read-only while removal is pending; a failed removal leaves the draft intact.
  Messages that have already started cannot be edited. Resending an edited
  message is a new submission, with literal leading slashes escaped as needed.
  Continue releases held work.
  Stop targets the specific running turn, **not** the next turn or the whole queue.
- `/model`, `/effort`, `/stop`, `/settle`, `/unsettle`, `/continue`, `/help`, and
  `/exit` match the TUI controls. `/exit` returns to the workspace; `//` escapes a
  literal slash. Model/effort changes apply on the next turn.
- Drafts and retry keys are in memory, scoped per agent. Switching retains drafts;
  refreshing discards them. They are never written to localStorage or the URL.
  Keystrokes update only the textarea and send-button state, not the whole chat.
  The transcript and individual message rows are memoized, so existing Markdown
  is not re-parsed when typing or when new messages arrive.
- Chat switches start at the bottom, without restoring scroll position. Message
  Scroller follows new output at the bottom and respects scrolling up to read.
  A successful local send explicitly resumes following, even when a touch or
  horizontal-scroll gesture paused it without moving the viewport. This also
  covers queued messages whose turn starts later. Transcript interaction during
  a pending send takes precedence over its late acknowledgement; failed sends,
  slash commands, and responses from another chat do not force a jump. The
  composer and transcript share a per-chat provider; no scroll timeouts or
  remount-to-bottom tricks are used.
- Desktop sidebar, mobile drawer, system-aware light/dark colors.

### URL state

- `/agents/:agentId`
- `/agents/:agentId?panel=project-settings&project=:projectId` for project options
  over the current chat (also supported on the workspace route)
- `/?dialog=new-agent`
- `/?dialog=new-project`
- `/projects/:projectId?panel=settings`
- `?sidebar=open` for the mobile drawer

Project options from the sidebar preserve the current viewport, transcript scroll,
and draft. The drawer and project-default dialogs are mutually exclusive; dismissing an
overlay never navigates to a different project or leaves an inactive modal mounted.

Components read navigation state directly from the router. Server settings,
drafts, unsaved form input, and transient collapse state are not URL parameters.

### Data and replay

`src/lib/api.generated.ts` is generated from `../api/openapi.yaml`.
`src/lib/store.ts` owns a multiplexed WebSocket, inventory, per-agent processed
cursors, and TUI-style display transcripts. It replays lifetime events, rather
than rendering conversation checkpoints again as duplicate messages. Output
events are completed display messages, not token deltas. Tool calls/results are
expandable and retain full uncapped tool output; Markdown is rendered without
raw HTML execution. The API does not currently expose separate approval actions
or an attachment-upload endpoint.

Inventory cursors never acknowledge processed events. Large `event_ref` payloads
are fetched from a reconstructed same-origin path and processed serially before
later frames. Duplicates are ignored, gaps fail rather than silently skipping,
and reconnects resume from successfully processed cursors. Stream protocol errors
are surfaced and require explicit Reconnect; transport failures back off and
retry automatically. Explicit Reconnect reloads inventory and replays from zero.
Projects refresh periodically because project-only changes have no WS event.
Live Git branches refresh every three seconds for all projects represented by
sidebar chats, including unselected and settled chats. Requests are deduplicated
per project, and stale responses from earlier connections are ignored. Agents
sharing a project show the same branch of that project’s working directory; this
is not a per-turn branch snapshot. Missing branches are shown as unavailable,
and agents without a project show “No project.”

This first implementation retains transcripts for observed agents in memory and
replays all existing agents, including settled history, on page load. Very large
workspaces will benefit from a later lazy history/cache layer; it is not a
virtualized or server-paginated archive browser yet.

HTTP mutations support large messages and idempotency keys. Failed message
retries reuse the key while text is unchanged. Browser HTTP on a trusted LAN
uses `getRandomValues` where secure-context-only `randomUUID` is unavailable.

## Check and build

```sh
npm run generate:api
npm run lint
npm test
npx playwright install chromium
npm run test:e2e
npm run build
# From repository root:
go test ./...
```

`CHROME_PATH=/path/to/chrome npm run test:e2e` uses an existing Chromium install.
Browser tests use deterministic HTTP/WebSocket fixtures, not real model calls.
They cover project-first creation, automatic restore-on-send, settings URLs,
Markdown and tool rendering, ephemeral drafts, mobile navigation, and scroller
follow/read behavior. Unit tests cover queue replay, duplicate/gap handling,
serial event references, failed-reference cursor safety, grouping, and mutations.
Go tests cover embedded assets, direct-link fallback, and API route isolation.
CI verifies generated types and the checked-in production bundle are current.

## Security

The server can execute tools with its OS user's privileges. **There is no built-in
authentication.** Use loopback/a trusted network or an authenticated TLS reverse
proxy (including WebSocket upgrades). Never expose it directly to the public
internet. Browser Origin checks are not an authentication mechanism.

## Preview artifacts

Captured from the light/dark desktop and mobile Playwright workflows with
deterministic API/WebSocket fixtures (no real model calls or project writes):

- [Desktop projects](../docs/screenshots/web-control-plane.png)
- [Chat and expanded tool output](../docs/screenshots/web-chat.png)
- [Mobile chat](../docs/screenshots/web-mobile-chat.png)
- [Project picker](../docs/screenshots/web-project-picker.png)
- [Mobile drawer, dark mode](../docs/screenshots/web-mobile.png)
- [Workflow video](../docs/screenshots/web-control-plane-demo.webm)
- [Composer controls, desktop](../docs/screenshots/web-composer.png)
- [Composer controls, mobile](../docs/screenshots/web-composer-mobile.png)
- [Sticky pending queue header, desktop](../docs/screenshots/web-pending-queue-sticky.png)
- [Sticky pending queue header, mobile](../docs/screenshots/web-pending-queue-sticky-mobile.png)
- [Pending message edit action](../docs/screenshots/web-pending-edit.png)
- [Pending message moved to composer](../docs/screenshots/web-pending-edit-after.png)
- [Pending message editing, mobile](../docs/screenshots/web-pending-edit-mobile.png)
- [Pending message edit workflow video](../docs/screenshots/web-pending-edit-demo.webm)
- [Model menu, desktop](../docs/screenshots/web-model-select.png)
- [Model menu, mobile](../docs/screenshots/web-model-select-mobile.png)
- [Effort menu, mobile](../docs/screenshots/web-effort-select-mobile.png)
- [Sidebar chat rows, desktop](../docs/screenshots/web-sidebar-chats.png)
- [Sidebar full-width entries](../docs/screenshots/web-sidebar-full-width.png)
- [Sidebar hover actions](../docs/screenshots/web-sidebar-hover-actions.png)
- [Sidebar reveal workflow video](../docs/screenshots/web-sidebar-hover-demo.webm)
- [Mobile drawer entry animation](../docs/screenshots/web-mobile-drawer-demo.webm)
- [Sidebar creation controls, desktop](../docs/screenshots/web-sidebar-create.png)
- [Sidebar creation controls, mobile](../docs/screenshots/web-sidebar-create-mobile.png)
- [Sidebar chat rows, mobile](../docs/screenshots/web-sidebar-chats-mobile.png)
- [Compact tool spacing, desktop](../docs/screenshots/web-tool-spacing.png)
- [Compact tool spacing, mobile](../docs/screenshots/web-tool-spacing-mobile.png)
- [Inline image preview](../docs/screenshots/web-inline-image.png)
- [Image lightbox, desktop](../docs/screenshots/web-image-lightbox.png)
- [Image lightbox, mobile](../docs/screenshots/web-image-lightbox-mobile.png)
- [Image lightbox workflow video](../docs/screenshots/web-image-lightbox-demo.webm)

The browser suite attaches desktop, picker, chat, settings, and mobile screenshots
to its results. To also retain workflow video:

```sh
TED_WEB_RECORD=1 npm run test:e2e -- --grep "plain interface"
# Artifacts: web/test-results/ (when viewed from the repository root)
```
