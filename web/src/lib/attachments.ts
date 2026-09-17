import type { Schema } from "./api";

export type Attachment = Schema["Attachment"];
export const MAX_ATTACHMENTS = 4;
export const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
export const IMAGE_TYPES = [
  "image/png",
  "image/jpeg",
  "image/webp",
  "image/gif",
];
export const IMAGE_ACCEPT = IMAGE_TYPES.join(",");

export function validateImageFiles(files: File[], existingCount: number) {
  if (files.length + existingCount > MAX_ATTACHMENTS)
    throw new Error(`Attach up to ${MAX_ATTACHMENTS} images per message.`);
  for (const file of files) {
    if (!IMAGE_TYPES.includes(file.type))
      throw new Error("Choose PNG, JPEG, WebP, or GIF images.");
    if (!file.size || file.size > MAX_IMAGE_BYTES)
      throw new Error("Each image must be nonempty and no larger than 5 MiB.");
    if ([...file.name].length > 256)
      throw new Error("Image filenames must not exceed 256 characters.");
  }
}

export function readImage(file: File): Promise<Attachment> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error(`Could not read ${file.name}.`));
    reader.onabort = () =>
      reject(new Error(`Reading ${file.name} was cancelled.`));
    reader.onload = () =>
      resolve({ name: file.name, url: String(reader.result) });
    reader.readAsDataURL(file);
  });
}
