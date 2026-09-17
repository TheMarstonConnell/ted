import { describe, expect, it } from "vitest";
import { MAX_IMAGE_BYTES, validateImageFiles } from "./attachments";

const image = (size = 16, type = "image/png", name = "screen.png") =>
  new File([new Uint8Array(size)], name, { type });

describe("screenshot selection", () => {
  it("accepts supported images at the size and count limits", () => {
    expect(() =>
      validateImageFiles(
        [
          image(MAX_IMAGE_BYTES),
          image(16, "image/jpeg"),
          image(16, "image/webp"),
          image(16, "image/gif"),
        ],
        0,
      ),
    ).not.toThrow();
  });
  it("counts existing attachments and rejects a batch atomically", () => {
    expect(() => validateImageFiles([image(), image()], 3)).toThrow("up to 4");
    expect(() =>
      validateImageFiles([image(), image(16, "image/svg+xml")], 0),
    ).toThrow("PNG, JPEG, WebP, or GIF");
  });
  it("rejects oversized, empty and unsupported files", () => {
    expect(() => validateImageFiles([image(MAX_IMAGE_BYTES + 1)], 0)).toThrow(
      "5 MiB",
    );
    expect(() => validateImageFiles([image(0)], 0)).toThrow("nonempty");
    expect(() => validateImageFiles([image(16, "text/plain")], 0)).toThrow(
      "PNG, JPEG, WebP, or GIF",
    );
  });
  it("limits Unicode filenames by code point", () => {
    expect(() =>
      validateImageFiles([image(16, "image/png", "📸".repeat(256))], 0),
    ).not.toThrow();
    expect(() =>
      validateImageFiles([image(16, "image/png", "📸".repeat(257))], 0),
    ).toThrow("256 characters");
  });
});
