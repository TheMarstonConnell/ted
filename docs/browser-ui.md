# Interactive shared browser

The Browser panel connects to the selected conversation's existing browser session.
It is not an iframe preview: navigation, login state, DOM and tabs are shared with
`ted browser` commands from that agent. Subagents have their own sessions; select
their conversation to inspect them.

## Interaction model

- Open **Browser** from a conversation. Opening the viewer does not start Chrome;
  an empty session offers an explicit **Open browser** action.
- Watch beside chat, expand the panel, or use the full-width view on narrow screens.
- The viewport is always interactive. Clicking focuses it for keyboard input;
  Escape releases local focus. On touchscreens, a tap clicks the remote page and
  a drag scrolls it. The address bar, tab strip and history controls operate on
  the displayed browser tab.
- There is no exclusive controller or takeover mode. Human input does not stop the
  agent. Both share Chrome's focus and page state: a simultaneous agent click can
  redirect typing, and a navigation can interrupt an interaction.
- Follow the agent's selected tab or pin a tab to inspect it without changing the
  agent's selected tab. Input is explicitly addressed to the displayed tab.
- Ted's activity overlay identifies agent pointer movement and dispatched clicks.
  Direct field-fill operations are indicated as fills, not invented mouse clicks.
  Human input does not appear as Ted's cursor. Arbitrary script-triggered page
  actions cannot always be mapped to pointer coordinates.
- Closing the viewer ends its subscription, not the browser session. Reopening
  reconnects to existing tabs. Browser lifetime remains managed by the daemon and
  the existing agent/session cleanup paths. Settling, project deletion and server
  shutdown also clean up viewer-created sessions in the service’s runtime home;
  they do not depend on an agent having run a model turn.

## Architecture and boundaries

A dedicated WebSocket bridges the control plane to a persistent Unix-socket
subscription in the browser daemon. Chrome DevTools Protocol provides JPEG
screencast frames and accepts constrained mouse, keyboard, text and navigation
commands. The daemon shares screencast capture with video recording, bounds
viewer buffers, and keeps human input independent of long-running agent waits.
The UI scales the stable browser viewport instead of resizing the page on every
panel layout change.

This is webpage interaction, not a streamed operating-system desktop. Native
Chrome UI, audio, system file pickers, file transfer and remote clipboard readback
are outside the initial scope. Plain-text paste is supported; keyboard shortcuts
reserved by the local browser/OS cannot all be forwarded.

## Security

The viewer exposes pages and authenticated sessions accessible to the server's
browser. The control plane has no built-in authentication: use loopback, a trusted
network, or an authenticated TLS reverse proxy that protects WebSocket upgrades.
Same-origin checks are an additional browser defense, not authentication.
For TLS termination with an HTTP backend, configure
`ted serve --public-origin https://ted.example.com` and preserve the browser's
Origin header; forwarded headers alone never alter origin validation. The
server resolves browser identity from the selected agent; clients cannot submit
arbitrary project paths or raw CDP methods. Browser input and activity events must
not be persisted in conversation logs or used to log typed secrets. Screenshots,
video recordings and the live viewport can still visibly contain sensitive data.

## End-to-end validation

Deterministic UI tests run with the normal Playwright suite. For a real
UI → API → daemon → Chrome test, build the embedded app and executable, then run:

```sh
(cd web && npm ci && npm run build)
go build -o /tmp/ted-browser-system .
cd web
TED_BROWSER_SYSTEM_BINARY=/tmp/ted-browser-system \
  npm run test:e2e -- browser-system.spec.ts
```

The test creates an isolated `TED_HOME` and project, starts its own server/daemon
and local webpage, and submits no model turns. The executable still requires the
normal provider configuration for server startup. Set `CHROME_PATH` if Playwright
needs a specific local browser executable. Add `TED_WEB_RECORD=1` to retain video.

The opt-in daemon integration suite exercises Chrome and recording directly:

```sh
TED_BROWSER_INTEGRATION=1 go test -race ./browser
```
