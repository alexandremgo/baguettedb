import { useToast } from "../Toast";
import { triggerInstall, installInstructions } from "./installPrompt";

interface InstallButtonProps {
  className?: string;
  /** Called after a click so the host can e.g. close the mobile sidebar. */
  onSelect?: () => void;
}

/** An always-visible "Install app" action. Where the browser captured an install prompt (Chromium)
 *  the click fires the native dialog; otherwise it shows how to install manually. We don't try to
 *  detect whether the app is already installed — that isn't reliable from a browser tab. */
export function InstallButton({ className, onSelect }: InstallButtonProps) {
  const { toast } = useToast();

  async function handleClick() {
    const opened = await triggerInstall();
    if (!opened) {
      // Manual-install steps differ per platform and need reading, so give the toast longer.
      toast(installInstructions(), "default", 9000);
    }
    onSelect?.();
  }

  return (
    <button type="button" className={className} onClick={() => void handleClick()}>
      Install app
    </button>
  );
}
