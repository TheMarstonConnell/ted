const fs = require('node:fs');

const MAX_GRAPHQL_INT = 2147483647;
const MAX_REFERENCES = 20;
const MAX_LOOKUPS = 100;
const SCAN_PAGES_PER_PR = 10;
const SCAN_PAGES_PER_EVENT = 100;
const COMMIT_QUERY = `
  query($owner: String!, $repo: String!, $number: Int!, $cursor: String) {
    repository(owner: $owner, name: $repo) {
      pullRequest(number: $number) {
        commits(first: 100, after: $cursor) {
          nodes { commit { message } }
          pageInfo { hasNextPage endCursor }
        }
      }
    }
  }`;

function references(text, repo) {
  const slug = `${repo.owner}/${repo.repo}`;
  const escaped = slug.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const numbers = new Set();
  for (const pattern of [
    /(?<![\w/#-])#(\d+)(?![\w-])/g,
    new RegExp(`(?<![\\w./-])${escaped}#(\\d+)(?![\\w-])`, 'gi'),
    new RegExp(`(?:https?://|(?<![\\w./-]))github\\.com/${escaped}` +
      '/issues/(\\d+)(?![\\w-])', 'gi'),
  ]) {
    for (const match of text.matchAll(pattern)) {
      const number = Number(match[1]);
      if (Number.isSafeInteger(number) && number > 0 && number <= MAX_GRAPHQL_INT) {
        numbers.add(number);
      }
    }
  }
  return numbers;
}

async function* commitPages({ github, repo, pr }) {
  let cursor = null;
  const seen = new Set();
  while (true) {
    const data = await github.graphql(COMMIT_QUERY, {
      ...repo, number: pr.number, cursor,
    });
    const commits = data.repository?.pullRequest?.commits;
    if (!Array.isArray(commits?.nodes) ||
        typeof commits.pageInfo?.hasNextPage !== 'boolean' ||
        commits.nodes.some((node) => typeof node?.commit?.message !== 'string')) {
      throw new Error('Incomplete PR commit connection');
    }
    yield {
      text: commits.nodes.map(({ commit }) => commit.message).join('\n'),
      hasNextPage: commits.pageInfo.hasNextPage,
    };
    if (!commits.pageInfo.hasNextPage) return;
    cursor = commits.pageInfo.endCursor;
    if (typeof cursor !== 'string' || !cursor || seen.has(cursor)) {
      throw new Error('Incomplete PR commit pagination: missing or repeated cursor');
    }
    seen.add(cursor);
  }
}

async function selectIssuePullRequests({ github, repo, pulls, issueNumber, core }) {
  const selected = [];
  let remaining = SCAN_PAGES_PER_EVENT;
  for (const pr of pulls) {
    let matches = references(`${pr.title}\n${pr.body || ''}`, repo).has(issueNumber);
    if (!matches) {
      const pages = commitPages({ github, repo, pr });
      let scanned = 0;
      try {
        while (true) {
          if (scanned === SCAN_PAGES_PER_PR || remaining === 0) {
            core.warning(`Commit scan budget reached at PR #${pr.number}; ` +
              'scheduling it conservatively');
            matches = true;
            break;
          }
          const page = await pages.next();
          if (page.done) break;
          scanned++;
          remaining--;
          if (references(page.value.text, repo).has(issueNumber)) {
            matches = true;
            break;
          }
          if (!page.value.hasNextPage) break;
        }
      } finally {
        await pages.return();
      }
    }
    if (matches) selected.push(pr);
  }
  return selected;
}

async function captureSpecification({ github, repo, pr, dir }) {
  fs.mkdirSync(dir, { recursive: true });
  // Only a completed capture may leave the file that authorizes mutations.
  fs.rmSync(`${dir}/issue-specs.json`, { force: true });
  fs.writeFileSync(`${dir}/pr.md`,
    `# PR #${pr.number}: ${pr.title}\n\n${pr.body || '(no description)'}\n`);
  const text = [pr.title, pr.body || ''];
  for await (const page of commitPages({ github, repo, pr })) text.push(page.text);
  const numbers = references(text.join('\n'), repo);
  if (numbers.size > MAX_LOOKUPS) {
    throw new Error(`Review not published: more than ${MAX_LOOKUPS} linked ` +
      'issue candidates would exceed the lookup budget');
  }
  const specs = [];
  let issues = '';
  let accessible = 0;
  for (const number of numbers) {
    let data;
    try {
      ({ data } = await github.rest.issues.get({ ...repo, issue_number: number }));
    } catch (error) {
      if (![301, 404, 410].includes(error.status)) throw error;
      specs.push({ number, absent: true });
      continue;
    }
    if (data.pull_request) continue;
    if (!data.repository_url?.endsWith(`/repos/${repo.owner}/${repo.repo}`)) {
      specs.push({ number, absent: true });
      continue;
    }
    if (accessible === MAX_REFERENCES) {
      throw new Error(`Review not published: more than ${MAX_REFERENCES} ` +
        'accessible linked issues would make the specification context incomplete');
    }
    accessible++;
    specs.push({ number, title: data.title, body: data.body || '' });
    issues += `# Issue #${number}: ${data.title}\n\n${data.body || ''}\n\n---\n\n`;
  }
  fs.writeFileSync(`${dir}/issues.md`, issues);
  fs.writeFileSync(`${dir}/issue-specs.json.tmp`, `${JSON.stringify(specs)}\n`,
    { mode: 0o600 });
  fs.renameSync(`${dir}/issue-specs.json.tmp`, `${dir}/issue-specs.json`);
}

function loadIssueSpecs(dir) {
  try {
    const specs = JSON.parse(fs.readFileSync(`${dir}/issue-specs.json`, 'utf8'));
    if (!Array.isArray(specs) || specs.length > MAX_LOOKUPS || specs.some((spec) =>
      !spec || !Number.isInteger(spec.number) || spec.number <= 0 ||
      spec.number > MAX_GRAPHQL_INT || (spec.absent !== true &&
        (typeof spec.title !== 'string' || typeof spec.body !== 'string')))) {
      return null;
    }
    return specs;
  } catch (error) {
    if (error.code === 'ENOENT' || error instanceof SyntaxError) return null;
    throw error;
  }
}

async function specificationIsCurrent({ pr, issueSpecs, readCurrent }) {
  if (issueSpecs === null) {
    return { current: null, prIsCurrent: false, issuesAreCurrent: false };
  }
  const result = await readCurrent();
  const current = result.repository.pullRequest;
  const prIsCurrent = current && current.headRefOid === pr.head.sha &&
    current.baseRefName === pr.base.ref && current.baseRefOid === pr.base.sha &&
    current.state === 'OPEN' && !current.isDraft && current.title === pr.title &&
    (current.body || '') === (pr.body || '');
  const issuesAreCurrent = issueSpecs.every((spec, index) => {
    const current = result.repository[`i${index}`];
    if (spec.absent === true) return current === null;
    return current && current.title === spec.title &&
      (current.body || '') === (spec.body || '');
  });
  return { current, prIsCurrent, issuesAreCurrent };
}

module.exports = {
  captureSpecification, selectIssuePullRequests, loadIssueSpecs, specificationIsCurrent,
};
