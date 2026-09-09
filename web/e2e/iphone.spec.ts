import { devices, expect, test } from "@playwright/test";
import { openProjectDefaults, workspace } from "./fixtures";

// Runs in Chromium as part of the normal suite, and in WebKit with test:iphone.
// This emulates an iPhone browser viewport, not the native iOS software keyboard.
test.use({ ...devices["iPhone 13"] });

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`iPhone readability ${colorScheme}`, () => {
    test.use({ colorScheme });
    test("selected chats, tool responses and short viewports remain usable", async ({
      page,
    }, testInfo) => {
      const { agents, emit } = await workspace(page);
      const output = (
        id: string,
        ResponseType: string,
        Content: string,
        ToolCallID = "",
      ) =>
        emit(id, "output", {
          ResponseType,
          Content,
          ToolCallID,
          ToolName: "bash",
          FullToolOutput: "",
        });
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .tap();
      agents.a1.title = "Selected chat";
      output("a1", "agent", "This is the current conversation.");
      await page.goto("/?dialog=new-agent");
      await page
        .getByRole("button", { name: "harness /srv/harness", exact: true })
        .tap();
      agents.a2.title = "Other chat";
      agents.a2.state = "running";
      output("a2", "agent", "Working on another task.");
      await page.goto("/agents/a1");
      const composer = page.getByRole("textbox", {
        name: "Message",
        exact: true,
      });
      await composer.fill("Keep my draft");
      await expect(composer).toHaveCSS("font-size", "16px");
      await page
        .getByRole("button", { name: "Open sidebar", exact: true })
        .tap();
      const drawer = page.getByRole("dialog", {
        name: "Workspace",
        exact: true,
      });
      const projectSettings = drawer.getByRole("button", {
        name: "Settings for harness",
        exact: true,
      });
      await expect(projectSettings).toHaveCSS("opacity", "1");
      await expect(projectSettings).toHaveCSS("width", "48px");
      expect(
        (await projectSettings.boundingBox())!.height,
      ).toBeGreaterThanOrEqual(48);
      const current = drawer.locator('[data-agent-id="a1"] a');
      const other = drawer.locator('[data-agent-id="a2"] a');
      await expect(current).toHaveAttribute("aria-current", "page");
      await expect(other).not.toHaveAttribute("aria-current", "page");
      await expect(other).toContainText("Running");
      await expect(drawer.locator('[data-slot="project-heading"]')).toHaveCSS(
        "font-weight",
        "600",
      );
      await expect(
        current.getByText("Selected chat", { exact: true }),
      ).toHaveCSS("font-weight", "600");
      await expect(other.getByText("Other chat", { exact: true })).toHaveCSS(
        "font-weight",
        "500",
      );
      expect(
        await current.evaluate((n) => getComputedStyle(n).backgroundColor),
      ).not.toBe(
        await other.evaluate((n) => getComputedStyle(n).backgroundColor),
      );
      // Verify the selected row's body and secondary metadata retain AA contrast
      // in both neutral themes, including alpha-composited branch text.
      for (const text of [
        current.getByText("Selected chat", { exact: true }),
        current.getByText("main", { exact: true }),
      ]) {
        const ratio = await text.evaluate((node) => {
          const canvas = document.createElement("canvas");
          canvas.width = canvas.height = 1;
          const ctx = canvas.getContext("2d")!;
          const bg = getComputedStyle(node.closest("a")!).backgroundColor;
          const fg = getComputedStyle(node).color;
          ctx.fillStyle = bg;
          ctx.fillRect(0, 0, 1, 1);
          const background = Array.from(
            ctx.getImageData(0, 0, 1, 1).data,
          ).slice(0, 3);
          ctx.fillStyle = fg;
          ctx.fillRect(0, 0, 1, 1);
          const foreground = Array.from(
            ctx.getImageData(0, 0, 1, 1).data,
          ).slice(0, 3);
          const luminance = (rgb: number[]) =>
            rgb
              .map((v) => {
                const s = v / 255;
                return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
              })
              .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i], 0);
          const a = luminance(background),
            b = luminance(foreground);
          return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
        });
        expect(ratio).toBeGreaterThanOrEqual(4.5);
      }
      const screenshot = testInfo.outputPath("iphone-sidebar.png");
      await page.screenshot({ path: screenshot });
      await testInfo.attach("iphone-sidebar", {
        path: screenshot,
        contentType: "image/png",
      });
      await current.tap();
      await expect(drawer).toHaveCount(0);
      await expect(composer).toHaveValue("Keep my draft");
      output("a1", "tool", "Check the files", "files");
      const tool = page.getByRole("button", {
        name: "Check the files",
        exact: true,
      });
      await expect(tool).toBeDisabled();
      await expect(tool.locator('[data-slot="tool-waiting"]')).toBeVisible();
      const pending = testInfo.outputPath("iphone-tool-waiting.png");
      await page.screenshot({ path: pending });
      await testInfo.attach("iphone-tool-waiting", {
        path: pending,
        contentType: "image/png",
      });
      output("a1", "tool_result", "README.md\nsrc/", "files");
      await expect(tool).toBeEnabled();
      expect((await tool.boundingBox())!.height).toBeGreaterThanOrEqual(48);
      await tool.tap();
      await expect(
        page.getByText("README.md\nsrc/", { exact: true }),
      ).toBeVisible();
      const result = testInfo.outputPath("iphone-tool-result.png");
      await page.screenshot({ path: result });
      await testInfo.attach("iphone-tool-result", {
        path: result,
        contentType: "image/png",
      });
      await tool.tap();
      await page.setViewportSize({ width: 320, height: 480 });
      await composer.fill("A longer draft\n".repeat(20));
      expect((await composer.boundingBox())!.height).toBeLessThanOrEqual(120);
      await expect(
        page.getByRole("button", { name: "Send message", exact: true }),
      ).toBeInViewport();
      await expect(
        page.getByRole("button", { name: "Open sidebar", exact: true }),
      ).toBeInViewport();
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBe(320);
      const short = testInfo.outputPath("iphone-short-viewport.png");
      await page.screenshot({ path: short });
      await testInfo.attach("iphone-short-viewport", {
        path: short,
        contentType: "image/png",
      });
      await openProjectDefaults(page);
      const dialog = page.getByRole("dialog", {
        name: "harness defaults",
        exact: true,
      });
      await dialog.getByRole("button", { name: "Close", exact: true }).tap();
      await expect(dialog).toHaveCount(0);
      await composer.tap();
      await expect(composer).toBeFocused();
      await expect(composer).toHaveValue("A longer draft\n".repeat(20));
    });
  });
}
