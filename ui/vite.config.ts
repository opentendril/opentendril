import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The Stem sets no CORS headers, so in development we proxy the REST and
// WebSocket surfaces through the Vite origin. Point STEM_TARGET at a remote
// Stem if it is not on localhost:8080. In production, serve ui/dist from the
// same origin as the Stem (or behind a reverse proxy that fronts both).
const target = process.env.STEM_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": { target, changeOrigin: true },
      "/health": { target, changeOrigin: true },
      "/ws": { target, changeOrigin: true, ws: true },
    },
  },
});
