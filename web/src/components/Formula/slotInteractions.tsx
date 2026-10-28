/**
 * Shared interaction machinery for slot-driven display renderings (checklist, cards): resolves the
 * display's slots against the view's attached formulas, runs one-tap deletes with an Undo toast
 * reverting the run's change batch, builds the per-row `⋯` menu (details, seeded edit, unslotted
 * single-row actions, delete), and owns the param/confirm dialog those paths open.
 */

import { useState, type ReactNode } from "react";
import type { Column, Value } from "../../api/databases";
import type { Formula } from "../../api/formulas";
import { Button } from "../Button";
import { Dialog } from "../Dialog";
import type { MenuItem } from "../Menu";
import { useToast } from "../Toast";
import { useRevertChangeBatch, useRunFormula } from "../../hooks/useFormulas";
import { isDestructive, promptParamsOf } from "./actions";
import { formulaTitle } from "./selectors";
import { FormulaParamForm } from "./FormulaParamForm";
import {
  buildRunParams,
  defaultFields,
  missingVisibleRequired,
  seedFieldsFromRow,
  type ParamFieldValue,
} from "./params";
import styles from "./slotInteractions.module.css";

interface SlotInteractionsArgs {
  bdbId: string;
  /** The view being rendered; its display config names the slots. */
  formula: Formula;
  /** Every formula attached to the view, for slot resolution and the row menu. */
  attached: Formula[];
  canWrite: boolean;
  hasDetail: boolean;
  onOpenDetail: (rowId: number) => void;
  columns: Column[];
  /** Set when this rendering lives inside a row-detail modal: the parent row the modal is scoped
   *  to. Slotted actions declaring :parent_rowid receive it — the add slot must, so inserts land
   *  in that row's children; the row-bound slots may. */
  parentRowId?: number | null;
}

export function useSlotInteractions({
  bdbId,
  formula,
  attached,
  canWrite,
  hasDetail,
  onOpenDetail,
  columns,
  parentRowId,
}: SlotInteractionsArgs) {
  const run = useRunFormula(bdbId);
  const revert = useRevertChangeBatch(bdbId);
  const { toast } = useToast();

  const slots = formula.display?.slots ?? {};
  const bySlot = (ref?: string) =>
    ref ? attached.find((f) => f.id === ref) : undefined;
  const addAction = canWrite ? bySlot(slots.add) : undefined;
  const toggleAction = canWrite ? bySlot(slots.toggle) : undefined;
  const editAction = canWrite ? bySlot(slots.edit) : undefined;
  const deleteAction = canWrite ? bySlot(slots.delete) : undefined;
  const reorderAction = canWrite ? bySlot(slots.reorder) : undefined;
  const moveAction = canWrite ? bySlot(slots.move) : undefined;
  // The declared row_actions list is the per-row menu's order: authored actions in place, and the
  // edit, delete, or detail formula named there placing that built-in entry. An attached action
  // named nowhere in the display renders nothing.
  const rowMenuRefs = slots.row_actions ?? [];

  // The action whose param/confirm dialog is open, with the rowid it targets (null for add) and
  // the option-select params the seed pre-filled, whose blank entry the dialog drops.
  const [dialog, setDialog] = useState<{
    action: Formula;
    rowId: number | null;
    destructive: boolean;
    seededSelects: Set<string>;
  } | null>(null);
  const [fields, setFields] = useState<Record<string, ParamFieldValue>>({});

  function rowValues(row: Value[]): Record<string, unknown> {
    return Object.fromEntries(
      columns.map((c, i) => [c.name, row[i]?.value ?? null]),
    );
  }

  /** Selection params an action run binds: the tapped row as :rowid, plus — inside a detail
   *  modal — the parent row as :parent_rowid when the action declares it. The run engine rejects
   *  undeclared params, so :parent_rowid is only sent to actions that ask for it. */
  function selectionFor(
    action: Formula,
    rowId: number | null,
  ): Record<string, unknown> {
    const selection: Record<string, unknown> =
      rowId != null ? { rowid: rowId } : {};
    if (
      parentRowId != null &&
      action.params.some((p) => p.name === "parent_rowid")
    ) {
      selection.parent_rowid = parentRowId;
    }
    return selection;
  }

  function runDelete(rowId: number) {
    run.mutate(
      {
        ref: deleteAction!.id,
        data: { params: selectionFor(deleteAction!, rowId) },
      },
      {
        onSuccess: (res) => {
          const batchId = res.batch_id;
          toast("Item deleted", "default", 6000, {
            label: "Undo",
            onClick: () => {
              if (batchId) {
                revert.mutate(batchId, {
                  onError: (err) =>
                    toast(
                      err instanceof Error ? err.message : "Undo failed",
                      "error",
                    ),
                });
              }
            },
          });
        },
      },
    );
  }

  function openDialog(
    action: Formula,
    rowId: number | null,
    seed?: Record<string, unknown>,
  ) {
    run.reset();
    // The params' declared defaults pre-fill first, so an untouched add dialog shows exactly what an
    // omitting run binds; a row seed (an edit, a row action) then wins for every field it carries,
    // leaving the defaults on the fields the row says nothing about.
    const prompt = promptParamsOf(action);
    const seeded = seed
      ? { ...defaultFields(prompt), ...seedFieldsFromRow(prompt, seed) }
      : defaultFields(prompt);
    setFields(seeded);
    // A select pre-filled with a valid option drops its blank entry: the blank could only mean
    // "unchanged" (seeded) or "the default" (defaulted) — both already expressed by the selected
    // value. Un-pre-filled selects keep it, their blank carrying real meaning (any).
    const seededSelects = new Set(
      prompt
        .filter((p) => {
          const raw = seeded[p.name]?.raw;
          return (
            raw != null && (p.options?.some((o) => o.value === raw) ?? false)
          );
        })
        .map((p) => p.name),
    );
    // An edit-slot action is treated as a correction, not a removal, so it skips the delete confirm
    // and undo warning even when it rewrites owned children and so derives verb `delete`. This keys
    // on slot placement, not the SQL — an edit could be authored to genuinely delete; the delete slot
    // and delete-verb row actions are where that intent belongs, and they keep the warning.
    const destructive = action.id !== editAction?.id && isDestructive(action);
    setDialog({ action, rowId, destructive, seededSelects });
  }

  // The selection an add run binds: inside a detail modal, the parent row as :parent_rowid (the
  // new child row doesn't exist yet). Empty for top-level renderings, whose adds are row-free.
  const addSelection: Record<string, unknown> = addAction
    ? selectionFor(addAction, null)
    : {};

  function runDialog() {
    if (!dialog || missingDialogRequired) return;
    const params = {
      ...selectionFor(dialog.action, dialog.rowId),
      ...buildRunParams(promptParamsOf(dialog.action), fields),
    };
    run.mutate(
      { ref: dialog.action.id, data: { params } },
      { onSuccess: () => setDialog(null) },
    );
  }

  function menuItemsFor(rowId: number, row: Value[]): MenuItem[] {
    // The built-in chrome every row carries, keyed by the slot ref a row_actions entry may repeat
    // to position it. A missing entry (no detail, or a viewer without the write slots) stays out
    // wherever it would have gone.
    const builtIn: [string | undefined, MenuItem | null][] = [
      [
        slots.detail,
        hasDetail
          ? { label: "Details", onSelect: () => onOpenDetail(rowId) }
          : null,
      ],
      [
        slots.edit,
        editAction
          ? {
              // The slot's authored label names what the edit touches ("Edit line…"), which matters
              // when the menu also carries edit-ish row actions; a label-less slot keeps the generic
              // verb, not the slug.
              label: `${editAction.label || "Edit"}…`,
              onSelect: () => openDialog(editAction, rowId, rowValues(row)),
            }
          : null,
      ],
      [
        slots.delete,
        deleteAction
          ? {
              label: deleteAction.label || "Delete",
              destructive: true,
              onSelect: () => runDelete(rowId),
            }
          : null,
      ],
    ];

    const items: MenuItem[] = [];
    const placed = new Set<string>();
    for (const ref of rowMenuRefs) {
      const chrome = builtIn.find(([slotRef]) => slotRef === ref);
      if (chrome) {
        placed.add(ref);
        if (chrome[1]) items.push(chrome[1]);
        continue;
      }
      const action = canWrite ? bySlot(ref) : undefined;
      if (!action) continue;
      const needsDialog =
        promptParamsOf(action).length > 0 || isDestructive(action);
      items.push({
        label: needsDialog ? `${formulaTitle(action)}…` : formulaTitle(action),
        destructive: isDestructive(action),
        onSelect: () =>
          needsDialog
            ? openDialog(action, rowId, rowValues(row))
            : run.mutate({
                ref: action.id,
                data: { params: selectionFor(action, rowId) },
              }),
      });
    }
    // Built-ins the list left unplaced trail it as their own group.
    let trailing = true;
    for (const [slotRef, item] of builtIn) {
      if (!item || (slotRef != null && placed.has(slotRef))) continue;
      items.push(
        trailing && items.length > 0
          ? { ...item, separatorBefore: true }
          : item,
      );
      trailing = false;
    }
    return items;
  }

  const errorMessage = run.error instanceof Error ? run.error.message : null;
  const dialogParams = dialog ? promptParamsOf(dialog.action) : [];
  // Submitting waits for every visible required field, exactly what the server will demand — a
  // hidden one doesn't count, since the run doesn't require it while its condition is off.
  const missingDialogRequired = missingVisibleRequired(dialogParams, fields);

  const dialogNode: ReactNode = (
    <Dialog
      open={dialog !== null}
      onClose={() => setDialog(null)}
      title={dialog ? formulaTitle(dialog.action) : ""}
    >
      {dialog && (
        <>
          {dialogParams.length > 0 && (
            <FormulaParamForm
              bdbId={bdbId}
              params={dialogParams}
              values={fields}
              seededSelects={dialog.seededSelects}
              selectionRowId={dialog.rowId}
              onChange={(name, value) =>
                setFields((prev) => ({ ...prev, [name]: value }))
              }
            />
          )}
          {dialog.destructive && (
            <p className={styles.confirmText}>
              This can’t be undone from here once confirmed.
            </p>
          )}
          {errorMessage && <div className={styles.error}>{errorMessage}</div>}
          <div className={styles.dialogActions}>
            <Button
              size="sm"
              variant="secondary"
              onClick={() => setDialog(null)}
            >
              Cancel
            </Button>
            <Button
              size="sm"
              variant={dialog.destructive ? "destructive" : "primary"}
              loading={run.isPending}
              disabled={missingDialogRequired}
              onClick={runDialog}
            >
              {dialog.rowId != null ? "Save" : "Add"}
            </Button>
          </div>
        </>
      )}
    </Dialog>
  );

  return {
    run,
    addAction,
    toggleAction,
    editAction,
    deleteAction,
    reorderAction,
    moveAction,
    addSelection,
    selectionFor,
    menuItemsFor,
    openDialog,
    dialogOpen: dialog !== null,
    dialogNode,
    errorMessage,
  };
}
