/** Left sidebar with bdb navigation and create button. Slides in on mobile. */

import { NavLink, useNavigate } from "react-router-dom";
import { DndContext, closestCenter } from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { BaguetteLogo } from "../BaguetteLogo";
import { Badge } from "../Badge";
import { Button } from "../Button";
import { ReorderToggleIcon } from "../ReorderToggleIcon";
import { useBdbs } from "../../hooks/useBdbs";
import { useBdbReorder } from "../../hooks/useBdbReorder";
import { extPlugin } from "../../extPlugins";
import { InstallButton } from "./InstallButton";
import { SortableBdbItem } from "./SortableBdbItem";
import { badgedRole, bdbTitle, roleBadgeVariant } from "../../utils/bdbDisplay";
import styles from "./Sidebar.module.css";

interface SidebarProps {
  open: boolean;
  onNavigate: () => void;
}

export function Sidebar({ open, onNavigate }: SidebarProps) {
  const { data: bdbs = [] } = useBdbs();
  const navigate = useNavigate();
  const { reordering, toggleReordering, canReorder, sensors, handleDragEnd, sortableIds } =
    useBdbReorder(bdbs);

  return (
    <aside
      className={[styles.sidebar, open ? styles.sidebarOpen : ""].join(" ")}
    >
      <div className={styles.logo}>
        <NavLink to="/bdbs" className={styles.logoLink} onClick={onNavigate}>
          <BaguetteLogo wordmark />
        </NavLink>
      </div>

      <div className={styles.navArea}>
        <div>
          <div className={styles.sectionHeader}>
            <span className={styles.sectionLabel}>bdbs</span>
            {canReorder && (
              <button
                type="button"
                className={styles.reorderToggle}
                onClick={toggleReordering}
                aria-label={reordering ? "Done reordering" : "Reorder bdbs"}
                aria-pressed={reordering}
              >
                <ReorderToggleIcon active={reordering} />
              </button>
            )}
          </div>

          {reordering ? (
            <DndContext
              sensors={sensors}
              collisionDetection={closestCenter}
              onDragEnd={handleDragEnd}
            >
              <SortableContext items={sortableIds} strategy={verticalListSortingStrategy}>
                <div className={styles.nav}>
                  {bdbs.map((c) => (
                    <SortableBdbItem key={c.id} id={c.id} name={c.name} />
                  ))}
                </div>
              </SortableContext>
            </DndContext>
          ) : (
            <nav className={styles.nav}>
              {bdbs.map((c) => (
                <NavLink
                  key={c.id}
                  to={`/bdbs/${c.id}`}
                  className={({ isActive }) =>
                    [styles.navItem, isActive ? styles.navItemActive : ""].join(" ")
                  }
                  onClick={onNavigate}
                  title={bdbTitle(c)}
                >
                  <span className={styles.navItemName}>{c.name}</span>
                  {badgedRole(c.my_role) && (
                    <Badge variant={roleBadgeVariant(c.my_role)} className={styles.navItemBadge}>
                      {c.my_role}
                    </Badge>
                  )}
                </NavLink>
              ))}
            </nav>
          )}
        </div>

        {extPlugin.navItems.length > 0 && (
          <div className={styles.extSection}>
            <nav className={styles.nav}>
              <InstallButton className={styles.navButton} onSelect={onNavigate} />
              {extPlugin.navItems.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  className={({ isActive }) =>
                    [styles.navItem, isActive ? styles.navItemActive : ""].join(" ")
                  }
                  onClick={onNavigate}
                >
                  {item.label}
                </NavLink>
              ))}
            </nav>
          </div>
        )}
      </div>

      <div className={styles.actions}>
        <Button
          size="sm"
          onClick={() => { navigate("/bdbs/new"); onNavigate(); }}
          className={styles.createButton}
        >
          + New Bdb
        </Button>
      </div>
    </aside>
  );
}
