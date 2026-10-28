/** Top bar with hamburger menu, theme toggle, and user dropdown menu. */

import { useTheme } from "../../hooks/useTheme";
import { MenuIcon, Moon, Sun } from "../icons";
import { UserMenu } from "./UserMenu";
import styles from "./TopBar.module.css";

interface TopBarProps {
  onMenuClick: () => void;
}

export function TopBar({ onMenuClick }: TopBarProps) {
  const { theme, toggleTheme } = useTheme();

  return (
    <header className={styles.topBar}>
      <button
        className={styles.menuButton}
        onClick={onMenuClick}
        aria-label="Toggle menu"
      >
        <MenuIcon size={20} aria-hidden="true" />
      </button>
      <div className={styles.spacer} />
      <div className={styles.user}>
        <button
          className={styles.themeToggle}
          onClick={toggleTheme}
          aria-label={theme === "light" ? "Switch to dark mode" : "Switch to light mode"}
          title={theme === "light" ? "Dark mode" : "Light mode"}
        >
          {theme === "light" ? <Moon size={18} aria-hidden="true" /> : <Sun size={18} aria-hidden="true" />}
        </button>
        <UserMenu />
      </div>
    </header>
  );
}
