import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

function gate() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

for (const { mobile, method } of [
  { mobile: false, method: "Enter" },
  { mobile: true, method: "arrow" },
] as const) {
  test.describe(`send feedback ${mobile ? "mobile" : "desktop"}`, () => {
    test.use({
      viewport: { width: mobile ? 390 : 1440, height: 844 },
      isMobile: mobile,
      hasTouch: mobile,
    });
    test(`${method} shows the outgoing text before any network acknowledgement`, async ({
      page,
    }, testInfo) => {
      const { emit } = await workspace(page);
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      const composer = page.getByRole("textbox", {
        name: "Message",
        exact: true,
      });
      const preview = page.getByRole("region", { name: "Outgoing messages" });
      const posted = gate();
      const acknowledge = gate();
      const text = "Show my message immediately, even on a slow connection.";
      const queued = {
        id: "slow",
        text,
        status: "pending",
        created_at: new Date().toISOString(),
      };
      await page.route("**/v1/agents/a1/messages", async (route) => {
        expect(route.request().postDataJSON().text).toBe(text);
        posted.release();
        await acknowledge.promise;
        await route.fulfill({ json: queued });
      });
      const requests: string[] = [];
      page.on("request", (request) =>
        requests.push(`${request.method()} ${new URL(request.url()).pathname}`),
      );
      await composer.fill(text);
      if (method === "Enter") await composer.press("Enter");
      else
        await page
          .getByRole("button", { name: "Send message", exact: true })
          .click();
      await posted.promise;
      await expect(composer).toHaveValue("");
      await expect(preview).toHaveText(text);
      await expect(preview).toBeInViewport();
      await expect(composer).toBeInViewport();
      await expect(preview.getByRole("status")).toHaveCount(0);
      expect(requests).not.toContain("GET /v1/agents/a1");
      if (process.env.TED_WEB_RECORD) {
        const path = testInfo.outputPath("sending.png");
        await page.screenshot({ path });
        await testInfo.attach("Immediate outgoing message", {
          path,
          contentType: "image/png",
        });
      }
      await composer.fill("Keep typing the next message");
      const response = page.waitForResponse((response) =>
        response.url().endsWith("/v1/agents/a1/messages"),
      );
      acknowledge.release();
      await (await response).finished();
      await expect(preview).toHaveText(text);
      await expect(preview.getByRole("status")).toHaveCount(0);
      emit("a1", "message.queued", queued);
      await expect(preview).toHaveCount(0);
      await expect(
        page.getByRole("region", { name: "Pending messages" }),
      ).toContainText(text);
      emit("a1", "turn.started", { ...queued, status: "running" });
      await expect(
        page.getByRole("article", { name: "Your message", exact: true }),
      ).toHaveText(text);
      await expect(
        page.getByRole("region", { name: "Pending messages" }),
      ).toHaveCount(0);
      await expect(composer).toHaveValue("Keep typing the next message");
    });
  });
}

test("keeps the newest outgoing preview visible in the capped outbox", async ({
  page,
}) => {
  await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  let count = 0;
  await page.route("**/v1/agents/a1/messages", async (route) => {
    const text = route.request().postDataJSON().text;
    await route.fulfill({
      json: {
        id: `delayed-${++count}`,
        text,
        status: "pending",
        created_at: new Date().toISOString(),
      },
    });
  });
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  const preview = page.getByRole("region", { name: "Outgoing messages" });
  await composer.fill(Array(12).fill("Earlier outgoing content").join("\n"));
  await composer.press("Enter");
  await expect(preview.locator(":scope > div")).toHaveCount(1);
  await composer.fill("Newest outgoing message");
  const send = page.getByRole("button", {
    name: "Send message",
    exact: true,
  });
  await expect(send).toBeEnabled();
  await send.click();
  const newest = preview.getByText("Newest outgoing message", { exact: true });
  await expect(newest).toBeVisible();
  await expect
    .poll(() =>
      preview.evaluate((outbox) => {
        const items = [...outbox.children].map((item) =>
          item.getBoundingClientRect(),
        );
        const bounds = outbox.getBoundingClientRect();
        return {
          newestVisible: items.at(-1)!.bottom <= bounds.bottom + 1,
          messageGap: Math.round(items[1].top - items[0].bottom),
        };
      }),
    )
    .toEqual({ newestVisible: true, messageGap: 24 });
});

test("failed sends remove the preview, restore the draft, and reuse the retry key", async ({
  page,
}) => {
  const { emit } = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  const preview = page.getByRole("region", { name: "Outgoing messages" });
  const fail = gate();
  const keys: string[] = [];
  await page.route("**/v1/agents/a1/messages", async (route) => {
    keys.push(route.request().headers()["idempotency-key"]);
    if (keys.length === 1) {
      await fail.promise;
      await route.fulfill({
        status: 503,
        json: { error: { message: "Send unavailable" } },
      });
    } else {
      const queued = {
        id: "retry",
        text: route.request().postDataJSON().text,
        status: "pending",
        created_at: new Date().toISOString(),
      };
      emit("a1", "message.queued", queued);
      emit("a1", "turn.started", { ...queued, status: "running" });
      await route.fulfill({ json: queued });
    }
  });
  await composer.fill("//literal slash");
  await composer.press("Enter");
  await expect(preview).toContainText("/literal slash");
  fail.release();
  await expect(page.getByRole("alert")).toContainText("Send unavailable");
  await expect(preview).toHaveCount(0);
  await expect(composer).toHaveValue("//literal slash");
  await composer.press("Enter");
  await expect(
    page.getByRole("article", { name: "Your message", exact: true }),
  ).toHaveText("/literal slash");
  await expect(preview).toHaveCount(0);
  expect(keys).toHaveLength(2);
  expect(keys[0]).toBeTruthy();
  expect(keys[1]).toBe(keys[0]);
});

test("an in-flight preview stays with its chat and a failure preserves newer typing", async ({
  page,
}) => {
  await workspace(page);
  await page.goto("/?dialog=new-agent");
  const create = () =>
    page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
  await create();
  const fail = gate();
  await page.route("**/v1/agents/a1/messages", async (route) => {
    await fail.promise;
    await route.fulfill({
      status: 503,
      json: { error: { message: "Send unavailable" } },
    });
  });
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  const preview = page.getByRole("region", { name: "Outgoing messages" });
  await composer.fill("Old chat outgoing message");
  await composer.press("Enter");
  await expect(preview).toContainText("Old chat outgoing message");
  await composer.fill("Newer draft");
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await create();
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await expect(preview).toHaveCount(0);
  await composer.fill("Other chat draft");
  await page.locator('a[href="/agents/a1"]').click();
  await expect(preview).toContainText("Old chat outgoing message");
  await expect(composer).toHaveValue("Newer draft");
  await page.locator('a[href="/agents/a2"]').click();
  fail.release();
  await expect(composer).toHaveValue("Other chat draft");
  await expect(preview).toHaveCount(0);
  await page.locator('a[href="/agents/a1"]').click();
  await expect(page.getByRole("alert")).toContainText("Send unavailable");
  await expect(preview).toHaveCount(0);
  await expect(composer).toHaveValue("Newer draft");
});
