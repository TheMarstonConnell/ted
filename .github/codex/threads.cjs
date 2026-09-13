const COMMENT_PAGE = `
  pageInfo { hasNextPage endCursor }
  nodes { id createdAt body path line originalLine author { login __typename } }`;

const THREAD_FIELDS = `
  id
  isResolved
  isOutdated
  comments(first: 100) { ${COMMENT_PAGE} }`;

const QUERY = `
  query ($owner: String!, $name: String!, $number: Int!, $cursor: String) {
    repository(owner: $owner, name: $name) {
      pullRequest(number: $number) {
        reviewThreads(first: 100, after: $cursor) {
          pageInfo { hasNextPage endCursor }
          nodes { ${THREAD_FIELDS} }
        }
      }
    }
  }`;

const THREAD = `
  query ($id: ID!) {
    node(id: $id) { ... on PullRequestReviewThread { ${THREAD_FIELDS} } }
  }`;

const MORE_COMMENTS = `
  query ($id: ID!, $cursor: String) {
    node(id: $id) {
      ... on PullRequestReviewThread {
        comments(first: 100, after: $cursor) { ${COMMENT_PAGE} }
      }
    }
  }`;

const FINDING_PREFIX =
  /^(?:<!-- codex-finding:[a-z0-9][a-z0-9._:/-]{0,199} -->\n)?\*\*\[(?:(?:standards|spec) \/ (?:blocking|suggestion|nit)|minimalism \/ suggestion)\]\*\*/;

async function drainComments({ github, thread }) {
  let page = thread.comments.pageInfo;
  while (page.hasNextPage) {
    const result = await github.graphql(MORE_COMMENTS, {
      id: thread.id,
      cursor: page.endCursor,
    });
    const connection = result.node.comments;
    thread.comments.nodes.push(...connection.nodes);
    page = connection.pageInfo;
  }
  return thread;
}

async function fetchThreads({ github, context }) {
  const pr = context.payload.pull_request;
  const nodes = [];
  let cursor = null;
  let hasNext = true;
  while (hasNext) {
    const result = await github.graphql(QUERY, {
      owner: context.repo.owner,
      name: context.repo.repo,
      number: pr.number,
      cursor,
    });
    const connection = result.repository.pullRequest.reviewThreads;
    nodes.push(...connection.nodes);
    hasNext = connection.pageInfo.hasNextPage;
    cursor = connection.pageInfo.endCursor;
  }
  for (const thread of nodes) await drainComments({ github, thread });
  return nodes;
}

async function fetchThread({ github, id }) {
  const result = await github.graphql(THREAD, { id });
  if (!result.node) return null;
  return drainComments({ github, thread: result.node });
}

function candidates(threads) {
  const found = [];
  for (const thread of threads) {
    if (thread.isResolved) continue;
    if (!ownedByReviewer(thread)) continue;
    const first = thread.comments.nodes[0];
    found.push({
      id: thread.id,
      path: first.path,
      // line is null after the anchor disappears; originalLine remembers it.
      line: first.line ?? first.originalLine,
      outdated: thread.isOutdated,
      body: first.body,
    });
  }
  return found;
}

function ownedByReviewer(thread) {
  if (!thread) return false;
  const comments = thread.comments.nodes;
  if (comments.length === 0) return false;
  const first = comments[0];
  if (first.author?.login !== 'github-actions') return false;
  if (!FINDING_PREFIX.test(first.body)) return false;
  // A non-bot reply makes the thread theirs; an unnamed author was a person.
  return !comments.some((comment) => comment.author?.__typename !== 'Bot');
}

module.exports = { fetchThreads, fetchThread, candidates, ownedByReviewer };
