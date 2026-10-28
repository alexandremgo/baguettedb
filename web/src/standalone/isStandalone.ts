/** Reports whether the app is running as an installed / standalone PWA, on any device. This reflects
 *  how the app was launched (standalone window vs browser tab), not the viewport, so it is stable for
 *  the session and does not flip on resize. */
export function isStandalone(): boolean {
  return (
    window.matchMedia("(display-mode: standalone)").matches ||
    // iOS Safari does not report display-mode; it exposes this non-standard flag instead.
    (window.navigator as Navigator & { standalone?: boolean }).standalone === true
  );
}
