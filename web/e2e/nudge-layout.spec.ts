import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

for (const width of [320, 390, 1440]) {
  test(`bot source attribution fits and remains readable at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 844 });
    const { agents, events, emit } = await workspace(page);
    const now = new Date().toISOString();
    agents.target = {
      id: "target",
      project_id: "p",
      title: "Notification layout",
      settings: { model: "test/model", effort: "medium" },
      settled: false,
      held: false,
      state: "idle",
      cursor: 0,
      created_at: now,
      updated_at: now,
    };
    events.target = [];
    await page.goto("/agents/target");
    const source = "s".repeat(256);
    const message = {
      id: "bot-layout",
      text: "Review complete. " + "A".repeat(2048),
      kind: "bot",
      sender_agent_id: source,
      status: "running",
      created_at: now,
    };
    emit("target", "turn.started", message);
    const card = page.locator("[data-bot-notification]");
    await expect(card).toBeVisible();
    const contained = () =>
      card.evaluate((node) => node.scrollWidth <= node.clientWidth + 1);
    expect(await contained()).toBe(true);
    await card.getByRole("button").click();
    await expect(
      card.getByText(`From chat ${source} (caller-supplied)`, { exact: true }),
    ).toBeVisible();
    expect(await contained()).toBe(true);
    await page.reload();
    await expect(card).toBeVisible();
    expect(await contained()).toBe(true);
  });
}
