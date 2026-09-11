import { test, expect, type Page } from "@playwright/test";
import { openProjectDefaults, workspace } from "./fixtures";

async function createChat(page: Page) {
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect(page).toHaveURL(/\/agents\/a1$/);
}

test("an empty chat can override its worktree base before first send", async ({
  page,
}) => {
  const { agents } = await workspace(page);
  await createChat(page);

  const location = page.getByRole("combobox", { name: "Workspace" });
  await expect(location).toHaveValue("current_checkout");
  await location.selectOption("worktree");
  const branch = page.getByRole("combobox", { name: "Start from" });
  await expect(branch).toBeEnabled();
  await branch.selectOption("origin/release");
  await expect
    .poll(() => agents.a1.workspace)
    .toMatchObject({
      mode: "worktree",
      base_branch: "origin/release",
      locked: false,
    });

  await page.getByRole("textbox", { name: "Message" }).fill("Ship it");
  await page.getByRole("button", { name: "Send message" }).click();

  await expect(location).toHaveCount(0);
  const indicator = page.getByRole("group", { name: "Workspace location" });
  await expect(indicator).toHaveText("Worktree");
  await expect(indicator).not.toContainText("/srv/harness");
  await expect(indicator).not.toContainText("main");
  await expect(page.locator('[data-agent-id="a1"]')).toContainText(
    "origin/release",
  );
  await expect(indicator).toHaveAttribute("title", "/var/lib/ted/worktrees/a1");
  await expect(indicator.locator("button, select, [tabindex]")).toHaveCount(0);
  await expect.poll(() => agents.a1.workspace.locked).toBe(true);
});

test("project settings save workspace defaults for newly created chats", async ({
  page,
}) => {
  const { agents } = await workspace(page);
  await page.goto("/");
  await expect(
    page.getByRole("button", { name: "harness", exact: true }),
  ).toBeVisible();
  await openProjectDefaults(page);
  const dialog = page.getByRole("dialog", { name: "harness defaults" });
  await dialog
    .getByRole("combobox", { name: "Workspace" })
    .selectOption("worktree");
  await dialog
    .getByRole("combobox", { name: "Start from" })
    .selectOption("upstream/trunk");
  await dialog.getByRole("button", { name: "Save defaults" }).click();
  await expect(dialog).toHaveCount(0);

  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect
    .poll(() => agents.a1.workspace)
    .toMatchObject({
      mode: "worktree",
      base_branch: "upstream/trunk",
      locked: false,
    });
  await expect(page.getByRole("combobox", { name: "Workspace" })).toHaveValue(
    "worktree",
  );
  await expect(page.getByRole("combobox", { name: "Start from" })).toHaveValue(
    "upstream/trunk",
  );
});

test("workspace setup progress and terminal failure lock and block the chat", async ({
  page,
}) => {
  const fixture = await workspace(page);
  await createChat(page);
  const message = page.getByRole("textbox", { name: "Message" });
  await message.fill("Keep this draft");

  fixture.setWorkspace("a1", {
    mode: "worktree",
    base_branch: "origin/release",
    locked: true,
    status: "fetching",
  });
  await expect(page.getByText("Setting up workspace")).toBeVisible();
  await expect(page.getByText("Fetching origin/release…")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Send message" }),
  ).toBeDisabled();
  await expect(page.getByRole("combobox", { name: "Workspace" })).toHaveCount(
    0,
  );

  fixture.agents.a1.held = true;
  fixture.setWorkspace("a1", {
    mode: "worktree",
    base_branch: "origin/release",
    locked: true,
    status: "failed",
    error: "Remote branch origin/release no longer exists.",
  });
  await expect(page.getByText("Workspace setup failed")).toBeVisible();
  await expect(
    page.getByText("Remote branch origin/release no longer exists."),
  ).toBeVisible();
  await expect(
    page.getByText("This chat can’t continue", { exact: false }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: /retry/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Continue" })).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Send message" }),
  ).toBeDisabled();
  await expect(message).toHaveValue("Keep this draft");
});

test("a current checkout ignores stored worktree base metadata", async ({
  page,
}) => {
  const fixture = await workspace(page);
  await createChat(page);
  fixture.setWorkspace("a1", {
    mode: "current_checkout",
    base_branch: "origin/release",
    locked: true,
    status: "ready",
    path: "/srv/harness",
  });
  const indicator = page.getByRole("group", { name: "Workspace location" });
  await expect(indicator).toHaveText("Local");
  await expect(indicator).toHaveAttribute("title", "/srv/harness");
  await expect(indicator).not.toContainText("origin/release");
});

test("worktrees are disabled for a non-Git project", async ({ page }) => {
  const fixture = await workspace(page);
  fixture.setProjectBranches({
    is_git: false,
    branches: [],
    default_branch: "",
  });
  await createChat(page);
  const location = page.getByRole("combobox", { name: "Workspace" });
  await expect(location.locator('option[value="worktree"]')).toBeDisabled();
  await expect(location).toHaveValue("current_checkout");
  await expect(
    page.getByText("Worktrees aren’t available", { exact: false }),
  ).toBeVisible();
});

test("compact workspace settings explain branch-list failures", async ({
  page,
}) => {
  await workspace(page);
  await page.route("**/v1/projects/p/branches", (route) =>
    route.fulfill({
      status: 503,
      json: { error: { message: "Branch catalog unavailable" } },
    }),
  );
  await createChat(page);
  await expect(
    page.getByText("Could not load remote branches.", { exact: false }),
  ).toBeVisible();
});

for (const width of [320, 390, 1440]) {
  for (const mode of ["current_checkout", "worktree"] as const) {
    test(`footer workspace selector becomes plain text (${width}px, ${mode})`, async ({
      page,
    }) => {
      await page.setViewportSize({ width, height: 844 });
      const fixture = await workspace(page);
      await createChat(page);
      const footer = page.getByRole("group", {
        name: "Composer footer",
        exact: true,
      });
      const selector = footer.getByRole("combobox", {
        name: "Workspace",
        exact: true,
      });
      await expect(selector).toBeVisible();
      await expect(
        selector.locator('option[value="current_checkout"]'),
      ).toHaveText("Local");
      await expect(
        page
          .getByRole("group", { name: "Message input", exact: true })
          .getByRole("combobox", { name: "Workspace", exact: true }),
      ).toHaveCount(0);
      await selector.selectOption(mode);
      if (mode === "worktree")
        await expect(
          footer.getByRole("combobox", { name: "Start from", exact: true }),
        ).toBeVisible();
      const inputBox = (await page
        .getByRole("group", { name: "Message input", exact: true })
        .boundingBox())!;
      const selectorBox = (await selector.boundingBox())!;
      expect(selectorBox.y).toBeGreaterThan(inputBox.y + inputBox.height);
      expect(selectorBox.width).toBeLessThan(200);
      await expect(
        page.getByRole("group", { name: "Workspace settings", exact: true }),
      ).toHaveCount(1);
      if (width < 768) {
        expect(selectorBox.height).toBeGreaterThanOrEqual(48);
        await expect(
          footer.getByRole("button", { name: "Open sidebar", exact: true }),
        ).toBeInViewport();
      }
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      await page
        .getByRole("textbox", { name: "Message", exact: true })
        .fill("Start working");
      await page
        .getByRole("button", { name: "Send message", exact: true })
        .click();
      const indicator = footer.getByRole("group", {
        name: "Workspace location",
        exact: true,
      });
      await expect(indicator).toHaveText(
        mode === "worktree" ? "Worktree" : "Local",
      );
      await expect(footer.getByRole("combobox")).toHaveCount(0);
      const folder = indicator.locator("svg.lucide-folder");
      await expect(folder).toHaveCount(mode === "current_checkout" ? 1 : 0);
      if (mode === "current_checkout") {
        await expect(folder).toHaveAttribute("aria-hidden", "true");
        await expect(folder).toHaveCSS("width", "12px");
      }
      const muted = await footer
        .locator('[data-slot="context-usage"]')
        .evaluate((node) => getComputedStyle(node).color);
      await expect(indicator).toHaveCSS("color", muted);
      await expect(indicator.locator("button, select, [tabindex]")).toHaveCount(
        0,
      );
      await expect(
        footer.getByRole("button", { name: /Copy workspace path/ }),
      ).toHaveCount(0);
      await expect.poll(() => fixture.agents.a1.workspace.locked).toBe(true);
    });
  }
}

for (const theme of ["light", "dark"] as const) {
  test(`Local footer uses a muted folder icon (${theme})`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ colorScheme: theme });
    await page.setViewportSize({
      width: theme === "light" ? 1440 : 390,
      height: 844,
    });
    await workspace(page);
    await createChat(page);
    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    const selector = footer.getByRole("combobox", {
      name: "Workspace",
      exact: true,
    });
    await expect(selector).toHaveValue("current_checkout");
    await expect(selector.locator("option:checked")).toHaveText("Local");
    const muted = await footer
      .locator('[data-slot="context-usage"]')
      .evaluate((node) => getComputedStyle(node).color);
    await expect(selector).toHaveCSS("color", muted);
    await expect(footer.locator("svg.lucide-folder")).toHaveCSS("color", muted);
    await expect(footer.locator("svg.lucide-folder")).toHaveCSS(
      "width",
      "12px",
    );
    await page
      .getByRole("textbox", { name: "Message", exact: true })
      .fill("Work in this local checkout.");
    await page.screenshot({
      path: testInfo.outputPath(`local-footer-${theme}-draft.png`),
    });
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    const location = footer.getByRole("group", {
      name: "Workspace location",
      exact: true,
    });
    await expect(location).toHaveText("Local");
    await expect(location).toHaveCSS("color", muted);
    await expect(location.locator("svg.lucide-folder")).toHaveCSS(
      "color",
      muted,
    );
    await expect(location).toHaveAttribute("title", "/srv/harness");
    await expect(footer.getByRole("combobox")).toHaveCount(0);
    await expect(footer.getByRole("button", { name: /copy/i })).toHaveCount(0);
    await page.screenshot({
      path: testInfo.outputPath(`local-footer-${theme}-locked.png`),
    });
  });
}

for (const width of [320, 390, 1440]) {
  test(`footer restores workspace-aware branches and vertical dividers (${width}px)`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 844 });
    const fixture = await workspace(page);
    await createChat(page);
    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    const branch = footer.getByRole("group", {
      name: "Git branch",
      exact: true,
    });
    const separators = footer.locator('[data-slot="separator"]:visible');
    await expect(branch).toHaveText("main");
    await expect(separators).toHaveCount(width < 768 ? 3 : 2);
    for (const divider of await separators.all()) {
      await expect(divider).toHaveAttribute("aria-hidden", "true");
      await expect(divider).toHaveCSS("width", "1px");
      await expect(divider).toHaveCSS("height", "16px");
    }
    await footer
      .getByRole("combobox", { name: "Workspace", exact: true })
      .selectOption("worktree");
    await expect(
      footer.getByRole("combobox", { name: "Start from", exact: true }),
    ).toBeVisible();
    await expect(branch).toHaveCount(0); // Don't show the project's branch as this worktree's branch.
    await expect(separators).toHaveCount(width < 768 ? 3 : 2);
    await page.screenshot({
      path: testInfo.outputPath(`footer-branch-${width}-draft.png`),
    });
    const worktreeBranch =
      "ted/isolated-chat-with-a-long-descriptive-branch-name";
    fixture.setWorkspace("a1", {
      mode: "worktree",
      base_branch: "origin/release",
      branch: worktreeBranch,
      path: "/worktrees/a1",
      locked: true,
      status: "ready",
    });
    await expect(branch).toHaveText(worktreeBranch);
    await expect(branch).toHaveAttribute("title", worktreeBranch);
    await expect(footer.getByText("main", { exact: true })).toHaveCount(0);
    await expect(
      footer.getByRole("group", { name: "Workspace location", exact: true }),
    ).toHaveText("Worktree");
    await expect(footer.getByRole("combobox")).toHaveCount(0);
    await expect(separators).toHaveCount(width < 768 ? 3 : 2);
    const branchBox = (await branch.boundingBox())!;
    const contextBox = (await footer
      .locator('[data-slot="context-usage"]')
      .boundingBox())!;
    expect(branchBox.width).toBeGreaterThan(28);
    expect(branchBox.x + branchBox.width).toBeLessThan(contextBox.x);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    if (width < 768)
      await expect(
        footer.getByRole("button", { name: "Open sidebar", exact: true }),
      ).toBeInViewport();
    await page.screenshot({
      path: testInfo.outputPath(`footer-branch-${width}-locked.png`),
    });
    // A missing worktree branch must not fall back to the main checkout.
    fixture.setWorkspace("a1", {
      mode: "worktree",
      locked: true,
      status: "failed",
      error: "Setup failed",
    });
    await expect(branch).toHaveCount(0);
    await expect(separators).toHaveCount(width < 768 ? 2 : 1);
  });
}

test("local footer branch follows project updates, not the worktree base setting", async ({
  page,
}) => {
  const fixture = await workspace(page);
  await createChat(page);
  const footer = page.getByRole("group", {
    name: "Composer footer",
    exact: true,
  });
  fixture.setWorkspace("a1", {
    mode: "current_checkout",
    base_branch: "origin/release",
    locked: true,
    status: "ready",
  });
  const branch = footer.getByRole("group", { name: "Git branch", exact: true });
  await expect(branch).toHaveText("main");
  await page.route("**/v1/projects/p", (route) =>
    route.fulfill({
      json: {
        id: "p",
        name: "harness",
        root: "/srv/harness",
        git_branch: "feature/checked-out",
        defaults: { model: "test/model", effort: "medium" },
      },
    }),
  );
  await expect(branch).toHaveText("feature/checked-out");
  await expect(footer.getByText("origin/release", { exact: true })).toHaveCount(
    0,
  );
});

test("footer does not leave branch dividers behind outside Git", async ({
  page,
}) => {
  await workspace(page);
  await page.route("**/v1/projects/p", (route) =>
    route.fulfill({
      json: {
        id: "p",
        name: "harness",
        root: "/srv/harness",
        defaults: { model: "test/model", effort: "medium" },
      },
    }),
  );
  await createChat(page);
  const footer = page.getByRole("group", {
    name: "Composer footer",
    exact: true,
  });
  await expect(
    footer.getByRole("group", { name: "Git branch", exact: true }),
  ).toHaveCount(0);
  await expect(footer.locator('[data-slot="separator"]:visible')).toHaveCount(
    1,
  );
});
