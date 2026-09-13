const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { threadResolution } = require('./thread-resolution.cjs');
const { fetchThread } = require('./threads.cjs');
const resolveThreads = require('./resolve-threads.cjs');
const postReview = require('./post-review.cjs');

const snapshot = {
  number: 7, title: 'Review request', body: 'Requirements',
  state: 'open', draft: false,
  head: { sha: 'head' }, base: { ref: 'main', sha: 'base' },
};
const context = {
  repo: { owner: 'TheMarstonConnell', repo: 'ted' },
  payload: { pull_request: snapshot },
};
const finding = {
  finding_id: 'spec:target:rule', path: 'file.txt', line: 1,
  axis: 'spec', severity: 'suggestion', body: 'Fix this.',
};
const thread = (id, createdAt = id) => ({
  id, isResolved: false,
  comments: {
    pageInfo: { hasNextPage: false, endCursor: null },
    nodes: [{
      id: `comment-${id}`, createdAt,
      body: `<!-- codex-finding:${finding.finding_id} -->\n**[spec / suggestion]** Fix this.`,
      path: 'file.txt', line: 1, originalLine: 1,
      author: { login: 'github-actions', __typename: 'Bot' },
    }],
  },
});
const claim = (value) => value.comments.nodes.push({
  body: 'Please leave this open', author: { login: 'person', __typename: 'User' },
});

function fixture(ids = ['a', 'b']) {
  const state = {
    threads: new Map(ids.map((id) => [id, thread(id)])),
    current: true, events: [], warnings: [],
    unavailableNodes: new Set(),
    onResolve: () => {}, onRead: () => {}, onReopen: () => {},
  };
  const github = {
    graphql: async (document, variables) => {
      if (document.includes('unresolveReviewThread')) {
        const id = variables.threadId;
        state.events.push(`reopen:${id}`);
        await state.onReopen(id);
        state.threads.get(id).isResolved = false;
        return {};
      }
      if (document.includes('resolveReviewThread')) {
        const id = variables.threadId;
        state.events.push(`resolve:${id}`);
        state.threads.get(id).isResolved = true;
        await state.onResolve(id);
        return {};
      }
      if (document.includes('reviewThreads')) {
        return { repository: { pullRequest: { reviewThreads: {
          nodes: structuredClone([...state.threads.values()]),
          pageInfo: { hasNextPage: false, endCursor: null },
        } } } };
      }
      if (document.includes('node(id: $id)')) {
        if (state.unavailableNodes.has(variables.id)) return { node: null };
        const value = state.threads.get(variables.id);
        await state.onRead(value);
        return { node: structuredClone(value) };
      }
      return { repository: { pullRequest: {
        headRefOid: state.current ? 'head' : 'new-head',
        baseRefName: 'main', baseRefOid: 'base',
        state: 'OPEN', isDraft: false, title: snapshot.title, body: snapshot.body,
      } } };
    },
  };
  return {
    state, github,
    core: { warning: (message) => state.warnings.push(message), info: () => {} },
    session: threadResolution({ github, reviewIsCurrent: async () => state.current }),
  };
}

test('stale review before selection mutates nothing', async () => {
  const { state, session } = fixture();
  state.current = false;
  assert.equal(await session.resolve('a'), 'stale');
  assert.deepEqual(state.events, []);
});

test('a missing GraphQL node returns null and is skipped before resolution', async () => {
  const { state, github, session } = fixture();
  state.unavailableNodes.add('a');
  assert.equal(await fetchThread({ github, id: 'a' }), null);
  assert.equal(await session.resolve('a'), 'skipped');
  await session.rollback();
  assert.deepEqual(state.events, []);
});

test('a missing node after acknowledged resolution triggers compensation', async () => {
  const { state, session } = fixture();
  state.onResolve = (id) => state.unavailableNodes.add(id);
  assert.equal(await session.resolve('a'), 'claimed');
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a']);
  assert.equal(state.threads.get('a').isResolved, false);
  await session.rollback();
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a']);
});

test('an unavailable node does not discard failed post-resolution compensation', async () => {
  const { state, session } = fixture();
  await session.resolve('a');
  state.onResolve = (id) => state.unavailableNodes.add(id);
  state.onReopen = (id) => {
    if (state.unavailableNodes.has(id)) throw new Error('Could not resolve to a node');
  };
  await assert.rejects(session.resolve('b'), /Could not resolve to a node/);
  await assert.rejects(session.current(), /Could not reopen b: Could not resolve to a node/);
  assert.equal(state.threads.get('b').isResolved, true);
  state.unavailableNodes.delete('b');
  assert.equal(await session.current(), true);
  assert.equal(state.threads.get('b').isResolved, false);
  assert.equal(state.threads.get('a').isResolved, true);
  assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:b', 'reopen:b']);
});

test('ownership changes before resolution are left alone', async () => {
  const { state, session } = fixture();
  claim(state.threads.get('a'));
  assert.equal(await session.resolve('a'), 'skipped');
  assert.deepEqual(state.events, []);
});

test('already resolved threads never enter the compensation journal', async () => {
  const { state, session } = fixture();
  state.threads.get('a').isResolved = true;
  assert.equal(await session.resolve('a'), 'skipped');
  state.current = false;
  assert.equal(await session.current(), false);
  assert.deepEqual(state.events, []);
  assert.equal(state.threads.get('a').isResolved, true);
});

test('a review changed during the ownership read is compensated after resolution', async () => {
  const { state, session } = fixture();
  state.onRead = () => { state.current = false; };
  assert.equal(await session.resolve('a'), 'stale');
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a']);
});

test('ownership change during resolution reopens the thread', async () => {
  const { state, session } = fixture();
  state.onResolve = (id) => claim(state.threads.get(id));
  assert.equal(await session.resolve('a'), 'claimed');
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a']);
  await session.rollback();
  assert.equal(state.events.length, 2);
});

test('claiming the last thread does not bypass stale-review rollback', async () => {
  const { state, session } = fixture();
  assert.equal(await session.resolve('a'), 'resolved');
  state.onResolve = (id) => {
    claim(state.threads.get(id));
    state.current = false;
  };
  assert.equal(await session.resolve('b'), 'stale');
  assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a']);
  state.current = true;
  assert.equal(await session.resolve('a'), 'stale');
});

test('failed ownership reread compensates the acknowledged resolution', async () => {
  const { state, session } = fixture();
  state.onRead = (value) => {
    if (value.isResolved) throw new Error('ownership read failed');
  };
  await assert.rejects(session.resolve('a'), /ownership read failed/);
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a']);
});

test('failed reread and compensation retain both errors and retryable undo', async () => {
  const { state, session } = fixture();
  state.onRead = (value) => {
    if (value.isResolved) throw new Error('ownership read failed');
  };
  state.onReopen = () => { throw new Error('reopen unavailable'); };
  await assert.rejects(session.resolve('a'), (error) => {
    assert.equal(error.errors.length, 2);
    assert.match(error.message, /ownership read failed.*reopen unavailable/);
    return true;
  });
  state.onReopen = () => {};
  await session.rollback();
  assert.deepEqual(state.events, ['resolve:a', 'reopen:a', 'reopen:a']);
  assert.equal(state.threads.get('a').isResolved, false);
});

test('stale rollback attempts every entry and retries only failed undo', async () => {
  const { state, session } = fixture();
  await session.resolve('a');
  state.onResolve = () => { state.current = false; };
  state.onReopen = (id) => {
    if (id === 'b') throw new Error('reopen unavailable');
  };
  await assert.rejects(session.resolve('b'), /Could not reopen b/);
  assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a']);
  state.onReopen = () => {};
  assert.equal(await session.current(), false);
  assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a', 'reopen:b']);
});

test('currency read failure undoes prior resolutions and stops the session', async () => {
  const { state, github } = fixture();
  const session = threadResolution({ github, reviewIsCurrent: async () => {
    if (!state.current) throw new Error('currency read failed');
    return true;
  } });
  await session.resolve('a');
  state.onResolve = () => { state.current = false; };
  await assert.rejects(session.resolve('b'), /currency read failed/);
  assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a']);
  state.current = true;
  assert.equal(await session.current(), false);
});

async function withFiles(run) {
  const keys = ['CTX_DIR', 'REVIEW_FILE', 'REVIEW_KIND', 'REVIEW_COMMAND_OUTCOME'];
  const previous = keys.map((key) => process.env[key]);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'thread-resolution-'));
  process.env.CTX_DIR = dir;
  process.env.REVIEW_FILE = `${dir}/review.json`;
  process.env.REVIEW_KIND = 'codex';
  process.env.REVIEW_COMMAND_OUTCOME = 'success';
  fs.writeFileSync(`${dir}/pull-request.json`, JSON.stringify(snapshot));
  fs.writeFileSync(`${dir}/issue-specs.json`, '[]');
  fs.writeFileSync(`${dir}/threads.json`, JSON.stringify([{ id: 'a' }, { id: 'b' }]));
  fs.writeFileSync(`${dir}/resolutions.json`, JSON.stringify({ threads: [
    { id: 'a', solved: true, reason: 'Fixed' },
    { id: 'b', solved: true, reason: 'Fixed' },
  ] }));
  fs.writeFileSync(`${dir}/review.json`, JSON.stringify({
    summary: '## standards\nOK\n\n## spec\nOne finding', findings: [finding],
  }));
  try {
    await run();
  } finally {
    keys.forEach((key, index) => {
      if (previous[index] === undefined) delete process.env[key];
      else process.env[key] = previous[index];
    });
    fs.rmSync(dir, { recursive: true });
  }
}

test('resolver warns on failed reread and continues to the next selected thread', async () => {
  await withFiles(async () => {
    const { state, github, core } = fixture();
    state.onRead = (value) => {
      if (value.id === 'a' && value.isResolved) throw new Error('reread failed');
    };
    await resolveThreads({ github, context, core });
    assert.deepEqual(state.events, ['resolve:a', 'reopen:a', 'resolve:b']);
    assert.match(state.warnings[0], /Could not close a: reread failed/);
  });
});

for (const [id, failure] of [['a', 'claim'], ['b', 'claim'], ['b', 'reread']]) {
  test(`resolver retries transient ${failure} compensation for ${id} while the review stays current`, async () => {
    await withFiles(async () => {
      const { state, github, core } = fixture();
      state.onResolve = (resolvedId) => {
        if (failure === 'claim' && resolvedId === id) claim(state.threads.get(id));
      };
      state.onRead = (value) => {
        if (failure === 'reread' && value.id === id && value.isResolved) {
          throw new Error('ownership read failed');
        }
      };
      let reopenFailed = false;
      state.onReopen = (reopenedId) => {
        if (reopenedId === id && !reopenFailed) {
          reopenFailed = true;
          throw new Error('transient reopen failure');
        }
      };
      await resolveThreads({ github, context, core });
      assert.equal(state.current, true);
      assert.equal(state.threads.get(id).isResolved, false);
      assert.equal(state.threads.get(id === 'a' ? 'b' : 'a').isResolved, true);
      assert.deepEqual(state.events, id === 'a'
        ? ['resolve:a', 'reopen:a', 'reopen:a', 'resolve:b']
        : ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:b']);
      assert.ok(state.warnings.some((message) => message.includes('transient reopen failure')));
    });
  });
}

test('resolver reopens earlier resolutions when the final thread is claimed on a stale review', async () => {
  await withFiles(async () => {
    const { state, github, core } = fixture();
    state.onResolve = (id) => {
      if (id === 'b') {
        claim(state.threads.get(id));
        state.current = false;
      }
    };
    await resolveThreads({ github, context, core });
    assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a']);
  });
});

function posterFixture() {
  const result = fixture([]);
  const { state, github } = result;
  const listFiles = async () => {};
  const listComments = async () => {};
  github.rest = {
    pulls: {
      listFiles,
      createReview: async () => {
        state.events.push('submit');
        state.threads.set('a', thread('a'));
        state.threads.set('b', thread('b'));
        state.threads.set('c', thread('c'));
        return { data: { id: 55 } };
      },
    },
    issues: {
      listComments,
      createComment: async ({ body }) => {
        state.events.push('summary');
        return { data: { id: 10, body } };
      },
      deleteComment: async () => {},
    },
  };
  github.paginate = async (method) => {
    if (method === listFiles) return [{
      filename: 'file.txt', patch: '@@ -0,0 +1 @@\n+content',
    }];
    if (method === listComments) return [];
    return [{ id: 99 }];
  };
  github.request = async () => { state.events.push('delete-inline'); };
  return result;
}

test('resolver warns on failed stale compensation without abandoning earlier undo', async () => {
  await withFiles(async () => {
    const { state, github, core } = fixture();
    state.onResolve = (id) => {
      if (id === 'b') state.current = false;
    };
    state.onReopen = (id) => {
      if (id === 'b') throw new Error('reopen unavailable');
    };
    await resolveThreads({ github, context, core });
    assert.deepEqual(state.events, ['resolve:a', 'resolve:b', 'reopen:b', 'reopen:a', 'reopen:b']);
    assert.ok(state.warnings.some((message) => /Could not close b: Could not reopen b/.test(message)));
    assert.ok(state.warnings.some((message) => /Could not finish thread resolution/.test(message)));
  });
});

test('poster compensates stale duplicates even when deleting inline comments fails', async () => {
  await withFiles(async () => {
    const { state, github, core } = posterFixture();
    state.onResolve = (id) => {
      if (id === 'a') state.current = false;
    };
    github.request = async () => { throw new Error('inline deletion failed'); };
    await assert.rejects(postReview({ github, context, core }), /inline deletion failed/);
    assert.deepEqual(state.events, ['submit', 'resolve:b', 'resolve:a', 'reopen:a', 'reopen:b']);
  });
});

test('poster retains newest duplicate selection and publishes after successful reconciliation', async () => {
  await withFiles(async () => {
    const { state, github, core } = posterFixture();
    await postReview({ github, context, core });
    assert.deepEqual(state.events, ['submit', 'resolve:b', 'resolve:a', 'summary']);
    assert.equal(state.threads.get('c').isResolved, false);
  });
});

test('poster reopens a duplicate claimed during resolution without closing the newest thread', async () => {
  await withFiles(async () => {
    const { state, github, core } = posterFixture();
    state.onResolve = (id) => {
      if (id === 'b') claim(state.threads.get(id));
    };
    await postReview({ github, context, core });
    assert.deepEqual(state.events, ['submit', 'resolve:b', 'reopen:b', 'resolve:a', 'summary']);
    assert.equal(state.threads.get('b').isResolved, false);
    assert.equal(state.threads.get('c').isResolved, false);
  });
});

test('poster throws on failed reread after undoing duplicate resolutions and inline comments', async () => {
  await withFiles(async () => {
    const { state, github, core } = posterFixture();
    state.onRead = (value) => {
      if (value.id === 'a' && value.isResolved) throw new Error('reread failed');
    };
    await assert.rejects(postReview({ github, context, core }), /reread failed/);
    assert.deepEqual(state.events, [
      'submit', 'resolve:b', 'resolve:a', 'reopen:a', 'reopen:b', 'delete-inline',
    ]);
  });
});

test('poster removes inline comments even when stale duplicate compensation fails', async () => {
  await withFiles(async () => {
    const { state, github, core } = posterFixture();
    state.onResolve = (id) => {
      if (id === 'a') state.current = false;
    };
    state.onReopen = (id) => {
      if (id === 'a') throw new Error('reopen unavailable');
    };
    await assert.rejects(postReview({ github, context, core }), /Could not reopen a/);
    assert.deepEqual(state.events, [
      'submit', 'resolve:b', 'resolve:a', 'reopen:a', 'reopen:b', 'reopen:a', 'delete-inline',
    ]);
    assert.equal(state.threads.get('b').isResolved, false);
    assert.equal(state.threads.get('a').isResolved, true);
  });
});
