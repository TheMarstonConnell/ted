import { defineConfig } from "@playwright/test";
import base from "./playwright.config";

// Separate command so the regular Chromium suite does not require WebKit.
export default defineConfig({
  ...base,
  testMatch: ["iphone.spec.ts", "send-scroll.spec.ts"],
  use: {
    ...base.use,
    browserName: "webkit",
    launchOptions: { executablePath: process.env.WEBKIT_PATH },
  },
});
