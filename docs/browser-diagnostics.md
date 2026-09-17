# Browser compatibility and blocked-page diagnostics

Ted uses a locally installed Chrome/Chromium through chromedp, with a persistent
project profile and optional isolated browser contexts. It is an automated,
headless browser, not the user's normal desktop profile. Authentication, installed
extensions, managed policies, available codecs, certificates and network access
can differ from the desktop browser. Browser requests originate on the Ted server.
The shared Browser panel streams a real browser tab, not an iframe containing the
remote site; a site's `X-Frame-Options` is not itself a reason the panel cannot show
that site's top-level page.

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
  including its numeric `status`, `request_id` and `resource_type`.
- `source: network`, `type: loading_failed`: Chrome's `text` (for example,
  `net::ERR_CERT_AUTHORITY_INVALID`), `request_id`, `resource_type`, `canceled`,
  and, when supplied by Chrome, `blocked_reason` and `cors_error`.

HTTP error pages are still navigable: a successful `open` does **not** mean the
server returned HTTP 200. Conversely, a subresource failure does not necessarily
mean the document failed. A canceled request can result from ordinary navigation,
not a site restriction. Chrome log entries may describe the same failure as a
network entry.

Network metadata intentionally omits URLs, headers, cookies, bodies and CORS
parameter values. Request IDs can be correlated with explicitly collected raw CDP
network events when deeper investigation is necessary. Raw CDP and existing
console/log output may contain secrets; inspect and share them cautiously. These
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

Ted retains chromedp's automation indicator and headless mode. It does not add
certificate-error or CORS bypass flags. It removes chromedp's legacy
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
