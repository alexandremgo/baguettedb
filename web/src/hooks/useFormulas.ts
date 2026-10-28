import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { revertChangeBatch } from "../api/changes";
import { executeQuery } from "../api/databases";
import type { Value } from "../api/databases";
import { listFormulas, runFormula } from "../api/formulas";
import { quoteIdent } from "../utils/sql";
import type { LookupConfig, RunFormulaRequest } from "../api/formulas";

/** Fetches all formulas (views + actions) for a bdb. */
export function useFormulas(bdbId: string | undefined) {
  return useQuery({
    queryKey: ["formulas", bdbId],
    queryFn: () => listFormulas(bdbId!),
    enabled: !!bdbId,
  });
}

/** One choice in a lookup param's picker: the label is shown, the value is bound. */
export interface LookupOption {
  value: string | number;
  label: string;
  /** The picked row's fill columns, keyed by the sibling param they land in — present only when
   *  the lookup declares a fill. Values are the columns' text form; NULL becomes an empty string
   *  so a pick clears a sibling the row leaves blank. */
  fill?: Record<string, string>;
}

/** The lookup picker's option cap. Fetches over-fetch by one row so truncation is detected
 *  honestly instead of inferred from an exact-cap count. */
export const LOOKUP_OPTIONS_CAP = 200;

/** One page of picker options, with the honest signal that rows exist beyond the cap — reaching
 *  those requires narrowing the query server-side, not filtering the page. */
export interface LookupOptionsPage {
  options: LookupOption[];
  truncated: boolean;
}

/** Fetches the distinct options for a lookup param's picker (capped, ordered by label), optionally
 *  narrowed by a label substring matched server-side and by the lookup's scope — each scope column
 *  compared against its sibling param's current value in scopeValues (absent entries add no
 *  constraint). Identifiers are validated at formula save time, so quoting them here is safe; the
 *  search term and scope values travel as bound arguments. */
export function useLookupOptions(
  bdbId: string,
  lookup: LookupConfig | undefined,
  search = "",
  excludeValue?: number,
  scopeValues?: Record<string, string>,
) {
  return useQuery({
    queryKey: ["lookup-options", bdbId, lookup, search, excludeValue, scopeValues],
    queryFn: async (): Promise<LookupOptionsPage> => {
      const conditions: string[] = [];
      const args: Value[] = [];
      if (search !== "") {
        conditions.push(`${quoteIdent(lookup!.label_column)} LIKE '%' || ? || '%'`);
        args.push({ type: "text", value: search });
      }
      if (excludeValue !== undefined) {
        conditions.push(`${quoteIdent(lookup!.value_column)} <> ?`);
        args.push({ type: "integer", value: excludeValue });
      }
      for (const [sibling, column] of Object.entries(lookup!.scope ?? {})) {
        const value = scopeValues?.[sibling];
        if (value === undefined) continue;
        // Bound as text: comparing against the column applies its affinity, so a numeric column
        // still matches its numeral.
        conditions.push(`${quoteIdent(column)} = ?`);
        args.push({ type: "text", value });
      }
      const filter = conditions.length === 0 ? "" : `WHERE ${conditions.join(" AND ")} `;
      // Fill columns ride along as extra selected columns, one per sibling in declaration order.
      // Grouping by value and label (instead of DISTINCT over every column) keeps one option per
      // pair even when a non-unique value column carries differing fill values; SQLite's bare-column
      // rule then picks one row's fills for it.
      const fillEntries = Object.entries(lookup!.fill ?? {});
      const fillSelect = fillEntries.map(([, column]) => `, ${quoteIdent(column)}`).join("");
      const sql =
        fillEntries.length === 0
          ? `SELECT DISTINCT ${quoteIdent(lookup!.value_column)} AS value, ` +
            `${quoteIdent(lookup!.label_column)} AS label ` +
            `FROM ${quoteIdent(lookup!.table)} ${filter}ORDER BY label LIMIT ${LOOKUP_OPTIONS_CAP + 1}`
          : `SELECT ${quoteIdent(lookup!.value_column)} AS value, ` +
            `${quoteIdent(lookup!.label_column)} AS label${fillSelect} ` +
            `FROM ${quoteIdent(lookup!.table)} ${filter}GROUP BY value, label ` +
            `ORDER BY label LIMIT ${LOOKUP_OPTIONS_CAP + 1}`;
      const result = await executeQuery(bdbId, {
        sql,
        args: args.length === 0 ? undefined : args,
      });
      const options = result.rows
        .filter((row) => row[0]?.value !== null && row[0]?.value !== undefined)
        .map((row) => {
          const option: LookupOption = {
            value: row[0].value as string | number,
            label: String(row[1]?.value ?? row[0].value),
          };
          if (fillEntries.length > 0) {
            option.fill = Object.fromEntries(
              fillEntries.map(([sibling], i) => {
                const raw = row[2 + i]?.value;
                return [sibling, raw === null || raw === undefined ? "" : String(raw)];
              }),
            );
          }
          return option;
        });
      return {
        options: options.slice(0, LOOKUP_OPTIONS_CAP),
        truncated: options.length > LOOKUP_OPTIONS_CAP,
      };
    },
    enabled: !!lookup,
    // Keeps the previous page rendered while a narrowed fetch is in flight, so the open list
    // doesn't flash empty between keystrokes.
    placeholderData: (previous) => previous,
  });
}

/** Reverts a change batch (e.g. one action run, addressed by the batch_id its run returned) and
 *  refreshes any cached view runs — the undo behind the checklist's one-tap delete. */
export function useRevertChangeBatch(bdbId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (batchId: string) => revertChangeBatch(bdbId, batchId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["formula-run", bdbId] });
      queryClient.invalidateQueries({ queryKey: ["formula-detail", bdbId] });
      queryClient.invalidateQueries({ queryKey: ["lookup-options", bdbId] });
    },
  });
}

/** Runs a formula (typically an action) and refreshes any cached view runs for the bdb, since a
 *  mutation may change rows shown by other formulas. */
export function useRunFormula(bdbId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (vars: { ref: string; data: RunFormulaRequest }) =>
      runFormula(bdbId, vars.ref, vars.data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["formula-run", bdbId] });
      queryClient.invalidateQueries({ queryKey: ["formula-detail", bdbId] });
      // Actions may create the very rows a picker offers (creatable lookups especially), so cached
      // option pages refetch too.
      queryClient.invalidateQueries({ queryKey: ["lookup-options", bdbId] });
    },
  });
}
