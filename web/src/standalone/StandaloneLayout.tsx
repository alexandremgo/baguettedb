/** Installable standalone shell: a three-zone header (back control, centered screen title, theme toggle), the
 *  active screen, and a bottom Formulas/Tables tab bar. The back control names its destination — a detail
 *  screen returns to its list, a list screen returns to the bdb switcher — while the centered title names the
 *  current screen. Fills the dynamic viewport with safe-area insets; content sits in a max-width column so it
 *  is full-width on a phone and centered on a wider window. */

import { Outlet, Link, NavLink, useMatch } from "react-router-dom";
import { BaguetteLogo } from "../components/BaguetteLogo";
import { ChevronLeft, Moon, SquareKanban, Sun, Table2 } from "../components/icons";
import { formulaTitle } from "../components/Formula/selectors";
import { useBdb } from "../hooks/useBdb";
import { useFormulas } from "../hooks/useFormulas";
import { useTheme } from "../hooks/useTheme";
import styles from "./StandaloneLayout.module.css";

export function StandaloneLayout() {
  const rawBdbId = useMatch("/app/bdbs/:bdbId/*")?.params.bdbId;
  // "new" is the create flow, not a bdb id — no bdb header context or bottom nav there.
  const bdbId = rawBdbId === "new" ? undefined : rawBdbId;
  const { data: bdb } = useBdb(bdbId);
  const { theme, toggleTheme } = useTheme();

  const formulaId = useMatch("/app/bdbs/:bdbId/formulas/:formulaId")?.params.formulaId;
  const tableName = useMatch("/app/bdbs/:bdbId/tables/:table")?.params.table;
  const onCreateFlow = useMatch("/app/bdbs/new/*") !== null;

  // The formula list is already cached by the formulas screens; here it only resolves the title.
  const { data: formulas = [] } = useFormulas(formulaId ? bdbId : undefined);
  const formula = formulaId ? formulas.find((f) => f.id === formulaId) : undefined;

  // Back names the destination; the centered title names the current screen. On the switcher there is
  // nothing above, so the brand takes the title slot.
  let back: { to: string; label: string } | null = null;
  let title: string | null = null;
  if (onCreateFlow) {
    back = { to: "/app/bdbs", label: "My bdbs" };
    title = "New bdb";
  } else if (bdbId && formulaId) {
    back = { to: `/app/bdbs/${bdbId}/formulas`, label: "Formulas" };
    title = formula ? formulaTitle(formula) : "";
  } else if (bdbId && tableName) {
    back = { to: `/app/bdbs/${bdbId}/tables`, label: "Tables" };
    title = decodeURIComponent(tableName);
  } else if (bdbId) {
    back = { to: "/app/bdbs", label: "My bdbs" };
    title = bdb?.name ?? "";
  }

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <div className={styles.headerInner}>
          <div className={styles.headerSide}>
            {back && (
              <Link to={back.to} className={styles.back}>
                <ChevronLeft size={18} aria-hidden="true" />
                {back.label}
              </Link>
            )}
          </div>
          <div className={styles.headerTitle}>
            {title != null ? (
              <span className={styles.title}>{title}</span>
            ) : (
              <BaguetteLogo wordmark className={styles.brand} />
            )}
          </div>
          <div className={[styles.headerSide, styles.headerSideEnd].join(" ")}>
            <button
              className={styles.themeToggle}
              onClick={toggleTheme}
              aria-label={theme === "light" ? "Switch to dark mode" : "Switch to light mode"}
              title={theme === "light" ? "Dark mode" : "Light mode"}
            >
              {theme === "light" ? <Moon size={18} aria-hidden="true" /> : <Sun size={18} aria-hidden="true" />}
            </button>
          </div>
        </div>
      </header>

      <main className={styles.content}>
        <div className={styles.column}>
          <Outlet />
        </div>
      </main>

      {bdbId && (
        <nav className={styles.nav}>
          <div className={styles.navInner}>
            <NavLink to={`/app/bdbs/${bdbId}/formulas`} className={navItemClass}>
              <SquareKanban size={20} aria-hidden="true" />
              Formulas
            </NavLink>
            <NavLink to={`/app/bdbs/${bdbId}/tables`} className={navItemClass}>
              <Table2 size={20} aria-hidden="true" />
              Tables
            </NavLink>
          </div>
        </nav>
      )}
    </div>
  );
}

function navItemClass({ isActive }: { isActive: boolean }): string {
  return isActive ? `${styles.navItem} ${styles.navItemActive}` : styles.navItem;
}
