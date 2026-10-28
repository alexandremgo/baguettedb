/** SchemaList renders a table's column schema behind a "Schema · N columns" disclosure, collapsed by default
 *  so the data leads: mono column names with a type pill, a caramel PK pill, and an outlined NOT NULL pill.
 *  Mount with `key={tableName}` so switching tables starts collapsed again. */

import { useState } from "react";
import type { TableColumn } from "../../api/databases";
import { ChevronDown, ChevronRight } from "../icons";
import styles from "./SchemaList.module.css";

interface SchemaListProps {
  columns: TableColumn[];
}

export function SchemaList({ columns }: SchemaListProps) {
  const [open, setOpen] = useState(false);

  return (
    <div>
      <button
        type="button"
        className={styles.toggle}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <span className={styles.chevron} aria-hidden>
          {open ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
        </span>
        Schema
        <span className={styles.count}>
          {columns.length} column{columns.length !== 1 ? "s" : ""}
        </span>
      </button>
      {open && (
        <div className={styles.grid}>
          {columns.map((col, i) => (
            <div key={i} className={styles.row}>
              <span className={styles.colName}>{col.name}</span>
              <span className={styles.colType}>{col.type}</span>
              {col.primary_key && <span className={styles.pkPill}>PK</span>}
              {col.not_null && <span className={styles.constraintPill}>NOT NULL</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
