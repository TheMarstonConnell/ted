const { fetchThread, ownedByReviewer } = require('./threads.cjs');

const RESOLVE = `
  mutation ($threadId: ID!) {
    resolveReviewThread(input: { threadId: $threadId }) { clientMutationId }
  }`;
const UNRESOLVE = `
  mutation ($threadId: ID!) {
    unresolveReviewThread(input: { threadId: $threadId }) { clientMutationId }
  }`;

// One sequential review run. GitHub has no conditional resolve or transaction;
// the journal covers acknowledged writes, not requests with unknown outcomes.
function threadResolution({ github, reviewIsCurrent }) {
  const resolved = new Set();
  const pendingUndo = new Set();
  let stopped = false;

  const reopen = async (id) => {
    pendingUndo.add(id);
    await github.graphql(UNRESOLVE, { threadId: id });
    pendingUndo.delete(id);
    resolved.delete(id);
  };
  const reopenAll = async (ids) => {
    const errors = [];
    for (const id of [...ids].reverse()) {
      try {
        await reopen(id);
      } catch (error) {
        errors.push(new Error(`Could not reopen ${id}: ${error.message}`, { cause: error }));
      }
    }
    if (errors.length) {
      throw new AggregateError(errors, errors.map((error) => error.message).join('; '));
    }
  };
  const rollback = () => reopenAll(resolved);
  const current = async () => {
    let isCurrent;
    try {
      isCurrent = !stopped && await reviewIsCurrent();
    } catch (error) {
      stopped = true;
      try {
        await rollback();
      } catch (undoError) {
        throw new AggregateError([error, undoError], `${error.message}; ${undoError.message}`);
      }
      throw error;
    }
    if (isCurrent) {
      // A current review does not make a failed ownership compensation optional.
      await reopenAll(pendingUndo);
      return true;
    }
    stopped = true;
    await rollback();
    return false;
  };
  const resolve = async (id) => {
    if (!await current()) return 'stale';
    const fresh = await fetchThread({ github, id });
    if (!fresh || fresh.isResolved || !ownedByReviewer(fresh)) return 'skipped';
    await github.graphql(RESOLVE, { threadId: id });
    resolved.add(id);
    let owned;
    try {
      owned = ownedByReviewer(await fetchThread({ github, id }));
    } catch (error) {
      try {
        await reopen(id);
      } catch (undoError) {
        throw new AggregateError([error, undoError],
          `${error.message}; Could not reopen ${id}: ${undoError.message}`);
      }
      throw error;
    }
    if (!owned) {
      await reopen(id);
    }
    if (!await current()) return 'stale';
    return owned ? 'resolved' : 'claimed';
  };
  return { current, resolve, rollback };
}

module.exports = { threadResolution };
