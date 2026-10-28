/** Dropdown menu shown when the user clicks their avatar in the TopBar; the dropdown header carries the name
 *  and email so the bar itself stays quiet. */

import { useEffect, useRef, useState } from "react";
import { NavLink } from "react-router-dom";
import { useAuth } from "../../auth";
import { extPlugin, type ExtUserMenuItem } from "../../extPlugins";
import styles from "./UserMenu.module.css";

export function UserMenu() {
  const { user, logout } = useAuth();
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  // Closes the menu when clicking outside.
  useEffect(() => {
    if (!open) return;
    function handleClickOutside(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [open]);

  if (!user) return null;

  const isAdmin = user.roles?.includes("admin") ?? false;

  // Built-in items live at the bottom; plugin items are prepended.
  const builtInItems: ExtUserMenuItem[] = [
    ...(isAdmin ? [{ type: "link" as const, to: "/admin", label: "Admin" }] : []),
    { type: "link", to: "/settings", label: "Settings" },
    { type: "separator" },
    { type: "action", label: "Sign Out", onClick: logout, variant: "danger" },
  ];

  const items: ExtUserMenuItem[] = [...extPlugin.userMenuItems, ...builtInItems];

  const fullName =
    user.first_name || user.last_name
      ? `${user.first_name} ${user.last_name}`.trim()
      : user.email;
  const initial = (
    user.first_name?.[0] ||
    user.last_name?.[0] ||
    user.email?.[0] ||
    "?"
  ).toUpperCase();

  return (
    <div className={styles.container} ref={containerRef}>
      <button
        className={styles.trigger}
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={fullName}
      >
        <span className={styles.avatar} aria-hidden="true">
          {initial}
        </span>
      </button>

      {open && (
        <div className={styles.menu} role="menu">
          <div className={styles.identity}>
            <div className={styles.identityName}>{fullName}</div>
            {fullName !== user.email && <div className={styles.identityEmail}>{user.email}</div>}
          </div>
          <div className={styles.separator} role="separator" />
          {items.map((item, i) => (
            <UserMenuEntry key={i} item={item} onNavigate={() => setOpen(false)} />
          ))}
        </div>
      )}
    </div>
  );
}

interface UserMenuEntryProps {
  item: ExtUserMenuItem;
  onNavigate: () => void;
}

function UserMenuEntry({ item, onNavigate }: UserMenuEntryProps) {
  switch (item.type) {
    case "header":
      return <div className={styles.header}>{item.render()}</div>;
    case "separator":
      return <div className={styles.separator} role="separator" />;
    case "link":
      return (
        <NavLink
          to={item.to}
          className={styles.item}
          role="menuitem"
          onClick={onNavigate}
        >
          {item.label}
        </NavLink>
      );
    case "action": {
      const variantClass = item.variant === "danger" ? styles.danger : "";
      return (
        <button
          type="button"
          className={[styles.item, variantClass].join(" ")}
          role="menuitem"
          onClick={() => {
            item.onClick();
            onNavigate();
          }}
        >
          {item.label}
        </button>
      );
    }
  }
}
