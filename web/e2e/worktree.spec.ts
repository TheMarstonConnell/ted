import { test, expect, type Page } from "@playwright/test";
import { openProjectDefaults, workspace } from "./fixtures";

async function createChat(page: Page) {
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect(page).toHaveURL(/\/agents\/a1$/);
}

test("an empty chat can override its worktree base before first send", async ({
  page,
  context,
}) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
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
  await expect(indicator).toContainText("Worktree");
  await expect(indicator).toContainText("origin/release");
  await expect(indicator).not.toContainText("/srv/harness");
  await expect(indicator).not.toContainText("main");
  await expect(page.locator('[data-agent-id="a1"]')).toContainText(
    "origin/release",
  );
  const copy = indicator.getByRole("button", { name: "Copy workspace path" });
  await expect(copy).toHaveAttribute("title", "/var/lib/ted/worktrees/a1");
  await copy.click();
  await expect
    .poll(() => page.evaluate(() => navigator.clipboard.readText()))
    .toBe("/var/lib/ted/worktrees/a1");
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
  await expect(indicator).toContainText("Current checkout");
  await expect(indicator).toContainText("main");
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

test("path copy reports failure when clipboard APIs are unavailable", async ({
  page,
}) => {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    });
    Object.defineProperty(Document.prototype, "execCommand", {
      configurable: true,
      value: () => false,
    });
  });
  const fixture = await workspace(page);
  await createChat(page);
  fixture.setWorkspace("a1", {
    mode: "current_checkout",
    base_branch: "origin/main",
    locked: true,
    status: "ready",
    path: "/srv/harness",
  });
  await page
    .getByRole("button", { name: "Copy workspace path: /srv/harness" })
    .click();
  await expect(page.getByText("Could not copy workspace path.")).toBeVisible();
});
