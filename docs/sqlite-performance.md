# SQLite migration performance

Baseline: `d27f0e7f4a3410966fbf918e0615e71d3ac3dbbe` (the optimized JSON
store in PR #22). Candidate: this SQLite migration. Raw output is committed in
[`performance/sqlite/before.txt`](performance/sqlite/before.txt) and
[`performance/sqlite/after.txt`](performance/sqlite/after.txt).

## Method

- Linux/amd64, AMD Ryzen 7 5700G, Go 1.26.1, `GOMAXPROCS=2`, same shared host.
- Identical `controlplane/storage_benchmark_test.go` source on both revisions.
  Each value is the median of **five benchmark means**, ten operations per
  sample—not request p50/p95/p99. Negative Δ means less elapsed time.
- Fixtures have 512-character content payloads, completed queues, held idle
  agents, and no model/browser/network work. Each history count includes that
  many transcript messages, queued records, and replay events per agent.
  Encoded seed sizes are about 0.27 MB, 27.1 MB, and 2.66 MB; temporary path
  lengths account for small size differences visible in the raw output.
- Both revisions seed from the same version-2 JSON format before `NewService`.
  Startup/import, fixture creation, and shutdown are outside timing. The
  candidate automatically migrates into SQLite; baseline remains JSON.
- Read-cursor updates are successful real service mutations. Every iteration
  advances the cursor and appends a new event, without reseeding the database
  between iterations. Timing includes rollback bookkeeping, persistence, and
  the detached full-agent return value. That return value still costs
  proportional to the **target** conversation size.
- Writes use real durable storage: baseline synced file + directory replacement;
  candidate WAL commits with `synchronous=FULL`. Syncing was not disabled.
- Page reads run the complete HTTP handler and response encoding, excluding
  network transport and request construction. Half the agents are settled;
  the request filters to the project and unsettled agents, ordered by update
  time. SQLite query-plan tests also cover every other filter/order combination.

## Results

| Workload | JSON before | SQLite after | Δ time | Before → after allocated bytes/op |
|---|---:|---:|---:|---:|
| Read-cursor update: 10 agents × 20 history rows | 35.496 ms | 5.905 ms | -83.4% | 712,017 → 25,883 |
| Read-cursor update: 1,000 agents × 20 history rows | 482.027 ms | 6.473 ms | -98.7% | 69,704,326 → 25,955 |
| Read-cursor update: 1 agent × 2,000 history rows | 100.803 ms | 13.553 ms | -86.6% | 7,788,964 → 1,780,850 |
| HTTP agent page: 10 agents, page size 10 | 0.602 ms | 0.771 ms | +28.1% | 250,866 → 268,854 |
| HTTP agent page: 1,000 agents, page size 10 | 1.284 ms | 1.368 ms | +6.6% | 506,304 → 499,489 |

The primary gain is removing **unrelated retained state** from the write path:
updating one agent no longer serializes every other conversation. The long-target
case still clones the target's full snapshot as required by the existing API.

Small-list SQL overhead can exceed a tiny in-memory scan. This is not a claim
that every read gets faster: point snapshots and already-offset history/event
pages deliberately remain memory reads. Indexed filtering/order avoids growing
RAM scans/sorts on the HTTP list path, but response JSON and exact filtered
counts still cost work. The measurements include slower samples rather than
selecting only wins. Filesystem contention makes elapsed write deltas noisy;
allocation reduction and bounded touched-row behavior provide additional evidence.

The existing all-route benchmark was adapted to the production SQLite backend
and smoke-tested for successful statuses across all 26 scenarios. Its database
reset is outside timing but performs substantial I/O, so it is **not** the basis
for the storage scaling comparison above.

## Reproduce

Run revisions sequentially, without concurrent local test/benchmark loads when
possible. Build current embedded assets before repository-wide checks. This
storage benchmark targets only `controlplane`, so the detached baseline does not
need the web bundle.

```sh
(cd web && npm ci && npm run build)
git worktree add --detach /tmp/ted-sqlite-before d27f0e7f4a3410966fbf918e0615e71d3ac3dbbe
cp controlplane/storage_benchmark_test.go /tmp/ted-sqlite-before/controlplane/
(cd /tmp/ted-sqlite-before && GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkStorage' -benchmem -benchtime=10x -count=5)
GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkStorage' -benchmem -benchtime=10x -count=5
./api/check-generated.sh
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

The SQLite tests exercise real SQL abort triggers, missing/truncated rows,
query plans against production SQL, legacy migration guards/backups, opaque
provider payloads, indexed read/write coherence, shutdown with a blocked worker,
and abrupt process exit with committed WAL data. See
[storage operations and migration](sqlite-storage.md) for recovery and backup
procedures and remaining limitations.
