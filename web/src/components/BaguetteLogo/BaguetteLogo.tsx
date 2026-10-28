/** BaguetteLogo renders the baguette mark — the emoji as a bundled PNG, so it looks identical on every OS —
 *  with an optional "BaguetteDB" wordmark in the display face. */

import styles from "./BaguetteLogo.module.css";

interface BaguetteLogoProps {
  /** Rendered size of the mark in px. */
  size?: number;
  /** Shows the "BaguetteDB" wordmark next to the mark; its font-size is inherited from the parent. */
  wordmark?: boolean;
  className?: string;
}

export function BaguetteLogo({ size = 20, wordmark = false, className }: BaguetteLogoProps) {
  const src = size <= 24 ? "/favicon-48x48.png" : "/favicon-192x192.png";
  return (
    <span className={[styles.root, className ?? ""].filter(Boolean).join(" ")}>
      <img src={src} width={size} height={size} alt="" className={styles.mark} />
      {wordmark && <span className={styles.wordmark}>BaguetteDB</span>}
    </span>
  );
}
