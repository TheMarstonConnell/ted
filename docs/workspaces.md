# Chat workspaces

A chat thread owns its workspace independently of its model, agent runtime, and
connected clients. **Local** means the project’s current checkout (the API mode
remains `current_checkout`). Choose **Local** or **Worktree** before sending the
first message. Worktree mode also exposes **Start from**, a remote-qualified base
branch such as `origin/main` or `upstream/develop`.

Project settings provide the defaults for newly created chats. Current checkout
is the initial default; the base branch is taken from the remote's local `HEAD`
reference, preferring `origin`. If no default is known, choose a branch explicitly.
Branch discovery lists locally known remote-tracking branches and excludes
symbolic `HEAD` entries and local-only branches. Non-Git projects cannot use
Worktree mode. Discovery does not contact the network.

## First send and execution

The first accepted, nonempty message permanently locks both workspace choices in
the same durable transaction that queues the message. This is enforced by the
server, including concurrent requests and clients other than the web interface.
Creating an empty chat does not create a worktree or lock the choice.

- **Local** runs directly in the project's original directory, sharing
  its branch, local modifications, and untracked files with other checkout chats.
  No fetch or branch switch occurs.
- **Worktree** first fetches the selected remote branch into a thread-private Git
  ref, resolves its exact commit, then creates a new branch and linked worktree.
  The original checkout is not switched, reset, stashed, or pulled. Fetch failures
  never fall back to a stale commit. The new worktree starts from committed code;
  ignored files, secrets, dependencies, and uncommitted modifications are not copied.

Worktrees live at `<control-plane-data-dir>/worktrees/<project-id>/<thread-id>`.
Their generated branches use `ted/<title-slug>-<thread-id>`. The control-plane data
directory must be outside the project checkout; otherwise setup fails rather
than placing generated files inside the original working directory.

The composer footer replaces the directory display with a compact workspace
selector and, in Worktree mode, a starting-branch picker. After first send these
become plain **Local** or **Worktree** text, including during setup or
after failure. The directory is available in the text’s tooltip. The Git branch
appears beside the workspace in the footer, separated by a subtle vertical line;
Local uses the project’s live branch, and Worktree uses its recorded branch. The
starting-branch picker fills that slot before worktree setup. The sidebar also
shows the generated branch. The recorded branch is the **creation branch**, not
an enforced constraint on future Git operations.
Users and agents may switch branches inside the workspace normally.

Later turns, model changes, and resumed chats reuse the recorded directory.
They do not fetch again, recreate the worktree, or reset its contents. Project
default changes do not alter existing chats, including already-created drafts.

## Failures and persistence

Git setup runs asynchronously, with noninteractive credentials and bounded
command timeouts. The agent does not begin until setup succeeds. A setup failure
leaves the first message visible with its error and permanently blocks further
execution in that thread. There is **no setup retry**, no changing the choices,
and no fallback to the original directory: create a new chat to try again.

Server restart during fetching or creation is treated as failed setup, never as
permission to repeat provisioning. The durable queue and one active worker per
thread prevent duplicate worktree creation. Idempotent repeated requests return
the existing thread/message without repeating the external operation.

A missing or inaccessible established workspace blocks further execution. It is
not silently recreated. Worktrees, generated branches, and private base refs are
retained: archiving/settling chats does not clean them up. Cleanup and
merge/cherry-pick actions are outside this feature's scope.

## API

- `Project.workspace_defaults`: `{ "mode": "current_checkout" | "worktree", "base_branch"?: "origin/main" }`
- `GET /v1/projects/{id}/branches`: `{ "is_git": true, "branches": ["origin/main"], "default_branch": "origin/main" }`
- `POST /v1/agents`: optional `workspace` selection; otherwise snapshots project defaults.
- `PATCH /v1/agents/{id}/workspace`: replace a draft's workspace selection;
  returns `409 workspace_locked` after first send.
- `Agent.workspace`: selection plus `locked`, `status`, and (when available)
  `path`, `branch`, `base_commit`, and `error`.
- Workspace status is `draft`, `fetching`, `creating`, `ready`, or `failed` and
  appears in agent snapshots and `agent.updated` events.
- Failed workspaces reject message submission and Continue with
  `409 workspace_failed`. Repeating an acknowledged idempotency key may still
  retrieve the original result; it never retries setup.

## Terminal startup

New TUI threads inherit the project default. Override it explicitly when needed:

```sh
ted tui --workspace worktree --base-branch origin/main
ted tui --workspace current_checkout
```

`--base-branch origin/main` alone implies Worktree mode. These startup overrides
cannot be combined with `--resume` or `--continue`; resuming reuses the thread's
existing workspace. The HTTP workspace endpoint remains available to edit any
empty, unlocked thread before its first message.

## Child agents and existing directories

`--parent-agent <id>` creates a new child of an existing agent on the same server.
The relationship is durable and creation-only. Children appear beneath their
parent in the web sidebar, with an expand/collapse control. A family stays in its
root parent's project section while any member is active; an entirely settled
family moves to Settled chats. Settling, stopping, or disconnecting a parent does
not cascade to children (the existing lifetime rule for a TUI-owned server still
applies).

```sh
# Agent tool commands already receive TED_THREAD_ID. For tmux, expand it in the
# spawning shell rather than relying on the tmux server's environment.
tmux new-session -d -s tests \
  "ted tui --parent-agent '$TED_THREAD_ID' --prompt 'Implement the tests'"

# Explicit directory wins over implicit parent-worktree inheritance.
ted tui --parent-agent "$TED_THREAD_ID" --cwd /path/to/checkout \
  --workspace current_checkout

# Explicitly request a fresh worktree rather than sharing the parent's.
ted tui --parent-agent "$TED_THREAD_ID" \
  --workspace worktree --base-branch origin/main
```

Directory/workspace precedence for new TUI agents:

1. `--cwd <path>` selects the directory/source project, as if Ted were launched
   there. Relative paths resolve against the launching process's cwd; absolute
   paths refer to the server filesystem and need not exist on a remote client.
   It is **not** a destination path for generated worktrees.
2. Explicit workspace flags select Local or a fresh Worktree. With `--cwd`
   pointing at an established managed worktree, `current_checkout` means sharing
   that directory, not jumping back to the original checkout.
3. With neither `--cwd` nor workspace flags, a child shares its parent's
   established worktree. No fetch, branch switch, reset, copy, or new worktree is
   performed: both agents see the same branch, files, and uncommitted edits.
4. Otherwise project workspace defaults apply. A parent with an empty worktree
   draft has no worktree to share yet. A parent whose first-send setup is pending
   or failed cannot be inherited: wait for successful setup or select a workspace
   explicitly; there is no silent fallback.

Launching from a server-managed worktree, with or without `--parent-agent`, keeps
its original project identity and shares that tree unless a fresh Worktree is
explicitly requested. This also applies to worktrees owned by settled agents.
Explicit `--cwd` may select another project without changing the child relationship.
Parentage itself does not copy model/effort settings; project defaults and the
existing `--model` / `--effort` flags still apply.

A shared child records its own workspace path and locks it on first send. It does
not follow subsequent parent workspace changes. Resume and server restart reuse
that path; missing/deleted worktrees fail rather than being recreated. An unlocked
shared draft can still replace its inherited location using the existing workspace
endpoint. Concurrent edits in a shared directory are not isolated.

`--parent-agent` cannot be combined with `--resume` or `--continue`. `--cwd` can
select the project for `--continue`, but cannot override an explicit `--resume`.
Neither resume mode changes parentage or the recorded workspace.

### API fields

`POST /v1/agents` additionally accepts:

- `parent_agent_id`: an existing agent ID on this server; immutable after creation.
- `working_directory`: an existing absolute server-local project root or established
  managed worktree path belonging to `project_id`. Omission allows implicit parent
  inheritance when parent and child use the same project. An explicit directory
  suppresses implicit parent inheritance but otherwise uses the selected project's
  defaults, except that an existing managed worktree is shared by default.

The parent ID appears in agent resources, WebSocket inventory, and agent updates;
it is omitted for root agents. `Agent.workspace.shared: true` marks an existing
managed worktree that will never be provisioned by this child. Workspace selection
requests remain `current_checkout` or `worktree`; `shared` is server-owned metadata,
not another CLI mode or a writable workspace-selection field.
