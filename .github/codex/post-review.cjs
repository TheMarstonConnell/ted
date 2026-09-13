const fs = require('fs');
const { loadReviewContext } = require('./context.cjs');
const { reviewCurrentCheck } = require('./review-current.cjs');
const { fetchThreads, ownedByReviewer } = require('./threads.cjs');
const { threadResolution } = require('./thread-resolution.cjs');

const REVIEWERS = {
  codex: {
    marker: '<!-- codex-two-axis-review -->',
    axes: ['standards', 'spec'],
    severities: ['blocking', 'suggestion', 'nit'],
  },
  minimalism: {
    marker: '<!-- codex-minimalism-review -->',
    axes: ['minimalism'],
    severities: ['suggestion'],
  },
};
// The marker alone does not identify our own comment: any participant can
// paste it into theirs. REST reports the job's identity with the `[bot]`
// suffix, unlike the GraphQL login that threads.cjs matches.
const SUMMARY_AUTHOR = 'github-actions[bot]';
const THREAD_AUTHOR = 'github-actions';
const FINDING_MARKER = /<!-- codex-finding:([a-z0-9][a-z0-9._:/-]{0,199}) -->/;

// Older findings have no stable marker, so fall back to their location and
// axis without depending on model-generated prose.
const AXIS_PREFIX = /^\*\*\[(\w+) \//;
const axisOf = (body) => AXIS_PREFIX.exec(body || '')?.[1] ?? '';
const findingKey = (path, line, axis) => `${path}:${line}:${axis}`;
const FINDING_ID_FORMAT = /^[a-z0-9][a-z0-9._:/-]{0,199}$/;

function validateReview(review, reviewer) {
  if (!review || typeof review.summary !== 'string' ||
      review.summary.trim() === '' || !Array.isArray(review.findings)) {
    return 'the top-level summary/findings contract is invalid';
  }
  const missing = reviewer.axes.filter(
    (axis) => !new RegExp(`^##+ +${axis}\\b`, 'im').test(review.summary)
  );
  if (missing.length > 0) {
    return `summary is missing section(s): ${missing.join(', ')}`;
  }
  const findingIds = new Set();
  for (const [index, finding] of review.findings.entries()) {
    if (!finding || typeof finding.path !== 'string' ||
        finding.path.trim() === '' || !Number.isInteger(finding.line) ||
        finding.line < 1 || !reviewer.axes.includes(finding.axis) ||
        !reviewer.severities.includes(finding.severity) ||
        typeof finding.body !== 'string' || finding.body.trim() === '') {
      return `finding ${index} has invalid review fields`;
    }
    // Schema-invalid output still reaches here, so the type is established
    // before any string method: `(42).indexOf` would throw past the guard.
    if (typeof finding.finding_id !== 'string' ||
        !FINDING_ID_FORMAT.test(finding.finding_id)) {
      return `finding ${index} has an invalid finding_id`;
    }
    const firstColon = finding.finding_id.indexOf(':');
    const lastColon = finding.finding_id.lastIndexOf(':');
    if (firstColon < 1 || lastColon <= firstColon + 1 ||
        lastColon === finding.finding_id.length - 1 ||
        finding.finding_id.slice(0, firstColon) !== finding.axis) {
      return `finding ${index} has an invalid finding_id`;
    }
    if (findingIds.has(finding.finding_id)) {
      return `finding ${index} duplicates finding_id ${finding.finding_id}`;
    }
    findingIds.add(finding.finding_id);
  }
  return null;
}

module.exports = async ({ github, context, core }) => {
  context = loadReviewContext(context);
  const kind = process.env.REVIEW_KIND || 'codex';
  const reviewer = REVIEWERS[kind];
  if (!reviewer) throw new Error(`Unknown review kind: ${kind}`);

  const pr = context.payload.pull_request;
  const repo = context.repo;
  const headSha = pr.head.sha;
  let raw = '';
  try {
    raw = fs.readFileSync(process.env.REVIEW_FILE, 'utf8');
  } catch {}
  const reviewIsCurrent = reviewCurrentCheck({ github, context, core });
  const resolutions = threadResolution({ github, reviewIsCurrent });

  let review = null;
  try {
    review = JSON.parse(raw);
  } catch {}

  const deleteSummary = async (commentId) => {
    try {
      await github.rest.issues.deleteComment({ ...repo, comment_id: commentId });
    } catch (error) {
      // Concurrent current runs can both choose the same superseded comment.
      if (error.status !== 404) throw error;
    }
  };

  let createdReview;
  const rollbackInline = async () => {
    const errors = [];
    try {
      await resolutions.rollback();
    } catch (error) {
      errors.push(error);
    }
    try {
      if (createdReview) {
        const comments = await github.paginate(
          'GET /repos/{owner}/{repo}/pulls/{pull_number}/reviews/{review_id}/comments',
          {
            ...repo,
            pull_number: pr.number,
            review_id: createdReview.id,
            per_page: 100,
          }
        );
        for (const comment of comments) {
          await github.request(
            'DELETE /repos/{owner}/{repo}/pulls/comments/{comment_id}',
            { ...repo, comment_id: comment.id }
          );
        }
        createdReview = undefined;
      }
    } catch (error) {
      errors.push(error);
    }
    if (errors.length) {
      throw new AggregateError(errors, errors.map((error) => error.message).join('; '));
    }
  };
  const currentOrRollback = async () => {
    try {
      if (await resolutions.current()) return true;
    } catch (error) {
      try {
        await rollbackInline();
      } catch (undoError) {
        throw new AggregateError([error, undoError], `${error.message}; ${undoError.message}`);
      }
      throw error;
    }
    await rollbackInline();
    return false;
  };
  const reconcileInline = async () => {
    const currentThreads = await fetchThreads({ github, context });
    if (!await currentOrRollback()) return false;
    const byFinding = new Map();
    for (const thread of currentThreads) {
      if (thread.isResolved || !ownedByReviewer(thread)) continue;
      const first = thread.comments.nodes[0];
      const match = FINDING_MARKER.exec(first.body);
      if (!match) continue;
      const matches = byFinding.get(match[1]) || [];
      matches.push({
        thread,
        commentId: first.id,
        createdAt: first.createdAt,
      });
      byFinding.set(match[1], matches);
    }
    for (const matches of byFinding.values()) {
      if (matches.length < 2) continue;
      matches.sort((a, b) =>
        b.createdAt.localeCompare(a.createdAt) ||
        b.commentId.localeCompare(a.commentId)
      );
      for (const { thread } of matches.slice(1)) {
        try {
          if (await resolutions.resolve(thread.id) === 'stale') {
            await rollbackInline();
            return false;
          }
        } catch (error) {
          try {
            await rollbackInline();
          } catch (undoError) {
            throw new AggregateError([error, undoError], `${error.message}; ${undoError.message}`);
          }
          throw error;
        }
      }
    }
    return true;
  };

  const replaceSummary = async (body) => {
    if (!await currentOrRollback()) return;
    const { data: created } = await github.rest.issues.createComment({
      ...repo,
      issue_number: pr.number,
      body,
    });
    if (!await currentOrRollback()) {
      await deleteSummary(created.id);
      return;
    }

    const comments = await github.paginate(github.rest.issues.listComments, {
      ...repo,
      issue_number: pr.number,
      per_page: 100,
    });
    const summaries = comments.filter((comment) =>
      comment.user?.type === 'Bot' &&
      comment.user?.login === SUMMARY_AUTHOR &&
      comment.body?.startsWith(`${reviewer.marker}\n`)
    );
    if (!summaries.some((comment) => comment.id === created.id)) {
      summaries.push(created);
    }
    if (!await currentOrRollback()) {
      await deleteSummary(created.id);
      return;
    }
    const winner = summaries.reduce(
      (latest, comment) => !latest || comment.id > latest.id ? comment : latest,
      null
    );
    // Always keep the newest summary. This converges concurrent same-spec runs
    // on one comment without either run overwriting a newer run's result.
    if (winner?.id !== created.id) {
      await deleteSummary(created.id);
      return;
    }
    for (const comment of summaries) {
      if (comment.id === created.id) continue;
      if (!await currentOrRollback()) {
        await deleteSummary(created.id);
        return;
      }
      await deleteSummary(comment.id);
    }
    if (!await currentOrRollback()) await deleteSummary(created.id);
  };

  if (process.env.REVIEW_COMMAND_OUTCOME &&
      process.env.REVIEW_COMMAND_OUTCOME !== 'success') {
    await replaceSummary(
      `${reviewer.marker}\n## ${kind} review\n\n` +
      `Review failed for ${headSha}; no findings were published.\n`
    );
    return;
  }

  const validationError = validateReview(review, reviewer);
  if (validationError) {
    core.warning(`Codex output rejected: ${validationError}`);
    if (!await currentOrRollback()) return;
    await replaceSummary(
      `${reviewer.marker}\n## ${kind} review\n\n` +
      `No review was published for ${headSha}: ${validationError}.\n`
    );
    return;
  }

  // listFiles returns the live PR diff, so establish that it still belongs to
  // this event before using its line coordinates with the event's commit SHA.
  if (!await currentOrRollback()) return;

  // An invalid anchor rejects the entire review, so collect commentable lines
  // from the NEW side of each diff before constructing the batch.
  const files = await github.paginate(github.rest.pulls.listFiles, {
    ...repo,
    pull_number: pr.number,
    per_page: 100,
  });
  const anchorable = new Map();
  for (const file of files) {
    const lines = new Set();
    let line = 0;
    for (const patchLine of (file.patch || '').split('\n')) {
      const hunk = /^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(patchLine);
      if (hunk) {
        line = Number(hunk[1]);
        continue;
      }
      if (patchLine.startsWith('-') || patchLine.startsWith('\\')) continue;
      if (line > 0) lines.add(line);
      line++;
    }
    anchorable.set(file.filename, lines);
  }

  // If a fixed and resolved issue later returns, it deserves a fresh comment.
  const threads = await fetchThreads({ github, context });
  const seenIds = new Set();
  const seenLegacyLocations = new Set();
  for (const thread of threads) {
    if (thread.isResolved) continue;
    for (const comment of thread.comments.nodes) {
      if (comment.author?.__typename !== 'Bot' ||
          comment.author.login !== THREAD_AUTHOR) continue;
      const match = FINDING_MARKER.exec(comment.body);
      if (match) seenIds.add(match[1]);
      else {
        // line goes null once the anchor scrolls away; originalLine remembers.
        seenLegacyLocations.add(findingKey(
          comment.path,
          comment.line ?? comment.originalLine,
          axisOf(comment.body)
        ));
      }
    }
  }

  const inline = [];
  const unanchored = [];
  for (const finding of review.findings) {
    const body = `<!-- codex-finding:${finding.finding_id} -->\n` +
      `**[${finding.axis} / ${finding.severity}]** ${finding.body}`;
    if (seenIds.has(finding.finding_id) ||
        seenLegacyLocations.has(findingKey(
          finding.path,
          finding.line,
          finding.axis
        ))) continue;
    seenIds.add(finding.finding_id);
    if (anchorable.get(finding.path)?.has(finding.line)) {
      inline.push({
        path: finding.path,
        line: finding.line,
        side: 'RIGHT',
        body,
      });
    } else {
      unanchored.push(finding);
    }
  }

  if (inline.length > 0) {
    if (!await currentOrRollback()) return;
    const { data: created } = await github.rest.pulls.createReview({
      ...repo,
      pull_number: pr.number,
      commit_id: headSha,
      event: 'COMMENT',
      comments: inline,
    });
    createdReview = created;
    if (!await currentOrRollback()) return;
  }
  if (!await reconcileInline()) return;

  const counts = Object.fromEntries(reviewer.axes.map((axis) => [axis, 0]));
  for (const finding of review.findings) {
    counts[finding.axis] = (counts[finding.axis] || 0) + 1;
  }

  let body = `${reviewer.marker}\n## ${kind} review\n\n`;
  body += `Reviewed ${headSha}: ${review.findings.length} finding(s): `;
  body += `${reviewer.axes.map((axis) => `${counts[axis]} ${axis}`).join(', ')}.\n\n`;
  body += `${review.summary.trim()}\n`;
  if (unanchored.length > 0) {
    body += '\n### Findings outside the diff\n\n';
    for (const finding of unanchored) {
      body += `- \`${finding.path}:${finding.line}\` `;
      body += `**[${finding.axis} / ${finding.severity}]** ${finding.body}\n`;
    }
  }
  if (!await currentOrRollback()) return;
  await replaceSummary(body);
};
