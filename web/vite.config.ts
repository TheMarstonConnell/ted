import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  server: {
    host: "0.0.0.0",
    port: 8283,
    strictPort: true,
    proxy: {
      // Preserve the browser Host: the server checks WebSocket Origin against it.
      "/v1": {
        target: process.env.TED_API_URL || "http://127.0.0.1:8281",
        ws: true,
        changeOrigin: false,
      },
      "/health": {
        target: process.env.TED_API_URL || "http://127.0.0.1:8281",
        changeOrigin: false,
      },
    },
  },
});
