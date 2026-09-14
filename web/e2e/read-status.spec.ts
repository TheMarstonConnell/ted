import { expect, test, type Page } from "@playwright/test";
import { workspace } from "./fixtures";

const response = (Content: string, ResponseType = "agent") => ({
  Content,
  ResponseType,
  ToolCallID: "",
  ToolName: "",
  FullToolOutput: "",
});

async function seed(page: Page) {
  const fixture = await workspace(page);
  for (const [id, title] of [
    ["a", "Current conversation"],
    ["b", "Review changes"],
  ]) {
    fixture.agents[id] = {
      id,
      title,
      project_id: "p",
      settings: { model: "test/model", effort: "medium" },
      settled: false,
      held: false,
      state: "idle",
      cursor: 0,
      read_cursor: 0,
      last_response_cursor: 0,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    };
    fixture.events[id] = [];
  }
  await page.goto("/agents/a");
  await page.bringToFront();
  await expect(page.getByRole("textbox", { name: "Message" })).toBeEnabled();
  return fixture;
}

for (const colorScheme of ["light", "dark"] as const) {
  test(`unread dot and opening a chat (${colorScheme})`, async ({
    page,
  }, testInfo) => {
    await page.emulateMedia({ colorScheme });
    const { emit, agents } = await seed(page);
    const row = page.locator('[data-agent-id="b"] a');
    const dot = row.locator("[data-unread-dot]");
    emit("b", "output", response("Inspecting files", "tool"));
    await expect(dot).toHaveCount(0);
    emit("b", "output", response("Your changes are ready for review."));
    await expect(dot).toBeVisible();
    await expect(row).toHaveAccessibleName(/Unread: Review changes/);
    const box = await dot.boundingBox();
    const rowBox = await row.boundingBox();
    expect(box!.width).toBe(8);
    expect(box!.height).toBe(8);
    expect(box!.x - rowBox!.x).toBeLessThanOrEqual(2);
    expect(box!.y - rowBox!.y).toBeLessThanOrEqual(2);
    await page.screenshot({
      path: testInfo.outputPath(`unread-${colorScheme}.png`),
    });
    await row.click();
    await expect(
      page.getByText("Your changes are ready for review.", { exact: true }),
    ).toBeVisible();
    await expect(dot).toHaveCount(0);
    await expect.poll(() => agents.b.read_cursor).toBe(2);
    emit("b", "output", response("One more update while you are here."));
    await expect
      .poll(() => agents.b.read_cursor)
      .toBe(agents.b.last_response_cursor);
    await expect(dot).toHaveCount(0);
    await page.reload();
    await expect(
      page.getByText("One more update while you are here.", { exact: true }),
    ).toBeVisible();
    await expect(dot).toHaveCount(0);
  });
}

test("background and unfocused chats stay unread until returning", async ({
  page,
}) => {
  const { emit, agents } = await seed(page);
  // Headless platforms vary in whether bringing a second tab forward changes
  // visibility, so drive the browser lifecycle signals explicitly.
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    Object.defineProperty(document, "hasFocus", {
      configurable: true,
      value: () => false,
    });
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("blur"));
  });
  emit("a", "output", response("An update while away."));
  const dot = page.locator('[data-agent-id="a"] [data-unread-dot]');
  await expect(dot).toBeVisible();
  expect(agents.a.read_cursor).toBe(0);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(dot).toBeVisible();
  expect(agents.a.read_cursor).toBe(0);
  await page.evaluate(() => {
    Object.defineProperty(document, "hasFocus", {
      configurable: true,
      value: () => true,
    });
    window.dispatchEvent(new Event("focus"));
  });
  await expect(dot).toHaveCount(0);
  await expect.poll(() => agents.a.read_cursor).toBe(1);
});

test("receipts from another device clear unread, but a later response restores it", async ({
  page,
}) => {
  const { emit, agents } = await seed(page);
  const dot = page.locator('[data-agent-id="b"] [data-unread-dot]');
  emit("b", "output", response("Ready for review."));
  await expect(dot).toBeVisible();
  agents.b.read_cursor = agents.b.last_response_cursor;
  emit("b", "agent.updated", { ...agents.b });
  await expect(dot).toHaveCount(0);
  emit("b", "output", response("A new response."));
  await expect(dot).toBeVisible();
  await page.reload();
  await expect(dot).toBeVisible();
});

test("failed receipts retry only while the chat is in the foreground", async ({
  page,
}) => {
  const { emit, agents } = await seed(page);
  let attempts = 0;
  await page.route("**/v1/agents/a/read", async (route) => {
    attempts++;
    if (attempts === 1) {
      await route.fulfill({
        status: 503,
        json: { error: { code: "unavailable", message: "Try again" } },
      });
    } else await route.fallback();
  });
  emit("a", "output", response("A response to read."));
  await expect.poll(() => attempts).toBe(1);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect(
    page.locator('[data-agent-id="a"] [data-unread-dot]'),
  ).toBeVisible();
  await page.waitForTimeout(3200);
  expect(attempts).toBe(1);
  expect(agents.a.read_cursor).toBe(0);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await expect.poll(() => agents.a.read_cursor).toBe(1);
  expect(attempts).toBe(2);
});

test("opening a chat waits for its response to load before acknowledging it", async ({
  page,
}) => {
  const fixture = await seed(page);
  // Reconnect with inventory ahead of the event replay, as happens with a
  // slow event reference fetch. No new subscription can mark inventory read.
  let release!: () => void;
  let reads = 0;
  const event = {
    agent_id: "a",
    cursor: 1,
    type: "output",
    data: response("Loaded after replay."),
    created_at: "2026-01-01T00:00:00Z",
  };
  Object.assign(fixture.agents.a, { cursor: 1, last_response_cursor: 1 });
  await page.route("**/v1/agents/a/read", async (route) => {
    reads++;
    await route.fallback();
  });
  await page.routeWebSocket("**/v1/ws", (ws) => {
    ws.onMessage(() => {
      ws.send(JSON.stringify({ type: "subscribed", request_id: "test" }));
      ws.send(JSON.stringify({ type: "inventory", agent: fixture.agents.a }));
      release = () => ws.send(JSON.stringify({ type: "event", event }));
    });
  });
  await page.reload();
  const dot = page.locator('[data-agent-id="a"] [data-unread-dot]');
  await expect(dot).toBeVisible();
  await expect(
    page.getByText("Replaying chat history…", { exact: true }),
  ).toBeVisible();
  expect(reads).toBe(0);
  await expect.poll(() => typeof release).toBe("function");
  release();
  await expect(
    page.getByText("Loaded after replay.", { exact: true }),
  ).toBeVisible();
  await expect.poll(() => reads).toBe(1);
  await expect(dot).toHaveCount(0);
});
