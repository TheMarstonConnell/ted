import { test, expect, type Page, type Locator } from "@playwright/test";

async function chooseOption(page: Page, trigger: Locator, label: string) {
  await trigger.click();
  await page.getByRole("option", { name: label, exact: true }).click();
  await expect(page.getByRole("listbox")).toHaveCount(0);
}

// Deterministic browser tests: no provider credentials, paid turns, or writes to
// real projects. Real same-origin Go/Vite smoke testing is documented separately.
async function workspace(page: Page) {
  let project = {
    id: "p",
    name: "harness",
    root: "/srv/harness",
    git_branch: "main",
    defaults: { model: "test/model", effort: "medium" },
  };
  const agents: Record<string, any> = {};
  const events: Record<string, any[]> = {};
  const sockets: any[] = [];
  function inventory(id: string) {
    sockets.forEach((ws) =>
      ws.send(JSON.stringify({ type: "inventory", agent: agents[id] })),
    );
  }
  function emit(id: string, type: string, data: unknown) {
    const event = {
      agent_id: id,
      cursor: ++agents[id].cursor,
      type,
      data,
      created_at: new Date().toISOString(),
    };
    events[id].push(event);
    inventory(id);
    sockets.forEach((ws) => ws.send(JSON.stringify({ type: "event", event })));
  }
  await page.routeWebSocket("**/v1/ws", (ws) => {
    sockets.push(ws);
    ws.onMessage((raw) => {
      const request = JSON.parse(String(raw));
      ws.send(
        JSON.stringify({ type: "subscribed", request_id: request.request_id }),
      );
      Object.values(agents).forEach((agent) => {
        ws.send(JSON.stringify({ type: "inventory", agent }));
        events[agent.id]
          .filter((e) => e.cursor > (request.cursors[agent.id] || 0))
          .forEach((event) =>
            ws.send(JSON.stringify({ type: "event", event })),
          );
      });
    });
  });
  await page.route("**/v1/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      method = request.method();
    const body = request.postDataJSON();
    let result: unknown;
    if (url.pathname === "/v1/projects") {
      if (method === "POST") project = { ...project, ...body };
      result = method === "POST" ? project : [project];
    } else if (url.pathname === "/v1/projects/p") result = project;
    else if (url.pathname === "/v1/models")
      result = [
        {
          id: "test/model",
          name: "Test model",
          provider: "test",
          context_window: 100000,
          efforts: ["low", "medium", "high"],
          default_effort: "medium",
        },
      ];
    else if (url.pathname === "/v1/agents") {
      if (method === "POST") {
        expect(body).toEqual({ project_id: "p" });
        const id = `a${Object.keys(agents).length + 1}`;
        agents[id] = {
          id,
          project_id: "p",
          title: "",
          settings: project.defaults,
          settled: false,
          held: false,
          state: "idle",
          cursor: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        events[id] = [];
        inventory(id);
        result = agents[id];
      } else result = Object.values(agents);
    } else {
      const [, , , id, operation] = url.pathname.split("/");
      if (method === "PATCH" && !operation) {
        agents[id].settled = body.settled;
        inventory(id);
      }
      if (operation === "messages" && method === "DELETE") {
        const messageId = decodeURIComponent(url.pathname.split("/")[5]);
        const message = events[id].findLast(
          (event) =>
            (event.type.startsWith("message.") ||
              event.type.startsWith("turn.")) &&
            event.data.id === messageId,
        )?.data;
        if (!message || !["pending", "cancelled"].includes(message.status)) {
          await route.fulfill({
            status: message ? 409 : 404,
            json: {
              error: { message: "Only pending messages can be removed" },
            },
          });
          return;
        }
        if (message.status === "pending")
          emit(id, "message.cancelled", { ...message, status: "cancelled" });
        await route.fulfill({ status: 204 });
        return;
      }
      if (operation === "messages" && method === "POST") {
        expect(agents[id].settled).toBe(false);
        expect(request.headers()["idempotency-key"]).toBeTruthy();
        const queued = {
          id: `m${events[id].length}`,
          text: body.text,
          status: "pending",
          created_at: new Date().toISOString(),
        };
        emit(id, "message.queued", queued);
        emit(id, "turn.started", { ...queued, status: "running" });
        emit(id, "output", {
          Content: "**Ready to build.**\n\n```ts\nconst connected = true\n```",
          ResponseType: "agent",
          ToolCallID: "",
          ToolName: "",
          FullToolOutput: "",
        });
        emit(id, "turn.completed", { ...queued, status: "completed" });
        result = queued;
      } else if (operation === "settings" && method === "PATCH") {
        agents[id].settings = { ...agents[id].settings, ...body };
        inventory(id);
        result = agents[id];
      } else result = agents[id];
    }
    await route.fulfill({
      status:
        method === "POST" && !url.pathname.endsWith("messages") ? 201 : 200,
      json: result,
    });
  });
  return { agents, events, emit };
}

test("complete project-first workflow, settlement, Markdown, drafts and deep links", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const { agents } = await workspace(page);
  await page.goto("/");
  await expect(
    page.getByRole("link", { name: "harness /srv/harness", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Connected to server", { exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect(page).toHaveURL(/\/agents\/a1$/);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("My unsent draft");
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await expect(
    page.getByRole("textbox", { name: "Message", exact: true }),
  ).toHaveValue("");
  await page.locator('a[href="/agents/a1"]').click();
  await expect(
    page.getByRole("textbox", { name: "Message", exact: true }),
  ).toHaveValue("My unsent draft");
  await page.getByRole("button", { name: "Settle agent", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Restore agent", exact: true }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/agents\/a1$/);
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("Build a web control plane");
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .press("Enter");
  await expect(
    page.getByRole("textbox", { name: "Message", exact: true }),
  ).toHaveValue("");
  await expect(page.locator(".markdown strong")).toHaveText("Ready to build.");
  expect(agents.a1.settled).toBe(false);
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("/help");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(
    page.getByText("Use // to send", { exact: false }),
  ).toBeVisible();
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("Clear on refresh");
  await page.reload();
  await expect(
    page.getByRole("textbox", { name: "Message", exact: true }),
  ).toHaveValue("");
  await expect(page.locator(".markdown strong")).toHaveCount(1);
  await page.getByRole("button", { name: "View context", exact: true }).click();
  await expect(page).toHaveURL(/panel=settings/);
  await chooseOption(
    page,
    page.getByRole("dialog").getByLabel("Reasoning effort"),
    "high",
  );
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(agents.a1.settings.effort).toBe("high");
  expect(errors).toEqual([]);
});

test("folder-derived project creation and mobile drawer", async ({ page }) => {
  await workspace(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await page.getByRole("button", { name: "Open sidebar" }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "New project", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await page
    .getByLabel("Server directory")
    .fill("/new-pool/network/Github/control-plane/");
  await expect(page.getByText("Project name: control-plane")).toBeVisible();
  await page
    .getByRole("button", { name: "Create project", exact: true })
    .click();
  await expect(page.getByRole("dialog", { name: "New chat" })).toBeVisible();
  await page
    .getByRole("button", {
      name: "control-plane /new-pool/network/Github/control-plane/",
    })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("Message Scroller follows the bottom, respects reading position, and exposes tool output", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  const output = (text: string) => ({
    Content: text,
    ResponseType: "agent",
    ToolCallID: "",
    ToolName: "",
    FullToolOutput: "",
  });
  for (let i = 0; i < 25; i++)
    emit(
      "a1",
      "output",
      output(`Message ${i}\n\n${"A useful explanation. ".repeat(20)}`),
    );
  const viewport = page.getByRole("region", { name: "Messages", exact: true });
  await expect
    .poll(() =>
      viewport.evaluate((e) => e.scrollHeight - e.scrollTop - e.clientHeight),
    )
    .toBeLessThan(5);
  await viewport.hover();
  await page.mouse.wheel(0, -10000);
  await expect
    .poll(() => viewport.evaluate((e) => e.scrollTop))
    .toBeLessThan(10);
  emit("a1", "output", output("A new message while you read older history"));
  await page.waitForTimeout(300);
  await expect
    .poll(() => viewport.evaluate((e) => e.scrollTop))
    .toBeLessThan(10);
  await page.getByRole("button", { name: "Scroll to end" }).click();
  await expect
    .poll(() =>
      viewport.evaluate((e) => e.scrollHeight - e.scrollTop - e.clientHeight),
    )
    .toBeLessThan(5);
  emit("a1", "output", {
    ...output("capped"),
    ResponseType: "tool_result",
    ToolName: "bash",
    FullToolOutput: "Complete uncapped tool output",
  });
  await page.getByRole("button", { name: "bash output", exact: true }).click();
  await expect(
    page.getByText("Complete uncapped tool output", { exact: true }),
  ).toBeVisible();
});

for (const colorScheme of ["light", "dark"] as const) {
  test(`plain interface remains usable on desktop and mobile (${colorScheme})`, async ({
    page,
  }, testInfo) => {
    const { agents, emit } = await workspace(page);
    await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
    const capture = async (name: string) => {
      const path = testInfo.outputPath(`${name}.png`);
      await page.screenshot({ path });
      await testInfo.attach(name, { path, contentType: "image/png" });
    };
    const fitsViewport = async () => {
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    };

    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "Projects", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "harness /srv/harness", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("Connected to server", { exact: true }),
    ).toHaveCount(0);
    await expect(page.locator('[data-slot="badge"]')).toHaveCount(0);
    await expect(
      page.getByText(/CONTROL PLANE|YOUR AGENTS|Let’s build something/),
    ).toHaveCount(0);
    expect(
      await page.locator("html").evaluate((e) => e.classList.contains("dark")),
    ).toBe(colorScheme === "dark");
    await fitsViewport();
    await capture(`workspace-${colorScheme}`);

    await page.getByRole("button", { name: "New chat", exact: true }).click();
    await expect(
      page.getByRole("dialog", { name: "New chat", exact: true }),
    ).toBeVisible();
    await capture(`project-picker-${colorScheme}`);
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    await expect(
      page.getByText("Send a message to start this chat."),
    ).toBeVisible();
    // The header has only the title and actions, not project breadcrumbs or state chips.
    await expect(page.locator("header")).not.toContainText("harness");
    await expect(page.locator("header")).not.toContainText("idle");
    await page
      .getByRole("textbox", { name: "Message", exact: true })
      .fill("Show me the project structure.");
    await page
      .getByRole("button", { name: "Send message", exact: true })
      .click();
    await expect(page.locator(".markdown strong")).toHaveText(
      "Ready to build.",
    );
    const userMessage = page.getByRole("article", {
      name: "Your message",
      exact: true,
    });
    const assistantMessage = page.getByRole("article", {
      name: "Assistant message",
      exact: true,
    });
    await expect(userMessage).toContainText("Show me the project structure.");
    await expect(
      userMessage.getByRole("button", { name: "Copy message", exact: true }),
    ).toBeVisible();
    await expect(
      assistantMessage.getByRole("button", {
        name: "Copy message",
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page
        .getByRole("region", { name: "Messages", exact: true })
        .getByText(/^(You|Ted)$/),
    ).toHaveCount(0);
    const userBox = (await userMessage.boundingBox())!;
    const assistantBox = (await assistantMessage.boundingBox())!;
    expect(userBox.x).toBeGreaterThan(assistantBox.x);
    expect(
      Math.abs(userBox.x + userBox.width - assistantBox.x - assistantBox.width),
    ).toBeLessThan(1);
    expect(
      await userMessage.evaluate((e) => getComputedStyle(e).backgroundColor),
    ).not.toBe(
      await assistantMessage.evaluate(
        (e) => getComputedStyle(e).backgroundColor,
      ),
    );
    emit("a1", "output", {
      Content: "src/\n  App.tsx\n  components/\n  lib/",
      ResponseType: "tool_result",
      ToolName: "bash",
      ToolCallID: "tool-1",
      FullToolOutput: "",
    });
    const tool = page.getByRole("button", { name: "bash output", exact: true });
    await tool.focus();
    await page.keyboard.press("Enter");
    await expect(tool).toHaveAttribute("aria-expanded", "true");
    await expect(
      page.getByText("src/\n  App.tsx\n  components/\n  lib/", { exact: true }),
    ).toBeVisible();
    await capture(`chat-${colorScheme}`);

    await page
      .getByRole("button", { name: "View context", exact: true })
      .click();
    await expect(
      page.getByRole("dialog").getByLabel("Model", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("dialog").getByLabel("Reasoning effort"),
    ).toBeVisible();
    await capture(`settings-${colorScheme}`);
    await page.keyboard.press("Escape");

    // Operational states remain visible as plain text, with their actions intact.
    agents.a1.held = true;
    emit("a1", "message.queued", {
      id: "pending-1",
      text: "Review the changes",
      status: "pending",
      created_at: new Date().toISOString(),
    });
    await expect(page.getByText("Queue held · 1 pending")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Continue", exact: true }),
    ).toBeEnabled();
    await expect(
      page.getByRole("button", { name: "Cancel", exact: true }),
    ).toBeEnabled();
    agents.a1.held = false;
    agents.a1.state = "running";
    emit("a1", "turn.started", {
      id: "running-1",
      text: "Current turn",
      status: "running",
      created_at: new Date().toISOString(),
    });
    await expect(page.getByText("Ted is working…")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Stop", exact: true }),
    ).toBeEnabled();

    await page.setViewportSize({ width: 390, height: 844 });
    await fitsViewport();
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toBeInViewport();
    await capture(`mobile-chat-${colorScheme}`);
    await page
      .getByRole("button", { name: "Open sidebar", exact: true })
      .click();
    await expect(
      page.getByRole("dialog", { name: "Workspace", exact: true }),
    ).toBeVisible();
    await capture(`mobile-sidebar-${colorScheme}`);
    await page
      .getByRole("button", { name: "Settings for harness", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await expect(page.getByLabel("Server directory")).toHaveValue(
      "/srv/harness",
    );
    await fitsViewport();
    await capture(`mobile-settings-${colorScheme}`);
  });
}

test("mobile project options dismiss back to the same chat without leaving a blocking drawer", async ({
  page,
}) => {
  await workspace(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await expect(page).toHaveURL(/\/agents\/a1$/);
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await composer.fill("Keep this draft and chat");
  await page.getByRole("button", { name: "Open sidebar", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByLabel("Settings for harness", { exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "harness defaults" }),
  ).toBeVisible();
  await page.mouse.click(385, 10);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page).toHaveURL(/\/agents\/a1$/);
  await expect(composer).toHaveValue("Keep this draft and chat");
  await composer.click();
  await composer.fill("Still clickable");
  await page.getByRole("button", { name: "Open sidebar", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Workspace", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Close sidebar", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.getByRole("button", { name: "View context", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Agent settings", exact: true }),
  ).toBeVisible();
});

test.describe("mobile modal hand-offs", () => {
  test.use({
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
  });

  for (const dismiss of [
    "outside tap",
    "close button",
    "escape",
    "save",
    "browser back",
  ] as const) {
    test(`project options: ${dismiss} releases modal locks and preserves the chat`, async ({
      page,
    }) => {
      await workspace(page);
      await page.goto("/?dialog=new-agent");
      await page.getByRole("button", { name: "harness /srv/harness" }).tap();
      const composer = page.getByRole("textbox", {
        name: "Message",
        exact: true,
      });
      await composer.fill("Draft survives project options");
      // The actual transcript node must survive the overlay, not just its text.
      const transcript = await page
        .getByRole("region", { name: "Messages", exact: true })
        .elementHandle();
      for (let repeat = 0; repeat < 2; repeat++) {
        await page
          .getByRole("button", { name: "Open sidebar", exact: true })
          .tap();
        await page
          .getByRole("dialog")
          .getByLabel("Settings for harness", { exact: true })
          .tap();
        await expect(page).toHaveURL(
          /\/agents\/a1\?panel=project-settings&project=p$/,
        );
        await expect(
          page.getByRole("dialog", { name: "harness defaults" }),
        ).toBeVisible();
        await expect(page.locator('[data-slot="sheet-overlay"]')).toHaveCount(
          0,
        );
        if (dismiss === "outside tap") await page.touchscreen.tap(385, 10);
        if (dismiss === "close button")
          await page.getByRole("button", { name: "Close", exact: true }).tap();
        if (dismiss === "escape") await page.keyboard.press("Escape");
        if (dismiss === "save")
          await page
            .getByRole("button", { name: "Save defaults", exact: true })
            .tap();
        if (dismiss === "browser back") {
          await page.goBack();
          await expect(
            page.getByRole("dialog", { name: "Workspace", exact: true }),
          ).toBeVisible();
          await page
            .getByRole("button", { name: "Close sidebar", exact: true })
            .tap();
        }
        await expect(page.getByRole("dialog")).toHaveCount(0);
        await expect(
          page.locator(
            '[data-slot="sheet-overlay"], [data-slot="dialog-overlay"]',
          ),
        ).toHaveCount(0);
        await expect(page).toHaveURL(/\/agents\/a1$/);
        expect(await transcript!.evaluate((node) => node.isConnected)).toBe(
          true,
        );
        await expect(composer).toHaveValue("Draft survives project options");
        await composer.tap();
        await expect(composer).toBeFocused();
      }
    });
  }

  test("changing agents from the drawer cleans up the old modal", async ({
    page,
  }) => {
    await workspace(page);
    await page.goto("/?dialog=new-agent");
    await page.getByRole("button", { name: "harness /srv/harness" }).tap();
    await page.getByRole("button", { name: "Open sidebar", exact: true }).tap();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "New chat", exact: true })
      .tap();
    await page.getByRole("button", { name: "harness /srv/harness" }).tap();
    await expect(page).toHaveURL(/\/agents\/a2$/);
    await page.getByRole("button", { name: "Open sidebar", exact: true }).tap();
    await page.getByRole("dialog").locator('a[href="/agents/a1"]').tap();
    await expect(page).toHaveURL(/\/agents\/a1$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page.getByRole("textbox", { name: "Message", exact: true }).tap();
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toBeFocused();
    await page.getByRole("button", { name: "View context", exact: true }).tap();
    await expect(
      page.getByRole("dialog", { name: "Agent settings", exact: true }),
    ).toBeVisible();
  });
});

test("mobile composer navigation uses a bottom sheet", async ({ page }) => {
  await workspace(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  const menu = page.getByRole("button", { name: "Open sidebar", exact: true });
  const hint = page.getByText(
    "Enter to send · Shift + Enter for newline · /help",
  );
  await expect(menu).toBeVisible();
  await expect(hint).toBeHidden();
  await expect(
    page.locator("header").getByRole("button", { name: "Open sidebar" }),
  ).toHaveCount(0);
  const menuBox = (await menu.boundingBox())!;
  const sendBox = (await page
    .getByRole("button", { name: "Send message", exact: true })
    .boundingBox())!;
  const input = page.getByRole("group", { name: "Message input", exact: true });
  const inputBox = (await input.boundingBox())!;
  const location = page.getByRole("group", {
    name: "Project location",
    exact: true,
  });
  const locationBox = (await location.boundingBox())!;
  expect(menuBox.y).toBeGreaterThan(inputBox.y + inputBox.height);
  expect(menuBox.y).toBeGreaterThan(sendBox.y + sendBox.height);
  const contextBox = (await page
    .getByRole("group", { name: "Composer footer", exact: true })
    .getByRole("button", { name: "View context", exact: true })
    .boundingBox())!;
  expect(locationBox.x + locationBox.width).toBeLessThan(contextBox.x);
  expect(contextBox.x + contextBox.width).toBeLessThan(menuBox.x);
  expect(menuBox.x + menuBox.width).toBe(inputBox.x + inputBox.width);
  await expect(
    location.getByText("/srv/harness", { exact: true }),
  ).toBeHidden();
  await expect(
    input.getByRole("button", { name: "Open sidebar", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("Unsent draft");
  await menu.click();
  const sheet = page.getByRole("dialog", { name: "Workspace", exact: true });
  await expect(sheet).toHaveAttribute("data-side", "bottom");
  await expect
    .poll(async () => {
      const box = (await sheet.boundingBox())!;
      return Math.round(box.y + box.height);
    })
    .toBe(844);
  const box = (await sheet.boundingBox())!;
  expect(box.x).toBe(0);
  expect(box.width).toBe(390);
  expect(box.y).toBeGreaterThan(0);
  await page
    .getByRole("button", { name: "Close sidebar", exact: true })
    .click();
  await expect(sheet).toHaveCount(0);
  await expect(
    page.getByRole("textbox", { name: "Message", exact: true }),
  ).toHaveValue("Unsent draft");
  await page.setViewportSize({ width: 1440, height: 960 });
  await expect(menu).toBeHidden();
  await expect(hint).toHaveCount(0);
  await expect(page.locator("aside")).toBeVisible();
});

test("tool call expands in place as its output arrives", async ({ page }) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  const call = {
    ResponseType: "tool",
    ToolName: "bash",
    ToolCallID: "",
    Content: 'Ran shell command - "echo hello"',
    FullToolOutput: "",
  };
  emit("a1", "output", call);
  const toggle = page.getByRole("button", { name: "echo hello", exact: true });
  await toggle.click();
  await expect(page.getByText("Waiting for output…")).toBeVisible();
  emit("a1", "output", {
    ...call,
    ResponseType: "tool_result",
    ToolCallID: "call-1",
    Content: "capped",
    FullToolOutput: "hello from the tool",
  });
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    page.getByText("hello from the tool", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Waiting for output…")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "bash output", exact: true }),
  ).toHaveCount(0);
  await toggle.click();
  await expect(
    page.getByText("hello from the tool", { exact: true }),
  ).toBeHidden();
});

test("working status shimmers and respects reduced motion", async ({
  page,
}) => {
  const { agents, emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  agents.a1.state = "running";
  emit("a1", "output", { ResponseType: "agent", Content: "Working on it" });
  const working = page.getByText("Ted is working…", { exact: true });
  await expect(working).toBeVisible();
  const body = page
    .getByRole("article", { name: "Assistant message", exact: true })
    .locator(".markdown");
  await expect(working).toHaveCSS(
    "font-size",
    await body.evaluate((e) => getComputedStyle(e).fontSize),
  );
  await expect(working).toHaveCSS(
    "line-height",
    await body.evaluate((e) => getComputedStyle(e).lineHeight),
  );

  await expect(working).toHaveCSS("animation-name", "tw-shimmer");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(working).toHaveCSS("animation-name", "none");
  await expect(working).toBeVisible();
  agents.a1.state = "stopping";
  emit("a1", "output", { ResponseType: "agent", Content: "Stopping" });
  await expect(working).toHaveCount(0);
  await expect(
    page.getByText("Stopping current turn…", { exact: true }),
  ).toBeVisible();
});

test("pending messages enter the transcript only at turn start and preserve newer drafts", async ({
  page,
}) => {
  const { agents, emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  agents.a1.state = "running";
  const queued = {
    id: "pending-test",
    text: "My queued follow-up",
    status: "pending",
    created_at: new Date().toISOString(),
  };
  let accept!: () => void;
  const response = new Promise<void>((resolve) => {
    accept = resolve;
  });
  await page.route("**/v1/agents/a1/messages", async (route) => {
    emit("a1", "message.queued", queued);
    await response;
    await route.fulfill({ status: 202, json: queued });
  });
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await composer.fill(queued.text);
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(composer).toHaveValue("");
  await expect(
    page.locator("span[title]").filter({ hasText: queued.text }),
  ).toHaveCount(1);
  const messages = page.getByRole("region", { name: "Messages", exact: true });
  await expect(messages.getByText(queued.text, { exact: true })).toHaveCount(0);
  await composer.fill("My next draft");
  accept();
  await expect(
    page.getByRole("button", { name: "Send message", exact: true }),
  ).toBeEnabled();
  await expect(composer).toHaveValue("My next draft");
  emit("a1", "output", {
    ResponseType: "agent",
    Content: "Previous turn finished",
  });
  emit("a1", "turn.started", { ...queued, status: "running" });
  await expect(messages.getByText(queued.text, { exact: true })).toBeVisible();
  await expect(messages.getByText(queued.text, { exact: true })).toHaveCount(1);
  await expect(
    page.locator("span[title]").filter({ hasText: queued.text }),
  ).toHaveCount(0);
  await expect(page.getByText("Up next", { exact: false })).toHaveCount(0);
});

test("failed submission restores an untouched draft", async ({ page }) => {
  await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page.getByRole("button", { name: "harness /srv/harness" }).click();
  await page.route("**/v1/agents/a1/messages", (route) =>
    route.fulfill({ status: 500, json: { error: { message: "Send failed" } } }),
  );
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await composer.fill("Please keep this");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(composer).toHaveValue("Please keep this");
  await expect(
    page.getByRole("button", { name: "Send message", exact: true }),
  ).toBeEnabled();
});

for (const width of [1440, 390, 320]) {
  test(`composer keeps model and effort inside and context in the footer (${width}px)`, async ({
    page,
  }, testInfo) => {
    const { agents, emit } = await workspace(page);
    await page.setViewportSize({ width, height: 960 });
    await page.route("**/v1/models", (route) =>
      route.fulfill({
        json: [
          {
            id: "test/model",
            name: "Test model",
            provider: "test",
            efforts: ["low", "medium", "high"],
            default_effort: "medium",
            context_window: 100000,
          },
          {
            id: "test/other",
            name: "Another model with a very long display name",
            provider: "test",
            efforts: ["low", "high"],
            default_effort: "low",
            context_window: 100000,
          },
          {
            id: "test/no-effort",
            name: "No reasoning effort",
            provider: "test",
            efforts: [],
            default_effort: "",
            context_window: 100000,
          },
        ],
      }),
    );
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    const input = page.getByRole("group", {
      name: "Message input",
      exact: true,
    });
    const actions = input.getByRole("group", {
      name: "Message actions",
      exact: true,
    });
    const settings = input.getByRole("group", {
      name: "Chat settings",
      exact: true,
    });
    const model = settings.getByLabel("Model", { exact: true });
    const effort = settings.getByLabel("Reasoning effort", { exact: true });
    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    const context = footer.getByRole("button", {
      name: "View context",
      exact: true,
    });
    const composer = input.getByRole("textbox", {
      name: "Message",
      exact: true,
    });
    await expect
      .poll(async () => (await composer.boundingBox())!.height)
      .toBe(48);
    await expect(model).toHaveText("Test model");
    await expect(effort).toHaveText("medium");
    await expect(context).toHaveText(width < 768 ? "—" : "Context", {
      useInnerText: true,
    });
    const location = page.getByRole("group", {
      name: "Project location",
      exact: true,
    });
    const directory = location.getByText("/srv/harness", { exact: true });
    if (width < 768) await expect(directory).toBeHidden();
    else await expect(directory).toBeVisible();
    await expect(location.getByText("main", { exact: true })).toBeVisible();
    expect((await model.boundingBox())!.width).toBeLessThan(200);
    expect((await effort.boundingBox())!.width).toBeLessThan(130);
    const initialSendX = (await actions
      .getByRole("button", { name: "Send message", exact: true })
      .boundingBox())!.x;
    await expect(page.getByText("Enter to send", { exact: false })).toHaveCount(
      0,
    );
    await expect(
      page.locator("header").getByRole("button", { name: "Agent settings" }),
    ).toHaveCount(0);
    await context.click();
    await expect(
      page.getByRole("dialog").getByText("Context usage is not available yet."),
    ).toBeVisible();
    await page.keyboard.press("Escape");

    await composer.fill("Keep this draft while changing settings");
    await expect(settings.locator("select")).toHaveCount(0);
    for (const [name, trigger] of [
      ["model", model],
      ["effort", effort],
    ] as const) {
      await trigger.click();
      const popup = page.locator('[data-slot="select-content"][data-open]');
      await expect(popup).toBeVisible();
      await expect(popup).toHaveAttribute("data-side", "top");
      await expect(popup).toHaveAttribute("data-align-trigger", "false");
      await expect
        .poll(async () => {
          const triggerBox = (await trigger.boundingBox())!;
          const popupBox = (await popup.boundingBox())!;
          return popupBox.y + popupBox.height - triggerBox.y;
        })
        .toBeLessThanOrEqual(0);
      const popupBox = (await popup.boundingBox())!;
      expect(popupBox.x).toBeGreaterThanOrEqual(0);
      expect(popupBox.x + popupBox.width).toBeLessThanOrEqual(width);
      const path = testInfo.outputPath(`${name}-select-${width}.png`);
      await page.screenshot({ path });
      await testInfo.attach(`${name}-select-${width}`, {
        path,
        contentType: "image/png",
      });
      await page.keyboard.press("Escape");
      await expect(page.getByRole("listbox")).toHaveCount(0);
      await expect(trigger).toBeFocused();
      await expect(composer).toHaveValue(
        "Keep this draft while changing settings",
      );
    }
    // Inline changes save immediately, and an incompatible effort falls back to the model default.
    await chooseOption(
      page,
      model,
      "Another model with a very long display name",
    );
    await expect(model).toHaveText(
      "Another model with a very long display name",
    );
    await expect(effort).toHaveText("low");
    await expect(effort).toBeEnabled();
    expect(
      (await actions
        .getByRole("button", { name: "Send message", exact: true })
        .boundingBox())!.x,
    ).toBe(initialSendX);
    await chooseOption(page, effort, "high");
    await expect(effort).toHaveText("high");
    expect(agents.a1.settings).toEqual({ model: "test/other", effort: "high" });
    await expect(composer).toHaveValue(
      "Keep this draft while changing settings",
    );
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await chooseOption(page, model, "No reasoning effort");
    await expect(model).toHaveText("No reasoning effort");
    await expect(effort).toBeDisabled();
    await chooseOption(
      page,
      model,
      "Another model with a very long display name",
    );
    await expect(model).toHaveText(
      "Another model with a very long display name",
    );
    await expect(effort).toHaveText("low");

    agents.a1.context_usage = {
      model: "test/other",
      estimated_tokens: 25000,
      context_window: 100000,
      input_tokens: 24000,
      output_tokens: 1000,
      known: true,
      estimated: true,
    };
    agents.a1.held = true;
    emit("a1", "message.queued", {
      id: "pending-footer",
      text: "Queued work",
      status: "pending",
      created_at: new Date().toISOString(),
    });
    await expect(context).toHaveText(width < 768 ? "25%" : "Context 25%", {
      useInnerText: true,
    });
    await context.click();
    const dialog = page.getByRole("dialog", {
      name: "Agent settings",
      exact: true,
    });
    await expect(
      dialog.getByText("Context: 25,000 estimated / 100,000 tokens"),
    ).toBeVisible();
    await expect(
      dialog.getByText("Usage: 24,000 input / 1,000 output tokens"),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(
      actions.getByRole("button", { name: "Continue", exact: true }),
    ).toBeEnabled();
    const continued = page.waitForRequest(
      (r) => r.url().endsWith("/a1/continue") && r.method() === "POST",
    );
    await actions
      .getByRole("button", { name: "Continue", exact: true })
      .click();
    await continued;
    agents.a1.held = false;
    agents.a1.state = "running";
    emit("a1", "turn.started", {
      id: "running-footer",
      text: "Working",
      status: "running",
      created_at: new Date().toISOString(),
    });
    await expect(
      actions.getByRole("button", { name: "Stop", exact: true }),
    ).toBeEnabled();
    const stopped = page.waitForRequest(
      (r) => r.url().endsWith("/a1/stop") && r.method() === "POST",
    );
    await actions.getByRole("button", { name: "Stop", exact: true }).click();
    expect((await stopped).postDataJSON()).toEqual({
      turn_id: "running-footer",
    });
    await expect(composer).toHaveValue(
      "Keep this draft while changing settings",
    );

    // A long, scrollable draft must not overlap the bottom action row.
    await composer.fill("A line of text\n".repeat(30));
    await composer.press("End");
    await expect
      .poll(async () => (await composer.boundingBox())!.height)
      .toBe(208);
    expect(
      await composer.evaluate((e) => e.scrollHeight > e.clientHeight),
    ).toBe(true);
    const inputBox = (await input.boundingBox())!;
    const textBox = (await composer.boundingBox())!;
    const actionsBox = (await actions.boundingBox())!;
    const settingsBox = (await settings.boundingBox())!;
    expect(actionsBox.y).toBeGreaterThanOrEqual(textBox.y + textBox.height);
    expect(actionsBox.y + actionsBox.height).toBeLessThanOrEqual(
      inputBox.y + inputBox.height,
    );
    expect(settingsBox.y).toBeGreaterThanOrEqual(textBox.y + textBox.height);
    expect(settingsBox.y + settingsBox.height).toBeLessThanOrEqual(
      inputBox.y + inputBox.height,
    );
    expect(settingsBox.x).toBeGreaterThan(inputBox.x);
    expect(settingsBox.x + settingsBox.width).toBeLessThan(actionsBox.x);
    expect((await location.boundingBox())!.y).toBeGreaterThan(
      inputBox.y + inputBox.height,
    );
    for (const field of [model, effort]) {
      const box = (await field.boundingBox())!;
      expect(box.x + box.width).toBeLessThan(actionsBox.x);
    }
    if (width >= 768) {
      expect(
        Math.abs(
          settingsBox.y + settingsBox.height - actionsBox.y - actionsBox.height,
        ),
      ).toBeLessThan(1);
    }
    for (const name of ["Send message", "Stop"]) {
      const button = actions.getByRole("button", { name, exact: true });
      await expect(button).toBeInViewport();
      const box = (await button.boundingBox())!;
      expect(box.x).toBeGreaterThan(inputBox.x + inputBox.width / 2);
      expect(box.x + box.width).toBeLessThan(inputBox.x + inputBox.width);
    }
    await expect(
      actions.getByRole("button", { name: "Open sidebar", exact: true }),
    ).toHaveCount(0);
    await expect(
      input.getByRole("button", { name: "View context", exact: true }),
    ).toHaveCount(0);
    const contextBox = (await context.boundingBox())!;
    expect(contextBox.y).toBeGreaterThan(inputBox.y + inputBox.height);
    if (width < 768) {
      const menu = footer.getByRole("button", {
        name: "Open sidebar",
        exact: true,
      });
      await expect(menu).toBeInViewport();
      const menuBox = (await menu.boundingBox())!;
      const locationBox = (await location.boundingBox())!;
      expect(menuBox.y).toBeGreaterThan(inputBox.y + inputBox.height);
      expect(locationBox.x + locationBox.width).toBeLessThan(contextBox.x);
      expect(contextBox.x + contextBox.width).toBeLessThan(menuBox.x);
      expect(menuBox.x + menuBox.width).toBe(inputBox.x + inputBox.width);
    }
    await expect(model).toBeInViewport();
    await expect(effort).toBeInViewport();
    await expect(context).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await composer.fill("Keep this draft while changing settings");
    await composer.fill("");
    await expect
      .poll(async () => (await composer.boundingBox())!.height)
      .toBe(48);
    await composer.fill("Keep this draft while changing settings");
    const path = testInfo.outputPath(`composer-${width}.png`);
    await page.screenshot({ path });
    await testInfo.attach(`composer-${width}`, {
      path,
      contentType: "image/png",
    });
  });
}

test("failed inline settings changes keep the previous settings and draft", async ({
  page,
}) => {
  await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  await page.route("**/v1/agents/a1/settings", (route) =>
    route.fulfill({
      status: 500,
      json: { error: { message: "Settings failed" } },
    }),
  );
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  const effort = page
    .getByRole("group", { name: "Chat settings", exact: true })
    .getByLabel("Reasoning effort");
  await composer.fill("My draft");
  await chooseOption(page, effort, "high");
  await expect(page.getByRole("alert")).toContainText("Settings failed");
  await expect(effort).toHaveText("medium");
  await expect(effort).toBeEnabled();
  await expect(composer).toHaveValue("My draft");
});

test("user bubbles wrap long messages and contain scrollable Markdown on mobile and desktop", async ({
  page,
}) => {
  await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const text = `Please review **this change**.\n\n${"unbroken-text".repeat(40)}\n\n\`\`\`ts\nconst value = "${"long code ".repeat(40)}"\n\`\`\``;
  await page.getByRole("textbox", { name: "Message", exact: true }).fill(text);
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  const bubble = page.getByRole("article", {
    name: "Your message",
    exact: true,
  });
  await expect(bubble.locator("strong")).toHaveText("this change");
  await expect(bubble.locator("pre")).toContainText("const value");
  for (const width of [320, 1440]) {
    await page.setViewportSize({ width, height: 960 });
    const messages = page.getByRole("region", {
      name: "Messages",
      exact: true,
    });
    const box = (await bubble.boundingBox())!;
    const viewport = (await messages.boundingBox())!;
    expect(box.x).toBeGreaterThan(viewport.x);
    expect(box.x + box.width).toBeLessThan(viewport.x + viewport.width);
    expect(await messages.evaluate((e) => e.scrollWidth <= e.clientWidth)).toBe(
      true,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  }
});

test("shadcn effort selection supports the empty default value and keyboard interaction without sending the draft", async ({
  page,
}) => {
  const { agents } = await workspace(page);
  await page.route("**/v1/models", (route) =>
    route.fulfill({
      json: [
        {
          id: "test/model",
          name: "Test model",
          provider: "test",
          efforts: ["", "medium", "high"],
          default_effort: "",
          context_window: 100000,
        },
      ],
    }),
  );
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("Do not send when selecting an effort");
  const effort = page
    .getByRole("group", { name: "Chat settings", exact: true })
    .getByRole("combobox", { name: "Reasoning effort", exact: true });
  await effort.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("listbox")).toBeVisible();
  await page.keyboard.press("Home");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(effort).toHaveText("Default");
  expect(agents.a1.settings.effort).toBe("");
  await expect(draft).toHaveValue("Do not send when selecting an effort");
  await expect(page.getByRole("article")).toHaveCount(0);
  await page.reload();
  await expect(effort).toHaveText("Default");
});

test.describe("touch select menus", () => {
  test.use({ hasTouch: true, viewport: { width: 390, height: 844 } });
  test("long model catalog opens above the composer, scrolls, and releases focus after selection", async ({
    page,
  }) => {
    const { agents } = await workspace(page);
    await page.route("**/v1/models", (route) =>
      route.fulfill({
        json: [
          {
            id: "test/model",
            name: "Test model",
            provider: "test",
            efforts: ["medium"],
            default_effort: "medium",
            context_window: 100000,
          },
          ...Array.from({ length: 50 }, (_, i) => ({
            id: `test/model-${i}`,
            name: `Model ${i}`,
            provider: "test",
            efforts: ["low"],
            default_effort: "low",
            context_window: 100000,
          })),
        ],
      }),
    );
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .tap();
    const settings = page.getByRole("group", {
      name: "Chat settings",
      exact: true,
    });
    const model = settings.getByRole("combobox", {
      name: "Model",
      exact: true,
    });
    await model.tap();
    const popup = page.locator('[data-slot="select-content"][data-open]');
    await expect(popup).toHaveAttribute("data-side", "top");
    const last = page.getByRole("option", { name: "Model 49", exact: true });
    await last.scrollIntoViewIfNeeded();
    await expect(last).toBeInViewport();
    await last.tap();
    await expect(model).toHaveText("Model 49");
    await expect(
      settings.getByRole("combobox", { name: "Reasoning effort", exact: true }),
    ).toHaveText("low");
    expect(agents.a1.settings.model).toBe("test/model-49");
    await expect(page.getByRole("listbox")).toHaveCount(0);
    const draft = page.getByRole("textbox", { name: "Message", exact: true });
    await draft.tap();
    await expect(draft).toBeFocused();
    await draft.fill("Still usable after choosing a model");
    await page.getByRole("button", { name: "Send message", exact: true }).tap();
    await expect(
      page.getByRole("article", { name: "Your message", exact: true }),
    ).toContainText("Still usable after choosing a model");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
});

for (const width of [1440, 390]) {
  test(`sidebar chat rows show live branches and settle without navigating (${width}px)`, async ({
    page,
  }, testInfo) => {
    const { agents, emit } = await workspace(page);
    await page.setViewportSize({ width, height: 960 });
    for (let i = 0; i < 2; i++) {
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      await expect(page).toHaveURL(new RegExp(`/agents/a${i + 1}$`));
    }
    const project = {
      id: "q",
      name: "other-project",
      root: "/srv/other-project",
      defaults: { model: "test/model", effort: "medium" },
    };
    let branch: string | undefined = "feature/sidebar";
    // The list API does not contain live branch data. Each project must be fetched,
    // including the project belonging to the chat that isn't currently selected.
    await page.route("**/v1/projects", (route) =>
      route.fulfill({
        json: [
          { ...project, id: "p", name: "harness", root: "/srv/harness" },
          project,
        ],
      }),
    );
    await page.route("**/v1/projects/q", (route) =>
      route.fulfill({ json: { ...project, git_branch: branch } }),
    );
    agents.a1.title = "First chat";
    agents.a2.title = "Second chat";
    agents.a2.project_id = "q";
    emit("a1", "output", { ResponseType: "usage", Content: "" });
    emit("a2", "output", { ResponseType: "usage", Content: "" });
    await page.goto("/agents/a1");
    const draft = page.getByRole("textbox", { name: "Message", exact: true });
    await draft.fill("Keep my current chat and draft");
    if (width < 768)
      await page
        .getByRole("button", { name: "Open sidebar", exact: true })
        .click();
    const sidebar =
      width < 768
        ? page.getByRole("dialog", { name: "Workspace", exact: true })
        : page.locator("aside");
    const first = sidebar.locator('[data-agent-id="a1"]');
    const second = sidebar.locator('[data-agent-id="a2"]');
    await expect(first).toContainText("main");
    await expect(second).toContainText("feature/sidebar");
    const titleBox = (await second
      .getByText("Second chat", { exact: true })
      .boundingBox())!;
    const branchBox = (await second
      .getByText("feature/sidebar", { exact: true })
      .boundingBox())!;
    expect(branchBox.y).toBeGreaterThanOrEqual(titleBox.y + titleBox.height);
    const settle = second.getByRole("button", {
      name: "Settle chat: Second chat",
      exact: true,
    });
    await second.getByRole("link").hover();
    await expect(settle).toHaveCSS("opacity", "1");
    await expect(settle).toHaveCSS("width", "32px");
    const linkBox = (await second.getByRole("link").boundingBox())!;
    const buttonBox = (await settle.boundingBox())!;
    expect(buttonBox.x).toBeGreaterThanOrEqual(linkBox.x + linkBox.width);
    await expect(settle).toBeVisible();
    await expect(second.locator("a button")).toHaveCount(0);
    branch = "feature/branch-changed-outside-the-selected-chat";
    await expect(second).toContainText(branch, { timeout: 7000 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const screenshot = testInfo.outputPath(`sidebar-chats-${width}.png`);
    await page.screenshot({ path: screenshot });
    await testInfo.attach(`sidebar-chats-${width}`, {
      path: screenshot,
      contentType: "image/png",
    });

    const originalURL = page.url();
    let finish!: () => void;
    let calls = 0;
    const pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    await page.route("**/v1/agents/a2", async (route) => {
      if (route.request().method() === "PATCH") {
        calls++;
        await pending;
      }
      await route.fallback();
    });
    await settle.click();
    await expect(settle).toBeDisabled();
    await sidebar
      .getByRole("button", { name: "Settled chats", exact: true })
      .focus();
    await sidebar
      .getByRole("button", { name: "Settled chats", exact: true })
      .hover();
    await expect(settle).toHaveCSS("opacity", "0");
    await second.getByRole("link").hover();
    await expect(settle).toHaveCSS("opacity", "0.5");
    await settle.evaluate((button: HTMLButtonElement) => button.click());
    expect(calls).toBe(1);
    finish();
    await expect(second).toBeHidden();
    expect(agents.a2.settled).toBe(true);
    await expect(page).toHaveURL(originalURL);
    await sidebar
      .getByRole("button", { name: "Settled chats", exact: true })
      .click();
    await expect(second).toContainText(branch);
    await second.getByRole("link").hover();
    await second
      .getByRole("button", { name: "Restore chat: Second chat", exact: true })
      .click();
    await expect(
      second.getByRole("button", {
        name: "Settle chat: Second chat",
        exact: true,
      }),
    ).toBeEnabled();
    expect(agents.a2.settled).toBe(false);
    await expect(page).toHaveURL(originalURL);

    // Failed settlement stays in place and is retryable.
    await page.route("**/v1/agents/a1", async (route) => {
      if (route.request().method() === "PATCH") {
        await route.fulfill({
          status: 503,
          json: { error: { message: "Could not settle chat" } },
        });
      } else await route.fallback();
    });
    await first.getByRole("link").hover();
    await first
      .getByRole("button", { name: "Settle chat: First chat", exact: true })
      .click();
    await expect(first.getByRole("alert")).toContainText(
      "Could not settle chat",
    );
    await expect(
      first.getByRole("button", {
        name: "Settle chat: First chat",
        exact: true,
      }),
    ).toBeEnabled();
    expect(agents.a1.settled).toBe(false);
    await page.unroute("**/v1/agents/a1");
    await first.getByRole("link").hover();
    await first
      .getByRole("button", { name: "Settle chat: First chat", exact: true })
      .click();
    await first.getByRole("link").hover();
    await expect(
      first.getByRole("button", {
        name: "Restore chat: First chat",
        exact: true,
      }),
    ).toBeVisible();
    expect(agents.a1.settled).toBe(true);
    await expect(page).toHaveURL(originalURL);
    if (width < 768)
      await sidebar
        .getByRole("button", { name: "Close sidebar", exact: true })
        .click();
    await expect(draft).toHaveValue("Keep my current chat and draft");
    await expect(
      page.getByRole("button", { name: "Restore agent", exact: true }),
    ).toBeVisible();
  });
}

test("consecutive tools use 6px spacing while message boundaries keep 24px", async ({
  page,
}, testInfo) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const user = (id: string, text: string) =>
    emit("a1", "turn.started", {
      id,
      text,
      status: "running",
      created_at: new Date().toISOString(),
    });
  const output = (kind: string, text: string, id = "") =>
    emit("a1", "output", {
      ResponseType: kind,
      Content: text,
      ToolCallID: id,
      ToolName: kind === "agent" ? "" : "bash",
      FullToolOutput: "",
    });
  user("first", "Check the repository");
  output("agent", "I’ll check the files and Git status.");
  output("tool", "ls src", "files");
  output("tool", "git status --short", "git");
  output("tool_result", "Additional tool output", "unmatched");
  output("agent", "The repository is ready.");
  user("second", "Thanks");
  const content = page.locator('[data-slot="message-scroller-content"]');
  const items = content.locator('[data-slot="message-scroller-item"]');
  await expect(items).toHaveCount(7);
  const gaps = () =>
    items.evaluateAll((elements) =>
      elements.slice(1).map((element, index) => {
        const previous = elements[index].getBoundingClientRect();
        return Math.round(
          element.getBoundingClientRect().top - previous.bottom,
        );
      }),
    );
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 960 });
    await expect.poll(gaps).toEqual([24, 24, 6, 6, 24, 24]);
    const path = testInfo.outputPath(`tool-spacing-${width}.png`);
    await page.screenshot({ path });
    await testInfo.attach(`tool-spacing-${width}`, {
      path,
      contentType: "image/png",
    });
  }
  // Expansion and correlated output update only the tool card's height, not its surrounding gaps.
  await page.getByRole("button", { name: "ls src", exact: true }).click();
  await expect(
    page.getByText("Waiting for output…", { exact: true }),
  ).toBeVisible();
  output("tool_result", "App.tsx\ncomponents/\nlib/", "files");
  await expect(
    page.getByText("App.tsx\ncomponents/\nlib/", { exact: true }),
  ).toBeVisible();
  await expect(items).toHaveCount(7);
  await expect.poll(gaps).toEqual([24, 24, 6, 6, 24, 24]);
});

for (const [width, height, colorScheme] of [
  [1440, 960, "light"],
  [390, 844, "dark"],
] as const) {
  test.describe(`chat image lightbox (${width}px ${colorScheme})`, () => {
    test.use({
      viewport: { width, height },
      hasTouch: width < 768,
      colorScheme,
    });
    test("previews images in the window and closes without losing chat state", async ({
      page,
    }, testInfo) => {
      const { emit } = await workspace(page);
      const errors: string[] = [];
      page.on("pageerror", (error) => errors.push(error.message));
      const referrers: (string | undefined)[] = [];
      await page.route("**/preview-image.png", async (route) => {
        referrers.push(route.request().headers().referer);
        await route.fulfill({
          path: "../docs/screenshots/web-tool-spacing.png",
          contentType: "image/png",
        });
      });
      await page.emulateMedia({ reducedMotion: "reduce" });
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      emit("a1", "output", {
        ResponseType: "agent",
        Content:
          "Here’s the chat preview:\n\n[![Desktop preview](/preview-image.png)](https://example.com/original)\n\n[Documentation](https://example.com/docs)",
      });
      const trigger = page.getByRole("button", {
        name: "Open image: Desktop preview",
        exact: true,
      });
      const thumbnail = trigger.getByRole("img", {
        name: "Desktop preview",
        exact: true,
      });
      await expect
        .poll(() => thumbnail.evaluate((e: HTMLImageElement) => e.naturalWidth))
        .toBeGreaterThan(0);
      await expect(thumbnail).not.toHaveCSS("box-shadow", "none");
      await expect(thumbnail).toHaveCSS("border-top-width", "1px");
      await expect(thumbnail).toHaveCSS("border-top-style", "solid");
      const thumbnailBox = (await thumbnail.boundingBox())!;
      const triggerBox = (await trigger.boundingBox())!;
      // Padding protects the shadow from the scroller item's paint clipping,
      // including when the image consumes all available width on mobile.
      expect(thumbnailBox.x - triggerBox.x).toBeGreaterThanOrEqual(16);
      expect(
        triggerBox.x + triggerBox.width - thumbnailBox.x - thumbnailBox.width,
      ).toBeGreaterThanOrEqual(16);
      expect(thumbnailBox.y - triggerBox.y).toBeGreaterThanOrEqual(16);
      expect(
        triggerBox.y + triggerBox.height - thumbnailBox.y - thumbnailBox.height,
      ).toBeGreaterThanOrEqual(16);
      const itemBox = await thumbnail.evaluate((e) => {
        const box = e
          .closest('[data-slot="message-scroller-item"]')!
          .getBoundingClientRect();
        return { left: box.left, right: box.right };
      });
      expect(thumbnailBox.x - itemBox.left).toBeGreaterThanOrEqual(16);
      expect(
        itemBox.right - thumbnailBox.x - thumbnailBox.width,
      ).toBeGreaterThanOrEqual(16);
      await expect(page.locator("a button")).toHaveCount(0);
      await expect(
        page.getByRole("link", { name: "Documentation", exact: true }),
      ).toHaveAttribute("href", "https://example.com/docs");
      const draft = page.getByRole("textbox", { name: "Message", exact: true });
      await draft.fill("Keep this draft while viewing the image");
      const url = page.url();
      const capture = async (name: string) => {
        const path = testInfo.outputPath(`${name}-${width}.png`);
        await page.screenshot({ path });
        await testInfo.attach(name, { path, contentType: "image/png" });
      };
      await capture("inline-image");
      if (width < 768) await trigger.tap();
      else {
        await trigger.focus();
        await page.keyboard.press("Enter");
      }
      const dialog = page.getByRole("dialog", {
        name: "Image preview",
        exact: true,
      });
      const image = dialog.getByRole("img", {
        name: "Desktop preview",
        exact: true,
      });
      await expect(image).toBeVisible();
      const naturalRatio = await image.evaluate(
        (e: HTMLImageElement) => e.naturalWidth / e.naturalHeight,
      );
      await expect
        .poll(async () => (await image.boundingBox())!.width)
        .toBeCloseTo(Math.min(width - 32, (height - 96) * naturalRatio), 0);
      const box = (await image.boundingBox())!;
      expect(
        Math.abs(
          box.width - Math.min(width - 32, (height - 96) * naturalRatio),
        ),
      ).toBeLessThan(2);
      expect(Math.abs(box.width / box.height - naturalRatio)).toBeLessThan(
        0.01,
      );
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.y).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(width);
      expect(box.y + box.height).toBeLessThanOrEqual(height);
      await expect(page.locator('[data-slot="dialog-overlay"]')).toHaveClass(
        /bg-black\/80/,
      );
      await expect(
        dialog.getByRole("button", {
          name: "Close image preview",
          exact: true,
        }),
      ).toBeInViewport();
      await image.click();
      await expect(dialog).toBeVisible();
      await capture("image-lightbox");
      // Streaming events should not remount the Markdown image and close its dialog.
      const originalDialog = await dialog.elementHandle();
      emit("a1", "output", {
        ResponseType: "agent",
        Content: "More output while the image is open",
      });
      await expect(
        page
          .locator("article")
          .filter({ hasText: "More output while the image is open" }),
      ).toHaveCount(1);
      expect(await originalDialog!.evaluate((e) => e.isConnected)).toBe(true);
      await expect(dialog).toBeVisible();
      await expect(page).toHaveURL(url);
      expect(page.context().pages()).toHaveLength(1);
      await dialog
        .getByRole("button", { name: "Close image preview", exact: true })
        .click();
      for (const close of ["escape", "backdrop", "letterbox"] as const) {
        await expect(dialog).toHaveCount(0);
        await expect(trigger).toBeFocused();
        await expect(draft).toHaveValue(
          "Keep this draft while viewing the image",
        );
        if (width < 768) await trigger.tap();
        else await trigger.click();
        await expect(dialog).toBeVisible();
        if (close === "escape") await page.keyboard.press("Escape");
        else if (close === "backdrop") {
          const backdrop = page.locator('[data-slot="dialog-overlay"]');
          if (width < 768) await backdrop.tap({ position: { x: 4, y: 4 } });
          else await backdrop.click({ position: { x: 4, y: 4 } });
        } else {
          await expect
            .poll(async () => Math.round((await dialog.boundingBox())!.height))
            .toBe(height - 32);
          const bounds = (await dialog.boundingBox())!;
          await dialog.click({
            position: { x: bounds.width / 2, y: bounds.height - 4 },
          });
        }
      }
      await expect(dialog).toHaveCount(0);
      await expect(trigger).toBeFocused();
      await expect(draft).toHaveValue(
        "Keep this draft while viewing the image",
      );
      expect(referrers.length).toBeGreaterThan(0);
      expect(referrers.every((value) => value === undefined)).toBe(true);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      // Browser navigation unmounts the viewer cleanly, even while it is open.
      await trigger.click();
      await expect(dialog).toBeVisible();
      await page.goBack();
      await expect(
        page.getByRole("dialog", { name: "New chat", exact: true }),
      ).toBeVisible();
      await expect(page.getByRole("dialog")).toHaveCount(1);
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await page
        .getByRole("textbox", { name: "Message", exact: true })
        .fill("Input still works");
      expect(errors).toEqual([]);
    });
  });
}

test("portrait and failed images remain usable in user-message previews", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/portrait.svg", (route) =>
    route.fulfill({
      contentType: "image/svg+xml",
      body: '<svg xmlns="http://www.w3.org/2000/svg" width="300" height="900"><rect width="300" height="900" fill="#555"/></svg>',
    }),
  );
  await page.route("**/broken-image.png", (route) =>
    route.fulfill({ status: 404, body: "Not found" }),
  );
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  emit("a1", "turn.started", {
    id: "image-message",
    text: "![Portrait](/portrait.svg)\n\n![Broken](/broken-image.png)",
    status: "running",
    created_at: new Date().toISOString(),
  });
  const message = page.getByRole("article", {
    name: "Your message",
    exact: true,
  });
  const portrait = message.getByRole("button", {
    name: "Open image: Portrait",
    exact: true,
  });
  await expect
    .poll(() =>
      portrait
        .locator("img")
        .evaluate((e: HTMLImageElement) => e.naturalHeight),
    )
    .toBe(900);
  await portrait.click();
  const dialog = page.getByRole("dialog", {
    name: "Image preview",
    exact: true,
  });
  const image = dialog.getByRole("img", { name: "Portrait", exact: true });
  await expect(image).toBeVisible();
  await expect
    .poll(async () => Math.round((await image.boundingBox())!.height))
    .toBe(748);
  const box = (await image.boundingBox())!;
  expect(Math.abs(box.width / box.height - 1 / 3)).toBeLessThan(0.01);
  await page.keyboard.press("Escape");
  await message
    .getByRole("button", { name: "Open image: Broken", exact: true })
    .click();
  await expect(dialog.getByRole("alert")).toHaveText(
    "This image could not be loaded.",
  );
  await dialog
    .getByRole("button", { name: "Close image preview", exact: true })
    .click();
  await expect(dialog).toHaveCount(0);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
});

test("sidebar chats use full width and slide to reveal settle actions on hover or keyboard focus", async ({
  page,
}, testInfo) => {
  const { agents } = await workspace(page);
  for (let i = 0; i < 2; i++) {
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`/agents/a${i + 1}$`));
  }
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("Keep my draft");
  await draft.hover();
  const sidebar = page.locator("aside");
  const first = sidebar.locator('[data-agent-id="a1"]');
  const second = sidebar.locator('[data-agent-id="a2"]');
  const firstAction = first.getByRole("button", { name: /^Settle chat:/ });
  const secondAction = second.getByRole("button", { name: /^Settle chat:/ });
  await expect(firstAction).toHaveCSS("opacity", "0");
  await expect(secondAction).toHaveCSS("opacity", "0");
  await expect(firstAction).toHaveCSS("width", "0px");
  await expect(secondAction).toHaveCSS("width", "0px");
  const before = (await first.getByRole("link").boundingBox())!;
  expect(before.width).toBe((await first.boundingBox())!.width);
  const fullWidthPath = testInfo.outputPath("sidebar-full-width.png");
  await page.screenshot({ path: fullWidthPath });
  await testInfo.attach("sidebar-full-width", {
    path: fullWidthPath,
    contentType: "image/png",
  });
  await first.getByRole("link").hover();
  await expect(firstAction).toHaveCSS("opacity", "1");
  await expect(secondAction).toHaveCSS("opacity", "0");
  await expect(firstAction).toHaveCSS("width", "32px");
  await expect
    .poll(async () => (await first.getByRole("link").boundingBox())!.width)
    .toBeCloseTo(before.width - 36, 0);
  const after = (await first.getByRole("link").boundingBox())!;
  expect(after.y).toBe(before.y);
  expect(after.height).toBe(before.height);
  const actionBox = (await firstAction.boundingBox())!;
  expect(actionBox.x + actionBox.width).toBe(before.x + before.width);
  // Moving from the link onto its action must not hide the action.
  await firstAction.hover();
  await expect(firstAction).toHaveCSS("opacity", "1");
  const path = testInfo.outputPath("sidebar-hover-actions.png");
  await page.screenshot({ path });
  await testInfo.attach("sidebar-hover-actions", {
    path,
    contentType: "image/png",
  });
  await draft.hover();
  await expect(firstAction).toHaveCSS("opacity", "0");
  await expect(firstAction).toHaveCSS("width", "0px");
  await expect
    .poll(async () => (await first.getByRole("link").boundingBox())!.width)
    .toBeCloseTo(before.width, 0);
  await first.getByRole("link").focus();
  await expect(firstAction).toHaveCSS("opacity", "1");
  await page.keyboard.press("Tab");
  await expect(firstAction).toBeFocused();
  await expect(firstAction).toHaveCSS("opacity", "1");
  await page.keyboard.press("Enter");
  await expect(first).toBeHidden();
  expect(agents.a1.settled).toBe(true);
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await sidebar
    .getByRole("button", { name: "Settled chats", exact: true })
    .click();
  await draft.focus();
  await draft.hover();
  const restore = first.getByRole("button", { name: /^Restore chat:/ });
  await expect(restore).toHaveCSS("opacity", "0");
  await first.getByRole("link").hover();
  await expect(restore).toHaveCSS("opacity", "1");
  await restore.click();
  await expect(firstAction).toBeEnabled();
  expect(agents.a1.settled).toBe(false);
  await expect(draft).toHaveValue("Keep my draft");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await draft.focus();
  await draft.hover();
  await expect(firstAction).toHaveCSS("width", "0px");
  expect(
    await firstAction.evaluate((e) =>
      parseFloat(getComputedStyle(e).transitionDuration),
    ),
  ).toBeLessThan(0.001);
  await first.getByRole("link").focus();
  await expect(firstAction).toHaveCSS("width", "32px");
});

for (const width of [390, 1440]) {
  test.describe(`touch sidebar actions (${width}px)`, () => {
    test.use({ hasTouch: true, viewport: { width, height: 960 } });
    test("settle and restore remain visible without hover", async ({
      page,
    }, testInfo) => {
      const { agents } = await workspace(page);
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .tap();
      await expect(page).toHaveURL(/\/agents\/a1$/);
      expect(
        await page.evaluate(
          () => matchMedia("(hover: hover) and (pointer: fine)").matches,
        ),
      ).toBe(false);
      if (width < 768)
        await page
          .getByRole("button", { name: "Open sidebar", exact: true })
          .tap();
      const sidebar =
        width < 768
          ? page.getByRole("dialog", { name: "Workspace", exact: true })
          : page.locator("aside");
      const row = sidebar.locator('[data-agent-id="a1"]');
      const settle = row.getByRole("button", { name: /^Settle chat:/ });
      await expect(settle).toHaveCSS("opacity", "1");
      await expect(settle).toHaveCSS("width", "32px");
      const rowBox = (await row.boundingBox())!;
      const linkBox = (await row.getByRole("link").boundingBox())!;
      expect(rowBox.width - linkBox.width).toBe(36);
      const path = testInfo.outputPath(`sidebar-touch-actions-${width}.png`);
      await page.screenshot({ path });
      await testInfo.attach("sidebar-touch-actions", {
        path,
        contentType: "image/png",
      });
      await settle.tap();
      await expect(row).toBeHidden();
      expect(agents.a1.settled).toBe(true);
      await sidebar
        .getByRole("button", { name: "Settled chats", exact: true })
        .tap();
      const restore = row.getByRole("button", { name: /^Restore chat:/ });
      await expect(restore).toHaveCSS("opacity", "1");
      await restore.tap();
      await expect(settle).toBeEnabled();
      await expect(settle).toHaveCSS("opacity", "1");
      expect(agents.a1.settled).toBe(false);
    });
  });
}

for (const width of [1440, 390]) {
  test.describe(`sidebar create buttons (${width}px)`, () => {
    test.use({ viewport: { width, height: 960 }, hasTouch: width < 768 });
    test("separates the primary chat action from icon-only project creation", async ({
      page,
    }, testInfo) => {
      await workspace(page);
      await page.goto("/");
      await expect(
        page.getByRole("link", { name: "harness /srv/harness", exact: true }),
      ).toBeVisible();
      const openSidebar = async () => {
        if (width < 768)
          await page
            .getByRole("button", { name: "Open sidebar", exact: true })
            .tap();
      };
      await openSidebar();
      const sidebar =
        width < 768
          ? page.getByRole("dialog", { name: "Workspace", exact: true })
          : page.locator("aside");
      const group = sidebar.getByRole("group", {
        name: "Create chat or project",
        exact: true,
      });
      const chat = group.getByRole("button", { name: "New chat", exact: true });
      const project = group.getByRole("button", {
        name: "New project",
        exact: true,
      });
      await expect(group.getByRole("button")).toHaveCount(2);
      await expect(chat).toHaveText("New chat");
      await expect(project).toHaveText("");
      await expect(project).toHaveAttribute("title", "New project");
      await expect(
        sidebar
          .getByRole("navigation", { name: "Agents", exact: true })
          .getByRole("button", { name: "New project", exact: true }),
      ).toHaveCount(0);
      const chatBox = (await chat.boundingBox())!;
      const projectBox = (await project.boundingBox())!;
      expect(projectBox.width).toBe(projectBox.height);
      expect(chatBox.width).toBeGreaterThan(projectBox.width);
      expect(chatBox.height).toBe(projectBox.height);
      expect(chatBox.y).toBe(projectBox.y);
      expect(projectBox.x - chatBox.x - chatBox.width).toBe(8);
      await expect(project).toHaveCSS("border-left-width", "1px");
      for (const button of [chat, project]) {
        expect(
          await button.evaluate((element) => {
            const style = getComputedStyle(element);
            return [
              style.borderTopLeftRadius,
              style.borderTopRightRadius,
              style.borderBottomLeftRadius,
              style.borderBottomRightRadius,
            ].every((radius) => parseFloat(radius) > 0);
          }),
        ).toBe(true);
      }
      const path = testInfo.outputPath(`sidebar-create-${width}.png`);
      await page.screenshot({ path });
      await testInfo.attach("sidebar-create", {
        path,
        contentType: "image/png",
      });
      if (width < 768) await project.tap();
      else {
        await chat.focus();
        await page.keyboard.press("Tab");
        await expect(project).toBeFocused();
        await page.keyboard.press("Enter");
      }
      const form = page.getByRole("dialog", {
        name: "Create a project",
        exact: true,
      });
      await expect(form).toBeVisible();
      await expect(page.getByRole("dialog")).toHaveCount(1);
      await expect(form.getByLabel("Server directory")).toBeVisible();
      await form.getByRole("button", { name: "Close", exact: true }).click();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await openSidebar();
      if (width < 768) await chat.tap();
      else await chat.click();
      await expect(
        page.getByRole("dialog", { name: "New chat", exact: true }),
      ).toBeVisible();
      await expect(page.getByRole("dialog")).toHaveCount(1);
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      await expect(page).toHaveURL(/\/agents\/a1$/);
      await expect(
        page.getByRole("textbox", { name: "Message", exact: true }),
      ).toBeVisible();
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    });
  });
}

test("connection failures remain actionable without a persistent sidebar connection footer", async ({
  page,
}) => {
  await workspace(page);
  let available = false;
  await page.route("**/v1/projects", async (route) => {
    if (available) await route.fallback();
    else
      await route.fulfill({
        status: 503,
        json: { error: { message: "Server unavailable" } },
      });
  });
  await page.goto("/");
  await expect(
    page.getByText("Could not connect to the server.", { exact: true }),
  ).toBeVisible();
  await expect(page.locator("aside").getByRole("status")).toHaveCount(0);
  available = true;
  await page.getByRole("button", { name: "Reconnect", exact: true }).click();
  await expect(
    page.getByRole("link", { name: "harness /srv/harness", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Reconnect", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByText("Connected to server", { exact: true }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Open sidebar", exact: true }).click();
  const sidebar = page.getByRole("dialog", { name: "Workspace", exact: true });
  await expect(
    sidebar.getByRole("navigation", { name: "Agents", exact: true }),
  ).toBeVisible();
  await expect(sidebar.getByRole("status")).toHaveCount(0);
  await expect(
    sidebar.getByText(/Connected to server|Connecting…|Disconnected/),
  ).toHaveCount(0);
});

test.describe("mobile composer footer", () => {
  test.use({ hasTouch: true, viewport: { width: 320, height: 740 } });
  test("keeps navigation reachable with long project details or no project", async ({
    page,
  }) => {
    const { agents, emit } = await workspace(page);
    const root = `/srv/${"a-long-project-directory/".repeat(6)}harness`;
    const branch = `feature/${"a-long-branch-name-".repeat(6)}`;
    const project = {
      id: "p",
      name: "harness",
      root,
      git_branch: branch,
      defaults: { model: "test/model", effort: "medium" },
    };
    await page.route("**/v1/projects", (route) =>
      route.fulfill({ json: [project] }),
    );
    await page.route("**/v1/projects/p", (route) =>
      route.fulfill({ json: project }),
    );
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: `harness ${root}`, exact: true })
      .tap();
    const draft = page.getByRole("textbox", { name: "Message", exact: true });
    await draft.fill("Keep this mobile draft");
    const footer = page.getByRole("group", {
      name: "Composer footer",
      exact: true,
    });
    const menu = footer.getByRole("button", {
      name: "Open sidebar",
      exact: true,
    });
    const location = footer.getByRole("group", {
      name: "Project location",
      exact: true,
    });
    const context = page.getByRole("button", {
      name: "View context",
      exact: true,
    });
    await expect(context).toHaveText("—", { useInnerText: true });
    await expect(location.getByText(root, { exact: true })).toHaveAttribute(
      "title",
      root,
    );
    await expect(
      location.locator("[title]").filter({ hasText: branch }),
    ).toHaveAttribute("title", branch);
    const menuBox = (await menu.boundingBox())!;
    const locationBox = (await location.boundingBox())!;
    const contextBox = (await context.boundingBox())!;
    expect(locationBox.x + locationBox.width).toBeLessThan(contextBox.x);
    expect(contextBox.x + contextBox.width).toBeLessThan(menuBox.x);
    await expect(location.getByText(root, { exact: true })).toBeHidden();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await expect(menu).toBeInViewport();
    agents.a1.context_usage = {
      model: "test/model",
      input_tokens: 0,
      output_tokens: 0,
      estimated_tokens: 0,
      context_window: 100000,
      known: true,
      estimated: true,
    };
    emit("a1", "output", { ResponseType: "usage", Content: "" });
    await expect(context).toHaveText("0%", { useInnerText: true });
    await context.tap();
    await expect(
      page.getByRole("dialog", { name: "Agent settings", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Close", exact: true }).tap();
    await menu.tap();
    await expect(
      page.getByRole("dialog", { name: "Workspace", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Close sidebar", exact: true })
      .tap();
    await expect(draft).toHaveValue("Keep this mobile draft");
    agents.a1.project_id = "";
    emit("a1", "output", { ResponseType: "usage", Content: "" });
    await expect(location).toHaveCount(0);
    await expect(menu).toBeVisible();
    await menu.tap();
    await expect(
      page.getByRole("dialog", { name: "Workspace", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Close sidebar", exact: true })
      .tap();
    await expect(draft).toHaveValue("Keep this mobile draft");
    await page.setViewportSize({ width: 1440, height: 960 });
    await expect(menu).toBeHidden();
    await expect(context).toHaveText("Context 0%", { useInnerText: true });
    await expect(context).toBeVisible();
    await expect(footer).toBeVisible();
  });
});

for (const width of [1440, 390]) {
  test.describe(`edit pending messages (${width}px)`, () => {
    test.use({ viewport: { width, height: 960 }, hasTouch: width < 768 });
    test("moves only the chosen message into the composer without resending", async ({
      page,
    }, testInfo) => {
      const { agents, events, emit } = await workspace(page);
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      agents.a1.state = "running";
      emit("a1", "turn.started", {
        id: "current",
        text: "Current work",
        status: "running",
        created_at: new Date().toISOString(),
      });
      const message = {
        id: "edit-me",
        text: "Review the changes\n\nKeep the tests focused.",
        status: "pending",
        created_at: new Date().toISOString(),
      };
      emit("a1", "message.queued", message);
      emit("a1", "message.queued", {
        ...message,
        id: "keep-queued",
        text: "Another pending message",
      });
      const mutations: string[] = [];
      page.on("request", (request) => {
        if (
          request.method() !== "GET" &&
          request.url().includes("/v1/agents/a1/")
        )
          mutations.push(
            `${request.method()} ${new URL(request.url()).pathname}`,
          );
      });
      const row = page.locator('[data-pending-message-id="edit-me"]');
      const edit = row.getByRole("button", { name: "Edit", exact: true });
      await expect(edit).toBeEnabled();
      const before = testInfo.outputPath(`pending-edit-before-${width}.png`);
      await page.screenshot({ path: before });
      await testInfo.attach("pending-edit-before", {
        path: before,
        contentType: "image/png",
      });
      if (width < 768) await edit.tap();
      else await edit.click();
      const draft = page.getByRole("textbox", { name: "Message", exact: true });
      await expect(row).toHaveCount(0);
      await expect(draft).toHaveValue(message.text);
      await expect(draft).toBeFocused();
      await expect(draft).toBeEditable();
      await expect(
        page.locator('[data-pending-message-id="keep-queued"]'),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Stop", exact: true }),
      ).toBeVisible();
      expect(mutations).toEqual(["DELETE /v1/agents/a1/messages/edit-me"]);
      expect(
        events.a1.find((event) => event.type === "message.cancelled").data.id,
      ).toBe("edit-me");
      await expect(
        page
          .getByRole("region", { name: "Messages", exact: true })
          .getByText(message.text, { exact: true }),
      ).toHaveCount(0);
      const after = testInfo.outputPath(`pending-edit-after-${width}.png`);
      await page.screenshot({ path: after });
      await testInfo.attach("pending-edit-after", {
        path: after,
        contentType: "image/png",
      });
      await draft.fill("The revised message");
      await page
        .getByRole("button", { name: "Send message", exact: true })
        .click();
      await expect(
        page
          .getByRole("article", { name: "Your message", exact: true })
          .filter({ hasText: "The revised message" }),
      ).toBeVisible();
      await expect(draft).toHaveValue("");
      expect(mutations).toEqual([
        "DELETE /v1/agents/a1/messages/edit-me",
        "POST /v1/agents/a1/messages",
      ]);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    });
  });
}

test("editing pending work asks before replacing a draft and keeps it intact if cancellation fails", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const message = {
    id: "edit-confirm",
    text: "Queued work",
    status: "pending",
    created_at: new Date().toISOString(),
  };
  emit("a1", "message.queued", message);
  const edit = page
    .locator('[data-pending-message-id="edit-confirm"]')
    .getByRole("button", { name: "Edit", exact: true });
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("My current draft");
  let deletes = 0;
  await page.route("**/v1/agents/a1/messages/edit-confirm", async (route) => {
    deletes++;
    await route.fulfill({
      status: 503,
      json: { error: { message: "Could not remove pending message" } },
    });
  });
  await edit.click();
  const confirmation = page.getByRole("dialog", {
    name: "Replace current draft?",
    exact: true,
  });
  await expect(confirmation).toBeVisible();
  await confirmation
    .getByRole("button", { name: "Keep draft", exact: true })
    .click();
  await expect(draft).toHaveValue("My current draft");
  expect(deletes).toBe(0);
  await edit.click();
  await confirmation
    .getByRole("button", { name: "Edit message", exact: true })
    .click();
  await expect(confirmation).toHaveCount(0);
  await expect(page.getByRole("alert")).toContainText(
    "Could not remove pending message",
  );
  await expect(draft).toHaveValue("My current draft");
  await expect(draft).toBeEditable();
  await expect(edit).toBeEnabled();
  expect(deletes).toBe(1);
  await page.unroute("**/v1/agents/a1/messages/edit-confirm");
  await edit.click();
  await confirmation
    .getByRole("button", { name: "Edit message", exact: true })
    .click();
  await expect(draft).toHaveValue(message.text);
  await expect(edit).toHaveCount(0);
  await expect(draft).toBeFocused();
});

test("a message that starts while editing cannot be removed or copied over the existing draft", async ({
  page,
}) => {
  const { agents, emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const message = {
    id: "edit-race",
    text: "Queued work",
    status: "pending",
    created_at: new Date().toISOString(),
  };
  emit("a1", "message.queued", message);
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("Do not overwrite this draft");
  let finish!: () => void;
  const gate = new Promise<void>((resolve) => {
    finish = resolve;
  });
  await page.route("**/v1/agents/a1/messages/edit-race", async (route) => {
    await gate;
    await route.fallback();
  });
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Edit message", exact: true })
    .click();
  await expect(draft).toHaveJSProperty("readOnly", true);
  agents.a1.state = "running";
  emit("a1", "turn.started", { ...message, status: "running" });
  await expect(
    page.locator('[data-pending-message-id="edit-race"]'),
  ).toHaveCount(0);
  finish();
  await expect(page.getByRole("alert")).toContainText(
    "Only pending messages can be removed",
  );
  await expect(draft).toHaveValue("Do not overwrite this draft");
  await expect(draft).toBeEditable();
  await expect(
    page.getByRole("button", { name: "Stop", exact: true }),
  ).toBeVisible();
});

test("pending edits remain associated with their original chat when switching away and back during removal", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  for (let i = 0; i < 2; i++) {
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    await expect(page).toHaveURL(new RegExp(`/agents/a${i + 1}$`));
  }
  const message = {
    id: "edit-switch",
    text: "The first chat's pending message",
    status: "pending",
    created_at: new Date().toISOString(),
  };
  emit("a1", "message.queued", message);
  await page.locator('aside a[href="/agents/a1"]').click();
  let finish!: () => void;
  const gate = new Promise<void>((resolve) => {
    finish = resolve;
  });
  let requests = 0;
  await page.route("**/v1/agents/a1/messages/edit-switch", async (route) => {
    requests++;
    await gate;
    await route.fallback();
  });
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(draft).toHaveJSProperty("readOnly", true);
  await page.locator('aside a[href="/agents/a2"]').click();
  await expect(draft).toBeEditable();
  await draft.fill("A different chat's draft");
  await page.locator('aside a[href="/agents/a1"]').click();
  await expect(draft).toHaveValue("");
  await expect(draft).toHaveJSProperty("readOnly", true);
  await expect(
    page.getByRole("button", { name: "Edit", exact: true }),
  ).toBeDisabled();
  expect(requests).toBe(1);
  finish();
  await expect(draft).toHaveValue(message.text);
  await expect(draft).toBeEditable();
  await expect(
    page.getByRole("button", { name: "Edit", exact: true }),
  ).toHaveCount(0);
  await page.locator('aside a[href="/agents/a2"]').click();
  await expect(draft).toHaveValue("A different chat's draft");
  await page.locator('aside a[href="/agents/a1"]').click();
  await expect(draft).toHaveValue(message.text);
});

test("editing a pending literal slash message uses a new submission key instead of reusing its cancelled receipt", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const keys: string[] = [];
  const texts: string[] = [];
  let stops = 0;
  page.on("request", (request) => {
    if (request.url().endsWith("/a1/stop")) stops++;
  });
  await page.route("**/v1/agents/a1/messages", async (route) => {
    const request = route.request();
    keys.push(request.headers()["idempotency-key"]);
    texts.push(request.postDataJSON().text);
    const message = {
      id: `slash-${keys.length}`,
      text: request.postDataJSON().text,
      status: "pending",
      created_at: new Date().toISOString(),
    };
    emit("a1", "message.queued", message);
    if (keys.length === 1)
      await route.fulfill({
        status: 503,
        json: { error: { message: "Response lost after enqueue" } },
      });
    else await route.fulfill({ status: 202, json: message });
  });
  const draft = page.getByRole("textbox", { name: "Message", exact: true });
  await draft.fill("//stop");
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText(
    "Response lost after enqueue",
  );
  await expect(draft).toHaveValue("//stop");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Edit", exact: true }),
  ).toHaveCount(0);
  await expect(draft).toHaveValue("//stop");
  await expect(
    page.getByRole("button", { name: "Send message", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Send message", exact: true }).click();
  await expect(
    page.locator('[data-pending-message-id="slash-2"]'),
  ).toBeVisible();
  expect(texts).toEqual(["/stop", "/stop"]);
  expect(keys[0]).toBeTruthy();
  expect(keys[1]).toBeTruthy();
  expect(keys[1]).not.toBe(keys[0]);
  expect(stops).toBe(0);
});

for (const width of [1440, 390]) {
  test(`pending queue header stays fixed while rows scroll (${width}px)`, async ({
    page,
  }, testInfo) => {
    const { agents, emit } = await workspace(page);
    await page.setViewportSize({ width, height: 960 });
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    for (let i = 0; i < 16; i++)
      emit("a1", "message.queued", {
        id: `sticky-${i}`,
        text: `Pending message ${i + 1}`,
        status: "pending",
        created_at: new Date().toISOString(),
      });
    const queue = page.getByRole("region", {
      name: "Pending messages",
      exact: true,
    });
    const header = queue.locator('[data-slot="pending-queue-header"]');
    await expect(header).toHaveText("Up next · 16 pending");
    const before = (await header.boundingBox())!;
    expect((await queue.boundingBox())!.height).toBeLessThanOrEqual(160);
    await queue.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await expect
      .poll(() => queue.evaluate((element) => element.scrollTop))
      .toBeGreaterThan(0);
    await expect(header).toBeInViewport();
    expect((await header.boundingBox())!.y).toBe(before.y);
    await expect(header).toHaveCSS(
      "background-color",
      await queue.evaluate(
        (element) => getComputedStyle(element).backgroundColor,
      ),
    );
    expect(
      await page.evaluate(
        ({ x, y }) =>
          !!document
            .elementFromPoint(x, y)
            ?.closest('[data-slot="pending-queue-header"]'),
        { x: before.x + before.width / 2, y: before.y + before.height / 2 },
      ),
    ).toBe(true);
    await expect(
      queue.locator('[data-pending-message-id="sticky-0"]'),
    ).not.toBeInViewport();
    const last = queue.locator('[data-pending-message-id="sticky-15"]');
    await expect(last).toBeInViewport();
    const path = testInfo.outputPath(`pending-queue-sticky-${width}.png`);
    await page.screenshot({ path });
    await testInfo.attach("pending-queue-sticky", {
      path,
      contentType: "image/png",
    });
    await last.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(header).toHaveText("Up next · 15 pending");
    expect((await header.boundingBox())!.y).toBe(before.y);
    await queue
      .locator('[data-pending-message-id="sticky-14"]')
      .getByRole("button", { name: "Edit", exact: true })
      .click();
    await expect(header).toHaveText("Up next · 14 pending");
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toHaveValue("Pending message 15");
    agents.a1.held = true;
    emit("a1", "output", { ResponseType: "usage", Content: "" });
    await expect(header).toHaveText("Queue held · 14 pending");
    const heldPosition = (await header.boundingBox())!.y;
    await queue.evaluate((element) => {
      element.scrollTop = 0;
    });
    expect((await header.boundingBox())!.y).toBe(heldPosition);
    await expect(
      queue.locator('[data-pending-message-id="sticky-0"]'),
    ).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
  });
}

test("typing in a long chat does not rerender the transcript", async ({
  page,
}, testInfo) => {
  // Timestamp formatting is a render probe for message rows in both development
  // and production bundles; it avoids machine-dependent frame-time thresholds.
  await page.addInitScript(() => {
    const probe = window as typeof window & { messageRenderCount: number };
    probe.messageRenderCount = 0;
    const format = Date.prototype.toLocaleTimeString;
    Date.prototype.toLocaleTimeString = function (
      this: Date,
      ...args: Parameters<typeof format>
    ) {
      probe.messageRenderCount++;
      return format.apply(this, args);
    };
  });
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const content = (i: number) =>
    `## Result ${i}\n\n${"A detailed explanation of the implementation. ".repeat(15)}\n\n- Read the source\n- Run the tests\n\n\`\`\`ts\nconst ready = true\n\`\`\``;
  for (let i = 0; i < 120; i++)
    emit("a1", "output", { ResponseType: "agent", Content: content(i) });
  await expect(
    page.getByRole("article", { name: "Assistant message", exact: true }),
  ).toHaveCount(120);
  const renders = () =>
    page.evaluate(
      () =>
        (window as typeof window & { messageRenderCount: number })
          .messageRenderCount,
    );
  expect(await renders()).toBeGreaterThanOrEqual(120);
  const input = page.getByRole("textbox", { name: "Message", exact: true });
  await input.focus();
  const before = await renders();
  const start = Date.now();
  await input.pressSequentially("Typing should stay fast");
  const typingRenders = (await renders()) - before;
  await testInfo.attach("typing-performance", {
    body: JSON.stringify({
      typingMs: Date.now() - start,
      transcriptRenders: typingRenders,
    }),
    contentType: "application/json",
  });
  expect(typingRenders).toBe(0);
  await expect(input).toHaveValue("Typing should stay fast");
  await expect(
    page.getByRole("button", { name: "Send message", exact: true }),
  ).toBeEnabled();
  await input.fill("");
  await expect(
    page.getByRole("button", { name: "Send message", exact: true }),
  ).toBeDisabled();
  expect(await renders()).toBe(before);
  emit("a1", "output", { ResponseType: "agent", Content: content(120) });
  await expect(
    page.getByRole("article", { name: "Assistant message", exact: true }),
  ).toHaveCount(121);
  const liveRenders = (await renders()) - before;
  expect(liveRenders).toBeGreaterThan(0);
  expect(liveRenders).toBeLessThan(8);
  await expect(
    page.getByRole("heading", { name: "Result 120", exact: true }),
  ).toBeVisible();
});

test.describe("mobile drawer animation", () => {
  test.use({ hasTouch: true, viewport: { width: 390, height: 844 } });
  test("plays a short entry animation on each open and honors reduced motion", async ({
    page,
  }, testInfo) => {
    await page.addInitScript(() => {
      const probe = window as typeof window & {
        drawerAnimationStarts: {
          duration: number;
          transform: string;
          opacity: string;
        }[];
      };
      probe.drawerAnimationStarts = [];
      document.addEventListener("animationstart", (event) => {
        if (
          !(event.target instanceof HTMLElement) ||
          event.target.dataset.slot !== "sheet-content"
        )
          return;
        const animation = event.target
          .getAnimations()
          .find(
            (animation) =>
              animation instanceof CSSAnimation &&
              animation.animationName === "enter",
          );
        if (!(animation?.effect instanceof KeyframeEffect)) return;
        const first = animation.effect.getKeyframes()[0];
        probe.drawerAnimationStarts.push({
          duration: Number(animation.effect.getTiming().duration),
          transform: String(first.transform),
          opacity: String(first.opacity),
        });
      });
    });
    await workspace(page);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .tap();
    const menu = page.getByRole("button", {
      name: "Open sidebar",
      exact: true,
    });
    const drawer = page.getByRole("dialog", { name: "Workspace", exact: true });
    const starts = () =>
      page.evaluate(
        () =>
          (
            window as typeof window & {
              drawerAnimationStarts: {
                duration: number;
                transform: string;
                opacity: string;
              }[];
            }
          ).drawerAnimationStarts,
      );
    for (let i = 1; i <= 2; i++) {
      await menu.tap();
      await expect(drawer).toBeVisible();
      await expect(drawer).toHaveCSS("animation-name", "enter");
      await expect(drawer).toHaveCSS("animation-duration", "0.2s");
      await expect.poll(async () => (await starts()).length).toBe(i);
      const animation = (await starts()).at(-1)!;
      expect(animation.duration).toBe(200);
      expect(animation.transform).toContain("16px");
      expect(animation.opacity).toBe("0");
      await expect
        .poll(async () => {
          const box = (await drawer.boundingBox())!;
          return Math.round(box.y + box.height);
        })
        .toBe(844);
      if (i === 1) {
        const path = testInfo.outputPath("mobile-drawer-animation.png");
        await page.screenshot({ path });
        await testInfo.attach("mobile-drawer-animation", {
          path,
          contentType: "image/png",
        });
      }
      await page.keyboard.press("Escape");
      await expect(drawer).toHaveCount(0);
    }
    await page.emulateMedia({ reducedMotion: "reduce" });
    await menu.tap();
    await expect(drawer).toBeVisible();
    await expect(drawer).toHaveCSS("animation-name", "none");
    expect((await starts()).length).toBe(2);
    await drawer
      .getByRole("button", { name: "Settings for harness", exact: true })
      .tap();
    await expect(
      page.getByRole("dialog", { name: "harness defaults", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await page.getByRole("button", { name: "Close", exact: true }).tap();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const input = page.getByRole("textbox", { name: "Message", exact: true });
    await input.tap();
    await expect(input).toBeFocused();
    await input.fill("Still responsive after closing the drawer");
  });
});
