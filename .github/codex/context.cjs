const fs = require('fs');
const { loadIssueSpecs, specificationIsCurrent } = require('./specification.cjs');

function loadReviewContext(context) {
  const snapshot = process.env.CTX_DIR &&
    `${process.env.CTX_DIR}/pull-request.json`;
  if (!snapshot || !fs.existsSync(snapshot)) return context;

  const pullRequest = JSON.parse(fs.readFileSync(snapshot, 'utf8'));
  return {
    ...context,
    // actions/github-script's `repo` is a prototype getter, so object spread
    // drops it. Restore explicitly: every GraphQL currency check below needs
    // `{owner, repo}` for its $owner/$repo variables.
    repo: context.repo,
    payload: {...context.payload, pull_request: pullRequest},
  };
}

// GraphQL reports a missing issue as a NOT_FOUND error on that alias, and
// Octokit throws even though the rest of the response is intact. Absent
// candidates (a dependabot changelog citing upstream #4645) are expected to
// stay missing, so keep the partial data and let the alias read as null.
async function queryToleratingMissingIssues(github, query, variables) {
  try {
    return await github.graphql(query, variables);
  } catch (error) {
    const tolerable = Array.isArray(error?.errors) && error.data &&
      error.errors.every((item) => item.type === 'NOT_FOUND' &&
        Array.isArray(item.path) && item.path.length === 2 &&
        item.path[0] === 'repository' && /^i\d+$/.test(item.path[1]));
    if (!tolerable) throw error;
    return error.data;
  }
}

async function readCurrentInputs({ github, repo, pr, issueSpecs }) {
  const issueDeclarations = issueSpecs
    .map((_, index) => `$n${index}: Int!`).join(', ');
  const fields = issueSpecs.map((_, index) =>
    `i${index}: issue(number: $n${index}) { title body }`
  ).join('\n');
  const variables = { ...repo, pullNumber: pr.number };
  for (const [index, spec] of issueSpecs.entries()) {
    variables[`n${index}`] = spec.number;
  }
  const result = await queryToleratingMissingIssues(github, `
    query($owner: String!, $repo: String!, $pullNumber: Int!
          ${issueDeclarations ? `, ${issueDeclarations}` : ''}) {
      repository(owner: $owner, name: $repo) {
        pullRequest(number: $pullNumber) {
          headRefOid
          baseRefName
          baseRefOid
          state
          isDraft
          title
          body
        }
        ${fields}
      }
    }`, variables);
  return result;
}

function reviewInputsAreCurrent(args) {
  return specificationIsCurrent({
    ...args,
    readCurrent: () => readCurrentInputs(args),
  });
}

module.exports = { loadReviewContext, loadIssueSpecs, reviewInputsAreCurrent };
