const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const postReview = require('./post-review.cjs');
const { reviewCurrentCheck } = require('./review-current.cjs');

const snapshot = {
  number: 7,
  title: 'Honor issue #42',
  body: 'Requested behavior',
  state: 'open',
  draft: false,
  head: { sha: 'head' },
  base: { ref: 'main', sha: 'base' },
};
const context = {
  repo: { owner: 'TheMarstonConnell', repo: 'ted' },
  payload: { pull_request: snapshot },
};

function graphqlPr(pr = snapshot) {
  return {
    headRefOid: pr.head.sha,
    baseRefName: pr.base.ref,
    baseRefOid: pr.base.sha,
    state: pr.state.toUpperCase(),
    isDraft: pr.draft,
    title: pr.title,
    body: pr.body,
  };
}

async function withContextFiles(run) {
  const previous = process.env.CTX_DIR;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-current-'));
  process.env.CTX_DIR = dir;
  fs.writeFileSync(`${dir}/pull-request.json`, JSON.stringify(snapshot));
  try {
    return await run(dir);
  } finally {
    if (previous === undefined) delete process.env.CTX_DIR;
    else process.env.CTX_DIR = previous;
    fs.rmSync(dir, { recursive: true });
  }
}

test('currentness includes PR and linked-issue specification text', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, JSON.stringify([
      { number: 42, title: 'Requirement', body: 'Must recover' },
    ]));
    const livePr = graphqlPr();
    const liveIssue = { title: 'Requirement', body: 'Must recover' };
    const warnings = [];
    const github = {
      graphql: async () => ({ repository: {
        pullRequest: livePr,
        i0: liveIssue,
      } }),
    };
    const isCurrent = reviewCurrentCheck({
      github,
      context,
      core: { warning: (message) => warnings.push(message) },
    });

    assert.equal(await isCurrent(), true);
    livePr.body = 'Changed request';
    assert.equal(await isCurrent(), false);
    livePr.body = snapshot.body;
    liveIssue.title = 'Changed requirement';
    assert.equal(await isCurrent(), false);
    assert.equal(warnings.length, 2);
  });
});

test('currentness snapshots the PR and linked issues in one query', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, JSON.stringify([
      { number: 42, title: 'Requirement', body: 'Must recover' },
    ]));
    let calls = 0;
    const github = {
      graphql: async () => {
        calls++;
        return { repository: {
          pullRequest: { ...graphqlPr(), title: 'Edited request' },
          i0: { title: 'Requirement', body: 'Must recover' },
        } };
      },
    };
    const isCurrent = reviewCurrentCheck({
      github,
      context,
      core: { warning: () => {} },
    });

    assert.equal(await isCurrent(), false);
    assert.equal(calls, 1);
  });
});

test('currentness requires absent issue candidates to remain absent', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, JSON.stringify([
      { number: 42, absent: true },
    ]));
    let liveIssue = null;
    const github = {
      graphql: async () => ({ repository: {
        pullRequest: graphqlPr(),
        i0: liveIssue,
      } }),
    };
    const isCurrent = reviewCurrentCheck({
      github,
      context,
      core: { warning: () => {} },
    });

    assert.equal(await isCurrent(), true);
    liveIssue = { title: 'Now present', body: 'New requirements' };
    assert.equal(await isCurrent(), false);
  });
});

test('currentness tolerates GraphQL NOT_FOUND for absent issue candidates', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, JSON.stringify([
      { number: 4645, absent: true },
      { number: 42, title: 'Requirement', body: 'Must recover' },
    ]));
    const notFound = (alias) => ({
      type: 'NOT_FOUND',
      path: ['repository', alias],
      message: `Could not resolve to an Issue with the number of ${alias}.`,
    });
    let missing = ['i0'];
    const github = {
      graphql: async () => {
        const data = { repository: {
          pullRequest: graphqlPr(),
          i0: null,
          i1: missing.includes('i1') ? null :
            { title: 'Requirement', body: 'Must recover' },
        } };
        throw Object.assign(new Error('Request failed'), {
          errors: missing.map(notFound),
          data,
        });
      },
    };
    const isCurrent = reviewCurrentCheck({
      github,
      context,
      core: { warning: () => {} },
    });

    assert.equal(await isCurrent(), true);
    missing = ['i0', 'i1'];
    assert.equal(await isCurrent(), false);
  });
});

test('currentness rethrows GraphQL errors that are not missing issues', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, JSON.stringify([
      { number: 4645, absent: true },
    ]));
    let failure;
    const github = {
      graphql: async () => { throw failure; },
    };
    const isCurrent = reviewCurrentCheck({
      github,
      context,
      core: { warning: () => {} },
    });

    for (const errors of [
      [{ type: 'RATE_LIMITED', path: ['repository'] }],
      [{ type: 'NOT_FOUND', path: ['repository', 'pullRequest'] }],
      [{ type: 'NOT_FOUND', path: ['repository'] }],
      [{ type: 'NOT_FOUND' }],
      [
        { type: 'NOT_FOUND', path: ['repository', 'i0'] },
        { type: 'RATE_LIMITED', path: ['repository'] },
      ],
    ]) {
      failure = Object.assign(new Error('Request failed'), {
        errors,
        data: { repository: { pullRequest: graphqlPr(), i0: null } },
      });
      await assert.rejects(isCurrent(), (error) => error === failure);
    }

    failure = Object.assign(new Error('No partial data'), {
      errors: [{ type: 'NOT_FOUND', path: ['repository', 'i0'] }],
    });
    await assert.rejects(isCurrent(), (error) => error === failure);
  });
});

test('inline comments are removed when the final currentness guard fails', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, '[]');
    const reviewFile = `${dir}/review.json`;
    fs.writeFileSync(reviewFile, JSON.stringify({
      summary: '## standards\nOK\n\n## spec\nOne issue',
      findings: [{
        finding_id: 'spec:target:rule',
        path: 'file.txt',
        line: 1,
        axis: 'spec',
        severity: 'suggestion',
        body: 'Fix this.',
      }],
    }));
    const previousReviewFile = process.env.REVIEW_FILE;
    process.env.REVIEW_FILE = reviewFile;
    let currentnessCalls = 0;
    let submittedReview;
    const paginatedRoutes = [];
    const requests = [];
    const listFiles = async () => {};
    const listComments = async () => {};
    const github = {
      paginate: async (method, values) => {
        if (method === listFiles) return [{
          filename: 'file.txt',
          patch: '@@ -0,0 +1 @@\n+content',
        }];
        paginatedRoutes.push({ route: method, values });
        return [{ id: 99 }];
      },
      graphql: async (document) => {
        if (document.includes('reviewThreads')) {
          return { repository: { pullRequest: { reviewThreads: {
            nodes: [],
            pageInfo: { hasNextPage: false, endCursor: null },
          } } } };
        }
        currentnessCalls++;
        const pullRequest = graphqlPr();
        if (currentnessCalls === 4) pullRequest.title = 'Edited request';
        return { repository: { pullRequest } };
      },
      request: async (route, values) => {
        requests.push({ route, values });
        return { data: null };
      },
      rest: {
        pulls: {
          listFiles,
          createReview: async (review) => {
            submittedReview = review;
            return { data: { id: 55 } };
          },
        },
        issues: { listComments },
      },
    };

    try {
      await postReview({
        github,
        context,
        core: { warning: () => {} },
      });
    } finally {
      if (previousReviewFile === undefined) delete process.env.REVIEW_FILE;
      else process.env.REVIEW_FILE = previousReviewFile;
    }

    assert.equal(currentnessCalls, 4);
    assert.equal(Object.hasOwn(submittedReview, 'body'), false);
    assert.equal(paginatedRoutes.length, 1);
    assert.match(paginatedRoutes[0].route, /reviews\/\{review_id\}\/comments/);
    assert.equal(paginatedRoutes[0].values.per_page, 100);
    assert.deepEqual(requests.map(({ route }) => route), [
      'DELETE /repos/{owner}/{repo}/pulls/comments/{comment_id}',
    ]);
    assert.equal(requests[0].values.comment_id, 99);
  });
});

test('later concurrent inline findings are resolved after submission', async () => {
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, '[]');
    const reviewFile = `${dir}/review.json`;
    fs.writeFileSync(reviewFile, JSON.stringify({
      summary: '## standards\nOK\n\n## spec\nOne issue',
      findings: [{
        finding_id: 'spec:target:rule',
        path: 'file.txt',
        line: 1,
        axis: 'spec',
        severity: 'suggestion',
        body: 'Fix this.',
      }],
    }));
    const previousReviewFile = process.env.REVIEW_FILE;
    process.env.REVIEW_FILE = reviewFile;
    const listFiles = async () => {};
    const listComments = async () => {};
    let threadFetches = 0;
    const resolved = [];
    const findingBody =
      '<!-- codex-finding:spec:target:rule -->\n' +
      '**[spec / suggestion]** Fix this.';
    const thread = (id, createdAt) => ({
      id: `thread-${id}`,
      isResolved: false,
      comments: {
        nodes: [{
          id: `comment-${id}`,
          createdAt,
          body: findingBody,
          path: 'file.txt',
          line: 1,
          originalLine: 1,
          author: { login: 'github-actions', __typename: 'Bot' },
        }],
        pageInfo: { hasNextPage: false, endCursor: null },
      },
    });
    const github = {
      paginate: async (method) => {
        if (method === listFiles) return [{
          filename: 'file.txt',
          patch: '@@ -0,0 +1 @@\n+content',
        }];
        if (method === listComments) return [];
        return [];
      },
      graphql: async (document, variables) => {
        if (document.includes('resolveReviewThread')) {
          resolved.push(variables.threadId);
          return { resolveReviewThread: { clientMutationId: null } };
        }
        if (document.includes('reviewThreads')) {
          threadFetches++;
          const nodes = threadFetches === 1 ? [] : [
            thread('old', '2026-01-01T00:00:00Z'),
            thread('new', '2026-01-01T00:00:01Z'),
          ];
          return { repository: { pullRequest: { reviewThreads: {
            nodes,
            pageInfo: { hasNextPage: false, endCursor: null },
          } } } };
        }
        if (document.includes('node(id: $id)')) {
          const id = variables.id.replace('thread-', '');
          return { node: thread(id, id === 'old' ?
            '2026-01-01T00:00:00Z' : '2026-01-01T00:00:01Z') };
        }
        return { repository: { pullRequest: graphqlPr() } };
      },
      rest: {
        pulls: {
          listFiles,
          createReview: async () => ({ data: { id: 55 } }),
        },
        issues: {
          listComments,
          createComment: async ({ body }) => ({ data: { id: 10, body } }),
          deleteComment: async () => {},
        },
      },
    };

    try {
      await postReview({ github, context, core: { warning: () => {} } });
    } finally {
      if (previousReviewFile === undefined) delete process.env.REVIEW_FILE;
      else process.env.REVIEW_FILE = previousReviewFile;
    }

    assert.equal(threadFetches, 2);
    assert.deepEqual(resolved, ['thread-old']);
  });
});

async function runSummaryReplacement(createId) {
  let deleted;
  await withContextFiles(async (dir) => {
    fs.writeFileSync(`${dir}/issue-specs.json`, '[]');
    const reviewFile = `${dir}/review.json`;
    fs.writeFileSync(reviewFile, JSON.stringify({
      summary: '## standards\nOK\n\n## spec\nOK',
      findings: [],
    }));
    const previousReviewFile = process.env.REVIEW_FILE;
    process.env.REVIEW_FILE = reviewFile;
    deleted = [];
    const marker = '\u003c!-- codex-two-axis-review --\u003e\n';
    const listFiles = async () => {};
    const listComments = async () => {};
    const github = {
      paginate: async (method) => method === listFiles ? [] : [
        { id: 10, user: { type: 'Bot', login: 'github-actions[bot]' }, body: marker },
        { id: 30, user: { type: 'Bot', login: 'github-actions[bot]' }, body: marker },
      ],
      graphql: async (document) => {
        if (document.includes('reviewThreads')) {
          return { repository: { pullRequest: { reviewThreads: {
            nodes: [],
            pageInfo: { hasNextPage: false, endCursor: null },
          } } } };
        }
        return { repository: { pullRequest: graphqlPr() } };
      },
      rest: {
        pulls: {
          listFiles,
        },
        issues: {
          listComments,
          createComment: async ({ body }) => ({ data: { id: createId, body } }),
          deleteComment: async ({ comment_id: id }) => deleted.push(id),
        },
      },
    };

    try {
      await postReview({
        github,
        context,
        core: { warning: () => {} },
      });
    } finally {
      if (previousReviewFile === undefined) delete process.env.REVIEW_FILE;
      else process.env.REVIEW_FILE = previousReviewFile;
    }
  });
  return deleted;
}

test('summary replacement yields to a newer concurrent summary', async () => {
  assert.deepEqual(await runSummaryReplacement(20), [20]);
});

test('summary replacement removes older summaries when it wins', async () => {
  assert.deepEqual(await runSummaryReplacement(40), [10, 30]);
});
