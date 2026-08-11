import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// The build output is embedded into the Go binary, so it has to be
// self-contained and served from the root path.
//
// For development, run `pi-go -web -web-dev http://localhost:5173`: the browser
// talks to the Go server, which reverse-proxies everything that is not /api to
// vite. Keeping a single origin is what makes the token in the URL and the
// same-origin check work without opening CORS.
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    target: "es2020",
    chunkSizeWarningLimit: 1500,
  },
});
