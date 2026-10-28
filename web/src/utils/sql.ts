/** Wraps a SQL identifier in double quotes, doubling any embedded double quote, so table and column names
 *  are safe to splice into query text. */
export function quoteIdent(name: string): string {
  return `"${name.replaceAll('"', '""')}"`;
}
