import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

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
  await expect(preview.locator(":scope > article")).toHaveCount(1);
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
