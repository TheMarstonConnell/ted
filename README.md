# Ted

A Go coding agent with a Bubble Tea terminal interface.

## System prompt

The agent instructions live in [`agent/system_prompt.md`](agent/system_prompt.md).
Go embeds this file into the binary at build time; edit it and rebuild (or use
`go run`) to apply changes. No prompt file is needed at runtime.

## Run

```sh
go run . tui
```

To automatically send an initial user message when the TUI starts:

```sh
go run . tui --prompt "Explain the current project without modifying files."
```

The prompt is sent as literal chat text (not a slash command), and the TUI
remains interactive afterward. An omitted, empty, or whitespace-only prompt
starts the usual idle session.

You can also select the startup model and reasoning effort:

```sh
go run . tui --model codex/gpt-5.6-terra --effort high --prompt "Explain this project."
```

`--model` and `--effort` can be used independently. The model is selected before
applying effort, and both take effect before the initial prompt. Omitted or empty
flags keep defaults. Invalid models or unsupported efforts fail before the TUI
starts. Model IDs use the same `provider/model-id` format as `/model`.

Configure `OPENROUTER_API_KEY`, or reuse an existing Codex CLI login (`codex
login`). The CLI loads `.env`; the agent package does not. Configured providers
supply a static model catalog; there is no live model fetch yet.

### Discover providers, models, and efforts

List model IDs available through your configured providers without starting the TUI:

```sh
go run . models
```

List configured provider names, or filter models to one provider:

```sh
go run . providers
go run . models --provider codex
```

Without `--provider` (or with an empty value), `models` lists all configured
providers' models. Unknown or unconfigured provider names return an error.
`providers` prints one configured provider name per line.

List reasoning effort options for a specific model:

```sh
go run . efforts codex/gpt-5.6-terra
# low
# medium
# high
```

These commands load `.env` and use the same provider configuration as `tui`.
Results come from the static catalog, with no API request. Model IDs are printed
one per line and can be passed to `tui --model`. `efforts` reports when a model
has no configurable effort; unknown or unavailable model IDs return an error.

### Slash commands

| Command | Behavior |
| --- | --- |
| `/model` | Searchable model picker |
| `/model <provider/model-id>` | Select a model directly |
| `/effort` | Effort picker for the current model |
| `/effort <value>` | Set supported reasoning effort directly |
| `/help` | List commands |
| `/exit` | Exit the application |

Type to filter a picker, use Up/Down to select, Enter to confirm, and Escape to
cancel. The current selection has a check mark. Outside a picker, Escape and
Ctrl+C also exit. Plain `exit` is sent as chat. Use `//` to send chat beginning with a literal slash (for example,
`//model` sends `/model` to the model). Unknown commands produce local errors.

Settings belong to the running agent instance and are not persisted. A model
switch retains compatible effort; otherwise it uses the new model's default
(or clears effort if unsupported) and reports the adjustment. The static
integration currently exposes low/medium/high for the Codex entries and
OpenRouter's listed OpenAI model. The other OpenRouter entries do not expose
configurable effort; this is not a claim that the upstream models cannot reason.

Settings changes and overlapping turns are rejected while a turn is active.
Command results appear only in the UI transcript, never in model history.
Conversation text and tool exchanges survive model switches. Opaque reasoning
is retained in stored history but only replayed to its originating model.
Live cross-model/provider compatibility still depends on the upstream APIs.

## Embed in Go

The importable core is `github.com/TheMarstonConnell/ted/agent`; it has no
Bubble Tea, CLI, or slash-command dependencies.

```go
package example

import "github.com/TheMarstonConnell/ted/agent"

func Run(apiKey string) (string, error) {
    a := agent.NewAgent(nil, []agent.Provider{
        agent.NewOpenRouterProvider(apiKey),
    })
    if _, err := a.SetModel("openrouter/openai/gpt-5.6-luna"); err != nil {
        return "", err
    }
    if _, err := a.SetEffort(agent.EffortHigh); err != nil {
        return "", err
    }
    if err := a.Turn("Explain the current project without modifying files."); err != nil {
        return "", err
    }
    messages := a.Messages()
    return messages[len(messages)-1].Content.Text(), nil
}
```

- `ListModels`, `ListEfforts`, and `Settings` expose discovery and current state.
- `SetModel` and `SetEffort` validate and return a `SettingsChange` describing
  before/after settings. Use `errors.Is(err, agent.ErrBusy)` to detect busy state.
- `Messages` returns a detached snapshot of **committed** history. Failed turns
  leave that history unchanged; shell side effects cannot be rolled back.
- `SetOutput` optionally installs a synchronous event callback. Return promptly;
  inspecting settings and committed history from the callback is safe. Events
  emitted during a failed turn are not retracted.
- A nil logger discards diagnostics. No output callback is required.
- Implement `Provider` to add a backend. Its catalog should be stable and safe
  for concurrent reads. Provider model IDs are local; the agent qualifies them
  with `Provider.Name() + "/"`. Completion requests contain the qualified ID,
  selected effort, and a detached message snapshot.

**Tool execution is not sandboxed.** The built-in bash tool runs with the host
process's permissions and working directory. Each bash call has a two-minute
timeout; captured output and a timeout message are returned to the model. On
Unix, cancellation kills the command's process group; other platforms kill the
direct process. Output-pipe cleanup is bounded to one additional second. This
is not a sandbox: processes that detach from the group may survive, and shell
side effects are not undone. Only embed it where that access
is appropriate. Turn cancellation, custom tool policies, saved state, live
catalogs, and an HTTP API are outside this first version. The existing Codex auth
store can refresh and rewrite Codex credentials; agent settings do not write files.

## Command adapter

`github.com/TheMarstonConnell/ted/commands` exposes `New(a)`,
`Handle(input)`, and `Execute(name, args...)`. Results are plain data:
confirmation text, exit requests, settings changes, or selection requests with stable choice
IDs. A client confirms a selection with `Execute(selection.Command, choice.ID)`;
validation happens again at execution time. Direct programmatic callers should
use agent methods rather than construct slash strings.

The TUI owns picker rendering, filtering, and keybindings. The command package
owns parsing and dispatch. The agent owns operations, validation, and state.
`/help` is adapter metadata rather than a conversation operation.

## Test

```sh
go test ./...
go test -race ./...
go vet ./...
```

Ordinary tests use fake providers and HTTP transports, not live credentials.
Existing Codex integration tests remain opt-in with `HARNESS_LIVE_CODEX=1`.

## Completion retries

Ted automatically retries an interrupted model request up to three times (four
attempts total), using exponential backoff with jitter. This includes peer
HTTP/2 `INTERNAL_ERROR` / `REFUSED_STREAM` resets, truncated response streams,
network timeouts or resets, and HTTP 408, 429, 500, 502, 503, and 504 responses.
Authentication failures and invalid requests are not retried. The TUI shows a
retry notice; diagnostics record the attempt and delay.

Retries happen around a **single provider request**, not the whole agent turn.
Already executed bash commands and their results remain in that request's
history and are not replayed by the retry loop. Providers buffer response
streams until completion, so partial tool calls from a failed attempt are not
executed. Retrying can still incur additional provider usage. If all attempts
fail, ted reports the error using its existing turn-failure behavior; this is
not a durable resume/checkpoint system.

## Browser automation through bash

Ted still exposes **only the bash tool** to the model. Browser automation uses
noninteractive `ted browser ...` CLI commands backed by chromedp and a local
Unix-socket service. Install Chrome/Chromium and put a built `ted` binary on
`PATH` before using browser commands from the agent:

```sh
go install .
ted browser --help
```

The service starts on demand. Chrome starts only when a browser action needs
it. Each canonical Git worktree root (or working directory outside Git) owns a
persistent browser profile. Cookies and site storage survive browser restarts,
so login sessions can be reused until the site expires them. Ted does not store
passwords or provide a password manager. Treat the profile as sensitive account
data; don't commit it or share it casually.

Threads receive their own tabs but **share the project's authentication and
storage**. Signing out in one thread affects the others. Start a thread's first
browser action with `--isolated` for a clean incognito session instead. Isolated
state is not persisted. Child agents get fresh thread IDs, not ownership of
the parent's tabs. This is operational separation, not a security boundary
against an agent with unrestricted bash access.

### Commands

Commands return JSON on stdout and a nonzero exit status on failure. Browser
errors include a stable `error.code`. The agent's bash environment supplies
`TED_THREAD_ID` and `TED_PROJECT_ROOT`; manual invocations use thread `manual`
and the current project's root. Override these with `--thread` and `--project`
when controlling an explicit session. `--timeout` defaults to `30s`.

```sh
ted browser open http://localhost:3000/login
ted browser snapshot
ted browser fill --label Email --value test@example.com
ted browser click --role button --name 'Sign in'
ted browser wait --url-pattern '*/dashboard'
ted browser screenshot
ted browser screenshot --full-page

ted browser tabs
ted browser tab new https://example.com
ted browser tab select TAB_ID
ted browser tab close TAB_ID

ted browser record start
# Run browser actions here.
ted browser record stop

ted browser console
ted browser errors
ted browser close  # Closes thread tabs; does not delete the project profile.
```

Prefer compact snapshots and semantic targets (`--label`, `--role` with
`--name`) or snapshot references (`--ref`) over dumping HTML. CSS targeting is
available with `--selector`. `fill` and `select` accept `--value`; `press`
accepts `--key`; `scroll` accepts `--x` and `--y` deltas. Obtain a fresh snapshot
when references become stale. Ambiguous targets are errors rather than a
license to click an arbitrary match. Snapshots and semantic targeting are a
bounded, DOM-based view of the main document, not a complete accessibility-tree
implementation. Complex iframe/shadow-DOM workflows may need raw CDP.

### Raw CDP escape hatch

Use the high-level commands for routine tasks. Raw CDP is available when they
don't cover an operation, but does not provide their targeting or waiting
semantics:

```sh
ted browser cdp Page.getLayoutMetrics
ted browser cdp Runtime.evaluate --params '{"expression":"document.title"}'
ted browser cdp Network.setExtraHTTPHeaders --params-file ./headers.json
printf '%s' '{"expression":"document.title"}' |
  ted browser cdp Runtime.evaluate --params-file -
ted browser cdp listen Network.responseReceived --timeout 5s --limit 20
```

Methods target the current thread tab by default. `--browser` targets the
shared browser and can affect other threads. Do not place secrets in raw CDP
arguments or shell commands that will appear in the conversation.

### Artifacts and storage

`TED_HOME` defaults to `~/.ted`; the shell's actual `$HOME` is unchanged. Agents
pass its construction-time absolute path to bash tools, so changing directories
does not redirect browser storage or screenshot attachments. Browser
profiles and service files live beneath `$TED_HOME/browser/`, while thread
artifacts live beneath `$TED_HOME/threads/<thread-id>/artifacts/`. Directories
are private. This feature does not introduce a general-purpose agent filesystem
or credential store.

Screenshots are registered in a thread artifact manifest. After a bash call,
the host attaches newly registered screenshot images as model input, with
path, file size, and count checks. Printing an arbitrary image path does not
attach it. Use an image-capable model for visual inspection.

Recording requires `ffmpeg` and captures silent page-viewport video, not audio,
browser chrome, or OS dialogs. Video artifacts are for human review; screenshots
provide the agent's visual input. Browser action traces accompany the artifacts;
high-level field values are redacted. Website content and screenshots may still
contain sensitive information.

### Browser integration tests

Normal tests do not require Chrome. To exercise real navigation, targeting,
screenshots, cookie/storage persistence across browser restarts, thread/project
isolation, and recording, put Chrome/Chromium on `PATH` and run:

```sh
TED_BROWSER_INTEGRATION=1 go test -race ./... -count=1
```

Recording verification also requires `ffmpeg` and `ffprobe`. Integration tests
use temporary profiles, not your normal project login state. Recording brings
its tab to the foreground so Chrome produces screencast frames; avoid competing
foreground-tab changes during a recording in the shared project browser.

### Diagnostics and payload limits

Diagnostics are appended to `harness.log` in the working directory by default.
Set `LOG_FILE` to change the path and `LOG_LEVEL` to change verbosity (default:
`info`). Startup, outgoing request byte counts, HTTP statuses, tool-output sizes,
and completion failures are logged at the default level. Debug logging can
include conversation/tool contents; use it only when appropriate.

Bash retains at most **64 KiB** of combined stdout/stderr per call, followed by
an explicit truncation notice and any exit/timeout error. Excess output is drained
and discarded rather than buffered. Redirect large output to a file and inspect
selected sections with `head`, `tail`, or `grep`.

Screenshot attachments are limited to **1 MiB each**, with up to four per tool-call
batch. Invalid or oversized screenshots are skipped with a status message and a
log warning; the original artifact is not deleted. Capture a smaller image if
needed. Both model providers reject serialized requests larger than **8 MiB**
locally, with guidance to reduce content or start a fresh conversation. This is
a harness safety budget, not a guarantee of a provider's limit; history is not
automatically compacted.
