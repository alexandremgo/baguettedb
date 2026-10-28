import { useState } from "react";
import styles from "./CopyableCode.module.css";

/**
 * Code snippet that copies its text to the clipboard on click: inline, or with `block` a preformatted block for
 * several lines, such as a configuration file's content, which keeps its text shown while it says so.
 */
export function CopyableCode({ text, block = false }: { text: string; block?: boolean }) {
  const [copied, setCopied] = useState(false);

  function handleClick() {
    navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  }

  const className = `${styles.code} ${block ? styles.block : ""} ${copied ? styles.copied : ""}`;
  if (block) {
    return (
      <pre className={className} onClick={handleClick} role="button" tabIndex={0}>
        {text}
      </pre>
    );
  }
  return (
    <code className={className} onClick={handleClick} role="button" tabIndex={0}>
      {copied ? "Copied!" : text}
    </code>
  );
}
