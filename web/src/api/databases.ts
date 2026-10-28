/** Databases API — schema introspection and SQL execution for a bdb's database. */

import { get, post } from "./client";

export interface Database {
  id: string;
  bdb_id: string;
  created_at: string;
  updated_at: string;
}

export interface Column {
  name: string;
  decl_type: string;
}

export interface Value {
  type: "null" | "integer" | "float" | "text" | "blob";
  value: unknown;
}

/** A row touched by a DML statement, returned alongside a mutation result. */
export interface AffectedRow {
  table_name: string;
  row_id: string;
  operation: string;
  old_values?: Record<string, unknown>;
  new_values?: Record<string, unknown>;
}

export interface QueryResult {
  columns: Column[];
  rows: Value[][];
  affected_row_count: number;
  last_insert_row_id?: number;
  batch_id?: string;
  affected_rows?: AffectedRow[];
  affected_rows_truncated?: boolean;
}

export interface TableColumn {
  name: string;
  type: string;
  not_null: boolean;
  default?: string;
  primary_key: boolean;
}

export interface ForeignKey {
  from: string;
  table: string;
  to: string;
  on_update: string;
  on_delete: string;
}

export interface TableDescription {
  columns: TableColumn[];
  foreign_keys: ForeignKey[];
  /** "table", "view", or "virtual". */
  kind?: "table" | "view" | "virtual";
  /** SQLite virtual-table module name (e.g. "fts5") when kind is "virtual". */
  module?: string;
}

export interface QueryRequest {
  sql: string;
  args?: Value[];
}

function basePath(bdbId: string): string {
  return `/api/databases/v1/bdbs/${bdbId}/database`;
}

export function getDatabase(bdbId: string): Promise<Database> {
  return get<Database>(basePath(bdbId));
}

/** The calling user's account-wide storage usage across every bdb they own. */
export interface AccountStorage {
  total_storage_bytes_used: number;
  /** Aggregate cap across the user's bdbs; 0 = unlimited. */
  total_storage_bytes_limit: number;
}

export function getAccountStorage(): Promise<AccountStorage> {
  return get<AccountStorage>("/api/databases/v1/account/storage");
}

export function listTables(bdbId: string): Promise<QueryResult> {
  return get<QueryResult>(`${basePath(bdbId)}/tables`);
}

export function describeTable(bdbId: string, tableName: string): Promise<TableDescription> {
  return get<TableDescription>(`${basePath(bdbId)}/tables/${tableName}`);
}

export function executeQuery(bdbId: string, data: QueryRequest): Promise<QueryResult> {
  return post<QueryResult>(`${basePath(bdbId)}/query`, data);
}

export function executeMutation(bdbId: string, data: QueryRequest): Promise<QueryResult> {
  return post<QueryResult>(`${basePath(bdbId)}/data`, data);
}

export function executeSchema(bdbId: string, data: QueryRequest): Promise<QueryResult> {
  return post<QueryResult>(`${basePath(bdbId)}/schema`, data);
}
