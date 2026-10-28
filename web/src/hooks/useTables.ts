import { useQuery } from "@tanstack/react-query";
import { listTables, describeTable, executeQuery } from "../api/databases";
import { quoteIdent } from "../utils/sql";

/** Fetches the list of table names for a bdb's database. */
export function useTables(bdbId: string | undefined) {
  return useQuery({
    queryKey: ["tables", bdbId],
    queryFn: async () => {
      const result = await listTables(bdbId!);
      return result.rows
        .map((row) => row.find((v) => v.type === "text"))
        .filter(Boolean)
        .map((v) => String(v!.value));
    },
    enabled: !!bdbId,
  });
}

/** Fetches the schema (column info) for a specific table. */
export function useTableSchema(bdbId: string | undefined, tableName: string | null) {
  return useQuery({
    queryKey: ["table-schema", bdbId, tableName],
    queryFn: () => describeTable(bdbId!, tableName!),
    enabled: !!bdbId && !!tableName,
  });
}

/** Fetches a table's first `limit` rows, reading one extra so the caller can tell whether more
 *  exist (slice to `limit` before rendering). */
export function useTableData(bdbId: string | undefined, tableName: string | null, limit = 50) {
  return useQuery({
    queryKey: ["table-data", bdbId, tableName, limit],
    queryFn: () =>
      executeQuery(bdbId!, { sql: `SELECT * FROM ${quoteIdent(tableName!)} LIMIT ${limit + 1}` }),
    enabled: !!bdbId && !!tableName,
  });
}
