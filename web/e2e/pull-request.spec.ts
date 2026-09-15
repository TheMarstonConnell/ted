import { test, expect, type Page } from "@playwright/test";
import { workspace } from "./fixtures";

const branch = "ted/show-thread-pull-request-associations";

async function setup(page: Page) {
  const fixture = await workspace(page);
  const associations: Record<string, { branch?: string; number?: number }> = {
    a1: { branch, number: 42 },
    a2: { branch: "ted/previous-work", number: 18 },
  };
  await page.route("**/v1/agents/*/pull-request", (route) => {
    const id = new URL(route.request().url()).pathname.split("/")[3];
    return route.fulfill({ json: associations[id] || {} });
  });
  for (const [id, title, settled] of [
    ["a1", "Show thread pull requests", false],
    ["a2", "Improve workspace setup", true],
  ] as const) {
    fixture.agents[id] = {
      id,
      project_id: "p",
      title,
      settings: { model: "test/model", effort: "medium" },
      settled,
      held: false,
      state: "idle",
      cursor: 0,
      read_cursor: 0,
      last_response_cursor: 0,
      created_at: "2026-07-01T00:00:00Z",
      updated_at: "2026-07-01T00:00:00Z",
      workspace: {
        mode: "worktree",
        locked: true,
        status: "ready",
        path: `/var/lib/ted/worktrees/${id}`,
        branch: associations[id].branch,
      },
    };
    fixture.events[id] = [];
  }
  await page.goto("/agents/a1");
  return { ...fixture, associations };
}

for (const [width, theme] of [
  [1440, "light"],
  [1440, "dark"],
  [390, "light"],
  [320, "dark"],
] as const) {
  test(`PR number precedes the branch in sidebar and composer (${width}px ${theme})`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.emulateMedia({ colorScheme: theme });
    await setup(page);
    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    const pr = footer.getByLabel("Pull request #42", { exact: true });
    await expect(pr).toHaveText("#42");
    const gitBranch = footer.getByRole("group", {
      name: "Git branch",
      exact: true,
    });
    await expect(gitBranch).toHaveAttribute("title", branch);
    const prBox = (await pr.boundingBox())!;
    const branchBox = (await gitBranch.boundingBox())!;
    expect(prBox.x + prBox.width).toBeLessThanOrEqual(branchBox.x);
    expect(Math.abs(prBox.y - branchBox.y)).toBeLessThan(4);
    await expect(
      footer.locator('[data-slot="context-usage"]'),
    ).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page
      .getByRole("textbox", { name: "Message", exact: true })
      .fill("The PR number now stays beside each thread’s branch.");
    if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1200);
    await page.screenshot({
      path: testInfo.outputPath("pull-request-chat.png"),
    });

    if (width < 768)
      await footer.getByRole("button", { name: "Open sidebar" }).click();
    const row = page.locator('[data-agent-id="a1"]:visible');
    await expect(
      row.getByLabel("Pull request #42", { exact: true }),
    ).toBeVisible();
    const metadata = row.getByText("#42", { exact: true }).locator("..");
    await expect(metadata).toHaveText(`#42${branch}`);
    const separation = await metadata.evaluate(
      (node) =>
        node.getBoundingClientRect().top -
        node.previousElementSibling!.getBoundingClientRect().bottom,
    );
    expect(separation).toBeGreaterThanOrEqual(-0.5);
    await page.getByRole("button", { name: /Settled/ }).click();
    await expect(
      page
        .locator('[data-agent-id="a2"]:visible')
        .getByLabel("Pull request #18"),
    ).toBeVisible();
    if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1200);
    await page.screenshot({
      path: testInfo.outputPath("pull-request-sidebar.png"),
    });
    await testInfo.attach("PR numbers in sidebar", {
      path: testInfo.outputPath("pull-request-sidebar.png"),
      contentType: "image/png",
    });
    await testInfo.attach("PR number beside workspace branch", {
      path: testInfo.outputPath("pull-request-chat.png"),
      contentType: "image/png",
    });
  });
}

for (const width of [1440, 390]) {
  test(`inherited worktree drafts show their recorded PR and keep workspace controls (${width}px)`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    const fixture = await setup(page);
    fixture.setWorkspace("a1", {
      mode: "worktree",
      locked: false,
      shared: true,
      status: "draft",
      path: "/var/lib/ted/worktrees/parent",
      branch,
      base_branch: "origin/main",
    });

    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    await expect(
      footer.getByRole("combobox", { name: "Workspace", exact: true }),
    ).toBeVisible();
    await expect(
      footer.getByLabel("Pull request #42", { exact: true }),
    ).toBeVisible();
    await expect(
      footer.getByRole("group", { name: "Git branch", exact: true }),
    ).toHaveText(branch);
    await expect(
      footer.getByRole("combobox", { name: "Start from", exact: true }),
    ).toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("inherited-worktree-draft.png"),
    });
    await testInfo.attach("Inherited worktree draft PR", {
      path: testInfo.outputPath("inherited-worktree-draft.png"),
      contentType: "image/png",
    });
  });
}

test("PR metadata refreshes after creation and never follows a different or unstarted branch", async ({
  page,
}) => {
  await page.clock.install();
  const fixture = await setup(page);
  const footer = page.getByRole("group", {
    name: "Composer footer",
    exact: true,
  });
  await expect(footer.getByLabel("Pull request #42")).toBeVisible();
  fixture.associations.a1 = { branch };
  await page.clock.fastForward(30000);
  await expect(footer.locator('[aria-label^="Pull request #"]')).toHaveCount(0);
  fixture.associations.a1 = { branch, number: 43 };
  await page.clock.fastForward(30000);
  await expect(footer.getByLabel("Pull request #43")).toBeVisible();

  fixture.setWorkspace("a1", {
    ...fixture.agents.a1.workspace,
    branch: "ted/different-branch",
  });
  await expect(
    footer.getByRole("group", { name: "Git branch", exact: true }),
  ).toHaveText("ted/different-branch");
  await expect(footer.locator('[aria-label^="Pull request #"]')).toHaveCount(0);
  await expect(
    page.locator('[data-agent-id="a1"] [aria-label^="Pull request #"]'),
  ).toHaveCount(0);

  fixture.setWorkspace("a1", {
    mode: "worktree",
    locked: false,
    status: "draft",
    base_branch: "origin/main",
  });
  await expect(
    footer.getByRole("combobox", { name: "Start from" }),
  ).toBeVisible();
  await expect(footer.locator('[aria-label^="Pull request #"]')).toHaveCount(0);
});

test("Local threads use the live checkout branch, not an old workspace branch", async ({
  page,
}) => {
  const fixture = await setup(page);
  fixture.associations.a1 = { branch: "main", number: 77 };
  fixture.setWorkspace("a1", {
    mode: "current_checkout",
    locked: true,
    status: "ready",
    branch: "outdated-branch",
  });
  const footer = page.getByRole("group", {
    name: "Composer footer",
    exact: true,
  });
  await expect(footer.getByLabel("Pull request #77")).toBeVisible();
  await expect(
    footer.getByRole("group", { name: "Git branch", exact: true }),
  ).toHaveText("main");
  await expect(
    page.locator('[data-agent-id="a1"]').getByLabel("Pull request #77"),
  ).toBeVisible();
});
