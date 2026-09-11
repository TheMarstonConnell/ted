import { expect, test, type Page } from "@playwright/test";
import { workspace } from "./fixtures";

function sidebarAgent(
  id: string,
  title: string,
  created: number,
  parent_agent_id?: string,
) {
  return {
    id,
    project_id: ["child", "loose"].includes(id)
      ? "explicit-workspace-project"
      : "p",
    ...(parent_agent_id ? { parent_agent_id } : {}),
    title,
    settings: { model: "test/model", effort: "medium" },
    settled: false,
    held: false,
    state: "idle",
    workspace: {
      mode: "worktree",
      shared: Boolean(parent_agent_id),
      locked: true,
      status: "ready",
      path: "/srv/ted/worktrees/harness/parent",
      branch: "ted/parent-chat",
      base_branch: "origin/main",
    },
    cursor: 0,
    created_at: `2026-01-0${created}T00:00:00Z`,
    updated_at: `2026-01-0${created}T00:00:00Z`,
  };
}

async function seedFamily(page: Page) {
  const fixture = await workspace(page);
  Object.assign(fixture.agents, {
    root: sidebarAgent("root", "Parent chat", 1),
    child: sidebarAgent("child", "Child chat", 3, "root"),
    grandchild: sidebarAgent("grandchild", "Grandchild chat", 4, "child"),
    loose: sidebarAgent("loose", "Loose chat", 2, "missing-parent"),
  });
  for (const id of Object.keys(fixture.agents)) fixture.events[id] = [];
  return fixture;
}

async function settle(page: Page, title: string) {
  const button = page.getByRole("button", {
    name: `Settle chat: ${title}`,
    exact: true,
  });
  await button.focus();
  await button.click();
  await expect(
    page.getByRole("button", {
      name: `Restore chat: ${title}`,
      exact: true,
    }),
  ).toBeAttached();
}

test("agent families nest, collapse, navigate and follow inventory updates", async ({
  page,
}, testInfo) => {
  const fixture = await seedFamily(page);
  await page.goto("/agents/root");

  const root = page.locator('[data-agent-id="root"]');
  const child = page.locator('[data-agent-id="child"]');
  const grandchild = page.locator('[data-agent-id="grandchild"]');
  const rootChildren = page.getByRole("group", {
    name: "Child chats for Parent chat",
    exact: true,
  });
  const childChildren = page.getByRole("group", {
    name: "Child chats for Child chat",
    exact: true,
  });

  await expect(root.getByRole("link")).toHaveAttribute("aria-current", "page");
  await expect(rootChildren.locator('[data-agent-id="child"]')).toBeVisible();
  await expect(
    childChildren.locator('[data-agent-id="grandchild"]'),
  ).toBeVisible();
  // The child has different project metadata but follows its family root into
  // the harness project instead of becoming a top-level Misc chat.
  await expect(
    page
      .locator('[data-project-id="p"]')
      .locator("xpath=following-sibling::*[1]")
      .locator('[data-agent-id="child"]'),
  ).toBeVisible();
  await expect(page.getByText("Misc", { exact: true })).toBeVisible();
  await expect(page.locator("a button, button a")).toHaveCount(0);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1000);

  await page
    .getByRole("button", {
      name: "Collapse child chats for Parent chat",
      exact: true,
    })
    .click();
  await expect(child).not.toBeVisible();
  await expect(grandchild).not.toBeVisible();
  await expect(root).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath("sidebar-agent-family-collapsed.png"),
  });
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1000);
  await page
    .getByRole("button", {
      name: "Expand child chats for Parent chat",
      exact: true,
    })
    .click();

  await grandchild.getByRole("link").click();
  await expect(page).toHaveURL(/\/agents\/grandchild$/);
  await expect(grandchild.getByRole("link")).toHaveAttribute(
    "aria-current",
    "page",
  );

  // A websocket inventory can establish parentage after initial load.
  fixture.updateAgent("loose", { parent_agent_id: "root" });
  await expect(rootChildren.locator('[data-agent-id="loose"]')).toBeVisible();
  await expect(page.getByText("Misc", { exact: true })).toHaveCount(0);

  await page.screenshot({
    path: testInfo.outputPath("sidebar-agent-family-desktop.png"),
  });
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);

  // Settling one member is independent: it neither settles descendants nor
  // removes a mixed active family from its root project.
  await settle(page, "Child chat");
  await expect(
    grandchild.getByRole("button", {
      name: "Settle chat: Grandchild chat",
      exact: true,
    }),
  ).toBeAttached();
  await settle(page, "Parent chat");
  await expect(rootChildren).toBeVisible();
  await expect(
    grandchild.getByRole("button", {
      name: "Settle chat: Grandchild chat",
      exact: true,
    }),
  ).toBeAttached();
});

test("agent family controls and navigation fit the mobile drawer", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await seedFamily(page);
  await page.goto("/agents/root?sidebar=open");

  const drawer = page.getByRole("dialog", { name: "Workspace", exact: true });
  const child = drawer.locator('[data-agent-id="child"]');
  const toggle = drawer.getByRole("button", {
    name: "Collapse child chats for Parent chat",
    exact: true,
  });
  await expect(toggle).toBeVisible();
  await expect(child).toBeVisible();
  const bounds = (await child.boundingBox())!;
  expect(bounds.x).toBeGreaterThanOrEqual(0);
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(390);
  await expect(
    child.getByRole("button", { name: "Settle chat: Child chat", exact: true }),
  ).toBeVisible();

  await toggle.click();
  await expect(child).not.toBeVisible();
  await drawer
    .getByRole("button", {
      name: "Expand child chats for Parent chat",
      exact: true,
    })
    .click();
  await page.screenshot({
    path: testInfo.outputPath("sidebar-agent-family-mobile.png"),
  });
  await child.getByRole("link").click();
  await expect(drawer).toHaveCount(0);
  await expect(page).toHaveURL(/\/agents\/child$/);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);
});
