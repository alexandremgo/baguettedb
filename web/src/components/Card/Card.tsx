/** Bordered surface container with optional CardHeader and CardFooter. */

import type { ReactNode } from "react";
import styles from "./Card.module.css";

interface CardProps {
  children: ReactNode;
  className?: string;
}

interface CardHeaderProps {
  children: ReactNode;
  className?: string;
}

interface CardFooterProps {
  children: ReactNode;
  className?: string;
}

export function Card({ children, className }: CardProps) {
  return (
    <div className={[styles.card, className ?? ""].filter(Boolean).join(" ")}>
      {children}
    </div>
  );
}

export function CardHeader({ children, className }: CardHeaderProps) {
  return (
    <div
      className={[styles.header, className ?? ""].filter(Boolean).join(" ")}
    >
      {children}
    </div>
  );
}

export function CardFooter({ children, className }: CardFooterProps) {
  return (
    <div
      className={[styles.footer, className ?? ""].filter(Boolean).join(" ")}
    >
      {children}
    </div>
  );
}
