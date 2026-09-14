import { test, expect } from "@playwright/test";
import { openProjectDefaults, workspace } from "./fixtures";

test("settled project deletion warns, can be cancelled, and removes sidebar history", async ({
  page,
}) => {
  const { agents } = await workspace(page);
  let deleted = false;
  await page.route("**/v1/projects/p", async (route) => {
    if (route.request().method() !== "DELETE") return route.fallback();
    expect(Object.values(agents).every((agent) => agent.settled)).toBe(true);
    deleted = true;
    for (const id of Object.keys(agents)) delete agents[id];
    await route.fulfill({ status: 204 });
  });
  await page.route("**/v1/projects", async (route) => {
    if (!deleted) return route.fallback();
    await route.fulfill({ json: [] });
  });
  await page.goto("/");
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await page.getByRole("button", { name: "Settle agent", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Restore agent", exact: true }),
  ).toBeVisible();
  await openProjectDefaults(page);
  await page
    .getByRole("button", { name: "Delete project", exact: true })
    .click();
  await expect(
    page.getByText("permanently delete all your settled agents", {
      exact: false,
    }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Keep project", exact: true }).click();
  expect(deleted).toBe(false);
  await page
    .getByRole("button", { name: "Delete project", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Confirm delete", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.locator('a[href="/agents/a1"]')).toHaveCount(0);
  expect(deleted).toBe(true);
});
