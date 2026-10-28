# Web frontend — Claude Code instructions

## Icons

- UI chrome icons come from **Lucide React**, always via the central registry `src/components/icons.ts`
  (named re-exports, alphabetical). Never import `lucide-react` directly in a component and never
  hand-roll inline `<svg>` chrome.
- Default sizing: `size={16}` for controls, `size={14}` inside dense elements (pickers, popover
  buttons). Icons inherit color via `currentColor`; pass `aria-hidden="true"` when the icon is
  decorative next to a text label, and give icon-only buttons an `aria-label`.
- **Chrome vs. user data**: icons replace chrome only. Emoji or symbols that come from user data
  (bdb names, formula display content, starter manifests) are content — render them as-is, never
  swap them for icons.

## Styling

- Design tokens live in `src/styles/` (`primitives.css` → `semantic.css`); components use CSS
  Modules. Colors and shadows come from semantic tokens only; scale tokens (space, radius, text,
  weight, leading, font, duration) come straight from `primitives.css`. No Tailwind, no inline
  style objects for theming.
- `semantic.css` values are mirrored in `pkg/auth/api/templates/_partials.html` (the server-rendered
  sign-in pages): change both together.
- `--font-display` (Schibsted Grotesk, self-hosted via `@fontsource-variable`) is for the wordmark
  and headings; body text stays on `--font-sans` (system stack). Fonts are npm-bundled — never load
  fonts from a CDN.
- Boolean **filter** params render as the `Switch` primitive (a filter boolean is a mode); booleans
  inside action/add dialogs stay checkboxes.

## Dependencies

- After any dependency change, regenerate the lockfile from scratch (`rm -rf node_modules
  package-lock.json && npm install`) and verify `npm ci` passes in a linux container matching CI's
  node major (`node:24-alpine`) - a macOS `npm ci` skips other platforms' optional deps and can pass
  on a lockfile CI rejects. Then run `make notices` from the repo root and commit the regenerated
  `THIRD_PARTY_LICENSES`.

## Verifying

- Run `npm run lint` and `npm run build` (`tsc -b && vite build`), as CI does. A bare `tsc --noEmit`
  is not enough: it can pass where the project build fails.
