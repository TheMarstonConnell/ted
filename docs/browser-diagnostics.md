# Browser compatibility and blocked-page diagnostics

Ted uses a locally installed Chrome/Chromium through chromedp, with a persistent
project profile and optional isolated browser contexts. It is automated and
headless by default, not the user's normal desktop profile. Authentication, installed
extensions, managed policies, available codecs, certificates and network access
can differ from the desktop browser. Browser requests originate on the Ted server.
The shared Browser panel streams a real browser tab, not an iframe containing the
remote site; a site's `X-Frame-Options` is not itself a reason the panel cannot show
that site's top-level page.

## Persistent headed mode

Some login flows behave differently in headless Chrome. To run genuine headed
Chrome through the same live viewer, save this JSON in
`$TED_HOME/browser/config.json`:

```json
{
  "mode": "headed"
}
```

For the web control plane, use **`<ted serve --data-dir>/runtime/browser/config.json`**.
This is the browser home used by chats, not necessarily the server shell's
`$TED_HOME`. A standalone CLI normally uses `~/.ted/browser/config.json`.
Configuration is local to that daemon and applies to all its project browsers.
The default when the file is absent is `{"mode":"headless"}`. Unknown settings,
invalid mode values, and malformed configuration fail rather than silently
starting a differently configured browser.

Optionally add `"executable": "/absolute/path/to/chrome"` to select a particular
Chrome/Chromium installation. Otherwise Ted uses its normal executable discovery.
Use a current Chrome build with working sandbox support, run as an unprivileged
user, and leave browser security enabled. Ted does not download browsers or
system packages automatically.

On Linux, headed mode uses an inherited `DISPLAY` and `XAUTHORITY` when available.
If `DISPLAY` is unset, install **Xvfb** and its runtime dependencies on the server
(for example, `sudo apt-get install xvfb` on Ubuntu). Ted then creates a private,
authenticated virtual display for each project browser. It chooses an unused
display, disables X11 TCP listening, and removes its display process and credentials
after Chrome closes. It does not need `xauth` or a VNC server. Missing display
support produces an actionable startup error; it never silently falls back to
headless mode. Other platforms use their native graphical desktop.

Settings are read once before the daemon's first Chrome launch. After changing
configuration or upgrading Ted, gracefully restart the **browser daemon for that
home** (`ted browser serve`, SIGTERM), not just the viewer. `ted browser close`
closes a thread's tabs but does not reload launch settings. A server restart alone
may leave an existing detached browser daemon running. New daemon launches must
use the upgraded Ted executable.

Mode changes preserve the existing project profile and stored cookies; a restart
closes current tabs. The browser contents remain 1440 × 900: Ted sizes the native
headed window around its toolbars, including new/isolated windows and adopted
popups, rather than clipping the streamed page. Connecting a viewer does not
resize a tab or reset explicit agent emulation. The viewer still streams webpage
content, not Chrome's native controls or operating-system dialogs.

Headed mode is **not** user-agent spoofing or a promise of unrestricted access.
Automation remains disclosed, and sandbox, certificate, CORS and other web
security checks remain enforced. One reported Google/WorkOS login succeeded after
switching to headed mode; external authentication behavior is not a CI contract.
Tests use local fixtures, never real credentials or Google's live sign-in flow.

## Diagnose before changing configuration

```sh
ted browser errors
ted browser console
ted browser cdp Browser.getVersion --browser
ted browser cdp Browser.getBrowserCommandLine --browser
```

These commands use the current thread's selected tab/session. The errors buffer
retains at most 500 entries, including JavaScript exceptions, Chrome log warnings
and errors, and network failure metadata observed after attachment:

- `source: network`, `type: http_error`: an HTTP response of 400 or above,
  including its numeric `status` and `resource_type`.
- `source: network`, `type: loading_failed`: Chrome's `text` (for example,
  `net::ERR_CERT_AUTHORITY_INVALID`), `resource_type`, `canceled`,
  and, when supplied by Chrome, `blocked_reason` and `cors_error`.

HTTP error pages are still navigable: a successful `open` does **not** mean the
server returned HTTP 200. Conversely, a subresource failure does not necessarily
mean the document failed. A canceled request can result from ordinary navigation,
not a site restriction. Chrome log entries may describe the same failure as a
network entry.

Network metadata intentionally omits URLs, headers, cookies, bodies and CORS
parameter values. Raw CDP and existing console/log output may contain secrets;
inspect and share them cautiously. These
network records are memory-only, not added to the action trace. They are not a
complete HAR or a retrospective record of requests before target attachment.

## Interpret the evidence

| Evidence | Possible cause / safe next step |
| --- | --- |
| DNS or connection errors | Check DNS, proxy, egress/firewall and reachability from the Ted server. |
| Certificate/TLS error | Check the server certificate chain, hostname, clock and trusted CA configuration. Do not disable verification. |
| `cors_error`, CSP or mixed-content `blocked_reason` | Fix the site's policy or use its supported access flow; do not disable browser web security. |
| HTTP 401/403 | Authentication, authorization or a site/intermediary policy decision. Status alone does not establish which one. |
| HTTP 429 | Rate limiting; honor the site's retry guidance. |
| Challenge or access-denied page with HTTP 200 | Inspect the actual page. A successful transport cannot identify or overcome site restrictions. |
| Chrome startup failure | Check the installed browser, native dependencies, profile permissions and sandbox support. |

Some sites decline automated browsers or require a supported interactive flow.
Ted does not spoof automation, solve challenges or promise unrestricted access.
Use an approved API or supported login/access method when required by the site.

## Launch-policy audit

Ted retains chromedp's automation indicator in both modes. Headed mode removes the
headless launch flag; it does not override the user agent or add certificate-error
or CORS bypass flags. It removes chromedp's legacy
`disable-features=site-per-process,Translate,BlinkGenPropertyTrees` override so
Chrome chooses its own site-isolation and feature defaults. It also explicitly
prevents chromedp's automatic `--no-sandbox` fallback under root: deploy as an
unprivileged user with working Chrome sandbox support rather than disabling it.
Other inherited chromedp automation defaults remain; this is not a claim that all
launch settings match a desktop browser or constitute a comprehensive security
hardening profile.

Local integration coverage verifies real HTTP 403/429 diagnostics, rejected
cross-origin fetches, rejected untrusted TLS navigation and recovery, and the
launch arguments without external websites:

```sh
TED_BROWSER_INTEGRATION=1 go test -race ./browser -run TestNetworkDiagnosticsIntegration
```
