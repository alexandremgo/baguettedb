import type { VitePWAOptions } from "vite-plugin-pwa";

/** Branding overrides a downstream distribution can apply to the PWA manifest. */
export interface PwaBranding {
  name?: string;
  shortName?: string;
  themeColor?: string;
  backgroundColor?: string;
}

/**
 * pwaOptions builds the shared vite-plugin-pwa configuration: the installable manifest plus the
 * auth-safe Workbox rules. Auth is cookie-based and the OAuth login flow is a chain of
 * server-rendered, same-origin navigations, so the service worker must never serve the app shell
 * for those navigations nor cache authenticated requests — otherwise it hijacks the redirect flow
 * or drops the Set-Cookie round-trip. The navigateFallbackDenylist + config-revalidation rules
 * below keep login intact. A downstream distribution overrides only the branding fields.
 */
export function pwaOptions(branding: PwaBranding = {}): Partial<VitePWAOptions> {
  return {
    registerType: "autoUpdate",
    // The service worker only exists in production builds by default. Opting in with PWA_DEV=true
    // also runs it under `vite dev` (which proxies /api,/oauth2,...) for local testing, while keeping
    // ordinary dev runs free of SW caching / HMR friction.
    devOptions: {
      enabled: process.env.PWA_DEV === "true",
      type: "module",
    },
    manifest: {
      name: branding.name ?? "BaguetteDB",
      short_name: branding.shortName ?? "BaguetteDB",
      // Installed launches open the standalone shell directly; scope stays "/" so the rest of the app
      // (including the OAuth/login flow outside /app) still opens inside the installed window.
      start_url: "/app/bdbs",
      scope: "/",
      display: "standalone",
      theme_color: branding.themeColor ?? "#c9731d",
      background_color: branding.backgroundColor ?? "#0a0a0a",
      icons: [
        { src: "/favicon-192x192.png", sizes: "192x192", type: "image/png" },
        { src: "/favicon-512x512.png", sizes: "512x512", type: "image/png" },
        { src: "/favicon-512x512.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
      ],
    },
    workbox: {
      // config.json carries the per-distribution OAuth client_id/redirect_uri and is injected at
      // deploy time — never precache it; revalidate so a stale copy can't silently break login.
      globIgnores: ["**/config.json"],
      navigateFallback: "/index.html",
      // The app shell must NOT be served for the server-rendered OAuth/login navigations, the
      // OAuth callback (query-bearing), or API/auth requests — those must reach the backend with
      // cookies intact. Listing them here keeps the SPA fallback from hijacking the login flow. Every
      // backend prefix pkg/webui refuses the app shell for is here, plus the callback and config.json.
      navigateFallbackDenylist: [
        /^\/api\//,
        /^\/oauth2\//,
        /^\/login/,
        /^\/consent/,
        /^\/\.well-known\//,
        /^\/authorize/,
        /^\/token/,
        /^\/register/,
        /^\/userinfo/,
        /^\/mcp/,
        /^\/healthz/,
        /^\/static\//,
        /^\/internal\//,
        /^\/callback/,
        /^\/config\.json$/,
      ],
      runtimeCaching: [
        {
          urlPattern: /\/config\.json$/,
          handler: "NetworkFirst",
          options: { cacheName: "runtime-config" },
        },
      ],
    },
  };
}
