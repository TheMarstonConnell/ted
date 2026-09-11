import { expect, test, type Page, type Locator } from "@playwright/test";
import { workspace } from "./fixtures";

async function longChat(page: Page) {
  const fixture = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const output = (text: string, id = "a1") =>
    fixture.emit(id, "output", {
      ResponseType: "agent",
      Content: text,
      ToolName: "",
      ToolCallID: "",
      FullToolOutput: "",
    });
  for (let i = 0; i < 25; i++)
    output(
      `Message ${i}\n\n${"An explanation of the implementation. ".repeat(20)}`,
    );
  output(
    `A horizontally scrollable example\n\n\`\`\`text\n${"abcdefghij".repeat(150)}\n\`\`\``,
  );
  const viewport = page.getByRole("region", { name: "Messages", exact: true });
  const gap = () =>
    viewport.evaluate((n) => n.scrollHeight - n.scrollTop - n.clientHeight);
  await expect(
    page.getByRole("article", { name: "Assistant message", exact: true }),
  ).toHaveCount(26);
  await page.evaluate(() => document.fonts.ready);
  await expect.poll(gap).toBeLessThan(5);
  await expect(viewport).not.toHaveAttribute("data-autoscrolling", "");
  return {
    ...fixture,
    viewport,
    gap,
    output,
    composer: page.getByRole("textbox", { name: "Message", exact: true }),
    send: page.getByRole("button", { name: "Send message", exact: true }),
  };
}

async function readOlder(page: Page, viewport: Locator, mobile: boolean) {
  if (mobile) {
    // Playwright has no touch-pan API, and mobile WebKit rejects mouse wheels.
    // Replay the touch intent and resulting scroll position without pretending
    // this is a physical iPhone gesture or native keyboard test.
    await viewport.evaluate((node) => {
      // Keep intent and movement in one browser task. Separate protocol calls
      // allow a pending resize/frame to observe the old bottom position and
      // re-enable following between the gesture and its synthetic movement.
      node.dispatchEvent(
        new TouchEvent("touchmove", {
          bubbles: true,
          touches: [],
          changedTouches: [],
        }),
      );
      node.scrollTop = 0;
    });
  } else {
    await viewport.hover();
    await page.mouse.wheel(0, -100000);
  }
  await expect
    .poll(() => viewport.evaluate((n) => n.scrollTop))
    .toBeLessThan(5);
}

for (const mobile of [false, true]) {
  test.describe(`send scrolling ${mobile ? "iPhone-sized" : "desktop"}`, () => {
    test.use({
      viewport: { width: mobile ? 390 : 1440, height: 844 },
      isMobile: mobile,
      hasTouch: mobile,
    });
    test("sending resumes following after a gesture that leaves us at the bottom", async ({
      page,
    }, testInfo) => {
      const { viewport, gap, composer, send } = await longChat(page);
      await composer.fill("Send after a gesture at the bottom");
      await expect.poll(gap).toBeLessThan(5);
      const code = viewport.locator("pre").last();
      if (mobile) {
        // Replay the scroller's touch-move handler without a vertical scroll,
        // as happens with a horizontal gesture inside a nested code block.
        await code.dispatchEvent("touchmove", {
          touches: [],
          changedTouches: [],
        });
      } else {
        await code.hover();
        await page.mouse.wheel(200, 0);
        await expect
          .poll(() => code.evaluate((n) => n.scrollLeft))
          .toBeGreaterThan(0);
      }
      expect(await gap()).toBeLessThan(5);
      await send.click();
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toContainText("Send after a gesture at the bottom");
      await expect(composer).toHaveValue("");
      await expect.poll(gap).toBeLessThan(5);
      const path = testInfo.outputPath("send-at-bottom.png");
      await page.screenshot({ path });
      await testInfo.attach("send-at-bottom", {
        path,
        contentType: "image/png",
      });
    });

    test("sending a tall draft stays at the bottom as the composer shrinks", async ({
      page,
    }) => {
      const { gap, composer } = await longChat(page);
      const text = "A line in a long draft\n".repeat(20);
      await composer.fill(text);
      await expect.poll(gap).toBeLessThan(5);
      const before = (await composer.boundingBox())!.height;
      await composer.press("Enter");
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toContainText("A line in a long draft");
      await expect(composer).toHaveValue("");
      await expect
        .poll(async () => (await composer.boundingBox())!.height)
        .toBeLessThan(before);
      await expect.poll(gap).toBeLessThan(5);
    });

    test("a queued send resumes following before the turn starts, then respects reading again", async ({
      page,
    }) => {
      const { emit, output, viewport, gap, composer, send } =
        await longChat(page);
      let queued: {
        id: string;
        text: string;
        status: string;
        created_at: string;
      };
      await page.route("**/v1/agents/a1/messages", async (route) => {
        queued = {
          id: "queued-later",
          text: route.request().postDataJSON().text,
          status: "pending",
          created_at: new Date().toISOString(),
        };
        emit("a1", "message.queued", queued);
        await route.fulfill({ json: queued });
      });
      await composer.fill("Follow my queued turn");
      await readOlder(page, viewport, mobile);
      await send.click();
      await expect(
        page.getByText("Up next · 1 pending", { exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toHaveCount(0);
      await expect.poll(gap).toBeLessThan(5);
      emit("a1", "turn.started", { ...queued!, status: "running" });
      output("The queued reply arrived later. " + "More output. ".repeat(100));
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toHaveText("Follow my queued turn");
      await expect.poll(gap).toBeLessThan(5);
      await readOlder(page, viewport, mobile);
      output("Passive output must not pull a reader back down.");
      await expect(viewport).toContainText(
        "Passive output must not pull a reader back down.",
      );
      await page.evaluate(
        () =>
          new Promise((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(resolve)),
          ),
      );
      expect(await viewport.evaluate((n) => n.scrollTop)).toBeLessThan(5);
    });

    test("reading during a slow send takes precedence over its late acknowledgement", async ({
      page,
    }) => {
      const { emit, viewport, composer, send } = await longChat(page);
      let release!: () => void;
      const held = new Promise<void>((resolve) => {
        release = resolve;
      });
      let posted!: () => void;
      const requestSeen = new Promise<void>((resolve) => {
        posted = resolve;
      });
      await page.route("**/v1/agents/a1/messages", async (route) => {
        const queued = {
          id: "slow",
          text: route.request().postDataJSON().text,
          status: "pending",
          created_at: new Date().toISOString(),
        };
        posted();
        await held;
        emit("a1", "message.queued", queued);
        emit("a1", "turn.started", { ...queued, status: "running" });
        await route.fulfill({ json: queued });
      });
      await composer.fill("A slow send");
      await send.click();
      await requestSeen;
      await readOlder(page, viewport, mobile);
      release();
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toHaveText("A slow send");
      await expect(
        page
          .getByRole("group", { name: "Chat settings", exact: true })
          .getByLabel("Model", { exact: true }),
      ).toBeEnabled();
      await page.evaluate(
        () =>
          new Promise((resolve) =>
            requestAnimationFrame(() => requestAnimationFrame(resolve)),
          ),
      );
      expect(await viewport.evaluate((n) => n.scrollTop)).toBeLessThan(5);
    });

    test("a failed send restores the draft without leaving the reading position", async ({
      page,
    }) => {
      const { viewport, composer, send } = await longChat(page);
      await page.route("**/v1/agents/a1/messages", (route) =>
        route.fulfill({
          status: 503,
          json: { error: { message: "Send unavailable" } },
        }),
      );
      await composer.fill("Keep this failed draft");
      await readOlder(page, viewport, mobile);
      await send.click();
      await expect(page.getByRole("alert")).toContainText("Send unavailable");
      await expect(composer).toHaveValue("Keep this failed draft");
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toHaveCount(0);
      expect(await viewport.evaluate((n) => n.scrollTop)).toBeLessThan(5);
    });
  });
}

test("a late send acknowledgement cannot scroll another chat", async ({
  page,
}) => {
  const { emit, output, viewport, composer, send } = await longChat(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let posted!: () => void;
  const requestSeen = new Promise<void>((resolve) => {
    posted = resolve;
  });
  await page.route("**/v1/agents/a1/messages", async (route) => {
    const queued = {
      id: "old-chat-send",
      text: route.request().postDataJSON().text,
      status: "pending",
      created_at: new Date().toISOString(),
    };
    posted();
    await held;
    emit("a1", "message.queued", queued);
    emit("a1", "turn.started", { ...queued, status: "running" });
    await route.fulfill({ json: queued });
  });
  await composer.fill("Send in the old chat");
  await send.click();
  await requestSeen;
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  await expect(page).toHaveURL(/\/agents\/a2$/);
  for (let i = 0; i < 25; i++)
    output(`Another chat ${i}\n\n${"Long reply. ".repeat(100)}`, "a2");
  await expect(
    page.getByRole("article", { name: "Assistant message", exact: true }),
  ).toHaveCount(25);
  await composer.fill("Keep the new chat draft");
  await viewport.hover();
  await page.mouse.wheel(0, -100000);
  await expect
    .poll(() => viewport.evaluate((n) => n.scrollTop))
    .toBeLessThan(5);
  const acknowledged = page.waitForResponse(
    (r) => r.url().endsWith("/a1/messages") && r.request().method() === "POST",
  );
  release();
  await (await acknowledged).finished();
  await page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await expect(composer).toHaveValue("Keep the new chat draft");
  expect(await viewport.evaluate((n) => n.scrollTop)).toBeLessThan(5);
});
