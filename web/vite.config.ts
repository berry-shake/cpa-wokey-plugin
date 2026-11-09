import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { viteSingleFile } from "vite-plugin-singlefile";

// CPA base URL used by the dev-server proxy. Override with VITE_CPA_BASE.
const cpaBase = process.env.VITE_CPA_BASE ?? "http://127.0.0.1:8317";

// The plugin serves the page at /v0/resource/plugins/wokey/manage with a CSP
// that only allows inline scripts and styles, so everything is inlined into a
// single index.html. The logo stays a relative URL (logo.svg next to manage).
export default defineConfig({
  base: "./",
  plugins: [react(), viteSingleFile()],
  build: {
    assetsInlineLimit: 100000000,
    cssCodeSplit: false,
    rollupOptions: {
      output: { inlineDynamicImports: true },
    },
  },
  server: {
    port: 5175,
    proxy: {
      "/v0/management": { target: cpaBase, changeOrigin: true },
    },
  },
});
