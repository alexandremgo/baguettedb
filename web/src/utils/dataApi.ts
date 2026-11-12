/** The Data API as an app sees it: where it is called, and the instructions a coding agent needs to write an app that
 *  calls it with a key. The contract itself is described by the Data API manual, which the instructions carry. */

import type { Formula, FormulaParam } from "../api/formulas";
import { getConfig } from "../config";

/** Name the instructions suggest for the setting holding the key's value, which never goes into them. */
const KEY_SETTING = "BAGUETTEDB_DATA_API_KEY";

/** The text form a value of each date and time type takes. */
const formats: Partial<Record<FormulaParam["type"], string>> = {
  date: "YYYY-MM-DD",
  datetime: "YYYY-MM-DD HH:MM:SS",
  time: "HH:MM:SS",
};

/** Returns the Data API's base URL on this instance: on the API's origin, which is the page's own when it serves both. */
export function dataAPIBaseURL(): string {
  return `${getConfig().apiUrl || window.location.origin}/api/data/v1`;
}

/** Returns the type of the parameter p's value, by its source when that decides it, and what the value is when its
 *  source says more than its type: the rowids a selection or a reorder takes, of the rows of view, the view the
 *  formula is attached to, when there is one; a pagination control; a date bound. */
function describeValue(p: FormulaParam, view?: Formula): { type: string; note?: string } {
  const rowsOf = view ? `rows of the \`${view.name}\` view` : "rows a view answered";
  switch (p.source) {
    case "selection-single":
      return { type: "integer", note: `the \`rowid\` of one of the ${rowsOf}` };
    case "selection-bulk":
      return { type: "array of integers", note: `the \`rowid\`s of ${rowsOf}` };
    case "reorder":
      return { type: "array of integers", note: `the \`rowid\`s of ${rowsOf}, in their new order` };
    case "pagination":
      return { type: p.type, note: "a pagination control, see the manual's Answers" };
    case "window":
      return { type: "date `YYYY-MM-DD`", note: "a bound of the date range" };
  }
  const type = formats[p.type] ? `${p.type} \`${formats[p.type]}\`` : p.type;
  return { type: p.multiple ? `array of ${type}` : type };
}

/** Returns how the instructions describe the parameter p, of a formula attached to view when it is: its type,
 *  whether it is required, what it accepts, and what its value is. */
function describeParam(p: FormulaParam, view?: Formula): string {
  const { type, note } = describeValue(p, view);
  const facts = [type, p.optional || p.source === "pagination" ? "optional" : "required"];
  if (p.default !== undefined) facts.push(`default ${JSON.stringify(p.default)}`);
  if (p.defaults !== undefined) facts.push(`default ${JSON.stringify(p.defaults)}`);
  if (p.options?.length) {
    facts.push(`one of: ${p.options.map((o) => (o.label ? `${o.value} (${o.label})` : o.value)).join(", ")}`);
  }
  if (p.lookup) facts.push(`a value of ${p.lookup.table}.${p.lookup.value_column}`);
  let line = `\`${p.name}\` (${facts.join(", ")})`;
  const notes = [note, p.hint].filter(Boolean);
  if (notes.length > 0) line += `: ${notes.join("; ")}`;
  if (p.fields?.length) {
    line += `. An array of objects, each with: ${p.fields.map((field) => describeParam(field)).join("; ")}`;
  }
  return line;
}

/** Returns the instructions section describing formula f, attached to view when it is: its name and ID, kind,
 *  description, parameters, and a view's columns. */
function describeFormula(f: Formula, view?: Formula): string[] {
  const title = f.label ? `${f.name}: "${f.label}"` : f.name;
  const lines = [`### ${title} (${f.kind})`, "", `ID: ${f.id}`];
  if (f.description) lines.push("", f.description);
  lines.push("");
  if (f.params.length === 0) {
    lines.push("Takes no parameters.");
  } else {
    lines.push("Parameters:", ...f.params.map((p) => `- ${describeParam(p, view)}`));
  }
  if (f.kind === "view" && f.result_columns?.length) {
    lines.push("", `Each row has: ${f.result_columns.map((c) => `\`${c}\``).join(", ")}.`);
  }
  return lines;
}

/**
 * Returns, as Markdown, what a coding agent needs to write an app using the bdb bdbId, named bdbName, with the Data
 * API key keyName: first the key's specifics, its base URL and the formulas it runs with their parameters and a view's
 * columns, then manual, the Data API manual. bdbFormulas are all the bdb's formulas, among which an attached formula's
 * view is found, to say which view's rows its selection takes. The key's value is never in it: the app reads it from
 * its configuration.
 */
export function dataAPIInstructions(
  bdbId: string,
  bdbName: string,
  keyName: string,
  formulas: Formula[],
  manual: string,
  bdbFormulas: Formula[],
): string {
  const byId = new Map(bdbFormulas.map((f) => [f.id, f]));
  const base = `${dataAPIBaseURL()}/bdbs/${bdbId}`;
  const lines = [
    `# The "${keyName}" Data API key of the "${bdbName}" bdb`,
    "",
    `This app uses a BaguetteDB bdb as its database, through the Data API, with the Data API key "${keyName}": it ` +
      "runs the formulas listed below and nothing else.",
    "",
    `- Base URL: ${base}`,
    `- The key goes in every request's \`Authorization: Bearer <key>\` header. Read it, and the base URL, from the ` +
      `app's configuration, such as an environment variable named ${KEY_SETTING}, and never commit it.`,
    "- An agent with BaguetteDB's MCP tools or command line can read the rest itself: `get_docs` (topic `data-api`) " +
      "and `get_formula`, or `bdb docs data-api` and `bdb formulas show`. The Data API manual follows the formulas.",
    "",
    "## The key's formulas",
  ];
  if (formulas.length === 0) {
    lines.push("", "The key runs no formula yet: the bdb's owner adds them in BaguetteDB.");
  }
  for (const f of formulas) {
    const view = f.attached_to_formula_id ? byId.get(f.attached_to_formula_id) : undefined;
    lines.push("", ...describeFormula(f, view));
  }
  lines.push("", "---", "", manual.trimEnd());
  return lines.join("\n") + "\n";
}
