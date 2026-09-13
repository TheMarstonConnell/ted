const fs = require('fs');
const { loadReviewContext } = require('./context.cjs');
const { reviewCurrentCheck } = require('./review-current.cjs');
const { threadResolution } = require('./thread-resolution.cjs');

module.exports = async ({ github, context, core }) => {
  context = loadReviewContext(context);
  const dir = process.env.CTX_DIR;

  let output = null;
  try {
    output = JSON.parse(fs.readFileSync(`${dir}/resolutions.json`, 'utf8'));
  } catch {}
  const validThread = (thread) =>
    thread && typeof thread === 'object' && !Array.isArray(thread) &&
    Object.keys(thread).every((key) => ['id', 'solved', 'reason'].includes(key)) &&
    typeof thread.id === 'string' && typeof thread.solved === 'boolean' &&
    typeof thread.reason === 'string';
  if (!output || typeof output !== 'object' || Array.isArray(output) ||
      !Array.isArray(output.threads) ||
      Object.keys(output).some((key) => key !== 'threads') ||
      !output.threads.every(validThread)) {
    core.warning('Resolver output was not valid schema JSON; resolving nothing');
    return;
  }

  const known = new Set(
    JSON.parse(fs.readFileSync(`${dir}/threads.json`, 'utf8')).map((thread) => thread.id)
  );
  const reviewIsCurrent = reviewCurrentCheck({ github, context, core });
  const resolutions = threadResolution({ github, reviewIsCurrent });

  // Judging takes minutes. cancel-in-progress cannot retract a mutation that
  // is already in flight, so a superseded run could close a finding that the
  // new head reintroduced, and the replacement run would skip it as resolved.
  if (!await resolutions.current()) return;

  let resolved = 0;
  for (const thread of output.threads) {
    if (!thread.solved) continue;
    if (!known.has(thread.id)) {
      core.warning(`Skipping unknown thread ID ${thread.id}`);
      continue;
    }
    try {
      const result = await resolutions.resolve(thread.id);
      if (result === 'stale') return;
      if (result === 'skipped') {
        core.info(`Leaving ${thread.id}: no longer ours to close`);
        continue;
      }
      if (result === 'claimed') {
        core.info(`Reopened ${thread.id}: claimed while it was being closed`);
        continue;
      }
    } catch (error) {
      core.warning(`Could not close ${thread.id}: ${error.message}`);
      continue;
    }
    core.info(`Resolved ${thread.id}: ${thread.reason || 'no reason given'}`);
    resolved++;
  }
  try {
    if (!await resolutions.current()) return;
  } catch (error) {
    core.warning(`Could not finish thread resolution: ${error.message}`);
    return;
  }
  core.info(`Resolved ${resolved} thread(s)`);
};
