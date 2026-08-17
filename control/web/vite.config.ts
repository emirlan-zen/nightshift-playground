import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

// Built assets are embedded into the Go binary (control/web.go) and served
// from `/`. Keep base relative-root so the hashed asset URLs resolve behind
// Cloudflare Access in production.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(__dirname, "src") },
  },
  // Dev-only: the SPA calls same-origin /api/*, so proxy it to the Go control
  // plane running in dev mode (NIGHTSHIFT_DEV=1 on :8787, see `make dev`). No
  // effect on the production build — the binary serves both SPA and API itself.
  server: {
    proxy: {
      "/api": { target: "http://localhost:8787", changeOrigin: true },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 900,
  },
});
