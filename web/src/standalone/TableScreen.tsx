/** Standalone read-only table view — one card per row, with the schema behind a disclosure, a
 *  client-side filter over the loaded rows, and load-more paging. */

import { useState } from "react";
import { useParams } from "react-router-dom";
import { Button } from "../components/Button";
import { SchemaList } from "../components/SchemaList";
import { X } from "../components/icons";
import { useTableData, useTableSchema } from "../hooks/useTables";
import { formatCellValue } from "../utils/cellValue";
import { SkeletonRows } from "../components/Skeleton";
import styles from "./Standalone.module.css";

export function TableScreen() {
  const { bdbId, table } = useParams<{ bdbId: string; table: string }>();
  const [limit, setLimit] = useState(50);
  const [query, setQuery] = useState("");
  const { data, isLoading, error } = useTableData(bdbId, table ?? null, limit);
  const { data: schema } = useTableSchema(bdbId, table ?? null);

  if (isLoading) return <SkeletonRows />;
  if (error || !data) return <p className={styles.error}>Couldn't load this table.</p>;

  const hasMore = data.rows.length > limit;
  const allRows = data.rows.slice(0, limit);
  const term = query.trim().toLowerCase();
  const rows =
    term === ""
      ? allRows
      : allRows.filter((row) =>
          row.some((cell) => formatCellValue(cell?.value).toLowerCase().includes(term)),
        );
  const countLabel =
    term !== ""
      ? `${rows.length} of ${allRows.length} row${allRows.length !== 1 ? "s" : ""}`
      : `${allRows.length}${hasMore ? "+" : ""} row${allRows.length === 1 ? "" : "s"}`;

  return (
    <>
      {schema && <SchemaList key={table} columns={schema.columns} />}

      <div className={styles.rowFilterBar}>
        <div className={styles.rowFilterWrap}>
          <input
            type="search"
            className={styles.rowFilter}
            placeholder="Filter rows…"
            aria-label="Filter the loaded rows"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setQuery("");
            }}
          />
          {query !== "" && (
            <button
              type="button"
              className={styles.rowFilterClear}
              aria-label="Clear filter"
              onClick={() => setQuery("")}
            >
              <X size={14} aria-hidden="true" />
            </button>
          )}
        </div>
        <span className={styles.rowCount}>{countLabel}</span>
      </div>

      {rows.length === 0 && (
        <p className={styles.muted}>{term !== "" ? "No rows match the filter." : "No rows."}</p>
      )}
      <div className={styles.cards}>
        {rows.map((row, i) => (
          <div key={i} className={styles.card}>
            {data.columns.map((col, j) => (
              <div key={col.name} className={styles.field}>
                <span className={styles.fieldKey}>{col.name}</span>
                <span className={styles.fieldValue}>{formatCellValue(row[j]?.value)}</span>
              </div>
            ))}
          </div>
        ))}
      </div>
      {hasMore && (
        <div className={styles.loadMoreBar}>
          <Button size="sm" variant="secondary" onClick={() => setLimit((l) => l + 50)}>
            Load more
          </Button>
        </div>
      )}
    </>
  );
}
