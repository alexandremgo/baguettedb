// Applies the theme before first paint so every page — including the unauthenticated ones that never mount the
// theme hook — renders with the stored choice, falling back to the system preference.
try {
  var theme = localStorage.getItem("theme");
  if (theme !== "light" && theme !== "dark") {
    theme = matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  document.documentElement.dataset.theme = theme;
} catch (e) {
  /* Storage can be unavailable (private mode); the light default applies. */
}
