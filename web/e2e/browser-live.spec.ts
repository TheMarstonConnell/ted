import { test, expect, type Page, type WebSocketRoute } from "@playwright/test";
import { workspace } from "./fixtures";
import type { LiveCommand, LiveTab } from "../src/lib/browser-live";

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
    canvas.width = 16;
    canvas.height = 9;
    const ctx = canvas.getContext("2d")!;
    const frame = (color: string) => {
      ctx.fillStyle = color;
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      return canvas.toDataURL("image/jpeg").split(",")[1];
    };
    return { top: frame("#e2e8f0"), scrolled: frame("#dbeafe") };
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
    navigate: (id: string, url: string) => {
      tabs = tabs.map((tab) => (tab.id === id ? { ...tab, url } : tab));
      state();
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
  const panel = page.getByRole("region", { name: "Live browser" });
  await expect(
    panel.getByText("Shared with Ted", { exact: false }),
  ).toHaveCount(0);
  await expect(
    panel.getByRole("heading", { name: "Browser", exact: true }),
  ).toHaveCount(0);
  const navigation = panel.getByRole("form", { name: "Browser navigation" });
  const address = navigation.getByRole("textbox", { name: "Browser address" });
  const expand = navigation.getByRole("button", { name: "Expand browser" });
  const close = navigation.getByRole("button", { name: "Close browser panel" });
  for (const control of [expand, close]) {
    const addressBox = (await address.boundingBox())!;
    const controlBox = (await control.boundingBox())!;
    expect(
      Math.abs(
        addressBox.y +
          addressBox.height / 2 -
          controlBox.y -
          controlBox.height / 2,
      ),
    ).toBeLessThan(1);
    expect(controlBox.x).toBeGreaterThan(addressBox.x + addressBox.width);
  }
  await address.fill("https://do-not-navigate.test");
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

for (const width of [1024, 1280]) {
  test(`window controls stay with the URL in a desktop split (${width}px)`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    const live = await liveBrowser(page);
    await live.open();
    const navigation = page.getByRole("form", { name: "Browser navigation" });
    const addressBox = (await navigation
      .getByRole("textbox", { name: "Browser address" })
      .boundingBox())!;
    for (const name of ["Expand browser", "Close browser panel"]) {
      const box = (await navigation
        .getByRole("button", { name, exact: true })
        .boundingBox())!;
      expect(
        Math.abs(box.y + box.height / 2 - addressBox.y - addressBox.height / 2),
      ).toBeLessThan(1);
      expect(box.x).toBeGreaterThanOrEqual(addressBox.x + addressBox.width);
    }
    if (width === 1280) {
      const backBox = (await navigation
        .getByRole("button", { name: "Browser back" })
        .boundingBox())!;
      expect(
        Math.abs(
          backBox.y + backBox.height / 2 - addressBox.y - addressBox.height / 2,
        ),
      ).toBeLessThan(1);
    }
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBe(width);
  });
}

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

test("address editing preserves drafts and resyncs remote navigation when focus leaves", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  const tab = live.newTab("https://initial.test");
  const address = page.getByRole("textbox", { name: "Browser address" });
  await expect(address).toHaveValue("https://initial.test");
  await address.fill("https://draft.test");
  live.navigate(tab, "https://redirected.test");
  await expect(address).toHaveValue("https://draft.test");
  await page
    .getByRole("button", { name: "Reload browser page", exact: true })
    .click();
  await expect(address).toHaveValue("https://redirected.test");
  for (const viaKeyboard of [false, true]) {
    const url = `https://submit.test/${viaKeyboard ? "keyboard" : "mouse"}`;
    await address.fill(url);
    if (viaKeyboard) {
      await address.press("Tab");
      await expect(
        page.getByRole("button", { name: "Go", exact: true }),
      ).toBeFocused();
      await page.keyboard.press("Enter");
    } else {
      await page.getByRole("button", { name: "Go", exact: true }).click();
    }
    await expect
      .poll(() =>
        live.commands.filter((command) => command.type === "navigate").at(-1),
      )
      .toMatchObject({ tab_id: tab, url });
  }
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

test("chorded mouse buttons release independently", async ({ page }) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const box = (await live.viewport.boundingBox())!;
  for (const first of ["left", "right"] as const) {
    const second = first === "left" ? "right" : "left";
    const before = live.commands.length;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await page.mouse.down({ button: first });
    await page.mouse.down({ button: second });
    await page.mouse.up({ button: first });
    await page.mouse.up({ button: second });
    await expect
      .poll(() =>
        live.commands
          .slice(before)
          .filter(
            (c) => c.event === "mousePressed" || c.event === "mouseReleased",
          )
          .map((c) => [c.event, c.button, c.buttons]),
      )
      .toEqual([
        ["mousePressed", first, first === "left" ? 1 : 2],
        ["mousePressed", second, 3],
        ["mouseReleased", first, second === "left" ? 1 : 2],
        ["mouseReleased", second, 0],
      ]);
  }
});

test("touch pan scrolls remotely, touch tap clicks, and mouse drag remains a drag", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const box = (await live.viewport.boundingBox())!;
  const x = box.x + box.width / 2;
  const startY = box.y + box.height * 0.72;
  await expect(live.viewport).toHaveCSS("font-size", "16px");
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
  await expect
    .poll(() =>
      live.commands
        .slice(beforeMouse)
        .some((command) => command.event === "mouseReleased"),
    )
    .toBe(true);
  const mouseDrag = live.commands.slice(beforeMouse);
  expect(mouseDrag.some((command) => command.event === "mousePressed")).toBe(
    true,
  );
  expect(mouseDrag.some((command) => command.event === "mouseReleased")).toBe(
    true,
  );
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
  }) => {
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
    const navigation = page.getByRole("form", { name: "Browser navigation" });
    const addressBox = (await navigation
      .getByRole("textbox", { name: "Browser address" })
      .boundingBox())!;
    const closeBox = (await navigation
      .getByRole("button", { name: "Close browser panel" })
      .boundingBox())!;
    expect(
      Math.abs(
        addressBox.y + addressBox.height / 2 - closeBox.y - closeBox.height / 2,
      ),
    ).toBeLessThan(1);
    expect(closeBox.width).toBeGreaterThanOrEqual(width < 768 ? 48 : 32);
    expect(closeBox.height).toBeGreaterThanOrEqual(width < 768 ? 48 : 32);
    await page.getByRole("button", { name: "Close browser panel" }).click();
    await expect(
      page.getByRole("textbox", { name: "Message", exact: true }),
    ).toHaveValue("Mobile draft");
  });
}

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

test("100% view keeps native size, scrolls without clipping, and maps input", async ({
  page,
}) => {
  const live = await liveBrowser(page);
  await live.open();
  live.newTab();
  await expect(live.viewport).toBeVisible();
  const fitted = await live.viewport.boundingBox();
  expect(fitted!.width).toBeLessThan(1280);
  const toggle = page.getByRole("button", { name: "Show browser at 100%" });
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect
    .poll(async () => (await live.viewport.boundingBox())!.width)
    .toBe(1280);
  const geometry = await live.viewport.evaluate((element) => {
    const host = element.parentElement!.parentElement!;
    const origin = element.getBoundingClientRect();
    const container = host.getBoundingClientRect();
    host.scrollLeft = 300;
    host.scrollTop = 100;
    return {
      left: origin.left - container.left,
      top: origin.top - container.top,
      scrollLeft: host.scrollLeft,
      scrollTop: host.scrollTop,
    };
  });
  expect(geometry.left).toBe(0);
  expect(geometry.top).toBeGreaterThanOrEqual(0);
  expect(geometry.scrollLeft).toBe(300);
  const box = (await live.viewport.boundingBox())!;
  await page.mouse.click(box.x + 400, box.y + geometry.scrollTop + 50);
  await expect
    .poll(() =>
      live.commands
        .filter(
          (command) =>
            command.type === "mouse" && command.event === "mousePressed",
        )
        .at(-1),
    )
    .toMatchObject({ x: 400, y: geometry.scrollTop + 50 });
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-pressed", "false");
  await expect
    .poll(async () => (await live.viewport.boundingBox())!.width)
    .toBe(fitted!.width);
});

test("fit view never magnifies a small source frame", async ({ page }) => {
  const live = await liveBrowser(page);
  await live.open();
  const id = live.newTab();
  await expect(live.viewport).toBeVisible();
  live.send({
    type: "frame",
    tab_id: id,
    data: "jpeg",
    width: 160,
    height: 90,
  });
  await expect
    .poll(async () => (await live.viewport.boundingBox())!.width)
    .toBe(160);
  await expect
    .poll(async () => (await live.viewport.boundingBox())!.height)
    .toBe(90);
});
