/** Renders a QueryResult (columns + typed rows) as a table, with NULL shown distinctly, an
 *  optional leading checkbox column for selecting rows by their rowid, and an optional trailing
 *  per-row ⋯ menu column. */

import type { QueryResult, Value } from "../../api/databases";
import { formatCellValue, isNullValue, parseStructuredValue } from "../../utils/cellValue";
import { Menu, type MenuItem } from "../Menu";
import { StructuredCell } from "../StructuredCell";
import { Table } from "../Table";
import { TruncatedCell } from "../TruncatedCell/TruncatedCell";
import styles from "./QueryResultTable.module.css";

/** Row-selection wiring: which column holds each row's rowid, plus the current set and a toggle. */
export interface RowSelection {
  rowIdColumn: number;
  selected: Set<number>;
  onToggle: (rowId: number) => void;
}

/** Row-click wiring: which column holds each row's rowid, plus the open callback. A row without a
 *  numeric rowid ignores the click. */
export interface RowClick {
  rowIdColumn: number;
  onOpen: (rowId: number) => void;
}

/** Per-row menu wiring: which column holds each row's rowid, plus the item builder. A row without
 *  a numeric rowid — or with no items — gets no menu. */
export interface RowMenu {
  rowIdColumn: number;
  itemsFor: (rowId: number, row: Value[]) => MenuItem[];
}

interface QueryResultTableProps {
  result: QueryResult;
  emptyMessage?: string;
  selection?: RowSelection;
  rowClick?: RowClick;
  rowMenu?: RowMenu;
  /** Skips rendering columns named exactly `rowid` — the reserved identity column formula views
   *  must select for row-bound interactions and pagination, which members don't need to see. Only
   *  the cells are skipped: selection / click / menu wiring still reads the full row by index. */
  hideRowIdColumn?: boolean;
  /** Column names to skip rendering, kept in the row data for interaction wiring — like
   *  hideRowIdColumn, but driven by a view's display config (e.g. a machine column seeding an edit
   *  slot's group param). Cells are skipped by name; indices stay intact. */
  hiddenColumnNames?: Set<string>;
}

/** A rowid is a whole number; anything else (NULL, text) can't be selected. */
function numericRowId(value: unknown): number | null {
  return typeof value === "number" ? value : null;
}

/** Reports whether a column holds numbers, from its declared affinity when the type is known and
 *  from the loaded values otherwise (a computed column has no declared type). */
function isNumericColumn(declType: string, rows: Value[][], colIndex: number): boolean {
  if (/INT|REAL|FLOA|DOUB|NUM|DEC/i.test(declType)) return true;
  if (declType !== "") return false;
  let sawNumber = false;
  for (const row of rows) {
    const value = row[colIndex]?.value;
    if (value == null) continue;
    if (typeof value !== "number") return false;
    sawNumber = true;
  }
  return sawNumber;
}

export function QueryResultTable({
  result,
  emptyMessage = "No rows",
  selection,
  rowClick,
  rowMenu,
  hideRowIdColumn,
  hiddenColumnNames,
}: QueryResultTableProps) {
  // Renders each row straight from its positional Value[] so duplicate column names
  // (e.g. a joined query) don't collide the way a name-keyed object would.
  const valueColumns = result.columns
    .map((col, colIndex) => ({ col, colIndex }))
    .filter(({ col }) => !(hideRowIdColumn && col.name === "rowid"))
    .filter(({ col }) => !hiddenColumnNames?.has(col.name))
    .map(({ col, colIndex }) => ({
      key: String(colIndex),
      header: col.name,
      align: isNumericColumn(col.decl_type, result.rows, colIndex) ? ("right" as const) : undefined,
      render: (row: Value[]) => {
        const cell = row[colIndex];
        if (!cell || isNullValue(cell.value)) {
          return <span className={styles.null}>NULL</span>;
        }
        const structured = parseStructuredValue(cell.value);
        if (structured) {
          return <StructuredCell value={structured} />;
        }
        return (
          <TruncatedCell maxLength={50} copyable>
            {formatCellValue(cell.value)}
          </TruncatedCell>
        );
      },
    }));

  const selectColumn = selection
    ? [
        {
          key: "__select__",
          header: "",
          fit: true,
          render: (row: Value[]) => {
            const rowId = numericRowId(row[selection.rowIdColumn]?.value);
            if (rowId === null) {
              return null;
            }
            // The label enlarges the toggle target and swallows the click, so aiming at the
            // checkbox (even a few pixels off) never triggers the row's own click action.
            return (
              <label className={styles.selectCell} onClick={(e) => e.stopPropagation()}>
                <input
                  type="checkbox"
                  className={styles.checkbox}
                  checked={selection.selected.has(rowId)}
                  onChange={() => selection.onToggle(rowId)}
                />
              </label>
            );
          },
        },
      ]
    : [];

  const menuColumn = rowMenu
    ? [
        {
          key: "__menu__",
          header: "",
          fit: true,
          align: "right" as const,
          render: (row: Value[]) => {
            const rowId = numericRowId(row[rowMenu.rowIdColumn]?.value);
            if (rowId === null) {
              return null;
            }
            const items = rowMenu.itemsFor(rowId, row);
            if (items.length === 0) {
              return null;
            }
            // Swallows the click so opening the menu never triggers the row's own click action.
            return (
              <span className={styles.menuCell} onClick={(e) => e.stopPropagation()}>
                <Menu items={items} aria-label={`Actions for row ${rowId}`} />
              </span>
            );
          },
        },
      ]
    : [];

  // Keys rows by rowid when one is available, so per-cell state (an expanded TruncatedCell) does
  // not transfer to a different row when a refetch reorders or replaces the data.
  const rowIdColumn = selection?.rowIdColumn ?? rowMenu?.rowIdColumn;
  const rowKey = (row: Value[], i: number) => {
    const rowId = rowIdColumn != null ? numericRowId(row[rowIdColumn]?.value) : null;
    return rowId === null ? `i${i}` : `r${rowId}`;
  };

  return (
    <div className={styles.surface}>
      <Table
        columns={[...selectColumn, ...valueColumns, ...menuColumn]}
        data={result.rows}
        rowKey={rowKey}
        emptyMessage={emptyMessage}
        onRowClick={
          rowClick
            ? (row) => {
                const rowId = numericRowId(row[rowClick.rowIdColumn]?.value);
                if (rowId !== null) {
                  rowClick.onOpen(rowId);
                }
              }
            : undefined
        }
      />
    </div>
  );
}
