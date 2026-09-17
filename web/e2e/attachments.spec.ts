import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { expect, test, type Page, type TestInfo } from "@playwright/test";
import { workspace } from "./fixtures";

test.use({
  video: {
    mode: process.env.TED_WEB_RECORD ? "on" : "retain-on-failure",
    // Record desktop with TED_WEB_RECORD=1 and the 390px workflow with
    // TED_WEB_RECORD=mobile, avoiding Playwright's default 800px downscale.
    size:
      process.env.TED_WEB_RECORD === "mobile"
        ? { width: 390, height: 844 }
        : { width: 1440, height: 960 },
  },
});

// Real, decodable screenshots keep previews/lightboxes meaningful without an
// external image host. The workspace fixture never calls a paid provider.
const screenshotPath = fileURLToPath(
  new URL("../../docs/screenshots/web-composer.png", import.meta.url),
);
const mobilePath = fileURLToPath(
  new URL("../../docs/screenshots/web-composer-mobile.png", import.meta.url),
);
const image = (
  name = "composer.png",
  buffer = readFileSync(screenshotPath),
) => ({ name, mimeType: "image/png", buffer });
const attachment = (file = image()) => ({
  name: file.name,
  url: `data:${file.mimeType};base64,${file.buffer.toString("base64")}`,
});
const input = (page: Page) => page.getByLabel("Attach images", { exact: true });
const previews = (page: Page) =>
  page.getByLabel("Attached images", { exact: true });
const composer = (page: Page) =>
  page.getByRole("textbox", { name: "Message", exact: true });
const send = (page: Page) =>
  page.getByRole("button", { name: "Send message", exact: true });

const userMessages = (page: Page) =>
  page
    .getByRole("region", { name: "Messages", exact: true })
    .getByRole("article", { name: "Your message", exact: true });

async function start(page: Page) {
  const fixture = await workspace(page);
  await page.goto("/?dialog=new-agent");
  await hold(page);
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  await expect(page).toHaveURL(/\/agents\/a1$/);
  await expect(composer(page)).toBeEditable();
  return fixture;
}
function submissions(page: Page) {
  const requests: {
    body: { text: string; attachments?: { name: string; url: string }[] };
    key: string;
  }[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      /\/agents\/[^/]+\/messages$/.test(request.url())
    )
      requests.push({
        body: request.postDataJSON(),
        key: request.headers()["idempotency-key"],
      });
  });
  return requests;
}
async function transfer(page: Page, kind: "paste" | "drop", files = [image()]) {
  // Replay browser File/DataTransfer events through React handlers; this is
  // deliberately not a claim of native OS clipboard integration coverage.
  await page.getByRole("form", { name: "Message composer" }).evaluate(
    (form, { kind, files }) => {
      const data = new DataTransfer();
      for (const file of files) {
        const bytes = Uint8Array.from(atob(file.base64), (c) =>
          c.charCodeAt(0),
        );
        data.items.add(new File([bytes], file.name, { type: file.mimeType }));
      }
      if (kind === "paste")
        form.querySelector("textarea")!.dispatchEvent(
          new ClipboardEvent("paste", {
            bubbles: true,
            cancelable: true,
            clipboardData: data,
          }),
        );
      else {
        form.dispatchEvent(
          new DragEvent("dragover", {
            bubbles: true,
            cancelable: true,
            dataTransfer: data,
          }),
        );
        form.dispatchEvent(
          new DragEvent("drop", {
            bubbles: true,
            cancelable: true,
            dataTransfer: data,
          }),
        );
      }
    },
    {
      kind,
      files: files.map(({ buffer, ...file }) => ({
        ...file,
        base64: buffer.toString("base64"),
      })),
    },
  );
}
async function capture(page: Page, info: TestInfo, name: string) {
  await page.evaluate(() => document.fonts.ready);
  const path = info.outputPath(`${name}.png`);
  await page.screenshot({ path });
  await info.attach(name, { path, contentType: "image/png" });
}
// Only evidence recording needs deliberate visible holds.
const hold = (page: Page, ms = 1200) =>
  process.env.TED_WEB_RECORD ? page.waitForTimeout(ms) : Promise.resolve();

test("selects, removes, reselects, and sends screenshots with text; transcript opens a lightbox", async ({
  page,
}, info) => {
  const fixture = await start(page);
  fixture.updateAgent("a1", { title: "Review screenshot attachments" });
  const requests = submissions(page);
  await expect(input(page)).toBeHidden();
  await expect(input(page)).toHaveAttribute(
    "accept",
    "image/png,image/jpeg,image/webp,image/gif",
  );
  await hold(page);
  const picker = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Attach screenshots" }).click();
  const chooser = await picker;
  expect(chooser.isMultiple()).toBe(true);
  await chooser.setFiles([
    image(),
    image("mobile.png", readFileSync(mobilePath)),
  ]);
  await expect(previews(page).getByRole("img")).toHaveCount(2);
  await hold(page);
  await composer(page).fill(
    "Compare the desktop and mobile composer. Keep the controls easy to reach.",
  );
  await hold(page, 2000);
  await capture(page, info, "attachments-desktop-draft");
  await page
    .getByRole("button", { name: "Remove mobile.png", exact: true })
    .click();
  await expect(previews(page).getByRole("img")).toHaveCount(1);
  await hold(page);
  await input(page).setInputFiles(
    image("mobile.png", readFileSync(mobilePath)),
  );
  await expect(previews(page).getByRole("img")).toHaveCount(2);
  await hold(page);
  await send(page).click();
  const message = userMessages(page);
  await expect(message).toContainText(
    "Compare the desktop and mobile composer.",
  );
  await expect(message.getByRole("img")).toHaveCount(2);
  await expect(previews(page)).toHaveCount(0);
  await expect(composer(page)).toHaveValue("");
  expect(requests).toHaveLength(1);
  expect(requests[0].body.attachments).toEqual([
    attachment(),
    attachment(image("mobile.png", readFileSync(mobilePath))),
  ]);
  await hold(page, 2000);
  await capture(page, info, "attachments-desktop-sent");
  await message
    .getByRole("button", { name: "Open image: composer.png", exact: true })
    .click();
  const lightbox = page.getByRole("dialog", { name: "Image preview" });
  await expect(lightbox.getByRole("img")).toHaveAttribute(
    "src",
    attachment().url,
  );
  await expect(lightbox.getByRole("img")).toBeVisible();
  await hold(page, 2200);
  await capture(page, info, "attachments-desktop-lightbox");
  await page.keyboard.press("Escape");
  await expect(lightbox).toHaveCount(0);
  await expect(
    message.getByRole("button", {
      name: "Open image: composer.png",
      exact: true,
    }),
  ).toBeFocused();
  await hold(page, 2000);
});

for (const kind of ["paste", "drop"] as const) {
  test(`${kind} appends screenshots and sends an image-only message`, async ({
    page,
  }) => {
    await start(page);
    const requests = submissions(page);
    await expect(send(page)).toBeDisabled();
    await input(page).setInputFiles(image("first.png"));
    await transfer(page, kind, [image("second.png")]);
    await expect(previews(page).getByRole("img")).toHaveCount(2);
    await expect(composer(page)).toHaveValue("");
    await expect(send(page)).toBeEnabled();
    if (kind === "paste") await composer(page).press("Enter");
    else await send(page).click();
    await expect(userMessages(page).getByRole("img")).toHaveCount(2);
    expect(requests[0].body).toEqual({
      text: "",
      attachments: [
        attachment(image("first.png")),
        attachment(image("second.png")),
      ],
    });
    await expect(previews(page)).toHaveCount(0);
  });
}

test("removing the last screenshot disables an empty send and leaves text-only sends unchanged", async ({
  page,
}) => {
  await start(page);
  const requests = submissions(page);
  await input(page).setInputFiles(image());
  await page
    .getByRole("button", { name: "Remove composer.png", exact: true })
    .click();
  await expect(previews(page)).toHaveCount(0);
  await expect(send(page)).toBeDisabled();
  await composer(page).fill("No screenshot needed");
  await send(page).click();
  await expect(userMessages(page)).toHaveText("No screenshot needed");
  expect(requests[0].body).toEqual({ text: "No screenshot needed" });
});

test("draft attachments stay with their chat when switching away and back", async ({
  page,
}) => {
  await start(page);
  await input(page).setInputFiles(image("first-chat.png"));
  await composer(page).fill("First chat draft");
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await expect(previews(page)).toHaveCount(0);
  await input(page).setInputFiles(image("second-chat.png"));
  await composer(page).fill("Second chat draft");
  await page.locator('a[href="/agents/a1"]').click();
  await expect(composer(page)).toHaveValue("First chat draft");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "first-chat.png",
  );
  await page.locator('a[href="/agents/a2"]').click();
  await expect(composer(page)).toHaveValue("Second chat draft");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "second-chat.png",
  );
});

test("failed sends restore images and retry identity; changing images creates a new identity", async ({
  page,
}) => {
  await start(page);
  const requests = submissions(page);
  await page.route("**/v1/agents/a1/messages", (route) =>
    route.fulfill({
      status: 503,
      json: { error: { message: "Image send unavailable" } },
    }),
  );
  await input(page).setInputFiles(image());
  await composer(page).fill("Keep this screenshot");
  for (let i = 0; i < 2; i++) {
    await send(page).click();
    await expect(page.getByRole("alert")).toContainText(
      "Image send unavailable",
    );
    await expect(composer(page)).toHaveValue("Keep this screenshot");
    await expect(previews(page).getByRole("img")).toHaveAttribute(
      "src",
      attachment().url,
    );
  }
  expect(requests).toHaveLength(2);
  expect(requests[0].key).toBeTruthy();
  expect(requests[1]).toEqual(requests[0]);
  await page
    .getByRole("button", { name: "Remove composer.png", exact: true })
    .click();
  // Same filename, different bytes must be a different submission too.
  const replacement = image("composer.png", readFileSync(mobilePath));
  await input(page).setInputFiles(replacement);
  await page.unroute("**/v1/agents/a1/messages");
  await send(page).click();
  await expect(userMessages(page).getByRole("img")).toHaveAttribute(
    "src",
    attachment(replacement).url,
  );
  expect(requests).toHaveLength(3);
  expect(requests[2].key).not.toBe(requests[0].key);
  expect(requests[2].body.attachments).toEqual([attachment(replacement)]);
});

test("optimistic outgoing images open a lightbox and a late failure cannot overwrite a newer image draft", async ({
  page,
}) => {
  await start(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v1/agents/a1/messages", async (route) => {
    await held;
    await route.fulfill({
      status: 503,
      json: { error: { message: "Delayed failure" } },
    });
  });
  await input(page).setInputFiles(image("submitted.png"));
  await send(page).click();
  const outgoing = page.getByRole("region", { name: "Outgoing messages" });
  await expect(outgoing.getByRole("img")).toHaveAttribute(
    "alt",
    "submitted.png",
  );
  await outgoing
    .getByRole("button", { name: "Open image: submitted.png", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Image preview" }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await input(page).setInputFiles(image("new-draft.png"));
  await composer(page).fill("A newer draft");
  release();
  await expect(page.getByRole("alert")).toContainText("Delayed failure");
  await expect(outgoing).toHaveCount(0);
  await expect(composer(page)).toHaveValue("A newer draft");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "new-draft.png",
  );
});

test("queued Edit restores screenshots and escapes a literal slash before resending", async ({
  page,
}) => {
  const { emit, events } = await start(page);
  const requests = submissions(page);
  emit("a1", "message.queued", {
    id: "edit-image",
    text: "/help with this screenshot",
    attachments: [attachment()],
    status: "pending",
    created_at: new Date().toISOString(),
  });
  const row = page.locator('[data-pending-message-id="edit-image"]');
  await expect(row).toContainText("1 image");
  await row.getByRole("button", { name: "Edit", exact: true }).click();
  await expect(row).toHaveCount(0);
  await expect(composer(page)).toHaveValue("//help with this screenshot");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "src",
    attachment().url,
  );
  expect(
    events.a1.find((event) => event.type === "message.cancelled").data
      .attachments,
  ).toEqual([attachment()]);
  await send(page).click();
  await expect(userMessages(page)).toContainText("/help with this screenshot");
  expect(requests[0].body).toEqual({
    text: "/help with this screenshot",
    attachments: [attachment()],
  });
});

test("slash commands reject images without clearing the draft or making a request", async ({
  page,
}) => {
  await start(page);
  const requests = submissions(page);
  await input(page).setInputFiles(image());
  await composer(page).fill("/help");
  await send(page).click();
  await expect(page.getByRole("alert")).toContainText("not a slash command");
  await expect(composer(page)).toHaveValue("/help");
  await expect(previews(page).getByRole("img")).toHaveCount(1);
  expect(requests).toEqual([]);
  await composer(page).fill("//help");
  await send(page).click();
  await expect(userMessages(page)).toContainText("/help");
  await expect(
    page.getByRole("region", { name: "Outgoing messages" }),
  ).toHaveCount(0);
  expect(requests[0].body).toEqual({
    text: "/help",
    attachments: [attachment()],
  });
});

for (const invalid of [
  {
    name: "notes.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("not an image"),
    error: "Choose PNG, JPEG, WebP, or GIF",
  },
  {
    name: "vector.svg",
    mimeType: "image/svg+xml",
    buffer: Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"/>'),
    error: "Choose PNG, JPEG, WebP, or GIF",
  },
  {
    name: "empty.png",
    mimeType: "image/png",
    buffer: Buffer.alloc(0),
    error: "nonempty",
  },
  {
    name: "too-large.png",
    mimeType: "image/png",
    buffer: Buffer.alloc(5 * 1024 * 1024 + 1),
    error: "5 MiB",
  },
]) {
  test(`rejects ${invalid.name} without losing a valid screenshot`, async ({
    page,
  }) => {
    await start(page);
    await input(page).setInputFiles(image());
    const { error, ...file } = invalid;
    await input(page).setInputFiles(file);
    await expect(page.getByRole("alert")).toContainText(error);
    await expect(previews(page).getByRole("img")).toHaveCount(1);
    await expect(previews(page).getByRole("img")).toHaveAttribute(
      "alt",
      "composer.png",
    );
    await expect(send(page)).toBeEnabled();
  });
}

test("accepts four images, rejects a fifth atomically, and permits replacement after removal", async ({
  page,
}) => {
  await start(page);
  await input(page).setInputFiles([1, 2, 3, 4].map((n) => image(`${n}.png`)));
  await expect(previews(page).getByRole("img")).toHaveCount(4);
  await transfer(page, "drop", [image("fifth.png")]);
  await expect(page.getByRole("alert")).toContainText("up to 4 images");
  await expect(previews(page).getByRole("img")).toHaveCount(4);
  await page.getByRole("button", { name: "Remove 2.png", exact: true }).click();
  await input(page).setInputFiles(image("replacement.png"));
  await expect(previews(page).getByRole("img")).toHaveCount(4);
  await expect(
    previews(page).getByRole("img", { name: "replacement.png", exact: true }),
  ).toBeVisible();
});

for (const width of [320, 390]) {
  test.describe(`attachment layout at ${width}px`, () => {
    test.use({
      viewport: { width, height: 844 },
      isMobile: true,
      hasTouch: true,
    });
    test("wraps four previews without overflow and keeps mobile controls reachable", async ({
      page,
    }, info) => {
      await start(page);
      await hold(page);
      await input(page).setInputFiles(
        [1, 2, 3, 4].map((n) =>
          image(`mobile-layout-review-${n}.png`, readFileSync(mobilePath)),
        ),
      );
      await expect(previews(page).getByRole("img")).toHaveCount(4);
      await hold(page);
      await composer(page).fill("Review these mobile layouts.");
      await hold(page, 2000);
      await expect
        .poll(() =>
          page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        )
        .toBe(true);
      for (const control of [
        page.getByRole("button", { name: "Attach screenshots" }),
        send(page),
        page.getByRole("button", {
          name: "Remove mobile-layout-review-1.png",
          exact: true,
        }),
      ]) {
        const bounds = (await control.boundingBox())!;
        expect(bounds.width).toBeGreaterThanOrEqual(48);
        expect(bounds.height).toBeGreaterThanOrEqual(48);
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
        expect(bounds.y + bounds.height).toBeLessThanOrEqual(844);
      }
      const previewBounds = (await previews(page).boundingBox())!;
      const fieldBounds = (await composer(page).boundingBox())!;
      expect(previewBounds.y + previewBounds.height).toBeLessThanOrEqual(
        fieldBounds.y + 1,
      );
      await capture(page, info, `attachments-mobile-${width}`);
      await send(page).click();
      const message = userMessages(page);
      await expect(message.getByRole("img")).toHaveCount(4);
      await hold(page, 1800);
      await message
        .getByRole("button", {
          name: "Open image: mobile-layout-review-4.png",
          exact: true,
        })
        .click();
      const lightbox = page.getByRole("dialog", { name: "Image preview" });
      await expect(lightbox).toBeVisible();
      const bounds = (await lightbox.getByRole("img").boundingBox())!;
      expect(bounds.x).toBeGreaterThanOrEqual(0);
      expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
      await hold(page, 2000);
      await capture(page, info, `attachments-mobile-lightbox-${width}`);
      await page.getByRole("button", { name: "Close image preview" }).click();
      await expect(lightbox).toHaveCount(0);
      await hold(page, 2000);
    });
  });
}

test("accepts real PNG, JPEG, WebP and GIF bytes and preserves MIME types in the submitted data URLs", async ({
  page,
}) => {
  await start(page);
  const requests = submissions(page);
  const encoded = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 32;
    const context = canvas.getContext("2d")!;
    context.fillStyle = "#27374d";
    context.fillRect(0, 0, 32, 32);
    return ["image/jpeg", "image/webp"].map((mimeType) => ({
      mimeType,
      url: canvas.toDataURL(mimeType),
    }));
  });
  const files = [
    image(),
    ...encoded.map(({ mimeType, url }) => ({
      name: `sample.${mimeType.split("/")[1]}`,
      mimeType,
      buffer: Buffer.from(url.split(",")[1], "base64"),
    })),
    {
      name: "sample.gif",
      mimeType: "image/gif",
      buffer: Buffer.from(
        "R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7",
        "base64",
      ),
    },
  ];
  await input(page).setInputFiles(files);
  await expect(previews(page).getByRole("img")).toHaveCount(4);
  await expect
    .poll(() =>
      previews(page)
        .getByRole("img")
        .evaluateAll((images) =>
          images.every((node) => (node as HTMLImageElement).naturalWidth > 0),
        ),
    )
    .toBe(true);
  await send(page).click();
  await expect(userMessages(page).getByRole("img")).toHaveCount(4);
  expect(requests[0].body.attachments).toEqual(files.map(attachment));
});

test("accepts an image exactly at the 5 MiB boundary", async ({ page }) => {
  await start(page);
  // PNG decoders permit trailing bytes; retain a real PNG header/content rather
  // than disguising an all-zero buffer as an image.
  const buffer = Buffer.alloc(5 * 1024 * 1024);
  readFileSync(screenshotPath).copy(buffer);
  await input(page).setInputFiles(image("exact-limit.png", buffer));
  await expect(previews(page).getByRole("img")).toHaveCount(1);
  await expect
    .poll(() =>
      previews(page)
        .getByRole("img")
        .evaluate((node) => (node as HTMLImageElement).naturalWidth),
    )
    .toBeGreaterThan(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(send(page)).toBeEnabled();
});

test("editing an image-only queue item asks before replacing an image-only draft", async ({
  page,
}) => {
  const { emit } = await start(page);
  emit("a1", "message.queued", {
    id: "image-only",
    text: "",
    attachments: [attachment(image("queued.png"))],
    status: "pending",
    created_at: new Date().toISOString(),
  });
  await input(page).setInputFiles(image("draft.png"));
  const row = page.locator('[data-pending-message-id="image-only"]');
  await row.getByRole("button", { name: "Edit", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Replace current draft?" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Keep draft", exact: true }).click();
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "draft.png",
  );
  await expect(row).toBeVisible();
  await row.getByRole("button", { name: "Edit", exact: true }).click();
  await dialog
    .getByRole("button", { name: "Edit message", exact: true })
    .click();
  await expect(row).toHaveCount(0);
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "queued.png",
  );
  await expect(composer(page)).toHaveValue("");
  await expect(send(page)).toBeEnabled();
});

test("a failure arriving after a chat switch restores only the original chat's screenshot", async ({
  page,
}) => {
  await start(page);
  let release!: () => void;
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/v1/agents/a1/messages", async (route) => {
    await held;
    await route.fulfill({
      status: 503,
      json: { error: { message: "Original chat failed" } },
    });
  });
  await input(page).setInputFiles(image("original.png"));
  await composer(page).fill("Original chat message");
  await send(page).click();
  await expect(
    page.getByRole("region", { name: "Outgoing messages" }).getByRole("img"),
  ).toHaveAttribute("alt", "original.png");
  await page.getByRole("button", { name: "New chat", exact: true }).click();
  await page
    .getByRole("button", { name: "harness /srv/harness", exact: true })
    .click();
  await expect(page).toHaveURL(/\/agents\/a2$/);
  await expect(
    page.getByRole("region", { name: "Outgoing messages" }),
  ).toHaveCount(0);
  // URL changes before React commits the new keyed chat. Use the visible
  // picker action rather than racing a hidden input from the previous render.
  const picker = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Attach screenshots" }).click();
  await (await picker).setFiles(image("other-chat.png"));
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "other-chat.png",
  );
  await composer(page).fill("Keep this other chat draft");
  const response = page.waitForResponse(
    (r) => r.url().endsWith("/a1/messages") && r.request().method() === "POST",
  );
  release();
  await (await response).finished();
  await expect(composer(page)).toHaveValue("Keep this other chat draft");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "other-chat.png",
  );
  await page.locator('a[href="/agents/a1"]').click();
  await expect(composer(page)).toHaveValue("Original chat message");
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "original.png",
  );
});

test("an unchanged restored image-only draft retries successfully with the original idempotency key", async ({
  page,
}) => {
  await start(page);
  const requests = submissions(page);
  let attempts = 0;
  await page.route("**/v1/agents/a1/messages", async (route) => {
    if (++attempts === 1) {
      await route.fulfill({
        status: 503,
        json: { error: { message: "Try this screenshot again" } },
      });
    } else await route.fallback();
  });
  await input(page).setInputFiles(image());
  await send(page).click();
  await expect(page.getByRole("alert")).toContainText(
    "Try this screenshot again",
  );
  await expect(previews(page).getByRole("img")).toHaveAttribute(
    "alt",
    "composer.png",
  );
  await expect(composer(page)).toHaveValue("");
  await send(page).click();
  const messages = userMessages(page);
  await expect(messages).toHaveCount(1);
  await expect(messages.getByRole("img")).toHaveAttribute(
    "src",
    attachment().url,
  );
  await expect(previews(page)).toHaveCount(0);
  await expect(send(page)).toBeDisabled();
  expect(requests).toHaveLength(2);
  expect(requests[0].key).toBeTruthy();
  expect(requests[1]).toEqual(requests[0]);
});

test("keyboard removal of draft images restores focus to the composer", async ({
  page,
}, info) => {
  await start(page);
  await hold(page);
  const requests = submissions(page);
  await composer(page).fill("Keep this draft while removing screenshots.");
  await hold(page);
  await input(page).setInputFiles([
    image("first.png"),
    image("middle.png"),
    image("last.png"),
    image("final.png"),
  ]);
  await expect(previews(page).getByRole("img")).toHaveCount(4);
  await hold(page);
  for (const [index, name] of [
    "middle.png",
    "first.png",
    "final.png",
    "last.png",
  ].entries()) {
    const remove = page.getByRole("button", {
      name: `Remove ${name}`,
      exact: true,
    });
    await remove.focus();
    await expect(remove).toBeFocused();
    await hold(page);
    await remove.press(index % 2 ? "Space" : "Enter");
    await expect(composer(page)).toBeFocused();
    await expect(previews(page).getByRole("img")).toHaveCount(3 - index);
    await expect(composer(page)).toHaveValue(
      "Keep this draft while removing screenshots.",
    );
    await hold(page);
    if (index === 0)
      await capture(page, info, "attachment-remove-keyboard-focus");
  }
  await expect(previews(page)).toHaveCount(0);
  expect(requests).toEqual([]);
  await capture(page, info, "attachment-remove-last-keyboard-focus");
  await hold(page, 2000);
});
