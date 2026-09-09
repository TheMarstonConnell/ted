import { test, expect, type Locator } from "@playwright/test";
import { openAgentSettings, workspace } from "./fixtures";

async function style(element: Locator, property: string) {
  return element.evaluate(
    (node, key) => getComputedStyle(node).getPropertyValue(key),
    property,
  );
}

for (const width of [320, 390, 768, 1440, 1920]) {
  for (const colorScheme of ["light", "dark"] as const) {
    test.describe(`grid ${width}px ${colorScheme}`, () => {
      test.use({
        viewport: { width, height: 960 },
        colorScheme,
        hasTouch: width < 768,
      });
      test("shared rhythm, typography, surfaces and responsive bounds", async ({
        page,
      }, testInfo) => {
        const { emit } = await workspace(page);
        await page.goto("/?dialog=new-agent");
        await page
          .getByRole("button", { name: "harness /srv/harness", exact: true })
          .click();
        emit("a1", "turn.started", {
          id: "grid",
          text: "Standardize the interface without changing its character.",
          status: "running",
          created_at: new Date().toISOString(),
        });
        emit("a1", "output", {
          ResponseType: "agent",
          Content:
            "## A consistent workspace\n\nKeep related content close, and give separate groups room to breathe.\n\n- Inter for prose and controls\n- **IBM Plex Mono** for code and paths\n\n```ts\nconst grid = 8;\nconst inset = grid * 3;\n```\n\n| Layer | Spacing |\n| --- | --- |\n| Controls | 8px |\n\n*Responsive by default.*",
          ToolName: "",
          ToolCallID: "",
          FullToolOutput: "",
        });
        emit("a1", "output", {
          ResponseType: "tool",
          Content: "git status --short",
          ToolName: "bash",
          ToolCallID: "grid-tool",
          FullToolOutput: "",
        });
        const input = page.getByRole("group", {
          name: "Message input",
          exact: true,
        });
        const toolbar = page.getByRole("group", {
          name: "Composer toolbar",
          exact: true,
        });
        const assistant = page.getByRole("article", {
          name: "Assistant message",
          exact: true,
        });
        const markdown = assistant.locator(".markdown");
        await expect(markdown).toBeVisible();
        // Messages contain only their body: no timestamp/copy footer or empty spacer.
        for (const name of ["Your message", "Assistant message"]) {
          const message = page.getByRole("article", { name, exact: true });
          await expect(message.locator("time")).toHaveCount(0);
          await expect(
            message.getByRole("button", { name: /copy/i }),
          ).toHaveCount(0);
          await expect(message.locator(":scope > *")).toHaveCount(1);
        }

        await page.evaluate(() => document.fonts.ready);
        expect(
          await page.evaluate(() =>
            document.fonts.check('14px "Inter Variable"'),
          ),
        ).toBe(true);
        expect(
          await page.evaluate(() =>
            document.fonts.check('12px "IBM Plex Mono"'),
          ),
        ).toBe(true);
        expect(await style(markdown, "font-family")).toContain(
          "Inter Variable",
        );
        await expect(markdown).toHaveCSS("font-size", "14px");
        await expect(markdown).toHaveCSS("line-height", "24px");
        expect(await style(assistant.locator("pre"), "font-family")).toContain(
          "IBM Plex Mono",
        );
        await expect(assistant.locator("pre")).toHaveCSS("line-height", "20px");
        await expect(input).toHaveCSS("border-radius", "24px");
        await expect(toolbar).toHaveCSS(
          "padding-left",
          width < 768 ? "16px" : "24px",
        );
        await expect(toolbar).toHaveCSS(
          "padding-bottom",
          width < 768 ? "16px" : "24px",
        );
        // Footer content follows the field's text inset, including its 1px border.
        const footer = page.getByRole("group", {
          name: "Composer footer",
          exact: true,
        });
        const context = footer.locator('[data-slot="context-usage"]');
        await expect(context).toHaveCSS("font-size", "12px");
        expect(await style(context, "font-family")).toContain("IBM Plex Mono");
        await expect(context).toHaveCSS(
          "color",
          await style(
            footer.getByRole("group", {
              name: "Project location",
              exact: true,
            }),
            "color",
          ),
        );
        expect(
          await context.evaluate(
            (node) =>
              node.tagName === "SPAN" &&
              node.tabIndex === -1 &&
              !node.hasAttribute("role"),
          ),
        ).toBe(true);
        await expect(
          footer.getByRole("button", { name: "View context", exact: true }),
        ).toHaveCount(0);
        await context.click();
        await expect(page.getByRole("dialog")).toHaveCount(0);
        const textBounds = await input
          .getByRole("textbox", { name: "Message", exact: true })
          .evaluate((node) => {
            const box = node.getBoundingClientRect();
            const css = getComputedStyle(node);
            return {
              left: box.left + parseFloat(css.paddingLeft),
              right: box.right - parseFloat(css.paddingRight),
            };
          });
        const footerBounds = await footer.evaluate((node) => {
          const box = node.getBoundingClientRect();
          const css = getComputedStyle(node);
          return {
            left:
              box.left +
              parseFloat(css.borderLeftWidth) +
              parseFloat(css.paddingLeft),
            right:
              box.right -
              parseFloat(css.borderRightWidth) -
              parseFloat(css.paddingRight),
          };
        });
        expect(footerBounds.left).toBeCloseTo(textBounds.left, 1);
        expect(footerBounds.right).toBeCloseTo(textBounds.right, 1);
        expect(
          (await footer
            .getByRole("group", { name: "Project location", exact: true })
            .boundingBox())!.x,
        ).toBeCloseTo(textBounds.left, 1);
        const sizes = await assistant
          .locator("*")
          .evaluateAll((nodes) => [
            ...new Set(nodes.map((n) => getComputedStyle(n).fontSize)),
          ]);
        const weights = await assistant
          .locator("*")
          .evaluateAll((nodes) => [
            ...new Set(nodes.map((n) => getComputedStyle(n).fontWeight)),
          ]);
        await expect(assistant.locator("th").first()).toHaveCSS(
          "font-weight",
          "600",
        );
        expect(sizes.length).toBeLessThanOrEqual(3);
        expect(weights.length).toBeLessThanOrEqual(3);
        // The unboxed assistant content and composer surface share a left edge.
        expect(
          Math.abs(
            (await assistant.boundingBox())!.x - (await input.boundingBox())!.x,
          ),
        ).toBeLessThan(1);
        expect((await input.boundingBox())!.width).toBeLessThanOrEqual(768);
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth),
        ).toBe(width);
        if (width < 768) {
          for (const button of await toolbar.locator("button").all()) {
            const box = (await button.boundingBox())!;
            expect(box.height).toBeGreaterThanOrEqual(48);
            expect(box.width).toBeGreaterThanOrEqual(48);
          }
        }
        const screen = testInfo.outputPath("chat-grid.png");
        await page.screenshot({ path: screen });
        await testInfo.attach("chat-grid", {
          path: screen,
          contentType: "image/png",
        });
        await openAgentSettings(page);
        const dialog = page.getByRole("dialog");
        await expect(dialog).toHaveCSS("padding", "24px");
        await expect(dialog).toHaveCSS("border-radius", "24px");
        await expect(dialog).toHaveCSS("row-gap", "24px");
        const box = (await dialog.boundingBox())!;
        expect(box.x).toBeGreaterThanOrEqual(16);
        expect(box.x + box.width).toBeLessThanOrEqual(width - 16);
        expect(
          await dialog.evaluate((n) => n.scrollWidth <= n.clientWidth),
        ).toBe(true);
        await dialog.getByLabel("Reasoning effort", { exact: true }).click();
        const option = page.getByRole("option", { name: "high", exact: true });
        await expect(option).toBeVisible();
        await expect(option).toHaveCSS("border-radius", "8px");
        await option.click();
        const settings = testInfo.outputPath("settings-grid.png");
        await page.screenshot({ path: settings });
        await testInfo.attach("settings-grid", {
          path: settings,
          contentType: "image/png",
        });
        await page.getByRole("button", { name: "Close", exact: true }).click();
        await expect(dialog).toHaveCount(0);
      });
    });
  }
}

test.describe("short mobile viewport", () => {
  test.use({ viewport: { width: 320, height: 568 }, hasTouch: true });
  test("settings, destructive actions and unavailable models remain contained", async ({
    page,
  }) => {
    await workspace(page);
    await page.route("**/v1/models", (route) => route.fulfill({ json: [] }));
    await page.goto("/projects/p?panel=settings");
    const dialog = page.getByRole("dialog");
    await expect(dialog).toBeVisible();
    await page
      .getByRole("button", { name: "Delete project", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Confirm delete", exact: true }),
    ).toBeVisible();
    expect(await dialog.evaluate((n) => n.scrollWidth <= n.clientWidth)).toBe(
      true,
    );
    const bounds = (await dialog.boundingBox())!;
    expect(bounds.y).toBeGreaterThanOrEqual(16);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(552);
    await page
      .getByRole("button", { name: "Keep project", exact: true })
      .click();
    await expect(
      page.getByRole("button", { name: "Confirm delete", exact: true }),
    ).toHaveCount(0);
    await expect(
      page.getByRole("button", { name: "reload", exact: true }),
    ).toBeVisible();
  });
});
