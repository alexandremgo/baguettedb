/** The Data API as an app sees it: where it is called, and the instructions a coding agent needs to write an app that
 *  calls it with a key. */

import type { Formula, FormulaParam } from "../api/formulas";
import { getConfig } from "../config";

/** Name the instructions suggest for the setting holding the key's value, which never goes into them. */
const KEY_SETTING = "BAGUETTEDB_DATA_API_KEY";

/** Returns the Data API's base URL on this instance: on the API's origin, which is the page's own when it serves both. */
export function dataAPIBaseURL(): string {
  return `${getConfig().apiUrl || window.location.origin}/api/data/v1`;
}

/** Returns how the instructions describe the parameter p: its type, whether it is required, and what it accepts. */
function describeParam(p: FormulaParam): string {
  const facts: string[] = [p.multiple ? `list of ${p.type}` : p.type];
  if (p.source === "pagination") {
    facts.push("optional, pagination");
  } else {
    facts.push(p.optional ? "optional" : "required");
  }
  if (p.default !== undefined) facts.push(`default ${JSON.stringify(p.default)}`);
  if (p.defaults !== undefined) facts.push(`default ${JSON.stringify(p.defaults)}`);
  if (p.options?.length) {
    facts.push(`one of: ${p.options.map((o) => (o.label ? `${o.value} (${o.label})` : o.value)).join(", ")}`);
  }
  if (p.lookup) facts.push(`a value of ${p.lookup.table}.${p.lookup.value_column}`);
  let line = `\`${p.name}\` (${facts.join(", ")})`;
  if (p.hint) line += `: ${p.hint}`;
  if (p.fields?.length) {
    line += `. Each row is an object with: ${p.fields.map(describeParam).join("; ")}`;
  }
  return line;
}

/** Returns the instructions section describing formula f: its name and ID, kind, description and parameters. */
function describeFormula(f: Formula): string[] {
  const title = f.label ? `${f.name}: "${f.label}"` : f.name;
  const lines = [`### ${title} (${f.kind})`, "", `ID: ${f.id}`];
  if (f.description) lines.push("", f.description);
  lines.push("");
  if (f.params.length === 0) {
    lines.push("Takes no parameters.");
  } else {
    lines.push("Parameters:", ...f.params.map((p) => `- ${describeParam(p)}`));
  }
  return lines;
}

/**
 * Returns, as Markdown, what a coding agent needs to write an app using the bdb bdbId, named bdbName, with the Data
 * API key keyName: the base URL, how to send the key, the endpoints and their answers, the errors, and the formulas the
 * key runs with their parameters. The key's value is never in it: the app reads it from its configuration.
 */
export function dataAPIInstructions(bdbId: string, bdbName: string, keyName: string, formulas: Formula[]): string {
  const base = `${dataAPIBaseURL()}/bdbs/${bdbId}`;
  const lines = [
    `# BaguetteDB Data API: the "${bdbName}" bdb`,
    "",
    `This app uses a BaguetteDB bdb as its database, through the Data API, with the Data API key "${keyName}". The key ` +
      "runs the formulas listed below and nothing else: each is a named query or change with typed parameters, written " +
      "by the bdb's owner.",
    "",
    "## Calling it",
    "",
    `- Base URL: ${base}`,
    `- Every request sends the key: \`Authorization: Bearer <key>\`. Read the key from the app's configuration, such ` +
      `as an environment variable named ${KEY_SETTING}, and never commit it. Anyone who loads a front-end app can ` +
      "read its key, but the key only runs these formulas.",
    "- A browser may call it from any origin, without cookies or credentials.",
    `- \`GET ${base}/formulas\` lists the formulas the key runs, with their parameters.`,
    `- \`POST ${base}/formulas/{formula_id}/run\` runs one, with the JSON body \`{"params": {"name": value}}\`, or ` +
      "an empty body when it takes no parameters.",
    '  - A view answers `{"rows": [{"column": value}]}`, one object per row. A paged view adds ' +
      '`"pagination": {"has_more": true, "next_cursor": 42}`: run it again with the `cursor` parameter set to ' +
      "`next_cursor` for the next page, and `limit` for its size.",
    '  - An action answers `{"affected_row_count": 1, "last_insert_row_id": 7}`.',
    "- JavaScript loses precision on integers beyond 2^53.",
    "",
    "## Errors",
    "",
    'An error answers `{"error": "a message for people", "code": "a_stable_code"}`. Branch on the code:',
    "",
    "- 401 `missing_key`, `invalid_key`: no key was sent, or it is unknown or revoked.",
    "- 403 `key_not_for_bdb`, `formula_not_on_key`, `access_denied`: the key may not run this formula here.",
    "- 404 `formula_not_found`.",
    "- 400 `invalid_request`, `invalid_params`: the body, or a parameter value, is wrong; the message says which.",
    "- 413 `request_too_large`, `result_too_large`; 422 `query_failed`; 504 `timeout`.",
    "- 429 `rate_limited`, 503 `unavailable`: retry later, after the `Retry-After` header's seconds when it is sent.",
    "- 500 `internal_error`.",
    "",
    "## Formulas",
  ];
  if (formulas.length === 0) {
    lines.push("", "The key runs no formula yet: the bdb's owner adds them in BaguetteDB.");
  }
  for (const f of formulas) {
    lines.push("", ...describeFormula(f));
  }
  return lines.join("\n") + "\n";
}
