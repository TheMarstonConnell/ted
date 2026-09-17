# SQLite control-plane storage

The control plane stores projects, agents, queues, transcripts, replay events,
and idempotency receipts in `state.sqlite` under `--data-dir` (default
`$TED_HOME/controlplane`). Browser artifacts stay in the existing `runtime/`
subdirectory. The standalone `agent` package's legacy session files are unchanged.

This replaces the JSON checkpoint used in the [earlier performance review](server-performance.md).
See the [SQLite before/after measurements](sqlite-performance.md) for reproducible
write-scaling and indexed-read results.

## Transactions and ownership

Each durable mutation writes only its changed project/agent metadata, edited
queue entries, and appended conversation/event rows. Queue state, associated
replay events, and idempotency receipts commit in one transaction. A turn's
running reservation commits **before** the model or tools can execute.

SQLite uses WAL, `synchronous=FULL`, foreign keys, and a bounded busy timeout.
The existing process-level `state.lock` still permits only one Ted server to
own a data directory: SQLite's ability to accept multiple connections is not
permission to start duplicate workers. Storage failures reject acknowledgement,
restore the affected in-memory records, and cancel active work. SQLite is closed
only after workers have stopped, before the directory lock is released.

The runtime retains a coherent in-memory mirror for active work and detached
snapshots. This is **not** lazy loading of all conversation history: startup and
resident memory still grow with retained data. The main change is that each write
no longer clones, serializes, or replaces unrelated history.

## Tables and access paths

| Table | Data / primary access |
|---|---|
| `projects` | Project metadata; ID primary key and unique canonical root. |
| `agents` | Small metadata records, project/parent references, ordering/filter fields, and artifact-consumption offset. Queue/transcript contents are separate. |
| `queue_messages` | One queued-message record per ordinal, with unique `(agent_id, message_id)` lookup and a pending-only ordered index. |
| `transcript_messages` | One opaque conversation message per `(agent_id, ordinal)`. |
| `events` | One replay event per `(agent_id, cursor)`. |
| `receipts` | Unique existing idempotency scope; references its agent and, for submissions, its queued message. |
| `store_metadata` | SQLite schema/initialization authority marker. |

Agent listing has separate indexes for created/recent ordering, global/project
scope, and all/unsettled filters. Seconds and nanoseconds are separate integer
columns, preserving exact chronological order without Unix-nanosecond overflow
or variable-width timestamp string ordering. Indexed columns are checked against
stored metadata when loading. Parent/receipt-owner indexes support foreign-key
checks and deletion. Indexes are selected for actual access paths, not every field.

HTTP lists select matching IDs/counts through SQLite before cloning the selected
runtime records. Message-ID, first-pending, and project-root lookups use SQL
indexes. Point snapshots and already-offset history/event pages use the memory
mirror; no SQL lookup is needed to improve an existing map/slice access. The
existing Go `Agents`/`AgentsPage` snapshot helpers keep their signatures and RAM
behavior, including use after `Close`.

## Automatic migration

Stop the old server before upgrading. Allow space for the original JSON, its
backup, the SQLite database, and its WAL during import; migration temporarily
uses more disk space than the original checkpoint. On first opening the directory, the new
server validates and imports legacy JSON versions 1 and 2. Version-1 read-status
migration occurs during import, preserving the old read-status behavior.

For a legacy store:

1. Validate/import the state inside an uncommitted SQLite transaction.
2. Save the exact original JSON bytes as `state.json.pre-sqlite`, with private
   permissions, and sync that backup.
3. Atomically write and sync a pending SQLite guard to `state.json`.
4. Commit the SQL authority marker and imported rows together.
5. Atomically write and sync an active guard before the service accepts requests.

A fresh directory has no legacy backup. Pending guards distinguish that empty
seed from a legacy import. On restart, an initialized SQLite database wins; a
pending guard is completed without replaying the import. Pending/uninitialized
imports recover from their recorded seed. An active guard with missing, corrupt,
unsupported, or uninitialized SQLite state fails closed—**it must not restore a
stale JSON backup automatically**. An ordinary `state.json` appearing alongside
initialized SQLite is treated as conflicting state, not silently reimported.

The guard has a JSON version unsupported by old Ted binaries, preventing an
accidental old-server start against stale JSON. It is not a current checkpoint.
Do not edit, delete, or replace it to bypass this protection.

## Backup, restore, and deployment constraints

- **Use a local filesystem with reliable SQLite locking and syncing.** WAL on
  NFS/SMB/shared network storage is unsupported. Ted does not certify a mount's
  locking semantics; verify the deployment filesystem. Multiple app servers
  sharing this store are unsupported.
- On Unix, the database, WAL/SHM, guard, and legacy backup are private (0600).
  Keep the data directory restricted; use equivalent access controls on Windows.
  Known database/sidecar symlinks are rejected. Transcripts/tool outputs may
  contain secrets; this is not encrypted storage.
- For a simple current backup, stop Ted and wait for a successful clean close,
  then copy the entire data directory. Include artifacts if they are needed for
  restoration. Copy database and any surviving sidecars as one stopped-store
  snapshot, preserving permissions.
- For online backups use a SQLite-aware backup facility, not an ordinary copy of
  `state.sqlite`. Committed work can reside in `state.sqlite-wal`; copying just
  the main file while the server runs can lose acknowledged writes.
- Restore a complete current backup into a separate, stopped data directory,
  preserving the active guard and database together. Do not combine database,
  WAL, or guard files from different backups or edit a live database behind the
  runtime mirror.
- `state.json.pre-sqlite` is the **pre-migration recovery point**, not an up-to-date
  backup. Restoring it intentionally loses all later SQLite writes. If that is
  the recovery you want, retain the current directory for investigation, create
  a fresh directory, and place a copy of the backup there as `state.json` before
  starting the new server.
- Downgrading an already-used SQLite store to an old Ted binary is unsupported;
  there is no current-state JSON exporter in this change. Do not replace the
  guard with the pre-migration backup and continue in place.

## Remaining bounds

Writes remain serialized by the runtime mutex and SQLite writer. WAL does not
remove disk-sync cost or guarantee a latency improvement on every filesystem.
The transcript/event/queue retention policy is unchanged: there is no automatic
pruning or quota. Full snapshots still cost proportional to response size;
startup still reconstructs and validates retained state. Large offset pages and
exact filtered counts retain their usual costs even when indexed. Other runtime
operations, such as subtree traversal and project-deletion guard checks, still
use the memory mirror.

Any future history editing or pruning must explicitly update the transaction
change records; current event/transcript prefixes are immutable and only suffixes
are appended. Do not reintroduce full-store validation/scans on every commit.
