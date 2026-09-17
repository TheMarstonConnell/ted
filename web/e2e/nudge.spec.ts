import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

test("bot notifications merge while pending and remain distinct live and replayed", async ({
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
  await expect(page.getByText("Queue held · 0 pending")).toBeVisible();
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1000);

  const text = "Review is complete.";
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
    /^Bot notification from chat source \(caller-supplied\): Review is complete\./,
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

  const mergedText =
    text +
    "\n\nRegression checks passed. Ready to merge. " +
    "The implementation and regression coverage are ready to merge. ".repeat(8);
  const merged = { ...notification, text: mergedText };
  emit("target", "message.updated", merged);
  const pendingQueue = page.getByRole("region", { name: "Pending messages" });
  await expect(pendingQueue.locator("[data-pending-message-id]")).toHaveCount(
    1,
  );
  await expect(
    pendingQueue.locator('[data-pending-message-id="nudge-1"]'),
  ).toContainText(mergedText);
  await expect(pendingQueue.getByText("Queue held · 1 pending")).toBeVisible();
  await expect(page.locator("[data-bot-notification]")).toHaveCount(0);
  await expect(page.getByRole("article", { name: "Your message" })).toHaveCount(
    0,
  );
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
  const mergedPath = testInfo.outputPath("nudge-merged-pending.png");
  await page.screenshot({ path: mergedPath });
  await testInfo.attach("merged pending bot notification", {
    path: mergedPath,
    contentType: "image/png",
  });

  await page.reload();
  await expect(pendingQueue.locator("[data-pending-message-id]")).toHaveCount(
    1,
  );
  await expect(
    pendingQueue.locator('[data-pending-message-id="nudge-1"]'),
  ).toContainText(mergedText);
  await expect(page.locator("[data-bot-notification]")).toHaveCount(0);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);

  agents.target.held = false;
  emit("target", "turn.started", { ...merged, status: "running" });
  await expect(pendingQueue.locator("[data-pending-message-id]")).toHaveCount(
    0,
  );
  const card = page.locator("[data-bot-notification]");
  await expect(card).toHaveCount(1);
  const toggle = card.getByRole("button", {
    name: /Bot notification from chat source \(caller-supplied\): Review is complete\./,
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
      .getByText(mergedText, { exact: true }),
  ).toBeVisible();
  await expect(
    card.getByText("From chat source (caller-supplied)", { exact: true }),
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
      content: mergedText,
    },
  ]);
  await expect(card).toHaveCount(1);
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1500);
  await page.reload();
  await expect(page.locator("[data-bot-notification]")).toHaveCount(1);
  await expect(
    page.getByRole("button", {
      name: /Bot notification from chat source \(caller-supplied\): Review is complete\./,
    }),
  ).toBeVisible();
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(1000);
  await toggle.click();
  await expect(
    card
      .locator('[data-slot="collapsible-content"]')
      .getByText(mergedText, { exact: true }),
  ).toBeVisible();
  if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
});
