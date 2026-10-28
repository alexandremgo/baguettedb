import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { VitePWA } from "vite-plugin-pwa";
import { pwaOptions } from "./pwaConfig";

export default defineConfig({
  plugins: [react(), VitePWA(pwaOptions())],
  build: {
    // Lists every package that ends up in the bundle, with its license text: the input of the third-party notices.
    license: { fileName: ".vite/license.json" },
  },
  server: {
    // Allows reaching the dev server through an HTTPS tunnel / reverse proxy (e.g. to test the
    // installable PWA on a phone): the IPv4 loopback matches what such proxies connect to — Vite
    // would otherwise bind IPv6 ::1 only — and allowedHosts lets the tunnel host past the host check.
    host: "127.0.0.1",
    allowedHosts: [".ts.net"],
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/oauth2": "http://localhost:8080",
      "/login": "http://localhost:8080",
      "/consent": "http://localhost:8080",
      "/.well-known": "http://localhost:8080",
      "/authorize": "http://localhost:8080",
      "/token": "http://localhost:8080",
      "/static": "http://localhost:8080",
      "/config.json": "http://localhost:8080",
    },
  },
});
