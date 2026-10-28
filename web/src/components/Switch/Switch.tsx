/** Toggle switch for boolean modes (filters, settings): a native checkbox announced as a switch, drawn as a
 *  sliding track. Booleans that submit a value in a form (action dialogs) stay checkboxes; a switch implies the
 *  effect is immediate. */

import type { InputHTMLAttributes } from "react";
import styles from "./Switch.module.css";

type SwitchProps = InputHTMLAttributes<HTMLInputElement>;

export function Switch({ className, ...props }: SwitchProps) {
  return (
    <span className={[styles.root, className ?? ""].filter(Boolean).join(" ")}>
      <input type="checkbox" role="switch" className={styles.input} {...props} />
      <span className={styles.track} aria-hidden="true">
        <span className={styles.thumb} />
      </span>
    </span>
  );
}
