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
after failure. The directory is available in the text’s tooltip, and the sidebar
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
