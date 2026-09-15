import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

test("bot notifications are distinct while queued, live, and replayed", async ({
  page,
}, testInfo) => {
  const { agents, events, emit } = await workspace(page);
  const now = new Date().toISOString();
  agents.source = {
    id: "source",
    project_id: "p",
    title: "Review chat",
    settings: { model: "test/model", effort: "medium" },
    settled: false,
    held: false,
    state: "idle",
    cursor: 0,
    created_at: now,
    updated_at: now,
  };
  agents.target = {
    id: "target",
    project_id: "p",
    title: "Implementation chat",
    settings: { model: "test/model", effort: "medium" },
    settled: false,
    held: true,
    state: "idle",
    cursor: 0,
    created_at: now,
    updated_at: now,
  };
  events.source = [];
  events.target = [];
  await page.goto("/agents/target");

  const text =
    "Review is complete. " +
    "The implementation and regression coverage are ready to merge. ".repeat(8);
  const notification = {
    id: "nudge-1",
    text,
    kind: "bot",
    sender_agent_id: "source",
    status: "pending",
    created_at: now,
  };
  emit("target", "message.queued", notification);

  const pending = page.getByText(
    /^Bot notification from Review chat: Review is complete\./,
  );
  await expect(pending).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Pending messages" }).getByRole("button", {
      name: "Edit",
    }),
  ).toHaveCount(0);
  await expect(page.locator("[data-bot-notification]")).toHaveCount(0);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
  const pendingPath = testInfo.outputPath("nudge-pending.png");
  await page.screenshot({ path: pendingPath });
  await testInfo.attach("queued bot notification", {
    path: pendingPath,
    contentType: "image/png",
  });

  agents.target.held = false;
  emit("target", "turn.started", { ...notification, status: "running" });
  const card = page.locator("[data-bot-notification]");
  await expect(card).toHaveCount(1);
  const toggle = card.getByRole("button", {
    name: /Bot notification from Review chat: Review is complete\./,
  });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("article", { name: "Your message" })).toHaveCount(
    0,
  );
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    card
      .locator('[data-slot="collapsible-content"]')
      .getByText(text, { exact: true }),
  ).toBeVisible();
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(3000);
  const livePath = testInfo.outputPath("nudge-live.png");
  await page.screenshot({ path: livePath });
  await testInfo.attach("live bot notification", {
    path: livePath,
    contentType: "image/png",
  });

  emit("target", "conversation", [
    {
      role: "user",
      kind: "bot",
      sender_agent_id: "source",
      content: text,
    },
  ]);
  await expect(card).toHaveCount(1);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);
  await page.reload();
  await expect(page.locator("[data-bot-notification]")).toHaveCount(1);
  await expect(
    page.getByRole("button", {
      name: /Bot notification from Review chat: Review is complete\./,
    }),
  ).toBeVisible();
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
});
