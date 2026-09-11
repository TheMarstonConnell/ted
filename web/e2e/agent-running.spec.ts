import { test, expect } from "@playwright/test";
import { workspace } from "./fixtures";

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(colorScheme, () => {
    test.use({ colorScheme });
    test("running rows animate, follow inventory changes and respect reduced motion", async ({
      page,
    }, testInfo) => {
      const { agents, events, emit } = await workspace(page);
      const states = [
        "running",
        "running",
        "idle",
        "stopping",
        "held",
        "settled",
      ];
      states.forEach((state, index) => {
        const id = `a${index + 1}`;
        agents[id] = {
          id,
          project_id: "p",
          title: [
            "Implement running highlight",
            "Review regression tests",
            "Plan next task",
            "Stop requested",
            "Waiting for approval",
            "Archived work",
          ][index],
          settings: { model: "test/model", effort: "medium" },
          state: state === "held" || state === "settled" ? "running" : state,
          held: state === "held",
          settled: state === "settled",
          cursor: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        events[id] = [];
      });
      await page.goto("/agents/a1");
      const active = page.locator('[data-agent-id="a1"] a');
      const other = page.locator('[data-agent-id="a2"] a');
      await expect(active).toHaveAttribute("aria-current", "page");
      await expect(page.locator(".agent-running")).toHaveCount(2);
      for (const link of [active, other]) {
        await expect(link.locator(".agent-running-label")).toHaveText(
          "Running",
        );
        expect(
          await link.evaluate(
            (el) =>
              getComputedStyle(
                el.querySelector(".agent-running-border")!,
                "::before",
              ).animationName,
          ),
        ).toBe("agent-border-beam");
        expect(
          await link.evaluate(
            (el) =>
              getComputedStyle(
                el.querySelector(".agent-running-border")!,
                "::before",
              ).animationDuration,
          ),
        ).toBe("3s");
        await expect(link.locator(".agent-running-label")).toHaveCSS(
          "animation-name",
          "agent-label-shimmer",
        );
      }
      expect(await active.evaluate((el) => getComputedStyle(el).color)).toBe(
        await other.evaluate((el) => getComputedStyle(el).color),
      );
      const sidebarBackground = await page
        .locator("aside > div")
        .evaluate((el) => getComputedStyle(el).backgroundColor);
      const selectedBackground =
        colorScheme === "dark" ? "oklch(0.38 0 0)" : sidebarBackground;
      const headingBackground =
        colorScheme === "dark" ? "oklch(0.29 0 0)" : sidebarBackground;
      await expect(active).toHaveCSS("background-color", selectedBackground);
      await active.hover();
      await expect(active).toHaveCSS("background-color", selectedBackground);
      const heading = page.locator('[data-project-id="p"] button').first();
      await expect(heading).toHaveCSS("background-color", headingBackground);
      for (const raised of [active, heading]) {
        expect(
          await raised.evaluate((el) => getComputedStyle(el).boxShadow),
        ).not.toBe("none");
        await expect(raised).toHaveClass(/shadow-sm/);
      }
      await expect(other).not.toHaveClass(/shadow-sm/);
      await heading.hover();
      await expect(heading).toHaveCSS("background-color", headingBackground);
      await page.mouse.move(500, 500);
      const initialDistance = await active.evaluate((el) =>
        getComputedStyle(
          el.querySelector(".agent-running-border")!,
          "::before",
        ).getPropertyValue("offset-distance"),
      );
      await expect
        .poll(() =>
          active.evaluate((el) =>
            getComputedStyle(
              el.querySelector(".agent-running-border")!,
              "::before",
            ).getPropertyValue("offset-distance"),
          ),
        )
        .not.toBe(initialDistance);
      await page.evaluate(() => document.fonts.ready);
      await page.screenshot({
        path: testInfo.outputPath(`running-${colorScheme}.png`),
      });
      if (process.env.TED_WEB_RECORD) await page.waitForTimeout(8500);
      await page.emulateMedia({ reducedMotion: "reduce" });
      expect(
        await active.evaluate(
          (el) =>
            getComputedStyle(
              el.querySelector(".agent-running-border")!,
              "::before",
            ).animationName,
        ),
      ).toBe("none");
      await expect(active.locator(".agent-running-label")).toHaveCSS(
        "animation-name",
        "none",
      );
      await expect(active.locator(".agent-running-label")).toHaveCSS(
        "-webkit-text-fill-color",
        await active.evaluate((el) => getComputedStyle(el).color),
      );
      await page.screenshot({
        path: testInfo.outputPath(`running-${colorScheme}-reduced-motion.png`),
      });
      for (const [patch, label] of [
        [{ state: "stopping" }, "Stopping"],
        [{ state: "idle" }, "idle"],
        [{ state: "running", held: true }, "Held"],
      ] as const) {
        Object.assign(agents.a1, patch);
        emit("a1", "agent.updated", {});
        // Wait for this inventory update, not an already-absent class from
        // the previous state, before publishing the next transition.
        await expect(active.getByText(label, { exact: true })).toBeAttached();
        await expect(active).not.toHaveClass(/agent-running/);
      }
      Object.assign(agents.a1, { held: false, settled: true });
      emit("a1", "agent.updated", {});
      // Settling moves the row into an unmounted, collapsed section.
      await expect(active).toHaveCount(0);
      await page
        .getByRole("button", { name: "Settled chats", exact: true })
        .click();
      await expect(active).toBeVisible();
      await expect(active.getByText("Settled", { exact: true })).toBeAttached();
      await expect(active).not.toHaveClass(/agent-running/);
      Object.assign(agents.a1, {
        state: "running",
        held: false,
        settled: false,
      });
      emit("a1", "agent.updated", {});
      await expect(active).toHaveClass(/agent-running/);
      await page.emulateMedia({ forcedColors: "active" });
      expect(
        await active.evaluate(
          (el) =>
            getComputedStyle(el.querySelector(".agent-running-border")!)
              .borderTopStyle,
        ),
      ).toBe("solid");
      await expect(active.locator(".agent-running-label")).toHaveCSS(
        "animation-name",
        "none",
      );
      await page.emulateMedia({ forcedColors: "none" });
      await page.emulateMedia({ reducedMotion: "no-preference" });
      expect(
        await active.evaluate(
          (el) =>
            getComputedStyle(
              el.querySelector(".agent-running-border")!,
              "::before",
            ).animationName,
        ),
      ).toBe("agent-border-beam");
    });
  });
}

test.describe("mobile", () => {
  test.use({ hasTouch: true });

  test("running highlight fits the mobile drawer without blocking navigation", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const { agents, emit } = await workspace(page);
    await page.goto("/?dialog=new-agent");
    await page
      .getByRole("button", { name: "harness /srv/harness", exact: true })
      .click();
    agents.a1.state = "running";
    emit("a1", "agent.updated", {});
    await page.goto("/agents/a1?sidebar=open");
    const drawer = page.getByRole("dialog", { name: "Workspace", exact: true });
    const link = drawer.locator(".agent-running");
    await expect(link).toBeVisible();
    await expect(link.locator(".agent-running-label")).toHaveText("Running");
    const bounds = (await link.boundingBox())!;
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(390);
    const settle = drawer.getByRole("button", { name: /^Settle chat:/ });
    await expect(settle).toBeVisible();
    await expect(settle).toHaveCSS("opacity", "1");
    await link.click();
    await expect(drawer).toHaveCount(0);
    await expect(page).toHaveURL(/\/agents\/a1$/);
  });
});
