const fs = require('fs');
const { loadReviewContext } = require('./context.cjs');
const { fetchThreads, candidates } = require('./threads.cjs');

module.exports = async ({ github, context, core }) => {
  context = loadReviewContext(context);
  const found = candidates(await fetchThreads({ github, context }));
  fs.mkdirSync(process.env.CTX_DIR, { recursive: true });
  fs.writeFileSync(
    `${process.env.CTX_DIR}/threads.json`,
    JSON.stringify(found, null, 2)
  );
  core.setOutput('count', String(found.length));
  core.info(`${found.length} open Codex thread(s) to check`);
};
