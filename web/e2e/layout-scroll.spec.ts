import { expect, test } from "@playwright/test";
import { workspace } from "./fixtures";

for (const width of [390, 1440]) {
  test.describe(`viewport scrolling ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });
    test("long chats scroll internally without exposing space below the sidebar", async ({
      page,
    }, testInfo) => {
      const { emit } = await workspace(page);
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .click();
      for (let i = 0; i < 30; i++) {
        emit("a1", "output", {
          ResponseType: "agent",
          Content: `Message ${i}\n\n${"Long transcript content. ".repeat(40)}`,
          ToolName: "",
          ToolCallID: "",
          FullToolOutput: "",
        });
      }
      const messages = page.getByRole("region", {
        name: "Messages",
        exact: true,
      });
      await expect(
        page.getByRole("article", { name: "Assistant message", exact: true }),
      ).toHaveCount(30);
      await expect
        .poll(() => messages.evaluate((n) => n.scrollHeight > n.clientHeight))
        .toBe(true);
      expect(
        await page.evaluate(() => ({
          height: document.documentElement.scrollHeight,
          viewport: innerHeight,
        })),
      ).toEqual({ height: 844, viewport: 844 });
      await expect(page.locator("html")).toHaveCSS("overflow-y", "hidden");
      await expect(page.locator("body")).toHaveCSS("overflow-y", "hidden");
      await expect(page.locator("#root")).toHaveCSS("overflow-y", "hidden");
      await page.evaluate(() => window.scrollTo(0, 100000));
      expect(await page.evaluate(() => window.scrollY)).toBe(0);
      await messages.evaluate((n) => {
        n.scrollTop = 0;
      });
      await expect.poll(() => messages.evaluate((n) => n.scrollTop)).toBe(0);
      const screenshot = testInfo.outputPath("viewport-scroll.png");
      await page.screenshot({ path: screenshot });
      await testInfo.attach("viewport-scroll", {
        path: screenshot,
        contentType: "image/png",
      });
      if (width >= 768) {
        const sidebar = await page.locator("aside").boundingBox();
        expect(sidebar!.y).toBe(0);
        expect(sidebar!.height).toBe(844);
      }
    });
  });
}
