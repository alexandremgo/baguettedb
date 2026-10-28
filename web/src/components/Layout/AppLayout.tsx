/** App shell with collapsible sidebar and top bar. */

import { useState } from "react";
import { Outlet, Link } from "react-router-dom";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";
import { extPlugin } from "../../extPlugins";
import styles from "./AppLayout.module.css";

export function AppLayout() {
  const [sidebarOpen, setSidebarOpen] = useState(false);

  return (
    <div className={styles.layout}>
      {sidebarOpen && (
        <div
          className={styles.overlay}
          onClick={() => setSidebarOpen(false)}
        />
      )}
      <Sidebar
        open={sidebarOpen}
        onNavigate={() => setSidebarOpen(false)}
      />
      <div className={styles.main}>
        <TopBar onMenuClick={() => setSidebarOpen((o) => !o)} />
        <div className={styles.content}>
          <Outlet />
        </div>
        <footer className={styles.footer}>
          <span>© {new Date().getFullYear()} BaguetteDB. All rights reserved.</span>
          <Link to="/licenses" className={styles.footerLink}>Licenses</Link>
          {extPlugin.footerLinks?.map((link, i) => (
            <Link key={`footer-${i}`} to={link.to} className={styles.footerLink}>
              {link.label}
            </Link>
          ))}
        </footer>
      </div>
    </div>
  );
}
