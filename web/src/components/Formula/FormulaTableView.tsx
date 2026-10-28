/**
 * Renders a table-mode view: the plain column grid plus the display config's declared
 * interactions — a quick-add form above the grid when the add action marks params inline (else a
 * `+ Add` dialog button), a trailing per-row `⋯` menu (details, seeded edit,
 * row_actions entries, delete with undo toast), and a row click opening the detail slot. The bulk
 * selection toolbar stays with the parent, which owns the selection state and passes its wiring
 * through.
 */

import type { Column, Value } from "../../api/databases";
import { hiddenColumns, type Formula } from "../../api/formulas";
import { Button } from "../Button";
import { QueryResultTable, type RowSelection } from "../QueryResultTable";
import { promptParamsOf } from "./actions";
import { quickAddParams } from "./params";
import { QuickAddForm } from "./QuickAddForm";
import { useSlotInteractions } from "./slotInteractions";
import styles from "./FormulaTableView.module.css";

interface FormulaTableViewProps {
  bdbId: string;
  /** The view being rendered; its display config names the slots. */
  formula: Formula;
  columns: Column[];
  rows: Value[][];
  rowIdColumn: number;
  /** Every formula attached to this view, for slot resolution and the row menu. */
  attached: Formula[];
  canWrite: boolean;
  hasDetail: boolean;
  onOpenDetail: (rowId: number) => void;
  /** Bulk-selection wiring from the parent's toolbar, when the display declares a bulk list. */
  selection?: RowSelection;
}

export function FormulaTableView({
  bdbId,
  formula,
  columns,
  rows,
  rowIdColumn,
  attached,
  canWrite,
  hasDetail,
  onOpenDetail,
  selection,
}: FormulaTableViewProps) {
  const slot = useSlotInteractions({
    bdbId,
    formula,
    attached,
    canWrite,
    hasDetail,
    onOpenDetail,
    columns,
  });

  const quickAdd = slot.addAction ? quickAddParams(promptParamsOf(slot.addAction)) : null;

  return (
    <div className={styles.wrap}>
      {slot.errorMessage && !slot.dialogOpen && (
        <div className={styles.error}>{slot.errorMessage}</div>
      )}

      {slot.addAction && quickAdd && (
        <QuickAddForm
          bdbId={bdbId}
          action={slot.addAction}
          params={quickAdd}
          selection={slot.addSelection}
          run={slot.run}
          onOpenDialog={(typed) => slot.openDialog(slot.addAction!, null, typed)}
        />
      )}
      {slot.addAction && !quickAdd && (
        <div className={styles.addBar}>
          <Button size="sm" variant="secondary" onClick={() => slot.openDialog(slot.addAction!, null)}>
            + Add
          </Button>
        </div>
      )}

      <QueryResultTable
        result={{ columns, rows, affected_row_count: 0 }}
        emptyMessage="No rows match."
        hideRowIdColumn
        hiddenColumnNames={hiddenColumns(formula.display)}
        selection={selection}
        rowClick={
          hasDetail && rowIdColumn >= 0 ? { rowIdColumn, onOpen: onOpenDetail } : undefined
        }
        rowMenu={rowIdColumn >= 0 ? { rowIdColumn, itemsFor: slot.menuItemsFor } : undefined}
      />

      {slot.dialogNode}
    </div>
  );
}
