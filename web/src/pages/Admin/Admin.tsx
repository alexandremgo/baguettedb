/** Admin panel with tabs — Users (built-in) + plugin-provided tabs. */

import { useState } from "react";
import { extPlugin } from "../../extPlugins";
import { AdminUsers } from "./AdminUsers";
import styles from "./Admin.module.css";

export function Admin() {
  const tabs = [
    { id: "users", label: "Users" },
    ...(extPlugin.adminTabs ?? []),
  ];
  const [activeTab, setActiveTab] = useState("users");

  return (
    <div className={styles.container}>
      {tabs.length > 1 && (
        <div className={styles.tabBar}>
          {tabs.map((tab) => (
            <button
              key={tab.id}
              className={[styles.tab, activeTab === tab.id ? styles.tabActive : ""].join(" ")}
              onClick={() => setActiveTab(tab.id)}
            >
              {tab.label}
            </button>
          ))}
        </div>
      )}

      {activeTab === "users" && <AdminUsers />}
      {extPlugin.adminTabs?.map((tab) =>
        activeTab === tab.id ? <div key={tab.id}>{tab.element}</div> : null,
      )}
    </div>
  );
}
