# Ted

A Go coding agent with a Bubble Tea terminal interface.

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
