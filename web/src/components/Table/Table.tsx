/** Generic data table with typed columns, custom cell renderers, and empty state. */

import type { ReactNode } from "react";
import { EmptyState } from "../EmptyState";
import { Inbox } from "../icons";
import { formatCellValue } from "../../utils/cellValue";
import styles from "./Table.module.css";

interface Column<T> {
  key: string;
  header: string;
  /** Right-aligns the column (numbers scan better against a common right edge). */
  align?: "left" | "right";
  /** Shrink-wraps the column to its content — for control columns (checkbox, row menu), so data
   *  columns share the leftover width. */
  fit?: boolean;
  render?: (row: T) => ReactNode;
}

interface TableProps<T> {
  columns: Column<T>[];
  data: T[];
  rowKey: (row: T, index: number) => string;
  emptyMessage?: string;
  className?: string;
  /** Optional click handler invoked when a row is clicked. Adds a hover/cursor
   * affordance and keyboard activation (Enter/Space) on each row. */
  onRowClick?: (row: T) => void;
}

export function Table<T>({
  columns,
  data,
  rowKey,
  emptyMessage = "No data",
  className,
  onRowClick,
}: TableProps<T>) {
  return (
    <div className={[styles.wrapper, className ?? ""].filter(Boolean).join(" ")}>
      <table className={styles.table}>
        <thead>
          <tr>
            {columns.map((col) => (
              <th
                key={col.key}
                className={[
                  styles.th,
                  col.align === "right" ? styles.right : "",
                  col.fit ? styles.fit : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
              >
                {col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className={styles.empty}>
                <EmptyState compact icon={Inbox} title={emptyMessage} />
              </td>
            </tr>
          ) : (
            data.map((row, index) => (
              <tr
                key={rowKey(row, index)}
                className={[styles.tr, onRowClick ? styles.clickable : ""].filter(Boolean).join(" ")}
                onClick={onRowClick ? () => onRowClick(row) : undefined}
                onKeyDown={
                  onRowClick
                    ? (e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          onRowClick(row);
                        }
                      }
                    : undefined
                }
                tabIndex={onRowClick ? 0 : undefined}
                role={onRowClick ? "button" : undefined}
              >
                {columns.map((col) => (
                  <td
                    key={col.key}
                    className={[
                      styles.td,
                      col.align === "right" ? styles.right : "",
                      col.fit ? styles.fit : "",
                    ]
                      .filter(Boolean)
                      .join(" ")}
                  >
                    {col.render
                      ? col.render(row)
                      : formatCellValue((row as Record<string, unknown>)[col.key], "")}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
