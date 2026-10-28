/** The browser's beforeinstallprompt event, which is not in the standard DOM lib types. */
interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>;
}

// Chromium fires beforeinstallprompt early — before the install button mounts — so stash it at module
// load and reuse it when the button is clicked.
let deferredPrompt: BeforeInstallPromptEvent | null = null;

window.addEventListener("beforeinstallprompt", (e) => {
  e.preventDefault();
  deferredPrompt = e as BeforeInstallPromptEvent;
});

/** Fires the browser's native install dialog if one was captured; returns whether it did. */
export async function triggerInstall(): Promise<boolean> {
  if (!deferredPrompt) return false;
  await deferredPrompt.prompt();
  deferredPrompt = null;
  return true;
}

/** Returns platform-appropriate manual-install instructions, for when no native prompt was captured. */
export function installInstructions(): string {
  const ua = navigator.userAgent;
  // iPadOS 13+ reports as "Macintosh", so disambiguate an iPad from a real Mac via touch points.
  const isIOS =
    /iphone|ipad|ipod/i.test(ua) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  if (isIOS) {
    return "To install: tap the Share button, then choose Add to Home Screen.";
  }
  if (navigator.platform === "MacIntel" || /macintosh/i.test(ua)) {
    return "To install: in Safari: File → Add to Dock; in Chrome: click the install icon in the address bar (it might already be installed — check chrome://apps).";
  }
  return "To install: click the install icon in your browser's address bar if available. It might already be installed — check chrome://apps.";
}
