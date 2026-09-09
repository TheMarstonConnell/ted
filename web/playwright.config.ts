import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./e2e",
  use: {
    browserName: "chromium",
    baseURL: process.env.TED_WEB_URL || "http://127.0.0.1:5188",
    viewport: { width: 1440, height: 960 },
    launchOptions: { executablePath: process.env.CHROME_PATH },
    screenshot: "only-on-failure",
    video: process.env.TED_WEB_RECORD ? "on" : "retain-on-failure",
  },
  webServer: {
    command: "npm run dev -- --host 127.0.0.1 --port 5188 --strictPort",
    url: "http://127.0.0.1:5188",
    reuseExistingServer: !process.env.CI,
  },
});
