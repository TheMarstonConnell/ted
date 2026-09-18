import { expect, test, type Page } from "@playwright/test";
import { workspace } from "./fixtures";

async function largeWorkspace(page: Page) {
  const fixture = await workspace(page);
  const text =
    "Retained tool output that should not load in the sidebar. ".repeat(300);
  for (let index = 0; index < 42; index++) {
    const id = index < 2 ? ["a", "b"][index] : `archive-${index}`;
    const count = index < 2 ? 1 : 128;
    fixture.agents[id] = {
      id,
      title:
        index < 2
          ? ["Current conversation", "Review changes"][index]
          : `Archived investigation ${index}`,
      project_id: "p",
      settings: { model: "test/model", effort: "medium" },
      settled: index >= 2,
      held: false,
      state: "idle",
      cursor: count,
      read_cursor: 0,
      last_response_cursor: index < 2 ? count : 0,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      messages: [{ role: "assistant", content: text.repeat(64) }],
      queue: [{ id: "old", text, status: "completed" }],
    };
    fixture.events[id] = Array.from({ length: count }, (_, eventIndex) => ({
      agent_id: id,
      cursor: eventIndex + 1,
      type: "output",
      created_at: "2026-01-01T00:00:00Z",
      data: {
        Content: index < 2 ? `${fixture.agents[id].title} loaded.` : text,
        ResponseType: index < 2 ? "agent" : "tool_result",
        ToolCallID: "",
        ToolName: "bash",
        FullToolOutput: "",
      },
    }));
  }
  fixture.agents.b.title = "";
  fixture.agents.b.queue = [
    { id: "first", text: "Review changes", status: "completed" },
  ];
  return fixture;
}

for (const path of ["/", "/agents/a"]) {
  test(`large workspace starts without replaying background history: ${path}`, async ({
    page,
  }, testInfo) => {
    const fixture = await largeWorkspace(page);
    const pause = async () => {
      if (process.env.TED_WEB_RECORD) await page.waitForTimeout(2000);
    };
    const errors: string[] = [];
    const listRequests: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/v1/agents")
        listRequests.push(request.url());
    });
    await page.goto(path);
    await expect(page.locator('[data-agent-id="a"] a')).toBeVisible();
    await expect.poll(() => fixture.subscriptions.length).toBeGreaterThan(0);
    expect(listRequests.length).toBeGreaterThan(0);
    expect(
      listRequests.every(
        (url) => new URL(url).searchParams.get("summary") === "true",
      ),
    ).toBe(true);
    expect(
      fixture.subscriptions.every(
        (sub) => sub.event_agent_ids.length === (path === "/" ? 0 : 1),
      ),
    ).toBe(true);
    await pause();
    if (path === "/") {
      expect(fixture.deliveredEvents).toEqual([]);
      await page.locator('[data-agent-id="a"] a').click();
    }
    await expect(
      page.getByText("Current conversation loaded.", { exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Message" })).toBeEnabled();
    expect(
      fixture.deliveredEvents.every((event) => event.agent_id === "a"),
    ).toBe(true);

    await pause();
    fixture.emit("b", "output", {
      Content: "Review finished in the background.",
      ResponseType: "agent",
      ToolCallID: "",
      ToolName: "",
      FullToolOutput: "",
    });
    await expect(
      page.locator('[data-agent-id="b"] [data-unread-dot]'),
    ).toBeVisible();
    await expect(page.locator('[data-agent-id="b"] a')).toHaveAccessibleName(
      /Review changes/,
    );
    expect(
      fixture.deliveredEvents.every((event) => event.agent_id === "a"),
    ).toBe(true);
    await pause();
    await page.locator('[data-agent-id="b"] a').click();
    await expect(
      page.getByText("Review finished in the background.", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator('[data-agent-id="b"] [data-unread-dot]'),
    ).toHaveCount(0);
    await pause();
    await page.locator('[data-agent-id="a"] a').click();
    await expect(
      page.getByText("Current conversation loaded.", { exact: true }),
    ).toBeVisible();
    expect(
      fixture.deliveredEvents.some((event) =>
        event.agent_id.startsWith("archive-"),
      ),
    ).toBe(false);
    expect(errors).toEqual([]);
    await pause();
    await page.screenshot({ path: testInfo.outputPath("startup.png") });
  });
}
