import {
  test,
  expect,
  type Page,
  type WebSocketRoute,
  type TestInfo,
} from "@playwright/test";
import { workspace } from "./fixtures";
import type { LiveCommand, LiveTab } from "../src/lib/browser-live";

async function capture(page: Page, info: TestInfo, name: string) {
  const path = info.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await info.attach(name, { path, contentType: "image/png" });
}

async function touchGesture(
  page: Page,
  points: Array<{ x: number; y: number }>,
) {
  const session = await page.context().newCDPSession(page);
  await session.send("Emulation.setTouchEmulationEnabled", {
    enabled: true,
    maxTouchPoints: 1,
  });
  await session.send("Input.dispatchTouchEvent", {
    type: "touchStart",
    touchPoints: [{ ...points[0], id: 1 }],
  });
  for (const point of points.slice(1)) {
    await session.send("Input.dispatchTouchEvent", {
      type: "touchMove",
      touchPoints: [{ ...point, id: 1 }],
    });
  }
  await session.send("Input.dispatchTouchEvent", {
    type: "touchEnd",
    touchPoints: [],
  });
  await session.send("Emulation.setTouchEmulationEnabled", { enabled: false });
  await session.detach();
}

async function liveBrowser(page: Page) {
  const chat = await workspace(page);
  const commands: LiveCommand[] = [];
  const sockets: WebSocketRoute[] = [];
  let tabs: LiveTab[] = [];
  let selected = "";
  let pinned = "";
  let jpeg = "";
  let scrolledJpeg = "";
  let streamFrames = true;
  let nextId = 1;
  const send = (event: unknown) => sockets.at(-1)?.send(JSON.stringify(event));
  const state = () => {
    const viewed = pinned || selected;
    send({ type: "state", tabs, selected, pinned, tab_id: viewed });
    if (streamFrames && tabs.some((t) => t.id === viewed))
      send({
        type: "frame",
        tab_id: viewed,
        data: jpeg,
        width: 1280,
        height: 720,
      });
  };
  const newTab = (
    url = "https://demo.test",
    title = "Demo shop",
    human = false,
  ) => {
    const tab = { id: String(nextId++), title, url };
    tabs.push(tab);
    if (!human || !selected) selected = tab.id;
    if (human) pinned = tab.id;
    state();
    return tab.id;
  };
  await page.routeWebSocket("**/v1/agents/*/browser", (ws) => {
    sockets.push(ws);
    ws.onMessage((raw) => {
      const command = JSON.parse(String(raw)) as LiveCommand;
      commands.push(command);
      if (command.type === "watch") {
        pinned = command.tab_id || "";
        state();
      }
      if (command.type === "new") newTab(command.url, "Demo shop", true);
      if (command.type === "navigate") {
        if (!tabs.length) newTab(command.url, "Demo shop", true);
        else {
          tabs = tabs.map((t) =>
            t.id === command.tab_id ? { ...t, url: command.url! } : t,
          );
          state();
        }
      }
      if (["back", "forward", "reload"].includes(command.type)) state();
      if (command.event === "mouseWheel" && command.delta_y) {
        jpeg = scrolledJpeg;
        state();
      }
      if (command.type === "close") {
        tabs = tabs.filter((t) => t.id !== command.tab_id);
        if (!tabs.some((t) => t.id === selected)) selected = tabs[0]?.id || "";
        if (!tabs.some((t) => t.id === pinned)) pinned = "";
        state();
      }
    });
  });
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const frames = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 1280;
    canvas.height = 720;
    const ctx = canvas.getContext("2d")!;
    ctx.fillStyle = "#f8fafc";
    ctx.fillRect(0, 0, 1280, 720);
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, 1280, 88);
    ctx.fillStyle = "#0f172a";
    ctx.font = "600 28px sans-serif";
    ctx.fillText("Fieldwork", 64, 54);
    ctx.font = "18px sans-serif";
    ctx.fillText("Collection      About      Cart (1)", 872, 52);
    ctx.fillStyle = "#e2e8f0";
    ctx.fillRect(64, 152, 528, 480);
    ctx.fillStyle = "#94a3b8";
    ctx.fillRect(224, 264, 208, 248);
    ctx.fillStyle = "#64748b";
    ctx.fillRect(252, 240, 152, 24);
    ctx.fillStyle = "#334155";
    ctx.font = "16px sans-serif";
    ctx.fillText("EVERYDAY ESSENTIALS", 672, 192);
    ctx.fillStyle = "#0f172a";
    ctx.font = "600 40px sans-serif";
    ctx.fillText("The day pack", 672, 256);
    ctx.font = "24px sans-serif";
    ctx.fillText("$48.00", 672, 304);
    ctx.fillStyle = "#475569";
    ctx.font = "20px sans-serif";
    ctx.fillText("Made for wherever the day takes you.", 672, 360);
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(672, 400, 464, 56);
    ctx.fillStyle = "#64748b";
    ctx.fillText("Email for availability", 696, 436);
    ctx.fillStyle = "#0f172a";
    ctx.fillRect(672, 480, 464, 56);
    ctx.fillStyle = "#ffffff";
    ctx.fillText("Add to cart", 848, 516);
    ctx.fillStyle = "#64748b";
    ctx.font = "16px sans-serif";
    ctx.fillText("Deterministic browser preview fixture", 64, 684);
    const top = canvas.toDataURL("image/jpeg").split(",")[1];
    ctx.fillStyle = "#f8fafc";
    ctx.fillRect(0, 0, 1280, 720);
    ctx.fillStyle = "#ffffff";
    ctx.fillRect(0, 0, 1280, 88);
    ctx.fillStyle = "#0f172a";
    ctx.font = "600 28px sans-serif";
    ctx.fillText("Fieldwork", 64, 54);
    ctx.font = "600 40px sans-serif";
    ctx.fillText("Built for the long way around", 64, 184);
    ctx.fillStyle = "#475569";
    ctx.font = "20px sans-serif";
    ctx.fillText("You scrolled the shared page", 64, 232);
    ctx.fillStyle = "#dbeafe";
    ctx.fillRect(64, 288, 1152, 296);
    ctx.fillStyle = "#1e3a8a";
    ctx.font = "600 32px sans-serif";
    ctx.fillText("More from the collection", 416, 448);
    return {
      top,
      scrolled: canvas.toDataURL("image/jpeg").split(",")[1],
    };
  });
  jpeg = frames.top;
  scrolledJpeg = frames.scrolled;
  const open = async () => {
    await page.getByRole("button", { name: "Browser", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Open browser", exact: true }),
    ).toBeVisible();
  };
  const viewport = page.getByRole("textbox", {
    name: "Interactive browser viewport",
  });
  return {
    ...chat,
    commands,
    sockets,
    send,
    state,
    newTab,
    open,
    viewport,
    setStreaming: (enabled: boolean) => {
      streamFrames = enabled;
    },
    select: (id: string) => {
      selected = id;
      state();
    },
  };
}

test("subscribes without starting; desktop split, expand, narrow and close preserve drafts", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await composer.fill("Keep this draft");
  await live.open();
  expect(live.commands).toEqual([{ type: "watch", tab_id: "" }]);
  await expect(composer).toBeVisible();
  await expect(
    page.getByText("Shared with Ted", { exact: false }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Expand browser" }).click();
  await expect(composer).toBeHidden();
  await page.getByRole("button", { name: "Narrow browser" }).click();
  await expect(composer).toHaveValue("Keep this draft");
  await page.getByRole("button", { name: "Close browser panel" }).click();
  await expect(page.getByRole("region", { name: "Live browser" })).toHaveCount(
    0,
  );
  await expect(
    page.getByRole("button", { name: "Browser", exact: true }),
  ).toBeFocused();
  expect(live.commands.every((c) => c.type === "watch")).toBe(true);
});

test("tabs follow agent or pin independently, navigation and tab lifecycle use explicit IDs", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  await page.getByRole("button", { name: "Open browser", exact: true }).click();
  await expect(live.viewport).toBeVisible();
  const first = "1";
  await expect(
    page.getByRole("button", { name: "Follow agent", exact: true }),
  ).toHaveAttribute("aria-pressed", "false");
  await page.getByRole("button", { name: "Follow agent", exact: true }).click();
  const second = live.newTab("https://second.test", "Research");
  await expect(
    page.getByRole("textbox", { name: "Browser address" }),
  ).toHaveValue("https://second.test");
  await page.getByRole("button", { name: "Demo shop", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Demo shop", exact: true }),
  ).toHaveAttribute("aria-pressed", "true");
  live.select(second);
  await expect(
    page.getByRole("textbox", { name: "Browser address" }),
  ).toHaveValue("about:blank");
  await page
    .getByRole("textbox", { name: "Browser address" })
    .fill("https://example.test/path");
  await page.getByRole("button", { name: "Go", exact: true }).click();
  await page.getByRole("button", { name: "Browser back", exact: true }).click();
  await page
    .getByRole("button", { name: "Browser forward", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Reload browser page", exact: true })
    .click();
  await expect
    .poll(
      () =>
        live.commands.filter((c) =>
          ["navigate", "back", "forward", "reload"].includes(c.type),
        ).length,
    )
    .toBe(4);
  for (const command of live.commands.filter((c) =>
    ["navigate", "back", "forward", "reload"].includes(c.type),
  ))
    expect(command.tab_id).toBe(first);
  await page.getByRole("button", { name: "Follow agent", exact: true }).click();
  await expect(
    page.getByRole("textbox", { name: "Browser address" }),
  ).toHaveValue("https://second.test");
  await page
    .getByRole("button", { name: "New browser tab", exact: true })
    .click();
  await expect
    .poll(() => live.commands.filter((c) => c.type === "new").length)
    .toBe(2);
  await page
    .getByRole("button", { name: "Close tab Research", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Close tab Research", exact: true }),
  ).toHaveCount(0);
});

test("scaled hover, click, drag, double click, wheel, keys, paste, composition and scoped release", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const box = (await live.viewport.boundingBox())!;
  const center = { x: box.x + box.width / 2, y: box.y + box.height / 2 };
  await page.mouse.move(center.x, center.y);
  await expect
    .poll(() => live.commands.filter((c) => c.event === "mouseMoved").length)
    .toBeGreaterThan(0);
  const hover = live.commands.filter((c) => c.event === "mouseMoved").at(-1)!;
  expect(hover.x).toBeCloseTo(640, 0);
  expect(hover.y).toBeCloseTo(360, 0);
  await page.mouse.down();
  await page.mouse.move(center.x + 40, center.y + 20, { steps: 4 });
  await page.mouse.up();
  await expect
    .poll(() =>
      live.commands.some((c) => c.event === "mouseMoved" && c.buttons === 1),
    )
    .toBe(true);
  await live.viewport.dblclick({ position: { x: 80, y: 80 } });
  await expect
    .poll(() =>
      live.commands.some(
        (c) => c.event === "mousePressed" && c.click_count === 2,
      ),
    )
    .toBe(true);
  await page.mouse.move(center.x, center.y);
  await page.mouse.wheel(4, 80);
  await expect
    .poll(() => live.commands.some((c) => c.event === "mouseWheel"))
    .toBe(true);
  const wheel = live.commands.find((c) => c.event === "mouseWheel")!;
  expect(wheel.delta_y).toBeCloseTo((80 * 1280) / box.width, 0);
  await live.viewport.press("Shift+A");
  await live.viewport.press("ArrowLeft");
  await live.viewport.evaluate((node) => {
    const clipboardData = new DataTransfer();
    clipboardData.setData("text/plain", "Pasted ✓");
    node.dispatchEvent(
      new ClipboardEvent("paste", { clipboardData, bubbles: true }),
    );
  });
  await live.viewport.evaluate((node) => {
    node.dispatchEvent(
      new CompositionEvent("compositionstart", { bubbles: true }),
    );
    node.dispatchEvent(
      new CompositionEvent("compositionend", { bubbles: true, data: "日本語" }),
    );
  });
  await expect
    .poll(() =>
      live.commands.some((c) => c.type === "text" && c.text === "日本語"),
    )
    .toBe(true);
  expect(
    live.commands.some(
      (c) => c.event === "keyDown" && c.text === "A" && c.modifiers === 8,
    ),
  ).toBe(true);
  expect(
    live.commands.some((c) => c.type === "text" && c.text === "Pasted ✓"),
  ).toBe(true);
  const releases = live.commands.filter((c) => c.type === "release").length;
  await live.viewport.press("Escape");
  await expect(
    page.getByRole("button", { name: "Close browser panel" }),
  ).toBeFocused();
  await expect
    .poll(() => live.commands.filter((c) => c.type === "release").length)
    .toBeGreaterThan(releases);
  const keys = live.commands.filter((c) => c.type === "key").length;
  await page
    .getByRole("textbox", { name: "Message", exact: true })
    .fill("Only the chat");
  expect(live.commands.filter((c) => c.type === "key")).toHaveLength(keys);
  await live.viewport.click();
  await page.keyboard.down("Shift");
  await page.evaluate(() => window.dispatchEvent(new Event("blur")));
  await expect(live.viewport).not.toBeFocused();
  await page.keyboard.up("Shift");
});

test("touch pan scrolls remotely, touch tap clicks, and mouse drag remains a drag", async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const demoHold = () =>
    process.env.TED_WEB_RECORD === "1"
      ? page.waitForTimeout(1800)
      : Promise.resolve();
  await demoHold();
  const box = (await live.viewport.boundingBox())!;
  const x = box.x + box.width / 2;
  const startY = box.y + box.height * 0.72;
  const beforePan = live.commands.length;
  const image = page.getByAltText("Live browser page");
  const beforeSrc = await image.getAttribute("src");
  const points = Array.from({ length: 7 }, (_, index) => ({
    x,
    y: startY - index * 14,
  }));
  await touchGesture(page, points);
  await expect
    .poll(
      () =>
        live.commands
          .slice(beforePan)
          .filter((command) => command.event === "mouseWheel").length,
    )
    .toBeGreaterThan(0);
  const panCommands = live.commands.slice(beforePan);
  const wheels = panCommands.filter(
    (command) => command.event === "mouseWheel",
  );
  expect(wheels.length).toBeLessThan(points.length - 1);
  expect(
    wheels.reduce((total, command) => total + (command.delta_y || 0), 0),
  ).toBeGreaterThan(0);
  expect(
    panCommands.some(
      (command) =>
        command.event === "mousePressed" || command.event === "mouseReleased",
    ),
  ).toBe(false);
  await expect.poll(() => image.getAttribute("src")).not.toBe(beforeSrc);
  await demoHold();
  await capture(page, testInfo, "live-browser-touch-scroll-mobile");
  await demoHold();

  const beforeTap = live.commands.length;
  await touchGesture(page, [{ x: box.x + 80, y: box.y + 80 }]);
  await expect
    .poll(
      () =>
        live.commands
          .slice(beforeTap)
          .filter(
            (command) =>
              command.event === "mousePressed" ||
              command.event === "mouseReleased",
          ).length,
    )
    .toBe(2);
  expect(
    live.commands
      .slice(beforeTap)
      .filter(
        (command) =>
          command.event === "mousePressed" || command.event === "mouseReleased",
      )
      .map((command) => [command.event, command.button, command.buttons]),
  ).toEqual([
    ["mousePressed", "left", 1],
    ["mouseReleased", "left", 0],
  ]);
  await demoHold();

  const beforeMouse = live.commands.length;
  await page.mouse.move(x, startY);
  await page.mouse.down();
  await page.mouse.move(x + 32, startY - 24, { steps: 3 });
  await page.mouse.up();
  await expect
    .poll(() =>
      live.commands
        .slice(beforeMouse)
        .some(
          (command) => command.event === "mouseMoved" && command.buttons === 1,
        ),
    )
    .toBe(true);
  const mouseDrag = live.commands.slice(beforeMouse);
  expect(mouseDrag.some((command) => command.event === "mousePressed")).toBe(
    true,
  );
  expect(mouseDrag.some((command) => command.event === "mouseReleased")).toBe(
    true,
  );
  await demoHold();
});

test("agent overlay is tab-filtered, pointer-transparent, expires and clears on navigation", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const cursor = page.getByTestId("ted-browser-cursor");
  live.send({
    type: "activity",
    tab_id: "other",
    kind: "click",
    x: 640,
    y: 360,
  });
  await expect(cursor).toHaveCount(0);
  live.send({ type: "activity", tab_id: "1", kind: "click", x: 640, y: 360 });
  await expect(cursor).toBeVisible();
  expect(
    await cursor.evaluate((node) => getComputedStyle(node).pointerEvents),
  ).toBe("none");
  await live.viewport.click();
  await expect(cursor).toHaveCount(0, { timeout: 2500 });
  live.send({ type: "activity", tab_id: "1", kind: "move", x: 100, y: 100 });
  await expect(cursor).toBeVisible();
  await page
    .getByRole("button", { name: "Reload browser page", exact: true })
    .click();
  await expect(cursor).toHaveCount(0);
});

test("disconnect disables stale input and reconnects watch; errors remain visible and retryable", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const initialSockets = live.sockets.length;
  await live.sockets.at(-1)!.close();
  await expect(
    page.getByText("Browser disconnected. Reconnecting…"),
  ).toBeVisible();
  await expect(live.viewport).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "New browser tab" }),
  ).toBeDisabled();
  await expect.poll(() => live.sockets.length).toBe(initialSockets + 1);
  await expect(live.viewport).toBeVisible();
  expect(live.commands.filter((c) => c.type === "watch")).toHaveLength(2);
  expect(live.commands.filter((c) => c.type === "new")).toHaveLength(0);
  live.send({
    type: "error",
    code: "navigation_failed",
    message: "The page could not be reached.",
  });
  await expect(page.getByRole("alert")).toContainText(
    "The page could not be reached.",
  );
  await page
    .getByRole("button", { name: "Reconnect browser", exact: true })
    .click();
  await expect.poll(() => live.sockets.length).toBe(initialSockets + 2);
  await expect(live.viewport).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
});

for (const width of [320, 390, 768]) {
  test(`narrow ${width}px panel replaces chat, fits and restores draft`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 844 });
    const live = await liveBrowser(page);
    await page
      .getByRole("textbox", { name: "Message", exact: true })
      .fill("Mobile draft");
    await live.open();
    live.newTab();
    await expect(live.viewport).toBeVisible();
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toBeHidden();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
    const box = (await live.viewport.boundingBox())!;
    expect(box.width).toBeGreaterThan(250);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    await capture(page, testInfo, `live-browser-${width}`);
    await page.getByRole("button", { name: "Close browser panel" }).click();
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toHaveValue("Mobile draft");
  });
}

test("live browser preview demo", async ({ page }, testInfo) => {
  test.skip(
    process.env.TED_WEB_RECORD !== "1",
    "Opt-in visual evidence capture",
  );
  const live = await liveBrowser(page);
  live.emit("a1", "output", {
    ResponseType: "agent",
    Content:
      "I’m reviewing the shop. You can use the browser alongside me — there’s no need to pause or take over.",
  });
  const hold = () => page.waitForTimeout(1100);
  await hold();
  await live.open();
  await hold();
  await capture(page, testInfo, "live-browser-empty");
  await page.getByRole("button", { name: "Open browser", exact: true }).click();
  await hold();
  live.send({ type: "activity", tab_id: "1", kind: "click", x: 928, y: 508 });
  await page.waitForTimeout(150);
  await capture(page, testInfo, "live-browser-desktop");
  await hold();
  await live.viewport.click({ position: { x: 80, y: 80 } });
  await hold();
  await live.viewport.press("Escape");
  await hold();
  const second = live.newTab("https://docs.test", "Research");
  await hold();
  await page.getByRole("button", { name: "Demo shop", exact: true }).click();
  await hold();
  live.select(second);
  await hold();
  await page.getByRole("button", { name: "Follow agent", exact: true }).click();
  await hold();
  await page
    .getByRole("button", { name: "Expand browser", exact: true })
    .click();
  await hold();
  await capture(page, testInfo, "live-browser-expanded");
  await page
    .getByRole("button", { name: "Narrow browser", exact: true })
    .click();
  await hold();
  await page.emulateMedia({ colorScheme: "dark" });
  await hold();
  await capture(page, testInfo, "live-browser-dark");
  await page
    .getByRole("button", { name: "Close browser panel", exact: true })
    .click();
  await hold();
});

test("expanded browser does not acknowledge hidden new chat responses", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  await page
    .getByRole("button", { name: "Expand browser", exact: true })
    .click();
  live.emit("a1", "output", {
    ResponseType: "agent",
    Content: "This is behind the expanded browser.",
  });
  await page.waitForTimeout(150);
  expect(live.agents.a1.read_cursor).toBe(0);
  await page
    .getByRole("button", { name: "Close browser panel", exact: true })
    .click();
  await expect.poll(() => live.agents.a1.read_cursor).toBe(1);
});

test("static page remains interactive for same-tab pin/follow, history no-op and background close without new frames", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  live.setStreaming(false);
  await page.getByRole("button", { name: /^Demo shop/ }).click();
  await expect(live.viewport).toBeVisible();
  await page.getByRole("button", { name: "Follow agent", exact: true }).click();
  await expect(live.viewport).toBeVisible();
  await page.getByRole("button", { name: "Browser back", exact: true }).click();
  await expect(live.viewport).toBeVisible();
  await page.getByRole("button", { name: /^Demo shop/ }).click();
  live.newTab("https://research.test", "Research");
  await page
    .getByRole("button", { name: "Close tab Research", exact: true })
    .click();
  await expect(live.viewport).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Close tab Research", exact: true }),
  ).toHaveCount(0);
  await live.viewport.click();
  const deadKeyAllowed = await live.viewport.evaluate((node) => {
    const dead = new KeyboardEvent("keydown", {
      key: "Dead",
      code: "Quote",
      bubbles: true,
      cancelable: true,
    });
    node.dispatchEvent(dead);
    return !dead.defaultPrevented;
  });
  expect(deadKeyAllowed).toBe(true);
  await live.viewport.evaluate((node) => {
    node.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "@",
        code: "KeyQ",
        ctrlKey: true,
        altKey: true,
        modifierAltGraph: true,
        bubbles: true,
      }),
    );
    node.dispatchEvent(
      new KeyboardEvent("keyup", {
        key: "@",
        code: "KeyQ",
        ctrlKey: true,
        altKey: true,
        modifierAltGraph: true,
        bubbles: true,
      }),
    );
  });
  await expect
    .poll(
      () =>
        live.commands.filter(
          (command) => command.type === "text" && command.text === "@",
        ).length,
    )
    .toBe(1);
});
