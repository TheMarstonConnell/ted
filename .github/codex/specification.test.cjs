const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { captureSpecification, selectIssuePullRequests } = require('./specification.cjs');
const { loadIssueSpecs, reviewInputsAreCurrent } = require('./context.cjs');
const { reviewCurrentCheck } = require('./review-current.cjs');
const postReview = require('./post-review.cjs');

const repo = { owner: 'TheMarstonConnell', repo: 'ted' };
const pr = {
  number: 7, title: 'Requested change', body: '',
  head: { sha: 'head' }, base: { ref: 'main', sha: 'base' },
};
const livePr = () => ({
  headRefOid: 'head', baseRefName: 'main', baseRefOid: 'base',
  state: 'OPEN', isDraft: false, title: pr.title, body: pr.body,
});
const issue = (number) => ({
  number, title: `Requirement ${number}`, body: `Implement ${number}`,
  repository_url: 'https://api.github.com/repos/TheMarstonConnell/ted',
});
const page = (messages = [], hasNextPage = false, endCursor = null) => ({
  repository: { pullRequest: { commits: {
    nodes: messages.map((message) => ({ commit: { message } })),
    pageInfo: { hasNextPage, endCursor },
  } } },
});

function fixture(t, { messages = [], lookup = issue, pages } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-spec-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const lookups = [];
  const cursors = [];
  const github = {
    graphql: async (_query, variables) => {
      cursors.push(variables.cursor);
      return pages ? pages(cursors.length, variables) : page(messages);
    },
    rest: { issues: { get: async ({ issue_number: number }) => {
      lookups.push(number);
      return { data: lookup(number) };
    } } },
  };
  return { dir, github, lookups, cursors, repo, pr };
}

async function current(f, repository = {}) {
  return reviewInputsAreCurrent({
    github: { graphql: async () => ({ repository: {
      pullRequest: livePr(), ...repository,
    } }) }, repo, pr: f.pr, issueSpecs: loadIssueSpecs(f.dir),
  });
}

test('capture and scheduling share repository reference semantics', async (t) => {
  const f = fixture(t, { messages: [
    '#00042 TheMarstonConnell/ted#43 https://github.com/THEMARSTONCONNELL/TED/issues/44',
    'github.com/TheMarstonConnell/ted/issues/45 #42 other/project#46',
    'https://github.com/other/project/issues/47 #48suffix #49-hyphen',
    '#0 #2147483648 #9007199254740993',
  ] });
  f.pr = { ...pr, title: 'Change (#40)', body: 'See #41' };
  await captureSpecification(f);
  assert.deepEqual(f.lookups, [40, 41, 42, 43, 44, 45]);
  for (const number of [40, 41, 42, 43, 44, 45, 46, 47, 48, 49]) {
    const selected = await selectIssuePullRequests({
      ...f, pulls: [f.pr], issueNumber: number, core: { warning: assert.fail },
    });
    assert.equal(selected.length, number <= 45 ? 1 : 0, `issue ${number}`);
  }
});

test('complete capture persists present, absent, and transferred issues for validity', async (t) => {
  const f = fixture(t, { messages: ['#42 #43 #44 #45 #46 #47 #48'], lookup: (n) => {
    if ([43, 44, 45].includes(n)) {
      throw Object.assign(new Error('Unavailable'), { status: {43: 404, 44: 410, 45: 301}[n] });
    }
    if (n === 46) return { ...issue(n), repository_url: 'https://api.github.com/repos/other/repo' };
    if (n === 47) return { ...issue(n), pull_request: {} };
    return { ...issue(n), body: n === 48 ? null : issue(n).body };
  } });
  await captureSpecification(f);
  assert.deepEqual(loadIssueSpecs(f.dir), [
    { number: 42, title: 'Requirement 42', body: 'Implement 42' },
    ...[43, 44, 45, 46].map((number) => ({ number, absent: true })),
    { number: 48, title: 'Requirement 48', body: '' },
  ]);
  assert.equal(fs.statSync(`${f.dir}/issue-specs.json`).mode & 0o777, 0o600);
  assert.match(fs.readFileSync(`${f.dir}/issues.md`, 'utf8'), /# Issue #42/);
  assert.doesNotMatch(fs.readFileSync(`${f.dir}/issues.md`, 'utf8'), /# Issue #4[3-7]/);
  const live = { i0: issue(42), i1: null, i2: null, i3: null, i4: null,
    i5: { ...issue(48), body: null } };
  assert.equal((await current(f, live)).issuesAreCurrent, true);
  assert.equal((await current(f, { ...live, i0: null })).issuesAreCurrent, false);
  assert.equal((await current(f, { ...live, i1: issue(43) })).issuesAreCurrent, false);
  assert.equal((await current(f, { ...live, i4: issue(46) })).issuesAreCurrent, false);
  assert.equal((await current(f, { ...live, i0: { ...issue(42), body: 'Edited' } })).issuesAreCurrent, false);
});

test('an empty complete capture is current, but changed PR inputs are not', async (t) => {
  const f = fixture(t);
  await captureSpecification(f);
  assert.deepEqual(loadIssueSpecs(f.dir), []);
  assert.equal(fs.readFileSync(`${f.dir}/issues.md`, 'utf8'), '');
  assert.equal((await current(f)).prIsCurrent, true);
  for (const change of [
    { headRefOid: 'new' }, { baseRefOid: 'new' }, { baseRefName: 'other' },
    { state: 'CLOSED' }, { isDraft: true }, { title: 'Edited' }, { body: 'Edited' },
  ]) {
    assert.equal((await current(f, { pullRequest: { ...livePr(), ...change } })).prIsCurrent, false);
  }
});

test('capture traverses beyond REST and scheduling page limits', async (t) => {
  const f = fixture(t, { pages: (n) => page(
    Array.from({ length: 100 }, () => n === 12 ? '#42' : 'Unrelated commit'),
    n < 12, `cursor-${n}`,
  ) });
  await captureSpecification(f);
  assert.deepEqual(f.lookups, [42]);
  assert.deepEqual(f.cursors, [null, ...Array.from({ length: 11 }, (_, i) => `cursor-${i + 1}`)]);
  assert.equal((await current(f, { i0: issue(42) })).issuesAreCurrent, true);
});

test('missing or repeating continuation cursors never complete a capture', async (t) => {
  for (const cursor of [null, '', 'repeated']) {
    const f = fixture(t, { pages: () => page(['#42'], true, cursor) });
    await assert.rejects(captureSpecification(f), /Incomplete PR commit pagination/);
    assert.equal(loadIssueSpecs(f.dir), null);
    assert.equal((await current(f)).prIsCurrent, false);
    assert.ok(f.cursors.length <= 2);
  }
});

test('partial commit/API failures invalidate even an earlier successful capture', async (t) => {
  for (const failure of ['commits', 'issues']) {
    const f = fixture(t);
    await captureSpecification(f);
    if (failure === 'commits') {
      let calls = 0;
      f.github.graphql = async () => {
        if (++calls === 1) return page(['#42'], true, 'next');
        throw new Error('API unavailable');
      };
    } else {
      f.github.graphql = async () => page(['#42 #43']);
      f.github.rest.issues.get = async ({ issue_number: n }) => {
        if (n === 43) throw Object.assign(new Error('API unavailable'), { status: 403 });
        return { data: issue(n) };
      };
    }
    await assert.rejects(captureSpecification(f), /API unavailable/);
    assert.equal(loadIssueSpecs(f.dir), null);
    const previous = process.env.CTX_DIR;
    process.env.CTX_DIR = f.dir;
    try {
      const isCurrent = reviewCurrentCheck({
        github: { graphql: assert.fail }, context: { repo, payload: { pull_request: pr } },
        core: { warning: () => {} },
      });
      assert.equal(await isCurrent(), false);
    } finally {
      if (previous === undefined) delete process.env.CTX_DIR;
      else process.env.CTX_DIR = previous;
    }
  }
});

test('capture rejects incomplete commit data', async (t) => {
  for (const response of [
    { repository: { pullRequest: null } },
    { repository: { pullRequest: { commits: { nodes: [], pageInfo: {} } } } },
    { repository: { pullRequest: { commits: { nodes: [null], pageInfo: { hasNextPage: false } } } } },
  ]) {
    const f = fixture(t, { pages: () => response });
    await assert.rejects(captureSpecification(f), /Incomplete PR commit connection/);
    assert.equal(loadIssueSpecs(f.dir), null);
  }
});

test('capture enforces accessible-issue and lookup budgets without partial snapshots', async (t) => {
  for (const [count, absent, succeeds] of [[20, false, true], [21, false, false], [100, true, true], [101, true, false]]) {
    const f = fixture(t, {
      messages: [Array.from({ length: count }, (_, i) => `#${i + 1}`).join(' ')],
      lookup: (n) => {
        if (absent) throw Object.assign(new Error('Missing'), { status: 404 });
        return issue(n);
      },
    });
    if (succeeds) {
      await captureSpecification(f);
      assert.equal(loadIssueSpecs(f.dir).length, count);
    } else {
      await assert.rejects(captureSpecification(f), /Review not published/);
      assert.equal(loadIssueSpecs(f.dir), null);
    }
    assert.ok(f.lookups.length <= 100);
  }
});

test('scheduler conservatively includes unscanned PRs at both unchanged budgets', async (t) => {
  const f = fixture(t, { pages: (n) => page([], true, `cursor-${n}`) });
  const pulls = Array.from({ length: 12 }, (_, i) => ({ ...pr, number: i + 1 }));
  const warnings = [];
  const selected = await selectIssuePullRequests({ ...f, pulls, issueNumber: 42,
    core: { warning: (message) => warnings.push(message) } });
  assert.deepEqual(selected, pulls);
  assert.equal(f.cursors.length, 100);
  assert.equal(warnings.length, 12);
});

test('a terminal page at the scheduling limit is a complete non-match', async (t) => {
  const f = fixture(t, { pages: (n) => page([], n < 10, `cursor-${n}`) });
  assert.deepEqual(await selectIssuePullRequests({ ...f, pulls: [pr], issueNumber: 42,
    core: { warning: assert.fail } }), []);
  assert.equal(f.cursors.length, 10);
});

test('missing or malformed persisted specifications fail closed', async (t) => {
  const f = fixture(t);
  assert.equal(loadIssueSpecs(f.dir), null);
  for (const raw of ['{', '{}', '[null]', '[{"number":42}]']) {
    fs.writeFileSync(`${f.dir}/issue-specs.json`, raw);
    assert.equal(loadIssueSpecs(f.dir), null);
    assert.equal((await current(f)).prIsCurrent, false);
  }
});

test('prompt persistence failure does not publish the completion record', async (t) => {
  const f = fixture(t);
  fs.mkdirSync(`${f.dir}/issues.md`);
  await assert.rejects(captureSpecification(f), { code: 'EISDIR' });
  assert.equal(loadIssueSpecs(f.dir), null);
  assert.equal((await current(f)).prIsCurrent, false);
});

test('failure posting requires a completed capture for either reviewer', async (t) => {
  const f = fixture(t);
  const values = {
    CTX_DIR: f.dir, REVIEW_FILE: `${f.dir}/review.json`,
    REVIEW_COMMAND_OUTCOME: 'skipped', REVIEW_KIND: 'codex',
  };
  const previous = Object.fromEntries(Object.keys(values).map((key) => [key, process.env[key]]));
  Object.assign(process.env, values);
  t.after(() => {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  });
  const posted = [];
  const github = {
    graphql: async () => ({ repository: { pullRequest: livePr() } }),
    paginate: async () => [],
    rest: { issues: {
      createComment: async ({ body }) => {
        posted.push(body);
        return { data: { id: posted.length, body } };
      },
      deleteComment: assert.fail,
    } },
  };
  for (const kind of ['codex', 'minimalism']) {
    process.env.REVIEW_KIND = kind;
    await postReview({ github, context: { repo, payload: { pull_request: pr } },
      core: { warning: () => {} } });
  }
  assert.deepEqual(posted, []);
  await captureSpecification(f);
  process.env.REVIEW_COMMAND_OUTCOME = 'failure';
  for (const kind of ['codex', 'minimalism']) {
    process.env.REVIEW_KIND = kind;
    await postReview({ github, context: { repo, payload: { pull_request: pr } },
      core: { warning: () => {} } });
  }
  assert.equal(posted.length, 2);
  for (const body of posted) assert.match(body, /Review failed for head/);
});
