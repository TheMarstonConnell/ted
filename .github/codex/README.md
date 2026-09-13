# Codex pull-request reviewer

This ports Yamsap's two-axis review, minimalism pass, and thread resolution,
using the GitHub-hosted adaptation from
[`respawnit/respawn` at `d692859d1abcaf4dd3cedaa9dc002c3b8a1c46af`](https://github.com/respawnit/respawn/tree/d692859d1abcaf4dd3cedaa9dc002c3b8a1c46af/.github/codex)
(the local `respawn-demo` repo). `CODING_STANDARDS.md` is adapted from
[Yamsap at `a89b34e9cdacaf858e35eb6032b80c7b0d2f6700`](https://github.com/TheMarstonConnell/yamsap/blob/a89b34e9cdacaf858e35eb6032b80c7b0d2f6700/CODING_STANDARDS.md).
Ted-specific review context lives in `ted-review-guide.md`; the existing web
design system remains the UI style authority.

The trusted-base workflow performs three tasks whenever a non-draft,
same-repository pull request is opened or updated:

1. Re-evaluate and resolve earlier Codex findings that the new head addressed.
2. Review repository standards and the PR's stated specification separately.
3. Run a second review looking specifically for unnecessary new structure.

Review summaries are maintained as pinned-style issue comments. Actionable
findings are posted inline and deduplicated while their threads remain open.

## Runner prerequisite

The `review` job runs on `ubuntu-latest` (GitHub-hosted). Codex authenticates
from the `CODEX_AUTH_JSON` repository secret, which must contain the JSON from
a one-time `codex login` subscription auth (`~/.codex/auth.json`). Set it from
a machine that already completed login without printing the value to logs:

```bash
gh secret set CODEX_AUTH_JSON --repo TheMarstonConnell/ted < ~/.codex/auth.json
```

Do not commit that file to this repo. The workflow restores it into a
run-scoped `CODEX_HOME`, points the model process's `HOME` at that directory,
and deletes the copy at cleanup; subscription refreshes are discarded with the
ephemeral runner. Create the repository Actions variable `CODEX_REVIEW_ENABLED`
with the value `true` only after the secret exists. Until then, review jobs
skip. Then enable with:

```bash
gh variable set CODEX_REVIEW_ENABLED --repo TheMarstonConnell/ted --body true
```

Enable it only once this directory exists on the base branch: every step
reads its prompts and helpers from the base checkout, so a pull request whose
base commit predates this directory has nothing to read. Subscription auth can
expire or require re-login; if reviews start failing on auth, refresh the
secret from a fresh `codex login`.

This cached-login arrangement mirrors the current Yamsap runner. OpenAI's
[authentication guide](https://learn.chatgpt.com/docs/auth) recommends API-key
authentication as the default for CI/CD automation; Ted can move to that
setup when a dedicated OpenAI project and repository secret are provisioned.

The workflow uses GitHub-hosted ephemeral runners. It does not run for fork pull requests, does not persist checkout credentials,
and does not pass `GITHUB_TOKEN` to Codex. It installs an exact reviewed Codex
version into a per-run prefix with lifecycle scripts disabled, after verifying
the pinned package and Linux binary integrity. Installation then uses only
those local artifacts in offline mode. It restores the Codex login into a per-job
`CODEX_HOME`, points the model process's `HOME` at that run-scoped directory,
applies the trusted permission profile in `ci-config.toml`, and removes that
copy at cleanup.
Model-run commands can read only minimal tool runtime files, the PR checkout,
and workflow-provided context; all other host paths (including the Codex login)
and command networking are denied. Each CLI run is ephemeral, and the
workflow keeps generated review context in a run-and-attempt-scoped directory
under `runner.temp` and removes it at the end of the job.

Those read boundaries use Codex
[permission profiles](https://learn.chatgpt.com/docs/permissions), whose
specific `deny` rules override broader read grants. They are not the legacy
`--sandbox read-only` mode. Do not add `--sandbox` to these invocations: Codex
documents that doing so selects the older sandbox settings instead of the
path-level permission profile that hides `auth.json`.

The workflow uses `pull_request_target` so GitHub loads the workflow definition
from the base branch. Both the trigger and job condition allow only Ted's
`main` PR base; do not broaden that allowlist to contributor-controlled
branches. A push to `main` dispatches a replacement run for each eligible
open PR, and editing an issue dispatches replacements for eligible PRs that
reference it in their title, body, or commits. Reviews therefore do not remain
pinned to an obsolete base revision or issue specification. Commit references
are traversed through GitHub's paginated GraphQL connection rather than the
250-commit REST view. Each run captures the live target and linked-issue text;
posting and resolution compare that complete specification snapshot again
before and after mutations, rolling back a mutation that raced an edit. The run
checks its immutable base SHA into `trusted/` and checks the PR head into the
separate `pull-request/` directory. Executable
helpers, prompts, review guides, and output schemas always come from the
trusted checkout. Codex runs from the PR checkout while consuming those trusted
control assets. The fresh Codex configuration also marks the PR checkout
untrusted, preventing project-local Codex config, hooks, and rules from
overriding the review boundary. It disables automatic project-instruction and
web-search loading as well; repository guidance is inspected only as untrusted
review evidence. Never replace the trusted paths with files from the PR head.

## Specification snapshots

`specification.cjs` owns reference matching, commit traversal, issue
classification, snapshot persistence, and validity. `context.cjs` loads the PR
snapshot and supplies the single-query GraphQL transport for current inputs.
Posting and thread resolution keep using that adapter; neither needs to know
how capture completed or failed.

Capture reads references from the PR title, body, and every commit message.
It recognizes bare `#123`, same-repository `owner/repo#123`, and GitHub issue
URLs, deduplicating positive GraphQL-compatible issue numbers. Referenced PRs
are excluded. Missing and transferred issues remain absent candidates, so an
issue appearing at that number later invalidates the review. Present issues
must retain their title and body, and the PR must retain its head, base, title,
and body and remain open and non-draft.

The capture limit remains 100 candidate lookups and 20 accessible issues.
Commit capture has no page cap, but rejects missing or repeated continuation
cursors and incomplete responses. `issue-specs.json` is the completion record:
capture removes any previous record before fetching and atomically installs
the complete array only after writing the prompt context. An empty array is a
complete specification with no linked issues. A missing or malformed record
cannot authorize review or thread mutations. Failed capture therefore leaves
the failure in the Actions log without replacing an existing review summary
using partial context. Failures after complete capture retain the existing
failure-summary behavior.

Issue-event scheduling uses the same reference semantics, including normalized
numbers such as `#00042`. Its limits remain 10 commit pages per PR and 100 per
event. Exhausting either scan budget includes the PR conservatively; it does
not certify a complete capture. The scheduler loads its helper from a separate
trusted checkout, never from a PR head.

## Reversible thread resolution

`thread-resolution.cjs` owns ownership checks, resolution mutations, rereads,
currency checks, and the undo journal for one sequential review run. The resolver
selects known solved threads and warns on per-thread failures; the poster selects
older duplicate findings and propagates failures.

`resolve(id)` checks and closes a selected thread. `current()` rolls back recorded
resolutions when the review is stale. Before returning true, it also retries any
pending mandatory reopening after a human claim or failed ownership reread,
without reopening other successful resolutions. A failed retry prevents further
closes at that guard. The resolver's final guard retries even when the failure
occurred on its last selected thread. `rollback()` attempts every recorded undo;
entries are removed only after GitHub confirms reopening.

Compensation is best effort, not a GitHub transaction. The journal is in memory
and covers acknowledged writes, not requests with unknown outcomes. Persistent
reopening failures are reported but can still leave a thread closed.

Run the snapshot, currency, posting, and resolution regression tests with:

```bash
node --test .github/codex/*.test.cjs
```

Each finding carries a deterministic ID derived from its review axis, a stable
subject, and the underlying rule or structure—not its current path. Open
finding IDs are also available to later reviews for reuse. The posting helper
stores the ID in a hidden comment marker, so later reviews suppress the same
open finding even when the model changes its prose or the code is renamed or
moved.

The helper files retain the hosted adaptation's explicit `.cjs` CommonJS
extension so the copied helpers and tests run directly under Node without a
root npm project.
