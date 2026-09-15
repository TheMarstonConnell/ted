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
  await expect(child).not.toBeVisible();
  await expect(grandchild).not.toBeVisible();
  await root.getByRole("link").hover();
  const rootToggle = root.getByRole("button", {
    name: /child chats for Parent chat/,
  });
  await expect(rootToggle).toHaveAttribute("aria-expanded", "false");
  // A websocket inventory can establish parentage while the family is closed
  // without opening the new child or leaving its old Misc group behind.
  fixture.updateAgent("loose", { parent_agent_id: "root" });
  await expect(page.getByText("Misc", { exact: true })).toHaveCount(0);
  await expect(rootToggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('[data-agent-id="loose"]')).not.toBeVisible();
  await root.getByRole("link").hover();
  await rootToggle.click();
  await expect(child).toBeVisible();
  await expect(grandchild).not.toBeVisible();
  await expect(rootChildren.locator('[data-agent-id="child"]')).toBeVisible();
  await expect(rootChildren.locator('[data-agent-id="loose"]')).toBeVisible();
  // The child has different project metadata but follows its family root into
  // the harness project instead of becoming a top-level Misc chat.
  await expect(
    page
      .locator('[data-project-id="p"]')
      .locator("xpath=following-sibling::*[1]")
      .locator('[data-agent-id="child"]'),
  ).toBeVisible();
  await expect(page.getByText("Misc", { exact: true })).toHaveCount(0);
  await expect(page.locator("a button, button a")).toHaveCount(0);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1000);

  await root.getByRole("link").hover();
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

  await child.getByRole("link").hover();
  await child
    .getByRole("button", {
      name: "Expand child chats for Child chat",
      exact: true,
    })
    .click();
  await expect(
    childChildren.locator('[data-agent-id="grandchild"]'),
  ).toBeVisible();
  await grandchild.getByRole("link").click();
  await expect(page).toHaveURL(/\/agents\/grandchild$/);
  await expect(grandchild.getByRole("link")).toHaveAttribute(
    "aria-current",
    "page",
  );

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

  await page.reload();
  await expect(rootToggle).toHaveAttribute("aria-expanded", "false");
  await expect(child).not.toBeVisible();
  await expect(page).toHaveURL(/\/agents\/grandchild$/);
});

test("child toggles reveal on the right without moving settle or switching chats", async ({
  page,
}) => {
  await seedFamily(page);
  await page.goto("/agents/grandchild");
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("Keep this draft");
  await draft.hover();
  const root = page.locator('[data-agent-id="root"]');
  const child = page.locator('[data-agent-id="child"]');
  const link = root.getByRole("link");
  const toggle = root.getByRole("button", {
    name: /child chats for Parent chat/,
  });
  const settleButton = root.getByRole("button", {
    name: "Settle chat: Parent chat",
    exact: true,
  });
  const childToggle = child.getByRole("button", {
    name: /child chats for Child chat/,
  });
  for (const action of [toggle, settleButton]) {
    await expect(action).toHaveCSS("width", "0px");
    await expect(action).toHaveCSS("opacity", "0");
  }
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(child).not.toBeVisible();
  const before = (await link.boundingBox())!;
  expect(before.width).toBe((await root.boundingBox())!.width);
  await link.hover();
  for (const action of [toggle, settleButton]) {
    await expect(action).toHaveCSS("width", "32px");
    await expect(action).toHaveCSS("opacity", "1");
  }
  await expect(child).not.toBeVisible();
  await expect
    .poll(async () => (await link.boundingBox())!.width)
    .toBeCloseTo(before.width - 80, 0);
  const after = (await link.boundingBox())!;
  const toggleBox = (await toggle.boundingBox())!;
  const settleBox = (await settleButton.boundingBox())!;
  expect(after.x).toBe(before.x);
  expect(after.y).toBe(before.y);
  expect(after.height).toBe(before.height);
  expect(toggleBox.x).toBe(after.x + after.width + 8);
  expect(settleBox.x).toBe(toggleBox.x + toggleBox.width + 8);
  expect(settleBox.x + settleBox.width).toBe(before.x + before.width);
  await toggle.hover();
  await expect(toggle).toHaveCSS("opacity", "1");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(child).toBeVisible();
  await expect(childToggle).toHaveCSS("width", "0px");
  await expect(childToggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator('[data-agent-id="grandchild"]')).not.toBeVisible();
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(child).not.toBeVisible();
  await expect(page).toHaveURL(/\/agents\/grandchild$/);
  await expect(draft).toHaveValue("Keep this draft");
  await draft.focus();
  await draft.hover();
  await expect(toggle).toHaveCSS("width", "0px");
  await link.focus();
  await page.keyboard.press("Tab");
  await expect(toggle).toBeFocused();
  await expect(toggle).toHaveCSS("opacity", "1");
  await page.keyboard.press("Enter");
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(child).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(settleButton).toBeFocused();

  await child.getByRole("link").focus();
  await child.getByRole("link").hover();
  await expect(childToggle).toHaveCSS("opacity", "1");
  await expect(toggle).toHaveCSS("width", "0px");
  await expect(settleButton).toHaveCSS("width", "0px");

  await page.setViewportSize({ width: 700, height: 844 });
  await page.goto("/agents/grandchild?sidebar=open");
  expect(
    await page.evaluate(
      () =>
        matchMedia("(hover: hover) and (pointer: fine)").matches &&
        !matchMedia("(any-pointer: coarse)").matches,
    ),
  ).toBe(true);
  const drawer = page.getByRole("dialog", { name: "Workspace", exact: true });
  const narrowRoot = drawer.locator('[data-agent-id="root"]');
  const narrowLink = narrowRoot.getByRole("link");
  const narrowToggle = narrowRoot.getByRole("button", {
    name: /child chats for Parent chat/,
  });
  const narrowSettle = narrowRoot.getByRole("button", {
    name: "Settle chat: Parent chat",
    exact: true,
  });

  for (const action of [narrowToggle, narrowSettle]) {
    await expect(action).toHaveCSS("width", "0px");
    await expect(action).toHaveCSS("min-width", "0px");
    await expect(action).toHaveCSS("opacity", "0");
  }
  const restingLink = (await narrowLink.boundingBox())!;
  expect(restingLink.width).toBe((await narrowRoot.boundingBox())!.width);

  await narrowLink.hover();
  for (const action of [narrowToggle, narrowSettle]) {
    await expect(action).toHaveCSS("opacity", "1");
    const box = (await action.boundingBox())!;
    expect(box.width).toBeGreaterThanOrEqual(48);
    expect(box.height).toBeGreaterThanOrEqual(48);
  }
});

test.describe("touch family controls", () => {
  test.use({ hasTouch: true });

  test("agent family controls and navigation fit the mobile drawer", async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await seedFamily(page);
    await page.goto("/agents/root?sidebar=open");

    const drawer = page.getByRole("dialog", { name: "Workspace", exact: true });
    const child = drawer.locator('[data-agent-id="child"]');
    const toggle = drawer.getByRole("button", {
      name: /child chats for Parent chat/,
      exact: true,
    });
    await expect(toggle).toBeVisible();
    const root = drawer.locator('[data-agent-id="root"]');
    const settleButton = root.getByRole("button", {
      name: "Settle chat: Parent chat",
      exact: true,
    });
    for (const action of [toggle, settleButton]) {
      await expect(action).toHaveCSS("opacity", "1");
      const box = (await action.boundingBox())!;
      expect(box.width).toBeGreaterThanOrEqual(48);
      expect(box.height).toBeGreaterThanOrEqual(48);
    }
    const linkBox = (await root.getByRole("link").boundingBox())!;
    const toggleBox = (await toggle.boundingBox())!;
    const settleBox = (await settleButton.boundingBox())!;
    expect(toggleBox.x).toBeGreaterThanOrEqual(linkBox.x + linkBox.width);
    expect(settleBox.x).toBeGreaterThanOrEqual(toggleBox.x + toggleBox.width);
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(child).not.toBeVisible();
    await page.screenshot({
      path: testInfo.outputPath("sidebar-family-mobile-default-collapsed.png"),
    });
    await toggle.tap();
    await expect(child).toBeVisible();
    await expect(
      drawer.locator('[data-agent-id="grandchild"]'),
    ).not.toBeVisible();
    const bounds = (await child.boundingBox())!;
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(390);
    await expect(
      child.getByRole("button", {
        name: "Settle chat: Child chat",
        exact: true,
      }),
    ).toBeVisible();

    await page.screenshot({
      path: testInfo.outputPath("sidebar-agent-family-mobile.png"),
    });
    await child.getByRole("link").click();
    await expect(drawer).toHaveCount(0);
    await expect(page).toHaveURL(/\/agents\/child$/);
    if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);
  });
});
