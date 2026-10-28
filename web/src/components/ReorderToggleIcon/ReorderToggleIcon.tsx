/** Glyphs for a reorder-mode toggle: up/down arrows to enter the mode, a check to leave it. */

import { ArrowDownUp, Check } from "../icons";

interface ReorderToggleIconProps {
  active: boolean;
}

export function ReorderToggleIcon({ active }: ReorderToggleIconProps) {
  const Icon = active ? Check : ArrowDownUp;
  return <Icon size={16} strokeWidth={1.8} aria-hidden="true" focusable="false" />;
}
