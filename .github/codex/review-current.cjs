const { loadIssueSpecs, reviewInputsAreCurrent } = require('./context.cjs');

const text = (value) => value ?? '';

function reviewCurrentCheck({ github, context, core }) {
  const pr = context.payload.pull_request;
  const issueSpecs = loadIssueSpecs(process.env.CTX_DIR);

  return async () => {
    const { current, prIsCurrent, issuesAreCurrent } =
      await reviewInputsAreCurrent({
        github,
        repo: context.repo,
        pr,
        issueSpecs,
      });
    if (!prIsCurrent) {
      core.warning(
        `PR is no longer reviewable (head ${current?.headRefOid}, ` +
        `base ${current?.baseRefName}@${current?.baseRefOid}, ` +
        `state ${current?.state}, draft ${current?.isDraft}, ` +
        `specification changed ` +
        `${current?.title !== pr.title ||
          text(current?.body) !== text(pr.body)})`
      );
      return false;
    }
    if (!issuesAreCurrent) {
      core.warning('A linked issue changed; mutating nothing');
      return false;
    }
    return true;
  };
}

module.exports = { reviewCurrentCheck };
