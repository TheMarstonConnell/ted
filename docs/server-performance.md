# Server performance review

Baseline: `d0a764d5e9ff4cacccb68e5f422978c1242b697a` (before this PR).
Reviewed every one of the **24 OpenAPI operations**, shared persistence/settings/
workspace helpers, WebSocket commands, static serving, and browser-daemon hot paths.
This is a local performance study, **not a production latency SLA or proof that
no further optimization exists**.

## What changed

- Replace whole-object JSON marshal/unmarshal copying with typed, detached
  snapshots. Preserve queue/history ownership, opaque reasoning/tool data,
  private-provenance omission, JSON normalization, and rollback independence.
  Content snapshots reuse their decoded text instead of parsing it again.
- Return only the queue or requested history page when that is all the route
  needs. WebSocket inventory copies summaries rather than entire transcripts;
  the public full `SnapshotEvents` method keeps its existing contract.
- Permit concurrent read snapshots with an `RWMutex`. Build a parent/child map
  once when recursively settling agents, instead of scanning all agents for
  every descendant. Skip idempotency hashing when no key is supplied.
- Validate settings without constructing a runtime, resolving its identity, or
  spawning Git. Keep the existing model/effort validation and fallback rules.
- Share the immutable validated OpenAPI definition and precomputed route/query
  metadata. Reuse strictly parsed queries and bypass unnecessary empty-body
  reader allocations. Still validate every request against the schema.
- Map WebSocket summaries directly into generated types. Decode/validate commands
  once with exact integer lexemes, retaining integer syntax/range checks.
- Normalize workspace choices outside the service lock, then recheck state before
  committing. A concurrent first message still permanently wins the workspace
  lock. Parse the Git remote list only once per branch listing.
- Load the embedded SPA index once per handler. Cache unchanged recording-frame
  decoding, **not frame writes**: fixed-rate video duration stays unchanged.

Atomic checkpoint replacement, file **and** directory `fsync`, acknowledged-write
durability, request/body limits, error schemas, response shapes, Git freshness,
WebSocket backpressure, and browser session serialization are retained.

## Measurement method and limits

- Linux/amd64, AMD Ryzen 7 5700G, Go 1.26.1; `GOMAXPROCS=2`.
- Each route value is the **median of five benchmark mean times**, with 20
  requests per sample. It is **not** per-request p50/p95/p99. Negative Δ means
  lower elapsed time; positive Δ means the measured sample got slower.
- Identical benchmark source runs on the baseline and candidate. State is about
  **917 KiB**, containing 10 agents × 100 transcript messages, 100 queue records,
  and 100 events, plus 2 projects. Read cases reset before each subbenchmark;
  mutations reset from fixture JSON before each iteration, outside timing.
- HTTP measurements run the complete validated handler and response encoding
  through `httptest`, excluding request construction, reset, startup, network,
  and slow-client backpressure. Writes include real production checkpoint
  serialization, file/directory syncing, and rename on this host's filesystem.
- The WebSocket row is a real successful loopback handshake and therefore also
  includes its client/socket overhead. Replay/inventory are measured separately.
- Git branch routes use a local repository with `origin/main`. No benchmark calls
  a live model or GitHub. PR lookup includes both a draft/no-lookup path and a
  ready-worktree/warm-cache path with Git config stubbed. These are **not** cold
  `gh pr list` latency measurements.
- Submit queues behind a running-turn stub; Stop uses a no-op cancel; Continue
  has an empty queue; agent creation has no prompt. First-turn startup, provider
  latency, worktree provisioning, and all mutation branches are not represented
  by a single route number.
- **Disk-sync variance is large on this shared host.** Mutation samples ranged
  from tens to hundreds of milliseconds. Their deltas are observed elapsed-time
  changes, not an isolated causal estimate of CPU savings. Tiny route/Git/PR/
  handshake deltas can be noise; unchanged external work is explicitly labeled.
  Raw samples and allocation counts are committed under [`performance/`](performance/).
  Read/clone allocation reductions and deterministic concurrency tests provide
  stronger evidence than a small elapsed-time difference.

## Every API operation: before and after

The two additional rows cover paged listing and a warm PR lookup. Paths use
placeholder IDs; all cases assert successful response status.

| Method / route | Scenario / changed work | Before ms | After ms | Observed Δ time |
|---|---|---:|---:|---:|
| `GET /health` | Static response; shared validator | 0.0071 | 0.0048 | -32.3% |
| `GET /v1/projects` | 2 projects; shared read lock/validator | 0.0079 | 0.0076 | -3.7% |
| `POST /v1/projects` | New checkout project; settings selector + snapshot | 114.2575 | 57.2685 | -49.9% |
| `GET /v1/projects/{project_id}` | Live local Git branch; subprocess unchanged | 1.2399 | 1.2416 | +0.1% |
| `PATCH /v1/projects/{project_id}` | Name change; snapshot | 102.5650 | 77.2566 | -24.7% |
| `DELETE /v1/projects/{project_id}` | Empty project; snapshot | 23.2992 | 81.5202 | +249.9% |
| `GET /v1/projects/{project_id}/branches` | Local Git origin/main; pre-split remotes | 3.9284 | 3.6640 | -6.7% |
| `GET /v1/agents` | 10 full agents; typed snapshots | 9.7280 | 3.9106 | -59.8% |
| `GET /v1/agents?page=2&page_size=5` | 5 full agents; typed snapshots | 4.7507 | 2.0978 | -55.8% |
| `POST /v1/agents` | No prompt; settings selector + snapshot | 85.7865 | 90.0200 | +4.9% |
| `GET /v1/agents/{agent_id}` | Full queue/history; typed snapshot | 0.9764 | 0.4100 | -58.0% |
| `PATCH /v1/agents/{agent_id}` | Settle one agent; snapshot | 83.3349 | 25.4035 | -69.5% |
| `GET /v1/agents/{agent_id}/pull-request` | Draft worktree: no external lookup | 0.0064 | 0.0070 | +8.9% |
| `GET /v1/agents/{agent_id}/pull-request` | Ready worktree: warm PR cache, stubbed Git config | 0.0185 | 0.0209 | +13.5% |
| `PATCH /v1/agents/{agent_id}/workspace` | Draft to current checkout; snapshot | 116.1679 | 18.6426 | -84.0% |
| `PATCH /v1/agents/{agent_id}/settings` | Effort change; settings selector + snapshot | 126.6852 | 13.5422 | -89.3% |
| `GET /v1/agents/{agent_id}/messages` | 100 queue records; no history copy | 0.8383 | 0.0922 | -89.0% |
| `POST /v1/agents/{agent_id}/messages` | Queue behind running-turn stub; snapshot | 31.7622 | 12.5570 | -60.5% |
| `DELETE /v1/agents/{agent_id}/messages/{message_id}` | Cancel pending item; snapshot | 89.6422 | 12.5968 | -85.9% |
| `POST /v1/agents/{agent_id}/stop` | Running-turn stub, no-op cancel; snapshot | 166.7221 | 13.0986 | -92.1% |
| `POST /v1/agents/{agent_id}/continue` | Held agent, empty queue; snapshot | 31.3357 | 12.7961 | -59.2% |
| `GET /v1/agents/{agent_id}/history` | after=90, limit=10; page-only copy | 0.8039 | 0.0431 | -94.6% |
| `GET /v1/agents/{agent_id}/events` | after=90, limit=10; typed event copy | 0.1019 | 0.0438 | -57.0% |
| `GET /v1/agents/{agent_id}/events/{cursor}` | Event 95; typed event copy | 0.0173 | 0.0108 | -37.9% |
| `GET /v1/models` | 1 fake model; shared validator | 0.0061 | 0.0062 | +1.5% |
| `GET /v1/ws` | Real loopback 101 handshake; not replay | 0.2103 | 0.2083 | -0.9% |

### Regression investigation

The durable DELETE-project sample increased from 23.3 to 81.5 ms; it is not
presented as a route-latency win. A separate identical-fixture measurement with
only persistence stubbed (`BenchmarkDeleteProjectCPU`) reduced handler CPU /
allocation work from **16.810 to 6.878 ms**. The durable path still
writes the same checkpoint and performs the same file/directory sync sequence.
This supports a CPU improvement but does not establish an end-to-end storage
latency improvement. Retest real write latency on deployment storage.

A post-change CPU profile of the full agent-list benchmark attributes about
65% of sampled CPU (cumulative) to JSON compaction and 39% to snapshot message
copying (overlapping stacks). JSON normalization/response bytes are now major
remaining costs; eliminating their scans safely would need a representation or
cache-lifetime change, rather than silently returning different snapshots.

## Helper results

Service microbenchmarks use 100 iterations × 5 samples. SPA, cloning, and recording
helpers use 200 ms × 5 samples. Clone comparisons retain the original JSON
round-trip implementation in the benchmark, and the recording benchmark retains
decode-every-tick as its reference,
so each pair runs on the same fixture/binary. Clone state has 8 agents, 384
messages, and 1,024 events.

| Helper workload | Before | After | Δ time | Before → after B/op |
|---|---:|---:|---:|---:|
| Parallel 100-message agent read (2 CPUs) | 177.195 µs | 32.289 µs | -81.8% | 89,655 → 34,456 |
| Settle 1,000-agent tree (save stubbed) | 29.544 ms | 7.367 ms | -75.1% | 4,653,872 → 3,136,516 |
| WS inventory: 10 × 100-message agents, no replay | 1.709 ms | 2.725 µs | -99.8% | 904,314 → 5,480 |
| SPA GET | 1.529 µs | 1.027 µs | -32.8% | 2,864 → 1,936 |
| SPA HEAD | 0.894 µs | 0.411 µs | -54.0% | 1,504 → 576 |
| Clone durable state | 5.469 ms | 1.075 ms | -80.3% | 1,513,288 → 450,379 |
| Clone full agent | 317.977 µs | 64.750 µs | -79.6% | 88,438 → 29,354 |
| Clone transcript | 292.216 µs | 62.829 µs | -78.5% | 80,338 → 28,170 |
| Clone event page | 327.703 µs | 68.382 µs | -79.1% | 65,931 → 25,605 |
| Decode unchanged 256 KiB recording frame | 229.553 µs | 0.003 µs | <−99.9% | 270,336 → 0 |

The repeated-frame result measures only decoding a 256 KiB unchanged frame, not
Chrome capture, ffmpeg, I/O, or full video generation; its one cached allocation
is amortized. The WebSocket inventory result excludes socket writes/replay.
Tree settling deliberately stubs persistence to isolate traversal/copy cost.

HTTP validation and direct WebSocket mapping also have standalone
`BenchmarkValidateHTTP`, `BenchmarkNewHandler`, `BenchmarkSummaryWS`, and
`BenchmarkValidateWS` benchmarks. Repeated handler construction benefits from
shared setup; the first process-wide schema load still has a cold-start cost.

Post-review simplification removed generic clone and settings-selector façades,
a one-use JSON-whitespace helper, manual shallow clone loops, and manual
`sync.Once` result storage. A standards review superseded the minimalism request
to inline the frame cache: the small production `frameDecoder` remains so the
benchmark invokes real code instead of duplicating its branch. All standalone clone,
HTTP-validation/handler, WebSocket mapping/validation, and recording benchmarks
were rerun for five samples, as were all 26 route scenarios because clone calls
span route paths. Raw output is in `docs/performance/review-simplification.txt`,
`docs/performance/recording-benchmark.txt`, and
`docs/performance/routes-review-after.txt`. The clone and recording rows above
now use these final results. The later route run is a separate shared-host
observation and is not substituted into the sequential route table; it retains
every sample, including every durable-write sample (the final DELETE project run ranged from
68.192 to 75.943 ms). WebSocket command validation and internal conversion's
final median was 5.560 µs; clone allocation counts were unchanged.

## Additional review findings and deliberate limits

| Area reviewed | Disposition / remaining bottleneck |
|---|---|
| All POST/PATCH/DELETE paths | Whole-state checkpoint encoding/sync remains proportional to retained state. A WAL or transactional store could improve this further, but needs migration/recovery design; do not remove durability to improve a benchmark. |
| Agent/project lists | Filtering/sorting and full response bytes remain. Existing pagination is preserved; mandatory pagination or summary-only responses would change the API. Large-cardinality indexes/top-k selection need a scaling workload and transactional maintenance tests. |
| Queue submit/delete/stop | Queue lookup is still linear. Indexing may help very long queues, but was not justified by the bounded fixture; it adds recovery/rollback bookkeeping. |
| Git branch/branch-list routes | Live subprocess work remains. No stale TTL cache or hand-parsed `.git/HEAD` shortcut was added. Concurrent coalescing and parallel Git reads need concurrency/error/freshness measurements. |
| PR discovery | Existing 30-second result cache, singleflight, four-command limiter, and bounded command timeouts remain. Cold GitHub/network latency is unmeasured. Successful origin lookup is intentionally not cached across requests, avoiding a new origin-change delay. |
| Workspace/project creation | Agent workspace validation no longer holds the global mutex during Git. Project create/update and explicit creation-workspace validation can still perform Git under the write lock; moving those needs their own two-phase uniqueness/default/inheritance revalidation. |
| WebSocket `subscribe` / `submit` | Summary copying and typed command decoding improved. Replay volume, per-subscriber wakeups, JSON encoding, socket limits, and durable Submit work remain; no compression or event-loss shortcut. |
| Static GET/HEAD `/`, `/index.html`, `/agents/...`, `/projects/...` | Index preloaded; GET and HEAD measured above. No visible UI changes. |
| Static GET/HEAD `/assets/...` | Existing embedded file server and immutable caching retained; response size/bandwidth dominates. Precompressed assets require build/negotiation changes. |
| Unknown API/static routes and methods | Existing 404/405 handling and strict API error responses retained; method metadata is precomputed. |
| Browser `status`, `session-close`, `open`, `tabs`, tab lifecycle, targeting, `wait`, screenshot, recording, console/errors, CDP | Reviewed the Unix-socket daemon, which is **not an HTTP API**. Only unchanged-frame decoding is optimized here. Chrome/navigation/tool I/O dominates many actions. Target-scan bounds, tab metadata batching, event ring buffers, and project-root coalescing are further candidates requiring workload-specific measurements; they are not claimed as measured improvements. |
| Provider/session/artifact helpers | No provider retry policy, credential/metadata lifetime, artifact ownership/symlink checks, screenshot sync, or session format changes. These have correctness/security contracts beyond route micro-optimization. |

Further speedups remain possible. This pass targets measured local bottlenecks
without silently changing response contents, durability, freshness, or scheduling.
A production follow-up should collect p50/p95/p99 and lock profiles under mixed
reads/writes, large retained histories, real storage, and real Git/provider delays.

## Reproduce

Build embedded assets before repository-wide checks:

```sh
(cd web && npm ci && npm run build)
./api/check-generated.sh
go vet ./...
go test -race ./...
```

For the comparison, copy the benchmark file unchanged to a detached baseline
worktree, and run each revision sequentially (avoid concurrent tests/load):

```sh
git worktree add --detach /tmp/ted-server-before d0a764d5e9ff4cacccb68e5f422978c1242b697a
cp controlplane/routes_benchmark_test.go controlplane/service_benchmark_test.go \
  /tmp/ted-server-before/controlplane/
cp web/embed_benchmark_test.go /tmp/ted-server-before/web/
(cd /tmp/ted-server-before/web && npm ci && npm run build)
(cd /tmp/ted-server-before && GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkRoutes$' -benchmem -benchtime=20x -count=5)
(cd /tmp/ted-server-before && GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench 'BenchmarkParallelAgentRead|BenchmarkSettleTree|BenchmarkWebSocketInventorySnapshot' \
  -benchmem -benchtime=100x -count=5)
(cd /tmp/ted-server-before && GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkDeleteProjectCPU$' -benchmem -benchtime=20x -count=5)
(cd /tmp/ted-server-before && GOMAXPROCS=2 go test ./web -run '^$' \
  -bench '^BenchmarkSPA$' -benchmem -benchtime=200ms -count=5)
GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkRoutes$' -benchmem -benchtime=20x -count=5
GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench 'BenchmarkParallelAgentRead|BenchmarkSettleTree|BenchmarkWebSocketInventorySnapshot' \
  -benchmem -benchtime=100x -count=5
GOMAXPROCS=2 go test ./controlplane -run '^$' \
  -bench '^BenchmarkDeleteProjectCPU$' -benchmem -benchtime=20x -count=5
GOMAXPROCS=2 go test ./controlplane -run '^$' -bench '^BenchmarkClone' \
  -benchmem -benchtime=200ms -count=5
GOMAXPROCS=2 go test ./web ./browser -run '^$' \
  -bench 'BenchmarkSPA|BenchmarkRepeatedFrame' -benchmem -benchtime=200ms -count=5
```

Regression coverage includes mutable snapshot isolation, durable content
round-trip equality, private reasoning provenance, queue/history cursor bounds,
settle-tree behavior, schema validation, exact WebSocket integer conversion,
summary wire equivalence, and blocked workspace validation racing the first send.
The opt-in Chrome/ffmpeg recording integration also verifies usable video output.
